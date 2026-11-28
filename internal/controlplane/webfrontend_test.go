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
//
// SEEK_HOME is isolated here for the whole web surface (butler c_7vpiz5,
// 坑8). Today /attach stages under the serve dir derived from StorePath —
// no path resolver reaches ~/.seek — so this is belt-and-braces rather
// than a fix for a live leak: it keeps any FUTURE paths.* call in a web
// handler from landing in the developer's real ~/.seek (the ACP
// fileroute leak shape, cmd/seek/acp_test.go).
func newTestWeb(t *testing.T) (*WebFrontend, chan Inbound, string) {
	t.Helper()
	// Default (not override): a test that already pinned SEEK_HOME to a
	// dir it also hand-writes fixtures into keeps its own home.
	if os.Getenv("SEEK_HOME") == "" {
		t.Setenv("SEEK_HOME", t.TempDir())
	}
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
		`<use href="#i-plus"/>`,            // attach icon (W6-4: round ＋ button; paperclip symbol stays vendored for the tray)
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
		// W6-6: the settings entry opens the MODAL, not a middle-pane face.
		"settingsBtn.addEventListener('click', toggleSettings)",
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
		"viewing",                       // W6-5 history view body class (was sess-detail)
		// the tree itself: groups, collapsed-group memory, the two gestures
		"wsGroups", "ws-head", "WS_CLOSED_KEY", "treeBind", "treeNewSession",
		// W6-5: click-a-session enters the history view; lazy /resume keeps
		// the browse-vs-continue safety (see TestWeb_PageLazyResumeW65)
		"viewing", `id="hist"`,
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
	// W6-2 (c_onxxge) reintroduces #side-new as the sidebar's full-width
	// primary action (dsh shape) — this time wired to /new · openDirPicker,
	// so the W5 prohibition is lifted; the per-workspace ＋ stays too.

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
		// 未选条目时元信息行与标签行是空的——不兜底会留下两条只有 padding
		// 与下边框的空带（2026-11-27 复核实录）。
		"#mem-meta:empty, #mem-tags:empty { display: none; }",
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
		// fmtUSD 自带 $：成本行的标签写成 '$' 会渲染成「$ $0.00xx」（行首
		// 标签与悬停读数共用它，2026-11-27 复核实录）。
		"label: '成本', v: p => p.cost_usd || 0, f: fmtUSD",
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
	// The sidebar row's per-session cost is formatted server-priced data,
	// never a rate. W6-2 moved it from a visible .t2c cell into the row's
	// title tooltip (single-line rows); the pin follows the carrier.
	if !strings.Contains(page, "fmtUSD(s.cost_usd)") {
		t.Error("sidebar row lost its per-session cost readout (c_6xktw4 → W6-2 title tip)")
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
// breakpoint owns every narrow-screen degradation — the sidebar opens as a
// drawer over the centre column (W6-8: the 56px rail stays underneath so
// side-toggle stays reachable), wrapping command bar, ellipsis
// for the status tail and long setting values, stacked label/value rows,
// and the column-stacked asset panes. The pre selector pairing is the
// markdown-card gap this card closed (see the pitfalls entry): a fenced
// block in a session transcript must ride the same horizontal-scroll
// rule as .msg pre — that container is .sbody (the card first wrote
// .msg.seek > .txt (W6-3), which the merge narrowed: covers the same nodes with
// lower specificity, leaving the tool-tree .tpre rules intact).
// All additive strings — a rename strands the
// behaviour silently, so they are pinned the way W5's other cards are.
func TestWeb_PageMobileResponsive(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"@media (max-width: 480px)",              // the one narrow breakpoint
		"side-scrim",                             // drawer dismiss layer (element + rules)
		"matchMedia('(max-width: 480px)')",       // JS breakpoint == CSS breakpoint
		"drawerClose()",                          // row pick closes the drawer
		"flex-wrap: wrap",                        // command bar wraps, nothing unreachable
		".msg pre, #ag-last pre",                 // code-block h-scroll covers ALL body containers (W6-3: .sbody retired)
		"--seek-scrim",                           // scrim colour is a themed token
		".side-rail #side-head { display: grid;", // the icon rail centres its buttons (W6-8)
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

// TestWeb_PageVisualBase pins the W6-1 card (c_pozbmd): the page's whole
// visual base moves to dsh's measured shell values, with the type stack
// as the load-bearing piece. Four assertions, each structural:
//
//  1. the BODY rule carries no monospace family — the old
//     `font: 14px/1.5 ui-monospace, Menlo, monospace` is what made the
//     entire app read like a terminal log, and one `font: inherit` user
//     was enough to spread it everywhere;
//  2. in the style block (comments stripped — prose names the stack too)
//     `monospace` appears ZERO times and `Menlo` exactly ONCE, on the
//     --seek-mono declaration: the code stack deliberately has no bare
//     `monospace` tail (Windows CJK drops to SimSun, spec §b);
//  3. `ui-monospace` is gone from the page entirely — the old body
//     shorthand was its only home, and the token stack deliberately
//     starts at SF Mono instead;
//  4. the design-scale tokens are converged: font sizes come from the
//     14/13/12 + 21/19/18 title ladder (16px is the documented iOS
//     input-zoom guard inside the ≤640px block), weights from
//     400/500/600/700, radii from 8/12/22/999. (16px additionally carries
//     the W6-2 brand row — still inside the allowed set.)
//
// Raw-hex discipline is pinned by TestWeb_PageThemeSettings; this test
// only guards the places W6-1 rewrote, so a later card cannot quietly
// bring the mono body or a stray 11px back.
func TestWeb_PageVisualBase(t *testing.T) {
	page := string(webPage)
	start := strings.Index(page, "\n<style>\n")
	end := strings.Index(page, "</style>")
	if start < 0 || end < start {
		t.Fatalf("style block shape changed: start=%d end=%d", start, end)
	}
	style := page[start:end]
	// Comments are stripped FIRST: the token block's own comment spells
	// out "monospace" while explaining why the stack must not use it.
	stripped := cssCommentRE.ReplaceAllString(style, "")

	// ① the body rule: no mono family, and the sans stack comes from the token.
	bodyIdx := strings.Index(stripped, "\n  body {")
	if bodyIdx < 0 {
		t.Fatalf("body rule not found in the style block")
	}
	bodyEnd := strings.Index(stripped[bodyIdx:], "}")
	if bodyEnd < 0 {
		t.Fatal("body rule unterminated")
	}
	body := stripped[bodyIdx : bodyIdx+bodyEnd]
	for _, gone := range []string{"monospace", "Menlo", "ui-monospace"} {
		if strings.Contains(body, gone) {
			t.Errorf("body rule still names %q — the page-wide mono body is exactly the W6-1 defect", gone)
		}
	}
	for _, need := range []string{"font-family: var(--seek-sans)", "font-size: 14px", "line-height: 24px"} {
		if !strings.Contains(body, need) {
			t.Errorf("body rule lost %q (W6-1: 14/24 on the system sans stack)", need)
		}
	}

	// ② the mono stack exists exactly once, and it has no bare `monospace`.
	if n := strings.Count(stripped, "monospace"); n != 0 {
		t.Errorf("style block names monospace %d time(s) — the code stack must stop at Consolas (Windows CJK falls to SimSun, spec §b)", n)
	}
	if n := strings.Count(stripped, "Menlo"); n != 1 {
		t.Errorf("Menlo must appear exactly once (inside --seek-mono), got %d", n)
	}
	if !strings.Contains(stripped, `--seek-mono: "SF Mono", "JetBrains Mono", Menlo, Consolas;`) {
		t.Errorf("--seek-mono is missing or its stack changed — pre/code/kbd/.mono all ride this token")
	}
	// ③ `ui-monospace` must be gone from the page: it was the old body
	// shorthand's family, and the token stack starts at SF Mono instead.
	// (The comments (already stripped above) name the old rule while
	// explaining the fix, which is why this checks `stripped`.)
	if n := strings.Count(stripped, "ui-monospace"); n != 0 {
		t.Errorf("ui-monospace appears %d time(s) — the page's mono face is --seek-mono only (W6-1 §2)", n)
	}
	// Mono may only be reached through the token: a literal family anywhere
	// else means a second stack sneaked in beside the token.
	for _, line := range strings.Split(stripped, "\n") {
		if !strings.Contains(line, "font-family:") {
			continue
		}
		if strings.Contains(line, "var(--seek-sans)") || strings.Contains(line, "var(--seek-mono)") {
			continue
		}
		t.Errorf("literal font-family outside the token layer: %q", strings.TrimSpace(line))
	}

	// ④ the design scale is converged (the ladder the manifest counts).
	allowedSize := map[string]bool{"12": true, "13": true, "14": true, "16": true, "18": true, "19": true, "21": true, "24": true} // 24 = W6-4 empty-state Hero title
	for _, m := range reFontSize.FindAllStringSubmatch(stripped, -1) {
		if !allowedSize[m[1]] {
			t.Errorf("font-size %spx is outside the 14/13/12 + 21/19/18 ladder (16px = iOS input-zoom guard only)", m[1])
		}
	}
	for _, m := range reFontWeight.FindAllStringSubmatch(stripped, -1) {
		switch m[1] {
		case "400", "500", "600", "700":
		default:
			t.Errorf("font-weight %q is outside the 400/500/600/700 set", m[1])
		}
	}
	for _, m := range reBorderRadius.FindAllStringSubmatch(stripped, -1) {
		switch m[1] {
		case "8", "12", "22", "24", "999":
		default:
			t.Errorf("border-radius %spx is outside the 8/12/22/24/999 scale", m[1])
		}
	}

	// ⑤ 悬停行的行内操作组不靠底色盖字。行底被 --seek-hover 提过一档之后，
	// 一块 --seek-panel 底的补丁会在行上显形（亮偏浅、暗偏深）——让位改由
	// 行自己的 padding 承担。这个形态静态截图容易漏，先钉在这里。
	actsIdx := strings.Index(stripped, "\n  .sess-acts {")
	if actsIdx < 0 {
		t.Fatalf(".sess-acts rule not found in the style block")
	}
	actsEnd := strings.Index(stripped[actsIdx:], "}")
	if actsEnd < 0 {
		t.Fatal(".sess-acts rule unterminated")
	}
	if strings.Contains(stripped[actsIdx:actsIdx+actsEnd], "background:") {
		t.Errorf(".sess-acts paints its own background — a mask colour cannot follow the hover/selected/archived row layers")
	}
	yieldIdx := strings.Index(stripped, ".sess-row:hover, .sess-row:focus-within { padding-right: 68px; }")
	editingIdx := strings.Index(stripped, ".sess-row.editing { padding-right: 12px; }")
	if yieldIdx < 0 || editingIdx < yieldIdx {
		t.Errorf("hover row must yield to its inline actions, and the editing state must opt out AFTER it (same specificity): yield=%d editing=%d", yieldIdx, editingIdx)
	}
}

var (
	cssCommentRE   = regexp.MustCompile(`/\*[\s\S]*?\*/`)
	reFontSize     = regexp.MustCompile(`font-size:\s*([\d.]+)px`)
	reFontWeight   = regexp.MustCompile(`font-weight:\s*([\w.]+)`)
	reBorderRadius = regexp.MustCompile(`border-radius:\s*([\d.]+)px`)
)

// TestWeb_PageSkeletonW62 pins the W6-2 skeleton swap (c_onxxge): the top
// #bar row is GONE, the five view buttons live inside #side-foot, the
// visible binding info moved into a #conv-head project chip, and #status
// survives as a visually-hidden aria-live node (its text feeds ~18 headless
// probes — the node may not be deleted, only hidden). Positional
// assertions, the same style as W5's skeleton pins: a rename that strands
// the wiring fails here instead of silently dead-buttoning in production.
func TestWeb_PageSkeletonW62(t *testing.T) {
	page := string(webPage)
	if strings.Contains(page, `<nav id="bar"`) {
		t.Error("the #bar top row is still present — W6-2 removed it (view buttons moved to #side-nav)")
	}
	// The five view buttons must sit between #side-foot's opening tag and the
	// sidebar's closing </aside> (inside the foot, not merely on the page).
	foot := strings.Index(page, `<div id="side-foot">`)
	endAside := strings.Index(page, `</aside>`)
	if foot < 0 || endAside < 0 || foot > endAside {
		t.Fatalf("page shape changed: side-foot=%d </aside>=%d", foot, endAside)
	}
	for _, id := range []string{`id="mem-btn"`, `id="stat-btn"`, `id="agents-btn"`, `id="hk-btn"`, `id="set-btn"`} {
		at := strings.Index(page, id)
		if at < 0 || at < foot || at > endAside {
			t.Errorf("view button %q is not inside #side-foot (pos %d, foot %d..%d)", id, at, foot, endAside)
		}
	}
	for _, need := range []string{
		`id="conv-head"`,     // the one conversation header
		`id="composer-meta"`, // meter row under the composer
		`id="side-new"`,      // full-width new-session button
		`id="side-find"`,     // search toggle (collapsed #side-search)
		`<header id="status" class="warn" aria-live="polite">`, // hidden but alive
		`clip-path: inset(50%)`,                                // the visually-hidden pattern itself
		`fmtAgo`,                                               // relative time on session rows
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-2 skeleton piece %q (c_onxxge)", need)
		}
	}
	// 工作区小节头三枚图标钮的 DOM 顺序 = 卡面做③：搜索 / 归档 / 新建工作区。
	// 没有 CSS order 重排，DOM 序即视觉序与 Tab 序，三者必须一致（首版把后两枚
	// 写反了，2026-11-27 巡检指出）。
	iFind := strings.Index(page, `id="side-find"`)
	iArch := strings.Index(page, `id="side-arch"`)
	iWsNew := strings.Index(page, `id="side-ws-new"`)
	if iFind < 0 || iArch < iFind || iWsNew < iArch {
		t.Errorf("sidebar header icon order drifted: want #side-find < #side-arch < #side-ws-new, got %d/%d/%d", iFind, iArch, iWsNew)
	}
}

// TestWeb_PageChatFlowW63 pins the W6-3 conversation-flow unification
// (c_ovly6d): one 768px reading column, the four message forms (user
// right-aligned bubble on a themed token, frameless assistant, one-line
// tool calls, gray system lines), and — the load-bearing retirement —
// the page no longer emits the "tool · " role label anywhere, and the
// two renderers (live addMsg/runBubble + history histTurn) share one
// class vocabulary.
func TestWeb_PageChatFlowW63(t *testing.T) {
	page := string(webPage)
	if strings.Contains(page, "tool · ") {
		t.Error("page still emits the 'tool · ' role label — W6-3 retired it (results merge into the call row)")
	}
	for _, need := range []string{
		"max-width: 768px",           // the reading column
		"var(--seek-user-bubble)",    // user bubble rides a themed token, not a bare hex
		"--seek-user-bubble:#1f2a44", // dark value pinned
		"--seek-user-bubble:#edf3fe", // light value pinned
		".tcall",                     // the one tool-call row shape (live + history)
		"function histTurn",          // history renderer rides the same forms
		"turn-meta",                  // per-turn hover copy affordance
		"dataset.mi",                 // message-index stamp (blocks ≠ messages now)
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-3 chat-flow piece %q (c_ovly6d)", need)
		}
	}
	for _, gone := range []string{".smsg", ".swho", ".stool", ".ttree", ".tnode", "appendMsgPlain", "appendMsgTree"} {
		if strings.Contains(page, gone) {
			t.Errorf("page still carries the retired W4/W5 renderer piece %q (W6-3 must delete, not stack)", gone)
		}
	}
	// 旧消息类的单类规则随四形态一并删除（返工第 1 轮，2026-11-27）：留着会命中
	// 新类名——`.seek { background }` 让每条助手回复整块灰底，`.hint { italic }`
	// 让系统灰行变斜体。用户气泡右对齐的是阅读列里的气泡：消息盒上不能有
	// align-self（会把整个 768 盒推到容器右缘，气泡跑出阅读列）。#sess-msgs 的
	// 内边距只由对话流那段的容器规则给，自己的规则里不能再写 padding（W6-5 起
	// 只读浏览器退役、#sess-msgs 不复存在，对它的正向钉只留在下面两层分支）。
	for _, gone := range []string{
		"\n  .you {",
		"\n  .seek {",
		"\n  .hint {",
		".msg.user { align-self",
		"#sess-msgs { flex: 1; overflow-y: auto; padding",
	} {
		if strings.Contains(page, gone) {
			t.Errorf("page still carries the retired message rule %q (W6-3 must delete, not stack)", gone)
		}
	}
	for _, need := range []string{
		".msg.user { display: flex; flex-direction: column;",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-3 reading-column geometry rule %q (c_ovly6d rework)", need)
		}
	}
	// 返工第 2 轮（2026-11-27）：80% 上限必须落在气泡规则上。写进 `.msg.user`
	// 是白写——`#list > *, #sess-msgs > *` 的 (1,0,0) 会盖掉 (0,2,0) 的
	// max-width，长气泡照旧撑满 768 列（图解 06/13/20 实证）。移动端的 94%
	// 同理，一起钉住，别让它下次又落回消息盒上。
	if !strings.Contains(page, ".msg.user .bub { background: var(--seek-user-bubble); border-radius: 22px;\n                   padding: 10px 16px; max-width: 80%;") {
		t.Errorf("user bubble rule lost max-width: 80%% — the cap must sit on .msg.user .bub, not on the (1,0,0)-outranked .msg.user box (c_ovly6d rework 2)")
	}
	for _, gone := range []string{
		"\n              align-items: flex-end; max-width: 80%; }",
		"    .msg.user { max-width: 94%",
	} {
		if strings.Contains(page, gone) {
			t.Errorf("page still caps the user MESSAGE box instead of the bubble (%q) — #list > * outranks it, the cap never applies (c_ovly6d rework 2)", gone)
		}
	}
	if !strings.Contains(page, ".msg.user .bub { max-width: 94%; }") {
		t.Error("mobile user bubble lost its own max-width: 94% rule (c_ovly6d rework 2)")
	}
}

