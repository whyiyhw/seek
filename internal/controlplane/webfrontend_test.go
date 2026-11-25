package controlplane

// Web frontend MVP tests (card c_qucxtu acceptance): the handler-level
// contract via httptest — the embedded page, the token gate on POST
// /msg, the inbound pushed by a valid POST, and one SSE reply injected
// through Send. A Run-lifecycle test covers the real listener + banner
// + ctx shutdown. Real-browser smoke stays a post-merge action (the
// card parks it with the main session).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestWeb builds a WebFrontend wired to temp paths and a known
// token, with the inbox channel a test drains manually.
func newTestWeb(t *testing.T) (*WebFrontend, chan Inbound, string) {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "web.token")
	if err := os.WriteFile(tokenPath, []byte("test-token-0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	fe := NewWeb(WebConfig{
		TokenPath: tokenPath,
		StorePath: filepath.Join(dir, "bindings.json"),
	})
	in := make(chan Inbound, 8)
	fe.inbox = in
	return fe, in, "test-token-0123456789abcdef"
}

func TestWeb_RootServesEmbeddedPage(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("GET / content-type = %q, want text/html", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, webPage) {
		t.Errorf("GET / must serve the embedded page verbatim (got %d of %d bytes)", len(body), len(webPage))
	}
}

func TestWeb_MsgRequiresToken(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	post := func(url string) *http.Response {
		resp, err := http.Post(url, "application/json", strings.NewReader(`{"text":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post(srv.URL + "/msg"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /msg without token = %d, want 401", resp.StatusCode)
	}
	if resp := post(srv.URL + "/msg?token=wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /msg with wrong token = %d, want 401", resp.StatusCode)
	}
	if resp := post(srv.URL + "/msg?token=test-token-0123456789abcdef"); resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /msg with valid query token = %d, want 202", resp.StatusCode)
	}

	// The header path must work too (butler /today precedent: either).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/msg", strings.NewReader(`{"text":"hi"}`))
	req.Header.Set("Authorization", "Bearer test-token-0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("POST /msg with Bearer header = %d, want 202", resp.StatusCode)
	}
}

func TestWeb_MsgPushesInbound(t *testing.T) {
	fe, in, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/msg?token=test-token-0123456789abcdef",
		"application/json", strings.NewReader(`{"text":"/bind ~/code/foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	select {
	case m := <-in:
		if want := webConvID(); m.Conv != want {
			t.Errorf("inbound conv = %q, want %q", m.Conv, want)
		}
		if m.Text != "/bind ~/code/foo" {
			t.Errorf("inbound text = %q, want the slash command passed through verbatim", m.Text)
		}
		if !m.DirectAt {
			t.Error("browser messages are always direct: DirectAt must be true")
		}
		if m.EventID != "" {
			t.Error("EventID must stay empty — distinct messages are never deduped")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Inbound arrived on the plane channel")
	}
}

func TestWeb_MsgRejectsEmptyAndGarbage(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	for name, body := range map[string]string{
		"empty text":  `{"text":"   "}`,
		"not json":    `hello`,
		"wrong shape": `[1,2,3]`,
	} {
		resp, err := http.Post(srv.URL+"/msg?token=test-token-0123456789abcdef", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
}

func TestWeb_SSEReceivesInjectedReply(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// Stream the SSE endpoint, then inject one reply through Send —
	// the port the router delivers replies through — and assert the
	// exact event-json arrives.
	resp, err := http.Get(srv.URL + "/events?token=test-token-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	type sseEvent struct {
		Conv string `json:"conv"`
		Text string `json:"text"`
	}
	got := make(chan sseEvent, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue // "retry:", heartbeat comments, blank lines
			}
			var ev sseEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err == nil {
				got <- ev
			}
		}
	}()

	// Wait for subscription to register (the retry line is written
	// before the subscriber joins, so poll until Send lands).
	want := Reply{Text: "已绑定项目 /tmp/demo。\n第二行"}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := fe.Send(context.Background(), webConvID(), want); err != nil {
			t.Fatalf("Send: %v", err)
		}
		select {
		case ev := <-got:
			if ev.Text != want.Text {
				t.Errorf("sse text = %q, want %q (multi-line must survive as escaped JSON)", ev.Text, want.Text)
			}
			if ev.Conv != string(webConvID()) {
				t.Errorf("sse conv = %q, want %q", ev.Conv, webConvID())
			}
			return
		case <-time.After(100 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("no SSE event arrived within 2s of Send")
			}
		}
	}
}

func TestWeb_SendWithoutSubscriberDrops(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	if err := fe.Send(context.Background(), webConvID(), Reply{Text: "nobody home"}); err != nil {
		t.Errorf("Send with zero subscribers must be a no-op success, got %v", err)
	}
}

func TestWeb_StateReflectsBinding(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	get := func() map[string]any {
		resp, err := http.Get(srv.URL + "/state?token=test-token-0123456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := get(); m["bound"] != false {
		t.Errorf("fresh conversation must be unbound, got %v", m)
	}
	fe.store.Put(Binding{ConvID: webConvID(), ProjectPath: "/tmp/demo"})
	m := get()
	if m["bound"] != true || m["project"] != "/tmp/demo" {
		t.Errorf("after Put, state = %v, want bound /tmp/demo", m)
	}

	// No token → 401 like every stateful route.
	resp, err := http.Get(srv.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET /state without token = %d, want 401", resp.StatusCode)
	}
}

func TestWeb_RunLifecycle(t *testing.T) {
	dir := t.TempDir()
	fe := NewWeb(WebConfig{
		Addr:      freeAddr(t),
		TokenPath: filepath.Join(dir, "web.token"),
		StorePath: filepath.Join(dir, "bindings.json"),
		Banner:    &strings.Builder{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	in := make(chan Inbound, 1)
	go func() { done <- fe.Run(ctx, in) }()

	// Wait for the listener by polling the token-less GET / — polling
	// the banner Builder instead would race Run's write. The token file
	// is written before the listener opens, so once / answers it exists.
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get("http://" + fe.cfg.Addr + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("web frontend never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(filepath.Join(dir, "web.token"))
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	// POSIX mode assertion is Unix-only: Windows chmod is advisory,
	// Go reports the default 0666 there (same convention as
	// bindings_test.go).
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", info.Mode().Perm())
	}
	tokBytes, err := os.ReadFile(filepath.Join(dir, "web.token"))
	if err != nil || len(strings.TrimSpace(string(tokBytes))) < 16 {
		t.Errorf("token file content: %d bytes (%v), want ≥16", len(tokBytes), err)
	}

	resp, err := http.Post("http://"+fe.cfg.Addr+"/msg?token="+strings.TrimSpace(string(tokBytes)),
		"application/json", strings.NewReader(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("live POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("live POST status = %d, want 202", resp.StatusCode)
	}
	select {
	case m := <-in:
		if m.Conv != webConvID() {
			t.Errorf("live inbound conv = %q", m.Conv)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live POST never reached the plane channel")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run on ctx cancel = %v, want nil (clean shutdown)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	// Banner assertions AFTER Run returned: the done channel gives the
	// happens-before edge, so reading the Builder is race-free now.
	if s, ok := fe.cfg.Banner.(*strings.Builder); ok {
		line := strings.TrimSpace(s.String())
		if !strings.Contains(line, "http://"+fe.cfg.Addr+"/?token=") {
			t.Errorf("banner %q must be the loopback URL with the token", line)
		}
	} else {
		t.Error("test wiring: Banner must be a *strings.Builder")
	}
}

// TestWeb_PageAvoidsHTMLParsingSinks pins the XSS discipline (PRD §5.5,
// card c_iha3fr acceptance): model output reaches the DOM only via
// createElement/createTextNode/textContent — the embedded page AND its
// static assets (the markdown renderer is a resident since W5) must
// contain no HTML-string parsing sink, not even in a comment (a
// mention is a temptation; the static check is belt and braces).
func TestWeb_PageAvoidsHTMLParsingSinks(t *testing.T) {
	srcs := map[string]string{"page": string(webPage)}
	for _, e := range []string{"markdown.js"} {
		b, err := webStatic.ReadFile("static/" + e)
		if err != nil {
			t.Fatalf("static asset %s missing from embed: %v", e, err)
		}
		srcs[e] = string(b)
	}
	for name, src := range srcs {
		for _, sink := range []string{"innerHTML", "insertAdjacentHTML", "document.write"} {
			if strings.Contains(src, sink) {
				t.Errorf("%s references %q — text must reach the DOM via textContent/createTextNode only (§5.5)", name, sink)
			}
		}
	}
	// Positive pin: the W5 markdown renderer must exist as the first
	// static asset and build <pre> through the DOM API (regression
	// guard against a future "simpler" HTML-string rewrite).
	if !strings.Contains(srcs["markdown.js"], "document.createElement('pre')") {
		t.Error("static/markdown.js lost the fenced-code renderer (document.createElement('pre'))")
	}
}

// TestWeb_PageSessionsBrowser pins the W4.4 asset-plane UI contract: the
// embedded page consumes exactly the merged W4.2 method set over /rpc and
// walks messages by cursor. A method rename on either side strands the
// browser silently (a dead button, not a crash), so the names are pinned
// here against the page the same way the renderer is pinned above.
// (W5 c_txz6vz kept the data plane verbatim — only the chrome moved from
// a popup pane to the persistent sidebar, so these pins still hold.)
func TestWeb_PageSessionsBrowser(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"session/list",     // list pane
		"session/get",      // detail meta line
		"session/messages", // paginated message body
		"next_cursor",      // cursor pagination walked, not whole-file
		"加载更多",             // the load-more affordance exists
		"q('/rpc')",        // rides the same token'd /rpc route as chat
		"MSG_PAGE = 50",    // page size matches the server default (messagesDefaultLimit)
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the sessions browser piece %q (W4.4)", need)
		}
	}
	// The chat plane must survive the browser's addition untouched: the
	// browse toggle hides chat by class, it never unmounts it.
	for _, keep := range []string{"id=\"form\"", "id=\"list\"", "addEventListener('submit'"} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the chat-plane piece %q — W4.4 must be additive", keep)
		}
	}
}

// TestWeb_PageIconSystem pins the c_25d2ey icon/button language: every
// UI icon is an inline <symbol> in ONE sprite (vendored paths with the
// source + licence comment), call sites reuse it through <use>, the
// three button tiers exist as classes, and no emoji survives anywhere in
// the page source. Message content (user/model text) is runtime data and
// can still carry emoji — it never lives in this file.
func TestWeb_PageIconSystem(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"vendored 自 Lucide",                // source comment
		"ISC License",                      // licence comment
		`<symbol id="i-tasks"`,             // jobs/tasks semantics for the 后台 entry
		`<use href="#i-tasks"/>`,           // the nav button reuses it
		`<use href="#i-paperclip"/>`,       // attach icon
		`<use href="#i-mic"/>`,             // voice icon
		".btn-primary",                     // primary tier
		".btn-icon",                        // icon-button tier
		"fill: none; stroke: currentColor", // vendored d-only paths need the shell
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the icon-system piece %q (c_25d2ey)", need)
		}
	}
	if n := strings.Count(page, `<symbol id="i-`); n < 24 {
		t.Errorf("sprite has %d <symbol>s, want >= 24 (one definition per UI icon)", n)
	}
	// UI icons are SVG, not emoji/text glyphs. The page source carries no
	// message content, so any emoji here is an icon regression.
	emoji := regexp.MustCompile(`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{FE0F}]`)
	if m := emoji.FindString(page); m != "" {
		t.Errorf("embedded page still carries emoji %q — UI icons must be sprite SVG (c_25d2ey)", m)
	}
}

// TestWeb_PagePaneEntriesWired pins the five top-bar pane entries' click
// wiring. The audit entry lost its listener in a merge (the `panes.hooks`
// table kept the button, so every table-shaped pin stayed green) and only
// a real-browser hooksaudit run caught it — c_25d2ey. A string pin per
// entry is cheap; a missing line here is a dead entry.
func TestWeb_PagePaneEntriesWired(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"memBtn.addEventListener('click', () => togglePane('memory'))",
		"statBtn.addEventListener('click', () => togglePane('stats'))",
		"agentsBtn.addEventListener('click', () => togglePane('agents'))",
		"settingsBtn.addEventListener('click', () => togglePane('settings'))",
		"hkBtn.addEventListener('click', () => togglePane('hooks'))",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("pane entry lost its click wiring: %q (a dead top-bar entry)", need)
		}
	}
}

// TestWeb_PageSessionSidebar pins the left rail: the W5 card (c_txz6vz)
// made it persistent, and the workspace-tree card turned it into a
// TWO-LEVEL tree (workspace headers over session rows). What survives
// verbatim from W5 is pinned here — the rail, the collapse affordance
// with its localStorage bit and narrow-viewport default, the search box,
// the row paging, and the transcript swap on row click. The ＋新建 that
// used to live in the header moved onto each workspace header (its wire
// text is pinned in TestWeb_PageCommandSurface). It also asserts the
// retired W4.4 popup left no dead references behind: no browse button,
// no sess-view class, no sessions entry in the pane switcher.
func TestWeb_PageSessionSidebar(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"id=\"side\"",                   // the rail itself
		"id=\"side-toggle\"",            // collapse affordance
		"id=\"side-search\"",            // title/workspace filter input
		"id=\"side-ws-new\"",            // 新建工作区 (the tree's complement)
		"seek-side-collapsed",           // the remembered bit's key
		"localStorage.setItem(SIDE_KEY", // ... written back on toggle
		"innerWidth < 1024",             // narrow viewport defaults to collapsed
		"SESS_PAGE = 30",                // row page size (filter+page are client-side)
		"sess-detail",                   // main area swaps to a transcript on row click
		// the tree itself: groups, collapsed-group memory, the two gestures
		"wsGroups", "ws-head", "WS_CLOSED_KEY", "treeBind", "treeNewSession",
		// browse vs continue are deliberately two actions
		`id="sess-continue"`,
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the sidebar piece %q (W5 c_txz6vz / workspace tree)", need)
		}
	}
	// Acceptance ③ — the 📚 popup code is retired with no dead references.
	for _, gone := range []string{"browse-btn", "sess-view", "togglePane('sessions'"} {
		if strings.Contains(page, gone) {
			t.Errorf("embedded page still carries the retired W4.4 popup piece %q (W5 c_txz6vz must leave no dead reference)", gone)
		}
	}
	// The header ＋新建 that W5 shipped is retired in favour of per-header ＋
	// (a click on the old id would now be a no-op button — silently dead).
	if strings.Contains(page, `id="side-new"`) {
		t.Error("embedded page still carries #side-new — the workspace headers own the new-session action now")
	}
}

// TestWeb_PageThemeSettings pins the W5 theme/settings card (c_ni7mzw):
// the two token blocks and the single-attribute switch, the head
// bootstrap that runs BEFORE the stylesheet (that ordering is the whole
// anti-FOUC mechanism — a <script> after <style> would flash), the
// three-state runtime, and the read-only settings panel that consumes
// the `settings/get` method. Also pins the no-naked-colour rule: the
// style block must not carry raw hex outside the two token blocks, or a
// new component would silently stop following the theme.
func TestWeb_PageThemeSettings(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		":root[data-theme=\"light\"]",        // the light token block exists
		"--seek-inset",                       // semantic token names, not hex at call sites
		"localStorage.getItem('seek-theme')", // remembered choice
		"localStorage.setItem(THEME_KEY",     // ... written on toggle
		"localStorage.removeItem(THEME_KEY)", // system state = no remembered value
		"prefers-color-scheme: light",        // system follow
		"dataset.mode",                       // the three-state button group
		"settings/get",                       // the read-only snapshot method
		"id=\"set-body\"",                    // settings pane container
		"id=\"set-btn\"",                     // nav entry
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the theme/settings piece %q (W5 c_ni7mzw)", need)
		}
	}
	// Anti-FOUC ordering: the bootstrap block must come BEFORE <style>.
	// Anything else means the first paint uses the default theme and the
	// attribute lands afterwards — exactly the flash this card removes.
	// (The "<style>" search starts at the tag itself: prose higher in the
	// <head> names the tag too.)
	style := strings.Index(page, "\n<style>\n")
	boot := strings.Index(page, "localStorage.getItem('seek-theme')")
	if boot < 0 || style < 0 {
		t.Fatalf("page shape changed: boot=%d style=%d", boot, style)
	}
	if boot > style {
		t.Errorf("theme bootstrap (%d) sits AFTER <style> (%d) — that is the FOUC this card must remove", boot, style)
	}
	// Two token blocks, and the light one flips color-scheme too (native
	// scrollbars/form controls follow it).
	if strings.Count(page, "color-scheme:") < 2 {
		t.Errorf("want a color-scheme declaration per theme (dark + light)")
	}
	// No naked colour outside the token blocks: component rules must all
	// go through var(--seek-*), or the next component silently ignores
	// the theme. The two palette blocks are the exemption (they ARE the
	// palette), so the check starts after the light block's last line.
	styleEnd := strings.Index(page, "</style>")
	palAnchor := strings.Index(page, "--seek-shadow:0 4px 14px rgba(0,0,0,.12);")
	if styleEnd < 0 || palAnchor < 0 {
		t.Fatalf("palette shape changed: styleEnd=%d palAnchor=%d", styleEnd, palAnchor)
	}
	palEnd := strings.Index(page[palAnchor:], "}")
	if palEnd < 0 {
		t.Fatal("light token block unterminated")
	}
	rest := page[palAnchor+palEnd : styleEnd]
	if m := hexColourRE.FindString(rest); m != "" {
		t.Errorf("component CSS carries a naked colour %q — use a var(--seek-*) token so both themes follow", m)
	}
}

