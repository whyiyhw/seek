package session

// Read-path tests (W4.2): NewStoreAt, LoadHeader (line 1 only) and
// ReadMessagesPage (offset/limit windows, append-tail semantics — the
// §3.3 two-process caveat: no locks, line-level parse, torn tail
// skipped until whole).

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// newReadFixture builds a store with one 5-message session (multi-turn
// with tool calls — the shape the asset plane browses) and returns the
// session plus its file path.
func newReadFixture(t *testing.T) (*Store, *Session, string) {
	t.Helper()
	st, err := NewStoreAt(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess := &Session{
		SchemaVersion: CurrentSchemaVersion,
		ID:            "20261115-090000-aaaaaaaaaaaaaaaa",
		Model:         "deepseek-chat",
		CWD:           "/tmp/proj",
		Turns:         2,
		ToolCalls:     2,
		Usage:         deepseek.Usage{PromptTokens: 100, CompletionTokens: 40},
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "read main.go and fix the bug"},
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{ID: "call_1", Type: "function", Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"main.go"}`}},
				{ID: "call_2", Type: "function", Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"cmd":"go test"}`}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "call_1", Content: "package main…"},
			{Role: deepseek.RoleTool, ToolCallID: "call_2", Content: "ok\nseek\t1.2s"},
			{Role: deepseek.RoleAssistant, Content: "Fixed.", PredictedNext: "thanks"},
		},
	}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	return st, sess, filepath.Join(st.Dir(), sess.ID+".jsonl")
}

func TestNewStoreAt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	st, err := NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", st.Dir(), dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("NewStoreAt must create the dir: stat err=%v", err)
	}
	if _, err := NewStoreAt(""); err == nil {
		t.Error("empty dir must error")
	}
	// Round-trips through the ordinary Load path.
	sess := New("m", "/tmp", "", false, false)
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "hi"}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	got, err := st.Load(sess.ID)
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("Load via NewStoreAt: %v, messages=%d", err, len(got.Messages))
	}
}

func TestLoadHeader(t *testing.T) {
	st, sess, _ := newReadFixture(t)

	hdr, err := st.LoadHeader(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.ID != sess.ID || hdr.Model != "deepseek-chat" || hdr.Turns != 2 || hdr.ToolCalls != 2 {
		t.Errorf("header fields drifted: %+v", hdr)
	}
	if hdr.Usage.PromptTokens != 100 {
		t.Errorf("usage = %+v, want prompt_tokens 100", hdr.Usage)
	}
	if hdr.Messages != nil {
		t.Error("LoadHeader must not allocate messages")
	}
	// Marshal contract: no messages member leaks into the header shape.
	b, _ := json.Marshal(hdr)
	if strings.Contains(string(b), "\"messages\"") {
		t.Errorf("header marshals a messages member: %s", b)
	}
}

func TestLoadHeaderErrors(t *testing.T) {
	st, _, _ := newReadFixture(t)
	if _, err := st.LoadHeader("20260101-000000-0000000000000000"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing session = %v, want ErrNotExist", err)
	}
	if _, err := st.LoadHeader(""); err == nil {
		t.Error("empty id must error")
	}
	if _, err := st.LoadHeader("../escape"); err == nil || strings.Contains(err.Error(), "ErrNotExist") {
		t.Errorf("path-carrying id must be rejected as invalid, got %v", err)
	}
	// A corrupt header line is a decode error, not a not-found.
	sess := New("m", "/tmp", "", false, false)
	path := filepath.Join(st.Dir(), sess.ID+".jsonl")
	if err := os.WriteFile(path, []byte("not json at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadHeader(sess.ID); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Errorf("corrupt header = %v, want a decode error", err)
	}
}

func TestReadMessagesPage(t *testing.T) {
	st, sess, _ := newReadFixture(t)

	cases := []struct {
		name      string
		offset    int
		limit     int
		wantMsgs  int
		wantMore  bool
		wantFirst string // role of first returned message
	}{
		{"whole file", 0, 5, 5, false, deepseek.RoleUser},
		{"default-ish page", 0, 50, 5, false, deepseek.RoleUser},
		{"first page", 0, 2, 2, true, deepseek.RoleUser},
		{"middle page", 2, 2, 2, true, deepseek.RoleTool},
		{"last short page", 4, 2, 1, false, deepseek.RoleAssistant},
		{"offset at end", 5, 2, 0, false, ""},
		{"offset past end", 99, 2, 0, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, more, err := st.ReadMessagesPage(sess.ID, tc.offset, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) != tc.wantMsgs || more != tc.wantMore {
				t.Fatalf("got %d msgs more=%v, want %d more=%v", len(page), more, tc.wantMsgs, tc.wantMore)
			}
			if tc.wantFirst != "" {
				var m deepseek.Message
				if err := json.Unmarshal(page[0], &m); err != nil {
					t.Fatalf("first page item is not a message: %v (%s)", err, page[0])
				}
				if m.Role != tc.wantFirst {
					t.Errorf("first role = %q, want %q", m.Role, tc.wantFirst)
				}
			}
		})
	}

	// Raw passthrough: schema-v3 predicted_next survives byte-for-byte.
	page, _, err := st.ReadMessagesPage(sess.ID, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page[0]), `"predicted_next":"thanks"`) {
		t.Errorf("predicted_next lost in passthrough: %s", page[0])
	}

	// Argument validation.
	if _, _, err := st.ReadMessagesPage(sess.ID, -1, 5); err == nil {
		t.Error("negative offset must error")
	}
	if _, _, err := st.ReadMessagesPage(sess.ID, 0, 0); err == nil {
		t.Error("zero limit must error")
	}
	if _, _, err := st.ReadMessagesPage("20260101-000000-0000000000000000", 0, 5); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing session = %v, want ErrNotExist", err)
	}
}

