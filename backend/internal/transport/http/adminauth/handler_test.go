package adminauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminapp "github.com/chenyme/grok2api/backend/internal/application/adminauth"
	admindomain "github.com/chenyme/grok2api/backend/internal/domain/admin"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"github.com/chenyme/grok2api/backend/internal/transport/http/adminsession"
	"github.com/chenyme/grok2api/backend/internal/transport/http/middleware"
	"github.com/gin-gonic/gin"
)

func TestSessionCookiesAuthenticateRefreshAndProtectedRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}

	service := adminapp.NewService(
		relational.NewAdminRepository(database),
		relational.NewAdminSessionRepository(database),
		security.NewTokenService("12345678901234567890123456789012"),
		15*time.Minute,
		30*24*time.Hour,
	)
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	handler := NewHandler(service, true)
	root := router.Group("/api/admin/v1")
	handler.RegisterPublic(root)
	protected := root.Group("")
	protected.Use(middleware.AdminAuth(service))
	handler.RegisterAuthenticated(protected)

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"password123"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	if strings.Contains(login.Body.String(), `"refreshToken":`) {
		t.Fatalf("login response exposed refresh token: %s", login.Body.String())
	}
	cookies := login.Result().Cookies()
	accessCookie := cookieNamed(cookies, adminsession.AccessCookieName)
	refreshCookie := cookieNamed(cookies, adminsession.RefreshCookieName)
	if accessCookie == nil || accessCookie.Path != adminsession.AccessCookiePath || !accessCookie.HttpOnly || !accessCookie.Secure || accessCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected access cookie: %#v", accessCookie)
	}
	if refreshCookie == nil || refreshCookie.Path != adminsession.RefreshCookiePath || !refreshCookie.HttpOnly || !refreshCookie.Secure || refreshCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected refresh cookie: %#v", refreshCookie)
	}

	me := httptest.NewRecorder()
	meRequest := httptest.NewRequest(http.MethodGet, "/api/admin/v1/me", nil)
	meRequest.AddCookie(accessCookie)
	router.ServeHTTP(me, meRequest)
	if me.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated me status = %d, body = %s", me.Code, me.Body.String())
	}

	refresh := httptest.NewRecorder()
	refreshRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/refresh", strings.NewReader(`{}`))
	refreshRequest.Header.Set("Content-Type", "application/json")
	refreshRequest.AddCookie(refreshCookie)
	router.ServeHTTP(refresh, refreshRequest)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refresh.Code, refresh.Body.String())
	}
	if strings.Contains(refresh.Body.String(), `"refreshToken":`) {
		t.Fatalf("refresh response exposed refresh token: %s", refresh.Body.String())
	}
	rotatedAccessCookie := cookieNamed(refresh.Result().Cookies(), adminsession.AccessCookieName)
	rotatedRefreshCookie := cookieNamed(refresh.Result().Cookies(), adminsession.RefreshCookieName)
	if rotatedAccessCookie == nil || rotatedRefreshCookie == nil {
		t.Fatalf("refresh did not rotate both session cookies: %#v", refresh.Result().Cookies())
	}

	logout := httptest.NewRecorder()
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/logout", strings.NewReader(`{}`))
	logoutRequest.Header.Set("Content-Type", "application/json")
	logoutRequest.AddCookie(rotatedRefreshCookie)
	router.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status = %d, body = %s", logout.Code, logout.Body.String())
	}
	clearedAccessCookie := cookieNamed(logout.Result().Cookies(), adminsession.AccessCookieName)
	clearedRefreshCookie := cookieNamed(logout.Result().Cookies(), adminsession.RefreshCookieName)
	if clearedAccessCookie == nil || clearedAccessCookie.MaxAge >= 0 || clearedRefreshCookie == nil || clearedRefreshCookie.MaxAge >= 0 {
		t.Fatalf("logout did not clear both session cookies: %#v", logout.Result().Cookies())
	}

	revoked := httptest.NewRecorder()
	revokedRequest := httptest.NewRequest(http.MethodGet, "/api/admin/v1/me", nil)
	revokedRequest.AddCookie(rotatedAccessCookie)
	router.ServeHTTP(revoked, revokedRequest)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked access cookie status = %d, want %d", revoked.Code, http.StatusUnauthorized)
	}
}

func TestLogoutClearsCookiesWhenRevocationFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := adminapp.NewService(nil, failingSessionRepository{}, security.NewTokenService(testJWTSecret), time.Minute, time.Hour)
	router := gin.New()
	NewHandler(service, true).RegisterPublic(router.Group("/api/admin/v1"))

	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/logout", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: adminsession.RefreshCookieName, Value: "refresh-token"})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("logout status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	accessCookie := cookieNamed(recorder.Result().Cookies(), adminsession.AccessCookieName)
	refreshCookie := cookieNamed(recorder.Result().Cookies(), adminsession.RefreshCookieName)
	if accessCookie == nil || accessCookie.MaxAge >= 0 || refreshCookie == nil || refreshCookie.MaxAge >= 0 {
		t.Fatalf("failed logout kept session cookies: %#v", recorder.Result().Cookies())
	}
}

