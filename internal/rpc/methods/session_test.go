package methods

// Session method set tests (W4.2 acceptance, all sandbox: a temp-dir
// store with fake multi-session JSONL — no real key, no real home):
//
//  1. session/messages pagination walk == whole-file order (the
//     inspect-rpc §6.4 / card ① criterion), at several page sizes;
//  2. concurrent tail reads while a writer appends (deliberately torn)
//     never block and never surface a torn line (card ②);
//  3. bad cursors / missing sessions / invalid sids answer the §3.5
//     codes (card ③).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

const (
	mainSID    = "20261115-090000-aaaaaaaaaaaaaaaa"
	branchSID  = "20261115-100000-bbbbbbbbbbbbbbbb"
	emptySID   = "20261115-110000-cccccccccccccccc"
	unknownSID = "20260101-000000-0000000000000000"
)

// writeFixture builds the fake multi-session store: a multi-turn
// session with tool-call lines, a /branch child of it, and an empty
// header-only session. Save order fixes UpdatedAt order, so list is
// newest-first [empty, branch, main] deterministically.
func writeFixture(t *testing.T) *session.Store {
	t.Helper()
	st, err := session.NewStoreAt(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	main := &session.Session{
		SchemaVersion: 3,
		ID:            mainSID,
		Model:         "deepseek-chat",
		Yolo:          true,
		CWD:           "/tmp/proj",
		Turns:         2,
		ToolCalls:     2,
		Usage:         deepseek.Usage{PromptTokens: 100, CompletionTokens: 40, PromptCacheHitTokens: 60},
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "read main.go and fix the bug\nkeep the fix minimal"},
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{ID: "call_1", Type: "function", Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"main.go"}`}},
				{ID: "call_2", Type: "function", Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"cmd":"go test ./..."}`}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "call_1", Content: "package main\n…"},
			{Role: deepseek.RoleTool, ToolCallID: "call_2", Content: "ok\nseek\t1.2s"},
			{Role: deepseek.RoleAssistant, Content: "Fixed.", PredictedNext: "thanks"},
		},
	}
	branch := &session.Session{
		SchemaVersion: 3,
		ID:            branchSID,
		Model:         "deepseek-chat",
		CWD:           "/tmp/proj",
		ParentID:      mainSID,
		Messages:      []deepseek.Message{{Role: deepseek.RoleUser, Content: "what if we add tests?"}},
	}
	empty := &session.Session{SchemaVersion: 3, ID: emptySID, Model: "deepseek-chat", CWD: "/tmp/other"}
	for _, s := range []*session.Session{main, branch, empty} {
		if err := st.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// newFixtureRegistry wires a registry with the session family over the
// fixture store — the same registration servecli performs.
func newFixtureRegistry(t *testing.T) (*rpc.Registry, *session.Store) {
	t.Helper()
	st := writeFixture(t)
	reg := rpc.NewRegistry()
	NewSessions(st).RegisterAll(reg)
	return reg, st
}

func TestSessionList(t *testing.T) {
	reg, _ := newFixtureRegistry(t)

	res, err := reg.Dispatch(context.Background(), "session/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(sessionListResult)
	if len(got.Sessions) != 3 {
		t.Fatalf("sessions = %d, want 3 (%+v)", len(got.Sessions), got)
	}
	// Newest first (Save order main → branch → empty).
	if got.Sessions[0].SID != emptySID || got.Sessions[1].SID != branchSID || got.Sessions[2].SID != mainSID {
		t.Errorf("order = %s %s %s, want empty branch main (newest first)",
			got.Sessions[0].SID, got.Sessions[1].SID, got.Sessions[2].SID)
	}
	main := got.Sessions[2]
	if main.Title != "read main.go and fix the bug" {
		t.Errorf("title = %q, want first user line flattened", main.Title)
	}
	if main.Turns != 2 || main.ToolCalls != 2 || main.Model != "deepseek-chat" || main.CWD != "/tmp/proj" || main.ParentID != "" {
		t.Errorf("main row drifted: %+v", main)
	}
	if got.Sessions[1].ParentID != mainSID {
		t.Errorf("branch parent_id = %q, want %q", got.Sessions[1].ParentID, mainSID)
	}
	if got.Sessions[0].Title != "" {
		t.Errorf("empty session title = %q, want empty", got.Sessions[0].Title)
	}

	// Params leniency: no params member, and a future flag arriving
	// early (include_subagents, §3.1) is a no-op, not -32602.
	for _, params := range []string{``, `{"include_subagents":true}`} {
		if _, err := reg.Dispatch(context.Background(), "session/list", json.RawMessage(params)); err != nil {
			t.Errorf("params %q: %v", params, err)
		}
	}
}

func TestSessionListTruncatesLongTitles(t *testing.T) {
	st, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("宽", 100) // rune-aware cap, not byte cap
	sess := session.New("m", "/tmp", "", false, false)
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: long}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	if got := firstUserTitle(st.Dir(), sess.ID); len([]rune(got)) != titleMaxRunes+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("long title not capped at %d runes + ellipsis: %d runes %q", titleMaxRunes, len([]rune(got)), got)
	}
}

