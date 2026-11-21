package controlplane

// Remote approval cards (W5 card c_n37tnr, feature-web-frontend §3.4
// + §13.1): the permission y/N decision delivered to the browser. The
// serve-side hub of the contract — producer side: RunEvent
// approval_request / approval_cancel (SendStream translates) or the
// exported RequestApproval / CancelApproval for in-process producers;
// consumer side: the SSE named event `approval` on the conv stream
// plus /rpc approval/list (pending snapshot, the reconnect resync)
// and approval/decide (y|n|a — the plane's only state-mutating RPC
// method: the asset plane stays read-only, this is the interactive
// control surface, a different class).
//
// Idempotency is structural: the decide callback fires exactly once
// per id (a second decide observes state != pending, answers
// applied:false, never touches the callback); a producer retry with a
// known id never resurrects a resolved card.
//
// Child half (landed with this card): `seek -p … --json
// --approval-stdin` registers an askFn that emits approval_request
// jsonLines and blocks on the decision line its host writes to stdin
// (cmd/seek/approval_stdin.go). The plane's executor passes the flag,
// holds the child's stdin pipe and routes each hub decision into it
// keyed by req_id (executor.go's ApprovalRouter seam) — the decide
// callback installed here is that stdin write. "a" maps to
// permission's always-allow, denial stays a tool result. Wire contract:
// §13.1's c_n37tnr row in feature-web-frontend.md.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/whyiyhw/seek/internal/rpc"
)

// approvalStatePending / Resolved / Cancelled are the card lifecycle
// states on the wire (SSE payload + approval/list rows).
const (
	approvalStatePending   = "pending"
	approvalStateResolved  = "resolved"
	approvalStateCancelled = "cancelled"
)

// approvalDecisions is the closed decision set — the y/N/a of the
// card's three buttons, spelled exactly as the future child contract
// spells them.
var approvalDecisions = map[string]bool{"y": true, "n": true, "a": true}

// Payload caps (SSE frame discipline — the card is a glanceable
// prompt, not a transcript): summary one line, command a few, diff a
// small hunk. Producers keep longer context out of the card by
// design; over-cap input is truncated with a marker, not rejected.
const (
	approvalSummaryCap = 200
	approvalCommandCap = 400
	approvalDiffCap    = 2048
)

// approvalRetained bounds the resolved/cancelled entries kept for
// idempotent late decides (a second tab's click racing the first
// receipt). Beyond it the oldest terminal entry is evicted — a decide
// against an evicted id reads as unknown, which the client already
// treats as "card is gone".
const approvalRetained = 256

// ApprovalPayload is one approval request's decision-relevant content:
// permission.Action's subset (Kind/Command) plus Display's rendering
// hints (Summary/Diff), already flattened for the wire. ID is the
// producer-minted request id (the child's req_id); empty → the server
// mints one. Run is the originating run id when known (informational
// on the card, the keying stays ReqID).
type ApprovalPayload struct {
	ID      string
	Run     string
	Kind    string
	Summary string
	Command string
	Diff    string
}

// webApproval is one hub entry. Guarded by WebFrontend.mu together
// with the subscriber/run tables — the hub IS hub state.
type webApproval struct {
	id       string
	conv     ConvID
	payload  ApprovalPayload
	state    string // pending | resolved | cancelled
	decision string // y | n | a once resolved; "" otherwise
	decide   func(decision string)
}