// hexColourRE matches #rgb / #rrggbb literals in CSS.
var hexColourRE = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)

// TestWeb_PageStreamingBubble pins the W2.1 chat increment: the page
// folds the W1.4 named SSE events into a streaming bubble — deltas
// accumulate per run id, tool calls render as folded single-line
// status rows — and the run-bearing reply replaces the whole bubble
// with the authoritative final text. The event names are the §2.5
// wire contract: a rename on either side silently strands the bubble
// (an eternal caret, not a crash), so both ends are pinned to the
// page the same way the renderer and the sessions browser are above.
func TestWeb_PageStreamingBubble(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"addEventListener('delta'",     // fold text deltas by run id
		"addEventListener('tool'",      // fold tool status rows by run id
		"runs.get(m.run)",              // reply close-out keys the live run
		"classList.remove('streaming'", // caret stops at close-out
		"renderBody(r.el, m.text)",     // authoritative text replaces the preview
		"replaceChildren()",            // re-render path clears before rebuilding
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the streaming-bubble piece %q (W2.1)", need)
		}
	}
	// The pre-W2.1 reply path must survive: run-less replies (command
	// acks, dropped-frame fallback) still render as plain messages.
	for _, keep := range []string{"addMsg('seek', m.text)", "addEventListener('submit'"} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the plain-reply piece %q — W2.1 must be additive", keep)
		}
	}
}

