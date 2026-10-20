package mailer

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sobremesatime/chuangyue/pkg/config"
)

// debugSMTP 打开后打印假服务器收发的每一行（排障用，正常测试保持静默）。
var debugSMTP = os.Getenv("MAILER_TEST_DEBUG") != ""

// ---------------------------------------------------------------------------
// 配置校验
// ---------------------------------------------------------------------------

func TestNewSMTPSender_RejectsBadConfig(t *testing.T) {
	const from = "窗月 <no-reply@chuangyue.dev>"

	tests := []struct {
		name    string
		cfg     config.MailConfig
		wantSub string // 错误信息里应出现的关键字，用来确认拒绝的原因是对的
	}{
		{
			name:    "缺少 host",
			cfg:     config.MailConfig{From: from},
			wantSub: "mail.host",
		},
		{
			name:    "缺少 from",
			cfg:     config.MailConfig{Host: "smtp.example.com"},
			wantSub: "mail.from",
		},
		{
			name:    "from 不是合法地址",
			cfg:     config.MailConfig{Host: "smtp.example.com", From: "随便写点什么"},
			wantSub: "mail.from",
		},
		{
			name:    "tls 取值拼错",
			cfg:     config.MailConfig{Host: "smtp.example.com", From: from, TLS: "tls1.3"},
			wantSub: "mail.tls",
		},
		{
			// 明文投递会把验证码暴露在网络上，只允许连本机——与「log 仅 dev 可用」同一思路。
			name:    "tls=none 指向外网服务器",
			cfg:     config.MailConfig{Host: "smtp.example.com", From: from, TLS: TLSModeNone},
			wantSub: "仅允许连接本机",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender, err := newSMTPSender(tt.cfg)
			if err == nil {
				t.Fatalf("应当拒绝该配置，实际构造成功: %+v", sender)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("错误信息里应包含 %q，实际: %v", tt.wantSub, err)
			}
		})
	}
}

func TestNewSMTPSender_InfersModeAndPort(t *testing.T) {
	const from = "窗月 <no-reply@chuangyue.dev>"

	tests := []struct {
		name     string
		cfg      config.MailConfig
		wantMode string
		wantAddr string
	}{
		{
			// 465 是隐式 TLS 的惯例端口，据此推断可以避免「配了 465 却忘写 tls」。
			name:     "465 端口推断为隐式 TLS",
			cfg:      config.MailConfig{Host: "smtp.qq.com", Port: 465, From: from},
			wantMode: TLSModeSSL,
			wantAddr: "smtp.qq.com:465",
		},
		{
			name:     "587 端口推断为 STARTTLS",
			cfg:      config.MailConfig{Host: "smtp.qq.com", Port: 587, From: from},
			wantMode: TLSModeSTARTTLS,
			wantAddr: "smtp.qq.com:587",
		},
		{
			name:     "端口留空按模式取默认",
			cfg:      config.MailConfig{Host: "smtp.qq.com", TLS: TLSModeSSL, From: from},
			wantMode: TLSModeSSL,
			wantAddr: "smtp.qq.com:465",
		},
		{
			name:     "显式 tls 覆盖端口推断",
			cfg:      config.MailConfig{Host: "smtp.qq.com", Port: 465, TLS: TLSModeSTARTTLS, From: from},
			wantMode: TLSModeSTARTTLS,
			wantAddr: "smtp.qq.com:465",
		},
		{
			name:     "本机可用 tls=none",
			cfg:      config.MailConfig{Host: "127.0.0.1", TLS: TLSModeNone, From: from},
			wantMode: TLSModeNone,
			wantAddr: "127.0.0.1:25",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender, err := newSMTPSender(tt.cfg)
			if err != nil {
				t.Fatalf("构造失败: %v", err)
			}
			s, ok := sender.(*smtpSender)
			if !ok {
				t.Fatalf("期望 *smtpSender，实际 %T", sender)
			}
			if s.mode != tt.wantMode {
				t.Errorf("mode: 期望 %s，实际 %s", tt.wantMode, s.mode)
			}
			if s.addr != tt.wantAddr {
				t.Errorf("addr: 期望 %s，实际 %s", tt.wantAddr, s.addr)
			}
		})
	}
}

