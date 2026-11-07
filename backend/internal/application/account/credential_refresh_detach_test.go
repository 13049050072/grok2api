package account

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountdomain "github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
)

// blockingCredentialRefreshAdapter holds the exchange open until released or until its own context ends.
type blockingCredentialRefreshAdapter struct {
	*credentialRefreshAdapter
	startOnce sync.Once
	started   chan struct{}
	release   chan struct{}
	canceled  atomic.Bool
}

func (a *blockingCredentialRefreshAdapter) RefreshCredential(ctx context.Context, credential accountdomain.Credential) (provider.RefreshedCredential, error) {
	a.startOnce.Do(func() { close(a.started) })
	select {
	case <-ctx.Done():
		a.canceled.Store(true)
		return provider.RefreshedCredential{}, ctx.Err()
	case <-a.release:
	}
	return a.credentialRefreshAdapter.RefreshCredential(context.Background(), credential)
}

func TestEnsureCredentialRefreshOutlivesCancelledCaller(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	service, credential, base := newCredentialRefreshTestService(t, now)
	service.now = func() time.Time { return now }
	adapter := &blockingCredentialRefreshAdapter{credentialRefreshAdapter: base, started: make(chan struct{}), release: make(chan struct{})}
	service.providers = provider.NewRegistry(adapter)

	callerCtx, cancelCaller := context.WithCancel(context.Background())
	defer cancelCaller()
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.EnsureCredential(callerCtx, credential, true)
		firstDone <- err
	}()
	<-adapter.started
	type outcome struct {
		value accountdomain.Credential
		err   error
	}
	secondDone := make(chan outcome, 1)
	go func() {
		value, err := service.EnsureCredential(context.Background(), credential, true)
		secondDone <- outcome{value: value, err: err}
	}()

	cancelCaller()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller kept waiting for the shared refresh")
	}
	close(adapter.release)

	select {
	case second := <-secondDone:
		if second.err != nil || second.value.EncryptedAccessToken != "access-1" {
			t.Fatalf("waiting caller = %#v, error = %v", second.value, second.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting caller did not receive the shared refresh")
	}
	if adapter.canceled.Load() {
		t.Fatal("caller cancellation reached the provider exchange")
	}
	stored, err := service.accounts.Get(context.Background(), credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EncryptedAccessToken != "access-1" || stored.EncryptedRefreshToken != "refresh-1" || base.refreshCount.Load() != 1 {
		t.Fatalf("stored credential = %#v, refreshes = %d", stored, base.refreshCount.Load())
	}
}

func TestEnsureCredentialDoesNotStartRefreshForCancelledCaller(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	service, credential, adapter := newCredentialRefreshTestService(t, now)
	service.now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.EnsureCredential(ctx, credential, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if adapter.refreshCount.Load() != 0 {
		t.Fatalf("refresh count = %d", adapter.refreshCount.Load())
	}
}

type panickingCredentialRefreshAdapter struct {
	*credentialRefreshAdapter
}

func (a *panickingCredentialRefreshAdapter) RefreshCredential(context.Context, accountdomain.Credential) (provider.RefreshedCredential, error) {
	panic("refresh exploded")
}

func TestEnsureCredentialReturnsErrorWhenSharedRefreshPanics(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	service, credential, base := newCredentialRefreshTestService(t, now)
	service.now = func() time.Time { return now }
	service.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	service.providers = provider.NewRegistry(&panickingCredentialRefreshAdapter{credentialRefreshAdapter: base})

	if _, err := service.EnsureCredential(context.Background(), credential, true); !errors.Is(err, errCredentialRefreshPanic) {
		t.Fatalf("error = %v, want refresh panic error", err)
	}
	stored, err := service.accounts.Get(context.Background(), credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EncryptedAccessToken != credential.EncryptedAccessToken || stored.EncryptedRefreshToken != credential.EncryptedRefreshToken {
		t.Fatalf("stored credential changed after panic: %#v", stored)
	}
}