// TestWeb_PageStopButton pins the c_5m3fm6 dsh-InputBar alignment: while
// a streamed run is live the SEND key becomes the STOP key. The fork
// lives at the click layer — a submit event names #send as the
// submitter for Enter's implicit submission too, so click vs Enter
// cannot be told apart at submit — and the stop click sends the SAME
// /cancel command text as the top bar. The armed run's run-bearing ack
// closes the bubble WITHOUT the authoritative replace (on an
// interrupted run the accumulated deltas ARE the content): a cut
// marker is appended instead, stream state resets, and the key state
// follows the run table (first stream event flips it, close-out
// reverts it — no stop key when idle).
func TestWeb_PageStopButton(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"renderSend()",                  // key state is a function of the run table
		"runs.size > 0",                 // busy = a live streamed run
		"classList.toggle('stop'",       // the visual stop state
		"send.addEventListener('click'", // the fork sits at click, before submit
		"sendText('/cancel', false)",    // stop click = the /cancel command text
		"m.run === stopping",            // interrupted close keys the armed run
		"—— 已中断 ——",                     // the cut marker keeps the partial
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the stop-button piece %q (c_5m3fm6)", need)
		}
	}
	// Additivity: the plain send/stream paths must survive — Enter with a
	// draft still submits (conv-gate queuing is prior semantics).
	for _, keep := range []string{"addEventListener('submit'", "renderBody(r.el, m.text)"} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the prior send/stream piece %q — c_5m3fm6 must be additive", keep)
		}
	}
}