func TestNewSMTPSender_WiresAuthOnlyWhenUsernameSet(t *testing.T) {
	const from = "窗月 <no-reply@chuangyue.dev>"

	// 有账号：接上认证（本地中继常不需要认证，此时不应强行带凭据）。
	withAuth, err := newSMTPSender(config.MailConfig{
		Host: "smtp.qq.com", Port: 465, From: from, Username: "no-reply@chuangyue.dev", Password: "授权码",
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if withAuth.(*smtpSender).auth == nil {
		t.Error("配置了 username 就应当接上 SMTP 认证")
	}

	withoutAuth, err := newSMTPSender(config.MailConfig{Host: "smtp.qq.com", Port: 465, From: from})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if withoutAuth.(*smtpSender).auth != nil {
		t.Error("未配置 username 时不应带认证")
	}
}

func TestNew_LogDriverStillRejectedOutsideDev(t *testing.T) {
	// 既有安全闸门不能因为新增 smtp 驱动而失效：生产用 log 会静默丢掉所有验证码。
	if _, err := New(config.MailConfig{Driver: DriverLog, From: "a@b.com"}, "prod"); err == nil {
		t.Fatal("生产环境用 log 驱动应当被拒绝")
	}
	if _, err := New(config.MailConfig{Driver: DriverLog, From: "a@b.com"}, "dev"); err != nil {
		t.Fatalf("dev 环境应允许 log 驱动: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 报文构造
// ---------------------------------------------------------------------------

func TestBuildMessage_HeadersAndEncoding(t *testing.T) {
	from := &mail.Address{Name: "窗月", Address: "no-reply@chuangyue.dev"}
	msg := Message{
		To:      "user@example.com",
		Subject: "窗月 · 注册验证码",
		Body:    "你的注册验证码是 123456，5 分钟内有效。\n\n如果这不是你本人的操作，请忽略本邮件。",
	}
	raw, err := buildMessage(from, msg, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("构造报文失败: %v", err)
	}

	// 用标准库解析回来：说明生成的确实是合法邮件，而不是「看起来像」。
	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("标准库无法解析生成的报文: %v", err)
	}

	// 主题与中文展示名必须是 RFC 2047 编码字，不能是裸 UTF-8（header 只允许 ASCII）。
	if subj := parsed.Header.Get("Subject"); !strings.HasPrefix(subj, "=?UTF-8?") {
		t.Errorf("主题应做 RFC 2047 编码，实际: %q", subj)
	}
	decodedSubject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("解码主题失败: %v", err)
	}
	if decodedSubject != msg.Subject {
		t.Errorf("主题解码后不一致: 期望 %q，实际 %q", msg.Subject, decodedSubject)
	}

	if fromHdr := parsed.Header.Get("From"); !strings.Contains(fromHdr, "=?UTF-8?") || !strings.Contains(fromHdr, "<no-reply@chuangyue.dev>") {
		t.Errorf("发件人格式不对（应含编码字与尖括号地址），实际: %q", fromHdr)
	}
	if got := parsed.Header.Get("To"); got != "user@example.com" {
		t.Errorf("收件人不一致: %q", got)
	}
	if parsed.Header.Get("Date") == "" || parsed.Header.Get("MIME-Version") != "1.0" {
		t.Errorf("缺少 Date 或 MIME-Version: %v", parsed.Header)
	}
	if ct := parsed.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") || !strings.Contains(strings.ToUpper(ct), "UTF-8") {
		t.Errorf("Content-Type 不对: %q", ct)
	}
	if cte := parsed.Header.Get("Content-Transfer-Encoding"); !strings.EqualFold(cte, "base64") {
		t.Errorf("正文编码应为 base64，实际: %q", cte)
	}

	// 正文按 base64 解码后应与原文一致（换行统一成 CRLF）。
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatalf("读取正文失败: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(body)), ""))
	if err != nil {
		t.Fatalf("正文不是合法 base64: %v", err)
	}
	if want := toCRLF(msg.Body); string(decoded) != want {
		t.Errorf("正文不一致:\n期望: %q\n实际: %q", want, string(decoded))
	}

	// 换行必须是 CRLF：只给 \n 会被部分服务器拒收。
	if strings.Contains(strings.ReplaceAll(string(raw), "\r\n", ""), "\n") {
		t.Error("报文里存在孤立的 LF，SMTP 要求 CRLF")
	}
}

func TestBuildMessage_RejectsInvalidRecipient(t *testing.T) {
	from := &mail.Address{Address: "no-reply@chuangyue.dev"}
	if _, err := buildMessage(from, Message{To: "不是邮箱", Subject: "x"}, time.Now()); err == nil {
		t.Fatal("收件人非法时应当报错")
	}
}

// ---------------------------------------------------------------------------
// 端到端：真实走一遍 SMTP 协议
// ---------------------------------------------------------------------------

func TestSMTPSender_SendOverPlainLocalhost(t *testing.T) {
	server := newFakeSMTP(t, fakeOptions{})
	host, port := server.hostPort()

	sender := newTestSender(t, config.MailConfig{
		Host: host, Port: port, From: "窗月 <no-reply@chuangyue.dev>", TLS: TLSModeNone,
	}, nil)

	msg := Message{To: "user@example.com", Subject: "窗月 · 注册验证码", Body: "验证码 654321"}
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatalf("投递失败: %v", err)
	}

	mails := server.received()
	if len(mails) != 1 {
		t.Fatalf("服务器应收到 1 封邮件，实际 %d 封", len(mails))
	}
	got := mails[0]
	if got.from != "no-reply@chuangyue.dev" {
		t.Errorf("MAIL FROM 不一致: %q", got.from)
	}
	if len(got.to) != 1 || got.to[0] != "user@example.com" {
		t.Errorf("RCPT TO 不一致: %v", got.to)
	}
	if !strings.Contains(got.body, "验证码 654321") {
		t.Errorf("收到的正文里应包含验证码，实际正文: %q", got.body)
	}
	// 投递到对方手上的报文必须带 RFC 2047 编码的主题（中文不能裸奔）。
	if !strings.Contains(got.raw, "Subject: =?UTF-8?") {
		t.Errorf("报文主题应经 RFC 2047 编码，实际: %q", got.raw)
	}
	if errs := server.errors(); len(errs) != 0 {
		t.Errorf("服务器侧出现异常: %v", errs)
	}
}

func TestSMTPSender_StartTLSWithAuth(t *testing.T) {
	cert, pool := selfSignedTLS(t, net.ParseIP("127.0.0.1"))
	server := newFakeSMTP(t, fakeOptions{
		tlsConf:           &tls.Config{Certificates: []tls.Certificate{cert}},
		advertiseSTARTTLS: true,
		requireAuth:       true,
		username:          "no-reply@chuangyue.dev",
		password:          "授权码",
	})
	host, port := server.hostPort()

	cfg := config.MailConfig{
		Host: host, Port: port, TLS: TLSModeSTARTTLS,
		From: "窗月 <no-reply@chuangyue.dev>",
		// 口令故意写错：先确认认证真的会被服务器校验（否则「认证成功」毫无意义）
		Username: "no-reply@chuangyue.dev", Password: "错的口令",
	}

	wrongPass := newTestSender(t, cfg, pool)
	if err := wrongPass.Send(context.Background(), Message{To: "user@example.com", Subject: "s", Body: "b"}); err == nil {
		t.Fatal("口令错误时应当投递失败")
	}
	if server.authenticated() {
		t.Error("口令错误不应通过认证")
	}
	if n := len(server.received()); n != 0 {
		t.Errorf("认证失败后不应投递邮件，实际收到 %d 封", n)
	}

	// 换正确口令：走完 STARTTLS + AUTH 后应成功投递。
	cfg.Password = "授权码"
	sender := newTestSender(t, cfg, pool)
	if err := sender.Send(context.Background(), Message{To: "user@example.com", Subject: "窗月 · 注册验证码", Body: "验证码 111111"}); err != nil {
		t.Fatalf("STARTTLS 投递失败: %v", err)
	}
	if !server.authenticated() {
		t.Error("应当通过 SMTP 认证")
	}
	if !server.sawSTARTTLS() {
		t.Error("连接应当经过 STARTTLS 升级")
	}
	mails := server.received()
	if len(mails) != 1 || !strings.Contains(mails[0].body, "验证码 111111") {
		t.Fatalf("应收到 1 封含验证码的邮件，实际: %+v", mails)
	}
}

func TestSMTPSender_RefusesWhenServerLacksSTARTTLS(t *testing.T) {
	// 服务器不提供 STARTTLS：必须失败，绝不能退回明文把验证码发出去。
	server := newFakeSMTP(t, fakeOptions{advertiseSTARTTLS: false})
	host, port := server.hostPort()

	sender := newTestSender(t, config.MailConfig{
		Host: host, Port: port, TLS: TLSModeSTARTTLS, From: "窗月 <no-reply@chuangyue.dev>",
	}, nil)

	err := sender.Send(context.Background(), Message{To: "user@example.com", Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("服务器无 STARTTLS 时应拒绝投递")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("错误信息应点明 STARTTLS，实际: %v", err)
	}
	if n := len(server.received()); n != 0 {
		t.Errorf("不应退回明文投递，实际收到 %d 封", n)
	}
}

func TestSMTPSender_ToleratesNonStandardQuitReply(t *testing.T) {
	// 阿里云邮件推送的 465 端点按其文档示例在 QUIT 后回的是 `DONE`，不是标准 221，
	// Go 的 Client.Quit 会报 invalid response code。
	// 邮件是否被接收在 DATA 结束时就已经确定，因此这种收尾异常**不应**被当成投递失败——
	// 否则会出现「邮件其实已投递、接口却报失败」，调用方还会连带释放验证码与冷却。
	server := newFakeSMTP(t, fakeOptions{quitReply: "DONE"})
	host, port := server.hostPort()

	sender := newTestSender(t, config.MailConfig{
		Host: host, Port: port, TLS: TLSModeNone, From: "窗月 <no-reply@chuangyue.dev>",
	}, nil)

	if err := sender.Send(context.Background(), Message{To: "user@example.com", Subject: "s", Body: "验证码 999888"}); err != nil {
		t.Fatalf("QUIT 应答非标准时不应判为投递失败: %v", err)
	}
	mails := server.received()
	if len(mails) != 1 || !strings.Contains(mails[0].body, "验证码 999888") {
		t.Fatalf("服务器应已接收该邮件，实际: %+v", mails)
	}
}

func TestSMTPSender_RespectsCancelledContext(t *testing.T) {
	// 用 silent 服务器确保「不是服务器先断开」，而是 ctx 生效。
	server := newFakeSMTP(t, fakeOptions{silent: true})
	host, port := server.hostPort()

	sender := newTestSender(t, config.MailConfig{
		Host: host, Port: port, TLS: TLSModeNone, From: "窗月 <no-reply@chuangyue.dev>",
		Timeout: 5 * time.Second,
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消：连拨号都不该完成

	start := time.Now()
	err := sender.Send(ctx, Message{To: "user@example.com", Subject: "s", Body: "b"})
	if err == nil {
		t.Fatal("ctx 已取消时应报错")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("ctx 取消应立即生效，实际耗时 %v", elapsed)
	}
}

func TestSMTPSender_TimesOutOnSilentServer(t *testing.T) {
	// 服务器连上就不说话：Send 必须有上界，否则发验证码的请求会被一直挂住。
	server := newFakeSMTP(t, fakeOptions{silent: true})
	host, port := server.hostPort()

	const timeout = 300 * time.Millisecond
	sender := newTestSender(t, config.MailConfig{
		Host: host, Port: port, TLS: TLSModeNone, From: "窗月 <no-reply@chuangyue.dev>",
		Timeout: timeout,
	}, nil)

	start := time.Now()
	err := sender.Send(context.Background(), Message{To: "user@example.com", Subject: "s", Body: "b"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("服务器无响应时应超时报错")
	}
	if elapsed < timeout {
		t.Errorf("不应早于超时时间返回: %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("应在超时后很快返回，实际耗时 %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// 测试替身：一个够用的假 SMTP 服务器
// ---------------------------------------------------------------------------

type capturedMail struct {
	from string
	to   []string
	raw  string // 完整报文（含 header）
	body string // 按 Content-Transfer-Encoding 解码后的正文
}

type fakeOptions struct {
	tlsConf           *tls.Config
	advertiseSTARTTLS bool
	requireAuth       bool
	username          string
	password          string
	silent            bool
	// quitReply 覆盖 QUIT 的应答，用于模拟不返回标准 221 的服务商
	// （如阿里云邮件推送的 465 端点）。留空即回标准的 `221 Bye`。
	quitReply string
}

// fakeSMTP 是够本次测试用的 SMTP 服务器：只实现被用到的命令，
// 不做真实投递。相比 mock 接口，它能真实暴露「协议顺序 / TLS 升级 / 认证
// 往返 / dot-stuffing」这些问题——这些恰恰是最容易写错、也最该被验证的部分。
type fakeSMTP struct {
	ln   net.Listener
	opts fakeOptions

	mu        sync.Mutex
	mails     []capturedMail
	conns     []net.Conn
	authOK    bool
	startTLS  bool
	serveErrs []string
}

func newFakeSMTP(t *testing.T, opts fakeOptions) *fakeSMTP {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	f := &fakeSMTP{ln: ln, opts: opts}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // 监听已关闭，正常退出
			}
			f.track(conn)
			go f.serve(conn)
		}
	}()

	t.Cleanup(f.close)
	return f
}

func (f *fakeSMTP) hostPort() (string, int) {
	addr := f.ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) received() []capturedMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]capturedMail(nil), f.mails...)
}

func (f *fakeSMTP) authenticated() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authOK
}

func (f *fakeSMTP) sawSTARTTLS() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startTLS
}

// errors 返回服务器侧记录的异常（如 TLS 握手失败）。
// 断言它为空，能把「客户端报了个笼统错误」与「服务器那边其实先炸了」区分开。
func (f *fakeSMTP) errors() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.serveErrs...)
}

