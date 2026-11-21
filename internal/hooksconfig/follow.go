package hooksconfig

// Incremental tail-follow of the global audit JSONL (W5 c_xz2bey, the
// web /events `hooks` topic's source — inspect-rpc §6 #6/#7 closed).
// The writers are seek child processes in OTHER processes, so the
// follow is stat+read polling at the caller's cadence — the same shape
// as internal/subagent's Follower, minus the fold: an audit row IS the
// wire format (§3.6), so a push is the file line verbatim, never a
// re-shaped envelope.
//
// Corrupt-line rule mirrors ReadAuditLog's: a complete line that fails
// to unmarshal into AuditEntry is consumed but never pushed (skipping
// without consuming would re-scan it on every poll forever). A trailing
// partial line stays for the next poll — the writer's append completes
// it.

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// AuditFollower incrementally tails one hooks-audit.jsonl. Not safe for
// concurrent use — exactly one caller goroutine per file (the web
// frontend runs one per audit log path).
type AuditFollower struct {
	path   string
	offset int64
}

// NewAuditFollower anchors at path's current END: the first Poll
// surfaces only lines appended after this call. History comes from
// hooks/audit/tail (W4.6) — subscribe first, then fetch the snapshot,
// and pushes patch it; lines already in the file never replay. A
// missing file anchors at 0: its later creation is the first followable
// append (NewAuditLog creates it empty, so still no history replay).
func NewAuditFollower(path string) *AuditFollower {
	fo := &AuditFollower{path: path}
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
		fo.offset = st.Size()
	}
	return fo
}

// Poll reads the complete lines appended since the last call and
// returns them verbatim (newline stripped), oldest first, corrupt ones
// skipped-and-consumed. Best-effort by design: a missing file, a
// transient open/stat failure yields no lines and consumes nothing —
// the next Poll re-reads from the same offset, so liveness never loses
// data. A file that shrank (rewritten — never expected of an
// append-only log) re-anchors at 0; the re-read re-pushes whatever the
// new file holds, and clients dedupe (the audit row has no unique id
// beyond its own bytes).
func (fo *AuditFollower) Poll() []string {
	f, err := os.Open(fo.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	if st.Size() < fo.offset {
		fo.offset = 0
	}
	if st.Size() == fo.offset {
		return nil
	}
	r := bufio.NewReader(io.NewSectionReader(f, fo.offset, st.Size()-fo.offset))
	var lines []string
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && err != nil {
			break // partial trailing line: the writer's next append completes it
		}
		if len(line) > 0 {
			fo.offset += int64(len(line))
			body := line[:len(line)-1]
			var e AuditEntry
			if json.Unmarshal(body, &e) == nil {
				lines = append(lines, string(body))
			}
		}
		if err != nil {
			break // clean EOF
		}
	}
	return lines
}