func TestReadMessagesPageEmptySession(t *testing.T) {
	st, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := New("m", "/tmp", "", false, false)
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	page, more, err := st.ReadMessagesPage(sess.ID, 0, 50)
	if err != nil || len(page) != 0 || more {
		t.Fatalf("empty session: page=%d more=%v err=%v, want 0/false/nil", len(page), more, err)
	}
}

func TestReadMessagesPageTornTail(t *testing.T) {
	st, sess, path := newReadFixture(t)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Torn append (invalid JSON prefix, no '\n'): invisible until whole.
	torn := `{"role":"user","content":"mid app`
	if _, err := f.WriteString(torn); err != nil {
		t.Fatal(err)
	}
	page, more, err := st.ReadMessagesPage(sess.ID, 0, 50)
	if err != nil || len(page) != 5 || more {
		t.Fatalf("torn tail leaked: page=%d more=%v err=%v", len(page), more, err)
	}

	// Completing the line makes it visible — appended in order.
	if _, err := f.WriteString(`end"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	page, _, err = st.ReadMessagesPage(sess.ID, 0, 50)
	if err != nil || len(page) != 6 {
		t.Fatalf("completed line invisible: page=%d err=%v", len(page), err)
	}
	var m deepseek.Message
	json.Unmarshal(page[5], &m)
	if m.Content != "mid append" {
		t.Errorf("appended message = %q", m.Content)
	}

	// Whole-JSON tail without '\n' (write torn exactly after '}'):
	// visible now, and stable once the '\n' lands.
	if _, err := f.WriteString(`{"role":"user","content":"brace torn"}`); err != nil {
		t.Fatal(err)
	}
	page, _, err = st.ReadMessagesPage(sess.ID, 6, 50)
	if err != nil || len(page) != 1 {
		t.Fatalf("brace-torn tail should count as a whole message: page=%d err=%v", len(page), err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	page2, _, err := st.ReadMessagesPage(sess.ID, 6, 50)
	if err != nil || len(page2) != 1 || !reflect.DeepEqual(page, page2) {
		t.Fatalf("index shifted when the newline landed: %d vs %d (%v)", len(page), len(page2), err)
	}
	f.Close()
}

func TestReadMessagesPageCorruptLine(t *testing.T) {
	st, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := New("m", "/tmp", "", false, false)
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "one"}, {Role: deepseek.RoleAssistant, Content: "two"}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	// A COMPLETE line that is not JSON is corruption, Load's posture.
	path := filepath.Join(st.Dir(), sess.ID+".jsonl")
	raw, _ := os.ReadFile(path)
	raw = append(raw, []byte("definitely not json\n")...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReadMessagesPage(sess.ID, 0, 50); err == nil {
		t.Error("corrupt complete line must error")
	}
}

func TestReadMessagesPageBlankLineTolerance(t *testing.T) {
	st, sess, path := newReadFixture(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Blank inter-message line: Load's decoder skips inter-value
	// whitespace; the pager must agree or the two views diverge.
	lines := strings.SplitN(string(raw), "\n", 3)
	padded := lines[0] + "\n\n" + lines[2]
	if err := os.WriteFile(path, []byte(padded), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, more, err := st.ReadMessagesPage(sess.ID, 0, 50)
	if err != nil || more || len(page) != len(loaded.Messages) {
		t.Fatalf("blank line: page=%d load=%d more=%v err=%v", len(page), len(loaded.Messages), more, err)
	}
}
