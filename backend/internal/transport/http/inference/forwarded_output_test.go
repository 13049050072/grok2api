package inference

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCopyStreamCountsForwardedVisibleOutputBeforeInterruption(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sse := func(events ...string) string {
		return strings.Join(events, "\n\n") + "\n\n"
	}
	tests := []struct {
		name     string
		protocol streamProtocol
		body     string
		want     int64
	}{
		{
			name: "responses", protocol: streamProtocolResponses,
			body: sse(
				`data: {"type":"response.created","response":{"id":"resp_cut"}}`,
				`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning"}}`,
				`data: {"type":"response.reasoning_summary_text.delta","delta":"think"}`,
				`data: {"type":"response.output_text.delta","delta":"héllo"}`,
				`data: {"type":"response.function_call_arguments.delta","delta":"{\"a\":1}"}`,
			),
			want: int64(len("think") + len("héllo") + len(`{"a":1}`)),
		},
		{
			name: "chat", protocol: streamProtocolChat,
			body: sse(
				`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
				`data: {"choices":[{"delta":{"reasoning_content":"abc","reasoning":"abc"}}]}`,
				`data: {"choices":[{"delta":{"content":"hello"}}]}`,
				`data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{}"}}]}}]}`,
			),
			want: int64(len("abc") + len("hello") + len("{}")),
		},
		{
			name: "anthropic", protocol: streamProtocolAnthropic,
			body: sse(
				`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":12,"output_tokens":1}}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hi!"}}`,
				`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
			),
			want: int64(len("hmm") + len("hi!") + len(`{"q":`)),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			metadata, err := copyStream(context.Writer, &chunkErrorReader{data: []byte(test.body)}, test.protocol, nil)
			if !errors.Is(err, errUpstreamStreamRead) {
				t.Fatalf("copy error = %v", err)
			}
			if !metadata.Usage.OutputObserved || metadata.Usage.ForwardedOutputBytes != test.want {
				t.Fatalf("forwarded output = %d bytes (observed %t), want %d", metadata.Usage.ForwardedOutputBytes, metadata.Usage.OutputObserved, test.want)
			}
		})
	}
}
