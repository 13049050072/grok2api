package account

import (
	"context"
	"errors"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

func TestAutoDeleteConsoleUnlinkedWaitsForActiveLease(t *testing.T) {
	ctx := context.Background()
	service, repo := newAutoCleanTestService(t, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
	limiter := memory.NewConcurrencyLimiter()
	service.SetConcurrencyLimiter(limiter)
	service.consoleUnlinkedRetryDelay = 20 * time.Millisecond
	service.autoDeleteConsoleUnlinked.Store(true)
	value := mustUpsert(t, repo, accountdomain.Credential{
		Provider: accountdomain.ProviderConsole, AuthType: accountdomain.AuthTypeSSO, Name: "busy", SourceKey: "console-busy",
		EncryptedAccessToken: "x", Enabled: true, AuthStatus: accountdomain.AuthStatusActive,
	})
	release, acquired, err := limiter.Acquire(ctx, repository.AccountConcurrencyKey(value.ID), 1)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err)
	}

	if err := service.MarkReauthRequired(ctx, value.ID, accountdomain.LastErrorConsoleUnlinked); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.Get(ctx, value.ID)
	if err != nil {
		t.Fatalf("account deleted while its inference lease was active: %v", err)
	}
	if stored.AuthStatus != accountdomain.AuthStatusReauthRequired {
		t.Fatalf("auth status = %q, want reauthRequired", stored.AuthStatus)
	}
	time.Sleep(5 * service.consoleUnlinkedRetryDelay)
	assertPresent(t, repo, value.ID)

	release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := repo.Get(ctx, value.ID); errors.Is(err, repository.ErrNotFound) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("deferred unlinked Console account was not deleted after its lease ended")
}
