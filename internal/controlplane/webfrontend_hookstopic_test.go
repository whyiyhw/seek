package controlplane

// W5 c_xz2bey hooks-topic tests (card acceptance ①/②'s server half):
// push e2e over a hand-seeded audit log (verbatim lines, no history
// replay, corrupt lines swallowed), the EMPTY stream (no audit file at
// all — connection stays up and silent, later creation pushes), the
// unbound-conv leg (hooks is global — no /bind prerequisite, unlike
// subagents), conv/topic coexistence, shared global follow refcount +
// the disconnect leaktest (same caliber as TestWeb_TopicFollowDisconnectLeak),
// and heartbeat/retry head integrity — the tailscale --tcp SSE posture
// (PRD §12 open question) must not regress: the frame preamble a proxy
// or forwarded connection relies on stays `retry: 3000` + per-event
// flush, and the topic stream never disturbs the 15s `: ping` beat.

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// fastHooksPoll tightens the hooks follow tick for a test and restores
// it on cleanup (tests in this package run sequentially).
func fastHooksPoll(t *testing.T) {
	t.Helper()
	old := hooksTopicPoll
	hooksTopicPoll = 10 * time.Millisecond
	t.Cleanup(func() { hooksTopicPoll = old })
}

// hooksAuditPath redirects SEEK_HOME so paths.HooksAuditLog resolves
// in-test, seeds the global audit log with lines (none by default) and
// returns its path for the test's appends.
func hooksAuditPath(t *testing.T, lines ...string) string {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	path, err := paths.HooksAuditLog()
	if err != nil {
		t.Fatalf("HooksAuditLog: %v", err)
	}
	if len(lines) > 0 {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write audit log: %v", err)
		}
	}
	return path
}

const (
	hooksAuditA = `{"ts":"2026-11-20T09:00:00Z","event":"post_tool_use","hook":"fmt","tool":"bash","session_id":"s1","duration_ms":12,"exit_code":0}`
	hooksAuditB = `{"ts":"2026-11-20T09:00:05Z","event":"pre_tool_use","hook":"lint","tool":"edit","session_id":"s1","duration_ms":3,"exit_code":1,"denied":true,"reason":"blocked"}`
	hooksAuditC = `{"ts":"2026-11-20T09:00:09Z","event":"pre_prompt_inject","hook":"ctx","session_id":"s2","duration_ms":1,"exit_code":0,"injected_bytes":99}`
)

// wantHooksFrame waits for one hooks frame whose data contains hook
// and returns the raw data line.
func wantHooksFrame(t *testing.T, tap <-chan sseMsg, hook string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m, ok := <-tap:
			if !ok {
				t.Fatalf("stream closed before a %s frame arrived", hook)
			}
			if m.Event != topicHooks {
				continue // a conv-dimension frame on the same connection
			}
			if !strings.Contains(m.Data, `"hook":"`+hook+`"`) {
				continue
			}
			return m.Data
		case <-deadline:
			t.Fatalf("no hooks frame for %s arrived within 2s", hook)
		}
	}
}

// TestWeb_TopicHooksPush is the push e2e: history in the log at
// subscribe time never replays (snapshot comes from hooks/audit/tail);
// appended entries push as `event: hooks` frames carrying the audit
// line VERBATIM; a corrupt appended line is swallowed by the follower.
// Unbound conv is fine — the audit log is global, no /bind needed.
func TestWeb_TopicHooksPush(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	fastHooksPoll(t)
	audit := hooksAuditPath(t, hooksAuditA)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// Deliberately no binding on this conv: the hooks topic must not
	// require one (the subagents topic 400s there; its binding is what
	// picks the index file).
	tap, resp := openEventsRaw(t, srv, fe, "?token="+tok+"&topic=hooks", "default")
	defer resp.Body.Close()
	wantFramesQuiet(t, tap, 200*time.Millisecond) // history never replays

	appendIndexLines(t, audit, hooksAuditB)
	data := wantHooksFrame(t, tap, "lint")
	if data != hooksAuditB {
		t.Fatalf("frame data = %q, want the audit line verbatim %q", data, hooksAuditB)
	}

	// A corrupt complete line: consumed by the follower, never pushed.
	appendIndexLines(t, audit, `{"ts": not json`)
	wantFramesQuiet(t, tap, 150*time.Millisecond)

	appendIndexLines(t, audit, hooksAuditC)
	if data := wantHooksFrame(t, tap, "ctx"); data != hooksAuditC {
		t.Fatalf("frame data = %q, want %q verbatim", data, hooksAuditC)
	}
}

// TestWeb_TopicHooksEmptyStream is acceptance ①'s empty-stream case:
// no audit file at all — subscribe succeeds, the stream carries
// nothing (the 15s heartbeat would be the only traffic; this window
// sits far inside it), and the log's later CREATION (empty file, then
// appended lines) pushes — anchor 0 on a missing file.
func TestWeb_TopicHooksEmptyStream(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	fastHooksPoll(t)
	audit := hooksAuditPath(t) // SEEK_HOME redirected, no file seeded
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	tap, resp := openEventsRaw(t, srv, fe, "?token="+tok+"&topic=hooks", "default")
	defer resp.Body.Close()
	wantFramesQuiet(t, tap, 250*time.Millisecond)

	// Creation as the writer does it (O_CREATE then appends) — every
	// line in the new file is post-anchor traffic.
	appendIndexLines(t, audit, hooksAuditA)
	if data := wantHooksFrame(t, tap, "fmt"); data != hooksAuditA {
		t.Fatalf("frame data = %q, want %q verbatim", data, hooksAuditA)
	}
}

