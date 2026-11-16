package controlplane

// W2.2 command-UI tests (card c_evbzyh acceptance; feature-web-frontend
// §3.1 item 2 / §9 W2.2): the five quick-button commands driven through
// the exact wire a button rides — POST /msg plain text → REAL Router →
// binding store → SSE reply → GET /state — with the re-exec stub seek
// (happy mode) supplying one real run so the session writeback (D-C3)
// lands in /state. The browser-side half is pinned the W4.4 way (page
// strings against the embedded page): buttons exist, every send rides
// the ONE shared passthrough, arg commands prefill instead of sending
// half a command, a successful command POST triggers the immediate
// /state refresh, and SSE (re)connect restores the binding display.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newCmdE2E mirrors newStreamE2E (webfrontend_stream_test.go) but seeds
// RouterConfig.Projects so /projects has candidates to render — the
// router, pump shape, stub executor and frontend wiring are otherwise
// identical to the W1.4 harness.
func newCmdE2E(t *testing.T, projects []string) (*WebFrontend, *httptest.Server, string) {
	t.Helper()
	fe, in, tok := newTestWeb(t)
	ex, _ := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_MODE", "happy")
	r := NewRouter([]Frontend{fe}, OpenBindingStoreAt(fe.cfg.StorePath), ex, RouterConfig{Projects: projects})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for m := range in {
			go r.Handle(ctx, m) // Serve's shape: one goroutine per inbound
		}
	}()
	t.Cleanup(func() { close(in) })
	srv := httptest.NewServer(fe.handler())
	t.Cleanup(srv.Close)
	return fe, srv, tok
}

// getState reads GET /state for the conv a header-less client addresses
// (the legacy web:default — the conv this whole e2e rides).
func getState(t *testing.T, srv *httptest.Server, tok string) map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + "/state?token=" + tok)
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /state = %d, want 200", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode /state: %v", err)
	}
	return m
}

// wantDefaultFrame waits for the next default-event frame (name == "",
// the MVP reply shape command acks and run close-outs ride) while the
// W1.4 named family — delta/tool/usage of the interleaved prompt run —
// passes by unasserted. Substrings are checked on the ONE frame that
// matched the default shape, mirroring wantFrame's strictness.
func wantDefaultFrame(t *testing.T, tap <-chan wireFrame, substrs ...string) wireFrame {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case fr, ok := <-tap:
			if !ok {
				t.Fatalf("stream closed while waiting for a default frame containing %v", substrs)
			}
			if fr.name != "" {
				continue // delta/tool/usage of an interleaved run
			}
			for _, s := range substrs {
				if !strings.Contains(fr.data, s) {
					t.Fatalf("default frame %s lacks %q", fr.data, s)
				}
			}
			return fr
		case <-deadline:
			t.Fatalf("no default frame containing %v arrived within 5s", substrs)
		}
	}
}

