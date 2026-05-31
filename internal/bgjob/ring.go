package bgjob

import "sync"

// ringBuffer is a bounded, monotonic-offset output buffer for one
// background job. Writes append; once the buffer exceeds cap the oldest
// bytes are dropped from the front (recording how many) so total memory
// stays bounded — this is the write-time cap that keeps long-running
// jobs from ballooning history (PRD §3 "输出 write-time cap").
//
// Reads use an absolute monotonic cursor (total bytes ever written): a
// reader passes the cursor it last saw and gets back everything since,
// plus a gap count when the requested start fell into already-dropped
// territory. Concurrency-safe: stdout and stderr pipe goroutines both
// write here while the agent goroutine reads.
type ringBuffer struct {
	mu      sync.Mutex
	data    []byte
	cap     int
	dropped int64 // bytes discarded from the front (monotonic)
}

func newRing(capacity int) *ringBuffer {
	if capacity <= 0 {
		capacity = 64 * 1024
	}
	return &ringBuffer{cap: capacity}
}

// Write implements io.Writer. Appends p, then trims the front if the
// retained window grew past cap, accumulating the trimmed count into
// dropped so absolute offsets stay correct.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = append(r.data, p...)
	if len(r.data) > r.cap {
		n := len(r.data) - r.cap
		copy(r.data, r.data[n:]) // shift the kept tail to the front
		r.data = r.data[:r.cap]
		r.dropped += int64(n)
	}
	return len(p), nil
}

// readFrom returns the bytes from absolute offset cursor to the current
// end, the new end offset, and gap = how many bytes between cursor and
// the oldest retained byte were already dropped (0 when none). The
// returned window is a fresh copy, safe to use after the lock releases.
func (r *ringBuffer) readFrom(cursor int64) (window []byte, newCursor int64, gap int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := r.dropped + int64(len(r.data))
	if cursor >= total {
		return nil, total, 0
	}
	start := cursor
	if start < r.dropped {
		gap = r.dropped - start
		start = r.dropped
	}
	out := make([]byte, total-start)
	copy(out, r.data[start-r.dropped:])
	return out, total, gap
}

// snapshot returns a copy of the entire retained window. Used by
// until_regex matching, which scans the current buffer each tick.
func (r *ringBuffer) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.data))
	copy(out, r.data)
	return out
}
