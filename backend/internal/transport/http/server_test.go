package httpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/transport/http/middleware"
)

func testDependencies() Dependencies {
	return Dependencies{RequestTimeout: time.Second, MaxBodyBytes: 1024, ConcurrencyGate: middleware.NewConcurrencyGate(1024)}
}

func TestReadinessEndpointReturnsStructuredDegradedStateAsReady(t *testing.T) {
	deps := testDependencies()
	deps.Readiness = func(context.Context) ReadinessSnapshot {
		return ReadinessSnapshot{
			Ready: true, State: "degraded", UpdatedAt: time.Now().UTC(),
			Components: map[string]ReadinessComponent{
				"grok_build": {State: "ready"},
				"grok_web":   {State: "unavailable"},
			},
		}
	}
	router := New(deps)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body ReadinessSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Ready || body.State != "degraded" || body.Components["grok_build"].State != "ready" {
		t.Fatalf("body = %#v", body)
	}
}

func TestReadinessEndpointReturns503WhileReconciling(t *testing.T) {
	deps := testDependencies()
	deps.Readiness = func(context.Context) ReadinessSnapshot {
		return ReadinessSnapshot{Ready: false, State: "reconciling", UpdatedAt: time.Now().UTC()}
	}
	router := New(deps)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"state":"reconciling"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestReadinessEndpointHidesDetailsAndSharesCachedSnapshot(t *testing.T) {
	var computations atomic.Int32
	release := make(chan struct{})
	deps := testDependencies()
	deps.Readiness = func(context.Context) ReadinessSnapshot {
		computations.Add(1)
		<-release
		return ReadinessSnapshot{
			Ready: true, State: "degraded", UpdatedAt: time.Now().UTC(),
			Components: map[string]ReadinessComponent{
				"billing_ledger": {State: "degraded", Detail: "审计账本不可用；连续失败 0 次，丢失 3 条，队列 7/8"},
				"model_routes":   {State: "ready", Detail: "12 条已启用路由"},
			},
			Startup: &ReadinessStartupReport{ErrorCount: 2},
		}
	}
	router := New(deps)
	const probes = 8
	var wg sync.WaitGroup
	bodies := make(chan string, probes)
	for range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if recorder.Code != http.StatusOK {
				t.Errorf("status = %d", recorder.Code)
			}
			bodies <- recorder.Body.String()
		}()
	}
	deadline := time.Now().Add(time.Second)
	for computations.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(bodies)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if got := computations.Load(); got != 1 {
		t.Fatalf("readiness computations = %d, want 1", got)
	}
	for body := range bodies {
		if strings.Contains(body, "detail") || strings.Contains(body, "丢失") || strings.Contains(body, "startup") {
			t.Fatalf("anonymous readiness exposed details: %s", body)
		}
		var snapshot ReadinessSnapshot
		if err := json.Unmarshal([]byte(body), &snapshot); err != nil {
			t.Fatal(err)
		}
		if !snapshot.Ready || snapshot.State != "degraded" || snapshot.Components["billing_ledger"].State != "degraded" || snapshot.Components["model_routes"].State != "ready" {
			t.Fatalf("anonymous readiness = %#v", snapshot)
		}
	}
}

func TestReadinessProbeExpiresCacheAndDetachesComputation(t *testing.T) {
	var computations atomic.Int32
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	probe := newReadinessProbe(func(ctx context.Context) ReadinessSnapshot {
		computations.Add(1)
		cancelCaller()
		if ctx.Err() != nil {
			return ReadinessSnapshot{State: "not_ready"}
		}
		return ReadinessSnapshot{Ready: true, State: "ready"}
	})
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	probe.now = func() time.Time { return now }
	snapshot, err := probe.snapshot(callerCtx)
	if err != nil || !snapshot.Ready {
		t.Fatalf("snapshot after caller cancellation = %#v, err=%v", snapshot, err)
	}
	now = now.Add(readinessCacheTTL - time.Millisecond)
	if _, err := probe.snapshot(context.Background()); err != nil || computations.Load() != 1 {
		t.Fatalf("cached snapshot recomputed: computations=%d err=%v", computations.Load(), err)
	}
	now = now.Add(time.Millisecond)
	if _, err := probe.snapshot(context.Background()); err != nil || computations.Load() != 2 {
		t.Fatalf("expired snapshot reused: computations=%d err=%v", computations.Load(), err)
	}
}

func TestInferenceTrafficIsRejectedWhileReconciling(t *testing.T) {
	deps := testDependencies()
	deps.TrafficReady = func() bool { return false }
	router := New(deps)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"code":"service_reconciling"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSystemEndpointsRequireAdminAuthentication(t *testing.T) {
	deps := testDependencies()
	deps.PublicAPIBaseURL = "https://api.example.com"
	router := New(deps)
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/admin/v1/system"},
		{method: http.MethodGet, path: "/api/admin/v1/system/version"},
		{method: http.MethodPost, path: "/api/admin/v1/system/update/check"},
	} {
		request := httptest.NewRequest(route.method, route.path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want %d", route.method, route.path, recorder.Code, http.StatusUnauthorized)
		}
	}
}

func TestFrontendStaticFilesAndSPAFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>app</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("console.log('app')"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := testDependencies()
	deps.Logger = slog.Default()
	deps.FrontendStaticPath = root
	router := New(deps)

	for _, test := range []struct {
		path        string
		status      int
		body        string
		cachePrefix string
	}{
		{path: "/assets/app.js", status: http.StatusOK, body: "console.log('app')", cachePrefix: "public"},
		{path: "/dashboard", status: http.StatusOK, body: "<html>app</html>", cachePrefix: "no-cache"},
		{path: "/assets/missing.js", status: http.StatusNotFound},
		{path: "/api/admin/v1/missing", status: http.StatusNotFound},
		{path: "/swagger/index.html", status: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d", recorder.Code, test.status)
			}
			if test.body != "" && !strings.Contains(recorder.Body.String(), test.body) {
				t.Fatalf("body = %q", recorder.Body.String())
			}
			if test.cachePrefix != "" && !strings.HasPrefix(recorder.Header().Get("Cache-Control"), test.cachePrefix) {
				t.Fatalf("cache-control = %q", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestSwaggerRegistrationFollowsStartupConfig(t *testing.T) {
	disabledDeps := testDependencies()
	disabledDeps.Logger = slog.Default()
	disabled := New(disabledDeps)
	disabledRequest := httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil)
	disabledRecorder := httptest.NewRecorder()
	disabled.ServeHTTP(disabledRecorder, disabledRequest)
	if disabledRecorder.Code != http.StatusNotFound {
		t.Fatalf("disabled swagger status = %d, want %d", disabledRecorder.Code, http.StatusNotFound)
	}

	enabledDeps := testDependencies()
	enabledDeps.Logger = slog.Default()
	enabledDeps.SwaggerEnabled = true
	enabled := New(enabledDeps)
	enabledRequest := httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil)
	enabledRecorder := httptest.NewRecorder()
	enabled.ServeHTTP(enabledRecorder, enabledRequest)
	if enabledRecorder.Code != http.StatusOK {
		t.Fatalf("enabled swagger status = %d, want %d", enabledRecorder.Code, http.StatusOK)
	}
	var document struct {
		Info struct {
			Title string `json:"title"`
		} `json:"info"`
	}
	if err := json.Unmarshal(enabledRecorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode swagger document: %v", err)
	}
	if document.Info.Title != "Grok2API" {
		t.Fatalf("swagger title = %q, want %q", document.Info.Title, "Grok2API")
	}
}
