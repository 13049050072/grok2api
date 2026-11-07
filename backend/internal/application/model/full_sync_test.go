package model

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

func TestSyncObservedKeepsSharedRunWhenFirstCallerLeaves(t *testing.T) {
	const accountCount = 3
	adapter := &modelCapabilityAdapter{
		models:  make(map[uint64][]string, accountCount),
		entered: make(chan struct{}, accountCount),
		release: make(chan struct{}),
	}
	service, modelRepo, createAccount := newFullSyncTestService(t, adapter)
	for index := range accountCount {
		adapter.models[createAccount(account.ProviderBuild, fmt.Sprintf("shared-%d", index))] = []string{"grok-shared"}
	}

	firstCtx, leave := context.WithCancel(context.Background())
	defer leave()
	var first, second syncProgressRecorder
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.SyncObserved(firstCtx, first.observe)
		firstResult <- err
	}()
	for range accountCount {
		select {
		case <-adapter.entered:
		case <-time.After(5 * time.Second):
			close(adapter.release)
			t.Fatal("full sync did not reach upstream discovery")
		}
	}

	type syncResult struct {
		synced int
		err    error
	}
	secondResult := make(chan syncResult, 1)
	go func() {
		synced, err := service.SyncObserved(context.Background(), second.observe)
		secondResult <- syncResult{synced: synced, err: err}
	}()
	waitForProgress(t, &second, func(calls int, last [2]int) bool { return calls > 0 && last == [2]int{0, accountCount} })

	leave()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first caller err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first caller kept waiting after its context was canceled")
	}
	time.Sleep(50 * time.Millisecond)
	close(adapter.release)

	select {
	case result := <-secondResult:
		if result.err != nil || result.synced != 1 {
			t.Fatalf("second caller synced = %d, err = %v", result.synced, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second caller did not receive the shared result")
	}
	if attempts := adapter.attemptCount(); attempts != accountCount {
		t.Fatalf("upstream attempts = %d, want one shared run of %d", attempts, accountCount)
	}
	if _, last := second.snapshot(); last != [2]int{accountCount, accountCount} {
		t.Fatalf("second caller last progress = %v", last)
	}
	if _, last := first.snapshot(); last[0] != 0 {
		t.Fatalf("first caller kept receiving progress after leaving: %v", last)
	}
	if _, err := modelRepo.GetByPublicID(context.Background(), "Build/grok-shared"); err != nil {
		t.Fatalf("discovered route missing: %v", err)
	}

	if _, err := service.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if attempts := adapter.attemptCount(); attempts != 2*accountCount {
		t.Fatalf("upstream attempts after a new sync = %d, want %d", attempts, 2*accountCount)
	}
}

func TestSyncAllAccountsPublishesModelsDiscoveredBeforeCancellation(t *testing.T) {
	buildAdapter := &modelCapabilityAdapter{models: map[uint64][]string{}}
	webAdapter := &modelCapabilityAdapter{
		provider: account.ProviderWeb,
		models:   map[uint64][]string{},
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	defer close(webAdapter.release)
	service, modelRepo, createAccount := newFullSyncTestService(t, buildAdapter, webAdapter)
	buildAdapter.models[createAccount(account.ProviderBuild, "finished")] = []string{"grok-finished"}
	webAdapter.models[createAccount(account.ProviderWeb, "pending")] = []string{"grok-chat-fast"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var progress syncProgressRecorder
	result := make(chan error, 1)
	go func() {
		_, err := service.syncAllAccounts(ctx, progress.observe)
		result <- err
	}()
	select {
	case <-webAdapter.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("web discovery did not start")
	}
	waitForProgress(t, &progress, func(_ int, last [2]int) bool { return last[0] >= 1 })
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sync err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled sync did not return")
	}
	if _, err := modelRepo.GetByPublicID(context.Background(), "Build/grok-finished"); err != nil {
		t.Fatalf("model discovered before cancellation missing from route table: %v", err)
	}
}

type syncProgressRecorder struct {
	mu    sync.Mutex
	calls int
	last  [2]int
}

func (r *syncProgressRecorder) observe(completed, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.last = [2]int{completed, total}
}

func (r *syncProgressRecorder) snapshot() (int, [2]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.last
}

func waitForProgress(t *testing.T, recorder *syncProgressRecorder, ready func(calls int, last [2]int) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready(recorder.snapshot()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls, last := recorder.snapshot()
	t.Fatalf("progress not reported: calls = %d, last = %v", calls, last)
}

func newFullSyncTestService(t *testing.T, adapters ...provider.Adapter) (*Service, *relational.ModelRepository, func(account.Provider, string) uint64) {
	t.Helper()
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "model-full-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("access-token")
	if err != nil {
		t.Fatal(err)
	}
	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	registry := provider.NewRegistry(adapters...)
	accountService := accountapp.NewService(accountRepo, relational.NewAuditRepository(database), memory.NewDeviceSessionStore(), memory.NewStickyStore(), registry, cipher, nil)
	createAccount := func(providerValue account.Provider, name string) uint64 {
		t.Helper()
		value := account.Credential{Provider: providerValue, Name: name, SourceKey: name, EncryptedAccessToken: encrypted, ExpiresAt: time.Now().Add(time.Hour), AuthStatus: account.AuthStatusActive}
		if providerValue == account.ProviderWeb {
			value.AuthType = account.AuthTypeSSO
			value.WebTier = account.WebTierSuper
		}
		stored, _, err := accountRepo.UpsertByIdentity(ctx, value)
		if err != nil {
			t.Fatal(err)
		}
		return stored.ID
	}
	return NewService(modelRepo, accountRepo, accountService, registry), modelRepo, createAccount
}
