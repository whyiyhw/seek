package servecli

// TestServePlane_WebSmoke is the scripted web smoke (scripts/
// serve-smoke-test.sh) in Go form — card c_snerqc, reworked per the
// 2026-11-15 security ruling. The three HTTP contract assertions run
// against the REAL serve assembly: servePlane is the exact body
// `seek serve` dispatches to, the web frontend gets a real listener,
// and the M2 router answers the unbound prompt — HTTP → Inbound →
// router → Send → SSE, no fake anywhere in the chain.
//
// The listen address (web_addr) is injected test-side through
// WebConfig.Addr on a random loopback port. Production code carries
// no env/flag outlet for it: the earlier SEEK_WEB_ADDR seam was
// removed — a test seam must not add an unscoped listen-address
// escape to shipped code (external configurability belongs to the
// scope-checked channel from card c_x7ehom, loopback + tailnet only).
//
//	① GET  /       no token → 200 + the embedded HTML page
//	② POST /msg    no token → 401
//	③ SSE  /events subscribed with the token; a tokened POST /msg
//	                     round-trips the router: unbound conversation →
//	                     unboundHint back on SSE (executor never runs)
//	plus the drain contract: ctx cancel (the SIGTERM stand-in) →
//	servePlane returns nil → exit code 0.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/controlplane"
)

func TestServePlane_WebSmoke(t *testing.T) {
	dir := withHome(t)
	addr := freeLoopbackAddr(t)
	fe := controlplane.NewWeb(controlplane.WebConfig{
		Addr:   addr,
		Banner: io.Discard, // keep the token out of test logs
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr bytes.Buffer
	planeDone := make(chan error, 1)
	go func() { planeDone <- servePlane(ctx, []controlplane.Frontend{fe}, config.Config{}, &stderr) }()

	base := "http://" + addr
	// Liveness = the listener answering GET / (Banner is Discard, so
	// there is no startup line to poll).
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case err := <-planeDone: // already happened-before the read below
			t.Fatalf("plane died before the web listener came up: %v\nstderr: %s", err, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			// Drain first: a live plane still owns stderr, and reading a
			// bytes.Buffer concurrently is a data race, not a hint.
			cancel()
			<-planeDone
			t.Fatalf("web listener never came up within 10s\nstderr: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// ① GET / → 200 + HTML (the embedded page holds no secrets).
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("① GET / status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(string(body[:min(len(body), 64)])), "<!doctype html") {
		t.Errorf("① GET / body is not the HTML page (first 64 bytes: %q)", body[:min(len(body), 64)])
	}
	if t.Failed() {
		cancel()
		<-planeDone
		t.Fatal("断言① failed")
	}

	// ② POST /msg without a token → 401 (the gate precedes everything).
	postMsg := func(withToken bool, tok string) int {
		t.Helper()
		u := base + "/msg"
		if withToken {
			u += "?token=" + tok
		}
		r, err := http.Post(u, "application/json", strings.NewReader(`{"text":"冒烟 ping"}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if code := postMsg(false, ""); code != http.StatusUnauthorized {
		cancel()
		<-planeDone
		t.Fatalf("② POST /msg without token = %d, want 401", code)
	}

	// ③ SSE round trip: subscribe with the token (written to the
	// SEEK_HOME sandbox by ensureToken on first authenticated use),
	// inject one prompt, wait for the router's unboundHint on the wire.
	tokBytes, err := os.ReadFile(filepath.Join(dir, "serve", "web.token"))
	if err != nil {
		t.Fatalf("web.token must exist once the plane is up: %v", err)
	}
	tok := strings.TrimSpace(string(tokBytes))

	sseResp, err := http.Get(base + "/events?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events with valid token = %d, want 200", sseResp.StatusCode)
	}
	if ct := sseResp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("GET /events content-type = %q, want text/event-stream", ct)
	}
	events := make(chan string, 16)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(sseResp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				select {
				case events <- strings.TrimPrefix(line, "data: "):
				default: // never block the reader on a slow assertion loop
				}
			}
		}
	}()

	// The retry line is written BEFORE the subscriber registers, and the
	// MVP Send drops events for unregistered subscribers — half a second
	// covers that microsecond window (the prior shell smoke used the
	// same budget; re-POST below is the safety net, not the plan).
	time.Sleep(500 * time.Millisecond)
	deadline = time.Now().Add(10 * time.Second)
	sawReply := false
	for !sawReply {
		if code := postMsg(true, tok); code != http.StatusAccepted {
			t.Fatalf("③ POST /msg with token = %d, want 202", code)
		}
		select {
		case ev := <-events:
			if strings.Contains(ev, `"conv":"web:default"`) && strings.Contains(ev, "还没绑定") {
				sawReply = true // the router's unboundHint, round-tripped
			}
		case <-time.After(500 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatalf("③ SSE never carried the router's unboundHint within 10s; events seen: %v", drainEvents(events))
			}
		}
	}

	// Drain contract: drop the subscriber (closing the tab), cancel ctx
	// (the SIGTERM stand-in) → the plane drains and returns nil → the
	// exit-code handoff would be 0.
	sseResp.Body.Close()
	cancel()
	select {
	case err := <-planeDone:
		if err != nil {
			t.Errorf("plane must drain clean (exit 0 contract), got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("servePlane did not return after ctx cancel (drain hung)")
	}
	out := stderr.String() // read only after planeDone, see above
	if !strings.Contains(out, "plane up: frontends=[web]") {
		t.Errorf("startup log missing the web frontend line: %s", out)
	}
	if !strings.Contains(out, "drained cleanly") {
		t.Errorf("clean-shutdown log missing: %s", out)
	}
}

// drainEvents collects whatever is buffered, for failure diagnostics.
func drainEvents(events <-chan string) []string {
	var got []string
	for {
		select {
		case ev := <-events:
			got = append(got, ev)
		default:
			return got
		}
	}
}

// freeLoopbackAddr grabs an OS-assigned loopback port and gives it
// back: the test-side web_addr injection seam (WebConfig.Addr). The
// listen-close-reuse race is accepted — test-only, and a lost race
// fails loudly as a plane startup error, never as a false green.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
}