// pageFuncBody returns the source of a top-level JS function in the
// embedded page: from its declaration line (sig, e.g. "function
// closePhantom(") to the first "\n}\n" — a closing brace at column 0,
// which inside this page means the end of a top-level function. Fails
// when the declaration is gone: a renamed/deleted function is exactly
// what the pins below exist to catch.
//
// Why scope the assertions instead of a page-wide Contains: a page-wide
// check passes as soon as ANY code in the file spells the token, so a
// moved or rewritten function keeps the test green while the behaviour
// it pinned is gone.
func pageFuncBody(t *testing.T, page, sig string) string {
	t.Helper()
	i := strings.Index(page, sig)
	if i < 0 {
		t.Fatalf("embedded page lost the %q declaration", sig)
	}
	rest := page[i:]
	if j := strings.Index(rest, "\n}\n"); j > 0 {
		return rest[:j+2]
	}
	t.Fatalf("%q has no closing brace at column 0 — page structure changed shape", sig)
	return "" // unreachable
}

// TestWeb_PagePhantomRunResetsStopKey pins c_6mbjfq: the stop key must
// not stay stuck at ⏹ when a run's close-out frame was lost.
//
// The bug: SSE replays nothing, so a run that ends while the page is
// disconnected leaves the client-side run table holding an entry whose
// close-out never arrives — renderSend keeps reading runs.size > 0 and
// the send key stays a stop key forever. Clicking it sends /cancel,
// which the server answers run-less ("当前没有正在运行的任务。") — and
// the pre-fix page dropped that answer on the plain-message path
// without touching the table, so the key never recovered.
//
// The fix is client-only and adds no mechanism: reuse the c_5m3fm6
// "armed run" slot. A /cancel that comes back run-less is proof the
// server had no run left to interrupt, i.e. the armed run is over and
// only its close-out is missing — so close that entry the way an
// interrupted run is closed (keep the partial, mark it) and let
// renderSend take its runs.size = 0 branch.
//
// Rejected alternative (recorded here because the shape recurs): clear
// the run table on every (re)connect. EventSource reconnects on ANY
// transient blip, so that would drop the bookkeeping of a genuinely
// live run mid-stream — its later deltas would open a second bubble and
// the half already on screen would be orphaned. Age-based pruning needs
// per-run timestamps plus a sweep, i.e. a new resident mechanism.
func TestWeb_PagePhantomRunResetsStopKey(t *testing.T) {
	page := string(webPage)

	// The close helper itself: leaves the table, disarms /cancel at it,
	// reuses the c_5m3fm6 preserving close, and lets renderSend follow.
	body := pageFuncBody(t, page, "function closePhantom(")
	for _, need := range []string{
		"runs.delete(id)",                  // the phantom leaves the run table
		"if (lastRun === id) lastRun = ''", // …and stops arming /cancel at it
		"r.el.classList.remove('streaming')",
		"—— 已中断 ——",    // a lost close-out is still an interrupted run
		"renderSend()", // key state follows the table → runs.size = 0 → 发送
	} {
		if !strings.Contains(body, need) {
			t.Errorf("closePhantom lost %q — the phantom run would never leave the table (c_6mbjfq)", need)
		}
	}

	// The trigger is the armed /cancel's run-less answer — and only when
	// a stop was actually armed. Resetting on every run-less reply would
	// flip the key back to 发送 under a genuinely live run whenever some
	// other command's ack (commands skip the executor gate) lands
	// mid-stream.
	i := strings.Index(page, "} else if (m.text) {")
	if i < 0 {
		t.Fatal("page lost the run-less reply branch — the fix has nowhere to fire")
	}
	rest := page[i:]
	j := strings.Index(rest, "if (m.session_id)")
	if j < 0 {
		t.Fatal("page lost the session_id fold after the reply branches")
	}
	branch := rest[:j]
	for _, need := range []string{
		"if (stopping)", // gated on an armed run, never blanket
		"closePhantom(", // the armed run is closed as a phantom
		"stopping = ''", // and the arming is spent either way
	} {
		if !strings.Contains(branch, need) {
			t.Errorf("run-less /cancel answer does not reset the key: branch lost %q (c_6mbjfq)", need)
		}
	}

	// The rejected fix must stay rejected: a blunt table clear on
	// (re)connect drops live runs, and the page must carry no such call.
	if strings.Contains(page, "runs.clear()") {
		t.Error("page clears the whole run table — EventSource reconnects on any blip, so this orphans genuinely live bubbles (c_6mbjfq)")
	}
	// Additivity: the plain message path and the c_5m3fm6 run-bearing
	// close-out both survive the new branch.
	for _, keep := range []string{"addMsg('seek', m.text)", "m.run === stopping", "runs.delete(m.run)"} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the prior close-out piece %q — c_6mbjfq must be additive", keep)
		}
	}
}

