package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/internal/user/repository"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/mailer"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

// 验证码用途标识，与 OpenAPI 契约的 purpose 取值一一对应。
//
// purpose 必须进 Redis key（见 repository 的 auth:code:{purpose}:{email}），
// 否则会出现「用注册码去重置别人密码」这类跨用途越权。
const (
	purposeRegister = "register"
	purposeReset    = "reset"
)

// SendCode 按用途生成验证码并投递到用户邮箱。
//
// 两种用途对「邮箱是否已注册」的处理刻意相反：
//   - register：已注册就直接报错，省掉一封注定没用的邮件（代价是该接口可用于探测邮箱是否注册过，
//     对当前产品形态可以接受）；
//   - reset：无论是否注册都返回成功，且**未注册的邮箱不发信**——否则它就成了
//     「批量探测哪些邮箱注册过」的接口。
//
// reset 分支即使不发信也照常 Issue（占用冷却与小时配额），
// 让两种邮箱的限流行为一致，避免留下「未注册的邮箱可以无限打」的侧信道。
func (s *Service) SendCode(ctx context.Context, email, ip, purpose string) (*dto.SendCodeResult, error) {
	email = utils.NormalizeEmail(email)
	if !utils.IsValidEmail(email) {
		return nil, errcode.ErrInvalidParam.WithMsg("邮箱格式不正确")
	}

	// 只要「存在与否」，不关心用户内容：这里不读资料是为了避免无谓地把整行拉出来。
	_, err := s.repo.GetByEmail(ctx, email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	registered := err == nil

	switch purpose {
	case purposeRegister:
		if registered {
			return nil, errcode.ErrEmailTaken
		}
	case purposeReset:
		// 是否注册过不影响后续行为：两种情况都要返回同样的成功响应（见函数注释）。
	default:
		return nil, errcode.ErrInvalidParam.WithMsg("验证码用途不合法")
	}

	code, err := utils.NumericCode(codeLength)
	if err != nil {
		return nil, err
	}

	if err := s.codes.Issue(ctx, purpose, email, ip, code, s.policy()); err != nil {
		return nil, err
	}

	result := &dto.SendCodeResult{
		ExpiresIn: int64(s.vcfg.CodeTTL.Seconds()),
		Cooldown:  int64(s.vcfg.CodeCooldown.Seconds()),
	}

	// reset 且邮箱未注册：到这里就返回，响应与真正发过信时完全一致。
	if purpose == purposeReset && !registered {
		return result, nil
	}

	subject, body := codeMail(purpose, code, s.vcfg.CodeTTL)
	if err := s.mail.Send(ctx, mailer.Message{To: email, Subject: subject, Body: body}); err != nil {
		// 投递失败必须释放验证码与冷却，否则用户要白白等一个冷却周期才能重试。
		_ = s.codes.Release(ctx, purpose, email)
		return nil, fmt.Errorf("发送验证码邮件失败: %w", err)
	}

	return result, nil
}

// codeMail 按用途给出邮件主题与正文。
//
// 重置密码的文案额外提醒「非本人操作」要改密：这句话是这条链路唯一的用户侧告警出口——
// 攻击者发起重置时，真正的邮箱主人只能靠这封信察觉。
func codeMail(purpose, code string, ttl time.Duration) (subject, body string) {
	minutes := int(ttl.Minutes())
	if purpose == purposeReset {
		return "窗月 · 重置密码验证码",
			fmt.Sprintf("你的重置密码验证码是 %s，%d 分钟内有效。\n\n如果这不是你本人的操作，说明有人正在尝试重置你的密码，请忽略本邮件并尽快修改密码。",
				code, minutes)
	}
	return "窗月 · 注册验证码",
		fmt.Sprintf("你的注册验证码是 %s，%d 分钟内有效。\n\n如果这不是你本人的操作，请忽略本邮件。", code, minutes)
}

// policy 把配置映射为发码/校验策略。
func (s *Service) policy() repository.CodePolicy {
	return repository.CodePolicy{
		TTL:          s.vcfg.CodeTTL,
		Cooldown:     s.vcfg.CodeCooldown,
		MaxTry:       s.vcfg.CodeMaxTry,
		EmailHourCap: s.vcfg.EmailHourlyLimit,
		IPHourCap:    s.vcfg.IPHourlyLimit,
	}
}
