package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// SMTP 的三种 TLS 模式。
//
// 为什么需要三选一：各家邮件服务商的安全端口不一致——QQ / 163 与阿里云邮件推送推荐
// 465（隐式 TLS），也有厂商给 587（STARTTLS）。二者不是「更安全 / 更不安全」的区别，
// 而是握手时机不同：465 是连上就握手，587 是先明文打招呼、再用 STARTTLS 升级。
// none 只为本地调试（如 MailHog）保留，有额外的本机限制。
const (
	TLSModeSSL      = "ssl"      // 隐式 TLS：连接建立后立即握手（惯例端口 465）
	TLSModeSTARTTLS = "starttls" // 显式 TLS：EHLO 后升级（惯例端口 587 / 25）
	TLSModeNone     = "none"     // 不加密，仅允许本机
)

// 两种 TLS 模式的惯例端口，用于 mail.port 留空时兜底。
const (
	defaultPortSSL      = 465
	defaultPortSTARTTLS = 587
)

// defaultTimeout 是单次投递的总上限（连接 + 握手 + 认证 + 发送）。
//
// 为什么必须有：Send 是同步调用，直接挂在「发送注册验证码」这个 RPC 上。
// SMTP 服务器无响应时若不设上限，请求会一直挂到上游 context 超时为止，
// 用户看到的是「发码转圈」而不是明确的失败。
const defaultTimeout = 10 * time.Second

// base64LineLen 是 base64 正文的换行宽度（RFC 2045 建议不超过 76 字符）。
const base64LineLen = 76

// smtpSender 通过 SMTP 真实投递邮件。
type smtpSender struct {
	addr string // host:port
	host string // 仅主机名：TLS 校验与 AUTH 都需要它
	from *mail.Address
	auth smtp.Auth // nil 表示服务器不要求认证（如本地中继）

	mode    string // TLSModeSSL / TLSModeSTARTTLS / TLSModeNone
	timeout time.Duration

	// tlsConf 仅供测试注入（信任测试用自签 CA）。生产路径为 nil，
	// 走系统根证书 + 主机名校验，**不做 InsecureSkipVerify**。
	tlsConf *tls.Config
}

// Name 实现 Sender。
func (s *smtpSender) Name() string { return DriverSMTP }

// Send 实现 Sender：一次完整的 SMTP 会话。
func (s *smtpSender) Send(ctx context.Context, msg Message) error {
	if strings.TrimSpace(msg.To) == "" {
		return errors.New("收件人不能为空")
	}

	// 整场会话共用一个 deadline：既尊重上游 context（gRPC 会给 deadline，取更早的那个），
	// 也保证「服务器握手后装死」这种情况同样有上界——只设连接超时是拦不住它的。
	deadline := time.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	conn, err := (&net.Dialer{Deadline: deadline}).DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器 %s 失败: %w", s.addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(deadline)

	client, err := s.newClient(ctx, conn)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if err := s.upgrade(client); err != nil {
		return err
	}
	if s.auth != nil {
		if err := client.Auth(s.auth); err != nil {
			return fmt.Errorf("SMTP 认证失败（password 应填服务商给的 API Key / 授权码 / 客户端专用密码，而不是登录密码）: %w", err)
		}
	}
	if err := client.Mail(s.from.Address); err != nil {
		return fmt.Errorf("声明发件人失败: %w", err)
	}
	if err := client.Rcpt(msg.To); err != nil {
		return fmt.Errorf("声明收件人失败: %w", err)
	}

	raw, err := buildMessage(s.from, msg, time.Now())
	if err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("开始投递邮件正文失败: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		_ = w.Close() // 关闭而不检查错误：写入已失败，这里的错误没有额外信息
		return fmt.Errorf("写邮件正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("提交邮件正文失败: %w", err)
	}

	// QUIT 的应答**不作为投递成败的依据**：消息是否被接收，在 w.Close() 处就已经由
	// 服务器对 DATA 结束行（单独一个 "."）的应答确定了。部分服务商并不回标准 221——
	// 阿里云邮件推送的 465 端点按其文档示例回的是 `DONE`，Go 的 Client.Quit 会因此报
	// 「invalid response code」。若把它当失败，就会出现「邮件其实已投递、接口却报失败」，
	// 而且调用方会连带释放验证码与冷却——用户收到的码随即失效，还会被允许重复发码。
	if err := client.Quit(); err != nil {
		logger.L().Debug("SMTP 会话收尾应答异常，邮件此前已投递，不影响结果", zap.Error(err))
	}

	// 只记脱敏后的收件人与主题，**绝不记正文**——正文里有验证码。
	logger.L().Info("邮件已投递",
		zap.String("driver", DriverSMTP),
		zap.String("to", MaskAddress(msg.To)),
		zap.String("subject", msg.Subject),
	)
	return nil
}

// newClient 建立 SMTP 会话；隐式 TLS 时先把底层连接包成 TLS。
func (s *smtpSender) newClient(ctx context.Context, conn net.Conn) (*smtp.Client, error) {
	var underlying net.Conn = conn
	if s.mode == TLSModeSSL {
		tlsConn := tls.Client(conn, s.tlsConfig())
		// 显式握手以便拿到清晰的错误；否则延迟到读取欢迎语时才失败，错误更难定位。
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("TLS 握手失败（隐式 TLS 请确认端口是否 465）: %w", err)
		}
		underlying = tlsConn
	}

	client, err := smtp.NewClient(underlying, s.host)
	if err != nil {
		return nil, fmt.Errorf("建立 SMTP 会话失败: %w", err)
	}
	return client, nil
}

