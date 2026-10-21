// Package service 承载用户域的业务逻辑：注册、登录、刷新令牌、查询资料。
package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/sobremesatime/chuangyue/internal/user/dto"
	"github.com/sobremesatime/chuangyue/internal/user/model"
	"github.com/sobremesatime/chuangyue/internal/user/repository"
	"github.com/sobremesatime/chuangyue/pkg/config"
	"github.com/sobremesatime/chuangyue/pkg/errcode"
	"github.com/sobremesatime/chuangyue/pkg/jwt"
	"github.com/sobremesatime/chuangyue/pkg/logger"
	"github.com/sobremesatime/chuangyue/pkg/mailer"
	"github.com/sobremesatime/chuangyue/pkg/snowflake"
	"github.com/sobremesatime/chuangyue/pkg/storage"
	"github.com/sobremesatime/chuangyue/pkg/utils"
)

const (
	// codeLength 是邮箱验证码位数，与前端输入框及契约 pattern 保持一致。
	codeLength = 6
	// maxUsernameAttempts 是用户名冲突时的最大尝试次数（首次 + 换后缀重试）。
	maxUsernameAttempts = 4
)

// Service 组合仓储、验证码存储、邮件发送器与令牌管理器，对外提供用户域能力。
//
// 内嵌 *QueryService 使同一个 Service 同时覆盖写路径（注册 / 登录 / 资料）与
// 跨服务读路径（GetByIDs），与视频域 Service 内嵌 QueryService 的写法一致。
type Service struct {
	*QueryService
	repo    *repository.Repository
	codes   *repository.CodeStore
	refresh *repository.RefreshStore
	mail    mailer.Sender
	jwt     *jwt.Manager
	vcfg    config.VerifyConfig
	// store 是头像的落点：local 驱动写本地盘、minio 驱动进桶。
	// 由 main 注入而不是在这里按 ucfg 自行构造——构造失败要能让进程拒绝启动。
	store storage.Storage
}

// New 构造 Service。
//
// 依赖全部由 main / router 层注入，便于测试时替换实现。
func New(
	repo *repository.Repository,
	codes *repository.CodeStore,
	refresh *repository.RefreshStore,
	mail mailer.Sender,
	jwtMgr *jwt.Manager,
	vcfg config.VerifyConfig,
	store storage.Storage,
) *Service {
	return &Service{
		QueryService: NewQueryService(repo),
		repo:         repo,
		codes:        codes,
		refresh:      refresh,
		mail:         mail,
		jwt:          jwtMgr,
		vcfg:         vcfg,
		store:        store,
	}
}

// RefreshExpire 返回刷新令牌有效期，供 handler 设置 Cookie 的 Max-Age。
func (s *Service) RefreshExpire() time.Duration {
	return s.jwt.RefreshExpire()
}

// Register 注册新用户。
//
// 流程：邮箱归一化 -> 校验验证码（原子，失败即止）-> 邮箱查重 ->
// bcrypt 哈希密码 -> 由邮箱派生用户名 -> 落库（重名则换随机后缀重试）。
func (s *Service) Register(ctx context.Context, req dto.RegisterRequest) (*dto.UserInfo, error) {
	email := utils.NormalizeEmail(req.Email)
	if !utils.IsValidEmail(email) {
		return nil, errcode.ErrInvalidParam.WithMsg("邮箱格式不正确")
	}

	// 先消费验证码再做其他事：验证码是一次性凭证，校验通过即销毁。
	if err := s.codes.Verify(ctx, purposeRegister, email, req.Code, s.policy()); err != nil {
		return nil, err
	}

	exists, err := s.repo.ExistsByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errcode.ErrEmailTaken
	}

	hash, err := utils.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	base := utils.DeriveUsername(email)
	nickname := req.Nickname
	if nickname == "" {
		nickname = base
	}

	// 同名邮箱前缀很常见（如 alice@a.com 与 alice@b.com 都派生出 alice），
	// 冲突时换一个随机后缀重试；真正的唯一性由 uk_username 唯一键兜底。
	for attempt := 0; attempt < maxUsernameAttempts; attempt++ {
		username := base
		if attempt > 0 {
			username = utils.SuffixUsername(base)
		}

		u := &model.User{
			ID:           snowflake.NextID(),
			Username:     username,
			Email:        email,
			PasswordHash: hash,
			Nickname:     nickname,
			Role:         model.RoleUser,
			Status:       model.StatusNormal,
		}
		if err := s.repo.Create(ctx, u); err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				continue
			}
			return nil, err
		}
		return toUserInfo(u), nil
	}

	// 连续多次都撞用户名属于极端情况，让客户端重试。
	return nil, errcode.ErrServerBusy.WithMsg("用户名生成冲突，请重试")
}

