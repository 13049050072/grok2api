package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"
	fhttptest "github.com/bogdanfinn/fhttp/httptest"
	"github.com/bogdanfinn/websocket"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	egressdomain "github.com/chenyme/grok2api/backend/internal/domain/egress"
	infraegress "github.com/chenyme/grok2api/backend/internal/infra/egress"
	"github.com/chenyme/grok2api/backend/internal/infra/provider"
	"github.com/chenyme/grok2api/backend/internal/infra/provider/conversation"
	"github.com/chenyme/grok2api/backend/internal/infra/security"
)

func TestWebResponseErrorClassifiesUpstreamBusinessErrors(t *testing.T) {
	antiBot := webResponseError(map[string]any{"message": "Request rejected by anti-bot rules.", "code": float64(7)})
	if !errors.Is(antiBot, errWebAntiBot) || provider.IsUpstreamBusinessError(antiBot) {
		t.Fatalf("anti-bot error = %v (business=%v)", antiBot, provider.IsUpstreamBusinessError(antiBot))
	}
	usage := webResponseError(map[string]any{"message": "You have reached your usage limit"})
	if !errors.Is(usage, errWebUsageLimit) || !provider.IsUpstreamBusinessError(usage) || !strings.Contains(usage.Error(), "reached your usage limit") {
		t.Fatalf("usage-limit error = %v", usage)
	}
	generic := webResponseError(map[string]any{"message": "model overloaded"})
	if !provider.IsUpstreamBusinessError(generic) || generic.Error() != "model overloaded" {
		t.Fatalf("generic error = %v", generic)
	}
	if err := modelResponseStreamError(map[string]any{"streamErrors": []any{"Usage limit reached for this model"}}); !errors.Is(err, errWebUsageLimit) {
		t.Fatalf("string stream error = %v, want usage limit", err)
	}
	for _, err := range []error{
		gatewayEventError(map[string]any{"type": "error"}),
		func() error {
			_, _, err := parseGatewayEvent(map[string]any{"type": "response.done", "response": map[string]any{"status": "failed"}}, &parsedChat{})
			return err
		}(),
	} {
		if !provider.IsUpstreamBusinessError(err) {
			t.Fatalf("gateway error %v is not classified as an upstream business error", err)
		}
	}
}

const (
	testGatewaySessionCreated = `{"session_id":"conv_1","event":{"type":"session.created"}}`
	testGatewayAttached       = `{"session_id":"conv_1","event":{"type":"conversation.attached","conversation":{"id":"conv_1"}}}`
)

func TestPreflightRejectsAccountErrorsAfterGatewayLifecycleFrames(t *testing.T) {
	for _, test := range []struct {
		name  string
		frame string
		want  error
	}{
		{name: "usage limit error event", frame: `{"session_id":"conv_1","event":{"type":"error","error":{"message":"You have reached your usage limit"}}}`, want: errWebUsageLimit},
		{name: "anti-bot error event", frame: `{"session_id":"conv_1","event":{"type":"error","error":{"message":"Request rejected by anti-bot rules.","code":7}}}`, want: errWebAntiBot},
		{name: "usage limit stream error", frame: `{"session_id":"conv_1","event":{"type":"response.grok.output","output":{"stream_error":{"message":"usage quota exceeded"}}}}`, want: errWebUsageLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := io.NopCloser(strings.NewReader(testGatewaySessionCreated + "\n" + testGatewayAttached + "\n" + test.frame + "\n"))
			if stream, err := preflightUpstream(source); !errors.Is(err, test.want) || stream != nil {
				t.Fatalf("preflight = %v, %v; want %v", stream, err, test.want)
			}
		})
	}
}

func TestPreflightDetectsResultResponseError(t *testing.T) {
	source := io.NopCloser(strings.NewReader(`{"result":{"response":{"error":{"message":"usage limit reached"}}}}` + "\n"))
	if _, err := preflightUpstream(source); !errors.Is(err, errWebUsageLimit) {
		t.Fatalf("error = %v, want usage limit", err)
	}
	other := `{"result":{"response":{"error":{"message":"model overloaded"}}}}` + "\n"
	stream, err := preflightUpstream(io.NopCloser(strings.NewReader(other)))
	if err != nil {
		t.Fatalf("non-account result error must stay in the stream: %v", err)
	}
	if data, _ := io.ReadAll(stream); string(data) != other {
		t.Fatalf("stream = %q", data)
	}
}

func TestPreflightKeepsOtherFramesInStream(t *testing.T) {
	for _, frame := range []string{
		`{"session_id":"conv_1","event":{"type":"error","error":{"message":"model overloaded"}}}`,
		`{"session_id":"conv_1","event":{"type":"response.chunk","chunk":{"text":{"text":"hi","channel":"CHANNEL_ASSISTANT_RESPONSE"}}}}`,
	} {
		input := testGatewaySessionCreated + "\n" + testGatewayAttached + "\n" + frame + "\n"
		stream, err := preflightUpstream(io.NopCloser(strings.NewReader(input)))
		if err != nil {
			t.Fatalf("preflight(%s) error = %v", frame, err)
		}
		if data, _ := io.ReadAll(stream); string(data) != input {
			t.Fatalf("stream = %q, want every prefetched frame", data)
		}
	}
	firstFrameError := `{"session_id":"conv_1","event":{"type":"error","error":{"message":"model overloaded"}}}` + "\n"
	if _, err := preflightUpstream(io.NopCloser(strings.NewReader(firstFrameError))); err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("first-frame error = %v", err)
	}
}

