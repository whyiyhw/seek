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