// TestWeb_PageMemoryStats pins the W4.5 asset-plane UI contract: the
// page consumes exactly the W4.3 method set (memory/projects|list|get,
// stats/cache — plus W4.2's session/list as the stats picker) over the
// same /rpc route. A rename on either side strands a dead button, so
// the names are pinned against the page like the sessions set above.
func TestWeb_PageMemoryStats(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"memory/projects", // project picker
		"memory/list",     // entry list pane
		"memory/get",      // entry detail
		"stats/cache",     // the cache/cost view
		"cost_usd",        // cost is read server-priced, never computed here
		"q('/rpc')",       // rides the same token'd /rpc route
		"id=\"mem-proj\"", // the two pickers exist
		"id=\"stat-sess\"",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the memory/stats piece %q (W4.5)", need)
		}
	}
	// Additivity: chat plane AND the sessions browser both survive the
	// W4.5 panes (the pane switcher hides by class, never unmounts).
	// W5 c_txz6vz moved the sessions list into the persistent sidebar
	// (the 📚 popup button retired), so the pin rides the sidebar ids.
	for _, keep := range []string{
		"id=\"form\"", "addEventListener('submit'",
		"id=\"sess-list\"", "id=\"side-toggle\"", "session/messages", "加载更多",
	} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the prior-plane piece %q — W4.5 must be additive", keep)
		}
	}
	// The client-never-prices rule (§2.5): rate math must not creep into
	// the page — fmtUSD formats magnitude, it does not price.
	if strings.Contains(page, "PerMTok") || strings.Contains(page, "pricing") {
		t.Errorf("embedded page carries pricing vocabulary — rate cards live only in internal/pricing (§2.5)")
	}
}

