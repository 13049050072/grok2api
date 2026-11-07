package cli

import (
	"context"
	"encoding/base64"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

func TestParseImportedCredentialsTabSeparatedLines(t *testing.T) {
	data := "\ufeff\trt-no-email\tcustom-client\n" +
		"user@example.com\trt-one\r\n" +
		"user@example.com\trefresh_token=rt-two\t\n" +
		"  rt=rt-bare  \n"
	values, err := parseImportedCredentials([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 4 {
		t.Fatalf("values = %#v", values)
	}
	if values[0].Email != "" || values[0].RefreshToken != "rt-no-email" || values[0].OIDCClientID != "custom-client" {
		t.Fatalf("line without email = %#v", values[0])
	}
	if values[1].Email != "user@example.com" || values[1].Name != "user@example.com" || values[1].RefreshToken != "rt-one" || values[1].OIDCClientID != defaultOAuthClientID {
		t.Fatalf("email line = %#v", values[1])
	}
	if values[2].Email != "user@example.com" || values[2].RefreshToken != "rt-two" || values[3].RefreshToken != "rt-bare" || values[3].Email != "" {
		t.Fatalf("prefixed lines = %#v, %#v", values[2], values[3])
	}
	// Lines sharing an email may hold different teams' tokens; only the exchange can tell them apart.
	if values[1].SourceKey == values[2].SourceKey {
		t.Fatal("refresh tokens sharing an email collapsed into one source key")
	}
	bare, err := parseImportedCredentials([]byte("rt-one\n"))
	if err != nil {
		t.Fatal(err)
	}
	if bare[0].SourceKey != values[1].SourceKey {
		t.Fatal("email column changed the refresh token source key")
	}
}

func TestParseImportedCredentialsTabSeparatedLinesRejectsInvalidFields(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "too many columns", data: "rt-one\nuser@example.com\trt\tclient\textra\n", want: "第 2 行"},
		{name: "empty token", data: "user@example.com\t\tclient\n", want: "refresh token 不能为空"},
		{name: "email whitespace", data: "user example.com\trt\n", want: "邮箱不能包含空白字符"},
		{name: "client whitespace", data: "user@example.com\trt\tclient id\n", want: "client id不能包含空白字符"},
		{name: "oversized email", data: strings.Repeat("e", maxImportedTextFieldBytes+1) + "\trt\n", want: "邮箱长度"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseImportedCredentials([]byte(test.data)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBuildTextExportImportsBack(t *testing.T) {
	ctx := context.Background()
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	claims := func(value string) string {
		return "header." + base64.RawURLEncoding.EncodeToString([]byte(value)) + ".signature"
	}
	exchanges := map[string]struct {
		clientID string
		response string
	}{
		"rt-a": {clientID: defaultOAuthClientID, response: `{"access_token":"access-a2","refresh_token":"rt-a2","expires_in":3600,"id_token":"` + claims(`{"sub":"user-a","email":"a@example.com"}`) + `"}`},
		"rt-b": {clientID: "custom-client", response: `{"access_token":"access-b2","refresh_token":"rt-b2","expires_in":3600,"id_token":"` + claims(`{"sub":"user-b"}`) + `"}`},
	}
	adapter := NewAdapter(Config{}, cipher)
	adapter.oauth.http = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return oauthResponse(http.StatusBadRequest, `{"error":"invalid_request"}`), nil
		}
		exchange, ok := exchanges[request.Form.Get("refresh_token")]
		if !ok || request.Form.Get("client_id") != exchange.clientID {
			t.Errorf("unexpected exchange form = %#v", request.Form)
			return oauthResponse(http.StatusBadRequest, `{"error":"invalid_grant"}`), nil
		}
		return oauthResponse(http.StatusOK, exchange.response), nil
	})}
	openService := func(name string) (*accountapp.Service, *relational.AccountRepository) {
		t.Helper()
		database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := database.InitializeSchema(ctx); err != nil {
			t.Fatal(err)
		}
		accounts := relational.NewAccountRepository(database)
		return accountapp.NewService(accounts, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil), accounts
	}
	encrypt := func(value string) string {
		t.Helper()
		encrypted, err := cipher.Encrypt(value)
		if err != nil {
			t.Fatal(err)
		}
		return encrypted
	}

	source, sourceAccounts := openService("txt-export.db")
	ids := make([]uint64, 0, 3)
	for _, value := range []accountdomain.Credential{
		{Name: "a", Email: "a@example.com", UserID: "user-a", SourceKey: "txt-a", OIDCClientID: defaultOAuthClientID, EncryptedAccessToken: encrypt("access-a"), EncryptedRefreshToken: encrypt("rt-a")},
		{Name: "b", UserID: "user-b", SourceKey: "txt-b", OIDCClientID: "custom-client", EncryptedAccessToken: encrypt("access-b"), EncryptedRefreshToken: encrypt("rt-b")},
		{Name: "access-only", Email: "c@example.com", UserID: "user-c", SourceKey: "txt-c", EncryptedAccessToken: encrypt("access-c")},
	} {
		value.Provider, value.AuthType, value.Enabled, value.AuthStatus = accountdomain.ProviderBuild, accountdomain.AuthTypeOAuth, true, accountdomain.AuthStatusActive
		value.ExpiresAt = time.Now().UTC().Add(time.Hour)
		created, _, err := sourceAccounts.UpsertByIdentity(ctx, value)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	file, err := source.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderBuild, ids, accountapp.ExportFormatText)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(file.Data), "\n"), "\n")
	slices.Sort(lines)
	if file.Count != 2 || !slices.Equal(lines, []string{"\trt-b\tcustom-client", "a@example.com\trt-a"}) {
		t.Fatalf("txt export count = %d, lines = %q", file.Count, lines)
	}

	target, targetAccounts := openService("txt-import.db")
	result, err := target.ImportCredentialsWithProgress(ctx, file.Data, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 2 || result.Failed != 0 || result.Skipped != 0 {
		t.Fatalf("import result = %#v", result)
	}
	stored, err := targetAccounts.ListEnabled(ctx, accountdomain.ProviderBuild)
	if err != nil {
		t.Fatal(err)
	}
	byUser := make(map[string]accountdomain.Credential, len(stored))
	for _, value := range stored {
		byUser[value.UserID] = value
	}
	for userID, want := range map[string]struct{ email, clientID, refreshToken string }{
		"user-a": {email: "a@example.com", clientID: defaultOAuthClientID, refreshToken: "rt-a2"},
		"user-b": {email: "", clientID: "custom-client", refreshToken: "rt-b2"},
	} {
		value, ok := byUser[userID]
		if !ok {
			t.Fatalf("missing imported account %s in %#v", userID, stored)
		}
		refreshToken, err := cipher.Decrypt(value.EncryptedRefreshToken)
		if err != nil {
			t.Fatal(err)
		}
		if value.Email != want.email || value.OIDCClientID != want.clientID || refreshToken != want.refreshToken {
			t.Fatalf("imported %s = %#v, refresh = %q", userID, value, refreshToken)
		}
	}
}
