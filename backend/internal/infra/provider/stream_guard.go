package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
)

// ErrStreamPanic is the pipe error a consumer observes when a stream producer
// goroutine panicked.
var ErrStreamPanic = errors.New("上游响应流处理异常")

// RecoverStreamPanic must be deferred directly (not from a wrapping closure) by
// a goroutine that writes a response body into writer, so a panic fails that
// one stream instead of terminating the process.
func RecoverStreamPanic(writer *io.PipeWriter, logger *slog.Logger, stream string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("provider_stream_panic", "stream", stream, "panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
	if writer != nil {
		_ = writer.CloseWithError(fmt.Errorf("%w: %s", ErrStreamPanic, stream))
	}
}

// UpstreamBusinessError marks an error the upstream reported in a well-formed
// response (quota, policy, invalid request, ...). It says nothing about the
// health of the egress node that carried the exchange.
type UpstreamBusinessError struct {
	err error
}

func (e *UpstreamBusinessError) Error() string { return e.err.Error() }

func (e *UpstreamBusinessError) Unwrap() error { return e.err }

// NewUpstreamBusinessError wraps err without changing its message or its
// errors.Is identity.
func NewUpstreamBusinessError(err error) error {
	if err == nil || IsUpstreamBusinessError(err) {
		return err
	}
	return &UpstreamBusinessError{err: err}
}

func IsUpstreamBusinessError(err error) bool {
	var target *UpstreamBusinessError
	return errors.As(err, &target)
}

// ShouldReportEgressFailure reports whether err should count as a transport
// failure of the egress node. Client cancellation (including a body closed by
// the consumer and connections closed because ctx ended) and upstream business
// errors are excluded.
func ShouldReportEgressFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe) {
		return false
	}
	return !IsUpstreamBusinessError(err)
}
