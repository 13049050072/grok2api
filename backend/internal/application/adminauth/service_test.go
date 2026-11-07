package adminauth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/admin"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

const testJWTSecret = "12345678901234567890123456789012"

func TestRefreshTokenRotationAndLogout(t *testing.T) {
	database, err := relational.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService("12345678901234567890123456789012"), time.Minute, time.Hour)
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	_, tokens, err := service.Login(ctx, "admin", "password123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Refresh(ctx, tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAccess(ctx, rotated.AccessToken); err != nil {
		t.Fatalf("轮换后的 access token 无法使用: %v", err)
	}
	if err := service.Logout(ctx, rotated.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAccess(ctx, rotated.AccessToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("注销后的 access token 仍可使用: %v", err)
	}
	if _, err := service.Refresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("注销后的 refresh token 仍可使用: %v", err)
	}
	if _, err := service.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("旧 refresh token 仍可使用: %v", err)
	}
}

func TestRefreshTokenReuseRevokesSession(t *testing.T) {
	database := openAuthDatabase(t)
	service := NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	_, tokens, err := service.Login(ctx, "admin", "password123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.Refresh(ctx, tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("重放的旧 refresh token 错误 = %v", err)
	}
	if _, err := service.Refresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("旧 token 重放后会话未被撤销，新 refresh token 仍可使用: %v", err)
	}
	if _, err := service.AuthenticateAccess(ctx, rotated.AccessToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("旧 token 重放后 access token 仍可使用: %v", err)
	}
}

func TestSessionAbsoluteLifetimeLimitsRefreshAndAccess(t *testing.T) {
	database := openAuthDatabase(t)
	sessions := &agedSessionRepository{AdminSessionRepository: relational.NewAdminSessionRepository(database)}
	service := NewService(relational.NewAdminRepository(database), sessions, security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	_, tokens, err := service.Login(ctx, "admin", "password123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	sessions.age = maxSessionLifetime - 30*time.Minute
	rotated, err := service.Refresh(ctx, tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(rotated.RefreshTokenExpiresAt); remaining < 29*time.Minute || remaining > 31*time.Minute {
		t.Fatalf("refresh 有效期未截断到会话绝对上限，剩余 %s", remaining)
	}
	if _, err := service.AuthenticateAccess(ctx, rotated.AccessToken); err != nil {
		t.Fatalf("绝对上限内的 access token 无法使用: %v", err)
	}

	sessions.age = maxSessionLifetime + time.Minute
	if _, err := service.AuthenticateAccess(ctx, rotated.AccessToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("超过绝对上限的 access token 错误 = %v", err)
	}
	if _, err := service.Refresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("超过绝对上限的 refresh token 错误 = %v", err)
	}
}

func TestLoginAccountRateLimitIsScopedToClientIP(t *testing.T) {
	database := openAuthDatabase(t)
	service := NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	service.SetLoginRateLimiter(memory.NewRateLimiter())
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	for range loginAccountRateLimit {
		if _, _, err := service.Login(ctx, "admin", "wrong-password", "203.0.113.7"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failed login error = %v", err)
		}
	}
	if _, _, err := service.Login(ctx, "ADMIN", "password123", "203.0.113.7"); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("same client and account error = %v", err)
	}
	if _, _, err := service.Login(ctx, "admin", "password123", "198.51.100.8"); err != nil {
		t.Fatalf("其他来源的失败尝试锁定了管理员: %v", err)
	}
}

func TestLoginRateLimitGroupsIPv6ClientsBySlash64(t *testing.T) {
	database := openAuthDatabase(t)
	service := NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	service.SetLoginRateLimiter(memory.NewRateLimiter())
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	for index := range loginAccountRateLimit {
		clientIP := fmt.Sprintf("2001:db8:1:2::%x", index+1)
		if _, _, err := service.Login(ctx, "admin", "wrong-password", clientIP); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failed login from %s error = %v", clientIP, err)
		}
	}
	if _, _, err := service.Login(ctx, "admin", "password123", "2001:db8:1:2:ffff::1"); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("同一 /64 内轮换地址绕过了限流: %v", err)
	}
	if _, _, err := service.Login(ctx, "admin", "password123", "2001:db8:1:3::1"); err != nil {
		t.Fatalf("相邻 /64 被一并锁定: %v", err)
	}
}