// TestWeb_PageComposerW64 pins the W6-4 composer card + empty hero
// (c_ljtmy4): the form is a 774px r=22 card with a two-row layout (textarea
// + action row), #send is a round brand-blue icon button (semantics ride
// aria-label/title, no text), the empty state renders the Hero structure
// (mark + "seek" title) and vertically centers the group, and an unbound
// plain-text send opens the directory picker instead of posting.
func TestWeb_PageComposerW64(t *testing.T) {
	page := string(webPage)
	// 空态会话头是绝对定位的，包含块必须是主栏——#center 不定位时它横穿侧栏、
	// 盖住品牌行（W6-4 遗留，2026-11-27 返工）。
	for _, need := range []string{
		"border-radius: 22px",  // the card itself (form rule)
		"max-width: 774px",     // card width == reading column frame
		"border-radius: 999px", // round send (+ attach/mic/chips)
		`id="ws-chip"`,         // workspace chip in the action row
		`id="model-tag"`,       // model label (hidden until known)
		"hero-t",               // empty-state Hero title class
		"chat-empty-on",        // the centering body class
		"applyWsChip",          // chip/placeholder follow binding
		"先选一个工作区",              // unbound plain-text send → picker copy
		"body.chat-empty-on:not(.viewing) #center { position: relative; }",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-4 composer piece %q (c_ljtmy4)", need)
		}
	}
	if !strings.Contains(page, "#send:disabled { opacity: .4; }") {
		t.Error("round send lacks its 40% disabled treatment (W6-4 ①)")
	}
	// 空态布局与「让聊天面让位的视图」的组合：面板打开按内容态处理（会话头回
	// 文档流、内容顶对齐；W6-4 首版/二版缺陷，2026-11-27 返工）。看历史一态的
	// 规则由 TestWeb_PageLazyResumeW65 钉，只读浏览器已随 W6-5 退役。几何由
	// scripts/web-composer-manual.sh 无头量测。
	for _, need := range []string{
		"body.chat-empty-on #center { justify-content: center; }",
		"body.browsing.chat-empty-on #center { justify-content: flex-start; }",
		"body.browsing.chat-empty-on #conv-head { position: static; }",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-4 empty-state geometry rule %q (c_ljtmy4 rework)", need)
		}
	}
}

