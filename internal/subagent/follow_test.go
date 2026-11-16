package subagent

// Follower tests (W3.2): the incremental tail-follow over a hand-made
// subagents.jsonl — end-anchor (no history replay), appended-event
// pushes with folded rows, the missing-then-created index, partial
// trailing lines, non-panel kinds, truncation re-fold, and the fold
// rules that must hold incrementally (duplicate started, orphan
// terminal).

import (
	"os"
	"path/filepath"
	"testing"
)

// writeIndex seeds path with the given lines (each one jsonl record).
func writeIndex(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeLines(t, path, lines...)
}

// writeLines overwrites path with lines joined by \n (trailing \n
// only when there are lines) — also the truncate/rewrite primitive.
func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

// appendLines appends lines as complete jsonl records (the writer
// side's one-syscall shape: line + \n in a single append).
func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open index for append: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

const (
	startedSync = `{"event":"started","sub_sid":"sync-1","ts":"2026-01-01T10:00:00Z","parent_sid":"sess-A","parent_turn":4,"type":"explore","description":"find handlers"}`
	doneSync    = `{"event":"completed","sub_sid":"sync-1","ts":"2026-01-01T10:05:00Z","tokens":{"prompt":100,"completion":20,"cache_hit":80}}`
	startedBG   = `{"event":"started","sub_sid":"bg-1","ts":"2026-01-02T09:00:00Z","parent_sid":"sess-A","type":"general-purpose","description":"scan deps","background":true}`
	notifiedBG  = `{"event":"notified","sub_sid":"bg-1","ts":"2026-01-02T10:10:00Z","parent_turn":8}`
	doneBG      = `{"event":"completed","sub_sid":"bg-1","ts":"2026-01-02T09:40:00Z","tokens":{"prompt":50,"completion":10,"cache_hit":0}}`
)

// TestFollower_AnchorAtEnd: NewFollower anchors at the file's current
// end — pre-existing events never replay (the panel's snapshot comes
// from ListProject); appended events push exactly once, with the
// folded row carrying the terminal fields.
func TestFollower_AnchorAtEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.jsonl")
	writeIndex(t, path, startedSync, doneSync)

	fo := NewFollower(path)
	if got := fo.Poll(); got != nil {
		t.Fatalf("first Poll after anchor = %+v, want no pushes (no history replay)", got)
	}

	appendLines(t, path, startedBG)
	got := fo.Poll()
	if len(got) != 1 {
		t.Fatalf("after started append = %+v, want 1 push", got)
	}
	if got[0].Event != "started" || got[0].Sub.SubSid != "bg-1" || got[0].Sub.Status != StatusActive || !got[0].Sub.Background {
		t.Errorf("started push = %+v, want bg-1 active background row", got[0])
	}
	if got[0].Sub.Type != TypeGeneralPurpose || got[0].Sub.Description != "scan deps" || got[0].Sub.ParentSid != "sess-A" {
		t.Errorf("started push must seed the started-event fields: %+v", got[0].Sub)
	}
	if again := fo.Poll(); again != nil {
		t.Errorf("re-Poll without appends = %+v, want nil (exactly-once)", again)
	}

	appendLines(t, path, doneBG)
	got = fo.Poll()
	if len(got) != 1 || got[0].Event != "completed" {
		t.Fatalf("after completed append = %+v, want 1 completed push", got)
	}
	sub := got[0].Sub
	if sub.Status != StatusCompleted || sub.Tokens.Prompt != 50 || sub.Tokens.Completion != 10 {
		t.Errorf("completed push row = %+v, want terminal status + tokens", sub)
	}
	if sub.StartedAt.Equal(sub.EndedAt) || sub.EndedAt.IsZero() {
		t.Errorf("terminal push must set EndedAt: %+v", sub)
	}
}

// TestFollower_MissingIndexThenCreated: a bound project with no index
// yet anchors at 0; the file's creation is the first followable
// append and every line in it pushes.
func TestFollower_MissingIndexThenCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.jsonl")
	fo := NewFollower(path)
	if got := fo.Poll(); got != nil {
		t.Fatalf("Poll on missing index = %+v, want nil", got)
	}
	writeIndex(t, path, startedSync)
	got := fo.Poll()
	if len(got) != 1 || got[0].Event != "started" || got[0].Sub.SubSid != "sync-1" {
		t.Fatalf("Poll after index creation = %+v, want sync-1 started", got)
	}
}