// Login 校验账号密码并签发令牌。
//
// 返回值依次为：用户信息、访问令牌对、刷新令牌、错误。
// 刷新令牌交由 handler 写入 HttpOnly Cookie。
func (s *Service) Login(ctx context.Context, req dto.LoginRequest) (*dto.UserInfo, *dto.TokenPair, string, error) {
	account := strings.TrimSpace(req.Account)
	if account == "" {
		return nil, nil, "", errcode.ErrInvalidLogin
	}

	// 含 @ 视为邮箱，否则视为用户名：一个输入框同时支持两种身份，
	// 派生用户名的新用户与存量老用户都能登录。
	var u *model.User
	var err error
	if strings.Contains(account, "@") {
		u, err = s.repo.GetByEmail(ctx, utils.NormalizeEmail(account))
	} else {
		u, err = s.repo.GetByUsername(ctx, account)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 用户不存在与密码错误返回同一提示，避免暴露账号是否存在。
		return nil, nil, "", errcode.ErrInvalidLogin
	}
	if err != nil {
		return nil, nil, "", err
	}

	if !utils.CheckPassword(u.PasswordHash, req.Password) {
		return nil, nil, "", errcode.ErrInvalidLogin
	}
	if !u.IsNormal() {
		return nil, nil, "", errcode.ErrUserDisabled
	}

	pair, refreshToken, err := s.issueTokens(u)
	if err != nil {
		return nil, nil, "", err
	}
	return toUserInfo(u), pair, refreshToken, nil
}

// Refresh 用刷新令牌换取新的访问令牌，并轮换刷新令牌。
//
// 每次刷新都签发新的刷新令牌（Refresh Token Rotation），
// 降低刷新令牌长期泄露带来的风险；配合 guardReplay，轮换掉的旧令牌再用一次
// 就会被判定为泄露并吊销该用户全部会话。
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*dto.TokenPair, string, error) {
	claims, err := s.jwt.ParseRefreshToken(refreshToken)
	if err != nil {
		return nil, "", errcode.ErrTokenInvalid
	}

	// 重放检测排在最前：命中时既不查库也不签发，直接吊销并拒绝。
	if err := s.guardReplay(ctx, claims); err != nil {
		return nil, "", err
	}

	u, err := s.repo.GetByID(ctx, claims.UserID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", errcode.ErrUserNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if !u.IsNormal() {
		return nil, "", errcode.ErrUserDisabled
	}
	if claims.TokenVersion != u.TokenVersion {
		// 用户在令牌签发之后重置过密码（或触发过重放吊销）：这枚刷新令牌已被吊销。
		// 这是无状态 JWT 下唯一成本可接受的吊销点——刷新本来就要读用户行，
		// 比对版本号不产生额外查询；访问令牌则只能等它自然过期（最长 15 分钟）。
		return nil, "", errcode.ErrTokenInvalid
	}

	return s.issueTokens(u)
}

// guardReplay 检测刷新令牌重放，命中即吊销该用户全部会话。
//
// 判据是 jti：每次刷新都把当前这枚令牌的 jti 写进 Redis，同一枚令牌被用第二次
// 就说明它落到了别人手里（轮换后合法客户端手里已经是新的那枚了）。
// 此时**不能只是拒绝这一次**——攻击者很可能已经拿这枚令牌换到了新令牌，
// 所以必须推进 token_version，把这名用户所有会话连同攻击者手里的那份一起作废。
//
// 两个刻意的边界：
//   - **没有 jti 的存量令牌直接放行**。引入 jti 之前签发的令牌 jti 为空，
//     若把它们都算成同一个键，第二次刷新就会误判成重放，导致全站用户被踢下线。
//     放行的代价只是这些令牌在自然过期前不受重放保护（它们刷新一次就会拿到带 jti 的新令牌）。
//   - **Redis 故障时 fail-open**。用户服务启动即硬依赖 Redis，运行中它多半只是抖动；
//     把「保护组件故障」升级成「所有人都无法续期」，代价远大于少拦一次重放。
func (s *Service) guardReplay(ctx context.Context, claims *jwt.Claims) error {
	if claims.ID == "" {
		return nil
	}

	ttl := time.Until(claims.ExpiresAt.Time)
	if ttl <= 0 {
		// 过期令牌已被 ParseRefreshToken 挡下，这里只是给 SetNX 一个合法的过期时间
		// （EX 必须大于 0，否则 Redis 直接报错）。
		ttl = s.jwt.RefreshExpire()
	}

	used, err := s.refresh.MarkUsed(ctx, claims.ID, ttl)
	if err != nil {
		logger.L().Warn("刷新令牌重放检测不可用，本次放行",
			zap.String("jti", claims.ID), zap.Error(err))
		return nil
	}
	if !used {
		return nil
	}

	logger.L().Error("检测到刷新令牌重放，已吊销该用户全部会话",
		zap.Int64("user_id", claims.UserID), zap.String("jti", claims.ID))
	if err := s.repo.BumpTokenVersion(ctx, claims.UserID); err != nil {
		// 吊销失败也要拒绝这次刷新：宁可让用户重新登录，也不能让疑似泄露的令牌换到新令牌。
		logger.L().Error("吊销会话失败", zap.Int64("user_id", claims.UserID), zap.Error(err))
	}
	return errcode.ErrTokenInvalid
}

