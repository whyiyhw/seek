package controlplane

// W1.4 streaming wiring (card c_qcmul4; feature-web-frontend.md §2.4
// seam 1 landing + §2.5 event family): WebFrontend implements the
// OPTIONAL StreamSender extension, so the router drives prompts
// through Executor.RunStream and forwards each live jsonLine event
// here. This file translates that stream into the /events SSE family:
//
//	delta  {run, text}                      ← text_delta (non-reasoning)
//	tool   {run, id, name, phase, ok}       ← tool_start / tool_end
//	usage  {run, prompt, completion,
//	        cache_hit, cost_usd,
//	        model?, lane?, ctx_limit?}      ← turn_end; $ computed
//	                                          SERVER-side (below); the
//	                                          last three feed the top-bar
//	                                          ContextMeter (W5 c_oyp2qf)
//	reply  {"conv","text"} + run/session_id ← Send close-out (the MVP
//	                                          envelope, purely extended)
//	approval {id, conv, run, kind, summary,
//	          command, diff, state, decision} ← approval_request /
//	          approval_cancel run events + decide/cancel receipts — the
//	          approval hub owns the family (webfrontend_approval.go);
//	          W5 c_n37tnr
//
// Run lifecycle: the run id is a SERVER-minted random id (§2.5 —
// deliberately NOT Inbound.EventID, which web leaves empty so the
// plane never dedups browser messages). It is minted on the conv's
// first stream event of a message and popped by the run-closing Send,
// which happens after RunStream returns (the executor joins its stdout
// reader first), so per conv the wire order is always
// events…reply — the executor's per-conv gate serialises consecutive
// runs, making "at most one live run per conv" a structural property,
// not a hopeful one. A command ack that lands while a run is live
// (only /cancel can — it deliberately bypasses the gate) carries the
// run id and closes the bubble; the run's own terminal reply then
// arrives run-less. Clients are run-idempotent per §2.4, so either
// closing order renders correctly.
//
// Filtering follows the §2.5 table exactly: reasoning_delta,
// tool_delta, turn_start, agent_start and agent_end are consumed (run
// metadata) or dropped, never forwarded; tool args/results stay
// server-side (tool_end.result can be huge — the UI shows name+state).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/internal/modelcap"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Compile-time seam pin: WebFrontend carries the optional streaming
// extension (the router upcasts; the port's three methods are in
// webfrontend.go).
var _ StreamSender = (*WebFrontend)(nil)

// webRun is one live streamed run's server-side state. At most one per
// conv (executor gate); guarded by WebFrontend.mu together with the
// subscriber table — the run table IS hub state.
type webRun struct {
	id        string // server-minted, every SSE event of the run carries it
	model     string // agent_start.model — the usage $ pricing key
	sessionID string // agent_end.session_id — rides the reply close-out
}

// mintRunID mints a run id: 16 random bytes, hex. Same entropy class
// as the web token (crypto/rand), half the length — a run id is a
// correlation key, not a bearer credential.
func mintRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on Darwin/Linux/Windows in
		// practice; a zero id would still correlate, not corrupt.
		return "run-0"
	}
	return hex.EncodeToString(b[:])
}

// SendStream implements StreamSender: fold run metadata, translate the
// event, broadcast to the conv's subscribers. Best-effort like Send —
// broadcast drops for slow clients and returns nil, so a full
// subscriber buffer never feeds back into the run (the StreamSender
// contract: errors are the router's to log, never the run's to feel).
func (f *WebFrontend) SendStream(ctx context.Context, conv ConvID, ev RunEvent) error {
	f.mu.Lock()
	run := f.runs[conv]
	if run == nil {
		if f.runs == nil { // lazily built: Run-less tests never touch it
			f.runs = make(map[ConvID]*webRun)
		}
		run = &webRun{id: mintRunID()}
		f.runs[conv] = run
	}
	switch ev.Type {
	case "agent_start":
		run.model = ev.Model
	case "agent_end":
		run.sessionID = ev.SessionID
	}
	f.mu.Unlock()

	var fr sseFrame
	switch ev.Type {
	case "text_delta":
		if ev.Reasoning {
			return nil // reasoning stays out of the MVP UI (§2.5)
		}
		fr = sseFrame{name: "delta", data: webJSON(webDeltaEvent{Run: run.id, Text: ev.Delta})}
	case "approval_request":
		// W5 c_n37tnr: the additive approval seam. The hub broadcasts
		// the card frame itself. The decide callback is the run's stdin
		// writer when the executor filed one (ApprovalRouter.Route ran
		// just before this emit, off the stdout reader); for a producer
		// that speaks the event without that seam (sandbox stubs, the
		// pre-child contract) the callback is nil and the decision
		// still reads back through approval/decide — it just has no
		// child to reach.
		f.RequestApproval(conv, ApprovalPayload{
			ID: ev.ReqID, Run: run.id, Kind: ev.Kind,
			Summary: ev.Summary, Command: ev.Command, Diff: ev.Diff,
		}, f.deliverFor(ev.ReqID))
		return nil
	case "approval_cancel":
		f.CancelApproval(ev.ReqID)
		return nil
	case "tool_start":
		fr = sseFrame{name: "tool", data: webJSON(webToolEvent{Run: run.id, ID: ev.ID, Name: ev.Name, Phase: "start"})}
	case "tool_end":
		fr = sseFrame{name: "tool", data: webJSON(webToolEvent{Run: run.id, ID: ev.ID, Name: ev.Name, Phase: "end", OK: ev.Error == ""})}
	case "turn_end":
		// W5 c_oyp2qf: the usage row also carries the ContextMeter's
		// inputs — model + its lane label (modelcap, the display
		// authority) + the context window (budget, same-source as the
		// TUI status bar). All three additive and empty without a
		// model-bearing agent_start, so pre-W5 frames stay byte-shape
		// compatible.
		_, laneLbl := modelcap.LaneOf(run.model)
		ctxLimit := 0
		if run.model != "" {
			ctxLimit = budget.Limit(run.model)
		}
		fr = sseFrame{name: "usage", data: webJSON(webUsageEvent{
			Run:        run.id,
			Prompt:     ev.PromptTokens,
			Completion: ev.CompletionTokens,
			CacheHit:   ev.CacheHitTokens,
			CostUSD:    turnCostUSD(run.model, ev, time.Now()),
			Model:      run.model,
			Lane:       laneLbl,
			CtxLimit:   ctxLimit,
		})}
	default:
		// agent_start/agent_end fed the run table above; turn_start,
		// reasoning_delta, tool_delta and error are not in the §2.5
		// family — the run's fate is told by the reply close-out.
		return nil
	}
	f.broadcast(conv, fr)
	return nil
}

