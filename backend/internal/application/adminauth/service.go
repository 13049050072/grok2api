package adminauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/admin"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

const (
	loginIPRateLimit      = 30
	loginAccountRateLimit = 12
	// maxSessionLifetime 自会话创建起计算，refresh 轮换不能把会话续期到该上限之后。
	maxSessionLifetime = 90 * 24 * time.Hour
	// 单个 IPv6 主机通常可支配整段 /64，按完整地址计数时轮换地址即可绕过限流。
	rateLimitIPv6PrefixBits = 64
)

var (
	ErrInvalidCredentials      = errors.New("管理员账号或密码错误")
	ErrInvalidSession          = errors.New("管理员会话无效")
	ErrBootstrapRequired       = errors.New("首次启动需要设置管理员账号和密码")
	ErrBootstrapPasswordLength = fmt.Errorf("bootstrapAdmin.password 长度必须为 %d 到 %d 字节", security.MinPasswordBytes, security.MaxPasswordBytes)
	ErrInvalidPassword         = fmt.Errorf("新密码长度必须为 %d 到 %d 字节", security.MinPasswordBytes, security.MaxPasswordBytes)
	ErrLoginRateLimited        = errors.New("管理员登录尝试过于频繁")
	ErrRuntimeUnavailable      = errors.New("管理员认证运行态暂不可用")
)

type Tokens struct {
	AccessToken           string
	AccessTokenExpiresAt  time.Time
	RefreshToken          string
	RefreshTokenExpiresAt time.Time
}

// Service 负责编排单管理员登录、JWT 和 refresh session 生命周期。
type Service struct {
	admins            repository.AdminRepository
	sessions          repository.AdminSessionRepository
	tokens            *security.TokenService
	accessTTL         time.Duration
	refreshTTL        time.Duration
	loginLimiter      repository.RateLimiter
	dummyPasswordHash string
}

func NewService(admins repository.AdminRepository, sessions repository.AdminSessionRepository, tokens *security.TokenService, accessTTL, refreshTTL time.Duration) *Service {
	dummyHash, _ := security.HashPassword("grok2api-invalid-admin-password")
	return &Service{admins: admins, sessions: sessions, tokens: tokens, accessTTL: accessTTL, refreshTTL: refreshTTL, dummyPasswordHash: dummyHash}
}

func (s *Service) SetLoginRateLimiter(limiter repository.RateLimiter) { s.loginLimiter = limiter }