func TestSessionGet(t *testing.T) {
	reg, _ := newFixtureRegistry(t)

	res, err := reg.Dispatch(context.Background(), "session/get", json.RawMessage(fmt.Sprintf(`{"sid":%q}`, mainSID)))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(sessionGetResult)
	if got.Header.ID != mainSID || got.Header.Turns != 2 || got.Header.ToolCalls != 2 {
		t.Errorf("header drifted: %+v", got.Header)
	}
	if got.Turns != 2 {
		t.Errorf("turns = %d, want 2", got.Turns)
	}
	if got.CumulativeUsage != (deepseek.Usage{PromptTokens: 100, CompletionTokens: 40, PromptCacheHitTokens: 60}) {
		t.Errorf("cumulative_usage = %+v", got.CumulativeUsage)
	}
	// No messages member may leak through the header.
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), `"messages"`) {
		t.Errorf("result leaks a messages member: %s", b)
	}

	// Empty-but-existing session is a valid get, not not-found.
	res, err = reg.Dispatch(context.Background(), "session/get", json.RawMessage(fmt.Sprintf(`{"sid":%q}`, emptySID)))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(sessionGetResult); got.Turns != 0 || got.Header.ID != emptySID {
		t.Errorf("empty session get: %+v", got.Header)
	}
}

// walkAll pages session/messages from the top with the given page size
// and concatenates the raw lines — the client-side pagination walk.
func walkAll(t *testing.T, reg *rpc.Registry, sid string, limit int) ([]json.RawMessage, error) {
	t.Helper()
	var all []json.RawMessage
	cursor := ""
	for {
		params := fmt.Sprintf(`{"sid":%q,"limit":%d`, sid, limit)
		if cursor != "" {
			params += fmt.Sprintf(`,"cursor":%q`, cursor)
		}
		params += "}"
		res, err := reg.Dispatch(context.Background(), "session/messages", json.RawMessage(params))
		if err != nil {
			return all, err
		}
		r := res.(sessionMessagesResult)
		all = append(all, r.Messages...)
		if r.NextCursor == "" {
			return all, nil
		}
		if r.NextCursor == cursor {
			t.Fatalf("cursor stalled at %q", cursor)
		}
		cursor = r.NextCursor
	}
}