// TestWeb_PageStatsTrend pins the cross-session trend card (c_6xktw4):
// the page consumes stats/trend over the same /rpc route, draws with
// hand-written SVG (document.createElementNS, zero vendor — §4.2), keeps
// the hover readout + window switcher, carries an empty-state hint, and
// still never prices anything itself (§2.5).
func TestWeb_PageStatsTrend(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"stats/trend",        // the aggregation method
		"createElementNS",    // hand-drawn SVG, no chart library
		"id=\"trend-plot\"",  // the plot host
		"id=\"trend-tools\"", // window switcher + legend live here
		"trend-tip",          // hover readout (built at render time)
		"pointermove",        // hover interaction
		"TREND_WINDOWS",      // time-window switching
		"还没有历史会话",            // empty-state copy
		"折线需要 ≥2 个点",         // one-point boundary copy
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the trend-chart piece %q (c_6xktw4)", need)
		}
	}
	// Zero-vendor (§4.2): no chart library may sneak in as an asset.
	for _, gone := range []string{"chart.js", "d3.min", "echarts", "plotly"} {
		if strings.Contains(strings.ToLower(page), gone) {
			t.Errorf("page references a chart vendor %q — the trend line is hand-drawn SVG (zero build chain)", gone)
		}
	}
	// The sidebar cost column is formatted server-priced data, never a rate.
	if !strings.Contains(page, "t2c") {
		t.Error("sidebar row lost its per-session cost cell (c_6xktw4)")
	}
	if strings.Contains(page, "PerMTok") || strings.Contains(page, "pricing.") {
		t.Error("page carries pricing vocabulary — cost_usd is read server-priced (§2.5)")
	}
}

// freeAddr grabs an OS-assigned loopback port and gives it back. The
// classic listen-close-reuse race is accepted: test-only, retry-free,
// and collisions on loopback within one test binary are near-zero.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
}

