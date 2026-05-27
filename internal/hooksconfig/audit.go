package hooksconfig

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AuditEntry mirrors the JSON shape documented in PRD §3.6. Field names
// match the doc verbatim (wire format — once shipped, third-party
// monitoring tools may tail this file).
type AuditEntry struct {
	TS         string `json:"ts"`
	Event      string `json:"event"`
	Hook       string `json:"hook"`
	Tool       string `json:"tool,omitempty"`
	SessionID  string `json:"session_id"`
	DurationMs int64  `json:"duration_ms"`
	ExitCode   int    `json:"exit_code"`
	Denied     bool   `json:"denied"`
	// Reason is an optional human-readable explanation. Populated on
	// timeout / syntax-fail / static-skip so users tailing the log can
	// distinguish "hook ran and denied" from "hook never ran".
	Reason string `json:"reason,omitempty"`
}

// AuditLog is the append-only JSONL writer at ~/.seek/hooks-audit.jsonl.
// PRD §3.6 + verification criterion #8 require concurrent safety
// (multiple seek sessions tailing the same file).
//
// Strategy: open with O_APPEND once, hold a *sync.Mutex around the
// Write() so within a single process two goroutines can't interleave.
// Across processes, POSIX O_APPEND guarantees each Write is atomic up
// to PIPE_BUF (4 KiB on Linux/macOS) — our entries are well under that.
type AuditLog struct {
	mu   sync.Mutex
	w    io.WriteCloser
	now  func() time.Time
	path string
}

// NewAuditLog opens path (creating parent dirs as needed) in append
// mode. Returns nil + nil if path is empty so the wiring layer can
// disable audit logging by passing "".
func NewAuditLog(path string) (*AuditLog, error) {
	if path == "" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("hooksconfig: mkdir audit dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("hooksconfig: open audit log: %w", err)
	}
	return &AuditLog{w: f, now: time.Now, path: path}, nil
}

// Path returns the file path the log writes to. Useful for the CLI
// `seek hooks audit` which reads the same file.
func (a *AuditLog) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Append serialises entry to JSONL and writes one line. Thread-safe
// across goroutines in this process. Cross-process serialisation is
// best-effort via POSIX O_APPEND — adequate for typical hook payloads
// (well under 4 KiB).
func (a *AuditLog) Append(e AuditEntry) error {
	if a == nil {
		return nil
	}
	if e.TS == "" {
		e.TS = a.now().UTC().Format(time.RFC3339)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("hooksconfig: marshal audit: %w", err)
	}
	// Append exactly one newline. Marshal doesn't include trailing nl;
	// without it tail -f / wc -l would collapse adjacent entries.
	body = append(body, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = a.w.Write(body)
	if err != nil {
		return fmt.Errorf("hooksconfig: write audit: %w", err)
	}
	return nil
}

// Close flushes (no-op for unbuffered file) and closes the underlying
// file. Safe to call on a nil log.
func (a *AuditLog) Close() error {
	if a == nil || a.w == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	err := a.w.Close()
	a.w = nil
	return err
}

// ReadAuditLog opens path in read-only mode and yields every JSON
// line as an AuditEntry. Lines that fail JSON parsing are skipped
// (since the log is append-only, a half-written final line on crash
// is the only realistic corruption). ENOENT returns (nil, nil) so
// callers can show "no audit data yet" gracefully.
func ReadAuditLog(path string) ([]AuditEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("hooksconfig: open audit log: %w", err)
	}
	defer f.Close()
	var out []AuditEntry
	scanner := bufio.NewScanner(f)
	// Permit large lines (defaults to 64 KiB). Hook commands can be
	// long but >1 MiB lines are pathological; bump headroom to 1 MiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			// Skip corrupt lines silently — they were either a partial
			// write on crash or someone editing the file by hand. PRD
			// doesn't promise editor safety.
			continue
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("hooksconfig: scan audit log: %w", err)
	}
	return out, nil
}

// AverageDurationByHook computes the mean DurationMs grouped by Hook
// name across at most `limit` most-recent entries. `0` limit means "all
// entries". Used by `seek hooks list` to show the "近 50 次平均耗时"
// column called out in PRD §3.6.
func AverageDurationByHook(entries []AuditEntry, limit int) map[string]float64 {
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	sums := make(map[string]int64)
	counts := make(map[string]int)
	for _, e := range entries {
		sums[e.Hook] += e.DurationMs
		counts[e.Hook]++
	}
	out := make(map[string]float64, len(sums))
	for h, s := range sums {
		c := counts[h]
		if c == 0 {
			continue
		}
		out[h] = float64(s) / float64(c)
	}
	return out
}
