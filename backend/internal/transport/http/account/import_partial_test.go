package account

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	accountsyncapp "github.com/chenyme/grok2api/backend/internal/application/accountsync"
	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
	"github.com/gin-gonic/gin"
)

type chunkImportHTTPAdapter struct {
	count    int
	parseErr error
}

func (chunkImportHTTPAdapter) Provider() accountdomain.Provider { return accountdomain.ProviderBuild }

func (chunkImportHTTPAdapter) Definition() provider.Definition {
	return provider.Definition{
		Provider:       accountdomain.ProviderBuild,
		ModelNamespace: accountdomain.ProviderBuild.ModelNamespace(),
		Credential:     provider.CredentialSurface{AuthType: accountdomain.AuthTypeOAuth, Import: true},
	}
}

func (a chunkImportHTTPAdapter) ParseImportedCredentials([]byte) ([]provider.CredentialSeed, error) {
	if a.parseErr != nil {
		return nil, a.parseErr
	}
	seeds := make([]provider.CredentialSeed, 0, a.count)
	for index := 1; index <= a.count; index++ {
		seeds = append(seeds, provider.CredentialSeed{
			Provider: accountdomain.ProviderBuild, AuthType: accountdomain.AuthTypeOAuth, Name: fmt.Sprintf("chunk-%d", index),
			SourceKey: fmt.Sprintf("chunk-%d", index), AccessToken: fmt.Sprintf("access-%d", index),
		})
	}
	return seeds, nil
}

func (chunkImportHTTPAdapter) MarshalCredentials([]provider.CredentialSeed) ([]byte, error) {
	return nil, nil
}

type failingChunkImportRepository struct {
	*relational.AccountRepository
	calls  atomic.Int32
	failAt int32
}

func (r *failingChunkImportRepository) UpsertImportedCredentialsSafely(ctx context.Context, values []accountdomain.Credential) ([]repository.AccountUpsertResult, error) {
	if r.calls.Add(1) == r.failAt {
		return nil, errors.New("database is locked")
	}
	return r.AccountRepository.UpsertImportedCredentialsSafely(ctx, values)
}

// cancelAwareSynchronizerStub reports whether the pipeline was aborted before the account stream ended.
type cancelAwareSynchronizerStub struct {
	mu        sync.Mutex
	synced    int
	cancelled bool
}

func (s *cancelAwareSynchronizerStub) Sync(_ context.Context, accountIDs ...uint64) accountsyncapp.Result {
	return accountsyncapp.Result{Succeeded: len(accountIDs)}
}

func (s *cancelAwareSynchronizerStub) SyncStream(ctx context.Context, accountIDs <-chan uint64) accountsyncapp.Result {
	count := 0
	for range accountIDs {
		count++
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		s.cancelled = true
		return accountsyncapp.Result{Failed: count}
	}
	s.synced = count
	return accountsyncapp.Result{Succeeded: count}
}

func (s *cancelAwareSynchronizerStub) state() (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.synced, s.cancelled
}

type importErrorEvent struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Created int    `json:"created"`
	Synced  int    `json:"synced"`
}

func postChunkImport(t *testing.T, adapter chunkImportHTTPAdapter, failAt int32, syncer accountSynchronizer) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "chunk-import-http.db"))
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
	repo := &failingChunkImportRepository{AccountRepository: relational.NewAccountRepository(database), failAt: failAt}
	handler := NewHandler(accountapp.NewService(repo, nil, nil, nil, provider.NewRegistry(adapter), cipher, nil), syncer)
	router := gin.New()
	handler.Register(router.Group("/api/admin/v1"))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("ignored")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/admin/v1/accounts/import", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func decodeImportErrorEvent(t *testing.T, body string) importErrorEvent {
	t.Helper()
	_, payload, ok := strings.Cut(body, "event: error\ndata: ")
	if !ok {
		t.Fatalf("missing error event: %s", body)
	}
	payload, _, _ = strings.Cut(payload, "\n")
	var event importErrorEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatalf("error payload %q: %v", payload, err)
	}
	return event
}

func TestImportStreamKeepsSyncingPersistedAccountsAfterLaterChunkFails(t *testing.T) {
	syncer := &cancelAwareSynchronizerStub{}
	body := postChunkImport(t, chunkImportHTTPAdapter{count: 150}, 2, syncer)

	event := decodeImportErrorEvent(t, body)
	if event.Code != "authImportInterrupted" || event.Created != 100 || event.Synced != 100 || !strings.Contains(event.Message, "已新增 100") {
		t.Fatalf("error event = %#v", event)
	}
	if synced, cancelled := syncer.state(); cancelled || synced != 100 {
		t.Fatalf("initial sync for persisted accounts: cancelled=%v synced=%d", cancelled, synced)
	}
}

func TestImportStreamSurfacesInvalidFileReason(t *testing.T) {
	body := postChunkImport(t, chunkImportHTTPAdapter{parseErr: errors.New("第 3 行: 邮箱不能包含空白字符")}, 0, &cancelAwareSynchronizerStub{})

	event := decodeImportErrorEvent(t, body)
	if event.Code != "authImportInvalid" || !strings.Contains(event.Message, "第 3 行: 邮箱不能包含空白字符") || event.Created != 0 {
		t.Fatalf("error event = %#v", event)
	}
}
