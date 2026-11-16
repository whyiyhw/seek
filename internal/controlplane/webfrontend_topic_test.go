package controlplane

// W3.2 topic tests (card c_3736yl acceptance; feature-web-frontend
// §3.2, §8): SSE topic `subagents` on /events — push e2e over a
// hand-made index (no history replay, folded rows, notified silent),
// conv/topic coexistence in both directions, shared-follow refcount,
// cross-PROJECT isolation (two convs, two projects, no crosstalk),
// the drop-oldest buffer, and the follow-disconnect leaktest (same
// caliber as TestWeb_SSEDisconnectUnsubscribes: hub unregisters, map
// entries leave with their last user, goroutines exit).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

// fastTopicPoll tightens the follow tick for a test and restores it
// on cleanup (tests in this package run sequentially).
func fastTopicPoll(t *testing.T) {
	t.Helper()
	old := subagentsTopicPoll
	subagentsTopicPoll = 10 * time.Millisecond
	t.Cleanup(func() { subagentsTopicPoll = old })
}

// seedTopicProject binds a conv to a fresh temp project (SEEK_HOME
// redirected so paths.SubagentsIndex resolves in-test) and seeds the
// project's subagents.jsonl with lines; returns the index path for
// the test's appends. One SEEK_HOME per TEST: /events re-resolves
// the index at subscribe time from the env, so a second conv in the
// same test must go through seedTopicConv (a second redirect here
// would resolve conv A's index under conv B's home).
func seedTopicProject(t *testing.T, fe *WebFrontend, convNative string, lines ...string) string {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	return seedTopicConv(t, fe, convNative, t.TempDir(), lines...)
}

// seedTopicConv is seedTopicProject's per-conv core for a test that
// seeds more than one project: same shape, but the project is given
// and SEEK_HOME stays whatever the first seedTopicProject call set.
func seedTopicConv(t *testing.T, fe *WebFrontend, convNative, project string, lines ...string) string {
	t.Helper()
	conv := NewConvID("web", convNative)
	if convNative == "default" {
		conv = webConvID()
	}
	if err := fe.store.Put(Binding{
		ConvID:      conv,
		ProjectPath: project,
		SessionID:   "sess-A",
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Put binding: %v", err)
	}
	index, err := paths.SubagentsIndex(project)
	if err != nil {
		t.Fatalf("SubagentsIndex: %v", err)
	}
	// The index parent always exists (appendIndexLines' O_CREATE does
	// not mkdir parents); the file itself only when there is history.
	if err := os.MkdirAll(filepath.Dir(index), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if len(lines) > 0 {
		if err := os.WriteFile(index, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write index: %v", err)
		}
	}
	return index
}

// appendIndexLines appends complete jsonl records (the writer side's
// shape: line + \n per append).
func appendIndexLines(t *testing.T, index string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(index, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

// sseMsg is one SSE frame as the tap sees it: Event from the optional
// `event:` line ("" for plain data-only frames), Data the payload.
type sseMsg struct {
	Event string
	Data  string
}

// openEventsRaw opens /events with an arbitrary query and taps
// (event, data) frames — covers both the conv dimension (data-only)
// and the topic dimension (event: subagents) on one channel.
func openEventsRaw(t *testing.T, srv *httptest.Server, f *WebFrontend, query, convNative string) (<-chan sseMsg, *http.Response) {
	t.Helper()
	id := NewConvID("web", convNative)
	if convNative == "default" {
		id = webConvID()
	}
	before := subCount(f, id)

	resp, err := http.Get(srv.URL + "/events" + query)
	if err != nil {
		t.Fatalf("open /events %s: %v", query, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("open /events %s: status %d", query, resp.StatusCode)
	}
	t.Cleanup(func() { resp.Body.Close() })
	waitSubs(t, f, id, before+1)

	tap := make(chan sseMsg, 64)
	go func() {
		defer close(tap)
		sc := bufio.NewScanner(resp.Body)
		ev := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				tap <- sseMsg{Event: ev, Data: strings.TrimPrefix(line, "data: ")}
				ev = ""
			}
		}
	}()
	return tap, resp
}

// topicMsg decodes the topic envelope ({"topic","event","sub"}); the
// sub row reuses the W3.1 fixture shape.
type topicMsg struct {
	Topic string          `json:"topic"`
	Event string          `json:"event"`
	Sub   subagentFixture `json:"sub"`
}

// wantTopicMsg waits for one subagents frame whose envelope matches
// event + sub_sid and returns the decoded envelope.
func wantTopicMsg(t *testing.T, tap <-chan sseMsg, event, sid string) topicMsg {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m, ok := <-tap:
			if !ok {
				t.Fatalf("stream closed before a %s push for %s arrived", event, sid)
			}
			if m.Event != topicSubagents {
				continue // a conv-dimension frame on the same connection
			}
			var msg topicMsg
			if err := json.Unmarshal([]byte(m.Data), &msg); err != nil {
				t.Fatalf("topic envelope not JSON: %q: %v", m.Data, err)
			}
			if msg.Topic != topicSubagents {
				t.Fatalf("envelope topic = %q, want %q (%s)", msg.Topic, topicSubagents, m.Data)
			}
			if msg.Event == event && msg.Sub.SubSid == sid {
				return msg
			}
		case <-deadline:
			t.Fatalf("no %s push for %s arrived within 2s", event, sid)
		}
	}
}

// wantFramesQuiet asserts no frame of any kind arrives for window.
func wantFramesQuiet(t *testing.T, tap <-chan sseMsg, window time.Duration) {
	t.Helper()
	select {
	case m, ok := <-tap:
		if ok {
			t.Fatalf("cross-talk: got frame %+v while the stream should be quiet", m)
		}
	case <-time.After(window):
	}
}

// topicSubs peeks a follow's state for an index path: subscriber
// count (which IS the refcount) and existence.
func topicSubs(f *WebFrontend, indexPath string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fl, ok := f.follows[indexPath]
	if !ok {
		return 0, false
	}
	return len(fl.subs), true
}

// topicFanoutCount totals subscribers across every follow — the whole
// topic hub; 0 when every topic client has disconnected.
func topicFanoutCount(f *WebFrontend) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, fl := range f.follows {
		n += len(fl.subs)
	}
	return n
}