// TestWeb_DefaultAddrPinned: the zero WebConfig resolves to the
// loopback default and DefaultWebAddr keeps its shipped value — both
// are compat surfaces (configs that omit web_addr; scripts and
// tailscale forwards that point at 127.0.0.1:8790). Temp paths keep
// the serve dir out of the picture; only Addr resolution is exercised.
func TestWeb_DefaultAddrPinned(t *testing.T) {
	if DefaultWebAddr != "127.0.0.1:8790" {
		t.Errorf("DefaultWebAddr = %q, want 127.0.0.1:8790 (compat pin)", DefaultWebAddr)
	}
	dir := t.TempDir()
	fe := NewWeb(WebConfig{
		TokenPath: filepath.Join(dir, "web.token"),
		StorePath: filepath.Join(dir, "bindings.json"),
	})
	if fe.cfg.Addr != DefaultWebAddr {
		t.Errorf("zero WebConfig addr = %q, want DefaultWebAddr", fe.cfg.Addr)
	}
}

// TestWeb_RunBadAddr: a malformed address fails Run's listen with the
// address in the error. The listen owns syntax and port-in-use
// validation (scope is gated earlier — TestWeb_AddrScope) and there is
// never a silent fallback to the default address. Run returns the
// listen error synchronously, before the serve loop parks, so the call
// needs no goroutine.
func TestWeb_RunBadAddr(t *testing.T) {
	dir := t.TempDir()
	const bad = "127.0.0.1:no-port-digits"
	fe := NewWeb(WebConfig{
		Addr:      bad,
		TokenPath: filepath.Join(dir, "web.token"),
		StorePath: filepath.Join(dir, "bindings.json"),
	})
	err := fe.Run(context.Background(), make(chan Inbound, 1))
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("bad addr must fail Run naming the address, got %v", err)
	}
}

// TestWeb_AddrScope: the three states of the web_addr allowlist
// (2026-11-15 decision, c_x7ehom rework) — loopback passes, tailnet
// CGNAT passes, everything else is refused with the address named.
// Boundary IPs pin the /10 mask: 100.63.x and 100.128.x are NOT
// tailnet. 127.0.0.2 documents that the whole 127/8 is loopback.
func TestWeb_AddrScope(t *testing.T) {
	for _, tc := range []struct {
		addr string
		ok   bool // want allowed
	}{
		// Loopback family.
		{"127.0.0.1:8790", true},
		{"127.0.0.2:8790", true},
		{"[::1]:8790", true},
		{"localhost:8790", true},
		{"LOCALHOST:8790", true},
		// Tailnet CGNAT 100.64.0.0/10, boundaries included.
		{"100.64.0.0:8790", true},
		{"100.80.25.74:8790", true},
		{"100.127.255.254:8790", true},
		// Everything else: all-interfaces, LAN, public, just outside
		// the CGNAT block, and arbitrary hostnames.
		{"0.0.0.0:8790", false},
		{"[::]:8790", false},
		{":8790", false}, // empty host = every interface
		{"192.168.1.5:8790", false},
		{"8.8.8.8:8790", false},
		{"100.63.255.254:8790", false}, // one below the CGNAT block
		{"100.128.0.1:8790", false},    // one above it
		{"example.com:8790", false},
	} {
		err := checkWebAddr(tc.addr)
		if tc.ok {
			if err != nil {
				t.Errorf("checkWebAddr(%q) = %v, want allowed", tc.addr, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("checkWebAddr(%q) = nil, want refusal", tc.addr)
			continue
		}
		if !strings.Contains(err.Error(), tc.addr) {
			t.Errorf("refusal must name the address %q, got %v", tc.addr, err)
		}
		if !strings.Contains(err.Error(), "100.64.0.0/10") {
			t.Errorf("refusal must state the allowlist for %q, got %v", tc.addr, err)
		}
	}
	// Syntax stays the listen's business (TestWeb_RunBadAddr): an
	// address SplitHostPort cannot cut is out of the scope check's
	// jurisdiction and must fall through untouched.
	if err := checkWebAddr("127.0.0.1:not-a-port"); err != nil {
		t.Errorf("scope check must not judge syntax, got %v", err)
	}
}

// TestWeb_RunForbiddenAddr: a well-formed but out-of-scope web_addr
// (0.0.0.0) never reaches a listener — NewWeb's deferred cfgErr
// surfaces synchronously from Run, naming the address and the
// allowlist (the servecli plane-level exit is TestServePlane_
// BadWebAddrFails in internal/servecli).
func TestWeb_RunForbiddenAddr(t *testing.T) {
	dir := t.TempDir()
	const bad = "0.0.0.0:8790"
	fe := NewWeb(WebConfig{
		Addr:      bad,
		TokenPath: filepath.Join(dir, "web.token"),
		StorePath: filepath.Join(dir, "bindings.json"),
	})
	err := fe.Run(context.Background(), make(chan Inbound, 1))
	if err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("out-of-scope addr must fail Run naming the address, got %v", err)
	}
	if !strings.Contains(err.Error(), "100.64.0.0/10") {
		t.Errorf("refusal must state the allowlist, got %v", err)
	}
}

// TestWeb_PageMobileResponsive pins the c_lqtt2m 390px card: one ≤480px
// breakpoint owns every narrow-screen degradation — drawer-ised sidebar
// (expanded state overlays the centre column; collapsed keeps the 44px
// rail so side-toggle stays reachable), wrapping command bar, ellipsis
// for the status tail and long setting values, stacked label/value rows,
// and the column-stacked asset panes. The pre selector pairing is the
// markdown-card gap this card closed (see the pitfalls entry): a fenced
// block in a session transcript must ride the same horizontal-scroll
// rule as .msg pre — that container is .sbody (the card first wrote
// .smsg, which the merge narrowed: .sbody covers the same nodes with
// lower specificity, leaving the tool-tree .tpre rules intact).
// All additive strings — a rename strands the
// behaviour silently, so they are pinned the way W5's other cards are.
func TestWeb_PageMobileResponsive(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"@media (max-width: 480px)",        // the one narrow breakpoint
		"side-scrim",                       // drawer dismiss layer (element + rules)
		"matchMedia('(max-width: 480px)')", // JS breakpoint == CSS breakpoint
		"drawerClose()",                    // row pick closes the drawer
		"flex-wrap: wrap",                  // command bar wraps, nothing unreachable
		".msg pre, .sbody pre",             // code-block h-scroll covers ALL body containers
		"--seek-scrim",                     // scrim colour is a themed token
		"body.side-collapsed #side-head { flex-direction: column", // 44px rail stacks its icons
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the mobile-responsive piece %q (c_lqtt2m)", need)
		}
	}
	// The scrim must be inert at desktop widths: base display:none with
	// the flip living INSIDE the breakpoint block, or desktop gains a
	// click-swallowing full-viewport layer.
	scrim := strings.Index(page, "side-scrim")
	block := strings.Index(page, "@media (max-width: 480px)")
	baseFlip := strings.Index(page, "#side-scrim { display: none;")
	if scrim < 0 || block < 0 || baseFlip < 0 {
		t.Fatalf("page shape changed: scrim=%d block=%d baseFlip=%d", scrim, block, baseFlip)
	}
	if baseFlip > block {
		t.Errorf("scrim base rule must stay display:none OUTSIDE the breakpoint (got baseFlip=%d inside block=%d)", baseFlip, block)
	}
}