// TestWeb_PageComposerMobilePlaceholder pins the ≤640px short placeholder
// (card c_xhwykc): W6-8 fixed the 390px composer width, but the bound-state
// placeholder was still 22 chars at 16px — at 390px it wrapped onto a second
// line that rows=1 clipped (02/04/12/26 全中). Narrow widths now get a
// one-line copy; desktop keeps the long one.
func TestWeb_PageComposerMobilePlaceholder(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"matchMedia('(max-width: 640px)')", // same breakpoint as the 16px iOS-zoom guard
		"@media (max-width: 640px)",        // …and as the CSS block that raises the size
		"const PLACE_NARROW = '发消息，/ 看指令'",
		"const PLACE_LONG = '直接发消息；/ 看指令，@ 引用历史会话'",
		// one switch site: a copy drifting on its own puts the wrap back.
		"inputNarrowMQ && inputNarrowMQ.matches ? PLACE_NARROW : PLACE_LONG",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("composer placeholder switch lost %q (c_xhwykc)", need)
		}
	}
	// 短文案得真能放进 390px 的一行：16px 下内容宽约 260px（实测 textW 120.75px），
	// 旧长文案 283px 才放不下。静态侧钉住「明显更短」+ 保住了 / 指令提示。
	narrow := "发消息，/ 看指令"
	if n := len([]rune(narrow)); n > 12 {
		t.Errorf("窄屏占位 %q 有 %d 字——390px 一行（≈260px / 16px）装不下，至少要比长文案短一半", narrow, n)
	}
	if !strings.Contains(narrow, "/") {
		t.Errorf("窄屏占位 %q 丢了 / 指令入口提示", narrow)
	}
}

