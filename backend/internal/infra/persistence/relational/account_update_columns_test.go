package relational

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestAccountUpdateDoesNotOverwriteConcurrentWritesFromStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(openTestDatabase(t))
	created, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderBuild, AuthType: account.AuthTypeOAuth, Name: "stale", SourceKey: "stale-source",
		EncryptedAccessToken: "access-1", EncryptedRefreshToken: "refresh-1", ExpiresAt: time.Now().Add(time.Hour),
		Enabled: true, AuthStatus: account.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Writes that land between the admin form's read and its save.
	expiresAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	if _, err := repo.UpdateTokens(ctx, created.ID, "access-2", "refresh-2", expiresAt, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateIdentityMetadata(ctx, created.ID, "user@example.com", "user-1", "team-1"); err != nil {
		t.Fatal(err)
	}
	cooldownUntil := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	if err := repo.UpdateHealth(ctx, created.ID, account.ProviderBuild, 3, &cooldownUntil, "", false); err != nil {
		t.Fatal(err)
	}

	stale.Name = "renamed"
	stale.Priority = 7
	stale.MaxConcurrent = 3
	stale.BuildRouteMode = account.BuildRouteXAI
	stale.SourceKey = "hijacked"
	updated, err := repo.Update(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.Priority != 7 || updated.MaxConcurrent != 3 || updated.BuildRouteMode != account.BuildRouteXAI {
		t.Fatalf("editable settings were not saved: %#v", updated)
	}
	if updated.EncryptedAccessToken != "access-2" || updated.EncryptedRefreshToken != "refresh-2" || !updated.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("stale snapshot overwrote refreshed tokens: %#v", updated)
	}
	if updated.SourceKey != "stale-source" || updated.Email != "user@example.com" || updated.UserID != "user-1" || updated.TeamID != "team-1" {
		t.Fatalf("stale snapshot overwrote identity: %#v", updated)
	}
	if updated.FailureCount != 3 || updated.CooldownUntil == nil || !updated.CooldownUntil.Equal(cooldownUntil) {
		t.Fatalf("stale snapshot overwrote health: %#v", updated)
	}
}

func TestAccountUpdateWritesCloudflareCookie(t *testing.T) {
	ctx := context.Background()
	repo := NewAccountRepository(openTestDatabase(t))
	created, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "cookie", SourceKey: "cookie-source",
		EncryptedAccessToken: "sso", EncryptedCloudflareCookie: "cookie-1", Enabled: true, AuthStatus: account.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{"cookie-2", ""} {
		created.EncryptedCloudflareCookie = cookie
		updated, err := repo.Update(ctx, created)
		if err != nil {
			t.Fatal(err)
		}
		if updated.EncryptedCloudflareCookie != cookie || updated.EncryptedAccessToken != "sso" {
			t.Fatalf("cookie %q update = %#v", cookie, updated)
		}
	}
}

func TestMarkReauthRequiredIfCredentialUnchanged(t *testing.T) {
	ctx := context.Background()
	database := openTestDatabase(t)
	repo := NewAccountRepository(database)
	created, _, err := repo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderBuild, AuthType: account.AuthTypeOAuth, Name: "cas", SourceKey: "cas-source",
		EncryptedAccessToken: "access-1", EncryptedRefreshToken: "refresh-1", ExpiresAt: time.Now().Add(time.Hour),
		Enabled: true, AuthStatus: account.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateTokens(ctx, created.ID, "access-2", "refresh-2", time.Now().Add(time.Hour), 0); err != nil {
		t.Fatal(err)
	}

	if err := repo.MarkReauthRequiredIfCredentialUnchanged(ctx, created.ID, "access-1", "stale rejection"); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("rejection of a replaced token = %v, want conflict", err)
	}
	stored, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AuthStatus != account.AuthStatusActive || stored.LastError != "" || stored.ReauthMarkedAt != nil {
		t.Fatalf("replaced token was marked: %#v", stored)
	}

	if err := repo.MarkReauthRequiredIfCredentialUnchanged(ctx, created.ID, "access-2", "current rejection"); err != nil {
		t.Fatal(err)
	}
	marked, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if marked.AuthStatus != account.AuthStatusReauthRequired || marked.LastError != "current rejection" || marked.ReauthMarkedAt == nil {
		t.Fatalf("current token was not marked: %#v", marked)
	}

	anchor := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if err := database.db.WithContext(ctx).Model(&accountModel{}).Where("id = ?", created.ID).Update("reauth_marked_at", anchor).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkReauthRequiredIfCredentialUnchanged(ctx, created.ID, "access-2", "repeated rejection"); err != nil {
		t.Fatal(err)
	}
	repeated, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.LastError != "repeated rejection" || repeated.ReauthMarkedAt == nil || !repeated.ReauthMarkedAt.Equal(anchor) {
		t.Fatalf("repeated rejection must keep the first reauth anchor: %#v", repeated)
	}

	if err := repo.MarkReauthRequiredIfCredentialUnchanged(ctx, created.ID+100, "access-2", "missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing account = %v, want not found", err)
	}
}
