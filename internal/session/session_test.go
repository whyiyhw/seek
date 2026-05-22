package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func newStoreIn(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", dir)
	s, err := NewStore()
	if err != nil {
		t.Fatal(err)
	}
	if s.Dir() != dir {
		t.Fatalf("Dir=%q, want %q", s.Dir(), dir)
	}
	return s
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	store := newStoreIn(t)
	sess := New("deepseek-v4-flash", "/tmp", "sys", true)
	sess.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "hi"},
		{Role: deepseek.RoleAssistant, Content: "hello"},
	}
	sess.Turns = 1
	sess.Usage = deepseek.Usage{PromptTokens: 10, CompletionTokens: 5}

	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	got, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != sess.ID || got.Model != sess.Model || got.CWD != "/tmp" || !got.Yolo {
		t.Errorf("metadata mismatch: %+v", got)
	}
	if len(got.Messages) != 2 || got.Messages[1].Content != "hello" {
		t.Errorf("messages mismatch: %+v", got.Messages)
	}
	if got.Turns != 1 || got.Usage.PromptTokens != 10 {
		t.Errorf("stats mismatch: turns=%d usage=%+v", got.Turns, got.Usage)
	}
}

func TestSave_AtomicViaTempThenRename(t *testing.T) {
	// After Save, only <id>.json exists in the directory — no stray
	// .tmp file. Atomic-write contract verified by absence of tmp on
	// success.
	store := newStoreIn(t)
	sess := New("m", ".", "", false)
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(store.Dir(), sess.ID+".jsonl.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind: err=%v", err)
	}
}

func TestSave_RejectsEmptyID(t *testing.T) {
	store := newStoreIn(t)
	if err := store.Save(&Session{}); err == nil {
		t.Errorf("expected error for empty ID")
	}
}

func TestLoad_RejectsTraversal(t *testing.T) {
	store := newStoreIn(t)
	for _, bad := range []string{"", "../etc/passwd", "a/b", "x.y"} {
		if _, err := store.Load(bad); err == nil {
			t.Errorf("Load(%q) accepted; expected rejection", bad)
		}
	}
}

func TestLatest_EmptyStoreReturnsNil(t *testing.T) {
	store := newStoreIn(t)
	got, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for empty store, got %+v", got)
	}
}

func TestLatest_PicksMostRecentlyUpdated(t *testing.T) {
	store := newStoreIn(t)

	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	// a was created first AND updated most recently (simulates resuming an
	// old session after creating newer ones). Use saveDirect to preserve
	// the controlled UpdatedAt timestamps (Save calls Touch which would
	// overwrite them with time.Now()).
	a := &Session{ID: generateID(base), CreatedAt: base, UpdatedAt: base.Add(3 * time.Hour), Model: "m", SchemaVersion: CurrentSchemaVersion}
	b := &Session{ID: generateID(base.Add(time.Hour)), CreatedAt: base.Add(time.Hour), UpdatedAt: base.Add(time.Hour), Model: "m", SchemaVersion: CurrentSchemaVersion}
	c := &Session{ID: generateID(base.Add(2 * time.Hour)), CreatedAt: base.Add(2 * time.Hour), UpdatedAt: base.Add(2 * time.Hour), Model: "m", SchemaVersion: CurrentSchemaVersion}

	saveDirect(t, store, a)
	saveDirect(t, store, b)
	saveDirect(t, store, c)

	got, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	// Latest must pick by UpdatedAt, not by creation order — a has the
	// most recent UpdatedAt even though it was created first.
	if got.ID != a.ID {
		t.Errorf("got %s, want %s (latest by UpdatedAt)", got.ID, a.ID)
	}
}