func TestForwardResponseAnswersGatewayUsageLimitWith429(t *testing.T) {
	server := fhttptest.NewServer(fhttp.HandlerFunc(func(writer fhttp.ResponseWriter, request *fhttp.Request) {
		if request.URL.Path != "/ws/mgw/" {
			fhttp.NotFound(writer, request)
			return
		}
		connection, err := (&websocket.Upgrader{CheckOrigin: func(*fhttp.Request) bool { return true }}).Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		var initial map[string]any
		if err := connection.ReadJSON(&initial); err != nil {
			return
		}
		initialID, _ := initial["event"].(map[string]any)["event_id"].(string)
		_ = connection.WriteJSON(map[string]any{"session_id": "conv_1", "event": map[string]any{"type": "session.created", "client_event_id": initialID}})
		_ = connection.WriteJSON(map[string]any{"session_id": "conv_1", "event": map[string]any{"type": "conversation.attached", "conversation": map[string]any{"id": "conv_1"}}})
		var turn map[string]any
		for range 2 {
			if err := connection.ReadJSON(&turn); err != nil {
				return
			}
		}
		_ = connection.WriteJSON(map[string]any{"session_id": "conv_1", "event": map[string]any{"type": "error", "error": map[string]any{"message": "You have reached your usage limit"}}})
	}))
	defer server.Close()

	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("test-sso")
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewAdapter(Config{BaseURL: server.URL, StatsigMode: "manual", ChatTimeoutSeconds: 5}, infraegress.NewManager(egressRepositoryStub{}, cipher), cipher, nil, nil)
	for _, streaming := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			response, err := adapter.ForwardResponse(context.Background(), provider.ResponseResourceRequest{
				Credential: account.Credential{ID: 1, Provider: account.ProviderWeb, UserID: "497f19f8-49d4-458a-bee4-43ec3dcaf8ca", EncryptedAccessToken: encrypted},
				Method:     http.MethodPost, Path: "/responses", Model: "grok-chat-fast", Operation: conversation.OperationResponses,
				Body: []byte(fmt.Sprintf(`{"model":"grok-chat-fast","input":"hello","stream":%v}`, streaming)), Streaming: streaming,
			})
			if err != nil {
				t.Fatalf("ForwardResponse() error = %v, want a 429 response", err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if response.StatusCode != http.StatusTooManyRequests || !strings.Contains(string(body), `"usage_limit_reached"`) {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
		})
	}
}

