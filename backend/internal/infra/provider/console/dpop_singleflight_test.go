package console

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
)

func TestDPoPSessionExchangeOutlivesCanceledFirstCaller(t *testing.T) {
	var tokenRequests atomic.Int32
	tokenStarted := make(chan struct{})
	var startOnce sync.Once
	releaseToken := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseToken) }) }
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/dpop/token" {
			http.NotFound(writer, request)
			return
		}
		tokenRequests.Add(1)
		startOnce.Do(func() { close(tokenStarted) })
		select {
		case <-releaseToken:
		case <-request.Context().Done():
			return
		}
		serveTestDPoPToken(t, writer, request)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(release)

	adapter, credential := newConsoleTestAdapter(t, server.URL)
	lease, err := adapter.egress.AcquireCredential(t.Context(), egressdomain.ScopeConsole, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	type outcome struct {
		session dpopSession
		err     error
	}
	get := func(ctx context.Context) <-chan outcome {
		done := make(chan outcome, 1)
		go func() {
			session, _, err := adapter.dpop.get(ctx, adapter, credential, "test-sso", lease)
			done <- outcome{session: session, err: err}
		}()
		return done
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	first := get(firstCtx)
	select {
	case <-tokenStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("DPoP token exchange did not start")
	}
	second := get(context.Background())
	cancelFirst()
	select {
	case result := <-first:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("first caller err = %v, want context.Canceled", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled caller kept waiting for the shared exchange")
	}

	release()
	select {
	case result := <-second:
		if result.err != nil || result.session.accessToken == "" {
			t.Fatalf("second caller session=%+v err=%v", result.session, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second caller did not receive the shared session")
	}
	if got := tokenRequests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
	if _, ok := adapter.dpop.cached(dpopSessionCacheKey(server.URL, credential, "test-sso", lease)); !ok {
		t.Fatal("shared session was not cached")
	}
}