// upgrade 在 STARTTLS 模式下把明文连接升级为加密连接。
//
// 服务器不支持 STARTTLS 时**直接报错而不是退回明文继续**：宁可发不出去，
// 也不能把带着验证码的邮件丢进明文通道。这是本包最该守住的一条线。
//
// 这里不接受 ctx：net/smtp 的 StartTLS 不暴露 ctx 入口，超时统一由底层连接的
// deadline 兜底（Send 开头已按 ctx 与 timeout 取更早者设好）。
func (s *smtpSender) upgrade(client *smtp.Client) error {
	if s.mode != TLSModeSTARTTLS {
		return nil
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		return errors.New("SMTP 服务器未提供 STARTTLS，拒绝在明文通道投递（可改用 mail.tls=ssl + 465 端口）")
	}
	if err := client.StartTLS(s.tlsConfig()); err != nil {
		return fmt.Errorf("STARTTLS 升级失败: %w", err)
	}
	return nil
}

// tlsConfig 返回 TLS 配置。生产路径只信任系统根证书并校验主机名。
func (s *smtpSender) tlsConfig() *tls.Config {
	if s.tlsConf != nil {
		return s.tlsConf
	}
	return &tls.Config{
		ServerName: s.host,
		MinVersion: tls.VersionTLS12,
	}
}

// newSMTPSender 依据配置构造 SMTP 发送器，配置不当直接报错（fail-fast）。
//
// 校验放在启动时而不是「第一次发信时」：邮件配置错了在生产环境只会表现为
// 「用户收不到验证码但注册接口返回成功」，这种问题极难排查，不如起不来。
func newSMTPSender(cfg config.MailConfig) (Sender, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return nil, errors.New("mail.driver=smtp 需要配置 mail.host")
	}

	from, err := parseFrom(cfg.From)
	if err != nil {
		return nil, err
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.TLS))
	if mode == "" {
		// 按端口推断，省掉「配了 465 却忘了写 tls」这类低级失败。
		if cfg.Port == defaultPortSSL {
			mode = TLSModeSSL
		} else {
			mode = TLSModeSTARTTLS
		}
	}

	port := cfg.Port
	switch mode {
	case TLSModeSSL, TLSModeSTARTTLS:
		if port <= 0 {
			port = defaultPortSSL
			if mode == TLSModeSTARTTLS {
				port = defaultPortSTARTTLS
			}
		}
	case TLSModeNone:
		// 与 log 驱动「仅 dev 可用」同一思路的安全闸门：明文通道下验证码可被中间人截获，
		// 因此只放行本机（本地调试用的 MailHog / mailpit 就在本机）。
		if !isLocalHost(host) {
			return nil, fmt.Errorf("mail.tls=none 会明文投递验证码，仅允许连接本机 SMTP（当前 mail.host=%s）；外网请用 ssl 或 starttls", host)
		}
		if port <= 0 {
			port = 25
		}
	default:
		return nil, fmt.Errorf("不支持的 mail.tls: %s（可选 ssl / starttls / none）", cfg.TLS)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	s := &smtpSender{
		host:    host,
		addr:    net.JoinHostPort(host, strconv.Itoa(port)),
		from:    from,
		mode:    mode,
		timeout: timeout,
	}

	// 留空账号即视为服务器不要求认证（本机中继常见）。
	// 有账号时用 PlainAuth：它自身还有一道保护——非 TLS 且非本机时拒绝发送凭据。
	if username := strings.TrimSpace(cfg.Username); username != "" {
		s.auth = smtp.PlainAuth("", username, cfg.Password, host)
	}
	return s, nil
}