// TestWeb_CommandE2E is the W2.2 acceptance: every quick-button command
// through the real wire. Each step waits for the command's SSE reply
// BEFORE asserting /state — the router Puts the binding before it
// replies, so the observed reply is the happens-before edge (this is
// also what the page's post-command refreshState relies on: whichever
// of the two refreshes lands last sees the settled state).
func TestWeb_CommandE2E(t *testing.T) {
	projA, projB := t.TempDir(), t.TempDir()
	fe, srv, tok := newCmdE2E(t, []string{projA, projB})
	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	waitSubs(t, fe, webConvID(), 1)

	// /projects while unbound: both candidates listed, none marked.
	postMsg(t, srv, tok, "/projects")
	wantDefaultFrame(t, tap, "候选项目", projA, projB, "当前未绑定")

	// /new before any binding: the shared unbound hint, not a crash.
	postMsg(t, srv, tok, "/new")
	wantDefaultFrame(t, tap, "该会话还没绑定项目")

	// /bind without its path argument (the prefilled input sent
	// incomplete): usage text, binding untouched. (Substrings avoid
	// angle brackets: the SSE payload is Go-marshaled JSON, where <
	// rides as \u003c.)
	postMsg(t, srv, tok, "/bind")
	wantDefaultFrame(t, tap, "用法：/bind", "目录必须存在")
	if m := getState(t, srv, tok); m["bound"] != false {
		t.Fatalf("state after bare /bind = %v, want still unbound", m)
	}

	// /bind projA: ack, then /state flips to bound with a fresh
	// (empty) session.
	postMsg(t, srv, tok, "/bind "+projA)
	wantDefaultFrame(t, tap, "已绑定项目")
	if m := getState(t, srv, tok); m["bound"] != true || m["project"] != projA || m["session"] != "" {
		t.Fatalf("state after /bind = %v, want bound %s with empty session", m, projA)
	}

	// One real run (stub seek): the close-out reply carries a run id
	// (W1.4 envelope) and the session writeback lands in /state — the
	// state leg the /new button then resets.
	postMsg(t, srv, tok, "hello")
	fr := wantDefaultFrame(t, tap, "Hello hello")
	if !strings.Contains(fr.data, `"run":"`) {
		t.Errorf("prompt close-out must carry a run id: %s", fr.data)
	}
	sess := getState(t, srv, tok)["session"]
	if sess == nil || sess == "" {
		t.Fatalf("state after the run = %v, want the written-back session id", sess)
	}

	// /projects again: the bound candidate now carries the ← 当前 mark.
	postMsg(t, srv, tok, "/projects")
	wantDefaultFrame(t, tap, projA+"  ← 当前")

	// /new: project kept, session dropped.
	postMsg(t, srv, tok, "/new")
	wantDefaultFrame(t, tap, "已开新会话")
	if m := getState(t, srv, tok); m["project"] != projA || m["session"] != "" {
		t.Fatalf("state after /new = %v, want %s with the session cleared (was %v)", m, projA, sess)
	}

	// /switch projB: rebind, session stays fresh.
	postMsg(t, srv, tok, "/switch "+projB)
	wantDefaultFrame(t, tap, "已切换项目")
	if m := getState(t, srv, tok); m["project"] != projB || m["session"] != "" {
		t.Fatalf("state after /switch = %v, want %s with empty session", m, projB)
	}

	// /cancel with nothing live: the honest ack, binding untouched
	// (the live-run half is TestWeb_StreamCancelClosesRun, W1.4).
	postMsg(t, srv, tok, "/cancel")
	wantDefaultFrame(t, tap, "当前没有正在运行的任务")
	if m := getState(t, srv, tok); m["project"] != projB {
		t.Fatalf("state after /cancel = %v, want binding untouched", m)
	}
}

// TestWeb_PageCommandButtons pins the W2.2 page contract (a rename or a
// "simpler" rewrite strands the feature silently — a dead button, not a
// crash): five buttons, the shared passthrough, arg-prefill vs verbatim
// send, the post-command /state refresh and the SSE-reconnect binding
// restore. Chat-plane keep-pins live in TestWeb_PageSessionsBrowser.
func TestWeb_PageCommandButtons(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		`id="cmd-bind"`,
		`id="cmd-projects"`,
		`id="cmd-switch"`,
		`id="cmd-new"`,
		`id="cmd-cancel"`,
		"sendText(input.value.trim(), true)", // the form still rides it
		"sendText(text, false)",              // …and so do the buttons
		"wireCmd('cmd-bind', '/bind ')",      // arg commands prefill…
		"wireCmd('cmd-switch', '/switch ')",
		"wireCmd('cmd-projects', '/projects')", // …zero-arg send verbatim
		"wireCmd('cmd-new', '/new')",
		"wireCmd('cmd-cancel', '/cancel')",
		"text.startsWith('/')",             // command POST → immediate /state refresh
		"es.onopen = () => refreshState()", // reconnect restores the binding display
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W2.2 command-UI piece %q", need)
		}
	}
}
