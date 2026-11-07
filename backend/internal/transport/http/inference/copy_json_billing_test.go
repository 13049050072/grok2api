package inference

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

var errClientGone = errors.New("client connection closed")

type failingResponseWriter struct{ header http.Header }

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, errClientGone }

func (w *failingResponseWriter) WriteHeader(int) {}

func TestCopyJSONKeepsUsageWhenClientWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payload := []byte(`{"id":"resp_1","object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":120,"output_tokens":45,"total_tokens":165}}`)
	context, _ := gin.CreateTestContext(&failingResponseWriter{})

	metadata, err := copyJSON(context.Writer, bytes.NewReader(payload), streamProtocolResponses)
	if !errors.Is(err, errClientGone) {
		t.Fatalf("copy error = %v", err)
	}
	if !metadata.Usage.Reported || metadata.Usage.InputTokens != 120 || metadata.Usage.OutputTokens != 45 {
		t.Fatalf("usage after failed client write = %#v", metadata.Usage)
	}
	if metadata.ResponseID != "resp_1" {
		t.Fatalf("response id = %q", metadata.ResponseID)
	}
}
