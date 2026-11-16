package controlplane

// W2.3 status-meter tests (card c_npafpi acceptance; feature-web-
// frontend §3.1 item 3 / §9 W2.3): the page's token/$ meter consumes
// the W1.4 usage family — every turn_end accumulates (tokens plus the
// SERVER-priced $), the reply close-out pins the session id short.
// Acceptance line: 桩 seek 多 turn e2e 断言数值正确累积.
//
// Two layers, the established split for page cards (markdown renderer
// c_iha3fr, sessions browser c_hinacy — the page is vanilla JS with no
// node chain, §4.2, so its behaviour is pinned by string contract):
//
//   - the NUMBERS the meter accumulates from are pinned here over the
//     real wire (the webfrontend_stream_test.go harness, new
//     web_multi_turn stub): two turn_end lines → two usage frames in
//     turn order under ONE run id, summed totals exactly 2200/350/1900
//     plus $1+$2 — a last-event-wins or per-turn-reset wire fails the
//     arithmetic;
//   - the meter's page wiring is pinned by TestWeb_PageUsageMeter.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestWeb_MultiTurnUsageAccumulates: the W2.3 acceptance — a stub seek
// run with two turns must surface two usage events whose numbers the
// meter sums, with the session id arriving only on the close-out.
func TestWeb_MultiTurnUsageAccumulates(t *testing.T) {
	fe, srv, tok := newStreamE2E(t, "web_multi_turn")
	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	waitSubs(t, fe, webConvID(), 1)

	postMsg(t, srv, tok, "/bind "+t.TempDir())
	wantFrame(t, tap, "", "已绑定项目")

	postMsg(t, srv, tok, "two turns please")

	// Turn 0: deltas, then the first usage frame — run id minted here
	// must persist through BOTH turns (one message, one run).
	d := wantFrame(t, tap, "delta", `"text":"Hel"`)
	var del webDeltaEvent
	if err := json.Unmarshal([]byte(d.data), &del); err != nil || del.Run == "" {
		t.Fatalf("first delta must carry a run id: %s (%v)", d.data, err)
	}
	run := del.Run
	wantFrame(t, tap, "delta", `"text":"lo"`, `"run":"`+run+`"`)
	u1fr := wantFrame(t, tap, "usage", `"prompt":1000`, `"run":"`+run+`"`)
	var u1 webUsageEvent
	if err := json.Unmarshal([]byte(u1fr.data), &u1); err != nil {
		t.Fatalf("usage frame 1: %s (%v)", u1fr.data, err)
	}

	// Turn 1: a tool call and another delta between the usage frames —
	// the meter's accumulation must survive interleaved family members.
	wantFrame(t, tap, "tool", `"phase":"start"`, `"run":"`+run+`"`)
	wantFrame(t, tap, "tool", `"phase":"end"`, `"run":"`+run+`"`)
	wantFrame(t, tap, "delta", `"text":"more"`, `"run":"`+run+`"`)
	u2fr := wantFrame(t, tap, "usage", `"prompt":1200`, `"run":"`+run+`"`)
	var u2 webUsageEvent
	if err := json.Unmarshal([]byte(u2fr.data), &u2); err != nil {
		t.Fatalf("usage frame 2: %s (%v)", u2fr.data, err)
	}

	// Per-frame shape: the stub's turn numbers verbatim.
	if u1.Prompt != 1000 || u1.Completion != 200 || u1.CacheHit != 800 {
		t.Errorf("usage 1 = %+v, want 1000/200/800", u1)
	}
	if u2.Prompt != 1200 || u2.Completion != 150 || u2.CacheHit != 1100 {
		t.Errorf("usage 2 = %+v, want 1200/150/1100", u2)
	}

	// Close-out: the concatenated text plus the agent_end session id —
	// the meter's sid short display sources exactly this field.
	rfr := wantFrame(t, tap, "", `"text":"Hellomore"`)
	var re webReplyEvent
	if err := json.Unmarshal([]byte(rfr.data), &re); err != nil {
		t.Fatalf("reply frame: %s (%v)", rfr.data, err)
	}
	if re.Run != run || re.SessionID != "SW2" {
		t.Errorf("close-out = %+v, want run %s session SW2", re, run)
	}

	// The meter's arithmetic over these frames (the page's meter adds
	// every field, renders prompt+completion as tok): totals are the
	// SUM across turns — not the last turn, not either turn alone.
	if g := u1.Prompt + u2.Prompt; g != 2200 {
		t.Errorf("meter prompt total = %d, want 2200 (sum of both turns)", g)
	}
	if g := u1.Completion + u2.Completion; g != 350 {
		t.Errorf("meter completion total = %d, want 350", g)
	}
	if g := u1.CacheHit + u2.CacheHit; g != 1900 {
		t.Errorf("meter cache-hit total = %d, want 1900", g)
	}
	if tok1, tok2 := u1.Prompt+u1.Completion, u2.Prompt+u2.Completion; tok1 != 1200 || tok2 != 1350 {
		t.Errorf("per-turn tok = %d/%d, want 1200/1350", tok1, tok2)
	}
	total := u1.CostUSD + u2.CostUSD
	if total <= 0 || floatEq(total, u1.CostUSD) || floatEq(total, u2.CostUSD) {
		t.Errorf("meter $ total = %v, want the sum of both turns' costs (%v + %v)", total, u1.CostUSD, u2.CostUSD)
	}

	// Each frame's $ is the server-side card for its own turn — the
	// page adds cost_usd verbatim and never re-prices (§2.5); either
	// tier accepted (a boundary crossed between emit and check would
	// otherwise flake once a day, same posture as the W1.4 e2e).
	for i, u := range []webUsageEvent{u1, u2} {
		usage := deepseek.Usage{
			PromptTokens:          u.Prompt,
			CompletionTokens:      u.Completion,
			PromptCacheHitTokens:  u.CacheHit,
			PromptCacheMissTokens: u.Prompt - u.CacheHit,
		}
		cPeak := pricing.Cost("deepseek-flash", pricing.TierStandard, usage)
		cOff := pricing.Cost("deepseek-flash", pricing.TierOffPeak, usage)
		if !floatEq(u.CostUSD, cPeak) && !floatEq(u.CostUSD, cOff) {
			t.Errorf("usage %d cost_usd = %v, want the flash card at either tier (%v peak / %v off-peak)", i+1, u.CostUSD, cPeak, cOff)
		}
	}
}

// TestWeb_PageUsageMeter pins the W2.3 page contract the way every page
// piece is pinned (string contract over the embed): the meter listens
// to the NAMED usage event (onmessage never sees named frames — losing
// the listener strands the meter silently), consumes the server-priced
// cost_usd and the full token breakdown, and shows the session id short
// from the reply close-out.
func TestWeb_PageUsageMeter(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		"addEventListener('usage'",          // the named W1.4 event feeds the meter
		"cost_usd",                          // $ is the SERVER-computed field — the page never prices (§2.5)
		"cache_hit",                         // the full token breakdown accumulates
		"session_id",                        // agent_end's id rides the reply close-out
		"' · sid ' + meter.sid.slice(0, 8)", // ... and displays short
		"fmtUsd",                            // $ format mirrors pricing.FormatCost thresholds
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the usage-meter piece %q (W2.3)", need)
		}
	}
	// The status bar keeps its left half: the binding line still has a
	// dedicated element to write into (setStatus target moved, it did
	// not disappear).
	for _, keep := range []string{"id=\"status-l\"", "id=\"usage\""} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the status-bar element %q", keep)
		}
	}
}