// Bootstrap 在数据库没有管理员时创建唯一管理员。
func (s *Service) Bootstrap(ctx context.Context, username, password string) error {
	count, err := s.admins.Count(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if strings.TrimSpace(username) == "" || password == "" {
		return ErrBootstrapRequired
	}
	if len(password) < security.MinPasswordBytes || len(password) > security.MaxPasswordBytes {
		return ErrBootstrapPasswordLength
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.admins.Create(ctx, admin.Admin{Username: strings.TrimSpace(username), PasswordHash: hash})
	return err
}

// Login 校验密码并创建新的可撤销 refresh session。clientIP 应为按可信代理解析后的调用方地址。
func (s *Service) Login(ctx context.Context, username, password, clientIP string) (admin.Admin, Tokens, error) {
	username = strings.TrimSpace(username)
	if err := s.checkLoginRate(ctx, username, clientIP); err != nil {
		return admin.Admin{}, Tokens{}, err
	}
	value, err := s.admins.GetByUsername(ctx, username)
	if err != nil {
		_ = security.VerifyPassword(s.dummyPasswordHash, password)
		if !errors.Is(err, repository.ErrNotFound) {
			return admin.Admin{}, Tokens{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return admin.Admin{}, Tokens{}, ErrInvalidCredentials
	}
	if !security.VerifyPassword(value.PasswordHash, password) {
		return admin.Admin{}, Tokens{}, ErrInvalidCredentials
	}
	tokens, _, err := s.createSession(ctx, value.ID)
	return value, tokens, err
}

// Refresh 轮换 refresh token，旧 token 立即失效；再次提交上一个 token 会撤销整个会话。
func (s *Service) Refresh(ctx context.Context, rawRefreshToken string) (Tokens, error) {
	hash := security.HashToken(rawRefreshToken)
	session, err := s.sessions.GetByTokenHash(ctx, hash)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return Tokens{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return Tokens{}, s.revokeReusedSession(ctx, hash)
	}
	now := time.Now().UTC()
	if !sessionActive(session, now) {
		return Tokens{}, ErrInvalidSession
	}
	adminValue, err := s.admins.GetByID(ctx, session.AdminID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return Tokens{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return Tokens{}, ErrInvalidSession
	}
	accessToken, accessExpiresAt, err := s.tokens.CreateAccessToken(adminValue.ID, session.ID, s.accessTTL)
	if err != nil {
		return Tokens{}, err
	}
	refreshToken, err := security.NewOpaqueToken(32)
	if err != nil {
		return Tokens{}, err
	}
	refreshExpiresAt := now.Add(s.refreshTTL)
	if deadline := session.CreatedAt.Add(maxSessionLifetime); refreshExpiresAt.After(deadline) {
		refreshExpiresAt = deadline
	}
	if err := s.sessions.Rotate(ctx, session.ID, hash, security.HashToken(refreshToken), refreshExpiresAt); err != nil {
		if errors.Is(err, repository.ErrConflict) || errors.Is(err, repository.ErrNotFound) {
			return Tokens{}, ErrInvalidSession
		}
		return Tokens{}, err
	}
	return Tokens{AccessToken: accessToken, AccessTokenExpiresAt: accessExpiresAt, RefreshToken: refreshToken, RefreshTokenExpiresAt: refreshExpiresAt}, nil
}

// Logout 撤销当前 refresh session。
func (s *Service) Logout(ctx context.Context, rawRefreshToken string) error {
	session, err := s.sessions.GetByTokenHash(ctx, security.HashToken(rawRefreshToken))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	if err := s.sessions.Revoke(ctx, session.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	return nil
}

// AuthenticateAccess 校验 access token 并读取管理员。
func (s *Service) AuthenticateAccess(ctx context.Context, rawAccessToken string) (admin.Admin, error) {
	identity, err := s.tokens.ParseAccessToken(rawAccessToken)
	if err != nil {
		return admin.Admin{}, ErrInvalidSession
	}
	session, err := s.sessions.GetByID(ctx, identity.SessionID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return admin.Admin{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return admin.Admin{}, ErrInvalidSession
	}
	if session.AdminID != identity.AdminID || !sessionActive(session, time.Now().UTC()) {
		return admin.Admin{}, ErrInvalidSession
	}
	value, err := s.admins.GetByID(ctx, identity.AdminID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return admin.Admin{}, fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return admin.Admin{}, ErrInvalidSession
	}
	return value, nil
}

// ChangePassword 修改密码并撤销管理员的全部 refresh session。当前密码校验与登录共用限流器。
func (s *Service) ChangePassword(ctx context.Context, adminID uint64, currentPassword, newPassword, clientIP string) error {
	if len(newPassword) < security.MinPasswordBytes || len(newPassword) > security.MaxPasswordBytes {
		return ErrInvalidPassword
	}
	passwordKey := fmt.Sprintf("%d\n%s", adminID, rateLimitClient(clientIP))
	if err := s.checkRate(ctx, rateLimit{key: "admin-login:password-change:" + security.HashToken(passwordKey), limit: loginAccountRateLimit}); err != nil {
		return err
	}
	value, err := s.admins.GetByID(ctx, adminID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		return ErrInvalidCredentials
	}
	if !security.VerifyPassword(value.PasswordHash, currentPassword) {
		return ErrInvalidCredentials
	}
	hash, err := security.HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.admins.UpdatePasswordAndRevokeSessions(ctx, adminID, hash)
}

func (s *Service) createSession(ctx context.Context, adminID uint64) (Tokens, admin.Session, error) {
	refreshToken, err := security.NewOpaqueToken(32)
	if err != nil {
		return Tokens{}, admin.Session{}, err
	}
	refreshExpiresAt := time.Now().UTC().Add(min(s.refreshTTL, maxSessionLifetime))
	session, err := s.sessions.Create(ctx, admin.Session{AdminID: adminID, RefreshTokenHash: security.HashToken(refreshToken), ExpiresAt: refreshExpiresAt})
	if err != nil {
		return Tokens{}, admin.Session{}, err
	}
	accessToken, accessExpiresAt, err := s.tokens.CreateAccessToken(adminID, session.ID, s.accessTTL)
	if err != nil {
		_ = s.sessions.Revoke(ctx, session.ID)
		return Tokens{}, admin.Session{}, err
	}
	return Tokens{AccessToken: accessToken, AccessTokenExpiresAt: accessExpiresAt, RefreshToken: refreshToken, RefreshTokenExpiresAt: refreshExpiresAt}, session, err
}

// revokeReusedSession 处理已不是当前值的 refresh token：若它是某会话的上一个 token，
// 说明该 token 已被轮换后再次使用（可能被窃取），撤销整个会话。
func (s *Service) revokeReusedSession(ctx context.Context, tokenHash string) error {
	session, err := s.sessions.GetByPreviousTokenHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrInvalidSession
		}
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	if err := s.sessions.Revoke(ctx, session.ID); err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
	}
	return ErrInvalidSession
}

func sessionActive(session admin.Session, now time.Time) bool {
	return now.Before(session.ExpiresAt) && now.Before(session.CreatedAt.Add(maxSessionLifetime))
}

type rateLimit struct {
	key   string
	limit int
}

func (s *Service) checkLoginRate(ctx context.Context, username, clientIP string) error {
	client := rateLimitClient(clientIP)
	return s.checkRate(ctx,
		rateLimit{key: "admin-login:ip:" + security.HashToken(client), limit: loginIPRateLimit},
		// 账号维度按 (客户端 IP, 用户名) 计数，其他来源的失败尝试不会锁定管理员本人。
		rateLimit{key: "admin-login:ip-user:" + security.HashToken(client+"\n"+strings.ToLower(username)), limit: loginAccountRateLimit},
	)
}

// rateLimitClient 返回限流计数主体：IPv4 按单个地址，IPv6 按所在 /64。
func rateLimitClient(clientIP string) string {
	clientIP = strings.TrimSpace(clientIP)
	ip := net.ParseIP(clientIP)
	switch {
	case ip == nil:
		return clientIP
	case ip.To4() != nil:
		return ip.To4().String()
	default:
		return ip.Mask(net.CIDRMask(rateLimitIPv6PrefixBits, 8*net.IPv6len)).String() + fmt.Sprintf("/%d", rateLimitIPv6PrefixBits)
	}
}

func (s *Service) checkRate(ctx context.Context, limits ...rateLimit) error {
	if s.loginLimiter == nil {
		return nil
	}
	now := time.Now().UTC()
	for _, item := range limits {
		allowed, err := s.loginLimiter.Allow(ctx, item.key, item.limit, now)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRuntimeUnavailable, err)
		}
		if !allowed {
			return ErrLoginRateLimited
		}
	}
	return nil
}
