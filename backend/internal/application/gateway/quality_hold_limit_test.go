package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestQualityHoldHardLimitBoundsEveryHoldTimeout(t *testing.T) {
	tests := []struct {
		hold time.Duration
		want time.Duration
	}{
		{hold: 200 * time.Millisecond, want: 2200 * time.Millisecond},
		{hold: time.Second, want: 3 * time.Second},
		{hold: 10 * time.Second, want: 20 * time.Second},
		{hold: defaultQualityHoldTimeout, want: qualityHoldHardLimitMax},
		{hold: 45 * time.Second, want: qualityHoldHardLimitMax},
	}
	for _, test := range tests {
		if got := qualityHoldHardLimit(test.hold); got != test.want {
			t.Fatalf("hard limit for %s = %s, want %s", test.hold, got, test.want)
		}
	}
}

func TestPeekQualityStreamHardLimitDeliversUndecidedThinkingStream(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	defer writer.Close()
	// Cipher-only thinking with no visible text stays undecided after the soft
	// timeout; the upstream then goes quiet without closing the stream.
	stream := sse(
		`data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning"}}`,
		`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"`+strings.Repeat("e", 300)+`"}}`,
	)
	go func() { _, _ = io.WriteString(writer, stream) }()
	cfg := QualityRetryRuntime{MinOutputTokens: 32, HoldTimeout: 200 * time.Millisecond}
	started := time.Now()
	replay, verdict, _, _, err := peekQualityStream(context.Background(), reader, qualityProtocolResponses, cfg)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	limit := qualityHoldHardLimit(cfg.HoldTimeout)
	if elapsed < limit-100*time.Millisecond || elapsed > limit+time.Second {
		t.Fatalf("peek returned after %s, want the %s hard limit", elapsed, limit)
	}
	if verdict != QualityDeliver {
		t.Fatalf("undecided thinking stream at the hard limit = %s, want deliver", verdict)
	}
	_ = writer.Close()
	body, err := io.ReadAll(replay)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != stream {
		t.Fatalf("held prefix was not replayed: %q", body)
	}
}

func TestPeekQualityStreamHardLimitKeepsSilentStreamClosed(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	defer writer.Close()
	go func() {
		_, _ = io.WriteString(writer, sse(`data: {"type":"response.created","response":{"id":"resp_1"}}`))
	}()
	cfg := QualityRetryRuntime{MinOutputTokens: 32, HoldTimeout: 200 * time.Millisecond}
	replay, verdict, _, _, err := peekQualityStream(context.Background(), reader, qualityProtocolResponses, cfg)
	if replay != nil {
		_ = replay.Close()
	}
	if !errors.Is(err, errQualityEmptyStream) || verdict != QualityWait {
		t.Fatalf("silent stream at the hard limit = %s, %v; want wait with the empty-stream error", verdict, err)
	}
}
