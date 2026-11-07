package account

import (
	"context"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
)

func TestMarkReauthRequiredForCredentialIgnoresReplacedCredential(t *testing.T) {
	ctx := context.Background()
	service, repo := newAutoCleanTestService(t, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	service.autoDeleteConsoleUnlinked.Store(true)
	console := accountdomain.Credential{
		Provider: accountdomain.ProviderConsole, AuthType: accountdomain.AuthTypeSSO, Name: "console", SourceKey: "console-replaced",
		EncryptedAccessToken: "rejected-sso", Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	}
	rejected := mustUpsert(t, repo, console)
	console.EncryptedAccessToken = "reimported-sso"
	mustUpsert(t, repo, console)

	if err := service.MarkReauthRequiredForCredential(ctx, rejected, accountdomain.LastErrorConsoleUnlinked); err != nil {
		t.Fatal(err)
	}
	current, err := repo.Get(ctx, rejected.ID)
	if err != nil {
		t.Fatalf("stale rejection deleted the re-imported account: %v", err)
	}
	if current.AuthStatus != accountdomain.AuthStatusActive || current.LastError != "" {
		t.Fatalf("stale rejection marked the re-imported credential: %#v", current)
	}

	if err := service.MarkReauthRequiredForCredential(ctx, current, accountdomain.LastErrorConsoleUnlinked); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, rejected.ID); err == nil {
		t.Fatal("rejecting the current credential must still delete the unlinked Console account")
	}
}
