package provider

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testRegistrationJWT(subject string) string {
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + encode([]byte(`{"session_id":"`+subject+`"}`)) + "." + encode([]byte("signature-"+subject))
}

func TestLooksLikeJWT(t *testing.T) {
	jwt := testRegistrationJWT("sso")
	for value, want := range map[string]bool{
		jwt:                                true,
		" sso=" + jwt + "; Path=/":         true,
		"SSO=" + jwt:                       true,
		"sso-value":                        false,
		"Passw0rd":                         false,
		"a.b.c":                            false,
		jwt + ".extra":                     false,
		strings.Replace(jwt, ".", ".+", 1): false,
		strings.Replace(jwt, ".", "..", 1): false,
		"W10.e30.c2ln":                     false,
		base64.RawURLEncoding.EncodeToString([]byte("not json")) + ".e30.c2ln": false,
	} {
		if got := LooksLikeJWT(value); got != want {
			t.Errorf("LooksLikeJWT(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestRegistrationSSOIndexResolvesFromTheEnd(t *testing.T) {
	jwt := testRegistrationJWT("sso")
	workos := testRegistrationJWT("workos")
	const apiKey = "11111111-2222-4333-8444-555555555555"
	for _, test := range []struct {
		name  string
		line  string
		index int
		ok    bool
	}{
		{name: "too few fields", line: "a@example.com----pw"},
		{name: "minimal", line: "a@example.com----pw----sso", index: 2, ok: true},
		{name: "full layout", line: "a@example.com----pw----sso----wos----origin----user_01----" + apiKey, index: 2, ok: true},
		{name: "full layout with separator in password", line: "a@example.com----p----w----sso----wos----origin----user_01----" + apiKey, index: 3, ok: true},
		{name: "jwt sso with separator in password", line: "a@example.com----p----w----" + jwt, index: 3, ok: true},
		{name: "jwt sso with partial caches", line: "a@example.com----p----w----" + jwt + "----" + workos, index: 3, ok: true},
		{name: "jwt sso before jwt-like workos session", line: "a@example.com----p----w----" + jwt + "----" + workos + "----origin----user_01", index: 3, ok: true},
		{name: "undetermined", line: "a@example.com----p----w----sso"},
	} {
		t.Run(test.name, func(t *testing.T) {
			index, ok := RegistrationSSOIndex(strings.Split(test.line, "----"))
			if index != test.index || ok != test.ok {
				t.Fatalf("RegistrationSSOIndex() = %d, %v; want %d, %v", index, ok, test.index, test.ok)
			}
		})
	}
}