const (
	topicStartedSync = `{"event":"started","sub_sid":"sync-1","ts":"2026-01-01T10:00:00Z","parent_sid":"sess-A","parent_turn":4,"type":"explore","description":"find handlers"}`
	topicDoneSync    = `{"event":"completed","sub_sid":"sync-1","ts":"2026-01-01T10:05:00Z","tokens":{"prompt":100,"completion":20,"cache_hit":80}}`
	topicStartedBG   = `{"event":"started","sub_sid":"bg-1","ts":"2026-01-02T09:00:00Z","parent_sid":"sess-A","type":"general-purpose","description":"scan deps","background":true}`
	topicDoneBG      = `{"event":"completed","sub_sid":"bg-1","ts":"2026-01-02T09:40:00Z","tokens":{"prompt":50,"completion":10,"cache_hit":0}}`
	topicNotifiedBG  = `{"event":"notified","sub_sid":"bg-1","ts":"2026-01-02T10:10:00Z","parent_turn":8}`
)

// TestWeb_TopicGate: the topic rides behind the same gates as every
// route — no token 401, unknown topic 400, and (like GET /subagents)
// a topic follow on an unbound conv is 400 with the /bind hint, not a
// silent empty stream. All BEFORE any event-stream headers.
func TestWeb_TopicGate(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	get := func(q string) *http.Response {
		t.Helper()
		resp, err := http.Get(srv.URL + "/events" + q)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := get("?topic=subagents")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", resp.StatusCode)
	}

	resp = get("?token=test-token-0123456789abcdef&topic=weather")
	body := make(map[string]string)
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown topic = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body["error"], "weather") {
		t.Errorf("unknown-topic error should name the topic: %v", body)
	}

	resp = get("?token=test-token-0123456789abcdef&topic=subagents")
	body = make(map[string]string)
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unbound conv = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body["error"], "/bind") {
		t.Errorf("unbound-conv error should hint /bind: %v", body)
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
		t.Errorf("gate failures must not start an event-stream, got %q", ct)
	}
}

// TestWeb_TopicSubagentsPush is the push e2e: history in the file at
// subscribe time never replays (snapshot comes from GET /subagents);
// appended started/completed lines push as `event: subagents` frames
// carrying the folded row; notified stays silent.
func TestWeb_TopicSubagentsPush(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fastTopicPoll(t)
	index := seedTopicProject(t, fe, "default", topicStartedSync, topicDoneSync)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	tap, resp := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&topic=subagents", "default")
	defer resp.Body.Close() // LIFO: before srv.Close — an open SSE conn blocks it
	// History must not replay — the anchor holds even with the fast
	// tick running during the window.
	wantFramesQuiet(t, tap, 200*time.Millisecond)

	appendIndexLines(t, index, topicStartedBG)
	msg := wantTopicMsg(t, tap, "started", "bg-1")
	if msg.Sub.Status != "active" || !msg.Sub.Background || msg.Sub.Description != "scan deps" {
		t.Errorf("started row = %+v, want active background scan-deps", msg.Sub)
	}

	appendIndexLines(t, index, topicDoneBG)
	msg = wantTopicMsg(t, tap, "completed", "bg-1")
	if msg.Sub.Status != "completed" || msg.Sub.Tokens == nil || msg.Sub.Tokens.Prompt != 50 {
		t.Errorf("completed row = %+v, want terminal with tokens", msg.Sub)
	}

	// Non-panel kind: notified annotates the fold but never pushes.
	appendIndexLines(t, index, topicNotifiedBG)
	wantFramesQuiet(t, tap, 200*time.Millisecond)
}

