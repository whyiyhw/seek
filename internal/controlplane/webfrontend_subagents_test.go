package controlplane

// GET /subagents endpoint tests (W3.1 acceptance): the token gate,
// GET-only, the conv→binding→project resolution, and the folded list
// over a hand-made subagents.jsonl (柱 G existing events + D1/D3 P1
// shapes) — handler-level via httptest, same pattern as the MVP suite.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// subagentFixture is one folded row the endpoint tests assert on.
type subagentFixture struct {
	SubSid      string `json:"sub_sid"`
	ParentSid   string `json:"parent_sid"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Background  bool   `json:"background,omitempty"`
	Notified    bool   `json:"notified,omitempty"`
	Tokens      *struct {
		Prompt     int `json:"prompt"`
		Completion int `json:"completion"`
		CacheHit   int `json:"cache_hit"`
	} `json:"tokens,omitempty"`
}

// seedSubagentsIndex binds web:default to a temp project and writes
// the hand-made event lines into that project's subagents.jsonl
// (SEEK_HOME redirected so paths.SubagentsIndex resolves in-test).
// Returns the frontend (handler-ready).
func seedSubagentsIndex(t *testing.T, fe *WebFrontend, lines ...string) {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	project := t.TempDir()
	if err := fe.store.Put(Binding{
		ConvID:      webConvID(),
		ProjectPath: project,
		SessionID:   "sess-A",
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Put binding: %v", err)
	}
	if len(lines) == 0 {
		return // bound project with no index — its own case
	}
	index, err := paths.SubagentsIndex(project)
	if err != nil {
		t.Fatalf("SubagentsIndex: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(index), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(index, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
}

func getSubagents(t *testing.T, srv *httptest.Server, query string) (*http.Response, []subagentFixture) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/subagents" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var rows []subagentFixture
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
			t.Fatalf("decode body: %v", err)
		}
	}
	return resp, rows
}

func TestWeb_SubagentsRequiresToken(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	if resp, _ := getSubagents(t, srv, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /subagents without token = %d, want 401", resp.StatusCode)
	}
	if resp, _ := getSubagents(t, srv, "?token=wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /subagents wrong token = %d, want 401", resp.StatusCode)
	}
}

func TestWeb_SubagentsGETOnly(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/subagents?token=test-token-0123456789abcdef", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /subagents = %d, want 405", resp.StatusCode)
	}
}

func TestWeb_SubagentsUnboundConv(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/subagents?token=test-token-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unbound conv = %d, want 400", resp.StatusCode)
	}
	var body struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "/bind") {
		t.Errorf("error %q should hint /bind", body.Error)
	}
}

func TestWeb_SubagentsList(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	seedSubagentsIndex(t, fe,
		// 柱 G sync row, terminal with tokens.
		`{"event":"started","sub_sid":"sync-1","ts":"2026-01-01T10:00:00Z","parent_sid":"sess-A","parent_turn":4,"type":"explore","description":"find handlers"}`,
		`{"event":"completed","sub_sid":"sync-1","ts":"2026-01-01T10:05:00Z","tokens":{"prompt":100,"completion":20,"cache_hit":80}}`,
		// D1 background row, still running.
		`{"event":"started","sub_sid":"bg-1","ts":"2026-01-02T09:00:00Z","parent_sid":"sess-A","type":"general-purpose","description":"scan deps","background":true}`,
		// D1+D3 background row, completed + notified.
		`{"event":"started","sub_sid":"bg-2","ts":"2026-01-02T10:00:00Z","parent_sid":"sess-A","type":"explore","description":"audit","background":true}`,
		`{"event":"completed","sub_sid":"bg-2","ts":"2026-01-02T10:09:00Z","tokens":{"prompt":50,"completion":10,"cache_hit":0}}`,
		`{"event":"notified","sub_sid":"bg-2","ts":"2026-01-02T10:10:00Z","parent_turn":8}`,
		// Another parent's row — must appear only under ?parent=.
		`{"event":"started","sub_sid":"other-1","ts":"2026-01-03T10:00:00Z","parent_sid":"sess-B","type":"plan"}`,
	)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, rows := getSubagents(t, srv, "?token=test-token-0123456789abcdef")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /subagents = %d, want 200", resp.StatusCode)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(rows), rows)
	}
	// Newest first: other-1, bg-2, bg-1, sync-1.
	wantOrder := []string{"other-1", "bg-2", "bg-1", "sync-1"}
	for i, sid := range wantOrder {
		if rows[i].SubSid != sid {
			t.Fatalf("row[%d] = %s, want %s (newest first): %+v", i, rows[i].SubSid, sid, rows)
		}
	}
	bySid := map[string]subagentFixture{}
	for _, r := range rows {
		bySid[r.SubSid] = r
	}
	if r := bySid["sync-1"]; r.Status != "completed" || r.Background || r.Tokens == nil || r.Tokens.Prompt != 100 {
		t.Errorf("sync-1 folded wrong: %+v", r)
	}
	if r := bySid["bg-1"]; r.Status != "active" || !r.Background {
		t.Errorf("bg-1 folded wrong: %+v", r)
	}
	if r := bySid["bg-2"]; r.Status != "completed" || !r.Background || !r.Notified {
		t.Errorf("bg-2 folded wrong (D1 marker + D3 delivery): %+v", r)
	}

	// ?parent= filter narrows to that parent session's rows.
	resp, rows = getSubagents(t, srv, "?token=test-token-0123456789abcdef&parent=sess-B")
	if resp.StatusCode != http.StatusOK || len(rows) != 1 || rows[0].SubSid != "other-1" {
		t.Fatalf("?parent=sess-B → %d rows (%+v), want [other-1]", len(rows), rows)
	}

	// Unknown parent → empty array, still 200.
	resp, rows = getSubagents(t, srv, "?token=test-token-0123456789abcdef&parent=nope")
	if resp.StatusCode != http.StatusOK || len(rows) != 0 {
		t.Fatalf("?parent=nope → %d rows, want 0", len(rows))
	}
}

func TestWeb_SubagentsBoundProjectWithoutIndex(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	seedSubagentsIndex(t, fe) // binding only, no subagents.jsonl
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, rows := getSubagents(t, srv, "?token=test-token-0123456789abcdef")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bound project without index = %d, want 200", resp.StatusCode)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0", len(rows))
	}
}

// TestWeb_SubagentsDetail (W3.3): the ?sub= mode — folded row plus
// the sub transcript's last assistant entry; unknown sid 404; a row
// with no transcript on disk answers "" (an answer, not an error);
// `sub` wins over a simultaneous `parent`.
func TestWeb_SubagentsDetail(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fe, project := newBoundWebWithIndex(t, fe,
		// Terminal background row with a transcript to read.
		`{"event":"started","sub_sid":"bg-2","ts":"2026-01-02T10:00:00Z","parent_sid":"sess-A","type":"explore","description":"audit","background":true}`,
		`{"event":"completed","sub_sid":"bg-2","ts":"2026-01-02T10:09:00Z","tokens":{"prompt":50,"completion":10,"cache_hit":0}}`,
		// Active row whose child has not written a transcript yet.
		`{"event":"started","sub_sid":"bg-1","ts":"2026-01-02T09:00:00Z","parent_sid":"sess-A","type":"general-purpose","description":"scan deps","background":true}`,
	)
	seedTranscript(t, project, "sess-A", "bg-2",
		`{"schema_version":2,"id":"h"}`,
		`{"role":"user","content":"audit"}`,
		`{"role":"assistant","content":"first pass"}`,
		`{"role":"assistant","content":"final audit findings"}`,
	)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	get := func(query string) (*http.Response, subagentDetailBody) {
		t.Helper()
		resp, err := http.Get(srv.URL + "/subagents" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body subagentDetailBody
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode detail: %v", err)
			}
		}
		return resp, body
	}

	resp, d := get("?token=test-token-0123456789abcdef&sub=bg-2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail bg-2 = %d, want 200", resp.StatusCode)
	}
	if d.Sub.SubSid != "bg-2" || d.Sub.Status != "completed" || !d.Sub.Background {
		t.Errorf("detail row = %+v, want completed background bg-2", d.Sub)
	}
	if d.LastAssistant != "final audit findings" {
		t.Errorf("last_assistant = %q, want the LAST assistant entry", d.LastAssistant)
	}

	// sub wins over parent (sub_sid is unique in the index).
	resp, d = get("?token=test-token-0123456789abcdef&sub=bg-2&parent=sess-B")
	if resp.StatusCode != http.StatusOK || d.Sub.SubSid != "bg-2" {
		t.Fatalf("sub+parent = %d %+v, want sub to win", resp.StatusCode, d.Sub)
	}

	// No transcript yet: "" last_assistant, still 200.
	resp, d = get("?token=test-token-0123456789abcdef&sub=bg-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail bg-1 = %d, want 200", resp.StatusCode)
	}
	if d.Sub.Status != "active" || d.LastAssistant != "" {
		t.Errorf("detail bg-1 = %+v %+q, want active with empty tail", d.Sub, d.LastAssistant)
	}

	// Unknown sid: 404.
	resp, _ = get("?token=test-token-0123456789abcdef&sub=nope")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown sub = %d, want 404", resp.StatusCode)
	}
}

// subagentDetailBody mirrors subagentDetail for the endpoint tests.
type subagentDetailBody struct {
	Sub           subagentFixture `json:"sub"`
	LastAssistant string          `json:"last_assistant"`
}

// newBoundWebWithIndex is seedSubagentsIndex returning the bound
// project path too (the detail tests write transcripts under it).
func newBoundWebWithIndex(t *testing.T, fe *WebFrontend, lines ...string) (*WebFrontend, string) {
	t.Helper()
	seedSubagentsIndex(t, fe, lines...)
	// seedSubagentsIndex redirects SEEK_HOME then binds a temp project;
	// recover that project via the default conv's binding.
	b, err := fe.store.Get(webConvID())
	if err != nil {
		t.Fatalf("Get binding: %v", err)
	}
	return fe, b.ProjectPath
}

// seedTranscript writes a sub transcript under the redirected SEEK_HOME
// (paths.SubagentSessionDir resolves there), the Runner's on-disk shape.
func seedTranscript(t *testing.T, project, parentSid, subSid string, lines ...string) {
	t.Helper()
	dir, err := paths.SubagentSessionDir(project, parentSid, subSid)
	if err != nil {
		t.Fatalf("SubagentSessionDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}