// Profile 查询指定用户的资料。
func (s *Service) Profile(ctx context.Context, userID int64) (*dto.UserInfo, error) {
	u, err := s.repo.GetByID(ctx, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}
	return toUserInfo(u), nil
}

// UpdateProfile 部分更新个人资料，返回更新后的完整资料。
//
// 只处理请求里出现过的字段（指针非 nil），因此同一次提交不会误改其他列；
// 更新走 map 传列名，空串 / 0 / NULL 这些零值才能正常写进库。
//
// username 是这里唯一带唯一键的字段：先查重（排除自己）再由唯一键兜底；
// 改名成功后旧用户名立即无法登录，邮箱通道不受影响。
func (s *Service) UpdateProfile(ctx context.Context, userID int64, req dto.UpdateProfileRequest) (*dto.UserInfo, error) {
	current, err := s.repo.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errcode.ErrUserNotFound
		}
		return nil, err
	}

	updates := make(map[string]any, 7)

	if req.Username != nil {
		username := utils.NormalizeUsername(*req.Username)
		if !utils.IsValidUsername(username) {
			return nil, errcode.ErrInvalidParam.WithMsg("用户名需为 3-24 位，以字母开头，仅含小写字母、数字和下划线")
		}
		// 与原值相同就省掉这次查重：写回自己不会撞唯一键，也免得「只提交用户名
		// 且没有变化」的请求落到下面报「没有需要更新的字段」。
		if username != current.Username {
			taken, err := s.repo.ExistsByUsernameExcept(ctx, username, userID)
			if err != nil {
				return nil, err
			}
			if taken {
				return nil, errcode.ErrUsernameTaken
			}
		}
		updates["username"] = username
	}

	if req.Nickname != nil {
		nickname := strings.TrimSpace(*req.Nickname)
		if nickname == "" {
			return nil, errcode.ErrInvalidParam.WithMsg("昵称不能为空")
		}
		if utf8.RuneCountInString(nickname) > 32 {
			return nil, errcode.ErrInvalidParam.WithMsg("昵称不能超过 32 个字符")
		}
		updates["nickname"] = nickname
	}

	if req.Signature != nil {
		signature := strings.TrimSpace(*req.Signature)
		if utf8.RuneCountInString(signature) > 140 {
			return nil, errcode.ErrInvalidParam.WithMsg("个人签名不能超过 140 个字符")
		}
		updates["signature"] = signature
	}

	if req.Gender != nil {
		switch *req.Gender {
		case model.GenderUnset, model.GenderMale, model.GenderFemale, model.GenderSecret:
			updates["gender"] = *req.Gender
		default:
			return nil, errcode.ErrInvalidParam.WithMsg("性别取值不合法")
		}
	}

	if req.Birthday != nil {
		value := strings.TrimSpace(*req.Birthday)
		if value == "" {
			// 空串代表清空生日，写入 NULL
			updates["birthday"] = nil
		} else {
			birthday, err := time.Parse("2006-01-02", value)
			if err != nil {
				return nil, errcode.ErrInvalidParam.WithMsg("生日格式不正确")
			}
			if birthday.After(time.Now()) {
				return nil, errcode.ErrInvalidParam.WithMsg("生日不能晚于今天")
			}
			updates["birthday"] = birthday
		}
	}

	if req.School != nil {
		school := strings.TrimSpace(*req.School)
		if utf8.RuneCountInString(school) > 64 {
			return nil, errcode.ErrInvalidParam.WithMsg("学校不能超过 64 个字符")
		}
		updates["school"] = school
	}

	if req.Location != nil {
		location := strings.TrimSpace(*req.Location)
		if utf8.RuneCountInString(location) > 64 {
			return nil, errcode.ErrInvalidParam.WithMsg("现居地不能超过 64 个字符")
		}
		updates["location"] = location
	}

	if len(updates) == 0 {
		return nil, errcode.ErrInvalidParam.WithMsg("没有需要更新的字段")
	}

	if err := s.repo.UpdateProfile(ctx, userID, updates); err != nil {
		// 并发下两人同时改成同一个名字时，预检查可能双双通过，由 uk_username 兜底；
		// 这里把它翻成「已被占用」，而不是一个没有信息量的 500。
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, errcode.ErrUsernameTaken
		}
		return nil, err
	}

	u, err := s.repo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return toUserInfo(u), nil
}