// TestWeb_TopicHooksHeadIntegrity pins acceptance ④'s non-regression
// half the server owns: the topic stream opens with the exact SSE
// preamble every existing consumer (and the tailscale --tcp forward)
// sees — `retry: 3000` first, then per-event writes — and headers
// carry the no-buffering posture. The heartbeat itself stays 15s (too
// slow to wait for here; its code path is shared and untouched).
func TestWeb_TopicHooksHeadIntegrity(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	fastHooksPoll(t)
	audit := hooksAuditPath(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events?token=" + tok + "&topic=hooks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	if ab := resp.Header.Get("X-Accel-Buffering"); ab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", ab)
	}
	// First bytes on the wire must be the retry hint — read one line
	// without draining the stream (the follow goroutine keeps running
	// until the body closes).
	br := bufio.NewReader(resp.Body)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "retry: 3000\n" {
		t.Fatalf("first wire line = %q, want retry: 3000", line)
	}
	resp.Body.Close() // hang up; leaktest below owns the rest
	waitHooksHubEmpty(t, fe, audit)
}

// TestWeb_TopicHooksCoexistence: a hooks topic connection still sees
// its conv's chat replies (data-only frames) alongside hooks frames —
// the two dimensions of one connection stay separate.
func TestWeb_TopicHooksCoexistence(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	fastHooksPoll(t)
	audit := hooksAuditPath(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	tap, resp := openEventsRaw(t, srv, fe, "?token="+tok+"&conv=hk-conv&topic=hooks", "hk-conv")
	defer resp.Body.Close()

	appendIndexLines(t, audit, hooksAuditA)
	wantHooksFrame(t, tap, "fmt")

	conv := NewConvID("web", "hk-conv")
	if err := fe.Send(context.Background(), conv, Reply{Text: "chat still flows"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	select {
	case m, ok := <-tap:
		if !ok || m.Event != "" || !strings.Contains(m.Data, "chat still flows") {
			t.Fatalf("chat reply frame = %+v, want data-only frame with the reply", m)
		}
	case <-deadline:
		t.Fatal("chat reply did not reach the topic connection")
	}
	wantFramesQuiet(t, tap, 150*time.Millisecond)
}

// TestWeb_TopicHooksSharedRefcountAndLeak is acceptance ②'s server
// half + the inspect-rpc #6 leaktest caliber: two tabs share ONE
// global follow; the follow outlives the first disconnect and leaves
// with the last; every handler and follower goroutine exits after the
// hang-up (nothing stranded, < 100ms class — polled at 5ms).
func TestWeb_TopicHooksSharedRefcountAndLeak(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	fastHooksPoll(t)
	audit := hooksAuditPath(t, hooksAuditA)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	tap1, resp1 := openEventsRaw(t, srv, fe, "?token="+tok+"&topic=hooks", "default")
	defer resp1.Body.Close()
	tap2, resp2 := openEventsRaw(t, srv, fe, "?token="+tok+"&conv=hk-2&topic=hooks", "hk-2")
	defer resp2.Body.Close()

	if refs, ok := topicSubs(fe, audit); !ok || refs != 2 {
		t.Fatalf("global follow = (%d, %v), want one shared follow with 2 subscribers", refs, ok)
	}

	// Both tabs get the same push — global firehose, every subscriber
	// wants the same bytes.
	appendIndexLines(t, audit, hooksAuditB)
	wantHooksFrame(t, tap1, "lint")
	wantHooksFrame(t, tap2, "lint")

	// First disconnect: follow survives with its second subscriber.
	resp1.Body.Close()
	if _, ok := topicSubs(fe, audit); !ok {
		t.Fatal("follow must survive its first disconnect")
	}
	// And it still pushes to the remaining tab.
	appendIndexLines(t, audit, hooksAuditC)
	wantHooksFrame(t, tap2, "ctx")

	// Last disconnect: follow leaves, hub empties, goroutines exit.
	resp2.Body.Close()
	waitHooksHubEmpty(t, fe, audit)

	// Stop→start cycle: a fresh subscriber gets a fresh follow.
	tap3, resp3 := openEventsRaw(t, srv, fe, "?token="+tok+"&topic=hooks", "default")
	defer resp3.Body.Close()
	if refs, ok := topicSubs(fe, audit); !ok || refs != 1 {
		t.Fatalf("resubscribe follow = (%d, %v), want 1 subscriber", refs, ok)
	}
	wantFramesQuiet(t, tap3, 150*time.Millisecond) // anchored past the seeded lines
	appendIndexLines(t, audit, hooksAuditB)
	wantHooksFrame(t, tap3, "lint")
}

// waitHooksHubEmpty blocks until the audit follow is gone and no
// handler/hooksFollowLoop goroutine remains — the leaktest caliber of
// TestWeb_TopicFollowDisconnectLeak ("followLoop" also matches
// "hooksFollowLoop", so the shared stack scan covers both loops).
func waitHooksHubEmpty(t *testing.T, f *WebFrontend, audit string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := topicSubs(f, audit); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("audit follow survived the last disconnect")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := topicFanoutCount(f); n != 0 {
		t.Fatalf("topic hub not empty (%d subscribers)", n)
	}
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		stacks := string(buf[:n])
		if !strings.Contains(stacks, "handleEvents") && !strings.Contains(stacks, "followLoop") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handler/follower goroutine still alive 2s after disconnect:\n%s", stacks)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
