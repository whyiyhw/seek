package controlplane

// W1.4 streaming tests (card c_qcmul4 acceptance; feature-web-frontend
// §2.4/§2.5, §8 streaming entries): the full wire through REAL
// components — WebFrontend (httptest) + Router + Executor over the
// re-exec stub seek (web_stream / web_hang modes, executor_test.go) —
// zero real API keys. Pins:
//
//   - the §2.5 family in strict order with ONE run id per run:
//     delta → delta → tool(start) → tool(end,ok) → usage → reply
//     close-out carrying run + session_id; a second run mints a fresh
//     id; command replies never carry a run;
//   - live delivery: the stub writes its two deltas 150ms apart — a
//     dump-at-exit stream would deliver them back-to-back;
//   - server-side pricing: usage.cost_usd matches internal/pricing
//     (tier table in TestWeb_TurnCostUSDTiers, hand-checked card here);
//   - /cancel: the stream closes with the 已中断 replies instead of
//     hanging, no delta after the kill, exactly one closing reply
//     carries the live run id;
//   - the §10 proxy posture: X-Accel-Buffering: no on /events.
//
// The c_qucxtu baseline family (webfrontend_test.go) and the W1.3
// family (webfrontend_conv_test.go) stay green alongside — their
// default-event reply frames are byte-identical by construction (no
// live run on their convs).

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// newStreamE2E wires the W1.4 path end to end: the web frontend's
// handler via httptest, a real Router (store at the frontend's own
// StorePath, same handle contract as servecli) and the stub-seek
// Executor, with a per-message goroutine pump mirroring Serve's
// dispatch shape — /cancel MUST run while the prompt's Handle is still
// blocked inside the executor, so the pump may not serialise.
func newStreamE2E(t *testing.T, stubMode string) (*WebFrontend, *httptest.Server, string) {
	t.Helper()
	fe, in, tok := newTestWeb(t)
	ex, _ := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_MODE", stubMode)
	r := NewRouter([]Frontend{fe}, OpenBindingStoreAt(fe.cfg.StorePath), ex, RouterConfig{})
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

// wireFrame is one SSE frame as parsed off the wire: the event NAME
// ("" = default/message event — the MVP reply shape) and the data JSON.
type wireFrame struct {
	name string
	data string
}

// openSSEFrames connects one /events client on the default conv and
// parses frames (event: line then data: line; retry/heartbeat comment
// lines ignored). The tap preserves wire order.
func openSSEFrames(t *testing.T, srv *httptest.Server, tok string) (<-chan wireFrame, *http.Response) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/events?token=" + tok)
	if err != nil {
		t.Fatalf("open /events: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("open /events: status %d", resp.StatusCode)
	}
	tap := make(chan wireFrame, 64)
	go func() {
		defer close(tap)
		sc := bufio.NewScanner(resp.Body)
		name := ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				tap <- wireFrame{name: name, data: strings.TrimPrefix(line, "data: ")}
				name = ""
			}
		}
	}()
	return tap, resp
}

// wantFrame asserts the NEXT frame on the tap is exactly name (strict
// order — an out-of-order family member is the failure the §10 table
// worries about) and its data contains every substr. Returns the frame.
func wantFrame(t *testing.T, tap <-chan wireFrame, name string, substrs ...string) wireFrame {
	t.Helper()
	select {
	case fr, ok := <-tap:
		if !ok {
			t.Fatalf("stream closed while waiting for %q frame", name)
		}
		if fr.name != name {
			t.Fatalf("frame order: got %q %s, want event %q", fr.name, fr.data, name)
		}
		for _, s := range substrs {
			if !strings.Contains(fr.data, s) {
				t.Fatalf("%q frame data %s lacks %q", name, fr.data, s)
			}
		}
		return fr
	case <-time.After(5 * time.Second):
		t.Fatalf("no %q frame arrived within 5s", name)
	}
	return wireFrame{} // unreachable
}

// postMsg pushes one message through POST /msg and asserts 202.
func postMsg(t *testing.T, srv *httptest.Server, tok, text string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/msg?token="+tok,
		"application/json", strings.NewReader(`{"text":`+mustMarshalText(text)+`}`))
	if err != nil {
		t.Fatalf("POST /msg %q: %v", text, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /msg %q: status %d, want 202", text, resp.StatusCode)
	}
}