// TestWeb_PageLazyResumeW65 pins the W6-5 click-to-enter flow (c_mo7quv):
// the read-only browser (#browser/#sess-detail/#sess-back/#sess-continue) is
// GONE, the history renders into #hist with the same four-form renderer, and
// — the load-bearing safety — sending from a viewed session routes through
// ensureContext, which posts "/resume <sid>" (or the lazy /new · /bind)
// BEFORE the body and gives up (keeping the draft) on failure.
func TestWeb_PageLazyResumeW65(t *testing.T) {
	page := string(webPage)
	for _, gone := range []string{"sess-continue", "sess-back", "sess-detail", `id="browser"`, `id="sess-msgs"`} {
		if strings.Contains(page, gone) {
			t.Errorf("page still carries the retired read-only browser piece %q (W6-5 must delete, not stack)", gone)
		}
	}
	// 看历史是内容态（2026-11-27 返工）：实时列表空着时 chat-empty-on 还挂着，
	// 空态布局（会话头绝对化/内容居中/composer 上浮）不能跟进历史视图。
	// 提示可见 + 看历史闸（同日二次返工，验证 blocker ③）：看历史态 #list 是
	// display:none，懒收口的提示/错误必须落到 #hist、错误行用红字；当前会话
	// 生成中时发送钮禁用（停止去实时视图做）。W6-5 主提交还连带删了「面板打开
	// 时聊天面让位」的规则（32 张面板镜头全破没人看，web-subagents 一族红）——
	// 找回并钉住；面板从看历史态进入时会话头/高亮回实时口径。
	for _, need := range []string{
		"ensureContext",      // the lazy-context split lives in one function
		"'/resume ' + sid",   // the resume POST is built before the body
		"pendingFresh",       // lazy /new · /bind for the new-session buttons
		"body.viewing #list", // history view hides (not destroys) the live DOM
		`id="hist"`,          // history renders into #hist
		`id="fresh-hero"`,    // the lazy new-session empty state
		"草稿已保留",              // failure path keeps the draft
		"body.viewing.chat-empty-on #conv-head { position: static; }",
		"body.viewing.chat-empty-on #center { justify-content: flex-start; }",
		"body.viewing.chat-empty-on form { margin-bottom: 10px; }",
		"function ctxNotice(kind, text)",
		"ctxNotice('err', '（' + e.message + '——草稿已保留）')",
		".msg.err { color: var(--seek-err)",
		"if (runs.size > 0) { send.disabled = viewingOther(); return; }",
		"function gatedNow() { return runs.size > 0 && viewingOther(); }",
		"body.browsing #list, body.browsing #hist, body.browsing #hist-foot,",
		"body.browsing form, body.browsing #composer-meta { display: none; }",
		"body.browsing.chat-empty-on #conv-head { position: static; }",
		"if (wasViewing) markCur();",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-5 lazy-resume piece %q (c_mo7quv)", need)
		}
	}
}

