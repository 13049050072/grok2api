package invalidation

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

type testBus struct {
	mu         sync.Mutex
	published  []repository.InvalidationEvent
	events     []repository.InvalidationEvent
	publishErr error
}

func (b *testBus) PublishInvalidation(_ context.Context, event repository.InvalidationEvent) error {
	b.mu.Lock()
	b.published = append(b.published, event)
	b.mu.Unlock()
	return b.publishErr
}

func (b *testBus) ListenInvalidations(ctx context.Context, handler func(context.Context, repository.InvalidationEvent) error) error {
	for _, event := range b.events {
		if err := handler(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func TestNotifyAlwaysAppliesLocalInvalidation(t *testing.T) {
	var applied []repository.InvalidationEvent
	service := NewService(nil, "local", func(event repository.InvalidationEvent) {
		applied = append(applied, event)
	}, slog.Default())
	service.Notify(context.Background(), repository.InvalidationEvent{
		Kind: repository.InvalidationAccountStateChanged, Provider: account.ProviderBuild,
	})
	if len(applied) != 1 || applied[0].SourceInstance != "local" || applied[0].PublishedAt.IsZero() {
		t.Fatalf("local invalidation = %#v", applied)
	}
}

func TestNotifyAppliesLocallyWhenRemoteQueueIsFull(t *testing.T) {
	var applied int
	service := NewService(&testBus{}, "local", func(repository.InvalidationEvent) { applied++ }, slog.Default())
	for range cap(service.queue) {
		service.queue <- repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged}
	}
	service.Notify(context.Background(), repository.InvalidationEvent{Kind: repository.InvalidationAccountBillingChanged})
	if applied != 1 || service.dropped.Load() != 1 {
		t.Fatalf("applied=%d dropped=%d", applied, service.dropped.Load())
	}
}

func TestRunSubscriberIgnoresInvalidAndSameSourceEvents(t *testing.T) {
	bus := &testBus{events: []repository.InvalidationEvent{
		{Kind: "unknown", SourceInstance: "remote"},
		{Kind: repository.InvalidationAccountStateChanged, Provider: account.ProviderBuild, SourceInstance: "local"},
		{Kind: repository.InvalidationAccountQuotaChanged, Provider: account.ProviderWeb, SourceInstance: "remote", Revision: 2},
	}}
	var applied []repository.InvalidationEvent
	service := NewService(bus, "local", func(event repository.InvalidationEvent) {
		applied = append(applied, event)
	}, slog.Default())
	if err := service.RunSubscriber(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Kind != repository.InvalidationAccountQuotaChanged {
		t.Fatalf("applied events = %#v", applied)
	}
}

func TestRunPublisherDoesNotStopAfterPublishFailure(t *testing.T) {
	bus := &testBus{publishErr: errors.New("redis unavailable")}
	service := NewService(bus, "local", nil, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.RunPublisher(ctx) }()
	service.Notify(context.Background(), repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged})
	deadline := time.Now().Add(time.Second)
	for service.failures.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if service.failures.Load() != 1 {
		t.Fatalf("publish failures = %d", service.failures.Load())
	}
}

func TestEventKeyKeepsClientKeyInvalidationsDistinct(t *testing.T) {
	first := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationClientKeyChanged, ClientKeyID: 1})
	second := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationClientKeyChanged, ClientKeyID: 2})
	global := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationClientKeyChanged})
	if first == second || first == global || second == global {
		t.Fatalf("client-key invalidation keys were coalesced: first=%#v second=%#v global=%#v", first, second, global)
	}
}

func TestEventKeyKeepsProviderlessAccountInvalidationsDistinct(t *testing.T) {
	first := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged, AccountID: 1})
	second := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged, AccountID: 2})
	global := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged})
	if first == second || first == global || second == global {
		t.Fatalf("provider-less account invalidations were coalesced: first=%#v second=%#v global=%#v", first, second, global)
	}
	buildFirst := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged, Provider: account.ProviderBuild, AccountID: 1})
	buildSecond := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged, Provider: account.ProviderBuild, AccountID: 2})
	if buildFirst != buildSecond {
		t.Fatalf("provider-scoped invalidations should still coalesce: first=%#v second=%#v", buildFirst, buildSecond)
	}
}

func TestRunPublisherPublishesEachProviderlessAccountInvalidation(t *testing.T) {
	bus := &testBus{}
	service := NewService(bus, "local", nil, slog.Default())
	for _, id := range []uint64{1, 2, 1} {
		service.queue <- repository.InvalidationEvent{Kind: repository.InvalidationAccountStateChanged, AccountID: id, SourceInstance: "local"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.RunPublisher(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bus.mu.Lock()
		published := len(bus.published)
		bus.mu.Unlock()
		if published >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	seen := map[uint64]bool{}
	for _, event := range bus.published {
		seen[event.AccountID] = true
	}
	if len(bus.published) != 2 || !seen[1] || !seen[2] {
		t.Fatalf("published = %#v", bus.published)
	}
}

func TestEventKeyKeepsAccountHealthInvalidationsDistinct(t *testing.T) {
	first := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationAccountHealthChanged, Provider: account.ProviderBuild, AccountID: 1})
	second := eventKey(repository.InvalidationEvent{Kind: repository.InvalidationAccountHealthChanged, Provider: account.ProviderBuild, AccountID: 2})
	if first == second {
		t.Fatalf("account health invalidations were coalesced: first=%#v second=%#v", first, second)
	}
}
