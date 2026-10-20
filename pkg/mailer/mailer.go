// Package mailer 抽象邮件发送能力。
//
// 调用方只依赖 Sender 接口，两个驱动各自可替换：
//   - log（本文件内）：把邮件内容写进日志，本地开发从日志取验证码，仅 dev 可用；
//   - smtp（`smtp.go`）：真实投递，覆盖 QQ / 163 邮箱、阿里云邮件推送等所有提供 SMTP 的厂商。
//
// 业务代码（internal/user/service）两者都不感知，切换只改配置。
package mailer

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/logger"
)

// 当前支持的邮件驱动。
const (
	DriverLog  = "log"  // 只打日志，不投递（仅 dev）
	DriverSMTP = "smtp" // 真实投递
)

// devEnv 是唯一允许使用 log 驱动的运行环境。
const devEnv = "dev"

// Message 是一封纯文本邮件。
type Message struct {
	To      string // 收件人邮箱
	Subject string // 邮件主题
	Body    string // 纯文本正文
}

// Sender 是邮件发送方抽象。
type Sender interface {
	// Send 发送一封邮件。失败时返回错误，由调用方决定是否释放验证码冷却。
	Send(ctx context.Context, msg Message) error
	// Name 返回驱动名，用于启动日志与排障。
	Name() string
}

// New 依据配置构造 Sender。
//
// 安全约束：log 驱动只在 dev 环境可用。生产环境若误配成 log，
// 验证码会明文写进日志且用户永远收不到邮件，属于必须 fail-fast 的配置错误。
//
// smtp 驱动的参数校验见 newSMTPSender，同样在启动时失败——邮件配置错了在生产
// 只会表现为「注册接口返回成功但用户收不到码」，比起不来难查得多。
func New(cfg config.MailConfig, env string) (Sender, error) {
	driver := strings.ToLower(strings.TrimSpace(cfg.Driver))
	if driver == "" {
		driver = DriverLog
	}

	switch driver {
	case DriverLog:
		if env != devEnv {
			return nil, fmt.Errorf("mail.driver=log 仅允许在 dev 环境使用，当前 env=%s；生产请配置 mail.driver=smtp", env)
		}
		return &logSender{from: cfg.From}, nil
	case DriverSMTP:
		return newSMTPSender(cfg)
	default:
		return nil, fmt.Errorf("不支持的 mail.driver: %s（可选 log / smtp）", cfg.Driver)
	}
}

// logSender 把邮件内容写进日志，不做真实投递。
//
// 本地开发时从网关日志里取验证码即可完成注册，无需配置任何邮件服务。
type logSender struct {
	from string
}

// Name 实现 Sender。
func (s *logSender) Name() string { return DriverLog }

// Send 实现 Sender：只打日志，因此永远成功。
func (s *logSender) Send(_ context.Context, msg Message) error {
	logger.L().Info("邮件发送（log 驱动，未真实投递）",
		zap.String("driver", DriverLog),
		zap.String("from", s.from),
		zap.String("to", MaskAddress(msg.To)),
		zap.String("subject", msg.Subject),
		zap.String("body", msg.Body),
	)
	return nil
}

// MaskAddress 对邮箱脱敏，形如 a***e@example.com。
//
// 日志属于长期留存数据，不应出现完整收件人；验证码正文保持明文，
// 因为开发期正是靠它取码。
func MaskAddress(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := addr[:at], addr[at:]
	switch {
	case len(local) == 1:
		return local + "***" + domain
	case len(local) == 2:
		return local[:1] + "***" + domain
	default:
		return local[:1] + "***" + local[len(local)-1:] + domain
	}
}
