package web

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestRegistrationAccountTextAllowsSeparatorInPassword(t *testing.T) {
	encode := base64.RawURLEncoding.EncodeToString
	jwt := encode([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + encode([]byte(`{"session_id":"web"}`)) + "." + encode([]byte("signature"))
	for _, test := range []struct {
		name string
		line string
		want string
	}{
		{name: "plain sso is the last field", line: "a@example.com----pa----ss----word----sso-value", want: "sso-value"},
		{name: "jwt sso", line: "a@example.com----pa----ss----" + jwt, want: jwt},
		{name: "cookie copied sso", line: "a@example.com----pa----ss----sso=" + jwt + "; Path=/", want: jwt},
		{name: "console caches after jwt sso", line: "a@example.com----pa----ss----" + jwt + "----wos----origin----user_01", want: jwt},
		{name: "console caches with api key", line: "a@example.com----pa----ss----sso-value----wos----origin----user_01----11111111-2222-4333-8444-555555555555", want: "sso-value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			seeds, err := parsePlainTextCredentials(test.line)
			if err != nil || len(seeds) != 1 || seeds[0].AccessToken != test.want || seeds[0].Email != "a@example.com" {
				t.Fatalf("unexpected import: %#v, %v", seeds, err)
			}
			if dump := fmt.Sprintf("%#v", seeds[0]); strings.Contains(dump, "pa----ss") || strings.Contains(dump, `"ss"`) {
				t.Fatalf("password fragment leaked into the seed: %s", dump)
			}
		})
	}
	if _, err := parsePlainTextCredentials("a@example.com----pa----ss----"); err == nil {
		t.Fatal("line with an empty trailing SSO must be rejected")
	}
}