func TestLoginKeepsPerClientIPRateLimit(t *testing.T) {
	service := NewService(missingAdminRepository{}, nil, security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	service.SetLoginRateLimiter(memory.NewRateLimiter())
	ctx := context.Background()
	for index := range loginIPRateLimit {
		if _, _, err := service.Login(ctx, fmt.Sprintf("user-%d", index), "password123", "203.0.113.7"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d error = %v", index, err)
		}
	}
	if _, _, err := service.Login(ctx, "another-user", "password123", "203.0.113.7"); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("per-IP limit error = %v", err)
	}
	if _, _, err := service.Login(ctx, "another-user", "password123", "198.51.100.8"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("other client error = %v", err)
	}
}

func TestChangePasswordVerificationUsesLoginRateLimiter(t *testing.T) {
	database := openAuthDatabase(t)
	service := NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	service.SetLoginRateLimiter(memory.NewRateLimiter())
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	adminValue, _, err := service.Login(ctx, "admin", "password123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for range loginAccountRateLimit {
		if err := service.ChangePassword(ctx, adminValue.ID, "wrong-password", "password456", "203.0.113.7"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong current password error = %v", err)
		}
	}
	if err := service.ChangePassword(ctx, adminValue.ID, "password123", "password456", "203.0.113.7"); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("rate-limited password change error = %v", err)
	}
	if err := service.ChangePassword(ctx, adminValue.ID, "password123", "password456", "198.51.100.8"); err != nil {
		t.Fatalf("password change from another client failed: %v", err)
	}
}

func TestPasswordsAreLimitedToBcryptMaximum(t *testing.T) {
	database := openAuthDatabase(t)
	service := NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", ""); !errors.Is(err, ErrBootstrapRequired) {
		t.Fatalf("empty bootstrap password error = %v", err)
	}
	if err := service.Bootstrap(ctx, "admin", strings.Repeat("a", security.MinPasswordBytes-1)); !errors.Is(err, ErrBootstrapPasswordLength) {
		t.Fatalf("too short bootstrap password error = %v", err)
	}
	if err := service.Bootstrap(ctx, "admin", strings.Repeat("a", security.MaxPasswordBytes+1)); !errors.Is(err, ErrBootstrapPasswordLength) {
		t.Fatalf("too long bootstrap password error = %v", err)
	}
	longest := strings.Repeat("密", security.MaxPasswordBytes/len("密"))
	if err := service.Bootstrap(ctx, "admin", longest); err != nil {
		t.Fatalf("%d-byte bootstrap password rejected: %v", len(longest), err)
	}
	adminValue, _, err := service.Login(ctx, "admin", longest, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(ctx, adminValue.ID, longest, longest+"a", "127.0.0.1"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("too long new password error = %v", err)
	}
	if err := service.ChangePassword(ctx, adminValue.ID, longest, strings.Repeat("b", security.MaxPasswordBytes), "127.0.0.1"); err != nil {
		t.Fatalf("%d-byte new password rejected: %v", security.MaxPasswordBytes, err)
	}
}

func TestChangePasswordRevokesAllSessions(t *testing.T) {
	database, err := relational.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	service := NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService("12345678901234567890123456789012"), time.Minute, time.Hour)
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	adminValue, tokens, err := service.Login(ctx, "admin", "password123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(ctx, adminValue.ID, "password123", "password456", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateAccess(ctx, tokens.AccessToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("修改密码后的 access token 仍可使用: %v", err)
	}
	if _, err := service.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("修改密码后的 refresh token 仍可使用: %v", err)
	}
	if _, _, err := service.Login(ctx, "admin", "password123", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("旧密码仍可登录: %v", err)
	}
	if _, _, err := service.Login(ctx, "admin", "password456", "127.0.0.1"); err != nil {
		t.Fatalf("新密码无法登录: %v", err)
	}
}

func TestLoginRateLimiterFailureIsEnforced(t *testing.T) {
	service := NewService(nil, nil, security.NewTokenService("12345678901234567890123456789012"), time.Minute, time.Hour)
	service.SetLoginRateLimiter(rejectingRateLimiter{})
	if _, _, err := service.Login(context.Background(), "admin", "password123", "127.0.0.1"); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("login rate limit error = %v", err)
	}
}