func TestStreamingPreflightReportsGatewayTransportFailure(t *testing.T) {
	server := fhttptest.NewServer(fhttp.HandlerFunc(func(writer fhttp.ResponseWriter, request *fhttp.Request) {
		if request.URL.Path != "/ws/mgw/" {
			fhttp.NotFound(writer, request)
			return
		}
		connection, err := (&websocket.Upgrader{CheckOrigin: func(*fhttp.Request) bool { return true }}).Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		var initial map[string]any
		if err := connection.ReadJSON(&initial); err != nil {
			return
		}
		initialID, _ := initial["event"].(map[string]any)["event_id"].(string)
		_ = connection.WriteJSON(map[string]any{"session_id": "conv_1", "event": map[string]any{"type": "session.created", "client_event_id": initialID}})
		_ = connection.WriteJSON(map[string]any{"session_id": "conv_1", "event": map[string]any{"type": "conversation.attached", "conversation": map[string]any{"id": "conv_1"}}})
		var turn map[string]any
		for range 2 {
			if err := connection.ReadJSON(&turn); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cipher.Encrypt("test-sso")
	if err != nil {
		t.Fatal(err)
	}
	repository := &recordingWebEgressRepository{node: egressdomain.Node{ID: 1, Name: "web", Scope: egressdomain.ScopeWeb, Enabled: true, Health: 1}}
	adapter := NewAdapter(Config{BaseURL: server.URL, StatsigMode: "manual", ChatTimeoutSeconds: 5}, infraegress.NewManager(repository, cipher), cipher, nil, nil)
	response, err := adapter.ForwardResponse(context.Background(), provider.ResponseResourceRequest{
		Credential: account.Credential{ID: 1, Provider: account.ProviderWeb, UserID: "497f19f8-49d4-458a-bee4-43ec3dcaf8ca", EncryptedAccessToken: encrypted},
		Method:     http.MethodPost, Path: "/responses", Model: "grok-chat-fast", Operation: conversation.OperationResponses,
		Body: []byte(`{"model":"grok-chat-fast","input":"hello","stream":true}`), Streaming: true,
	})
	if err == nil {
		_ = response.Body.Close()
		t.Fatalf("ForwardResponse() status = %d, want the gateway read error", response.StatusCode)
	}
	repository.mu.Lock()
	node := repository.node
	repository.mu.Unlock()
	if node.LastError != egressdomain.LastErrorTransport || node.FailureCount != 1 {
		t.Fatalf("egress node after a transport failure before the first response frame = %+v", node)
	}
}

type feedbackRecordingRepository struct {
	egressRepositoryStub
	lookups atomic.Int32
}

func (r *feedbackRecordingRepository) GetEgressNode(context.Context, uint64) (egressdomain.Node, error) {
	r.lookups.Add(1)
	return egressdomain.Node{}, errors.New("not found")
}

func TestReportUpstreamFailureSkipsCancellationsAndBusinessErrors(t *testing.T) {
	cipher, err := security.NewCipher(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		err      error
		reported bool
	}{
		{name: "transport", ctx: context.Background(), err: errors.New("read: connection reset by peer"), reported: true},
		{name: "anti-bot", ctx: context.Background(), err: webResponseError(map[string]any{"message": "anti-bot", "code": float64(7)}), reported: true},
		{name: "client canceled", ctx: canceled, err: errors.New("read: use of closed network connection")},
		{name: "downstream closed", ctx: context.Background(), err: io.ErrClosedPipe},
		{name: "usage limit", ctx: context.Background(), err: webResponseError(map[string]any{"message": "usage limit reached"})},
		{name: "upstream error frame", ctx: context.Background(), err: webResponseError(map[string]any{"message": "model overloaded"})},
		{name: "gateway session ended", ctx: context.Background(), err: provider.NewUpstreamBusinessError(errors.New("Grok Gateway session 在响应完成前结束"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &feedbackRecordingRepository{}
			adapter := NewAdapter(Config{}, infraegress.NewManager(repository, cipher), cipher, nil, nil)
			adapter.reportUpstreamFailure(test.ctx, 7, test.err)
			if reported := repository.lookups.Load() > 0; reported != test.reported {
				t.Fatalf("egress feedback reported = %v, want %v", reported, test.reported)
			}
		})
	}
}

func TestInspectImageEditCaptureMatchesSeparatePasses(t *testing.T) {
	capture := []byte(strings.Join([]string{
		`{"result":{"response":{"userResponse":{"message":"edit","mediaGenInput":{"imageToImage":{"inputAssets":["asset-1","asset-2"]}},"metadata":{"modelConfigOverride":{"modelMap":{"imageEditModelConfig":{"resolvedImageReferences":["a","b"]}}}}}}}}`,
		`{"result":{"response":{"streamingImageGenerationResponse":{"imageUrl":"users/u/generated/edit-part-1/image.jpg","progress":40}}}}`,
		`{"result":{"response":{"streamingImageGenerationResponse":{"imageUrl":"users/u/generated/moderated/image.jpg","progress":100,"moderated":true}}}}`,
		`{"result":{"response":{"streamingImageGenerationResponse":{"imageUrl":"users/u/generated/final/image.jpg","progress":100}}}}`,
		`{"result":{"response":{"modelResponse":{"message":"{\"url\":\"users/u/generated/nested/image.jpg\",\"progress\":100}"}}}}`,
	}, "\n"))
	report := inspectImageEditCapture(capture, []string{"asset-1", "asset-2"}, "edit", true)
	if !report.userSeen || !report.referencesMatch || report.resolvedCount != 2 || !report.promptMatched {
		t.Fatalf("binding report = %+v", report)
	}
	if !slices.Equal(report.capturedURLs, extractCapturedImageURLs(capture)) || len(report.capturedURLs) == 0 {
		t.Fatalf("captured URLs = %#v, want %#v", report.capturedURLs, extractCapturedImageURLs(capture))
	}
	final := "https://assets.grok.com/users/u/generated/final/image.jpg"
	if !report.finalFrameSeen() || !slices.Equal(report.finalURLs, []string{final}) {
		t.Fatalf("final URLs = %#v", report.finalURLs)
	}
	parsed := &parsedChat{}
	if urls := selectImageEditResultURLs(parsed, report.capturedURLs); !slices.Equal(urls, imageEditResultURLs(parsed, capture)) {
		t.Fatalf("selected URLs = %#v, legacy = %#v", urls, imageEditResultURLs(parsed, capture))
	}
	moderated := "https://assets.grok.com/users/u/generated/moderated/image.jpg"
	if !report.selectedMatchesFinal([]string{final, moderated}) || report.selectedMatchesFinal([]string{moderated, final}) || report.selectedMatchesFinal(nil) {
		t.Fatalf("selectedMatchesFinal disagrees with final URLs %#v", report.finalURLs)
	}
	if skipped := inspectImageEditCapture(capture, nil, "edit", false); skipped.capturedURLs != nil || skipped.referencesMatch {
		t.Fatalf("report without URL collection = %+v", skipped)
	}
}
