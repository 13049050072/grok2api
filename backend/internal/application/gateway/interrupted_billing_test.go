package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	accountapp "github.com/chenyme/grok2api/backend/internal/application/account"
	clientkeyapp "github.com/chenyme/grok2api/backend/internal/application/clientkey"
	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/domain/audit"
	"github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	modeldomain "github.com/chenyme/grok2api/backend/internal/domain/model"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
)

func TestEstimateInterruptedStreamUsage(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","input":"hello","max_output_tokens":500}`)
	baseInput := audit.EstimateRequestInputTokens(body)
	tests := []struct {
		name       string
		usage      Usage
		status     int
		errorCode  string
		context    int64
		estimated  bool
		wantInput  int64
		wantOutput int64
	}{
		{name: "client disconnect without usage", usage: Usage{OutputObserved: true, ForwardedOutputBytes: 300}, status: http.StatusOK, errorCode: "client_stream_interrupted", estimated: true, wantInput: baseInput, wantOutput: 100},
		{name: "reported zero usage", usage: Usage{Reported: true, ForwardedOutputBytes: 301}, status: http.StatusOK, errorCode: "upstream_stream_incomplete", estimated: true, wantInput: baseInput, wantOutput: 101},
		{name: "reported input is kept", usage: Usage{Reported: true, InputTokens: 900, CachedInputTokens: 300, ForwardedOutputBytes: 30}, status: http.StatusOK, errorCode: "stream_interrupted", estimated: true, wantInput: 900, wantOutput: 10},
		{name: "explicit output limit caps estimate", usage: Usage{ForwardedOutputBytes: 30_000}, status: http.StatusOK, errorCode: "response_too_large", estimated: true, wantInput: baseInput, wantOutput: 500},
		{name: "stored context is added", usage: Usage{ForwardedOutputBytes: 3}, status: http.StatusOK, errorCode: "upstream_stream_idle_timeout", context: 4000, estimated: true, wantInput: baseInput + 4000, wantOutput: 1},
		{name: "reported output wins", usage: Usage{Reported: true, OutputTokens: 7, ForwardedOutputBytes: 300}, status: http.StatusOK, errorCode: "client_stream_interrupted"},
		{name: "completed stream", usage: Usage{ForwardedOutputBytes: 300}, status: http.StatusOK},
		{name: "explicit upstream failure", usage: Usage{ForwardedOutputBytes: 300}, status: http.StatusOK, errorCode: "upstream_stream_error"},
		{name: "nothing forwarded", usage: Usage{OutputObserved: true}, status: http.StatusOK, errorCode: "client_stream_interrupted"},
		{name: "upstream error status", usage: Usage{ForwardedOutputBytes: 300}, status: http.StatusBadGateway, errorCode: "stream_interrupted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, estimated := estimateInterruptedStreamUsage(test.usage, test.status, test.errorCode, body, test.context)
			if estimated != test.estimated {
				t.Fatalf("estimated = %t, want %t (usage %#v)", estimated, test.estimated, got)
			}
			if !estimated {
				if got != test.usage {
					t.Fatalf("usage changed without an estimate: %#v", got)
				}
				return
			}
			if !got.Reported || got.InputTokens != test.wantInput || got.OutputTokens != test.wantOutput || got.TotalTokens != test.wantInput+test.wantOutput {
				t.Fatalf("estimated usage = %#v, want input %d output %d", got, test.wantInput, test.wantOutput)
			}
			if test.usage.InputTokens > 0 && got.CachedInputTokens != test.usage.CachedInputTokens {
				t.Fatalf("reported cached input changed: %#v", got)
			}
		})
	}
}