// ChangePassword 修改当前登录用户的密码。
//
// 必须先证明「你是你」：拿当前密码与库里的哈希比对，避免访问令牌落到别人手里就能直接改密；
// 新密码沿用注册时的长度口径（6~64 个字符），并拒绝与旧密码相同。
//
// 刻意**不**推进令牌版本（与 ResetPassword 相反）：改密是本人带着当前密码完成的操作，
// 顺手把包括本机在内的所有会话踢掉、要求重新登录，对正常用户只是打扰。
// 令牌本身是无状态的，因此改密前签发的访问令牌仍会自然过期（最长 15 分钟）。
func (s *Service) ChangePassword(ctx context.Context, userID int64, req dto.ChangePasswordRequest) (*dto.UserInfo, error) {
	u, err := s.repo.GetByID(ctx, userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errcode.ErrUserNotFound
	}
	if err != nil {
		return nil, err
	}

	if !utils.CheckPassword(u.PasswordHash, req.OldPassword) {
		return nil, errcode.ErrOldPasswordWrong
	}
	if length := utf8.RuneCountInString(req.NewPassword); length < 6 || length > 64 {
		return nil, errcode.ErrInvalidParam.WithMsg("密码需为 6~64 个字符")
	}
	if req.NewPassword == req.OldPassword {
		return nil, errcode.ErrInvalidParam.WithMsg("新密码不能与当前密码相同")
	}

	hash, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdatePassword(ctx, userID, hash); err != nil {
		return nil, err
	}

	// 密码哈希不进 UserInfo，返回的资料与改密前一致，只是让前端拿到最新的权威副本。
	return toUserInfo(u), nil
}

// ResetPassword 用邮箱验证码重置密码（忘记密码通道）。
//
// 与 ChangePassword 的分工：这条路不需要登录、也不校验旧密码，凭的是一次性邮箱验证码；
// 正因为少了一道「知道旧密码」的证明，它要承担更高的安全要求——成功后把该用户的
// 令牌版本 +1，让此前签发的刷新令牌全部失效（见 repository.ResetPassword）。
//
// 顺序上先用验证码再查用户：验证码是一次性凭证，与注册同口径，校验通过即销毁。
func (s *Service) ResetPassword(ctx context.Context, req dto.ResetPasswordRequest) error {
	email := utils.NormalizeEmail(req.Email)
	if !utils.IsValidEmail(email) {
		return errcode.ErrInvalidParam.WithMsg("邮箱格式不正确")
	}

	// 长度口径与注册 / 改密一致；放在核销验证码之前，参数不合法时这枚码还能再用一次。
	if length := utf8.RuneCountInString(req.NewPassword); length < 6 || length > 64 {
		return errcode.ErrInvalidParam.WithMsg("密码需为 6~64 个字符")
	}

	if err := s.codes.Verify(ctx, purposeReset, email, req.Code, s.policy()); err != nil {
		return err
	}

	u, err := s.repo.GetByEmail(ctx, email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 码已核销却查不到用户：不报「用户不存在」，否则这里会成为邮箱枚举的出口。
		return errcode.ErrCodeInvalid
	}
	if err != nil {
		return err
	}
	if !u.IsNormal() {
		return errcode.ErrUserDisabled
	}

	hash, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}
	return s.repo.ResetPassword(ctx, u.ID, hash)
}

// -------- 管理端：用户管理 --------