func (f *fakeSMTP) track(conn net.Conn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.conns = append(f.conns, conn)
}

func (f *fakeSMTP) close() {
	_ = f.ln.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	if f.opts.silent {
		// 连得上但一声不吭：用来验证客户端的超时兜底。
		_, _ = io.Copy(io.Discard, conn)
		return
	}

	tp := textproto.NewReader(bufio.NewReader(conn))
	writeLine(conn, "220 fake ESMTP")

	var cur capturedMail
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		if debugSMTP {
			fmt.Printf("S-> %q\n", line)
		}
		verb, rest := splitCommand(line)

		switch verb {
		case "EHLO":
			writeLine(conn, f.ehloResponse())
		case "HELO":
			writeLine(conn, "250 fake")
		case "STARTTLS":
			if f.opts.tlsConf == nil {
				writeLine(conn, "502 STARTTLS 未启用")
				continue
			}
			writeLine(conn, "220 Ready to start TLS")
			tlsConn := tls.Server(conn, f.opts.tlsConf)
			if err := tlsConn.Handshake(); err != nil {
				f.recordErr("TLS 握手失败: " + err.Error())
				return
			}
			f.mu.Lock()
			f.startTLS = true
			f.mu.Unlock()
			conn = tlsConn
			tp = textproto.NewReader(bufio.NewReader(conn))
		case "AUTH":
			f.handleAuth(conn, rest)
		case "MAIL":
			if f.opts.requireAuth && !f.authenticated() {
				writeLine(conn, "530 需要先认证")
				continue
			}
			cur = capturedMail{from: extractAddr(rest)}
			writeLine(conn, "250 OK")
		case "RCPT":
			cur.to = append(cur.to, extractAddr(rest))
			writeLine(conn, "250 OK")
		case "DATA":
			writeLine(conn, "354 End data with <CR><LF>.<CR><LF>")
			raw, err := tp.ReadDotBytes()
			if err != nil {
				f.recordErr("读取正文失败: " + err.Error())
				return
			}
			cur.raw = string(raw)
			cur.body = decodeBody(cur.raw)
			f.mu.Lock()
			f.mails = append(f.mails, cur)
			f.mu.Unlock()
			cur = capturedMail{}
			writeLine(conn, "250 OK: queued")
		case "QUIT":
			reply := f.opts.quitReply
			if reply == "" {
				reply = "221 Bye"
			}
			writeLine(conn, reply)
			return
		default:
			// RSET / NOOP 等无关命令一律接受，避免测试被无关细节绊住。
			writeLine(conn, "250 OK")
		}
	}
}