func TestInterruptedResponseStreamSettlesEstimatedUsage(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "interrupted-billing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	auditRepo := relational.NewAuditRepository(database)
	keyRepo := relational.NewClientKeyRepository(database)
	credential, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderConsole, AuthType: account.AuthTypeSSO,
		Name: "console-interrupted", SourceKey: "console-interrupted", EncryptedAccessToken: "console-token",
		Enabled: true, AuthStatus: account.AuthStatusActive, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	const model = "grok-4.5"
	if err := modelRepo.UpsertRoutes(ctx, []modeldomain.Route{{
		PublicID: model, Provider: account.ProviderConsole, UpstreamModel: model,
		Capability: modeldomain.CapabilityResponses, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := modelRepo.ReplaceAccountCapabilities(ctx, credential.ID, []string{model}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	key, err := keyRepo.Create(ctx, clientkey.Key{
		Name: "interrupted-key", Prefix: "interrupted", SecretHash: strings.Repeat("e", 64), EncryptedSecret: "encrypted",
		Enabled: true, BillingLimitUSDTicks: 10_000_000_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry(statelessConsoleAdapter{})
	sticky := memory.NewStickyStore()
	selector := NewSelector(accountRepo, memory.NewConcurrencyLimiter(), sticky, registry, time.Hour, time.Second, time.Minute)
	accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), sticky, registry, testCipher(t), nil)
	service := NewService(modelRepo, auditRepo, accountService, clientkeyapp.NewService(keyRepo, nil, nil, 60, 4, nil), registry, selector, nil, 1)

	body := []byte(`{"model":"grok-4.5","input":"hello","stream":true,"max_output_tokens":4000}`)
	run := func(requestID string, usage Usage, errorCode string) {
		t.Helper()
		result, err := service.CreateResponse(ctx, Input{RequestID: requestID, ClientKey: key, PublicModel: model, Body: body, Streaming: true})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(result.Body)
		result.Finalize(usage, "", errorCode)
		_ = result.Body.Close()
	}

	// A client that disconnects before the final usage frame must still pay
	// for the output it already received.
	run("req-interrupted", Usage{OutputObserved: true, ForwardedOutputBytes: 3000}, "client_stream_interrupted")
	inputTokens := audit.EstimateRequestInputTokens(body)
	want, ok := audit.EstimateOfficialCost(model, inputTokens, 0, 1000, 0)
	if !ok || want.CostInUSDTicks <= 0 {
		t.Fatalf("expected pricing = %#v, %t", want, ok)
	}
	logs, _, err := auditRepo.List(ctx, 0, 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("audits = %#v, err = %v", logs, err)
	}
	if record := logs[0]; record.UsageSource != audit.UsageSourceEstimated || record.ErrorCode != "client_stream_interrupted" ||
		record.InputTokens != inputTokens || record.OutputTokens != 1000 || record.EstimatedCostInUSDTicks != want.CostInUSDTicks {
		t.Fatalf("interrupted audit = %#v, want estimated cost %d", record, want.CostInUSDTicks)
	}
	settled, err := keyRepo.Get(ctx, key.ID)
	if err != nil || settled.BilledUsageUSDTicks != want.CostInUSDTicks || settled.ReservedUsageUSDTicks != 0 {
		t.Fatalf("interrupted billing = %#v, err = %v, want billed %d", settled, err, want.CostInUSDTicks)
	}

	run("req-upstream-failed", Usage{OutputObserved: true, ForwardedOutputBytes: 3000}, "upstream_stream_error")
	logs, _, err = auditRepo.List(ctx, 0, 10)
	if err != nil || len(logs) != 2 || logs[0].UsageSource != audit.UsageSourceNone || logs[0].EstimatedCostInUSDTicks != 0 {
		t.Fatalf("explicit upstream failure audit = %#v, err = %v", logs, err)
	}
	settled, err = keyRepo.Get(ctx, key.ID)
	if err != nil || settled.BilledUsageUSDTicks != want.CostInUSDTicks || settled.ReservedUsageUSDTicks != 0 {
		t.Fatalf("explicit upstream failure billing = %#v, err = %v", settled, err)
	}
}

func TestStoredResponseContextIsReservedForContinuations(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "response-context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	auditRepo := relational.NewAuditRepository(database)
	responseRepo := relational.NewResponseRepository(database)
	keyRepo := relational.NewClientKeyRepository(database)
	credential, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderWeb, AuthType: account.AuthTypeSSO, Name: "web-context", SourceKey: "web-context",
		EncryptedAccessToken: "encrypted", Enabled: true, AuthStatus: account.AuthStatusActive, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	const model = "grok-4.5"
	if err := modelRepo.UpsertDiscovered(ctx, account.ProviderWeb, []string{model}); err != nil {
		t.Fatal(err)
	}
	if err := modelRepo.ReplaceAccountCapabilities(ctx, credential.ID, []string{model}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	const rootInputTokens, rootOutputTokens int64 = 200_000, 100_000
	rootCost, ok := audit.EstimateOfficialCost(model, rootInputTokens, 0, rootOutputTokens, 0)
	if !ok {
		t.Fatal("root pricing unavailable")
	}
	continuation := []byte(`{"model":"grok-4.5","previous_response_id":"resp-web-ownership","input":"continue","max_output_tokens":1000}`)
	bodyOnly, _ := audit.EstimateOfficialTextReservation(model, continuation)
	withContext, _ := audit.EstimateOfficialTextReservationWithContext(model, continuation, rootInputTokens+rootOutputTokens)
	if withContext.CostInUSDTicks <= bodyOnly.CostInUSDTicks {
		t.Fatalf("context reservation %d must exceed body-only reservation %d", withContext.CostInUSDTicks, bodyOnly.CostInUSDTicks)
	}
	// After the root turn is billed, the remaining budget covers the
	// continuation's incremental body but not the stored context it replays.
	key, err := keyRepo.Create(ctx, clientkey.Key{
		Name: "context-key", Prefix: "context-key", SecretHash: strings.Repeat("c", 64), EncryptedSecret: "encrypted",
		Enabled: true, BillingLimitUSDTicks: rootCost.CostInUSDTicks + (bodyOnly.CostInUSDTicks+withContext.CostInUSDTicks)/2,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry(webStoredResponseAdapter{})
	sticky := memory.NewStickyStore()
	accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), sticky, registry, testCipher(t), nil)
	selector := NewSelector(accountRepo, memory.NewConcurrencyLimiter(), sticky, registry, time.Hour, time.Second, time.Minute)
	service := NewService(modelRepo, auditRepo, accountService, clientkeyapp.NewService(keyRepo, nil, nil, 60, 4, nil), registry, selector, responseRepo, 1)

	root, err := service.CreateResponse(ctx, Input{
		RequestID: "req-context-root", ClientKey: key, PublicModel: model,
		Body: []byte(`{"model":"grok-4.5","input":"hello","max_output_tokens":1000}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(root.Body)
	root.Finalize(Usage{Reported: true, InputTokens: rootInputTokens, OutputTokens: rootOutputTokens, TotalTokens: rootInputTokens + rootOutputTokens}, "resp-web-ownership", "")
	_ = root.Body.Close()
	if got := service.responseContexts.lookup(key.ID, "resp-web-ownership", time.Now().UTC()); got != rootInputTokens+rootOutputTokens {
		t.Fatalf("remembered context = %d, want %d", got, rootInputTokens+rootOutputTokens)
	}
	if got := service.responseContexts.lookup(key.ID+1, "resp-web-ownership", time.Now().UTC()); got != 0 {
		t.Fatalf("context leaked across client keys: %d", got)
	}
	settled, err := keyRepo.Get(ctx, key.ID)
	if err != nil || settled.BilledUsageUSDTicks != rootCost.CostInUSDTicks || settled.ReservedUsageUSDTicks != 0 {
		t.Fatalf("root billing = %#v, err = %v, want billed %d", settled, err, rootCost.CostInUSDTicks)
	}

	_, err = service.CreateResponse(ctx, Input{
		RequestID: "req-context-next", ClientKey: key, PublicModel: model,
		PreviousResponseID: "resp-web-ownership", Body: continuation,
	})
	if !errors.Is(err, clientkeyapp.ErrBillingLimit) {
		t.Fatalf("continuation err = %v, want billing limit from the stored context reservation", err)
	}
}

func TestResponseContextCacheEvictsOldestAndExpires(t *testing.T) {
	var cache responseContextCache
	now := time.Now().UTC()
	cache.remember(1, "resp-expired", 10, now.Add(-time.Second))
	if got := cache.lookup(1, "resp-expired", now); got != 0 {
		t.Fatalf("expired context = %d", got)
	}
	cache.remember(1, "resp-oldest", 10, now.Add(time.Hour))
	for index := range responseContextCacheLimit - 1 {
		cache.remember(2, "resp-"+strconv.Itoa(index), 5, now.Add(time.Hour))
	}
	if got := cache.lookup(1, "resp-oldest", now); got != 10 {
		t.Fatalf("context evicted before the cache was full: %d", got)
	}
	cache.remember(2, "resp-newest", 5, now.Add(time.Hour))
	if got := cache.lookup(1, "resp-oldest", now); got != 0 {
		t.Fatalf("oldest context survived eviction: %d", got)
	}
	if len(cache.entries) != responseContextCacheLimit || len(cache.order) != responseContextCacheLimit {
		t.Fatalf("cache size = %d entries / %d order, want %d", len(cache.entries), len(cache.order), responseContextCacheLimit)
	}
}

type reservationExpiryRecorder struct {
	*relational.ClientKeyRepository
	mu       sync.Mutex
	expiries []time.Time
}

func (r *reservationExpiryRecorder) ReserveBillingUsage(ctx context.Context, id uint64, eventID string, amount int64, expiresAt time.Time) (bool, error) {
	r.mu.Lock()
	r.expiries = append(r.expiries, expiresAt)
	r.mu.Unlock()
	return r.ClientKeyRepository.ReserveBillingUsage(ctx, id, eventID, amount, expiresAt)
}

func (r *reservationExpiryRecorder) last(t *testing.T) time.Time {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.expiries) == 0 {
		t.Fatal("no billing reservation was made")
	}
	return r.expiries[len(r.expiries)-1]
}

func TestSyncMediaBillingUsesRequestScopedReservationAndBillsInterruptedDelivery(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "media-billing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accountRepo := relational.NewAccountRepository(database)
	modelRepo := relational.NewModelRepository(database)
	auditRepo := relational.NewAuditRepository(database)
	keyRepo := relational.NewClientKeyRepository(database)
	credential, _, err := accountRepo.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderConsole, AuthType: account.AuthTypeSSO,
		Name: "console-media-billing", SourceKey: "console-media-billing", EncryptedAccessToken: "console-token",
		Enabled: true, AuthStatus: account.AuthStatusActive, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	const (
		imageModel = "grok-imagine-image"
		ttsModel   = "media-billing-tts"
	)
	if err := modelRepo.UpsertRoutes(ctx, []modeldomain.Route{
		{PublicID: imageModel, Provider: account.ProviderConsole, UpstreamModel: imageModel, Capability: modeldomain.CapabilityImage, Enabled: true},
		{PublicID: ttsModel, Provider: account.ProviderConsole, UpstreamModel: ttsModel, Capability: modeldomain.CapabilityTTS, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := modelRepo.ReplaceAccountCapabilities(ctx, credential.ID, []string{imageModel, ttsModel}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	key, err := keyRepo.Create(ctx, clientkey.Key{
		Name: "media-billing-key", Prefix: "media-billing", SecretHash: strings.Repeat("f", 64), EncryptedSecret: "encrypted",
		Enabled: true, BillingLimitUSDTicks: 10_000_000_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	reservations := &reservationExpiryRecorder{ClientKeyRepository: keyRepo}
	registry := provider.NewRegistry(statelessConsoleAdapter{})
	sticky := memory.NewStickyStore()
	selector := NewSelector(accountRepo, memory.NewConcurrencyLimiter(), sticky, registry, time.Hour, time.Second, time.Minute)
	accountService := accountapp.NewService(accountRepo, auditRepo, memory.NewDeviceSessionStore(), sticky, registry, testCipher(t), nil)
	service := NewService(modelRepo, auditRepo, accountService, clientkeyapp.NewService(reservations, nil, nil, 60, 4, nil), registry, selector, nil, 1)
	service.UpdateRequestTimeout(3 * time.Hour)
	wantTTL := service.syncBillingReservationTTL()
	if wantTTL >= mediaBillingReservationTTL {
		t.Fatalf("fixture TTL %s must be shorter than the async media TTL", wantTTL)
	}
	assertRequestScopedReservation := func(label string, startedAt time.Time) {
		t.Helper()
		ttl := reservations.last(t).Sub(startedAt)
		if ttl < wantTTL-time.Second || ttl > wantTTL+time.Minute {
			t.Fatalf("%s reservation TTL = %s, want about %s (not the %s async media TTL)", label, ttl, wantTTL, mediaBillingReservationTTL)
		}
	}

	imagePrice, ok := audit.EstimateOfficialImageCost(imageModel, "", "", 1)
	if !ok {
		t.Fatal("image pricing unavailable")
	}
	runImage := func(requestID string, streaming bool, errorCode string) audit.Record {
		t.Helper()
		startedAt := time.Now().UTC()
		result, err := service.executeImage(ctx, requestID, key, imageModel, audit.OperationImage, modeldomain.CapabilityImage, func(account.Provider) bool {
			return true
		}, func(context.Context, account.Provider, account.Credential, string) (*provider.Response, error) {
			return &provider.Response{
				StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"data":[{"url":"https://example.com/a.png"}]}`)), QuotaUnits: 1,
			}, nil
		}, streaming, "", "", 1, 0, "", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		assertRequestScopedReservation(requestID, startedAt)
		_, _ = io.ReadAll(result.Body)
		result.Finalize(Usage{}, "", errorCode)
		_ = result.Body.Close()
		logs, _, err := auditRepo.List(ctx, 0, 1)
		if err != nil || len(logs) != 1 || logs[0].RequestID != requestID {
			t.Fatalf("%s audit = %#v, err = %v", requestID, logs, err)
		}
		return logs[0]
	}
	if record := runImage("req-image-delivery-cut", false, "client_stream_interrupted"); record.MediaOutputImages != 1 || record.EstimatedCostInUSDTicks != imagePrice.CostInUSDTicks {
		t.Fatalf("non-streaming image cut during delivery = %#v, want billed %d", record, imagePrice.CostInUSDTicks)
	}
	if record := runImage("req-image-stream-cut", true, "client_stream_interrupted"); record.MediaOutputImages != 0 || record.EstimatedCostInUSDTicks != 0 {
		t.Fatalf("streaming image may be cut before generation finishes and must not be billed: %#v", record)
	}

	ttsPricing, ok := audit.EstimateOfficialTTSCost("Hello 世界")
	if !ok {
		t.Fatal("TTS pricing unavailable")
	}
	startedAt := time.Now().UTC()
	ttsResult, err := service.executeVoice(ctx, "req-tts-delivery-cut", key, ttsModel, audit.OperationTTS, modeldomain.CapabilityTTS, true, ttsPricing, "", "", nil, func(account.Provider) bool {
		return true
	}, func(context.Context, account.Provider, account.Credential, string) (voiceExecutionResult, error) {
		return voiceExecutionResult{response: jsonVoiceResponse(http.StatusOK, map[string]any{"ok": true}), pricing: ttsPricing}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRequestScopedReservation("TTS", startedAt)
	ttsResult.Finalize(Usage{}, "", "stream_interrupted")
	_ = ttsResult.Body.Close()

	settled, err := keyRepo.Get(ctx, key.ID)
	wantBilled := imagePrice.CostInUSDTicks + ttsPricing.CostInUSDTicks
	if err != nil || settled.BilledUsageUSDTicks != wantBilled || settled.ReservedUsageUSDTicks != 0 {
		t.Fatalf("media billing = %#v, err = %v, want billed %d reserved 0", settled, err, wantBilled)
	}
}