// approvalView is the wire shape: the SSE `approval` event payload
// AND the approval/list row (one struct so the two can never drift).
type approvalView struct {
	ID       string `json:"id"`
	Conv     string `json:"conv"`
	Run      string `json:"run,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Summary  string `json:"summary"`
	Command  string `json:"command,omitempty"`
	Diff     string `json:"diff,omitempty"`
	State    string `json:"state"`
	Decision string `json:"decision,omitempty"`
}

// view snapshots the entry for the wire.
func (e *webApproval) view() approvalView {
	return approvalView{
		ID: e.id, Conv: string(e.conv), Run: e.payload.Run,
		Kind: e.payload.Kind, Summary: e.payload.Summary,
		Command: e.payload.Command, Diff: e.payload.Diff,
		State: e.state, Decision: e.decision,
	}
}

// mintApprovalID mints a request id — same entropy class as the run
// id (crypto/rand hex), its own name so greps stay honest.
func mintApprovalID() string { return mintRunID() }

// approvalTruncate caps s at n bytes with an ellipsis marker. Cuts on
// bytes like the rest of the plane's output shaping; a multibyte char
// split at the boundary renders one replacement glyph, never a panic.
func approvalTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(截断)"
}

// RequestApproval files one pending approval on conv and broadcasts
// the card. decide (optional) fires exactly once, with the first
// decision that lands ("y"/"n"/"a"), OUTSIDE the hub lock. A request
// whose producer id is already known — pending OR terminal — returns
// the existing id untouched: producer retries are idempotent and can
// never resurrect a resolved card.
func (f *WebFrontend) RequestApproval(conv ConvID, p ApprovalPayload, decide func(decision string)) string {
	p.Summary = strings.TrimSpace(p.Summary)
	p.Command = strings.TrimSpace(p.Command)
	if p.ID != "" {
		f.mu.Lock()
		if e, ok := f.approvals[p.ID]; ok {
			f.mu.Unlock()
			return e.id // known id: idempotent no-op
		}
		f.mu.Unlock()
	}
	if p.ID == "" {
		p.ID = mintApprovalID()
	}
	p.Summary = approvalTruncate(p.Summary, approvalSummaryCap)
	p.Command = approvalTruncate(p.Command, approvalCommandCap)
	p.Diff = approvalTruncate(p.Diff, approvalDiffCap)
	e := &webApproval{id: p.ID, conv: conv, payload: p,
		state: approvalStatePending, decide: decide}
	f.mu.Lock()
	if f.approvals == nil {
		f.approvals = make(map[string]*webApproval)
	}
	if _, dup := f.approvals[p.ID]; dup { // raced a twin mint: first wins
		f.mu.Unlock()
		return p.ID
	}
	f.approvals[p.ID] = e
	f.mu.Unlock()
	f.broadcastApproval(e)
	return e.id
}

// decideApproval applies one decision. Unknown id and a decision
// outside {y,n,a} are caller errors (the RPC layer answers -32602);
// an already-terminal entry is the IDEMPOTENT path: the recorded
// decision comes back with applied=false and the callback is NOT
// re-fired — a second tab racing the receipt reads the same outcome.
func (f *WebFrontend) decideApproval(id, decision string) (approvalView, bool, error) {
	if !approvalDecisions[decision] {
		return approvalView{}, false, rpc.Errorf(rpc.CodeInvalidParams,
			"decision must be one of y|n|a, got %q", decision)
	}
	f.mu.Lock()
	e, ok := f.approvals[id]
	if !ok {
		f.mu.Unlock()
		return approvalView{}, false, rpc.Errorf(rpc.CodeInvalidParams,
			"unknown approval id %q (already decided cards withdraw; resync via approval/list)", id)
	}
	if e.state != approvalStatePending {
		v := e.view()
		f.mu.Unlock()
		return v, false, nil // idempotent replay
	}
	e.state = approvalStateResolved
	e.decision = decision
	decide := e.decide
	f.retainTerminal(e)
	f.mu.Unlock()

	if decide != nil {
		decide(decision) // outside the lock: the callback may re-enter
	}
	v := e.view()
	f.broadcastApproval(e)
	return v, true, nil
}

// CancelApproval withdraws a pending card (producer-initiated — the
// run died, the child gave up). No decide callback fires: there is no
// decision, and the producer already knows. Unknown or terminal ids
// are a no-op (cancel is advisory cleanup, not an error path).
func (f *WebFrontend) CancelApproval(id string) {
	f.mu.Lock()
	e, ok := f.approvals[id]
	if !ok || e.state != approvalStatePending {
		f.mu.Unlock()
		return
	}
	e.state = approvalStateCancelled
	f.retainTerminal(e)
	f.mu.Unlock()
	f.broadcastApproval(e)
}

// retainTerminal records a terminal entry in the eviction FIFO and
// trims it to approvalRetained. Caller holds f.mu.
func (f *WebFrontend) retainTerminal(e *webApproval) {
	f.approvalDone = append(f.approvalDone, e.id)
	for len(f.approvalDone) > approvalRetained {
		delete(f.approvals, f.approvalDone[0])
		f.approvalDone = f.approvalDone[1:]
	}
}

// pendingApprovals snapshots conv's pending cards (approval/list's
// payload) — terminal entries never list; the page rebuilds from
// pending only.
func (f *WebFrontend) pendingApprovals(conv ConvID) []approvalView {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []approvalView{} // empty list (not null) on the wire — JS iterates it bare
	for _, e := range f.approvals {
		if e.conv == conv && e.state == approvalStatePending {
			out = append(out, e.view())
		}
	}
	return out
}

// broadcastApproval pushes one entry's view as the named `approval`
// SSE event on its own conv — pending pops the card in every tab of
// that conv, terminal withdraws it everywhere (the tab that clicked
// also gets its RPC receipt; the broadcast is the multi-tab half).
func (f *WebFrontend) broadcastApproval(e *webApproval) {
	f.broadcast(e.conv, sseFrame{name: "approval", data: webJSON(e.view())})
}

// ---- child-half wiring (the executor's ApprovalRouter seam) ----

// approvalDeliver is one run's decision sink, keyed by req_id: the
// executor's stdin-write closure for a child awaiting that id. The
// map is hub state (guarded by f.mu) and holds only ids of a LIVE run
// — Route adds, the decision or the run's Cancel removes. Two runs
// never collide on a key: the executor's per-conv gate means one live
// run per conv, and req_ids are child-minted per process.
type runApprovals struct {
	f    *WebFrontend
	mu   sync.Mutex
	ids  []string
	seen map[string]bool
}

// ApprovalRoute returns a per-run ApprovalRouter for the executor
// (controlplane.ApprovalRouter): Route registers the child's stdin
// writer, Cancel withdraws the run's leftover cards. Called by the
// plane's router for a frontend that implements ApprovalFrontend.
func (f *WebFrontend) ApprovalRoute() ApprovalRouter {
	return &runApprovals{f: f, seen: make(map[string]bool)}
}

// Route implements ApprovalRouter. Registration happens from the
// stdout reader BEFORE the same event is fanned into SendStream, so
// the card is always filed with a live delivery callback.
func (r *runApprovals) Route(reqID string, deliver func(decision string)) {
	r.f.mu.Lock()
	if r.f.approvalDeliver == nil {
		r.f.approvalDeliver = make(map[string]func(string))
	}
	r.f.approvalDeliver[reqID] = deliver
	r.f.mu.Unlock()

	r.mu.Lock()
	if !r.seen[reqID] {
		r.seen[reqID] = true
		r.ids = append(r.ids, reqID)
	}
	r.mu.Unlock()
}

// Cancel implements ApprovalRouter: withdraw every card this run filed
// and forget its delivery sinks. Resolved/cancelled cards are no-ops
// inside CancelApproval, so this is safe for a run that ended cleanly
// after the human answered.
func (r *runApprovals) Cancel() {
	r.mu.Lock()
	ids := r.ids
	r.ids = nil
	r.seen = make(map[string]bool)
	r.mu.Unlock()
	for _, id := range ids {
		r.f.mu.Lock()
		delete(r.f.approvalDeliver, id)
		r.f.mu.Unlock()
		r.f.CancelApproval(id)
	}
}

// deliverFor returns the delivery sink filed for reqID (nil when the
// run has none — a producer that speaks the event without the executor
// seam, e.g. the sandbox stub). Not consumed on read: a duplicate
// approval_request for the same id must not orphan the child's sink.
// Entries live until the run's Cancel or the next run's registration —
// per-conv serialisation means no two live runs share a conv.
func (f *WebFrontend) deliverFor(reqID string) func(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.approvalDeliver[reqID]
}

// RegisterApprovalMethods hangs the two approval methods on reg (the
// servecli web constructor calls it pre-Run, same §3.2 phase contract
// as the asset families — the hub is frontend-native state, so the
// methods live here rather than in internal/rpc/methods).
//
//	approval/list   {"conv":"<browser conv id>"} → {approvals:[pending…]}
//	approval/decide {"id":"…","decision":"y|n|a"}
//	                → {id, state, decision, applied} (applied=false = replay)
func (f *WebFrontend) RegisterApprovalMethods(reg *rpc.Registry) {
	reg.Register("approval/list", func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct {
			Conv string `json:"conv"`
		}
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, rpc.Errorf(rpc.CodeInvalidParams, "invalid params: %v", err)
			}
		}
		conv := webConvID() // absent conv → the legacy default conv
		if p.Conv != "" {
			if !validConvNative(p.Conv) {
				return nil, rpc.Errorf(rpc.CodeInvalidParams,
					"invalid conv %q: 1–64 chars of [A-Za-z0-9._-]", truncateConv(p.Conv))
			}
			conv = NewConvID("web", p.Conv)
		}
		return map[string]any{"approvals": f.pendingApprovals(conv)}, nil
	})
	reg.Register("approval/decide", func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct {
			ID       string `json:"id"`
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "invalid params: %v", err)
		}
		if p.ID == "" {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "id is required")
		}
		v, applied, err := f.decideApproval(p.ID, p.Decision)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"id": v.ID, "state": v.State, "decision": v.Decision, "applied": applied,
		}, nil
	})
}