func (f *fakeSMTP) ehloResponse() string {
	// 多行应答：除最后一行外都要用 "250-" 续行。
	// 注意这里**不带**结尾 CRLF——由 writeLine 统一补，多补一个就会多出一条空行，
	// 客户端会把那条空行当成下一条命令的应答（表现为 "short response"）。
	lines := []string{"250-fake greets you"}
	if f.opts.advertiseSTARTTLS {
		lines = append(lines, "250-STARTTLS")
	}
	lines = append(lines, "250 AUTH PLAIN")
	return strings.Join(lines, "\r\n")
}

func (f *fakeSMTP) handleAuth(w io.Writer, rest string) {
	fields := strings.Fields(rest)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "PLAIN") {
		writeLine(w, "504 只支持 AUTH PLAIN")
		return
	}

	raw, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		writeLine(w, "535 凭据解码失败")
		return
	}
	// AUTH PLAIN 的载荷是 \0user\0pass
	parts := strings.Split(string(raw), "\x00")
	if len(parts) < 3 || parts[len(parts)-2] != f.opts.username || parts[len(parts)-1] != f.opts.password {
		writeLine(w, "535 认证失败")
		return
	}

	f.mu.Lock()
	f.authOK = true
	f.mu.Unlock()
	writeLine(w, "235 认证成功")
}