// popRun removes and returns the conv's live run (nil when none) —
// the Send close-out's half of the lifecycle. Deleting on the FIRST
// run-bearing Send is what makes a /cancel ack close the bubble even
// though a second (run-less) reply follows.
func (f *WebFrontend) popRun(conv ConvID) *webRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[conv]
	delete(f.runs, conv)
	return run
}

// ---- §2.5 event payloads ----

// webReplyEvent is the reply close-out envelope: the MVP
// {"conv","text"} shape with the run additions appended (pure
// addition — a pre-W1.4 client JSON-decodes it and simply never reads
// the new keys; the no-run path in Send keeps marshalling the original
// map, byte-identical).
type webReplyEvent struct {
	Conv      string `json:"conv"`
	Text      string `json:"text"`
	Run       string `json:"run,omitempty"`
	SessionID string `json:"session_id,omitempty"` // "" on killed runs
}

// webDeltaEvent is the delta payload: one non-reasoning text chunk.
type webDeltaEvent struct {
	Run  string `json:"run"`
	Text string `json:"text"`
}

// webToolEvent is the folded tool-call row: phase start|end, ok only
// on end. Args and results deliberately NOT forwarded (§2.5 — the UI
// shows tool name + state; tool_end.result can be huge).
type webToolEvent struct {
	Run   string `json:"run"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phase string `json:"phase"`        // start | end
	OK    bool   `json:"ok,omitempty"` // end only
}

// webUsageEvent is the turn_end token/cost row. cost_usd is computed
// SERVER-side (§2.5): the client never prices, so rate cards live in
// exactly one place — internal/pricing, the same cards seek itself
// uses for its CLI footer. model/lane/ctx_limit (W5 c_oyp2qf) feed the
// top-bar ContextMeter: the lane label is modelcap's determination and
// the window is budget.Limit — both omitempty so a model-less stub run
// (no agent_start.model) emits the pre-W5 shape.
type webUsageEvent struct {
	Run        string  `json:"run"`
	Prompt     int     `json:"prompt"`
	Completion int     `json:"completion"`
	CacheHit   int     `json:"cache_hit"`
	CostUSD    float64 `json:"cost_usd"`
	Model      string  `json:"model,omitempty"`
	Lane       string  `json:"lane,omitempty"`
	CtxLimit   int     `json:"ctx_limit,omitempty"`
}

// turnCostUSD prices one turn_end event at the tier in effect at now —
// the same per-turn math seek itself runs when it Records usage
// (cmd/seek runJSON), so the web $ matches the CLI footer by
// construction. Miss is derived as prompt − cache_hit (deepseek.Usage
// invariant: PromptTokens = hit + miss), clamped at zero so a contract
// slip can't go negative.
func turnCostUSD(model string, ev RunEvent, now time.Time) float64 {
	miss := ev.PromptTokens - ev.CacheHitTokens
	if miss < 0 {
		miss = 0
	}
	return pricing.Cost(model, pricing.CurrentTier(now), deepseek.Usage{
		PromptTokens:          ev.PromptTokens,
		CompletionTokens:      ev.CompletionTokens,
		PromptCacheHitTokens:  ev.CacheHitTokens,
		PromptCacheMissTokens: miss,
	})
}

// webJSON marshals v, falling back to a valid tiny JSON object. The
// payload structs are made of strings/ints/bools — json.Marshal cannot
// fail on them; the fallback keeps a future field with a MarshalJSON
// bug from emitting a garbage SSE line.
func webJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{}`
	}
	return string(b)
}
