package account

import (
	"context"
	"errors"
	"strings"
	"testing"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

func TestSyncAccountIdentityDoesNotWriteUndetectableDuplicateIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, repo, adapter := newWebAccountSettingsTestService(t)
	const userID = "66666666-6666-4666-8666-666666666666"
	anonymous, _, err := repo.UpsertByIdentity(ctx, accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "anonymous",
		SourceKey:            "sso:" + strings.Repeat("1", 64),
		EncryptedAccessToken: "encrypted", Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	identified := accountdomain.Credential{
		Provider: accountdomain.ProviderWeb, AuthType: accountdomain.AuthTypeSSO, Name: "identified", Email: "user@example.com", UserID: userID,
		SourceKey: "sso:" + strings.Repeat("2", 64), EncryptedAccessToken: "encrypted", Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	}
	if _, err := repo.CreateCredentialIfAbsent(ctx, identified.Email, identified.UserID, "", identified); err != nil {
		t.Fatal(err)
	}
	adapter.identity = provider.AccountIdentity{UserID: userID}
	if err := service.SyncAccountIdentity(ctx, anonymous.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("SyncAccountIdentity err = %v", err)
	}
	stored, err := repo.Get(ctx, anonymous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.UserID != "" || stored.Email != "" || stored.LastError == "" || stored.AuthStatus != accountdomain.AuthStatusActive {
		t.Fatalf("duplicate identity handling = %#v", stored)
	}
}