// TestWeb_PageAssetPanelsW67 pins the W6-7 shape: memory / stats / agents /
// hooks share one head (.pane-bar — name + count | tools + close) and put
// their content in the 768 reading column; the list+detail panels show the
// detail only while a row is selected (.has-sel is the projection of
// memSel / agOpen), side by side from 1200px and as a replacement with a
// back row below that; empty states are one centred line — no italic, no
// full-width-parenthetical placeholders. Also the trend chart (merged-in
// item D): row labels sit above their own line, never where it starts.
func TestWeb_PageAssetPanelsW67(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		`<section id="agents" class="pane">`, // agents joins the .pane family
		`data-pane="agents"`, `data-pane="memory"`, `data-pane="stats"`, `data-pane="hooks"`,
		`<span class="pane-title">`,
		"padding: 16px max(32px, calc((100% - 768px) / 2))", // the 768 column
		".has-sel #mem-list, .has-sel #ag-list { flex: none; width: 320px; }",
		".has-sel #mem-detail, .has-sel #ag-detail { display: flex; }",
		"@media (max-width: 1199px) {",
		".has-sel #mem-list, .has-sel #ag-list { display: none; }",
		".has-sel .pane-back { display: inline-flex; }",
		".mem-row, .ag-row {", "height: 40px;", // session-row look
		".ag-badge::before { content: ''; width: 6px; height: 6px;", // status dot
		".ag-badge.st-running { --dot: var(--seek-brand); }",
		".ag-badge.st-done    { --dot: var(--seek-ok); }",
		".ag-badge.st-error   { --dot: var(--seek-err); }",
		"function syncAgSel()", "function closeAgent()",
		"function setMemSel(v)", "function closeMem()",
		"if (b.classList.contains('pane-x')) return togglePane(b.dataset.pane);",
		"‹ 返回列表",
		// Trend chart: label line above the plot strip of its own band.
		"const BAND = 38, R = 4, T = 4, B = 12, LAB = 16;",
		"{ x: 0, y: T + BAND * ri + 11, class: 'tlab' }",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-7 asset-panel piece %q (c_w74tuo)", need)
		}
	}
	// All four panels wear the same head.
	if got := strings.Count(page, `<div class="pane-bar">`); got != 4 {
		t.Errorf("W6-7: want exactly 4 .pane-bar heads (memory/stats/agents/hooks), got %d", got)
	}
	// The retired middle-pane face of the panels.
	for _, gone := range []string{
		"（在左侧选一条记录",               // the half-screen placeholder
		"font-style: italic",      // italic hints are gone page-wide
		".pane-head",              // the old grey strip
		"#ag-list { width: 380px", // fixed list column
		"#mem-list { width: 320px",
		"const H = 96, L = 30", // trend: label left of the line start
		"{ x: 2, y: y0 + 9, class: 'tlab' }",
	} {
		if strings.Contains(page, gone) {
			t.Errorf("page still carries the retired panel piece %q (W6-7 replaces, not stacks)", gone)
		}
	}
	// Empty-state copy is a plain sentence, not a parenthetical aside.
	for _, banned := range []string{
		"（还没有", "（这个项目还没有", "（没有历史会话", "（过滤后", "（无正文）", "（只有 1 个会话",
		"s.description || '（无描述）'", // the agents row default (chat tool lines keep theirs)
	} {
		if strings.Contains(page, banned) {
			t.Errorf("panel empty state still wrapped in full-width parentheses: %q", banned)
		}
	}
}

