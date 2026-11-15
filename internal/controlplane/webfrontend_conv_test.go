package controlplane

// W1.3 per-conv tests (card c_nl4nqg acceptance; feature-web-frontend
// §2.3 D-W3, §8): conv resolution (header / ?conv= / legacy default),
// two-conversation isolation, multi-tab fanout within one conv, SSE
// client-disconnect leak hygiene, unauthenticated /events 401, /state
// per conv, and the serve-restart survival of a legacy web:default
// binding. The c_qucxtu baseline family in webfrontend_test.go stays
// untouched and green alongside.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// subCount reads the hub's subscriber count for conv (same-package
// privilege; the poll loop below turns it into a readiness signal).
func subCount(f *WebFrontend, conv ConvID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs[conv])
}

// waitSubs polls until the hub holds want subscribers for conv — Send
// before registration is a documented drop, so tests synchronize on
// the hub state itself.
func waitSubs(t *testing.T, f *WebFrontend, conv ConvID, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for subCount(f, conv) != want {
		if time.Now().After(deadline) {
			t.Fatalf("hub never reached %d subscribers for %s (has %d)", want, conv, subCount(f, conv))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// openSSE connects one /events client for conv and waits until the hub
// registered it. Returns the response body and a tap of data-payloads.
func openSSE(t *testing.T, srv *httptest.Server, f *WebFrontend, conv string, header bool) (<-chan string, *http.Response) {
	t.Helper()
	// Baseline count BEFORE the request: reading it after Do() races
	// the handler's subscribe (register may land first, inflating the
	// want to an unreachable +2).
	id := NewConvID("web", conv)
	before := subCount(f, id)

	url := srv.URL + "/events?token=test-token-0123456789abcdef"
	var req *http.Request
	if header {
		req, _ = http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set(convHeader, conv)
	} else {
		req, _ = http.NewRequest(http.MethodGet, url+"&conv="+conv, nil)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open /events conv=%s: %v", conv, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("open /events conv=%s: status %d", conv, resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	waitSubs(t, f, id, before+1)

	tap := make(chan string, 16)
	go func() {
		defer close(tap)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				tap <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return tap, resp
}

// wantEvent asserts one payload containing substr arrives in time.
func wantEvent(t *testing.T, tap <-chan string, substr string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-tap:
			if !ok {
				t.Fatalf("stream closed before an event containing %q arrived", substr)
			}
			if strings.Contains(ev, substr) {
				return
			}
			// some other event of the same conv: keep waiting
		case <-deadline:
			t.Fatalf("no event containing %q arrived within 2s", substr)
		}
	}
}

// wantQuiet asserts nothing arrives on tap for the whole window — the
// isolation half of the acceptance (B must NOT see A's replies).
func wantQuiet(t *testing.T, tap <-chan string, window time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-tap:
		if ok {
			t.Fatalf("cross-talk: got %q while the conv should be isolated", ev)
		}
	case <-time.After(window):
	}
}

// TestWeb_ConvFromRequest pins the D-W3 resolution order and charset:
// header first, ?conv= fallback (EventSource), neither → legacy
// web:default, garbage → error (never a silent reroute — that would
// cross-wire conversations, the exact bug W1.3 exists to prevent).
func TestWeb_ConvFromRequest(t *testing.T) {
	f, _, _ := newTestWeb(t)
	mk := func(header, param string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/msg?conv="+param, nil)
		if header != "" {
			req.Header.Set(convHeader, header)
		}
		return req
	}
	for _, tc := range []struct {
		name    string
		header  string
		param   string
		want    ConvID
		wantErr bool
	}{
		{"header", "11111111-2222-3333-4444-555555555555", "", "web:11111111-2222-3333-4444-555555555555", false},
		{"param", "", "conv-b", "web:conv-b", false},
		{"header wins over param", "conv-a", "conv-b", "web:conv-a", false},
		{"neither → legacy default", "", "", webConvID(), false},
		{"full charset", "a.B_c-9", "", "web:a.B_c-9", false},
		{"space", "conv a", "", "", true},
		{"whitespace only", "   ", "", "", true},
		{"colon injection", "a:b", "", "", true},
		{"too long", strings.Repeat("a", convNativeMax+1), "", "", true},
		{"unicode", "会话一", "", "", true},
	} {
		conv, err := f.convFromRequest(mk(tc.header, tc.param))
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: convFromRequest = %q, want error", tc.name, conv)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: convFromRequest: %v", tc.name, err)
			continue
		}
		if conv != tc.want {
			t.Errorf("%s: conv = %q, want %q", tc.name, conv, tc.want)
		}
	}
}