func TestList_SortedByRecency(t *testing.T) {
	store := newStoreIn(t)
	for range 3 {
		mustSave(t, store, New("m", ".", "", false))
		time.Sleep(2 * time.Millisecond) // ensure UpdatedAt differs
	}
	infos, _, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 3 {
		t.Fatalf("got %d sessions, want 3", len(infos))
	}

	for i := 1; i < len(infos); i++ {
		if infos[i].UpdatedAt.After(infos[i-1].UpdatedAt) {
			t.Errorf("List not newest-first: %+v", infos)
		}
	}
}

func TestGenerateID_IsSortable(t *testing.T) {
	a := generateID(time.Date(2026, 1, 21, 10, 30, 0, 0, time.UTC))
	b := generateID(time.Date(2026, 1, 21, 10, 30, 1, 0, time.UTC))
	if !(a < b) {
		t.Errorf("ID order broke: %s !< %s", a, b)
	}
	if !strings.HasPrefix(a, "20260121-103000-") {
		t.Errorf("unexpected ID prefix: %s", a)
	}
}

func TestFork_NewIDParentLinkAndIndependentMessages(t *testing.T) {
	parent := New("deepseek-v4-flash", "/tmp", "sys", true)
	parent.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "first"},
		{Role: deepseek.RoleAssistant, Content: "answer"},
	}
	parent.Turns = 3
	parent.ToolCalls = 2
	parent.Usage = deepseek.Usage{PromptTokens: 100, CompletionTokens: 40}

	child := parent.Fork()

	if child.ID == parent.ID {
		t.Errorf("child reused parent ID %s", child.ID)
	}
	if child.ParentID != parent.ID {
		t.Errorf("ParentID = %q, want %q", child.ParentID, parent.ID)
	}
	if child.Model != parent.Model || child.CWD != parent.CWD ||
		child.Yolo != parent.Yolo || child.SystemPrompt != parent.SystemPrompt {
		t.Errorf("inherited metadata mismatch: %+v", child)
	}
	if child.Turns != 0 || child.ToolCalls != 0 || child.Usage.TotalTokens != 0 {
		t.Errorf("counters not reset: turns=%d tools=%d usage=%+v",
			child.Turns, child.ToolCalls, child.Usage)
	}
	if len(child.Messages) != len(parent.Messages) {
		t.Fatalf("messages len: %d vs %d", len(child.Messages), len(parent.Messages))
	}
	// Mutating the child must not bleed into the parent — the whole
	// point of forking is independent branches.
	child.Messages[0].Content = "mutated"
	if parent.Messages[0].Content != "first" {
		t.Errorf("parent message leaked: %s", parent.Messages[0].Content)
	}
}

func TestFork_SaveRoundtripPreservesParent(t *testing.T) {
	store := newStoreIn(t)
	parent := New("m", ".", "", false)
	parent.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "hi"}}
	mustSave(t, store, parent)

	child := parent.Fork()
	mustSave(t, store, child)

	loaded, err := store.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ParentID != parent.ID {
		t.Errorf("loaded ParentID = %q, want %q", loaded.ParentID, parent.ID)
	}
}

func TestRepair_DropsTrailingOrphanToolCalls(t *testing.T) {
	sess := New("m", ".", "", false)
	sess.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "go do thing"},
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{
				{ID: "call_1", Function: deepseek.ToolCallFunc{Name: "think", Arguments: `{"task":"x"}`}},
			},
		},
		// ↑ orphan: no matching tool message follows.
	}
	dropped := sess.Repair()
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	if got := len(sess.Messages); got != 1 {
		t.Errorf("len(Messages) = %d, want 1", got)
	}
	if sess.Messages[0].Role != deepseek.RoleUser {
		t.Errorf("user message should survive: %+v", sess.Messages[0])
	}
}

func TestRepair_LeavesValidHistoryAlone(t *testing.T) {
	sess := New("m", ".", "", false)
	sess.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "u"},
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{
				{ID: "call_1", Function: deepseek.ToolCallFunc{Name: "read"}},
			},
		},
		{Role: deepseek.RoleTool, ToolCallID: "call_1", Content: "result"},
		{Role: deepseek.RoleAssistant, Content: "all done"},
	}
	before := len(sess.Messages)
	dropped := sess.Repair()
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 (history is fine)", dropped)
	}
	if len(sess.Messages) != before {
		t.Errorf("Repair shouldn't have touched a valid history")
	}
}

