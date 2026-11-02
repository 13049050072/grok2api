package account

import (
	"context"
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

func TestValidateReplacedCredentialRequiresMatchingUpstreamIdentity(t *testing.T) {
	ctx := context.Background()
	service, repo, adapter := newWebAccountSettingsTestService(t)
	value, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "web", Email: "user@example.com", UserID: "user-1", SourceKey: "sso:validation-test", EncryptedAccessToken: "encrypted", Enabled: true, AuthStatus: accountdomain.AuthStatusReauthRequired, Priority: 1, MaxConcurrent: 8})
	if err != nil {
		t.Fatal(err)
	}
	adapter.identityErr = provider.ErrUnauthorized
	if err := service.ValidateReplacedCredential(ctx, value.ID); err == nil {
		t.Fatal("rejected credential activated")
	}
	adapter.identityErr = nil
	adapter.identity = provider.AccountIdentity{UserID: "other", Email: "user@example.com"}
	if err := service.ValidateReplacedCredential(ctx, value.ID); err == nil {
		t.Fatal("different identity activated")
	}
	stored, err := repo.Get(ctx, value.ID)
	if err != nil || stored.AuthStatus != accountdomain.AuthStatusReauthRequired {
		t.Fatalf("unverified account activated: %v", err)
	}
	adapter.identity = provider.AccountIdentity{UserID: "user-1", Email: "user@example.com"}
	if err := service.ValidateReplacedCredential(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.Get(ctx, value.ID)
	if err != nil || stored.AuthStatus != accountdomain.AuthStatusActive {
		t.Fatalf("verified account not activated: %v", err)
	}
}
