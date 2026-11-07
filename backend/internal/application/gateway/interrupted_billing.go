package gateway

import (
	"net/http"

	"github.com/chenyme/grok2api/backend/internal/domain/audit"
)

// interruptedStreamErrorCodes are 2xx stream endings after which generated
// output may already have reached the client while the terminal usage frame
// never arrived. Explicit upstream failures (upstream_stream_error) and empty
// responses are excluded: the former may carry its own usage and the latter
// delivered nothing.
var interruptedStreamErrorCodes = map[string]struct{}{
	"client_stream_interrupted":    {},
	"stream_interrupted":           {},
	"response_too_large":           {},
	"upstream_stream_interrupted":  {},
	"upstream_stream_incomplete":   {},
	"upstream_stream_idle_timeout": {},
	"upstream_output_loop":         {},
}

// estimateInterruptedStreamUsage replaces missing usage of an interrupted 2xx
// stream with the same byte heuristic used for reservations. Reported input
// usage is kept; output is derived from forwarded visible text and capped only
// by the client's explicit output limit. It deliberately does not cap at the
// reservation: settlement of reported usage is uncapped as well, and the
// reservation is an admission check rather than a price ceiling.
func estimateInterruptedStreamUsage(usage Usage, statusCode int, errorCode string, body []byte, contextInputTokens int64) (Usage, bool) {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices || usage.ForwardedOutputBytes <= 0 {
		return usage, false
	}
	if _, ok := interruptedStreamErrorCodes[errorCode]; !ok {
		return usage, false
	}
	if usage.Reported && (usage.OutputTokens > 0 || usage.ReasoningTokens > 0) {
		return usage, false
	}
	estimated := usage
	estimated.Reported = true
	if estimated.InputTokens <= 0 {
		estimated.InputTokens = audit.EstimateRequestInputTokens(body) + max(0, contextInputTokens)
		estimated.CachedInputTokens = 0
	}
	outputTokens := audit.EstimateTextTokens(usage.ForwardedOutputBytes)
	if limit, ok := audit.RequestOutputTokenLimit(body); ok {
		outputTokens = min(outputTokens, limit)
	}
	estimated.OutputTokens = outputTokens
	estimated.TotalTokens = estimated.InputTokens + outputTokens
	return estimated, true
}

// mediaDeliveryInterrupted reports whether a fully generated 2xx media
// response failed only while it was being written to the client.
func mediaDeliveryInterrupted(statusCode int, errorCode string) bool {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return false
	}
	return errorCode == "client_stream_interrupted" || errorCode == "stream_interrupted"
}
