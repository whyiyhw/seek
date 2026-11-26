package deepseek

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// Retry policy for transient DeepSeek failures (HTTP 5xx, 429, and
// transport errors like connection resets). One retry catches the common
// upstream-blip case; more would just amplify cost when the API is
// genuinely down. Safe because every re-sent body is byte-identical
// (prefix cache stays hot) and retry only fires before any caller-
// visible state is committed.
//
// The backoff is failure-class-dependent, mirroring
// pkg/llm/provider/openai/client.go's policy (the two packages keep
// identical retry shapes — pkg/deepseek may not import pkg/llm, so the
// rule is duplicated by hand, not shared). A fast rejection (reset, EOF,
// 5xx body) is a blip: 500ms. A TIMEOUT — net.Error whose Timeout() is
// true, which is what a stalled TLS handshake or a dead proxy reports —
// consumed its whole budget producing nothing, so retrying 500ms later
// walks back into the same stall; it gets a longer wait instead.
const (
	maxRetries   = 1
	retryBackoff = 500 * time.Millisecond
	// timeoutBackoff follows a TIMED-OUT attempt. See above.
	timeoutBackoff = 3 * time.Second
)

// backoffFor picks the wait before the next attempt: a timeout gets the
// long one, everything else the fixed short blip backoff.
func backoffFor(lastErr error) time.Duration {
	var ne net.Error
	if errors.As(lastErr, &ne) && ne.Timeout() {
		return timeoutBackoff
	}
	return retryBackoff
}

// retryCall runs op until it succeeds, retrying transient failures once
// with a class-dependent backoff (see backoffFor). On exhaustion the
// returned error is annotated
// with the number of attempts made ("… (after N attempts)") so callers
// can tell a one-shot upstream blip from an endpoint that stayed down
// across every attempt. ctx cancellation aborts immediately and
// unannotated (the caller already knows why it stopped).
func retryCall[T any](ctx context.Context, op func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	attempts := 0
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attempts++
		if attempt > 0 {
			if err := sleepCtx(ctx, backoffFor(lastErr)); err != nil {
				return zero, err
			}
		}
		v, err := op()
		if err == nil {
			return v, nil
		}
		lastErr = err
		if ctx.Err() != nil || !isRetryable(err) {
			return zero, err
		}
	}
	return zero, fmt.Errorf("%w (after %d attempts)", lastErr, attempts)
}

// isRetryable classifies a failure as transient. API errors are retryable
// only for 5xx / 429 (quota is worth one retry; other 4xx are
// configuration problems that retrying can't fix). Anything else — EOF,
// connection reset, read failures, decode failures — is transport-level
// and retried. Decode failures retry harmlessly: the second attempt
// fails identically, just annotated.
func isRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode/100 == 5 || apiErr.StatusCode == 429
	}
	return true
}