func TestLoginDistinguishesPersistenceFailure(t *testing.T) {
	service := NewService(failingAdminRepository{}, nil, security.NewTokenService("12345678901234567890123456789012"), time.Minute, time.Hour)
	if _, _, err := service.Login(context.Background(), "admin", "password123", "127.0.0.1"); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("login persistence error = %v", err)
	}
}

func TestConcurrentRefreshAllowsExactlyOneRotation(t *testing.T) {
	database, err := relational.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(context.Background()); err != nil {
		t.Fatal(err)
	}

	baseSessions := relational.NewAdminSessionRepository(database)
	sessions := newCoordinatedSessionRepository(baseSessions, 2)
	service := NewService(
		relational.NewAdminRepository(database),
		sessions,
		security.NewTokenService("12345678901234567890123456789012"),
		time.Minute,
		time.Hour,
	)
	ctx := context.Background()
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	_, tokens, err := service.Login(ctx, "admin", "password123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	type refreshResult struct {
		tokens Tokens
		err    error
	}
	results := make(chan refreshResult, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			rotated, refreshErr := service.Refresh(ctx, tokens.RefreshToken)
			results <- refreshResult{tokens: rotated, err: refreshErr}
		}()
	}
	close(start)

	var successful Tokens
	successCount := 0
	invalidCount := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successCount++
			successful = result.tokens
		case errors.Is(result.err, ErrInvalidSession):
			invalidCount++
		default:
			t.Fatalf("unexpected refresh error: %v", result.err)
		}
	}
	if successCount != 1 || invalidCount != 1 {
		t.Fatalf("successes = %d, invalid sessions = %d", successCount, invalidCount)
	}
	if _, err := service.Refresh(ctx, successful.RefreshToken); err != nil {
		t.Fatalf("winning refresh token is unusable: %v", err)
	}
}

type coordinatedSessionRepository struct {
	repository.AdminSessionRepository
	mu        sync.Mutex
	remaining int
	ready     chan struct{}
}

type rejectingRateLimiter struct{}

func (rejectingRateLimiter) Allow(context.Context, string, int, time.Time) (bool, error) {
	return false, nil
}

type failingAdminRepository struct{ repository.AdminRepository }

func (failingAdminRepository) GetByUsername(context.Context, string) (admin.Admin, error) {
	return admin.Admin{}, errors.New("database unavailable")
}

type missingAdminRepository struct{ repository.AdminRepository }

func (missingAdminRepository) GetByUsername(context.Context, string) (admin.Admin, error) {
	return admin.Admin{}, repository.ErrNotFound
}

// agedSessionRepository 把会话创建时间前移 age，用于模拟长期存活的会话。
type agedSessionRepository struct {
	repository.AdminSessionRepository
	age time.Duration
}

func (r *agedSessionRepository) GetByTokenHash(ctx context.Context, tokenHash string) (admin.Session, error) {
	session, err := r.AdminSessionRepository.GetByTokenHash(ctx, tokenHash)
	session.CreatedAt = session.CreatedAt.Add(-r.age)
	return session, err
}

func (r *agedSessionRepository) GetByID(ctx context.Context, id uint64) (admin.Session, error) {
	session, err := r.AdminSessionRepository.GetByID(ctx, id)
	session.CreatedAt = session.CreatedAt.Add(-r.age)
	return session, err
}

func openAuthDatabase(t *testing.T) *relational.Database {
	t.Helper()
	database, err := relational.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return database
}

func newCoordinatedSessionRepository(base repository.AdminSessionRepository, reads int) *coordinatedSessionRepository {
	return &coordinatedSessionRepository{AdminSessionRepository: base, remaining: reads, ready: make(chan struct{})}
}

func (r *coordinatedSessionRepository) GetByTokenHash(ctx context.Context, tokenHash string) (admin.Session, error) {
	session, err := r.AdminSessionRepository.GetByTokenHash(ctx, tokenHash)
	if err != nil {
		return admin.Session{}, err
	}
	r.mu.Lock()
	if r.remaining > 0 {
		r.remaining--
		if r.remaining == 0 {
			close(r.ready)
		}
	}
	ready := r.ready
	r.mu.Unlock()
	<-ready
	return session, nil
}