// TestWeb_MsgConvAddressing: POST /msg carries the conv into the
// Inbound (header), falls back to ?conv=, and header-less requests
// address the legacy web:default — the compat item of D-W3.
func TestWeb_MsgConvAddressing(t *testing.T) {
	f, in, _ := newTestWeb(t)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	post := func(header, param string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/msg?token=test-token-0123456789abcdef&conv="+param,
			strings.NewReader(`{"text":"hi"}`))
		if header != "" {
			req.Header.Set(convHeader, header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, tc := range []struct {
		name   string
		header string
		param  string
		want   ConvID
	}{
		{"header", "conv-a", "", "web:conv-a"},
		{"param", "", "conv-b", "web:conv-b"},
		{"legacy default", "", "", webConvID()},
	} {
		if code := post(tc.header, tc.param); code != http.StatusAccepted {
			t.Fatalf("%s: POST /msg = %d, want 202", tc.name, code)
		}
		select {
		case m := <-in:
			if m.Conv != tc.want {
				t.Errorf("%s: inbound conv = %q, want %q", tc.name, m.Conv, tc.want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no Inbound arrived", tc.name)
		}
	}
	// A present-but-invalid conv is a 400, not a silent default-conv
	// reroute: two clients must never be cross-wired by bad input.
	if code := post("not a conv", ""); code != http.StatusBadRequest {
		t.Errorf("POST /msg invalid conv = %d, want 400", code)
	}
	select {
	case m := <-in:
		t.Errorf("invalid conv must not produce an Inbound, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestWeb_ConvIsolation is the W1.3 acceptance core: two conversations
// subscribed simultaneously — A's replies never reach B and vice versa
// (both the ?conv= and header subscription paths are exercised).
func TestWeb_ConvIsolation(t *testing.T) {
	f, _, _ := newTestWeb(t)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	tapA, respA := openSSE(t, srv, f, "conv-a", false)
	defer respA.Body.Close()
	tapB, respB := openSSE(t, srv, f, "conv-b", true) // header path
	defer respB.Body.Close()

	if err := f.Send(context.Background(), NewConvID("web", "conv-a"), Reply{Text: "for A"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Send(context.Background(), NewConvID("web", "conv-b"), Reply{Text: "for B"}); err != nil {
		t.Fatal(err)
	}
	wantEvent(t, tapA, `"conv":"web:conv-a","text":"for A"`)
	wantEvent(t, tapB, `"conv":"web:conv-b","text":"for B"`)
	// The isolation half: after each side got its own reply, nothing
	// may leak across — A stays quiet for B's event and B for A's.
	wantQuiet(t, tapA, 300*time.Millisecond)
	wantQuiet(t, tapB, 300*time.Millisecond)

	// A conv with no subscribers is still a silent no-op (port
	// contract; also the pre-existing drop posture).
	if err := f.Send(context.Background(), NewConvID("web", "nobody"), Reply{Text: "void"}); err != nil {
		t.Errorf("Send to unsubscribed conv must be a no-op success, got %v", err)
	}
	wantQuiet(t, tapA, 200*time.Millisecond)
	wantQuiet(t, tapB, 200*time.Millisecond)
}

// TestWeb_ConvMultiTabFanout: every tab of ONE conversation sees the
// same stream in the same order — the multi-tab property the MVP had
// globally, now scoped to a conv (§3.1 item 4).
func TestWeb_ConvMultiTabFanout(t *testing.T) {
	f, _, _ := newTestWeb(t)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	tap1, resp1 := openSSE(t, srv, f, "conv-a", false)
	defer resp1.Body.Close()
	tap2, resp2 := openSSE(t, srv, f, "conv-a", true)
	defer resp2.Body.Close()

	conv := NewConvID("web", "conv-a")
	for _, text := range []string{"first", "second"} {
		if err := f.Send(context.Background(), conv, Reply{Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	// Both tabs, both events, same order (chan sends under the hub
	// lock preserve executor order; per-conn writes are serial).
	wantEvent(t, tap1, `"text":"first"`)
	wantEvent(t, tap1, `"text":"second"`)
	wantEvent(t, tap2, `"text":"first"`)
	wantEvent(t, tap2, `"text":"second"`)
	wantQuiet(t, tap1, 200*time.Millisecond)
	wantQuiet(t, tap2, 200*time.Millisecond)
}

// TestWeb_EventsUnauth401 pins the unauthenticated SSE gate (acceptance
// "未鉴权 SSE 401 零回归"): no token → 401 JSON, never an event-stream.
func TestWeb_EventsUnauth401(t *testing.T) {
	f, _, _ := newTestWeb(t)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	for _, url := range []string{
		srv.URL + "/events",
		srv.URL + "/events?conv=conv-a",
		srv.URL + "/events?token=wrong",
	} {
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", url, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
			t.Errorf("GET %s must not start an event-stream without a valid token", url)
		}
	}
	// An invalid conv is judged only BEHIND the gate: no-token + bad
	// conv stays 401 (auth first, same order as every route).
	resp, err := http.Get(srv.URL + "/events?conv=not+a+conv")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-token bad conv = %d, want 401 (auth precedes conv validation)", resp.StatusCode)
	}
}

// TestWeb_SSEDisconnectUnsubscribes is the leaktest acceptance item:
// the client hangs up → the hub unregisters the subscriber and the
// handler goroutine exits (no stack frame left in handleEvents), and
// the conv's map entry leaves with its last subscriber.
func TestWeb_SSEDisconnectUnsubscribes(t *testing.T) {
	f, _, _ := newTestWeb(t)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	conv := NewConvID("web", "leak-conv")
	_, resp := openSSE(t, srv, f, "leak-conv", false)
	resp.Body.Close() // the hang-up

	waitSubs(t, f, conv, 0)
	f.mu.Lock()
	_, convEntryLeft := f.subs[conv]
	f.mu.Unlock()
	if convEntryLeft {
		t.Error("conv map entry must leave with its last subscriber")
	}

	// Goroutine exit: poll the all-goroutine stack until no frame sits
	// in handleEvents (unregister runs in its defer, return follows).
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), "handleEvents") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("handler goroutine still alive 2s after client disconnect:\n%s", string(buf[:n]))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWeb_StatePerConv: /state answers the REQUESTING conv's binding —
// two convs, two independent bindings, no leakage either way.
func TestWeb_StatePerConv(t *testing.T) {
	f, _, _ := newTestWeb(t)
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	if err := f.store.Put(Binding{ConvID: NewConvID("web", "conv-b"), ProjectPath: "/tmp/b"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Put(Binding{ConvID: webConvID(), ProjectPath: "/tmp/default"}); err != nil {
		t.Fatal(err)
	}

	get := func(conv string) map[string]any {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/state?token=test-token-0123456789abcdef", nil)
		if conv != "" {
			req.Header.Set(convHeader, conv)
		}
		resp, err := http.DefaultClient.Do(req)
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
	if m := get("conv-a"); m["bound"] != false {
		t.Errorf("conv-a must be unbound, got %v", m)
	}
	if m := get("conv-b"); m["bound"] != true || m["project"] != "/tmp/b" {
		t.Errorf("conv-b state = %v, want bound /tmp/b", m)
	}
	if m := get(""); m["bound"] != true || m["project"] != "/tmp/default" {
		t.Errorf("header-less /state = %v, want the legacy default binding /tmp/default", m)
	}

	// Invalid conv → 400 on /state too.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/state?token=test-token-0123456789abcdef", nil)
	req.Header.Set(convHeader, "bad conv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("/state invalid conv = %d, want 400", resp.StatusCode)
	}
}

// TestWeb_DefaultBindingSurvivesRestart is the compat acceptance item:
// a web:default binding written by a pre-W1.3 serve is visible and
// continuable after "restart" — a fresh frontend over the same serve
// dir. Header-less requests still address it; nothing is migrated.
func TestWeb_DefaultBindingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	tokenPath := dir + "/web.token"
	if err := os.WriteFile(tokenPath, []byte("test-token-0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	storePath := dir + "/bindings.json"

	// The "old serve": a frontend whose router Put a legacy binding
	// with a live session id (as if a pre-W1.3 conversation ran).
	old := NewWeb(WebConfig{TokenPath: tokenPath, StorePath: storePath})
	if err := old.store.Put(Binding{
		ConvID:      webConvID(),
		ProjectPath: "/tmp/legacy",
		SessionID:   "sess-legacy-1234",
	}); err != nil {
		t.Fatal(err)
	}

	// The "restart": a brand-new frontend process over the same dir.
	restarted := NewWeb(WebConfig{TokenPath: tokenPath, StorePath: storePath})
	in := make(chan Inbound, 1)
	restarted.inbox = in
	srv := httptest.NewServer(restarted.handler())
	defer srv.Close()

	// Visible: header-less /state shows the legacy binding + session.
	resp, err := http.Get(srv.URL + "/state?token=test-token-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if m["bound"] != true || m["project"] != "/tmp/legacy" || m["session"] != "sess-legacy-1234" {
		t.Fatalf("restarted /state = %v, want legacy binding /tmp/legacy + session", m)
	}

	// Continuable: a header-less POST still lands on web:default, the
	// conv the router resolves through that binding's session.
	postResp, err := http.Post(srv.URL+"/msg?token=test-token-0123456789abcdef",
		"application/json", strings.NewReader(`{"text":"continue"}`))
	if err != nil {
		t.Fatal(err)
	}
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /msg after restart = %d, want 202", postResp.StatusCode)
	}
	select {
	case msg := <-in:
		if msg.Conv != webConvID() {
			t.Errorf("post-restart inbound conv = %q, want web:default", msg.Conv)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("post-restart POST never reached the plane channel")
	}
}