// TestWeb_PageInputEnhancements pins the c_7lsngm input contract: a
// multi-line input that sends on Enter and newlines on Shift+Enter, a
// registry-driven '/' command menu (↑↓ / Tab / Enter / Esc), and an '@'
// session reference fed by session/list. The keyboard paths are the
// acceptance surface, and every one of them is a string in the page — a
// rename or a "simpler" rewrite strands the feature silently, so they are
// pinned here the way W2.1/W4.4/W4.5 pin their own pieces.
func TestWeb_PageInputEnhancements(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		// ① multi-line: textarea, Enter sends, Shift+Enter falls through
		`<textarea id="input"`,
		"ev.key === 'Enter' && !ev.shiftKey",
		"Shift+Enter",     // the comment stating the contract
		"function grow()", // height follows content
		// ② '/' command menu: registry + filter + keyboard navigation.
		// The registry is a RESIDUE list now: the workspace tree owns
		// project/session navigation, so only the two gestures the tree
		// has no equivalent for stay typeable (/resume, /cancel).
		"const CMDS = [",
		"'/resume'", "'/cancel'",
		"function slashHits()",
		"ev.key === 'ArrowDown'",
		"ev.key === 'ArrowUp'",
		"ev.key === 'Tab'",
		"ev.key === 'Escape'",
		"function pick(i)",
		// ③ '@' session reference: session/list source + resume semantics
		"function atToken()",
		"rpc('session/list', {})",
		"sendText('/resume ' + sid, false)",
		// the overlay itself, and its narrow-screen degradation
		`id="menu"`,
		"@media (max-width: 640px)",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the c_7lsngm input-enhancement piece %q", need)
		}
	}
	// The chat plane keeps its old entry points: the form still submits
	// through the ONE passthrough, and the quick buttons stay wired.
	// Enter does not grow a second send path — it calls form.requestSubmit(),
	// so the attachment gate / "/" argument rule stay single-sourced.
	// (The top-bar command buttons are retired — the workspace tree carries
	// project/session navigation, pinned in TestWeb_PageCommandSurface; this
	// guard keeps only the SHARED passthrough pieces.)
	for _, keep := range []string{
		`id="form"`, "sendText(typed, true, ready)", "form.requestSubmit()",
	} {
		if !strings.Contains(page, keep) {
			t.Errorf("input enhancement must be additive — lost %q", keep)
		}
	}
	// The overlay must never capture keys globally: every listener that
	// touches menu navigation hangs off the input element, and the page
	// adds no document/window keydown handler.
	if strings.Contains(page, "document.addEventListener('keydown'") ||
		strings.Contains(page, "window.addEventListener('keydown'") {
		t.Error("input enhancement must not install a global keydown handler (Esc must not be hijacked page-wide)")
	}
}
