package account

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	cliprovider "github.com/chenyme/grok2api/backend/internal/infra/provider/cli"
	consoleprovider "github.com/chenyme/grok2api/backend/internal/infra/provider/console"
	webprovider "github.com/chenyme/grok2api/backend/internal/infra/provider/web"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestExportCredentialsRoundTripsImportFormat(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	accessToken, err := cipher.Encrypt("access-token")
	if err != nil {
		t.Fatal(err)
	}
	refreshToken, err := cipher.Encrypt("refresh-token")
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	repository := relational.NewAccountRepository(database)
	created, _, err := repository.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "primary", Email: "user@example.com", UserID: "user-1",
		SourceKey: "export-test", OIDCClientID: "client-1", EncryptedAccessToken: accessToken,
		EncryptedRefreshToken: refreshToken, ExpiresAt: expiresAt, Enabled: false,
		AuthStatus: accountdomain.AuthStatusActive, Priority: 1, MaxConcurrent: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := cliprovider.NewAdapter(cliprovider.Config{}, cipher)
	service := NewService(repository, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)

	result, err := service.ExportCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selectedResult, err := service.ExportProviderCredentialsByIDs(ctx, accountdomain.ProviderBuild, []uint64{created.ID})
	if err != nil || selectedResult.Count != 1 {
		t.Fatalf("selected export result = %#v, error = %v", selectedResult, err)
	}
	if _, err := service.ExportProviderCredentialsByIDs(ctx, accountdomain.ProviderWeb, []uint64{created.ID}); err == nil {
		t.Fatal("expected cross-provider selected export to fail")
	}
	if _, err := service.ExportProviderCredentialsCursor(ctx, accountdomain.ProviderBuild, 0, 0, maxCredentialExportAccounts+1); err == nil {
		t.Fatal("expected oversized export page to fail")
	}
	values, err := adapter.ParseImportedCredentials(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(values) != 1 {
		t.Fatalf("export count = %d, imported values = %d", result.Count, len(values))
	}
	value := values[0]
	if value.Name != "primary" || value.Email != "user@example.com" || value.UserID != "user-1" || value.OIDCClientID != "client-1" || value.AccessToken != "access-token" || value.RefreshToken != "refresh-token" || !value.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("round-trip credential = %#v", value)
	}
	progress := make([][2]int, 0, 2)
	if _, err := service.ImportCredentialsWithProgress(ctx, result.Data, nil, func(completed, total int) error {
		progress = append(progress, [2]int{completed, total})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 || progress[0] != [2]int{0, 1} || progress[1] != [2]int{1, 1} {
		t.Fatalf("import progress = %#v", progress)
	}

	multiProgress := make([][2]int, 0, 3)
	multiResult, err := service.ImportCredentialDocumentsWithProgress(ctx, [][]byte{
		result.Data,
		result.Data,
		[]byte(`{"provider":"grok_build","name":"secondary","access_token":"second-access","refresh_token":"second-refresh","user_id":"user-2"}`),
	}, nil, func(completed, total int) error {
		multiProgress = append(multiProgress, [2]int{completed, total})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if multiResult.Created != 1 || multiResult.Updated != 1 {
		t.Fatalf("multi-file import result = %#v", multiResult)
	}
	if len(multiProgress) != 3 || multiProgress[0] != [2]int{0, 2} || multiProgress[2] != [2]int{2, 2} {
		t.Fatalf("multi-file import progress = %#v", multiProgress)
	}
}

func TestExportProviderCredentialsCursorKeepsStableSnapshot(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "cursor-export.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	accessToken, err := cipher.Encrypt("access-token")
	if err != nil {
		t.Fatal(err)
	}
	repository := relational.NewAccountRepository(database)
	createAccount := func(name string) accountdomain.Credential {
		t.Helper()
		value, _, createErr := repository.UpsertByIdentity(ctx, accountdomain.Credential{
			Provider: accountdomain.ProviderBuild, Name: name, SourceKey: "cursor-" + name,
			UserID: name, EncryptedAccessToken: accessToken, Enabled: true,
			AuthStatus: accountdomain.AuthStatusActive, Priority: 1, MaxConcurrent: 8,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return value
	}
	first := createAccount("first")
	second := createAccount("second")
	third := createAccount("third")
	adapter := cliprovider.NewAdapter(cliprovider.Config{}, cipher)
	service := NewService(repository, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)

	pageOne, err := service.ExportProviderCredentialsCursor(ctx, accountdomain.ProviderBuild, 0, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if pageOne.Count != 2 || !pageOne.HasMore || pageOne.NextID != second.ID || pageOne.SnapshotMaxID != third.ID {
		t.Fatalf("first cursor page = %#v", pageOne)
	}
	createAccount("new-after-snapshot")
	pageTwo, err := service.ExportProviderCredentialsCursor(ctx, accountdomain.ProviderBuild, pageOne.NextID, pageOne.SnapshotMaxID, 2)
	if err != nil {
		t.Fatal(err)
	}
	values, err := adapter.ParseImportedCredentials(pageTwo.Data)
	if err != nil {
		t.Fatal(err)
	}
	if pageTwo.Count != 1 || pageTwo.HasMore || pageTwo.NextID != third.ID || len(values) != 1 || values[0].Name != "third" {
		t.Fatalf("second cursor page = %#v, values = %#v", pageTwo, values)
	}
	if first.ID >= second.ID || second.ID >= third.ID {
		t.Fatalf("test account IDs are not monotonic: %d, %d, %d", first.ID, second.ID, third.ID)
	}
	if _, err := service.ExportProviderCredentialsCursor(ctx, accountdomain.ProviderBuild, second.ID, 0, 2); err == nil {
		t.Fatal("expected continuation without snapshot boundary to fail")
	}
}

func TestValidateCredentialExportCountRejectsPartialReads(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected int
		total    int64
		actual   int
		wantErr  bool
	}{
		{name: "exact", expected: 2, total: 2, actual: 2},
		{name: "missing at count", expected: 2, total: 1, actual: 1, wantErr: true},
		{name: "deleted after count", expected: 2, total: 2, actual: 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateCredentialExportCount(test.expected, test.total, test.actual)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateCredentialExportCount() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestExportProviderCredentialsRoundTripsSSOProviders(t *testing.T) {
	for _, test := range []struct {
		name          string
		providerValue accountdomain.Provider
		adapter       provider.Adapter
		webTier       accountdomain.WebTier
	}{
		{name: "web", providerValue: accountdomain.ProviderWeb, adapter: webprovider.NewAdapter(webprovider.Config{}, nil, nil, nil, nil), webTier: accountdomain.WebTierSuper},
		{name: "console", providerValue: accountdomain.ProviderConsole, adapter: consoleprovider.NewAdapter(consoleprovider.Config{}, nil, nil, nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			nsfwAt := time.Date(2026, 7, 18, 8, 0, 0, 0, time.UTC)
			tosAt := nsfwAt.Add(-time.Hour)
			birthDateAt := tosAt.Add(-time.Hour)
			database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "export-sso.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := database.InitializeSchema(ctx); err != nil {
				t.Fatal(err)
			}
			cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
			if err != nil {
				t.Fatal(err)
			}
			token, err := cipher.Encrypt("sso-token")
			if err != nil {
				t.Fatal(err)
			}
			cookies, err := cipher.Encrypt("cf_clearance=clearance-token")
			if err != nil {
				t.Fatal(err)
			}
			repository := relational.NewAccountRepository(database)
			created, _, err := repository.UpsertByIdentity(ctx, accountdomain.Credential{
				Provider: test.providerValue, AuthType: accountdomain.AuthTypeSSO, WebTier: test.webTier,
				Name: test.name + "-account", SourceKey: test.name + "-export-test",
				EncryptedAccessToken: token, EncryptedCloudflareCookie: cookies,
				Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
				WebNSFWEnabledAt: &nsfwAt, WebTermsAcceptedAt: &tosAt,
				WebTermsAcceptedVersion: accountdomain.CurrentWebTermsVersion, WebBirthDateSetAt: &birthDateAt,
			})
			if err != nil {
				t.Fatal(err)
			}
			// 模拟旧账号先按 SSO 来源创建，后续身份同步再补齐邮箱与 user_id；
			// 回导必须命中原账号，不能因新身份字段生成重复记录。
			if err := repository.UpdateIdentityMetadata(ctx, created.ID, test.name+"@example.com", test.name+"-user-id", ""); err != nil {
				t.Fatal(err)
			}
			service := NewService(repository, nil, nil, nil, provider.NewRegistry(test.adapter), cipher, nil)

			result, err := service.ExportProviderCredentials(ctx, test.providerValue)
			if err != nil {
				t.Fatal(err)
			}
			codec, ok := test.adapter.(provider.CredentialCodecAdapter)
			if !ok {
				t.Fatal("adapter does not implement credential codec")
			}
			values, err := codec.ParseImportedCredentials(result.Data)
			if err != nil {
				t.Fatal(err)
			}
			if result.Count != 1 || len(values) != 1 || values[0].Provider != test.providerValue || values[0].AccessToken != "sso-token" || values[0].CloudflareCookies != "cf_clearance=clearance-token" || values[0].Email != test.name+"@example.com" || values[0].UserID != test.name+"-user-id" {
				t.Fatalf("round-trip result = %#v, values = %#v", result, values)
			}
			if test.providerValue == accountdomain.ProviderWeb && (values[0].WebTier != accountdomain.WebTierSuper || values[0].WebNSFWEnabledAt == nil || !values[0].WebNSFWEnabledAt.Equal(nsfwAt) || values[0].WebTermsAcceptedAt == nil || !values[0].WebTermsAcceptedAt.Equal(tosAt) || values[0].WebTermsAcceptedVersion != accountdomain.CurrentWebTermsVersion || values[0].WebBirthDateSetAt == nil || !values[0].WebBirthDateSetAt.Equal(birthDateAt)) {
				t.Fatalf("web metadata = %#v", values[0])
			}
			var imported ImportResult
			if test.providerValue == accountdomain.ProviderWeb {
				imported, err = service.ImportWebCredentialsWithProgress(ctx, result.Data, nil, nil)
			} else {
				imported, err = service.ImportConsoleCredentialsWithProgress(ctx, result.Data, nil, nil)
			}
			if err != nil || len(imported.AccountIDs) != 1 {
				t.Fatalf("reimport result = %#v, error = %v", imported, err)
			}
			stored, err := repository.Get(ctx, imported.AccountIDs[0])
			if err != nil || stored.Email != test.name+"@example.com" || stored.UserID != test.name+"-user-id" {
				t.Fatalf("reimported account = %#v, error = %v", stored, err)
			}
			if test.providerValue == accountdomain.ProviderWeb && (stored.WebNSFWEnabledAt == nil || !stored.WebNSFWEnabledAt.Equal(nsfwAt) || stored.WebTermsAcceptedAt == nil || !stored.WebTermsAcceptedAt.Equal(tosAt) || stored.WebTermsAcceptedVersion != accountdomain.CurrentWebTermsVersion || stored.WebBirthDateSetAt == nil || !stored.WebBirthDateSetAt.Equal(birthDateAt)) {
				t.Fatalf("reimported web metadata = %#v", stored)
			}
		})
	}
}

// newExportTestDatabase 打开临时 SQLite 并返回账号仓储与密文，避免各格式用例重复样板。
func newExportTestDatabase(t *testing.T, ctx context.Context) (*relational.AccountRepository, *security.Cipher) {
	t.Helper()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "export-formats.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return relational.NewAccountRepository(database), cipher
}

func newExportTestService(accounts *relational.AccountRepository, cipher *security.Cipher, adapters ...provider.Adapter) *Service {
	return NewService(accounts, nil, nil, nil, provider.NewRegistry(adapters...), cipher, nil)
}

func insertExportAccount(t *testing.T, ctx context.Context, accounts *relational.AccountRepository, value accountdomain.Credential) accountdomain.Credential {
	t.Helper()
	created, _, err := accounts.UpsertByIdentity(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func mustEncrypt(t *testing.T, cipher *security.Cipher, value string) string {
	t.Helper()
	encrypted, err := cipher.Encrypt(value)
	if err != nil {
		t.Fatal(err)
	}
	return encrypted
}

func TestParseExportFormatDefaultsToJSON(t *testing.T) {
	for _, test := range []struct {
		input string
		want  ExportFormat
		err   bool
	}{
		{input: "", want: ExportFormatJSON},
		{input: "json", want: ExportFormatJSON},
		{input: " JSON ", want: ExportFormatJSON},
		{input: "txt", want: ExportFormatText},
		{input: "CSV", want: ExportFormatCSV},
		{input: "xml", err: true},
	} {
		format, err := ParseExportFormat(test.input)
		if test.err {
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("format %q error = %v", test.input, err)
			}
			continue
		}
		if err != nil || format != test.want {
			t.Fatalf("format %q = %q, error = %v", test.input, format, err)
		}
	}
}

func TestExportFormattedTextCanBeImportedBack(t *testing.T) {
	ctx := context.Background()
	accounts, cipher := newExportTestDatabase(t, ctx)
	webAdapter := webprovider.NewAdapter(webprovider.Config{}, nil, nil, nil, nil)
	buildAdapter := cliprovider.NewAdapter(cliprovider.Config{}, cipher)
	service := newExportTestService(accounts, cipher, webAdapter, buildAdapter)

	webAccount := insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "web", Email: "web@example.com",
		SourceKey: "web-txt", WebTier: accountdomain.WebTierSuper,
		EncryptedAccessToken: mustEncrypt(t, cipher, "sso-token"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	file, err := service.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderWeb, []uint64{webAccount.ID}, ExportFormatText)
	if err != nil {
		t.Fatal(err)
	}
	if file.Count != 1 || file.Format != ExportFormatText || string(file.Data) != "sso-token\n" {
		t.Fatalf("web txt export = %#v", file)
	}
	seeds, err := webAdapter.ParseImportedCredentials(file.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 1 || seeds[0].AccessToken != "sso-token" || seeds[0].Provider != accountdomain.ProviderWeb {
		t.Fatalf("web txt reimport = %#v", seeds)
	}

	buildAccount := insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "build", Email: "build@example.com", SourceKey: "build-txt",
		OIDCClientID:         buildAdapter.DefaultOAuthClientID(),
		EncryptedAccessToken: mustEncrypt(t, cipher, "build-access"), EncryptedRefreshToken: mustEncrypt(t, cipher, "build-refresh"),
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	buildFile, err := service.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderBuild, []uint64{buildAccount.ID}, ExportFormatText)
	if err != nil {
		t.Fatal(err)
	}
	if string(buildFile.Data) != "build@example.com\tbuild-refresh\n" {
		t.Fatalf("build txt export = %q", buildFile.Data)
	}
	customClient := insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "build-custom", UserID: "custom-user", SourceKey: "build-txt-custom",
		OIDCClientID: "custom-client", EncryptedAccessToken: mustEncrypt(t, cipher, "custom-access"),
		EncryptedRefreshToken: mustEncrypt(t, cipher, "custom-refresh"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	customFile, err := service.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderBuild, []uint64{customClient.ID}, ExportFormatText)
	if err != nil {
		t.Fatal(err)
	}
	if string(customFile.Data) != "\tcustom-refresh\tcustom-client\n" {
		t.Fatalf("build txt custom client export = %q", customFile.Data)
	}
	buildSeeds, err := buildAdapter.ParseImportedCredentials(append(append([]byte{}, customFile.Data...), buildFile.Data...))
	if err != nil {
		t.Fatal(err)
	}
	if len(buildSeeds) != 2 || buildSeeds[0].RefreshToken != "custom-refresh" || buildSeeds[0].OIDCClientID != "custom-client" || buildSeeds[0].Email != "" ||
		buildSeeds[1].RefreshToken != "build-refresh" || buildSeeds[1].OIDCClientID != buildAdapter.DefaultOAuthClientID() || buildSeeds[1].Email != "build@example.com" {
		t.Fatalf("build txt reimport = %#v", buildSeeds)
	}

	// 只有 access token 的 build 账号无法按 refresh token 导入读回，txt 导出直接跳过。
	accessOnly := insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "build-fallback", Email: "fallback@example.com", SourceKey: "build-txt-fallback",
		EncryptedAccessToken: mustEncrypt(t, cipher, "build-fallback-access"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	accessOnlyFile, err := service.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderBuild, []uint64{accessOnly.ID}, ExportFormatText)
	if err != nil {
		t.Fatal(err)
	}
	if len(accessOnlyFile.Data) != 0 || accessOnlyFile.Count != 0 {
		t.Fatalf("build access-only txt export = %#v", accessOnlyFile)
	}
}

func TestExportFormattedCSVQuotesRFC4180Fields(t *testing.T) {
	ctx := context.Background()
	accounts, cipher := newExportTestDatabase(t, ctx)
	service := newExportTestService(accounts, cipher)
	created := insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "alpha, \"beta\"",
		Email: "quoted\"mail@example.com", UserID: "user-1", SourceKey: "web-csv",
		WebTier: accountdomain.WebTierSuper, EncryptedAccessToken: mustEncrypt(t, cipher, "sso-token"),
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	file, err := service.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderWeb, []uint64{created.ID}, ExportFormatCSV)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(file.Data)
	rows, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := []string{"id", "provider", "name", "email", "user_id", "tier", "status", "enabled", "token_type", "token", "created_at"}
	if len(rows) != 2 || !slices.Equal(rows[0], wantHeader) {
		t.Fatalf("csv rows = %#v", rows)
	}
	row := rows[1]
	if row[0] != strconv.FormatUint(created.ID, 10) || row[1] != string(accountdomain.ProviderWeb) || row[2] != "alpha, \"beta\"" ||
		row[3] != "quoted\"mail@example.com" || row[4] != "user-1" || row[5] != string(accountdomain.WebTierSuper) ||
		row[6] != string(accountdomain.AuthStatusActive) || row[7] != "true" || row[8] != "sso" || row[9] != "sso-token" {
		t.Fatalf("csv row = %#v", row)
	}
	if _, err := time.Parse(time.RFC3339, row[10]); err != nil {
		t.Fatalf("csv created_at %q: %v", row[10], err)
	}
	if !strings.Contains(raw, "\"alpha, \"\"beta\"\"\"") {
		t.Fatalf("csv name field not escaped: %s", raw)
	}
	if strings.Contains(strings.ReplaceAll(raw, "\r\n", ""), "\n") {
		t.Fatalf("csv must use CRLF line endings: %q", raw)
	}
	if !strings.HasSuffix(raw, "\r\n") {
		t.Fatalf("csv must end with CRLF: %q", raw)
	}
	if len(file.Data) != len(raw) || file.Count != 1 || file.Format != ExportFormatCSV {
		t.Fatalf("csv export = %#v", file)
	}
}

func TestExportFormattedCSVNeutralizesSpreadsheetFormulas(t *testing.T) {
	ctx := context.Background()
	accounts, cipher := newExportTestDatabase(t, ctx)
	service := newExportTestService(accounts, cipher)
	created := insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: `=HYPERLINK("http://evil.example","x")`,
		Email: "@mail.example", UserID: "-1+1", SourceKey: "web-csv-formula",
		EncryptedAccessToken: mustEncrypt(t, cipher, "+sso-token"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	file, err := service.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderWeb, []uint64{created.ID}, ExportFormatCSV)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(file.Data, []byte("\xef\xbb\xbf")) {
		t.Fatal("csv export must keep its plain header without a BOM")
	}
	rows, err := csv.NewReader(bytes.NewReader(file.Data)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("csv rows = %#v", rows)
	}
	row := rows[1]
	if row[2] != `'=HYPERLINK("http://evil.example","x")` || row[3] != "'@mail.example" || row[4] != "'-1+1" || row[9] != "'+sso-token" {
		t.Fatalf("formula cells were not neutralized: %#v", row)
	}
	if row[0] != strconv.FormatUint(created.ID, 10) || row[7] != "true" || row[8] != "sso" {
		t.Fatalf("plain cells changed: %#v", row)
	}
	for value, want := range map[string]string{"\tcmd": "'\tcmd", "\rcmd": "\"'\rcmd\"", "safe": "safe", "": ""} {
		if got := csvField(value); got != want {
			t.Fatalf("csvField(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestExportFormattedJSONMatchesLegacyBytes(t *testing.T) {
	ctx := context.Background()
	accounts, cipher := newExportTestDatabase(t, ctx)
	service := newExportTestService(accounts, cipher, cliprovider.NewAdapter(cliprovider.Config{}, cipher))
	created := insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "primary", Email: "user@example.com", UserID: "user-1", SourceKey: "build-json",
		OIDCClientID: "client-1", EncryptedAccessToken: mustEncrypt(t, cipher, "access-token"),
		EncryptedRefreshToken: mustEncrypt(t, cipher, "refresh-token"),
		ExpiresAt:             time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	legacy, err := service.ExportProviderCredentialsByIDs(ctx, accountdomain.ProviderBuild, []uint64{created.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []ExportFormat{ExportFormatJSON, ""} {
		file, err := service.ExportProviderCredentialsByIDsFormatted(ctx, accountdomain.ProviderBuild, []uint64{created.ID}, format)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(file.Data, legacy.Data) || file.Count != legacy.Count || file.Format != ExportFormatJSON {
			t.Fatalf("format %q changed json bytes: got %q, want %q", format, file.Data, legacy.Data)
		}
	}
}

func TestExportAllProvidersArchiveGroupsByProvider(t *testing.T) {
	ctx := context.Background()
	accounts, cipher := newExportTestDatabase(t, ctx)
	service := newExportTestService(accounts, cipher,
		cliprovider.NewAdapter(cliprovider.Config{}, cipher),
		webprovider.NewAdapter(webprovider.Config{}, nil, nil, nil, nil),
		consoleprovider.NewAdapter(consoleprovider.Config{}, nil, nil, nil),
	)
	insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "web-one", SourceKey: "archive-web-1",
		EncryptedAccessToken: mustEncrypt(t, cipher, "web-token-one"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "web-two", SourceKey: "archive-web-2",
		EncryptedAccessToken: mustEncrypt(t, cipher, "web-token-two"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, Name: "build-one", Email: "build@example.com", SourceKey: "archive-build",
		EncryptedAccessToken: mustEncrypt(t, cipher, "build-access"), EncryptedRefreshToken: mustEncrypt(t, cipher, "build-refresh"),
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	insertExportAccount(t, ctx, accounts, accountdomain.Credential{
		Provider: accountdomain.ProviderConsole, AuthType: accountdomain.AuthTypeSSO, Name: "console-one", SourceKey: "archive-console",
		EncryptedAccessToken: mustEncrypt(t, cipher, "console-token"), Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})

	exports, err := service.ExportAllProvidersArchive(ctx, ExportFormatText)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 3 {
		t.Fatalf("archive sources = %#v", exports)
	}
	byProvider := make(map[accountdomain.Provider]ProviderExport, len(exports))
	for _, export := range exports {
		byProvider[export.Provider] = export
	}
	wantLines := map[accountdomain.Provider]string{
		accountdomain.ProviderBuild:   "build@example.com\tbuild-refresh\n",
		accountdomain.ProviderWeb:     "web-token-one\nweb-token-two\n",
		accountdomain.ProviderConsole: "console-token\n",
	}
	wantCounts := map[accountdomain.Provider]int{accountdomain.ProviderBuild: 1, accountdomain.ProviderWeb: 2, accountdomain.ProviderConsole: 1}
	for providerValue, wantLine := range wantLines {
		export, ok := byProvider[providerValue]
		if !ok {
			t.Fatalf("missing archive source %s", providerValue)
		}
		if export.Count != wantCounts[providerValue] || string(export.Data) != wantLine || export.Format != ExportFormatText {
			t.Fatalf("archive source %s = %#v", providerValue, export)
		}
	}
}

// oversizedExportRepository 只实现 List，用来验证导出上限在读取后的判定。
type oversizedExportRepository struct {
	repository.AccountRepository
	values []accountdomain.Credential
	total  int64
}

func (r oversizedExportRepository) List(context.Context, repository.AccountListQuery) ([]accountdomain.Credential, int64, error) {
	return r.values, r.total, nil
}

func TestExportLimitsAskForNarrowerSelection(t *testing.T) {
	ctx := context.Background()
	service := NewService(oversizedExportRepository{total: maxCredentialExportAccounts + 1}, nil, nil, nil, nil, nil, nil)
	if _, err := service.ListIDs(ctx, "", ListFilter{}); !errors.Is(err, ErrExportLimit) {
		t.Fatalf("list ids error = %v", err)
	}
	if _, err := service.ExportAllProvidersArchive(ctx, ExportFormatJSON); !errors.Is(err, ErrExportLimit) {
		t.Fatalf("archive error = %v", err)
	}
}