func TestRepair_PartialMultiCallStillCountsAsOrphan(t *testing.T) {
	// Two tool_calls, only one gets a matching tool message —
	// still orphan because DeepSeek requires ALL of them satisfied.
	sess := New("m", ".", "", false)
	sess.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "u"},
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{
				{ID: "call_1", Function: deepseek.ToolCallFunc{Name: "a"}},
				{ID: "call_2", Function: deepseek.ToolCallFunc{Name: "b"}},
			},
		},
		{Role: deepseek.RoleTool, ToolCallID: "call_1", Content: "result"},
		// call_2 result missing — orphan.
	}
	dropped := sess.Repair()
	if dropped == 0 {
		t.Errorf("expected partial-fulfilment to be flagged as orphan")
	}
	// The whole tail from the assistant message onward should be gone.
	if len(sess.Messages) != 1 || sess.Messages[0].Role != deepseek.RoleUser {
		t.Errorf("expected [user] after repair, got %+v", sess.Messages)
	}
}

func TestRepair_HappyPathNoToolCallsAnywhere(t *testing.T) {
	sess := New("m", ".", "", false)
	sess.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "u"},
		{Role: deepseek.RoleAssistant, Content: "a"},
	}
	if d := sess.Repair(); d != 0 {
		t.Errorf("repair touched a plain-text history: dropped %d", d)
	}
}

func TestFork_DeepCopiesToolCalls(t *testing.T) {
	parent := New("m", ".", "", false)
	parent.Messages = []deepseek.Message{
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{
				{ID: "call_1", Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"cmd":"ls"}`}},
			},
		},
	}

	child := parent.Fork()

	// Mutate the child's ToolCall — must not bleed into the parent.
	child.Messages[0].ToolCalls[0].ID = "call_mutated"
	if parent.Messages[0].ToolCalls[0].ID != "call_1" {
		t.Errorf("parent ToolCall.ID was mutated by child: got %q", parent.Messages[0].ToolCalls[0].ID)
	}
}

func TestList_CollectsLoadErrors(t *testing.T) {
	store := newStoreIn(t)
	good := New("m", ".", "", false)
	mustSave(t, store, good)

	// Write a corrupt JSONL file that looks like a session file.
	corrupt := filepath.Join(store.Dir(), "20260101-000000-aabbcc.jsonl")
	if err := os.WriteFile(corrupt, []byte("not json {{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	infos, loadErrs, err := store.List()
	if err != nil {
		t.Fatalf("List fatal error: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("got %d infos, want 1 (only the good session)", len(infos))
	}
	if len(loadErrs) != 1 {
		t.Errorf("got %d load errors, want 1 (the corrupt file)", len(loadErrs))
	}
}

func mustSave(t *testing.T, s *Store, sess *Session) {
	t.Helper()
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
}

// saveDirect writes a session as JSONL directly to the store directory
// without calling Touch(), preserving controlled timestamps for tests
// that need to set UpdatedAt to a specific value (Store.Save would
// otherwise overwrite UpdatedAt with time.Now()).
//
// Mirrors Store.Save's wire format: line 1 is the header (Messages
// nil so omitempty drops the key), lines 2..N are one message each.
func saveDirect(t *testing.T, s *Store, sess *Session) {
	t.Helper()
	path := filepath.Join(s.Dir(), sess.ID+".jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	header := *sess
	header.Messages = nil
	if err := enc.Encode(&header); err != nil {
		t.Fatal(err)
	}
	for i := range sess.Messages {
		if err := enc.Encode(&sess.Messages[i]); err != nil {
			t.Fatal(err)
		}
	}
}
