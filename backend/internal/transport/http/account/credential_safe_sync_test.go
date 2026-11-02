package account

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	webprovider "github.com/chenyme/grok2api/backend/internal/infra/provider/web"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/gin-gonic/gin"
)

func TestSafeCredentialEndpointsCreateReplaceAndRejectDuplicates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	db, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "safe-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	repo := relational.NewAccountRepository(db)
	adapter := webprovider.NewAdapter(webprovider.Config{}, nil, cipher, nil, nil)
	service := accountapp.NewService(repo, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil)
	router := gin.New()
	NewHandler(service, nil).Register(router.Group("/api/admin/v1"))
	send := func(path, token, user string) *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(map[string]any{"provider": "grok_web", "email": "user@example.test", "userId": user, "document": map[string]any{"provider": "grok_web", "accounts": []any{map[string]any{"email": "user@example.test", "user_id": user, "sso_token": token}}}})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if strings.Contains(recorder.Body.String(), token) {
			t.Fatal("response exposed credential")
		}
		return recorder
	}
	first := send("/api/admin/v1/accounts/import-unique", "old-secret", "user-123")
	if first.Code != http.StatusOK {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	var result struct {
		Data struct {
			AccountID string `json:"accountId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil || result.Data.AccountID == "" {
		t.Fatalf("bad create result: %v", err)
	}
	duplicate := send("/api/admin/v1/accounts/import-unique", "different-secret", "user-123")
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate create: %d %s", duplicate.Code, duplicate.Body.String())
	}
	path := "/api/admin/v1/accounts/" + result.Data.AccountID + "/replace-credential"
	replacement := send(path, "new-secret", "user-123")
	if replacement.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", replacement.Code, replacement.Body.String())
	}
	var id uint64
	if _, err := fmt.Sscan(result.Data.AccountID, &id); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	token, err := cipher.Decrypt(stored.EncryptedAccessToken)
	if err != nil || token != "new-secret" {
		t.Fatal("replacement did not update the selected account")
	}
	wrong := send(path, "wrong-secret", "different-user")
	if wrong.Code != http.StatusConflict {
		t.Fatalf("wrong identity: %d %s", wrong.Code, wrong.Body.String())
	}
	stored, err = repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	token, err = cipher.Decrypt(stored.EncryptedAccessToken)
	if err != nil || token != "new-secret" {
		t.Fatal("rejected request changed the credential")
	}
}
