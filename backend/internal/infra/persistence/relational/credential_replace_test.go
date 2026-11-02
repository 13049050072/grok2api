package relational

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestReplaceCredentialIfUniquePreservesIdentityAndRejectsConflicts(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	repo := NewAccountRepository(db)
	original := account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "old", Email: "user@example.test", UserID: "user-123", SourceKey: "old-source", EncryptedAccessToken: "old-token", EncryptedCloudflareCookie: "old-cookie", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 3, MaxConcurrent: 8}
	saved, created, err := repo.UpsertByIdentity(ctx, original)
	if err != nil || !created {
		t.Fatalf("setup failed: %v", err)
	}
	build := account.Credential{Provider: account.ProviderBuild, AuthType: account.AuthTypeOAuth, Name: "linked-build", Email: "user@example.test", UserID: "user-123", SourceKey: "build-source", EncryptedAccessToken: "build-token", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8}
	linked, _, err := repo.UpsertByIdentity(ctx, build)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkWebToBuild(ctx, saved.ID, linked.ID); err != nil {
		t.Fatal(err)
	}
	var before accountModel
	if err := db.db.First(&before, saved.ID).Error; err != nil {
		t.Fatal(err)
	}
	replacement := account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Email: "user@example.test", UserID: "user-123", SourceKey: "new-source", EncryptedAccessToken: "new-token"}
	if err := repo.ReplaceCredentialIfUnique(ctx, saved.ID, "user@example.test", "user-123", "", replacement); err != nil {
		t.Fatal(err)
	}
	var after accountModel
	if err := db.db.First(&after, saved.ID).Error; err != nil {
		t.Fatal(err)
	}
	current, err := repo.Get(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.IdentityKey != before.IdentityKey || after.SourceKey != "new-source" || current.EncryptedAccessToken != "new-token" || current.EncryptedCloudflareCookie != "old-cookie" || current.Priority != 3 {
		t.Fatal("credential replacement changed unrelated identity or settings")
	}
	var linkCount int64
	if err := db.db.Model(&accountProviderLinkModel{}).Where("web_account_id = ? AND build_account_id = ?", saved.ID, linked.ID).Count(&linkCount).Error; err != nil || linkCount != 1 {
		t.Fatal("replacement lost the existing Build association")
	}
	replacement.SourceKey = "another-source"
	if err := repo.ReplaceCredentialIfUnique(ctx, saved.ID, "user@example.test", "wrong-user", "", replacement); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("wrong identity: %v", err)
	}
	duplicate := original
	duplicate.UserID = "different-user"
	duplicate.SourceKey = "duplicate-source"
	if _, _, err := repo.UpsertByIdentity(ctx, duplicate); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceCredentialIfUnique(ctx, saved.ID, "user@example.test", "user-123", "", replacement); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate email: %v", err)
	}
	current, err = repo.Get(ctx, saved.ID)
	if err != nil || current.EncryptedAccessToken != "new-token" {
		t.Fatal("rejected replacement changed credentials")
	}
}

func TestCreateCredentialIfAbsentConcurrentRequestsDoNotDuplicate(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	repo := NewAccountRepository(db)
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	start := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			value := account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "concurrent", Email: "same@example.test", UserID: fmt.Sprintf("user-%d", index), SourceKey: fmt.Sprintf("source-%d", index), EncryptedAccessToken: "token", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8}
			if _, err := repo.CreateCredentialIfAbsent(ctx, value.Email, value.UserID, "", value); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	var count int64
	if err := db.db.Model(&accountModel{}).Where("provider = ? AND email = ?", string(account.ProviderWeb), "same@example.test").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || count != 1 {
		t.Fatalf("created %d records, successful requests %d", count, succeeded)
	}
}

func TestCreateCredentialIfAbsentRejectsDuplicatesAndUnknownAccounts(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	repo := NewAccountRepository(db)
	value := account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "first", Email: "user@example.test", UserID: "user-123", SourceKey: "source-1", EncryptedAccessToken: "token-1", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8}
	id, err := repo.CreateCredentialIfAbsent(ctx, value.Email, value.UserID, "", value)
	if err != nil || id == 0 {
		t.Fatalf("create failed: %v", err)
	}
	second := value
	second.UserID = "another-user"
	second.SourceKey = "source-2"
	if _, err := repo.CreateCredentialIfAbsent(ctx, value.Email, second.UserID, "", second); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("same email: %v", err)
	}
	second.Email = "other@example.test"
	second.UserID = value.UserID
	if _, err := repo.CreateCredentialIfAbsent(ctx, second.Email, second.UserID, "", second); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("same user id: %v", err)
	}
	second.UserID = "another-user"
	second.SourceKey = value.SourceKey
	if _, err := repo.CreateCredentialIfAbsent(ctx, second.Email, second.UserID, "", second); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("same source: %v", err)
	}
	anonymous := value
	anonymous.Email = ""
	anonymous.UserID = ""
	anonymous.SourceKey = "anonymous-source"
	if _, _, err := repo.UpsertByIdentity(ctx, anonymous); err != nil {
		t.Fatal(err)
	}
	second.SourceKey = "source-2"
	if _, err := repo.CreateCredentialIfAbsent(ctx, second.Email, second.UserID, "", second); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("anonymous account: %v", err)
	}
}

func TestRefreshCannotOverwriteReplacedCredential(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	repo := NewAccountRepository(db)
	old := account.Credential{Provider: account.ProviderBuild, AuthType: account.AuthTypeOAuth, Name: "account", Email: "user@example.test", UserID: "user-123", SourceKey: "old", EncryptedAccessToken: "old-access", EncryptedRefreshToken: "old-refresh", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8}
	saved, _, err := repo.UpsertByIdentity(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	replacement := old
	replacement.SourceKey = "new"
	replacement.EncryptedAccessToken = "new-access"
	replacement.EncryptedRefreshToken = "new-refresh"
	if err := repo.ReplaceCredentialIfUnique(ctx, saved.ID, old.Email, old.UserID, "", replacement); err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateTokensIfUnchanged(ctx, saved.ID, "old-access", "old-refresh", "stale-access", "stale-refresh", time.Now().Add(time.Hour), 0)
	if !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	current, err := repo.Get(ctx, saved.ID)
	if err != nil || current.EncryptedAccessToken != "new-access" || current.EncryptedRefreshToken != "new-refresh" {
		t.Fatalf("replacement was overwritten: %v", err)
	}
	failure := repository.CredentialRefreshFailure{RetryAt: time.Now(), Count: 1, Permanent: true, Code: "invalid_grant"}
	if err := repo.RecordRefreshFailureIfUnchanged(ctx, saved.ID, "old-access", "old-refresh", failure, "rejected"); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("stale failure should be rejected: %v", err)
	}
	current, err = repo.Get(ctx, saved.ID)
	if err != nil || current.AuthStatus != account.AuthStatusActive || current.RefreshPermanent {
		t.Fatalf("stale failure changed status: %v", err)
	}
	if err := repo.RecordRefreshFailureIfUnchanged(ctx, saved.ID, "new-access", "new-refresh", failure, "rejected"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ActivateCredentialIfUnchanged(ctx, saved.ID, "old-access", "old-refresh"); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("stale validation should be rejected: %v", err)
	}
	if err := repo.ActivateCredentialIfUnchanged(ctx, saved.ID, "new-access", "new-refresh"); err != nil {
		t.Fatal(err)
	}
	current, err = repo.Get(ctx, saved.ID)
	if err != nil || current.AuthStatus != account.AuthStatusActive {
		t.Fatalf("validation did not restore account: %v", err)
	}
}
