package web

import "testing"

func TestRegistrationAccountText(t *testing.T) {
	seeds, err := parsePlainTextCredentials("a@example.com----password----sso-value\n")
	if err != nil || len(seeds) != 1 || seeds[0].Email != "a@example.com" || seeds[0].AccessToken != "sso-value" {
		t.Fatalf("unexpected import: %#v, %v", seeds, err)
	}
	if _, err := parsePlainTextCredentials("a@example.com----password"); err == nil {
		t.Fatal("password-only record must not be treated as SSO")
	}
}