// TestWeb_PageIconRailW68 pins the W6-8 shape: #shell is a two-column grid
// whose first track is the sidebar (280 expanded / 56 collapsed); the
// collapsed state settles into the icon rail (body.side-rail) only after the
// track transition, with the sidebar's children staying 280 wide meanwhile
// (no reflow); a viewport under 1024 collapses automatically without
// touching the stored preference; ≤480px keeps the rail while the full
// sidebar opens as a drawer (body.side-drawer) over the scrim.
func TestWeb_PageIconRailW68(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"grid-template-columns: 280px minmax(0, 1fr);",
		"body.side-collapsed #shell { grid-template-columns: 56px minmax(0, 1fr); }",
		"transition: grid-template-columns .3s cubic-bezier(.4,0,.2,1);",
		"#side > * { width: 280px; min-width: 280px; }", // no reflow while the track narrows
		".side-rail #side > * { width: 56px; min-width: 0; }",
		".side-rail #side-new { margin: 8px 8px 0; width: 40px; height: 40px;",
		".side-rail #side-nav button { width: 40px; height: 40px;",
		".side-rail :is(#side-new, #side-sec, #side-nav) .ic { width: 18px; height: 18px; }",
		".side-rail #side-head:is(:hover, :focus-within) #side-toggle .ic { display: inline-block; }", // brand doubles as the expand button
		"let sidePref = readSidePref();",
		"let sideCollapsed = window.innerWidth < 1024 ? true : sidePref === true;",
		"b.toggle('side-rail', sideCollapsed && sideSettled && !sideDrawer);",
		"b.toggle('side-drawer', sideDrawer);",
		"if (sideMQ && sideMQ.matches) return setDrawer(!sideDrawer);",
		"setSide(!sideCollapsed, window.innerWidth >= 1024);", // narrow-screen toggles are session-only
		"body.side-drawer #side { position: absolute; top: 0; bottom: 0; left: 0; z-index: 30;",
		"body.side-drawer #side-scrim { display: block; }",
		"#conv-proj-name { display: none; }",
		"#list, #hist { padding: 12px 16px; }",
		"form { width: calc(100% - 24px); margin: 4px 12px 8px; }",                       // composer side margins are real, not eaten by width:100%
		"#side-nav button { display: flex; align-items: center; gap: 8px; height: 32px;", // nav rows stay plain
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-8 icon-rail piece %q (c_eq3eak)", need)
		}
	}
	for _, gone := range []string{
		"#side { width: 280px; flex: none;", // the sidebar width no longer rides on its content
		"body.side-collapsed #side { width: 44px; }",
		"body:not(.side-collapsed) #side {", // the old "drawer == expanded state" breakpoint rules
		"width: min(300px, 84vw)",
		"transition: width .15s; }",
	} {
		if strings.Contains(page, gone) {
			t.Errorf("page still carries the retired sidebar piece %q (W6-8 replaces, not stacks)", gone)
		}
	}
}

