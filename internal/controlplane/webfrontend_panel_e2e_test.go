package controlplane

// W3.3 acceptance e2e (card c_ujgmpu; feature-web-frontend §9 W3.3):
// spawn a background child through a REAL subagent.Manager (stub
// Runner writing the production transcript shape) and watch the panel
// surfaces react — the SSE topic pushes `started`/`completed` without
// any list poll (面板实时出条目), GET /subagents folds the row, and the
// ?sub= detail reads the sub transcript's last assistant entry.
//
// This test doubles as the card's orchestration event-shape 核对: the
// events flow through the REAL writer (Manager.appendEvent — started
// with the background marker, completed with tokens) and fold through
// the REAL W3.1 reader, so a drift between orchestration's landed
// shapes and the reader's fixtures fails here, not in production.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/subagent"
)

// TestWeb_PanelE2EBackgroundSpawn is the W3.3 acceptance: a background
// spawn (stub Runner) appears on the panel in real time — subscribe
// FIRST, snapshot empty, spawn, started push (active bg row) arrives
// via SSE without polling, completed push (terminal + tokens) follows,
// the list folds the row, and the detail reads the transcript tail.
func TestWeb_PanelE2EBackgroundSpawn(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fastTopicPoll(t)

	// The panel's project: a temp dir bound to web:default, its state
	// tree under a redirected SEEK_HOME (seedTopicProject's shape, but
	// inlined because the Manager needs the same project path).
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

	// A real Manager over that project; the stub Runner plays the
	// production Runner's on-disk role (writes transcript.jsonl into
	// job.SessionDir, returns a summary + tokens).
	policy, err := permission.New(project, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	mgr, err := subagent.NewManager(subagent.ManagerOpts{
		ProjectAbsPath:   project,
		ParentSidFn:      func() string { return "sess-A" },
		ParentTracker:    cache.New(),
		ParentPolicy:     policy,
		ProjectSectionFn: func() string { return "" },
		SkillManifestFn:  func() string { return "" },
		ParentToolNamesFn: func() []string {
			return []string{"read", "grep", "list_dir"}
		},
		Runner: func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
			writeStubTranscript(t, job.SessionDir)
			return subagent.RunnerResult{
				Summary: "stub findings",
				Tokens:  subagent.Tokens{Prompt: 700, Completion: 90, CacheHit: 600},
				Turns:   3,
			}, nil
		},
		SessionCtx: sessionCtx,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sessionCancel(); mgr.Shutdown() }) // let the bg goroutine land before TempDir cleanup

	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// Panel protocol: subscribe FIRST, then snapshot — the anchor at
	// subscribe time means history never replays and every spawn event
	// after this point pushes.
	tap, resp := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&topic=subagents", "default")
	defer resp.Body.Close()

	// Snapshot before the spawn: bound project, empty fold.
	listResp, rows := getSubagents(t, srv, "?token=test-token-0123456789abcdef")
	if listResp.StatusCode != http.StatusOK || len(rows) != 0 {
		t.Fatalf("pre-spawn list = %d rows (status %d), want 0", len(rows), listResp.StatusCode)
	}

	// The spawn — real event shapes through the real writer.
	res := mgr.SpawnBackground(subagent.SpawnArgs{
		Description: "panel e2e scan",
		Prompt:      "scan the repo",
		Type:        subagent.TypeExplore,
	})
	if !strings.HasPrefix(res.Summary, "[agent: background started sub-sid=") {
		t.Fatalf("SpawnBackground handle: %s", res.Summary)
	}

	// Real-time entry: the `started` push arrives via SSE (no list
	// polling involved), carrying the folded ACTIVE background row.
	msg := wantTopicMsg(t, tap, "started", res.SubSid)
	if msg.Sub.Status != "active" || !msg.Sub.Background {
		t.Errorf("started push = %+v, want active background row", msg.Sub)
	}
	if msg.Sub.Description != "panel e2e scan" || msg.Sub.ParentSid != "sess-A" || msg.Sub.Type != "explore" {
		t.Errorf("started push folded fields = %+v (shape drift vs W3.1 reader)", msg.Sub)
	}

	// The stub Runner returns at once; the `completed` push follows
	// with the terminal row and the tokens from the event.
	msg = wantTopicMsg(t, tap, "completed", res.SubSid)
	if msg.Sub.Status != "completed" || msg.Sub.Tokens == nil || msg.Sub.Tokens.Prompt != 700 {
		t.Errorf("completed push = %+v, want terminal row with tokens", msg.Sub)
	}

	// The list fold agrees with the pushes.
	listResp, rows = getSubagents(t, srv, "?token=test-token-0123456789abcdef")
	if listResp.StatusCode != http.StatusOK || len(rows) != 1 {
		t.Fatalf("post-spawn list = %d rows (status %d), want 1", len(rows), listResp.StatusCode)
	}
	if rows[0].SubSid != res.SubSid || rows[0].Status != "completed" || !rows[0].Background || rows[0].Tokens == nil {
		t.Errorf("folded row = %+v, want completed background row with tokens", rows[0])
	}

	// The detail reads the sub transcript's LAST assistant entry —
	// the summary 单源 the index deliberately does not carry.
	detail, err := http.Get(srv.URL + "/subagents?token=test-token-0123456789abcdef&sub=" + res.SubSid)
	if err != nil {
		t.Fatal(err)
	}
	defer detail.Body.Close()
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("GET ?sub= status = %d, want 200", detail.StatusCode)
	}
	var d subagentDetailBody
	if err := json.NewDecoder(detail.Body).Decode(&d); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if d.Sub.SubSid != res.SubSid || d.Sub.Status != "completed" {
		t.Errorf("detail row = %+v, want the completed spawn", d.Sub)
	}
	if d.LastAssistant != "stub final assistant message" {
		t.Errorf("last_assistant = %q, want the transcript's last assistant entry", d.LastAssistant)
	}

	// The card's orchestration 事件形状核对, made raw: the REAL writer's
	// on-disk lines carry the fields the W3.1 reader's hand-made
	// fixtures assumed (background marker on started, tokens on
	// completed) — the wire dialect, not just the folded projection.
	index, err := paths.SubagentsIndex(project)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	startedLine, completedLine := "", ""
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(line, `"sub_sid":"`+res.SubSid+`"`) {
			if strings.Contains(line, `"event":"started"`) {
				startedLine = line
			}
			if strings.Contains(line, `"event":"completed"`) {
				completedLine = line
			}
		}
	}
	if !strings.Contains(startedLine, `"background":true`) || !strings.Contains(startedLine, `"parent_sid":"sess-A"`) {
		t.Errorf("real started line misses the reader-dialect fields: %s", startedLine)
	}
	if !strings.Contains(completedLine, `"tokens":`) {
		t.Errorf("real completed line misses the reader-dialect tokens field: %s", completedLine)
	}
}

