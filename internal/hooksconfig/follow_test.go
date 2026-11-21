package hooksconfig

// AuditFollower tests (W5 c_xz2bey, the /events hooks topic's source):
// anchor-at-end (no history replay), missing-file anchor-at-0 (later
// creation pushes), append→poll, partial trailing line held, corrupt
// complete line consumed-but-skipped, shrink re-anchor. Same caliber as
// internal/subagent's follow_test.go.

import (
	"os"
	"path/filepath"
	"testing"
)

const followLineA = `{"ts":"2026-11-20T09:00:00Z","event":"post_tool_use","hook":"fmt","tool":"bash","session_id":"s1","duration_ms":12,"exit_code":0}`
const followLineB = `{"ts":"2026-11-20T09:00:05Z","event":"pre_prompt_inject","hook":"ctx","session_id":"s1","duration_ms":1,"exit_code":0,"injected_bytes":99}`
const followLineC = `{"ts":"2026-11-20T09:00:09Z","event":"pre_tool_use","hook":"fmt","tool":"edit","session_id":"s2","duration_ms":3,"exit_code":1,"denied":true}`

func followAppend(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open audit log: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func TestAuditFollower_AnchorAtEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-audit.jsonl")
	followAppend(t, path, followLineA)
	fo := NewAuditFollower(path)
	if got := fo.Poll(); got != nil {
		t.Fatalf("history replayed: %v", got)
	}
	followAppend(t, path, followLineB)
	got := fo.Poll()
	if len(got) != 1 || got[0] != followLineB {
		t.Fatalf("poll = %v, want exactly [%s] verbatim", got, followLineB)
	}
	if got := fo.Poll(); got != nil {
		t.Fatalf("quiescent file re-pushed: %v", got)
	}
}

func TestAuditFollower_MissingFileThenCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-audit.jsonl")
	fo := NewAuditFollower(path) // missing: anchor 0, creation is followable
	if got := fo.Poll(); got != nil {
		t.Fatalf("missing file pushed: %v", got)
	}
	followAppend(t, path, followLineA, followLineB)
	got := fo.Poll()
	if len(got) != 2 || got[0] != followLineA || got[1] != followLineB {
		t.Fatalf("poll = %v, want both creation lines in order", got)
	}
}

func TestAuditFollower_PartialAndCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-audit.jsonl")
	followAppend(t, path, followLineA)
	fo := NewAuditFollower(path)

	// Corrupt complete line: consumed (offset advances past it), never
	// pushed — the ReadAuditLog skip rule, plus no eternal re-scan.
	followAppend(t, path, `{"ts": not json`)
	if got := fo.Poll(); got != nil {
		t.Fatalf("corrupt line pushed: %v", got)
	}
	// A later good line must not drag the corrupt one along.
	followAppend(t, path, followLineB)
	got := fo.Poll()
	if len(got) != 1 || got[0] != followLineB {
		t.Fatalf("poll after corrupt = %v, want only the good line", got)
	}

	// Partial trailing line (writer mid-append): held for the next poll.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(followLineC[:20])
	f.Close()
	if got := fo.Poll(); got != nil {
		t.Fatalf("partial line pushed: %v", got)
	}
	followAppend(t, path, followLineC[20:]+"\n")
	got = fo.Poll()
	if len(got) != 1 || got[0] != followLineC {
		t.Fatalf("poll after completion = %v, want the whole line", got)
	}
}

func TestAuditFollower_ShrinkReanchors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks-audit.jsonl")
	followAppend(t, path, followLineA, followLineB)
	fo := NewAuditFollower(path)
	// Rewrite the file shorter (log rotation shape — never expected of
	// the append-only log, but the follower must not wedge).
	if err := os.WriteFile(path, []byte(followLineC+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := fo.Poll()
	if len(got) != 1 || got[0] != followLineC {
		t.Fatalf("poll after shrink = %v, want the new file's content re-read from 0", got)
	}
}
