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
	if info.Mode().Perm() != 0o600 {
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