// writeStubTranscript writes the production Runner's on-disk shape into
// a subagent session dir: session JSONL v2 — header line, then
// deepseek.Message lines.
func writeStubTranscript(t *testing.T, sessionDir string) {
	t.Helper()
	lines := []string{
		`{"schema_version":2,"id":"stub","created_at":"2026-01-01T00:00:00Z"}`,
		`{"role":"user","content":"scan the repo"}`,
		`{"role":"assistant","content":"stub first assistant message"}`,
		`{"role":"assistant","content":"stub final assistant message"}`,
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "transcript.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write stub transcript: %v", err)
	}
}

// TestWeb_PageTaskPanel pins the task panel's page contract the same
// way TestWeb_PageSessionsBrowser pins W4.4: the surfaces the panel
// consumes, the topic-only listener discipline, and the chat plane
// staying mounted (additive growth).
//
// c_4doebx re-pinned the data half: the panel now reads /rpc
// subagent/list|get (the inspect-rpc §6 #12 close-out) instead of GET
// /subagents — the old endpoint stays mounted as the compatibility口
// (TestWeb_Subagents* covers it), so its disappearance from the page
// is a page change, not an endpoint removal.
func TestWeb_PageTaskPanel(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"subagent/list",                // list reads the /rpc method
		"subagent/get",                 // detail reads the /rpc method
		"topic=subagents",              // the panel's SSE topic opt-in
		"addEventListener('subagents'", // named-event listener ONLY —
		// no onmessage on the topic conn
		"syncTopicStream",  // subscribe lifecycle tied to panel + binding
		"syncAgentRefresh", // the polling half of "refresh" (c_4doebx)
		"AG_REFRESH_MS",    // ... and its interval
		"agRows",           // push merge keyed by sub_sid
		"last_assistant",   // detail field from subagent/get
		"id=\"ag-fields\"", // detail expansion (same info as ?sub=)
		"st-running",       // three-state badge classes
		"st-done",
		"st-error",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the task-panel piece %q (W3.3/c_4doebx)", need)
		}
	}
	// The chat plane and sessions browser must survive the panel's
	// addition (additive growth, same rule as W4.4).
	for _, keep := range []string{"id=\"agents\"", "id=\"form\"", "id=\"sess-list\""} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the %q piece — W3.3 must be additive", keep)
		}
	}
	// The topic connection must not render chat frames: the panel
	// handler is addEventListener-only, and assigning onmessage on the
	// SAME connection would double-render every reply.
	if strings.Contains(page, "topicES.onmessage") {
		t.Error("topic connection must not set onmessage (chat frames ride it)")
	}
	// The panel must not hash paths in JS to build a project_id: the
	// server hands it over in /state (one authority for the id rule).
	if strings.Contains(page, "crypto.subtle") {
		t.Error("page must not hash project paths client-side — /state carries project_id")
	}
}