// assertNoResidentGlobalKeydown pins the c_7lsngm discipline: the page keeps
// no resident document/window keydown listener, so Escape is never hijacked
// page-wide. The one sanctioned shape (W6-6 settings modal) arms a listener
// only while the modal is open, so a global keydown listener may exist only
// paired with its removal — an unpaired one is the page-wide hijack again.
func assertNoResidentGlobalKeydown(t *testing.T, page string) {
	t.Helper()
	for _, on := range []string{"document", "window"} {
		add := strings.Count(page, on+".addEventListener('keydown'")
		rm := strings.Count(page, on+".removeEventListener('keydown'")
		if add != rm {
			t.Errorf("global %s keydown listener must be paired with its removal (Esc must not be hijacked page-wide): add=%d remove=%d", on, add, rm)
		}
	}
}

// TestWeb_PageSettingsModalW66 pins the W6-6 shape: settings is a MODAL
// (role=dialog + aria-modal, scrim + centred box, three tabs), not the old
// middle-pane card stack. Pinned both sides: the modal pieces present, the
// retired .pane form absent, and the page free of dispatch-internal wording
// ("卡面口径" / "本卡配额") that only the card writer should ever read.
func TestWeb_PageSettingsModalW66(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		`id="settings" role="dialog" aria-modal="true"`, // modal semantics
		`id="set-scrim"`,     // the overlay
		`id="set-close"`,     // ✕ close affordance
		`id="set-tabs"`,      // left tab rail
		`data-tab="general"`, // 通用
		`data-tab="serve"`,   // 服务配置
		`data-tab="about"`,   // 关于
		"body.settings-view #settings { display: flex; }",
		"body.settings-view #set-scrim { display: block; }",
		"width: min(800px, calc(100vw - 32px))",
		"height: min(800px, calc(100vh - 100px))",
		"border-radius: 24px; box-shadow: var(--seek-shadow-pop)",
		"function openSettings(opener)",
		"function closeSettings(opts)",
		// Esc + focus trap hang off document but only while the modal is
		// open (armed in openSettings, disarmed in closeSettings): aria-modal
		// does not stop Tab, and a click on blank space inside the box drops
		// focus to <body> — a listener on the box itself never sees that
		// Escape (real-input probe, 2026-11-28).
		"document.addEventListener('keydown', modalKeys)",
		"document.removeEventListener('keydown', modalKeys)",
		"function modalKeys(ev)",
		"if (ev.key !== 'Tab') return;",
		"(ev.shiftKey ? f[f.length - 1] : f[0]).focus();",
		"setScrim.addEventListener('click'",   // overlay click closes
		"back.focus();",                       // focus returns to #set-btn
		"settings/get",                        // the read-only snapshot
		`setRow('路径', p, '', { mono: true })`, // candidate sub-rows: real label, no "·" orphan
		// 「跟随系统」 is 52px wide; the inherited 16px button padding left
		// exactly 52px of content box and wrapped it onto two lines.
		"padding: 0 2px; white-space: nowrap;",
		// The theme note speaks user language, not "记忆位".
		"note.textContent = storedTheme() ? '当前：' + now : '跟随系统 · 当前为' + now;",
		// A late settings/get must still reach an already-rendered empty hero.
		"list.querySelector('.chat-empty .hero')",
		"{ className: 'hero-ver', textContent: sideVer.textContent }",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the W6-6 settings-modal piece %q (c_ekuuof)", need)
		}
	}
	// The retired middle-pane face: no settings entry in the pane switcher,
	// no card stack, no right-aligned label/value pair split — and no Esc
	// listener scoped to the box (superseded by the document-level one).
	for _, gone := range []string{
		"settings: { btn: settingsBtn",
		`.set-card {`,
		"togglePane('settings')",
		`<section id="settings" class="pane">`,
		"settingsModal.addEventListener('keydown'",
		"'记忆位：' + stored",
	} {
		if strings.Contains(page, gone) {
			t.Errorf("page still carries the retired settings-pane piece %q (W6-6 replaces, not stacks)", gone)
		}
	}
	// Dispatch-internal wording must never reach users.
	for _, banned := range []string{"卡面口径", "本卡配额"} {
		if strings.Contains(page, banned) {
			t.Errorf("page exposes dispatch-internal wording %q — write the read-only note in user language", banned)
		}
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
	// keeps no resident document/window keydown listener.
	assertNoResidentGlobalKeydown(t, page)
}
