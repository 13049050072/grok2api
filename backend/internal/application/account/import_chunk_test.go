package account

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

type chunkImportAdapter struct {
	seeds []provider.CredentialSeed
}

func (chunkImportAdapter) Provider() accountdomain.Provider { return accountdomain.ProviderBuild }

func (chunkImportAdapter) Definition() provider.Definition {
	return provider.Definition{
		Provider:       accountdomain.ProviderBuild,
		ModelNamespace: accountdomain.ProviderBuild.ModelNamespace(),
		Credential:     provider.CredentialSurface{AuthType: accountdomain.AuthTypeOAuth, Import: true},
	}
}

func (a chunkImportAdapter) ParseImportedCredentials([]byte) ([]provider.CredentialSeed, error) {
	return a.seeds, nil
}

func (chunkImportAdapter) MarshalCredentials([]provider.CredentialSeed) ([]byte, error) {
	return nil, nil
}

// failingChunkRepository fails one safe-upsert call to simulate a database error in a later chunk.
type failingChunkRepository struct {
	*relational.AccountRepository
	calls  atomic.Int32
	failAt int32
}

func (r *failingChunkRepository) UpsertImportedCredentialsSafely(ctx context.Context, values []accountdomain.Credential) ([]repository.AccountUpsertResult, error) {
	if r.calls.Add(1) == r.failAt {
		return nil, errors.New("database is locked")
	}
	return r.AccountRepository.UpsertImportedCredentialsSafely(ctx, values)
}

func newChunkImportSeeds(count int) []provider.CredentialSeed {
	seeds := make([]provider.CredentialSeed, 0, count)
	for index := 1; index <= count; index++ {
		seeds = append(seeds, provider.CredentialSeed{
			Provider: accountdomain.ProviderBuild, AuthType: accountdomain.AuthTypeOAuth, Name: fmt.Sprintf("chunk-%d", index),
			SourceKey: fmt.Sprintf("chunk-%d", index), AccessToken: fmt.Sprintf("access-%d", index),
			Email: fmt.Sprintf("user-%d@example.com", index), UserID: fmt.Sprintf("user-%d", index),
		})
	}
	return seeds
}

func newChunkImportRepository(t *testing.T) (*relational.AccountRepository, *security.Cipher) {
	t.Helper()
	database, err := relational.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "chunk-import.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return relational.NewAccountRepository(database), cipher
}

func TestImportRetriesConflictingChunkItemByItem(t *testing.T) {
	ctx := context.Background()
	repo, cipher := newChunkImportRepository(t)
	existing, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderBuild, AuthType: accountdomain.AuthTypeOAuth, Name: "existing", SourceKey: "existing",
		EncryptedAccessToken: "existing-access", Email: "user-120@example.com", UserID: "someone-else",
		Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	seeds := newChunkImportSeeds(150)
	seeds[4].CloudflareCookies = "session=not-cloudflare"
	service := NewService(repo, nil, nil, nil, provider.NewRegistry(chunkImportAdapter{seeds: seeds}), cipher, nil)
	var observed []uint64
	var lastProgress [2]int
	result, err := service.ImportCredentialsWithProgress(ctx, []byte("ignored"), func(id uint64) error {
		observed = append(observed, id)
		return nil
	}, func(completed, total int) error {
		lastProgress = [2]int{completed, total}
		return nil
	})
	if err != nil {
		t.Fatalf("one conflicting account must not fail the import: %v", err)
	}
	if result.Created != 148 || result.Failed != 2 || len(result.AccountIDs) != 148 || len(observed) != 148 {
		t.Fatalf("result = created %d failed %d ids %d observed %d", result.Created, result.Failed, len(result.AccountIDs), len(observed))
	}
	want := []ImportFailure{{Index: 5, Reason: "Cloudflare Cookie 中没有有效字段"}, {Index: 120, Reason: "已有账号的邮箱、用户 ID 或团队不一致"}}
	if len(result.Failures) != len(want) || result.Failures[0] != want[0] || result.Failures[1] != want[1] {
		t.Fatalf("failures = %#v", result.Failures)
	}
	if lastProgress != [2]int{150, 150} {
		t.Fatalf("last progress = %v", lastProgress)
	}
	stored, err := repo.Get(ctx, existing.ID)
	if err != nil || stored.UserID != "someone-else" || stored.EncryptedAccessToken != "existing-access" {
		t.Fatalf("conflicting account was modified: %#v, %v", stored, err)
	}
}

func TestImportReturnsPartialResultWhenLaterChunkFails(t *testing.T) {
	ctx := context.Background()
	base, cipher := newChunkImportRepository(t)
	repo := &failingChunkRepository{AccountRepository: base, failAt: 2}
	service := NewService(repo, nil, nil, nil, provider.NewRegistry(chunkImportAdapter{seeds: newChunkImportSeeds(150)}), cipher, nil)

	result, err := service.ImportCredentials(ctx, []byte("ignored"))
	if err == nil {
		t.Fatal("later chunk failure was swallowed")
	}
	if result.Created != 100 || len(result.AccountIDs) != 100 {
		t.Fatalf("persisted first chunk must be reported: %#v", result)
	}
	values, err := base.ListEnabled(ctx, accountdomain.ProviderBuild)
	if err != nil || len(values) != 100 {
		t.Fatalf("stored accounts = %d, %v", len(values), err)
	}
}