// Card ① / inspect-rpc §6.4: paging from head to tail yields exactly
// the whole-file order — the walk equals session.Load at every page
// size, including one that exactly divides and one that over-shoots.
func TestSessionMessagesPaginationWalkEqualsFile(t *testing.T) {
	reg, st := newFixtureRegistry(t)
	want, err := st.Load(mainSID)
	if err != nil {
		t.Fatal(err)
	}

	for _, limit := range []int{1, 2, 3, 5, 7, messagesMaxLimit} {
		raw, err := walkAll(t, reg, mainSID, limit)
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if len(raw) != len(want.Messages) {
			t.Fatalf("limit %d: walked %d messages, want %d", limit, len(raw), len(want.Messages))
		}
		got := make([]deepseek.Message, len(raw))
		for i, r := range raw {
			if err := json.Unmarshal(r, &got[i]); err != nil {
				t.Fatalf("limit %d msg %d is not a message: %v (%s)", limit, i, err, r)
			}
		}
		if !reflect.DeepEqual(got, want.Messages) {
			t.Fatalf("limit %d: walk diverges from Load:\ngot  %+v\nwant %+v", limit, got, want.Messages)
		}
	}

	// Default limit (50) covers the fixture in one page with no cursor.
	res, err := reg.Dispatch(context.Background(), "session/messages", json.RawMessage(fmt.Sprintf(`{"sid":%q}`, mainSID)))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(sessionMessagesResult); len(r.Messages) != 5 || r.NextCursor != "" {
		t.Errorf("default page = %d msgs cursor %q, want 5 / none", len(r.Messages), r.NextCursor)
	}

	// Empty session: an empty JSON array, never null, never an error.
	res, err = reg.Dispatch(context.Background(), "session/messages", json.RawMessage(fmt.Sprintf(`{"sid":%q}`, emptySID)))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"messages":[]}` {
		t.Errorf("empty session result = %s, want {\"messages\":[]}", b)
	}

	// Cursor past the end (e.g. the tail was trimmed by Repair): an
	// empty page, not an error.
	res, err = reg.Dispatch(context.Background(), "session/messages", json.RawMessage(fmt.Sprintf(`{"sid":%q,"cursor":"99"}`, mainSID)))
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(sessionMessagesResult); len(r.Messages) != 0 || r.NextCursor != "" {
		t.Errorf("past-end page = %d msgs cursor %q, want 0 / none", len(r.Messages), r.NextCursor)
	}
}

// Card ②: the serve process tails a session while a writer (modelled
// on the §3.3 two-process append — direct O_APPEND, deliberately torn
// writes) appends. Readers must never block, never surface a torn
// line, and see a monotonically growing prefix.
func TestSessionMessagesTailReadConcurrentWithAppend(t *testing.T) {
	reg, st := newFixtureRegistry(t)
	path := filepath.Join(st.Dir(), mainSID+".jsonl")

	const appends = 120
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Errorf("writer open: %v", err)
			return
		}
		defer f.Close()
		for i := 0; i < appends; i++ {
			line := fmt.Sprintf(`{"role":"user","content":"append %03d"}`, i)
			half := line[:len(line)/2]
			if _, err := f.WriteString(half); err != nil {
				t.Errorf("writer half: %v", err)
				return
			}
			time.Sleep(300 * time.Microsecond) // let readers hit the torn tail
			if _, err := f.WriteString(line[len(line)/2:] + "\n"); err != nil {
				t.Errorf("writer rest: %v", err)
				return
			}
		}
	}()

	// Watchdog: the readers hold no locks, so they must settle well
	// inside any sane bound; a regression to a locking read deadlocks
	// here instead of hanging CI for the package timeout.
	settled := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prev := -1
			for i := 0; i < 40; i++ {
				res, err := reg.Dispatch(context.Background(), "session/messages", json.RawMessage(fmt.Sprintf(`{"sid":%q,"limit":200}`, mainSID)))
				if err != nil {
					t.Errorf("tail read: %v", err)
					return
				}
				msgs := res.(sessionMessagesResult).Messages
				if len(msgs) < prev {
					t.Errorf("message count shrank: %d < %d", len(msgs), prev)
				}
				prev = len(msgs)
				for _, raw := range msgs {
					var m deepseek.Message
					if err := json.Unmarshal(raw, &m); err != nil || m.Role == "" {
						t.Errorf("torn line surfaced: %v (%s)", err, raw)
						return
					}
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	go func() { wg.Wait(); close(settled) }()
	select {
	case <-settled:
	case <-time.After(30 * time.Second):
		t.Fatal("tail reads did not settle — a reader is blocking another (or the writer)")
	}
	<-writerDone

	// After the writer settles, the full walk equals the file: every
	// append visible, in order, torn tail now whole.
	raw, err := walkAll(t, reg, mainSID, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 5+appends {
		t.Fatalf("final walk = %d messages, want %d", len(raw), 5+appends)
	}
	var last deepseek.Message
	if err := json.Unmarshal(raw[len(raw)-1], &last); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("append %03d", appends-1); last.Content != want {
		t.Errorf("last message = %q, want %q", last.Content, want)
	}
}

// Card ③: the §3.5 error codes — -32602 for malformed params and
// cursors, -32001 for a well-formed but absent session (with the sid
// in structured data); store-internal failures stay plain errors that
// the host maps to -32603.
func TestSessionErrorCodes(t *testing.T) {
	reg, _ := newFixtureRegistry(t)

	cases := []struct {
		name   string
		method string
		params string
		want   int
	}{
		{"get: missing sid", "session/get", `{}`, rpc.CodeInvalidParams},
		{"get: empty sid", "session/get", `{"sid":""}`, rpc.CodeInvalidParams},
		{"get: traversal sid", "session/get", `{"sid":"../../etc/passwd"}`, rpc.CodeInvalidParams},
		{"get: dot-carrying sid", "session/get", `{"sid":"x.jsonl"}`, rpc.CodeInvalidParams},
		{"get: scalar params", "session/get", `"sid"`, rpc.CodeInvalidParams},
		{"get: array params", "session/get", `["sid"]`, rpc.CodeInvalidParams},
		{"get: unknown session", "session/get", fmt.Sprintf(`{"sid":%q}`, unknownSID), rpc.CodeSessionNotFound},

		{"messages: missing sid", "session/messages", `{}`, rpc.CodeInvalidParams},
		{"messages: slash sid", "session/messages", `{"sid":"a/b"}`, rpc.CodeInvalidParams},
		{"messages: unknown session", "session/messages", fmt.Sprintf(`{"sid":%q}`, unknownSID), rpc.CodeSessionNotFound},
		{"messages: cursor not a number", "session/messages", fmt.Sprintf(`{"sid":%q,"cursor":"later"}`, mainSID), rpc.CodeInvalidParams},
		{"messages: cursor negative", "session/messages", fmt.Sprintf(`{"sid":%q,"cursor":"-1"}`, mainSID), rpc.CodeInvalidParams},
		{"messages: cursor float", "session/messages", fmt.Sprintf(`{"sid":%q,"cursor":"1.5"}`, mainSID), rpc.CodeInvalidParams},
		{"messages: cursor number-typed", "session/messages", fmt.Sprintf(`{"sid":%q,"cursor":3}`, mainSID), rpc.CodeInvalidParams},
		{"messages: limit zero", "session/messages", fmt.Sprintf(`{"sid":%q,"limit":0}`, mainSID), rpc.CodeInvalidParams},
		{"messages: limit negative", "session/messages", fmt.Sprintf(`{"sid":%q,"limit":-5}`, mainSID), rpc.CodeInvalidParams},
		{"messages: limit over max", "session/messages", fmt.Sprintf(`{"sid":%q,"limit":%d}`, mainSID, messagesMaxLimit+1), rpc.CodeInvalidParams},
		{"messages: scalar params", "session/messages", `42`, rpc.CodeInvalidParams},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Dispatch(context.Background(), tc.method, json.RawMessage(tc.params))
			if err == nil {
				t.Fatal("want error, got success")
			}
			var e *rpc.Error
			if !errors.As(err, &e) {
				t.Fatalf("error = %T (%v), want *rpc.Error", err, err)
			}
			if e.Code != tc.want {
				t.Errorf("code = %d, want %d (%v)", e.Code, tc.want, err)
			}
			if tc.want == rpc.CodeSessionNotFound {
				data, ok := e.Data.(map[string]string)
				if !ok || data["sid"] == "" {
					t.Errorf("app error must name the sid in data: %+v", e)
				}
			}
		})
	}

	// The exact max limit is legal (boundary), unknown fields ignored.
	for _, params := range []string{
		fmt.Sprintf(`{"sid":%q,"limit":%d}`, mainSID, messagesMaxLimit),
		fmt.Sprintf(`{"sid":%q,"future_flag":true}`, mainSID),
	} {
		if _, err := reg.Dispatch(context.Background(), "session/messages", json.RawMessage(params)); err != nil {
			t.Errorf("params %s: %v", params, err)
		}
	}
}

// NewSessionsFromEnv resolves through the env ladder (the serve
// process's root) on first dispatch — sticky once, error included.
func TestNewSessionsFromEnvLazyResolve(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "") // isolate from a dev-machine override

	fam := NewSessionsFromEnv()
	// Construction must not have touched the filesystem…
	if _, err := os.Stat(filepath.Join(home, "sessions")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("wiring created the sessions dir: %v", err)
	}
	reg := rpc.NewRegistry()
	fam.RegisterAll(reg)

	// …first dispatch resolves and lists the fixture.
	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New("m", "/tmp", "", false, false)
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "hello"}}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	res, err := reg.Dispatch(context.Background(), "session/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(sessionListResult)
	if len(got.Sessions) != 1 || got.Sessions[0].SID != sess.ID || got.Sessions[0].Title != "hello" {
		t.Errorf("env-resolved list = %+v", got.Sessions)
	}

	// Stickiness: a family whose root cannot exist fails the same way
	// on every call (plain error → host maps -32603), never silently
	// resolving elsewhere. The blocker file makes MkdirAll fail with
	// ENOTDIR — deterministic on every OS.
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("file, not dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEEK_SESSIONS_DIR", filepath.Join(blocker, "sub"))
	broken := NewSessionsFromEnv()
	brokenReg := rpc.NewRegistry()
	broken.RegisterAll(brokenReg)
	for i := 0; i < 2; i++ {
		_, err := brokenReg.Dispatch(context.Background(), "session/list", json.RawMessage(`{}`))
		if err == nil {
			t.Fatal("broken root must error")
		}
		var e *rpc.Error
		if errors.As(err, &e) {
			t.Errorf("call %d: unexpected *rpc.Error %v, want a plain error the host maps to -32603", i, err)
		}
	}
}

// A corrupt complete line surfaces as a plain error (the host maps it
// to -32603), not an app code — corruption is not "not found".
func TestSessionMessagesCorruptFileIsPlainError(t *testing.T) {
	reg, st := newFixtureRegistry(t)
	path := filepath.Join(st.Dir(), mainSID+".jsonl")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("definitely not json\n")...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = reg.Dispatch(context.Background(), "session/messages", json.RawMessage(fmt.Sprintf(`{"sid":%q}`, mainSID)))
	if err == nil {
		t.Fatal("corrupt tail line must error")
	}
	var e *rpc.Error
	if errors.As(err, &e) {
		t.Fatalf("corruption is not an app error: %v", err)
	}
}

// The pagination cursor is the message index — spelled out so a future
// opaque-cursor migration has one test to update deliberately.
func TestCursorIsMessageIndex(t *testing.T) {
	reg, _ := newFixtureRegistry(t)
	res, err := reg.Dispatch(context.Background(), "session/messages", json.RawMessage(fmt.Sprintf(`{"sid":%q,"limit":2}`, mainSID)))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(sessionMessagesResult).NextCursor; got != strconv.Itoa(2) {
		t.Errorf("next_cursor = %q, want message index \"2\"", got)
	}
}