// mustMarshalText JSON-encodes s as a string value.
func mustMarshalText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestWeb_StreamE2EEventFamily: the §8 streaming entry — stub seek
// batches lines with wall-clock gaps; SSE delivers the delta/tool/
// usage family in order, live, under one server-minted run id, and the
// Send close-out reply replaces the bubble with the authoritative text
// + session_id.
func TestWeb_StreamE2EEventFamily(t *testing.T) {
	fe, srv, tok := newStreamE2E(t, "web_stream")
	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want %q (§10: proxies must not batch the stream)", got, "no")
	}
	waitSubs(t, fe, webConvID(), 1)

	// Command replies keep the MVP envelope verbatim: no run key ever
	// (commands do not exec), and a pre-W1.4 two-field decode works.
	postMsg(t, srv, tok, "/bind "+t.TempDir())
	ack := wantFrame(t, tap, "", `"conv":"web:default"`, `"text":"已绑定项目`)
	var legacy struct {
		Conv string `json:"conv"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(ack.data), &legacy); err != nil || legacy.Conv != "web:default" {
		t.Fatalf("command reply must decode as the legacy envelope, got %s (%v)", ack.data, err)
	}
	if strings.Contains(ack.data, `"run"`) {
		t.Errorf("command reply must not carry a run: %s", ack.data)
	}

	// The run: strict family order, one run id throughout.
	postMsg(t, srv, tok, "hello")
	d1 := wantFrame(t, tap, "delta", `"text":"Hel"`)
	var del webDeltaEvent
	if err := json.Unmarshal([]byte(d1.data), &del); err != nil || del.Text != "Hel" {
		t.Fatalf("delta frame: %s (%v)", d1.data, err)
	}
	run := del.Run
	if run == "" {
		t.Fatal("delta must carry a server-minted run id (§2.5)")
	}
	sawDelta1 := time.Now()

	d2 := wantFrame(t, tap, "delta", `"text":"lo"`, `"run":"`+run+`"`)
	sawDelta2 := time.Now()
	_ = d2
	// Liveness: the stub writes the two deltas 150ms apart, so a live
	// stream spans the gap; a dump-at-exit stream would land them
	// back-to-back.
	if gap := sawDelta2.Sub(sawDelta1); gap < 50*time.Millisecond {
		t.Errorf("deltas arrived %v apart, want ≥50ms (live delivery, stub gap is 150ms)", gap)
	}

	// Tool rows: folded start/end, ok only on end, args/result never
	// forwarded (§2.5 — the stub's args/result/secret payload must not
	// reach the wire).
	ts := wantFrame(t, tap, "tool", `"phase":"start"`, `"id":"t1"`, `"name":"read"`, `"run":"`+run+`"`)
	if strings.Contains(ts.data, "args") || strings.Contains(ts.data, "secret payload") {
		t.Errorf("tool start must not forward args: %s", ts.data)
	}
	if strings.Contains(ts.data, `"ok"`) {
		t.Errorf("tool start must not carry ok: %s", ts.data)
	}
	te := wantFrame(t, tap, "tool", `"phase":"end"`, `"ok":true`, `"run":"`+run+`"`)
	if strings.Contains(te.data, "result") || strings.Contains(te.data, "secret payload") {
		t.Errorf("tool end must not forward the result: %s", te.data)
	}

	// Usage: server-side $ — token fields verbatim, cost matching the
	// embedded card at whichever tier the event's instant fell in
	// (both accepted: a tier boundary crossed between emit and check
	// would otherwise flake once a day).
	ufr := wantFrame(t, tap, "usage", `"prompt":1000`, `"completion":200`, `"cache_hit":800`, `"run":"`+run+`"`)
	var u webUsageEvent
	if err := json.Unmarshal([]byte(ufr.data), &u); err != nil {
		t.Fatalf("usage frame: %s (%v)", ufr.data, err)
	}
	usage := deepseek.Usage{
		PromptTokens:          1000,
		CompletionTokens:      200,
		PromptCacheHitTokens:  800,
		PromptCacheMissTokens: 200,
	}
	cPeak := pricing.Cost("deepseek-flash", pricing.TierStandard, usage)
	cOff := pricing.Cost("deepseek-flash", pricing.TierOffPeak, usage)
	if !floatEq(u.CostUSD, cPeak) && !floatEq(u.CostUSD, cOff) {
		t.Errorf("usage.cost_usd = %v, want the flash card at either tier (%v peak / %v off-peak)", u.CostUSD, cPeak, cOff)
	}

	// Reply close-out: authoritative text over the accumulated deltas,
	// the run id to key the replacement, session_id from agent_end —
	// and still decodable by the legacy two-field client.
	rfr := wantFrame(t, tap, "", `"text":"Hello"`)
	var re webReplyEvent
	if err := json.Unmarshal([]byte(rfr.data), &re); err != nil {
		t.Fatalf("reply frame: %s (%v)", rfr.data, err)
	}
	if re.Run != run || re.SessionID != "SW1" || re.Conv != "web:default" {
		t.Errorf("reply close-out = %+v, want run %s session SW1", re, run)
	}

	// Second run: a FRESH run id (server lifecycle, not correlation
	// reuse) and the resumed session id back on the close-out.
	postMsg(t, srv, tok, "again")
	d3 := wantFrame(t, tap, "delta", `"text":"Hel"`)
	var del2 webDeltaEvent
	if err := json.Unmarshal([]byte(d3.data), &del2); err != nil {
		t.Fatalf("second-run delta: %s (%v)", d3.data, err)
	}
	if del2.Run == run {
		t.Errorf("second prompt reused run id %s — runs must be minted per message", run)
	}
	wantFrame(t, tap, "delta", `"text":"lo"`)
	wantFrame(t, tap, "tool", `"phase":"start"`)
	wantFrame(t, tap, "tool", `"phase":"end"`)
	wantFrame(t, tap, "usage")
	r2 := wantFrame(t, tap, "", `"text":"Hello"`)
	if !strings.Contains(r2.data, `"session_id":"SW1"`) {
		t.Errorf("second close-out must carry the resumed session id: %s", r2.data)
	}
}

// TestWeb_StreamCancelClosesRun: the §8 /cancel entry — kill mid-run
// and the stream CLOSES (both 已中断 replies arrive promptly; no delta
// after the kill; no hang). Exactly one closing reply carries the live
// run id: the /cancel ack and the run's terminal reply race (the ack
// pops the run table first in practice, but the order is not a
// contract — clients are run-idempotent per §2.4).
func TestWeb_StreamCancelClosesRun(t *testing.T) {
	fe, srv, tok := newStreamE2E(t, "web_hang")
	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	waitSubs(t, fe, webConvID(), 1)

	postMsg(t, srv, tok, "/bind "+t.TempDir())
	wantFrame(t, tap, "", "已绑定项目")

	postMsg(t, srv, tok, "work")
	d := wantFrame(t, tap, "delta", `"text":"stuck"`)
	var del webDeltaEvent
	if err := json.Unmarshal([]byte(d.data), &del); err != nil {
		t.Fatalf("delta frame: %s (%v)", d.data, err)
	}
	run := del.Run

	postMsg(t, srv, tok, "/cancel")

	deadline := time.After(5 * time.Second)
	replies, withRun := 0, 0
	for replies < 2 {
		select {
		case fr, ok := <-tap:
			if !ok {
				t.Fatalf("stream closed after only %d of 2 closing replies", replies)
			}
			switch fr.name {
			case "": // default event: the two 已中断 replies
				if !strings.Contains(fr.data, "已中断") {
					t.Fatalf("unexpected default frame during close-out: %s", fr.data)
				}
				replies++
				if strings.Contains(fr.data, `"run":"`+run+`"`) {
					withRun++
				}
			case "delta":
				t.Fatalf("delta must not arrive after /cancel: %s", fr.data)
			default:
				t.Fatalf("unexpected frame during close-out: %s %s", fr.name, fr.data)
			}
		case <-deadline:
			t.Fatalf("stream did not close within 5s of /cancel (replies so far: %d) — run still hanging?", replies)
		}
	}
	if withRun != 1 {
		t.Errorf("exactly one closing reply must carry run %s, got %d", run, withRun)
	}
	// Settled: nothing further (the killed stub is gone; the conv gate
	// released).
	select {
	case fr := <-tap:
		t.Fatalf("extra frame after close-out: %+v", fr)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestWeb_TurnCostUSDTiers pins the server-side pricing helper on
// fixed instants (the tier clock is the only time-dependent input):
// DeepSeek off-peak is exactly half of peak, GLM is tier-invariant,
// unknown models fall back to the flash card, and a cache-hit count
// above prompt tokens cannot price negative.
func TestWeb_TurnCostUSDTiers(t *testing.T) {
	// 2026-11-16 is a Monday: 10:00 Shanghai sits in the morning peak
	// window, 20:00 is off-peak.
	peak := time.Date(2026, 11, 16, 10, 0, 0, 0, pricing.Shanghai)
	off := time.Date(2026, 11, 16, 20, 0, 0, 0, pricing.Shanghai)
	if pricing.CurrentTier(peak) != pricing.TierStandard || pricing.CurrentTier(off) != pricing.TierOffPeak {
		t.Fatal("test wiring: the chosen instants must straddle a tier boundary")
	}
	ev := RunEvent{PromptTokens: 1000, CompletionTokens: 200, CacheHitTokens: 800}
	flashPeak := turnCostUSD("deepseek-flash", ev, peak)
	flashOff := turnCostUSD("deepseek-flash", ev, off)
	if flashPeak <= 0 {
		t.Fatalf("peak cost = %v, want > 0", flashPeak)
	}
	if !floatEq(flashOff, flashPeak/2) {
		t.Errorf("deepseek off-peak = %v, want exactly half of peak %v", flashOff, flashPeak)
	}
	// Hand-check against the embedded flash card: miss 200×$0.30 +
	// hit 800×$0.006 + output 200×$1.20 per 1M.
	if want := (200*0.30 + 800*0.006 + 200*1.20) / 1e6; !floatEq(flashPeak, want) {
		t.Errorf("peak cost = %v, want %v (embedded flash card math)", flashPeak, want)
	}
	if g := turnCostUSD("glm-5.3", ev, peak); !floatEq(turnCostUSD("glm-5.3", ev, off), g) {
		t.Errorf("glm-5.3 must be tier-invariant (NoOffPeakDiscount), peak %v vs off %v", g, turnCostUSD("glm-5.3", ev, off))
	}
	if !floatEq(turnCostUSD("", ev, peak), flashPeak) {
		t.Error("unknown model must fall back to the flash card")
	}
	// Clamp: hit (200) above prompt (100) → miss clamped to 0, never
	// a negative component.
	clamp := turnCostUSD("deepseek-flash", RunEvent{PromptTokens: 100, CacheHitTokens: 200}, peak)
	if clamp < 0 || !floatEq(clamp, 200*0.006/1e6) {
		t.Errorf("clamped cost = %v, want the hit-only price %v", clamp, 200*0.006/1e6)
	}
}

// TestRunEvent_DecodesAgentStartModel pins the W1.4 hop: RunEvent
// mirrors jsonLine's agent_start.model (W0.3) — SendStream's pricing
// key rides it — and stays zero-value-compatible with model-less
// lines.
func TestRunEvent_DecodesAgentStartModel(t *testing.T) {
	ev, ok := parseRunEvent(`{"type":"agent_start","model":"deepseek-flash"}`)
	if !ok || ev.Type != "agent_start" || ev.Model != "deepseek-flash" {
		t.Fatalf("RunEvent must mirror agent_start.model, got %+v ok=%v", ev, ok)
	}
	ev2, ok2 := parseRunEvent(`{"type":"agent_start"}`)
	if !ok2 || ev2.Model != "" {
		t.Fatalf("model-less agent_start must decode with Model empty, got %+v ok=%v", ev2, ok2)
	}
}

// floatEq compares floats at 1e-12 absolute — costs here are ~1e-4
// scale, so this is a ~1e-8 relative bound: enough to accept equal
// math with different operation order, tight enough to catch any real
// rate or mapping error.
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-12
}