// parseFrom 解析配置里的发件人，接受 `展示名 <addr@x.com>` 与裸地址两种写法。
func parseFrom(raw string) (*mail.Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("mail.from 未配置，无法确定发件人")
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return nil, fmt.Errorf("mail.from 格式不合法（应形如 `窗月 <no-reply@x.com>`）: %w", err)
	}
	return addr, nil
}

// isLocalHost 判断是否本机地址。
func isLocalHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(host), "127.")
	}
}

// buildMessage 组装一封符合 RFC 5322 / MIME 的纯文本邮件。
//
// 三处编码都不是可省的美化，缺一处就可能被退信或显示乱码：
//   - 主题与中文展示名必须走 RFC 2047 编码字（=?UTF-8?B?...?=），不能直接放 UTF-8 原文
//     （header 按 RFC 5322 只能是 ASCII）；
//   - 正文用 base64 而不是 8bit 原样发送，省去「单行不得超 998 字节」「中文不能被中继
//     当控制字符处理」这些坑，任何 SMTP 服务器都能安全转发；
//   - 换行必须是 CRLF，只给 \n 会被部分服务器拒收。
func buildMessage(from *mail.Address, msg Message, now time.Time) ([]byte, error) {
	to, err := mail.ParseAddress(strings.TrimSpace(msg.To))
	if err != nil {
		return nil, fmt.Errorf("收件人地址不合法: %w", err)
	}

	var b bytes.Buffer
	writeHeader := func(key, value string) {
		fmt.Fprintf(&b, "%s: %s\r\n", key, value)
	}
	// Date 与 Message-ID 缺失会被反垃圾策略扣分，Date 顺手带上。
	writeHeader("Date", now.Format(time.RFC1123Z))
	writeHeader("From", formatAddress(from))
	writeHeader("To", to.Address)
	writeHeader("Subject", mime.BEncoding.Encode("UTF-8", msg.Subject))
	writeHeader("MIME-Version", "1.0")
	writeHeader("Content-Type", `text/plain; charset="UTF-8"`)
	writeHeader("Content-Transfer-Encoding", "base64")
	// header 与正文之间必须有且只有一个空行
	b.WriteString("\r\n")

	encoded := base64.StdEncoding.EncodeToString([]byte(toCRLF(msg.Body)))
	for len(encoded) > base64LineLen {
		b.WriteString(encoded[:base64LineLen])
		b.WriteString("\r\n")
		encoded = encoded[base64LineLen:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")

	return b.Bytes(), nil
}

// formatAddress 输出 `展示名 <addr@x.com>`；展示名含中文时做 RFC 2047 编码。
//
// 不直接用 mail.Address.String()：它对非 ASCII 展示名的处理随 Go 版本有过变化，
// 这里自己编码，行为显式可控，也便于测试固定断言。
func formatAddress(a *mail.Address) string {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return a.Address
	}
	return encodeDisplayName(name) + " <" + a.Address + ">"
}

// encodeDisplayName 把展示名编码成可用于 header 的形式。
func encodeDisplayName(name string) string {
	// 纯 ASCII 且不含 header 特殊字符时按原文输出，最易读。
	if isPrintableASCII(name) && !strings.ContainsAny(name, `()<>@,;:\".[]`) {
		return name
	}
	// 含中文等情况统一走编码字：这是邮件客户端普遍识别、且不必操心引号转义的形式。
	return mime.BEncoding.Encode("UTF-8", name)
}

// isPrintableASCII 判断字符串是否只含可打印 ASCII。
func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

// toCRLF 把正文里的换行统一成 CRLF。
//
// 邮件正文的换行是协议要求（CRLF），但配置与代码里惯用 \n，
// 这里统一收口，避免调用方各自处理。
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