func TestSessionCookiesTrustForwardedProtoOnlyFromTrustedProxies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newTestAuthService(t)
	tests := []struct {
		name           string
		trustedProxies []string
		remoteAddr     string
		forwardedProto string
		secure         bool
	}{
		{name: "trusted proxy https", trustedProxies: []string{"10.0.0.0/8"}, remoteAddr: "10.1.2.3:4567", forwardedProto: "https", secure: true},
		{name: "trusted proxy first hop https", trustedProxies: []string{"10.1.2.3"}, remoteAddr: "10.1.2.3:4567", forwardedProto: "HTTPS, http", secure: true},
		{name: "trusted proxy http", trustedProxies: []string{"10.0.0.0/8"}, remoteAddr: "10.1.2.3:4567", forwardedProto: "http"},
		{name: "untrusted peer", trustedProxies: []string{"10.0.0.0/8"}, remoteAddr: "203.0.113.9:4567", forwardedProto: "https"},
		{name: "no trusted proxies", remoteAddr: "10.1.2.3:4567", forwardedProto: "https"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			NewHandler(service, false).WithTrustedProxies(test.trustedProxies).RegisterPublic(router.Group("/api/admin/v1"))
			request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"password123"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-Proto", test.forwardedProto)
			request.RemoteAddr = test.remoteAddr
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("login status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			for _, name := range []string{adminsession.AccessCookieName, adminsession.RefreshCookieName} {
				cookie := cookieNamed(recorder.Result().Cookies(), name)
				if cookie == nil || cookie.Secure != test.secure {
					t.Fatalf("%s cookie = %#v, want Secure=%v", name, cookie, test.secure)
				}
			}
		})
	}
}

func TestLoginRateLimitUsesTrustedProxyClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newTestAuthService(t)
	limiter := &recordingRateLimiter{}
	service.SetLoginRateLimiter(limiter)
	router := gin.New()
	if err := router.SetTrustedProxies([]string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	router.Use(middleware.ClientIP())
	NewHandler(service, false).RegisterPublic(router.Group("/api/admin/v1"))

	for _, test := range []struct{ remoteAddr, clientIP string }{
		{remoteAddr: "10.1.2.3:4567", clientIP: "203.0.113.20"},
		{remoteAddr: "198.51.100.10:4567", clientIP: "198.51.100.10"},
	} {
		limiter.keys = nil
		request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"wrong-password"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", "203.0.113.20")
		request.RemoteAddr = test.remoteAddr
		router.ServeHTTP(httptest.NewRecorder(), request)
		want := "admin-login:ip:" + security.HashToken(test.clientIP)
		if len(limiter.keys) == 0 || limiter.keys[0] != want {
			t.Fatalf("peer %s rate-limit keys = %v, want IP key for %s", test.remoteAddr, limiter.keys, test.clientIP)
		}
	}
}

func TestChangePasswordMapsLengthAndRateLimitErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := newTestAuthService(t)
	service.SetLoginRateLimiter(memory.NewRateLimiter())
	router := gin.New()
	handler := NewHandler(service, false)
	root := router.Group("/api/admin/v1")
	handler.RegisterPublic(root)
	protected := root.Group("")
	protected.Use(middleware.AdminAuth(service))
	handler.RegisterAuthenticated(protected)

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"password123"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(login, loginRequest)
	accessCookie := cookieNamed(login.Result().Cookies(), adminsession.AccessCookieName)
	if login.Code != http.StatusOK || accessCookie == nil {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	changePassword := func(currentPassword, newPassword string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"currentPassword": currentPassword, "newPassword": newPassword})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPut, "/api/admin/v1/me/password", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.AddCookie(accessCookie)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder
	}

	tooLong := changePassword("password123", strings.Repeat("密", 25))
	if tooLong.Code != http.StatusBadRequest || !strings.Contains(tooLong.Body.String(), "invalidNewPassword") || !strings.Contains(tooLong.Body.String(), "8 到 72 字节") {
		t.Fatalf("too long password status = %d, body = %s", tooLong.Code, tooLong.Body.String())
	}
	for range 12 {
		if recorder := changePassword("wrong-password", "password456"); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "currentPasswordIncorrect") {
			t.Fatalf("wrong current password status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
	}
	limited := changePassword("password123", "password456")
	if limited.Code != http.StatusTooManyRequests || !strings.Contains(limited.Body.String(), "passwordChangeRateLimited") {
		t.Fatalf("rate-limited password change status = %d, body = %s", limited.Code, limited.Body.String())
	}
}

const testJWTSecret = "12345678901234567890123456789012"

func newTestAuthService(t *testing.T) *adminapp.Service {
	t.Helper()
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	service := adminapp.NewService(relational.NewAdminRepository(database), relational.NewAdminSessionRepository(database), security.NewTokenService(testJWTSecret), 15*time.Minute, 30*24*time.Hour)
	if err := service.Bootstrap(ctx, "admin", "password123"); err != nil {
		t.Fatal(err)
	}
	return service
}

type failingSessionRepository struct {
	repository.AdminSessionRepository
}

func (failingSessionRepository) GetByTokenHash(context.Context, string) (admindomain.Session, error) {
	return admindomain.Session{}, errors.New("database unavailable")
}

type recordingRateLimiter struct{ keys []string }

func (r *recordingRateLimiter) Allow(_ context.Context, key string, _ int, _ time.Time) (bool, error) {
	r.keys = append(r.keys, key)
	return true, nil
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}