// TestWeb_TopicConvCoexistence: the two dimensions of ONE /events
// connection never leak into each other's semantics — a plain conv
// client sees no topic frames, a topic client still sees its conv's
// chat replies (data-only frames) alongside topic frames.
func TestWeb_TopicConvCoexistence(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fastTopicPoll(t)
	index := seedTopicProject(t, fe, "conv-a")
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	plainTap, plainResp := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&conv=conv-a", "conv-a")
	defer plainResp.Body.Close()
	topicTap, topicResp := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&conv=conv-a&topic=subagents", "conv-a")
	defer topicResp.Body.Close()

	appendIndexLines(t, index, topicStartedBG)
	wantTopicMsg(t, topicTap, "started", "bg-1")
	wantFramesQuiet(t, plainTap, 250*time.Millisecond) // no topic frames on the conv channel

	conv := NewConvID("web", "conv-a")
	if err := fe.Send(context.Background(), conv, Reply{Text: "for both"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for _, tapCh := range []<-chan sseMsg{plainTap, topicTap} {
		select {
		case m, ok := <-tapCh:
			if !ok || !strings.Contains(m.Data, "for both") || m.Event != "" {
				t.Fatalf("chat reply frame = %+v, want data-only frame with the reply", m)
			}
		case <-deadline:
			t.Fatal("chat reply did not reach the connection")
		}
	}
	// The chat reply must not have smuggled a topic event name along.
	wantFramesQuiet(t, topicTap, 150*time.Millisecond)
}

// TestWeb_TopicFollowSharedRefcount: two tabs share ONE follow of the
// same index (first subscriber anchors it); both receive the push;
// the follow outlives the first disconnect and leaves with the last.
func TestWeb_TopicFollowSharedRefcount(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fastTopicPoll(t)
	index := seedTopicProject(t, fe, "default", topicStartedSync)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	tap1, resp1 := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&topic=subagents", "default")
	defer resp1.Body.Close()
	tap2, resp2 := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&topic=subagents", "default")
	defer resp2.Body.Close()

	refs, ok := topicSubs(fe, index)
	if !ok || refs != 2 {
		t.Fatalf("follow state = (%d, %v), want one shared follow with 2 subscribers", refs, ok)
	}

	appendIndexLines(t, index, topicStartedBG)
	wantTopicMsg(t, tap1, "started", "bg-1")
	wantTopicMsg(t, tap2, "started", "bg-1")

	resp1.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if refs, ok := topicSubs(fe, index); ok && refs == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("follow did not drop to 1 subscriber after first disconnect (%d, %v)", refs, ok)
		}
		time.Sleep(5 * time.Millisecond)
	}

	resp2.Body.Close()
	for {
		if _, ok := topicSubs(fe, index); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("follow entry must leave with its last subscriber")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWeb_TopicFollowDisconnectLeak is the W3.2 acceptance item, the
// same caliber as TestWeb_SSEDisconnectUnsubscribes: the client hangs
// up → the hub unregisters both dimensions, the follow entry leaves,
// and the handler AND follower goroutines exit — then a resubscribe
// starts a fresh follow that pushes again.
func TestWeb_TopicFollowDisconnectLeak(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fastTopicPoll(t)
	index := seedTopicProject(t, fe, "leak-conv", topicStartedSync)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	conv := NewConvID("web", "leak-conv")
	_, resp := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&conv=leak-conv&topic=subagents", "leak-conv")
	resp.Body.Close() // the hang-up

	waitSubs(t, fe, conv, 0)
	deadline := time.Now().Add(2 * time.Second)
	for topicFanoutCount(fe) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("topic hub never emptied (has %d)", topicFanoutCount(fe))
		}
		time.Sleep(5 * time.Millisecond)
	}
	for {
		if _, ok := topicSubs(fe, index); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("follow entry survived the last disconnect")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if refs := topicFanoutCount(fe); refs != 0 {
		t.Errorf("topic subscribers = %d, want 0", refs)
	}

	// Goroutine exit: no handler or follower frame anywhere (the
	// unregister runs in the handler's defer, the follower on stop).
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		stacks := string(buf[:n])
		if !strings.Contains(stacks, "handleEvents") && !strings.Contains(stacks, "followLoop") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handler/follower goroutine still alive 2s after client disconnect:\n%s", stacks)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The stop→start cycle: a fresh subscriber gets a fresh follow and
	// pushes resume (the anchor is the new subscribe, so the already
	// appended history stays silent and only new lines push).
	tap2, resp2 := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&conv=leak-conv&topic=subagents", "leak-conv")
	defer resp2.Body.Close()
	if refs, ok := topicSubs(fe, index); !ok || refs != 1 {
		t.Fatalf("resubscribe follow = (%d, %v), want 1 subscriber", refs, ok)
	}
	wantFramesQuiet(t, tap2, 150*time.Millisecond) // anchored past sync-1
	appendIndexLines(t, index, topicStartedBG)
	wantTopicMsg(t, tap2, "started", "bg-1")
}

// TestWeb_PushTopicDropsOldest pins the "hub 满丢老" semantics on a
// small buffer (the §4.2 posture): a full topic channel evicts its
// OLDEST queued event to admit the push — freshest rows win, the
// follower never blocks.
func TestWeb_PushTopicDropsOldest(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	const bufCap = 8
	ch := make(chan string, bufCap)
	// Seed one follow directly (no server, no goroutine — the buffer
	// semantics are all this test exercises); pushFollow fans it out.
	fe.mu.Lock()
	fl := &topicFollow{stop: make(chan struct{}), subs: map[int]chan string{1: ch}}
	fe.follows = map[string]*topicFollow{"test-index": fl}
	fe.mu.Unlock()

	const total = bufCap + 4
	for i := 0; i < total; i++ {
		fe.pushFollow(fl, fmt.Sprintf("ev-%02d", i))
	}
	if got := len(ch); got != bufCap {
		t.Fatalf("buffer holds %d, want %d", got, bufCap)
	}
	for i := total - bufCap; i < total; i++ {
		want := fmt.Sprintf("ev-%02d", i)
		got := <-ch
		if got != want {
			t.Fatalf("buffer content = %q at position %d, want %q (oldest evicted)", got, i, want)
		}
	}
}

// TestWeb_TopicProjectIsolation is the rework round's target
// regression (card c_3736yl): two convs bound to DIFFERENT projects,
// both opted into ?topic=subagents — a line appended to project A's
// index pushes ONLY to A's subscriber. The original bug: the fanout
// was keyed by topic name alone, so A's follow pushed its rows into
// B's stream too (invisible to every earlier test — all single
// project).
func TestWeb_TopicProjectIsolation(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	fastTopicPoll(t)
	indexA := seedTopicProject(t, fe, "iso-a", topicStartedSync)
	indexB := seedTopicConv(t, fe, "iso-b", t.TempDir(), topicStartedSync)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	tapA, respA := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&conv=iso-a&topic=subagents", "iso-a")
	defer respA.Body.Close()
	tapB, respB := openEventsRaw(t, srv, fe, "?token=test-token-0123456789abcdef&conv=iso-b&topic=subagents", "iso-b")
	defer respB.Body.Close()

	// Two follows — one per project — of one subscriber each.
	if n, ok := topicSubs(fe, indexA); !ok || n != 1 {
		t.Fatalf("project A follow = (%d, %v), want 1 subscriber", n, ok)
	}
	if n, ok := topicSubs(fe, indexB); !ok || n != 1 {
		t.Fatalf("project B follow = (%d, %v), want 1 subscriber", n, ok)
	}

	// A appends: A's subscriber gets the row, B's stream stays quiet.
	appendIndexLines(t, indexA, topicStartedBG)
	wantTopicMsg(t, tapA, "started", "bg-1")
	wantFramesQuiet(t, tapB, 250*time.Millisecond)

	// The mirror: B's appends reach only B. Both lines go to B's index
	// (the fold skips a terminal event for a sub_sid with no preceding
	// started, so B needs its own started too).
	appendIndexLines(t, indexB, topicStartedBG, topicDoneBG)
	wantTopicMsg(t, tapB, "started", "bg-1")
	wantTopicMsg(t, tapB, "completed", "bg-1")
	wantFramesQuiet(t, tapA, 250*time.Millisecond)
}