// AdminList 分页查询全部用户（用户管理列表）。
//
// 关键词模糊匹配用户名 / 邮箱 / 昵称；role、status 为可选过滤。
func (s *Service) AdminList(ctx context.Context, in dto.AdminListUsersRequest) (*dto.UserPage, error) {
	page, pageSize, offset := utils.NormalizePage(in.Page, in.PageSize)

	if in.Role != nil && *in.Role != model.RoleUser && *in.Role != model.RoleAdmin {
		return nil, errcode.ErrInvalidParam.WithMsg("角色取值不合法")
	}
	if in.Status != nil && *in.Status != model.StatusNormal && *in.Status != model.StatusDisabled {
		return nil, errcode.ErrInvalidParam.WithMsg("账号状态取值不合法")
	}

	users, total, err := s.repo.List(ctx, repository.UserFilter{
		Keyword: strings.TrimSpace(in.Keyword),
		Role:    in.Role,
		Status:  in.Status,
	}, offset, pageSize)
	if err != nil {
		return nil, err
	}

	list := make([]dto.UserInfo, 0, len(users))
	for i := range users {
		list = append(list, *toUserInfo(&users[i]))
	}
	return &dto.UserPage{Users: list, Total: total, Page: page, PageSize: pageSize}, nil
}

// AdminUpdate 修改指定用户的账号状态 / 角色。
//
// 三条规则（错误提示直接给前端展示）：
//   - 至少给一个字段，空请求视为无意义写；
//   - **不能改自己**——否则管理员可以把自己降级或封禁，把自己锁在门外；
//   - 取值只能是 0/1（status 0 禁用 / 1 正常，role 0 普通 / 1 管理员）。
//
// 生效时机：禁用与角色变更都只在**下一次登录 / 刷新**时生效。access token 是无状态的，
// 已签发的那枚仍带着旧角色与「正常」状态，最长还能用 15 分钟；refresh 会读库并按新状态签发或拒绝。
func (s *Service) AdminUpdate(ctx context.Context, operatorID, targetID int64, in dto.AdminUpdateUserRequest) (*dto.UserInfo, error) {
	if operatorID == targetID {
		return nil, errcode.ErrInvalidParam.WithMsg("不能修改自己的账号状态或角色")
	}

	updates := make(map[string]any, 2)
	if in.Status != nil {
		if *in.Status != model.StatusNormal && *in.Status != model.StatusDisabled {
			return nil, errcode.ErrInvalidParam.WithMsg("账号状态取值不合法")
		}
		updates["status"] = *in.Status
	}
	if in.Role != nil {
		if *in.Role != model.RoleUser && *in.Role != model.RoleAdmin {
			return nil, errcode.ErrInvalidParam.WithMsg("角色取值不合法")
		}
		updates["role"] = *in.Role
	}
	if len(updates) == 0 {
		return nil, errcode.ErrInvalidParam.WithMsg("没有需要更新的字段")
	}

	// 先确认存在：UPDATE 命中 0 行不会报错，直接写会静默成功。
	if _, err := s.repo.GetByID(ctx, targetID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errcode.ErrUserNotFound
		}
		return nil, err
	}

	if err := s.repo.AdminUpdate(ctx, targetID, updates); err != nil {
		return nil, err
	}
	u, err := s.repo.GetByID(ctx, targetID)
	if err != nil {
		return nil, err
	}
	return toUserInfo(u), nil
}

// issueTokens 同时签发访问令牌与刷新令牌。
func (s *Service) issueTokens(u *model.User) (*dto.TokenPair, string, error) {
	accessToken, expiresIn, err := s.jwt.GenerateAccessToken(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		return nil, "", err
	}
	refreshToken, err := s.jwt.GenerateRefreshToken(u.ID, u.Role, u.TokenVersion)
	if err != nil {
		return nil, "", err
	}

	return &dto.TokenPair{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		ExpiresIn:   expiresIn,
	}, refreshToken, nil
}

// toUserInfo 把数据模型转换为对外 DTO，屏蔽密码哈希等敏感字段。
func toUserInfo(u *model.User) *dto.UserInfo {
	info := &dto.UserInfo{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Nickname:  u.Nickname,
		Avatar:    u.Avatar,
		Signature: u.Signature,
		Gender:    u.Gender,
		School:    u.School,
		Location:  u.Location,
		Role:      u.Role,
		Status:    u.Status,
		CreatedAt: u.CreatedAt,
	}
	// 生日按 YYYY-MM-DD 字符串对外，未填写时留空串；年龄由生日实时推算，不落库。
	if u.Birthday != nil {
		info.Birthday = u.Birthday.Format("2006-01-02")
		info.Age = calcAge(*u.Birthday, time.Now())
	}
	return info
}

// calcAge 按自然年推算周岁：今年生日还没过就减一岁。
func calcAge(birthday, now time.Time) int {
	age := now.Year() - birthday.Year()
	if now.Month() < birthday.Month() ||
		(now.Month() == birthday.Month() && now.Day() < birthday.Day()) {
		age--
	}
	if age < 0 {
		age = 0
	}
	return age
}
