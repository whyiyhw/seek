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
// matched the default shape, mirroring wantFrame's strictness — via
// containsFrameText, which also matches the needle's JSON-escaped
// spelling (see there).
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
				if !containsFrameText(fr.data, s) {
					t.Fatalf("default frame %s lacks %q", fr.data, s)
				}
			}
			return fr
		case <-deadline:
			t.Fatalf("no default frame containing %v arrived within 5s", substrs)
		}
	}
}

// containsFrameText matches s inside a RAW SSE data line (Go-marshalled
// JSON, never decoded). A needle rides that wire in two spellings:
// verbatim (CJK, plain ASCII — json.Marshal leaves them untouched) or
// JSON-escaped (backslashes doubled, < > & as \u003c-class escapes).
// Windows temp paths (`C:\Users\…`, doubled on the wire) and
// angle-bracket text can ONLY match the escaped spelling, so both are
// tried. Without this the /projects assertions pass on `/`-paths and
// fail on Windows nightly (2026-11-18), and the < case has to be
// dodged by hand (the old "/bind usage" comment's workaround).
func containsFrameText(data, s string) bool {
	if strings.Contains(data, s) {
		return true
	}
	b, err := json.Marshal(s)
	if err != nil {
		return false // unmarshalable needle can't ride the wire anyway
	}
	return strings.Contains(data, string(b[1:len(b)-1])) // strip outer quotes
}

// TestContainsFrameText pins the helper against a Windows-shaped frame:
// built by the SAME json.Marshal the production pump uses, so the wire
// carries doubled backslashes and \u003c escapes — exactly what a
// `/`-path CI lane never exercises. All three needle classes must
// match, and absent needles must not.
func TestContainsFrameText(t *testing.T) {
	payload := map[string]string{"text": "bound to " + `C:\Users\runner\proj` + " <here>, 已绑定"}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	frame := string(b)
	for _, s := range []string{
		"bound",                // plain ASCII: verbatim spelling
		`C:\Users\runner\proj`, // Windows path: escaped-only spelling
		"<here>",               // angle brackets: \u003c-escaped on the wire
		"已绑定",                  // CJK: verbatim
	} {
		if !containsFrameText(frame, s) {
			t.Errorf("containsFrameText(windows frame, %q) = false, want true", s)
		}
	}
	if containsFrameText(frame, "absent-needle") {
		t.Error("containsFrameText must not match absent needles")
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
	// incomplete): usage text, binding untouched. (Needles containing
	// angle brackets are fine now — containsFrameText also matches the
	// JSON-escaped spelling, where < rides as \u003c.)
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
//
// c_lnsdvu rewrote TWO of the five behaviors: #cmd-bind / #cmd-projects no
// longer prefill '/bind ' / send '/projects' — both open the project picker
// (third #menu source: project/list ∪ settings/get, click-a-row sends
// '/bind <root>'). The pins below assert the NEW wiring explicitly and the
// banned list below THAT asserts the old wireCmd shapes are gone — a
// behavior change must be rewritten in the test, never just deleted.
func TestWeb_PageCommandButtons(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		`id="cmd-bind"`,
		`id="cmd-projects"`,
		`id="cmd-switch"`,
		`id="cmd-new"`,
		`id="cmd-cancel"`,
		"sendText(typed, true, ready)",      // the form still rides the shared passthrough
		"sendText(text, false)",             // …and so do the buttons
		"wireCmd('cmd-switch', '/switch ')", // remaining arg command still prefills…
		"wireCmd('cmd-new', '/new')",        // …zero-arg still sends verbatim
		"wireCmd('cmd-cancel', '/cancel')",
		// c_lnsdvu project picker: both trigger buttons ride openBindPicker…
		"document.getElementById('cmd-bind').addEventListener('click', openBindPicker)",
		"document.getElementById('cmd-projects').addEventListener('click', openBindPicker)",
		// …candidates come from the two read-only rpc sources (no new method)…
		"rpc('project/list', {})",
		"rpc('settings/get', {})",
		// …a picked row sends the same wire a hand-typed /bind would…
		"sendText('/bind ' + e.root, false)",
		// …with the reset warning as the popup's fixed head line, the
		// current binding marked, and the both-sources-empty wording.
		"点选会重置当前会话（/bind 语义）；路径也可手打",
		"meta: (boundProject && cleanRoot(boundProject) === e.root) ? '当前' : ''",
		"没有可选项目：config serve.projects 未配置、本机也还没有 seek 会话记录——直接手打 /bind <路径>",
		"typed.startsWith('/')", // command POST → immediate /state refresh
		"body.startsWith('/')",
		"es.onopen = () => { connSet(true); refreshState(); apResync(); };", // reconnect restores the binding display + the approval card stack (W5 c_n37tnr) + the conn light (W5 c_oyp2qf)
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the command-UI piece %q", need)
		}
	}
	// The retired behaviors, pinned ABSENT: the old wireCmd prefill/direct-
	// send for the two picker buttons must not sneak back beside the new
	// wiring (both handlers on one button = the silent one wins).
	for _, banned := range []string{
		"wireCmd('cmd-bind'",
		"wireCmd('cmd-projects'",
	} {
		if strings.Contains(page, banned) {
			t.Errorf("embedded page still wires the retired behavior %q — c_lnsdvu replaced it with openBindPicker", banned)
		}
	}
}

// TestWeb_PageDirPicker (c_heffj3 acceptance ②): the /bind directory
// picker's browser-side pieces, pinned the W4.4 way (page strings
// against the embedded page) — the fs/list call, the entry row shown
// in the '/bind ' args context (piece 1 c_lnsdvu's candidate rows
// merge in front of it), the pick action riding the ONE sendText
// passthrough, the layer header, and the in-layer error branch. Also
// pins the no-new-global-keydown discipline: the picker's nav keys
// live on the textarea and the filter input, never on document/window.
func TestWeb_PageDirPicker(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"rpc('fs/list'", // the ONE new read-only method the picker rides
		"label: '浏览目录…', icon: 'folder'", // 目录模式入口（片 1 候选列表尾行；c_25d2ey 换 folder sprite）
		"/^\\/bind\\s/",                    // '/bind ' 参数期触发
		"sendText('/bind ' + path, false)", // 选这个目录 → 既有 /bind 透传，零新写面
		"setBtn(up, 'arrow-up', '上一级')",    // c_25d2ey：↑ 字符换成 sprite 图标
		"选这个目录",
		"目录名过滤",
		"label: '（' + dm.err + '）'",       // 失败在弹层内一行提示，不外抛不白屏
		"if (dirMode) return;",            // 目录模式独占弹层
		"menu.contains(ev.relatedTarget)", // 焦点进弹层（过滤框）不关层
		"leftEllipsis",                    // 层头路径过长从左侧省略
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the dir-picker piece %q", need)
		}
	}
	for _, banned := range []string{
		"document.addEventListener('keydown'", // 不新增全局键盘监听（c_7lsngm 纪律）
		"window.addEventListener('keydown'",
	} {
		if strings.Contains(page, banned) {
			t.Errorf("embedded page grew a global keydown listener: %q", banned)
		}
	}
}
