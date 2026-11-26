package controlplane

// W5 ContextMeter tests (card c_oyp2qf acceptance): the top-bar meter's
// inputs ride the W1.4 usage family, so the wire layer pins here over the
// real path (stub seek → executor → SSE), same harness split as the W2.3
// meter (webfrontend_usagebar_test.go):
//
//   - acceptance ③ (model/lane display agrees with the backend): the
//     SAME model family on its two lane ids — deepseek-flash (direct
//     API) vs deepseek-v4.1-flash (OpenCode Go gateway) — must arrive
//     with the lane label modelcap.LaneOf determines and the window
//     budget.Limit resolves. The page renders these fields verbatim, so
//     wire == backend determination by construction, and this test is
//     the proof the wire carries them.
//   - acceptance ①'s page half: the meter's wiring is pinned by string
//     contract over the embed (TestWeb_PageCtxMeter), the established
//     pattern for vanilla-JS page pieces.
//
// Acceptance ② (kill-the-SSE drill: light yellow → banner → green on
// resume) is a live-browser behaviour — sandbox E2E:
// scripts/web-ctxmeter-manual.sh + web-ctxmeter-headless.mjs.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/internal/modelcap"
)

// TestWeb_UsageCarriesLaneAndWindow: one stub run per lane sample; each
// usage frame must carry the model, modelcap's lane label for it, and
// budget's window — the display face never determines anything itself.
func TestWeb_UsageCarriesLaneAndWindow(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "deepseek-v4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			t.Setenv("CP_STUB_MODEL", model)
			fe, srv, tok := newStreamE2E(t, "web_lane")
			tap, resp := openSSEFrames(t, srv, tok)
			defer resp.Body.Close()
			waitSubs(t, fe, webConvID(), 1)

			postMsg(t, srv, tok, "/bind "+t.TempDir())
			wantFrame(t, tap, "", "已绑定项目")

			postMsg(t, srv, tok, "lane check")
			wantFrame(t, tap, "delta", `"text":"lane check"`)
			ufr := wantFrame(t, tap, "usage", `"model":"`+model+`"`)
			var u webUsageEvent
			if err := json.Unmarshal([]byte(ufr.data), &u); err != nil {
				t.Fatalf("usage frame: %s (%v)", ufr.data, err)
			}

			// The backend determination, computed HERE from the same
			// packages the server used — not a literal, so a lane-table
			// change moves both sides together.
			wantLaneID, wantLaneLbl := modelcap.LaneOf(model)
			if wantLaneID == "" {
				t.Fatalf("test bug: %q has no lane", model)
			}
			if u.Lane != wantLaneLbl {
				t.Errorf("usage.lane = %q, want modelcap's label %q", u.Lane, wantLaneLbl)
			}
			if wantLimit := budget.Limit(model); u.CtxLimit != wantLimit {
				t.Errorf("usage.ctx_limit = %d, want budget.Limit %d", u.CtxLimit, wantLimit)
			}
			// The two samples must genuinely straddle two lanes — a
			// determination regression that collapses them to one label
			// would otherwise pass both subtests vacuously.
			if u.Lane == "" {
				t.Errorf("usage frame carried no lane label: %s", ufr.data)
			}

			// Wait out the run's close-out before returning (c_2vst75):
			// the usage frame rides mid-run; the per-message goroutine
			// still owes the D-C3 session writeback (store.Put into this
			// test's first TempDir) plus this reply. Without the wait
			// that write raced t.TempDir's RemoveAll ("directory not
			// empty" under load) — the close-out is handlePrompt's last
			// act, so observing it orders the writeback before cleanup.
			wantFrame(t, tap, "", `"conv":"web:default"`, `"session_id":"SWL"`)
		})
	}
}

// TestWeb_UsageCarriesLaneTwoLanesDiffer: the acceptance sample pair must
// resolve to DIFFERENT lane labels — the whole point of the two-lane
// sample is that the display can tell them apart.
func TestWeb_UsageCarriesLaneTwoLanesDiffer(t *testing.T) {
	seen := map[string]string{}
	for _, model := range []string{"deepseek-flash", "deepseek-v4.1-flash"} {
		_, lbl := modelcap.LaneOf(model)
		if lbl == "" {
			t.Fatalf("%q has no lane label", model)
		}
		seen[lbl] = model
	}
	if len(seen) < 2 {
		t.Errorf("the two-lane sample collapsed to one label %v — the lane determination regressed", seen)
	}
}

// TestWeb_PageCtxMeter pins the page contract (string contract over the
// embed, the markdown/usagebar precedent): the meter listens to the named
// usage event, consumes the server-determined lane/window fields, derives
// occupancy from the LAST turn (not the cumulative sum), and grades colour
// at budget's thresholds; the connection light + banner + manual reconnect
// are all present and wired.
func TestWeb_PageCtxMeter(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		// ContextMeter: consumes the W5 usage additions verbatim
		"u.ctx_limit", // window from budget, server-side
		"u.lane",      // lane label from modelcap, server-side
		"ctx.used = (u.prompt || 0) + (u.completion || 0);", // LAST turn, not meter.prompt+meter.completion (cumulative)
		"renderCtx()", // usage listener repaints the meter
		// colour grades at budget.Classify's thresholds (0.60/0.75)
		"frac >= 0.75 ? 'crit' : frac >= 0.6 ? 'warn' : ''",
		// remaining-tokens readout (已用/余量 both present)
		"'% · 余 '",
		// connection light + banner + manual reconnect
		"id=\"conn\"",
		"connSet(false)",
		"connSet(true)",
		"id=\"connbar\"",
		"id=\"reconn\"",
		"addEventListener('click', () => listen())",
		// reconnect must not double-connect: listen closes the old ES first
		"if (es) es.close();",
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the ContextMeter piece %q (W5 c_oyp2qf)", need)
		}
	}
	// The meter and the light mount in the top bar, ahead of the usage
	// accumulator; the banner sits right under the header.
	for _, keep := range []string{
		`<span id="ctx" hidden>`,
		`<div id="connbar" hidden>`,
	} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the top-bar element %q", keep)
		}
	}
	// The old onerror that stomped the binding line must be gone — the
	// light/banner own the disconnect story now.
	if strings.Contains(page, "setStatus('SSE 断开") {
		t.Errorf("onerror still overwrites the binding status line — the conn light + banner own disconnect state (W5 c_oyp2qf)")
	}
}
