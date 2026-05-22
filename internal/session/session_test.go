package session

import (
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
	tmp := filepath.Join(store.Dir(), sess.ID+".json.tmp")
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

func TestLatest_PicksMostRecent(t *testing.T) {
	store := newStoreIn(t)

	a := New("m", ".", "", false)
	a.UpdatedAt = time.Now().Add(-2 * time.Hour)
	mustSave(t, store, a)

	b := New("m", ".", "", false)
	b.UpdatedAt = time.Now().Add(-1 * time.Hour)
	mustSave(t, store, b)

	c := New("m", ".", "", false)
	c.UpdatedAt = time.Now() // most recent — but Save overwrites with now()
	mustSave(t, store, c)

	got, err := store.Latest()
	if err != nil {
		t.Fatal(err)
	}
	// Save Touch()'es UpdatedAt to "now" so the order is determined by
	// save order — last save wins.
	if got.ID != c.ID {
		t.Errorf("got %s, want %s (latest)", got.ID, c.ID)
	}
}

func TestList_SortedByRecency(t *testing.T) {
	store := newStoreIn(t)
	for i := 0; i < 3; i++ {
		s := New("m", ".", "", false)
		mustSave(t, store, s)
		time.Sleep(2 * time.Millisecond) // ensure UpdatedAt differs
	}
	infos, err := store.List()
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

func mustSave(t *testing.T, s *Store, sess *Session) {
	t.Helper()
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
}
