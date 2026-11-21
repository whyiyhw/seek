package main

// W5 card c_n37tnr, child half (feature-web-frontend.md §13.1's
// c_n37tnr contract): the print-mode approval channel. With
// `seek -p … --json --approval-stdin` the host registers askFn on the
// permission policy — instead of denying silently it emits one
// additive JSONL line
//
//	{"type":"approval_request","req_id":"<minted>","kind":"bash",
//	 "summary":"…","command":"…","diff":"…"}
//
// and blocks reading stdin decision lines
//
//	{"type":"approval_decision","req_id":"…","decision":"y|n|a"}
//
// "a" means allow + a per-Kind session grant (the TUI's
// "[a] always: <kind>" answer, permission.Policy.SetAlwaysAllow).
// Anything else — "n", a malformed line, EOF — is a denial, which the
// tool layer already renders as the ErrDenied tool result (§6
// verified ②), so the fallback experience is unchanged. A request
// still pending when the run ends emits
//
//	{"type":"approval_cancel","req_id":"…"}
//
// so the consumer can withdraw the card.
//
// The channel is OPT-IN: plain `seek -p` keeps print mode's documented
// default (deny). The plane's executor passes the flag and holds the
// child's stdin writer (internal/controlplane/executor.go, "child
// half" seam), writing each browser decision back keyed by req_id.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/whyiyhw/seek/internal/permission"
)

// approvalDecision is one stdin decision line. Type is optional on the
// read side (a bare {"req_id","decision"} is accepted) — the producer
// is the plane, and being lenient here costs nothing while a stricter
// parser would turn a producer slip into a hung run.
type approvalDecision struct {
	Type     string `json:"type"`
	ReqID    string `json:"req_id"`
	Decision string `json:"decision"`
}

// approvalRelay is the print-mode askFn: it owns the JSONL emitter it
// shares with runJSON (one encoder ⇒ a card line can never interleave
// with a partial event line) and the stdin decision reader.
type approvalRelay struct {
	emit func(jsonLine)
	in   io.Reader

	// grant is the "[a]" answer's effect — wired to
	// permission.Policy.SetAlwaysAllow by main. nil degrades "a" to
	// allow-once (same as the TUI's nil-AlwaysAllowKind posture).
	grant func(permission.Kind)

	seq atomic.Int64 // req_id suffix; the pid keeps ids unique across runs

	mu      sync.Mutex
	pending map[string]bool // surfaced, undecided req ids — CancelPending's set

	once  sync.Once
	lines chan approvalDecision
}

// newApprovalRelay builds one relay over the decision source (os.Stdin
// in production; tests inject a scripted reader). emit is installed by
// runJSON before the agent loop starts — that write happens-before the
// goroutine the loop spawns, so the reader-side emit calls see it
// without a lock.
func newApprovalRelay(in io.Reader) *approvalRelay {
	return &approvalRelay{
		in:      in,
		pending: make(map[string]bool),
		lines:   make(chan approvalDecision, 8),
	}
}

// start launches the stdin reader exactly once. EOF closes lines,
// which every waiter reads as "no decider attached" → deny. Junk lines
// are skipped (a wrapper echoing logs on the same fd must not kill the
// channel); a decision nobody is waiting for is dropped rather than
// blocking the reader.
func (r *approvalRelay) start() {
	r.once.Do(func() {
		go func() {
			defer close(r.lines)
			sc := bufio.NewScanner(r.in)
			sc.Buffer(make([]byte, 0, 4096), 1<<20)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				var d approvalDecision
				if err := json.Unmarshal([]byte(line), &d); err != nil {
					continue
				}
				if d.Type != "" && d.Type != "approval_decision" {
					continue
				}
				if d.ReqID == "" || d.Decision == "" {
					continue
				}
				select {
				case r.lines <- d:
				default: // nobody waiting: drop, never block the reader
				}
			}
		}()
	})
}

// askFn adapts the relay to permission.Policy.SetAskFn. ctx is the
// session's signal context: cancellation withdraws the card and
// answers "deny" so the run can unwind.
func (r *approvalRelay) askFn(ctx context.Context) func(permission.Action) bool {
	return func(a permission.Action) bool { return r.ask(ctx, a) }
}

// ask surfaces one request and waits for its decision.
func (r *approvalRelay) ask(ctx context.Context, a permission.Action) bool {
	r.start()
	id := fmt.Sprintf("ap-%d-%d", os.Getpid(), r.seq.Add(1))
	r.mu.Lock()
	r.pending[id] = true
	r.mu.Unlock()
	r.emit(jsonLine{
		Type:    "approval_request",
		ReqID:   id,
		Kind:    string(a.Kind),
		Summary: approvalSummary(a),
		Command: a.Command,
		Diff:    a.Display.Diff,
	})

	for {
		select {
		case <-ctx.Done():
			r.cancel(id)
			return false
		case d, ok := <-r.lines:
			if !ok { // stdin EOF: no human attached → today's default
				r.cancel(id)
				return false
			}
			if d.ReqID != id {
				continue // stale decision for an abandoned request
			}
			r.resolve(id)
			switch d.Decision {
			case "y":
				return true
			case "a":
				if r.grant != nil {
					r.grant(a.Kind)
				}
				return true
			default:
				return false
			}
		}
	}
}

// resolve marks id decided (no cancel line: the consumer sees the
// decision's effect in the run's own stream).
func (r *approvalRelay) resolve(id string) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

// cancel withdraws one still-pending request, emitting approval_cancel
// exactly once per id.
func (r *approvalRelay) cancel(id string) {
	r.mu.Lock()
	was, ok := r.pending[id]
	delete(r.pending, id)
	r.mu.Unlock()
	if ok && was {
		r.emit(jsonLine{Type: "approval_cancel", ReqID: id})
	}
}

// CancelPending withdraws every request still open when the run ends
// (deferred in main's json branch): the child is about to stop, so a
// card left up would hang in the browser forever.
func (r *approvalRelay) CancelPending() {
	r.mu.Lock()
	ids := make([]string, 0, len(r.pending))
	for id := range r.pending {
		ids = append(ids, id)
	}
	r.mu.Unlock()
	for _, id := range ids {
		r.cancel(id)
	}
}

// approvalSummary is the card's one-liner: what the human is being
// asked to allow, in the same register as the TUI's prompt. The
// machine-readable detail rides Command / Diff alongside.
func approvalSummary(a permission.Action) string {
	switch a.Kind {
	case permission.KindBash:
		return "run a shell command"
	case permission.KindWrite:
		return "write " + a.Path
	case permission.KindEdit:
		return "edit " + a.Path
	case permission.KindRead:
		return "read " + a.Path
	case permission.KindMemoryRemember:
		return "save memory " + a.Display.MemoryName
	case permission.KindSkillInstall:
		return "install skill " + a.Display.SkillName
	default:
		return string(a.Kind)
	}
}
