package relational

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestUpdateIdentityMetadataGuardsDuplicatesOfUnidentifiedAccounts(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(openTestDatabase(t))
	newAnonymous := func(source string) account.Credential {
		t.Helper()
		value := account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "anonymous", SourceKey: source, EncryptedAccessToken: "token-" + source, Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8}
		stored, _, err := repo.UpsertByIdentity(ctx, value)
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	anonymous := newAnonymous("anonymous-1")
	identified := account.Credential{Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "identified", Email: "user@example.test", UserID: "user-1", SourceKey: "identified", EncryptedAccessToken: "token", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8}
	identifiedID, err := repo.CreateCredentialIfAbsent(ctx, identified.Email, identified.UserID, "", identified)
	if err != nil {
		t.Fatalf("create beside anonymous account: %v", err)
	}

	// A user_id-only match is invisible to the email-grouped duplicate preview, so it must not be written.
	err = repo.UpdateIdentityMetadata(ctx, anonymous.ID, "", "user-1", "")
	if !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("user_id duplicate err = %v", err)
	}
	current, err := repo.Get(ctx, anonymous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Email != "" || current.UserID != "" || !strings.Contains(current.LastError, "#") || !strings.Contains(current.LastError, "重复账号") {
		t.Fatalf("conflicting identity was written or not reported: %#v", current)
	}
	if current.AuthStatus != account.AuthStatusActive || !current.Enabled {
		t.Fatalf("duplicate guard changed scheduling state: %#v", current)
	}

	// A different email with the same user_id is not grouped by the preview either.
	if err := repo.UpdateIdentityMetadata(ctx, anonymous.ID, "other@example.test", "user-1", ""); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("user_id duplicate with different email err = %v", err)
	}
	if current, err = repo.Get(ctx, anonymous.ID); err != nil || current.Email != "" || current.UserID != "" {
		t.Fatalf("conflicting identity was written: %#v err=%v", current, err)
	}

	// The same email lets the duplicate preview show both rows, so the identity is kept and only logged.
	if err := repo.UpdateIdentityMetadata(ctx, anonymous.ID, "USER@example.test", "user-1", ""); err != nil {
		t.Fatalf("email duplicate should be written: %v", err)
	}
	if current, err = repo.Get(ctx, anonymous.ID); err != nil || current.Email != "USER@example.test" || current.UserID != "user-1" || current.LastError != "" {
		t.Fatalf("detectable duplicate identity not written or stale marker kept: %#v err=%v", current, err)
	}
	if other, err := repo.Get(ctx, identifiedID); err != nil || other.Email != identified.Email || other.UserID != identified.UserID {
		t.Fatalf("existing account changed: %#v err=%v", other, err)
	}

	// Unique identities and identical values in other providers are written as before.
	unique := newAnonymous("anonymous-2")
	if err := repo.UpdateIdentityMetadata(ctx, unique.ID, "unique@example.test", "user-2", "team-2"); err != nil {
		t.Fatal(err)
	}
	if current, err = repo.Get(ctx, unique.ID); err != nil || current.Email != "unique@example.test" || current.UserID != "user-2" || current.TeamID != "team-2" || current.LastError != "" {
		t.Fatalf("unique identity: %#v err=%v", current, err)
	}
	console, _, err := repo.UpsertByIdentity(ctx, account.Credential{Provider: account.ProviderConsole, AuthType: account.AuthTypeSSO, Name: "console", SourceKey: "console-source", EncryptedAccessToken: "token", Enabled: true, AuthStatus: account.AuthStatusActive, Priority: 1, MaxConcurrent: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateIdentityMetadata(ctx, console.ID, "", "user-2", "team-2"); err != nil {
		t.Fatalf("cross-provider identity is not a duplicate: %v", err)
	}

	// Re-writing an unchanged identity does not report pre-existing duplicates again.
	if err := repo.UpdateIdentityMetadata(ctx, anonymous.ID, "user@example.test", "user-1", ""); err != nil {
		t.Fatalf("unchanged identity: %v", err)
	}
	if err := repo.UpdateIdentityMetadata(ctx, 999999, "missing@example.test", "", ""); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing account err = %v", err)
	}
}
