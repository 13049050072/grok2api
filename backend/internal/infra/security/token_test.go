package security

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testTokenSecret = "12345678901234567890123456789012"

func TestParseAccessTokenRequiresValidRegisteredClaims(t *testing.T) {
	service := NewTokenService(testTokenSecret)
	raw, _, err := service.CreateAccessToken(1, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := service.ParseAccessToken(raw)
	if err != nil || identity.AdminID != 1 || identity.SessionID != 2 {
		t.Fatalf("ParseAccessToken() = %#v, %v", identity, err)
	}

	now := time.Now().UTC()
	sign := func(method jwt.SigningMethod, secret string, mutate func(*jwt.RegisteredClaims)) string {
		t.Helper()
		claims := adminClaims{AdminID: 1, SessionID: 2, RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "grok2api",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		}}
		mutate(&claims.RegisteredClaims)
		signed, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	unchanged := func(*jwt.RegisteredClaims) {}
	for name, token := range map[string]string{
		"missing exp":  sign(jwt.SigningMethodHS256, testTokenSecret, func(claims *jwt.RegisteredClaims) { claims.ExpiresAt = nil }),
		"expired":      sign(jwt.SigningMethodHS256, testTokenSecret, func(claims *jwt.RegisteredClaims) { claims.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute)) }),
		"wrong issuer": sign(jwt.SigningMethodHS256, testTokenSecret, func(claims *jwt.RegisteredClaims) { claims.Issuer = "other" }),
		"HS512":        sign(jwt.SigningMethodHS512, testTokenSecret, unchanged),
		"wrong secret": sign(jwt.SigningMethodHS256, strings.Repeat("x", len(testTokenSecret)), unchanged),
	} {
		if _, err := service.ParseAccessToken(token); err == nil {
			t.Fatalf("%s token was accepted", name)
		}
	}
}

func TestHashPasswordRejectsPasswordsBeyondBcryptLimit(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes)); err != nil {
		t.Fatalf("%d-byte password rejected: %v", MaxPasswordBytes, err)
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordBytes+1)); err == nil {
		t.Fatalf("%d-byte password accepted", MaxPasswordBytes+1)
	}
}

func TestClientKeyFormat(t *testing.T) {
	raw := FormatClientKey("abc123", "secret_value")
	if raw != "g2a_abc123_secret_value" {
		t.Fatalf("formatted key = %q", raw)
	}
	prefix, ok := SplitClientKey(raw)
	if !ok || prefix != "abc123" {
		t.Fatalf("SplitClientKey(%q) = %q, %v", raw, prefix, ok)
	}
	for _, value := range []string{"", "g2a_", "g2a__secret", "other_abc123_secret", "gbp_abc123_old_secret"} {
		if _, ok := SplitClientKey(value); ok {
			t.Fatalf("SplitClientKey(%q) unexpectedly succeeded", value)
		}
	}
}
