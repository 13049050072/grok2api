package relational

import (
	"context"
	"errors"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestSafeImportMatchesKnownIdentityAndRejectsAmbiguity(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)
	repo := NewAccountRepository(db)
	original := account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "old", Email: "user@example.test", UserID: "user-1", SourceKey: "old", EncryptedAccessToken: "old-token", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8}
	saved, _, err := repo.UpsertByIdentity(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	var before accountModel
	if err := db.db.First(&before, saved.ID).Error; err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.SourceKey, updated.EncryptedAccessToken = "new", "new-token"
	results, err := repo.UpsertImportedCredentialsSafely(ctx, []account.Credential{updated})
	if err != nil || len(results) != 1 || results[0].Created || results[0].ID != saved.ID {
		t.Fatalf("update: %#v %v", results, err)
	}
	var after accountModel
	if err := db.db.First(&after, saved.ID).Error; err != nil || before.IdentityKey != after.IdentityKey {
		t.Fatalf("identity changed: %v", err)
	}
	conflicting := updated
	conflicting.SourceKey, conflicting.UserID = "other", "other-user"
	if _, err := repo.UpsertImportedCredentialsSafely(ctx, []account.Credential{conflicting}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	current, err := repo.Get(ctx, saved.ID)
	if err != nil || current.EncryptedAccessToken != "new-token" {
		t.Fatalf("conflict modified credential: %v", err)
	}
}
