package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestIndexFoldASCIIOffsetsSliceOriginal(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, value, substr string
		want                int
	}{
		{name: "ascii", value: "abc RESETS IN: 5s", substr: "resets in:", want: 4},
		{name: "rune grows when lowered", value: "ȺȺȺ Resets in: 5s", substr: "resets in:", want: 7},
		{name: "rune shrinks when lowered", value: "\u212a\u212a resets in: 5s", substr: "resets in:", want: 7},
		{name: "invalid utf8", value: "\xff\xfe<TOOL_CALLS>", substr: "<tool_calls", want: 2},
		{name: "missing", value: "Ⱥ nothing here", substr: "resets in:", want: -1},
		{name: "kelvin is not ascii k", value: "\u212aey", substr: "key", want: -1},
		{name: "empty substr", value: "x", substr: "", want: 0},
	} {
		if got := IndexFoldASCII(test.value, test.substr); got != test.want {
			t.Fatalf("%s: IndexFoldASCII = %d, want %d", test.name, got, test.want)
		}
		if test.want > 0 && !HasPrefixFoldASCII(test.value[test.want:], test.substr) {
			t.Fatalf("%s: offset %d does not point at the match in the original string", test.name, test.want)
		}
	}
}

func TestRateLimitResetAfterWithNonASCIIPrefix(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{strings.Repeat("Ⱥ", 16), strings.Repeat("\u212a", 16)} {
		body := prefix + " Requests per Minute (actual/limit): 5/5. Resets in: 1m30s"
		if got := rateLimitResetAfter(body); got != 90*time.Second {
			t.Fatalf("prefix %q: resetAfter = %s, want 1m30s", prefix[:3], got)
		}
	}
}

func TestRecoverStreamPanicClosesPipe(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	reader, writer := io.Pipe()
	go func() {
		defer RecoverStreamPanic(writer, logger, "test_stream")
		_, _ = writer.Write([]byte("partial"))
		var values []int
		_ = values[3]
	}()
	data, err := io.ReadAll(reader)
	if string(data) != "partial" {
		t.Fatalf("data = %q", data)
	}
	if !errors.Is(err, ErrStreamPanic) || !strings.Contains(err.Error(), "test_stream") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(logs.String(), "provider_stream_panic") || !strings.Contains(logs.String(), "index out of range") {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestRecoverStreamPanicWithoutPanicKeepsStream(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	go func() {
		defer RecoverStreamPanic(writer, nil, "test_stream")
		_, _ = writer.Write([]byte("done"))
		_ = writer.Close()
	}()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "done" {
		t.Fatalf("data = %q, err = %v", data, err)
	}
}

func TestShouldReportEgressFailure(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	business := NewUpstreamBusinessError(errors.New("usage limit"))
	for _, test := range []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{name: "nil error", ctx: context.Background(), err: nil, want: false},
		{name: "transport", ctx: context.Background(), err: errors.New("connection reset by peer"), want: true},
		{name: "deadline with live context", ctx: context.Background(), err: context.DeadlineExceeded, want: true},
		{name: "unexpected eof", ctx: context.Background(), err: io.ErrUnexpectedEOF, want: true},
		{name: "canceled context", ctx: canceled, err: errors.New("use of closed network connection"), want: false},
		{name: "context canceled error", ctx: context.Background(), err: fmt.Errorf("read: %w", context.Canceled), want: false},
		{name: "consumer closed pipe", ctx: context.Background(), err: io.ErrClosedPipe, want: false},
		{name: "business", ctx: context.Background(), err: business, want: false},
		{name: "wrapped business", ctx: context.Background(), err: fmt.Errorf("stream: %w", business), want: false},
		{name: "nil context", ctx: nil, err: errors.New("boom"), want: true},
	} {
		if got := ShouldReportEgressFailure(test.ctx, test.err); got != test.want {
			t.Fatalf("%s: ShouldReportEgressFailure = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestUpstreamBusinessErrorPreservesMessageAndIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("Grok Web usage limit reached")
	wrapped := NewUpstreamBusinessError(fmt.Errorf("%w: quota", sentinel))
	if wrapped.Error() != "Grok Web usage limit reached: quota" || !errors.Is(wrapped, sentinel) || !IsUpstreamBusinessError(wrapped) {
		t.Fatalf("wrapped = %v", wrapped)
	}
	if NewUpstreamBusinessError(wrapped) != wrapped || NewUpstreamBusinessError(nil) != nil {
		t.Fatal("NewUpstreamBusinessError must be idempotent and nil-safe")
	}
	if IsUpstreamBusinessError(sentinel) {
		t.Fatal("plain error classified as business error")
	}
}
