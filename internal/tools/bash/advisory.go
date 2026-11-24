// advisory.go — the once-per-session gate on the success-path advisory.
//
// Why this exists at all: measured 2026-11-22, one session fired the
// cd-prefix advisory 574 times. The per-firing cost is small (a line),
// but the total is not: the advisory accounted for 410,967 bytes across
// 40 sessions — 3.1% of every tool-result byte ever sent — and each
// firing lands in HISTORY, so it is re-sent on every later request of
// that session. A nudge nobody acts on, repeated 574 times, is the most
// expensive kind of text in the transcript.
//
// The cap is per CLASS per session, not per command: one firing is
// enough to teach (if this mechanism can teach at all — see hint.go's
// measured table), and the model does not need the sixth reminder. A
// session-scoped limiter rather than a package-level global because
// tests and parallel sessions must not share it; it hangs off Tool so
// its lifetime IS the tool's lifetime.

package bash

import "sync"

// advisoryClass enumerates the classes that can fire. Kept as a typed
// enum rather than strings so a typo is a compile error and the
// limiter's map cannot grow an accidental key.
type advisoryClass int

const (
	advisoryRead advisoryClass = iota
	advisoryCD
	// advisoryClassCount is the number of classes; the bitmask tests
	// below rely on it staying LAST in the const block.
	advisoryClassCount
)

// advisoryLimiter remembers which classes have already fired in this
// session. Safe for concurrent use (the bash tool itself is not
// dispatched in parallel, but the guard is cheap and a future
// read-only bash fast path must not have to revisit this).
type advisoryLimiter struct {
	mu    sync.Mutex
	fired [advisoryClassCount]bool
}

// allow reports whether c may fire now, and records the firing when it
// returns true. A nil limiter allows everything — that is the
// uncapped behaviour for a Tool built as a zero value (tests, and any
// caller that never went through New).
func (l *advisoryLimiter) allow(c advisoryClass) bool {
	if l == nil {
		return true
	}
	if c < 0 || c >= advisoryClassCount {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fired[c] {
		return false
	}
	l.fired[c] = true
	return true
}
