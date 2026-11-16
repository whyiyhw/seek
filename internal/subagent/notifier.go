package subagent

import (
	"context"
	"strings"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Notifier is the PrePromptHook that delivers background-subagent
// terminal notices into the parent session (feature-orchestration
// PRD §4.1 D2). At each parent Prompt it drains the Manager's notice
// queue and returns every pending notice as a user-role <context>
// Prepend message — the existing injection seam (cache-volatile,
// this-turn region; agent.Prompt appends Prepend messages to
// history, so the notice is persisted into the MAIN session
// transcript by the normal save path and reconstructable on resume).
//
// This is the "completion event lands in the main session stream"
// half of the background contract. The other half — waking the TUI
// loop — is Manager.WakeCh; both read the same inbox without
// stealing from each other (Notifier DRAINS, TUI PEEKS via
// PendingNotices).
//
// REGISTRATION CONTRACT: register on the PARENT's hooks Registry
// only. Child subagents share the parent's hook surface through a
// hooks.Registry.CloneWithout(notifier) snapshot (cmd/seek wiring) —
// a child Prompt running this hook would drain the parent's pending
// notices into the wrong transcript.
type Notifier struct {
	mgr *Manager
}

// NewNotifier constructs the delivery hook for mgr's background
// notices. mgr must be the same Manager the parent's `agent` tool
// dispatches to.
func NewNotifier(m *Manager) *Notifier {
	return &Notifier{mgr: m}
}

// OnPrePrompt drains pending notices and shapes them as Prepend
// messages. One message per notice (not one merged blob): each
// carries an independently matchable "[agent: background ...]"
// wire-format prefix, and notices are rare (bounded by MaxConcurrent
// in flight), so per-notice granularity costs nothing.
//
// Delivery is also the `notified` WRITE point (PRD §4.1 D3/D5): each
// drained notice appends one consumption marker event to
// subagents.jsonl. The marker is what the resume backfill's fold
// reads to skip already-ingested rows — repeated resume never
// re-injects (幂等). Append-only, never rewrites the terminal event,
// and carries no summary (D5: the index is a narrow state stream).
//
// Marker writes are best-effort with the error swallowed: by the time
// we get here the notice IS delivered (the Prepend below is the
// delivery), so failing the whole Prompt over a marker would trade a
// delivered notice for nothing. The residual risk of a swallowed
// error is one duplicate delivery on the next resume — the same
// degradation class as runAndFinish's swallowed terminal-event
// writes.
//
// Returns the zero PrePromptOut when nothing is pending — "" means
// "no change" for UserText per the hooks contract.
func (n *Notifier) OnPrePrompt(_ context.Context, _ hooks.PrePromptIn) (hooks.PrePromptOut, error) {
	notices := n.mgr.DrainNotices()
	if len(notices) == 0 {
		return hooks.PrePromptOut{}, nil
	}
	var out hooks.PrePromptOut
	for _, nt := range notices {
		out.Prepend = append(out.Prepend, deepseek.Message{
			Role:    deepseek.RoleUser,
			Content: "<context>\n" + strings.TrimSpace(nt.Summary) + "\n</context>",
		})
		_ = n.mgr.markNotified(nt.SubSid)
	}
	return out, nil
}

// markNotified appends the `notified` consumption marker for subSid
// to the project index — the delivery-side write that closes the
// notification loop (fold reads it back as Notified=true). Called
// ONLY from OnPrePrompt: the marker means "ingested into the parent
// transcript", which is a property of Prepend delivery, not of
// enqueueing.
//
// parent_turn is deliberately omitted (the event shape allows it):
// the parent turn counter isn't plumbed to the PrePrompt seam — the
// same M11.0 limitation the `started` event's ParentTurn documents
// (internal/tools/agent: "ParentTurn is 0 ... the counter isn't
// wired through"). The fold doesn't read it; it stays forensically
// useful once a future card wires the counter.
func (m *Manager) markNotified(subSid string) error {
	indexPath, err := paths.SubagentsIndex(m.opts.ProjectAbsPath)
	if err != nil {
		return err
	}
	return appendEvent(indexPath, event{Kind: "notified", SubSid: subSid})
}

// Compile-time assertion: Notifier is registrable as a PrePromptHook.
var _ hooks.PrePromptHook = (*Notifier)(nil)
