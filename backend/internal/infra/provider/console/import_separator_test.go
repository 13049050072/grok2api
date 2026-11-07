package console

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestConsoleImportSeparatedLineAllowsSeparatorInPassword(t *testing.T) {
	t.Parallel()
	encode := base64.RawURLEncoding.EncodeToString
	jwt := func(subject string) string {
		return encode([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + encode([]byte(`{"session_id":"`+subject+`"}`)) + "." + encode([]byte("signature-"+subject))
	}
	minimalSSO, workosSSO := jwt("minimal"), jwt("workos")
	const password = "Secr3t----Passw0rd"
	full := "a@example.com----" + password + "----sso-full----wos-token----wos-origin----user_01----" + strings.ToUpper(testNewKeyID)
	minimal := "b@example.com----" + password + "----" + minimalSSO
	withoutKey := "c@example.com----" + password + "----" + workosSSO + "----wos-token----wos-origin----user_01"
	seeds, err := parseImportedCredentials([]byte(full + "\n" + minimal + "\n" + withoutKey))
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 3 {
		t.Fatalf("seeds = %d, want 3", len(seeds))
	}
	wantSession := workosSession{SessionToken: "wos-token", XAIOrigin: "wos-origin", WorkOSID: "user_01"}
	var session workosSession
	if seeds[0].AccessToken != "sso-full" || seeds[0].Email != "a@example.com" || seeds[0].ConsoleAPIKeyID != testNewKeyID {
		t.Fatalf("full seed = %#v", seeds[0])
	}
	if err := json.Unmarshal([]byte(seeds[0].ConsoleWorkOSSession), &session); err != nil || session != wantSession {
		t.Fatalf("full workos session = %q, %v", seeds[0].ConsoleWorkOSSession, err)
	}
	if seeds[1].AccessToken != minimalSSO || seeds[1].ConsoleAPIKeyID != "" || seeds[1].ConsoleWorkOSSession != "" {
		t.Fatalf("minimal seed = %#v", seeds[1])
	}
	session = workosSession{}
	if seeds[2].AccessToken != workosSSO || seeds[2].ConsoleAPIKeyID != "" {
		t.Fatalf("seed without api key = %#v", seeds[2])
	}
	if err := json.Unmarshal([]byte(seeds[2].ConsoleWorkOSSession), &session); err != nil || session != wantSession {
		t.Fatalf("workos session without api key = %q, %v", seeds[2].ConsoleWorkOSSession, err)
	}
	for _, seed := range seeds {
		if dump := fmt.Sprintf("%#v", seed); strings.Contains(dump, "Secr3t") || strings.Contains(dump, "Passw0rd") {
			t.Fatalf("password leaked into a credential seed: %s", dump)
		}
	}

	ambiguous := "d@example.com----a----b----c----d----sso-x----wos----origin----user_01"
	if _, err := parseImportedCredentials([]byte(ambiguous)); err == nil || !strings.Contains(err.Error(), "无法确定 sso") {
		t.Fatalf("ambiguous line error = %v", err)
	}
}