// TestFollower_PartialLine: bytes without the trailing newline are
// the writer mid-append — not consumed, not parsed; the completing
// append yields the push exactly once.
func TestFollower_PartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.jsonl")
	writeIndex(t, path) // empty: sync-1's started arrives via append

	fo := NewFollower(path)
	appendLines(t, path, startedSync)
	if got := fo.Poll(); len(got) != 1 || got[0].Event != "started" {
		t.Fatalf("started append = %+v, want 1 started push", got)
	}

	// Write half a record with no newline.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event":"completed","sub_sid":"sync-1","ts":"`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := fo.Poll(); got != nil {
		t.Fatalf("Poll on partial line = %+v, want nil", got)
	}

	appendLines(t, path, `2026-01-01T10:05:00Z","tokens":{"prompt":7,"completion":1,"cache_hit":0}}`)
	got := fo.Poll()
	if len(got) != 1 || got[0].Event != "completed" || got[0].Sub.Tokens.Prompt != 7 {
		t.Fatalf("Poll after completion = %+v, want exactly one completed push", got)
	}
	if again := fo.Poll(); again != nil {
		t.Errorf("re-Poll = %+v, want nil (offset advanced past the whole line)", again)
	}
}

// TestFollower_NonPanelKinds: `notified` annotates state without
// pushing (visible on the next terminal push); unknown kinds change
// nothing; malformed lines are skipped like readEvents does.
func TestFollower_NonPanelKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.jsonl")
	writeIndex(t, path) // empty index: bg-1's started must be followable, not anchored past

	fo := NewFollower(path)
	appendLines(t, path, startedBG, notifiedBG, `{"event":"teleported","sub_sid":"bg-1","ts":"2026-01-02T09:20:00Z"}`, `{not json`)
	got := fo.Poll()
	if len(got) != 1 || got[0].Event != "started" || got[0].Sub.SubSid != "bg-1" {
		t.Fatalf("Poll over started+notified+unknown+malformed = %+v, want only bg-1 started", got)
	}

	appendLines(t, path, doneBG)
	got = fo.Poll()
	if len(got) != 1 || got[0].Event != "completed" {
		t.Fatalf("terminal push = %+v, want 1 completed", got)
	}
	if !got[0].Sub.Notified {
		t.Errorf("terminal row must carry the earlier notified annotation: %+v", got[0].Sub)
	}
}

// TestFollower_TruncateRefolds: a file that shrank re-anchors at 0
// and re-folds — the rewritten file's records push again (rows merge
// by sub_sid downstream).
func TestFollower_TruncateRefolds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.jsonl")
	writeIndex(t, path, startedSync, doneSync, startedBG)

	fo := NewFollower(path)
	// Rewrite shorter: only a fresh row remains (size < offset).
	const fresh = `{"event":"started","sub_sid":"bg-2","ts":"2026-01-03T09:00:00Z","parent_sid":"sess-A","type":"plan","description":"redo"}`
	writeLines(t, path, fresh)
	got := fo.Poll()
	if len(got) != 1 || got[0].Event != "started" || got[0].Sub.SubSid != "bg-2" {
		t.Fatalf("Poll after truncate = %+v, want re-folded bg-2 started", got)
	}
	if got2 := fo.Poll(); got2 != nil {
		t.Errorf("post-reset steady state = %+v, want nil", got2)
	}
}

// TestFollower_FoldRulesIncremental: duplicate started keeps the
// first; a terminal for a never-started sub_sid never pushes.
func TestFollower_FoldRulesIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subagents.jsonl")
	writeIndex(t, path)

	fo := NewFollower(path)
	appendLines(t, path,
		startedSync,
		`{"event":"started","sub_sid":"sync-1","ts":"2026-01-01T10:01:00Z","parent_sid":"sess-X","type":"plan","description":"dup"}`,
		`{"event":"completed","sub_sid":"ghost-1","ts":"2026-01-01T10:02:00Z"}`,
		doneSync)
	got := fo.Poll()
	if len(got) != 2 {
		t.Fatalf("pushes = %+v, want started + completed only", got)
	}
	if got[0].Event != "started" || got[0].Sub.ParentSid != "sess-A" || got[0].Sub.Description != "find handlers" {
		t.Errorf("duplicate started must not reseed: %+v", got[0].Sub)
	}
	if got[1].Event != "completed" || got[1].Sub.SubSid != "sync-1" || got[1].Sub.Tokens.Prompt != 100 {
		t.Errorf("completed push = %+v, want terminal sync-1 with tokens", got[1])
	}
	if !got[1].Sub.StartedAt.Before(got[1].Sub.EndedAt) {
		t.Errorf("started/completed timestamps out of order: %+v", got[1].Sub)
	}
}