func (f *fakeSMTP) recordErr(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serveErrs = append(f.serveErrs, msg)
}

// newTestSender 构造 sender，并可注入信任自签 CA 的 TLS 配置。
func newTestSender(t *testing.T, cfg config.MailConfig, rootCAs *x509.CertPool) *smtpSender {
	t.Helper()

	built, err := newSMTPSender(cfg)
	if err != nil {
		t.Fatalf("构造 smtp sender 失败: %v", err)
	}
	sender, ok := built.(*smtpSender)
	if !ok {
		t.Fatalf("期望 *smtpSender，实际 %T", built)
	}
	// 测试服务器用自签证书，这里注入信任链；生产路径始终走系统根证书。
	if rootCAs != nil {
		sender.tlsConf = &tls.Config{RootCAs: rootCAs, ServerName: sender.host, MinVersion: tls.VersionTLS12}
	}
	return sender
}

// selfSignedTLS 现场生成一张自签证书：省去仓库里放固定证书文件，
// 也避免证书过期导致测试在某天突然变红。
func selfSignedTLS(t *testing.T, ip net.IP) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "chuangyue-mailer-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{ip},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("解析证书失败: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func writeLine(w io.Writer, s string) {
	if debugSMTP {
		fmt.Printf("S<- %q\n", s)
	}
	_, _ = io.WriteString(w, s+"\r\n")
}

// splitCommand 拆出命令动词（大写）与其余参数。
func splitCommand(line string) (string, string) {
	line = strings.TrimSpace(line)
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return strings.ToUpper(line), ""
}

// extractAddr 从 `FROM:<a@b.com>` 这类参数里取出裸地址。
func extractAddr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	return strings.Trim(s, "<>")
}

// decodeBody 从完整报文里取出正文并按其 Content-Transfer-Encoding 解码，得到可读文本。
//
// 刻意用 net/mail 解析而不是手工切分：这样断言的是「真实邮件客户端能读出的内容」，
// 而不是「我们碰巧切对了字符串」。
func decodeBody(raw string) string {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return raw
	}
	body, err := io.ReadAll(msg.Body)
	if err != nil {
		return raw
	}
	if strings.EqualFold(msg.Header.Get("Content-Transfer-Encoding"), "base64") {
		if decoded, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(string(body)), "")); err == nil {
			return string(decoded)
		}
	}
	return string(body)
}
