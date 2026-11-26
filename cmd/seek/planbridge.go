package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/permission"
	plantool "github.com/whyiyhw/seek/internal/tools/plan"
	"github.com/whyiyhw/seek/pkg/agent"
)

// planBridge implements BOTH the propose tool's Sink and the plan
// tool's Sink. Centralising the wiring here avoids the
// chicken-and-egg of plan-and-propose sharing batch-mode state across
// two structs: propose decides whether batch is on (via the user's
// pick), plan acts on it (flipping the permission gate per step), and
// the policy is the single source of truth that Check consults.
//
// Lifecycle.
//
//	user picks "approve" / "approve_batch" → Approved(steps, batch)
//	    bridge.batch = batch
//	    plan.Seed(steps)                 // emits PlanStepUpdated
//	    policy.SetPreApproved(false)     // clean slate
//	    agent.EmitEvent(PlanProposalApproved{Steps})
//
//	model calls plan(action="start",  index=N) → StepChanged(snap, N-1)
//	    if bridge.batch && currentIdx >= 0 {
//	        policy.SetPreApproved(true)
//	    } else {
//	        policy.SetPreApproved(false)
//	    }
//	    agent.EmitEvent(PlanStepUpdated{snap, currentIdx})
//
//	model calls plan(action="complete", index=N) → StepChanged(snap, -1)
//	    policy.SetPreApproved(false)
//	    agent.EmitEvent(PlanStepUpdated{snap, -1})
//
//	user picks "cancel"                      → Cancelled()
//	    bridge.batch = false
//	    plan.Clear()                      // emits PlanStepUpdated(nil, -1)
//	    policy.SetPreApproved(false)
//	    agent.EmitEvent(PlanProposalCancelled{})
//
//	user Esc on the stream / /plan-off       → policy.SetPreApproved(false)
//	    (called from the TUI cancellation path, not via this bridge)
//
// The mutex on `batch` serialises the rare cross-goroutine update:
// proposeAnswer (Ask goroutine) writes batch; StepChanged (tool
// dispatch goroutine) reads it.

type planBridge struct {
	mu    sync.Mutex
	batch bool
	// lastApproved is the step list the user most recently approved
	// (any flavour). Used by IsDuplicateOfLastApproved to suppress
	// byte-identical re-proposals — the model occasionally loops and
	// asks the user the same picker question after an adjust round
	// resolved nothing.
	lastApproved []string
	// lastArtifactPath / lastArtifactErr capture the outcome of the
	// most recent WriteArtifact call. Read by LastArtifactStatus
	// (propose.ArtifactReporter) so the tool result can surface the
	// path on success or a "(note: …)" line on failure. PRD §8.7.
	lastArtifactPath string
	lastArtifactErr  error
	// pendingProblem / pendingWhyNow / pendingDoneWhen /
	// pendingReviewedBy carry the propose args from the in-flight
	// Execute call into Approved, which only receives steps and
	// batch. OnProposeStart + OnProposeCompletion fill them before
	// sink.Approved fires; Approved clears them after consuming.
	pendingProblem    string
	pendingWhyNow     string
	pendingDoneWhen   []string
	pendingReviewedBy string
	getAgent          func() *agent.Agent
	planTool          *plantool.Tool
	policy            *permission.Policy
	// sessionID returns the live session ID (closure so /compact / /reset
	// rebuilds don't strand a stale value). Returns "" in --no-save
	// mode; we use that signal to skip artifact writes entirely.
	sessionID func() string
	// projectAbs is the resolved CWD at startup. Fixed for the
	// program's lifetime — /reset doesn't move the project root.
	projectAbs string
	// artifactEnabled gates the write. False in --no-save (user
	// explicitly wants ephemeral; honour that for artifacts too,
	// even though they live outside the session dir).
	artifactEnabled bool
	// now is the clock injected for testability. nil → time.Now.
	now func() time.Time
}

// RestoreApprovalMode installs the approval mode recovered from the
// transcript on resume (plan.ReconstructFromTranscript's batch return).
// The mode is transcript-only state: planBridge.batch is a runtime
// field with no persistence, so before this call a resumed session
// always believed the user had picked the plain "ask per call"
// approval and the plan(start) re-arm path silently disarmed instead
// of arming the per-step gate (c_n2k6qi §6.2).
//
// Deliberately NOT a separate persistence field on Session: the batch
// tag already rides the propose result in the transcript, and adding a
// header bool would be a second source of truth for the same fact.
func (b *planBridge) RestoreApprovalMode(batch bool) {
	b.mu.Lock()
	b.batch = batch
	b.mu.Unlock()
}

func (b *planBridge) Approved(steps []string, batch bool) {
	b.mu.Lock()
	b.batch = batch
	// Snapshot the approved steps for duplicate detection on
	// subsequent propose calls. We copy so a later mutation by
	// callers can't shift our reference.
	b.lastApproved = append(b.lastApproved[:0], steps...)
	// Consume the in-flight propose context (set by OnProposeStart +
	// OnProposeCompletion). We hold these only until the next propose,
	// so consuming here avoids stale data leaking into the next adjust
	// loop.
	problem := b.pendingProblem
	whyNow := b.pendingWhyNow
	doneWhen := append([]string(nil), b.pendingDoneWhen...)
	reviewedBy := b.pendingReviewedBy
	b.pendingProblem = ""
	b.pendingWhyNow = ""
	b.pendingDoneWhen = nil
	b.pendingReviewedBy = ""
	enabled := b.artifactEnabled
	projectAbs := b.projectAbs
	now := b.now
	sessionFn := b.sessionID
	b.mu.Unlock()

	// Write artifact (best-effort). Failure path: stash on the
	// bridge, print to stderr; propose.LastArtifactStatus surfaces
	// it to the model in the next breath. Success: stash path.
	if enabled && problem != "" && len(steps) > 0 {
		if now == nil {
			now = time.Now
		}
		sid := ""
		if sessionFn != nil {
			sid = sessionFn()
		}
		path, werr := plantool.WriteArtifact(plantool.ArtifactMetadata{
			Problem:        problem,
			DoneWhen:       doneWhen,
			Steps:          steps,
			WhyNow:         whyNow,
			ReviewedBy:     reviewedBy,
			SessionID:      sid,
			ProjectAbsPath: projectAbs,
			Batch:          batch,
			ApprovedAt:     now(),
		})
		b.mu.Lock()
		b.lastArtifactPath = path
		b.lastArtifactErr = werr
		b.mu.Unlock()
		if werr != nil {
			fmt.Fprintln(os.Stderr, "plan artifact:", werr)
		}
	} else {
		// Either disabled or insufficient context — clear any prior
		// status so a stale path doesn't get echoed into this turn's
		// result.
		b.mu.Lock()
		b.lastArtifactPath = ""
		b.lastArtifactErr = nil
		b.mu.Unlock()
	}

	if b.policy != nil {
		b.policy.SetPreApproved(false)
	}
	if b.planTool != nil {
		b.planTool.Seed(steps)
	}
	b.getAgent().EmitEvent(agent.PlanProposalApproved{Steps: steps})
}

// OnProposeStart implements propose.ContextReceiver. Snapshots the
// in-flight propose args so Approved can stuff them into the artifact
// write. AdjustRequested / Cancelled / duplicate-short-circuit don't
// consume them — they get overwritten by the next OnProposeStart or
// cleared on Cancelled.
func (b *planBridge) OnProposeStart(problem string, _ []string, whyNow string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pendingProblem = problem
	b.pendingWhyNow = whyNow
}

// OnProposeCompletion implements propose.CompletionReceiver — the
// sibling of ContextReceiver added when done_when/reviewed_by landed.
// Snapshotting here (rather than widening OnProposeStart) keeps every
// existing ContextReceiver implementation compiling unchanged.
func (b *planBridge) OnProposeCompletion(doneWhen []string, reviewedBy string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pendingDoneWhen = append(b.pendingDoneWhen[:0], doneWhen...)
	b.pendingReviewedBy = reviewedBy
}

// LastArtifactStatus implements propose.ArtifactReporter. Reset
// during Approved (either to a real path/err pair or to zeros),
// returned verbatim here for propose to fold into the tool result.
func (b *planBridge) LastArtifactStatus() (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastArtifactPath, b.lastArtifactErr
}

func (b *planBridge) AdjustRequested(feedback string) {
	b.getAgent().EmitEvent(agent.PlanProposalAdjustRequested{Feedback: feedback})
}

func (b *planBridge) Cancelled() {
	b.mu.Lock()
	b.batch = false
	b.lastApproved = nil
	b.pendingProblem = ""
	b.pendingWhyNow = ""
	b.pendingDoneWhen = nil
	b.pendingReviewedBy = ""
	// Clear artifact status too — a previous turn's success path
	// should not leak into the cancel's tool result (currently
	// cancel doesn't call LastArtifactStatus, but defensive).
	b.lastArtifactPath = ""
	b.lastArtifactErr = nil
	b.mu.Unlock()
	if b.policy != nil {
		b.policy.SetPreApproved(false)
	}
	if b.planTool != nil {
		b.planTool.Clear()
	}
	b.getAgent().EmitEvent(agent.PlanProposalCancelled{})
}

// IsDuplicateOfLastApproved implements propose.DuplicateChecker. The
// comparison is order-sensitive (re-ordering steps IS a real change)
// and whitespace-trimmed (extra trailing spaces are not). Returns
// false when no plan has been approved yet — there's nothing to
// dedupe against, so the first propose always shows the picker.
func (b *planBridge) IsDuplicateOfLastApproved(steps []string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lastApproved) == 0 || len(b.lastApproved) != len(steps) {
		return false
	}
	for i, s := range steps {
		if strings.TrimSpace(s) != strings.TrimSpace(b.lastApproved[i]) {
			return false
		}
	}
	return true
}

// ProgressSummary implements propose.ProgressReporter. Returns a
// one-line summary of step statuses suitable for embedding in the
// adjust-path tool result — the model carries this into the next
// proposal so completed work isn't redone. Empty string when no
// plan is loaded (nothing to summarise).
func (b *planBridge) ProgressSummary() string {
	if b.planTool == nil {
		return ""
	}
	steps, _ := b.planTool.Snapshot()
	if len(steps) == 0 {
		return ""
	}
	var done, inProgress, pending, skipped []string
	for i, s := range steps {
		idx := fmt.Sprintf("%d", i+1)
		switch s.Status {
		case plantool.StatusCompleted:
			done = append(done, idx)
		case plantool.StatusInProgress:
			inProgress = append(inProgress, idx)
		case plantool.StatusSkipped:
			skipped = append(skipped, idx)
		default:
			pending = append(pending, idx)
		}
	}
	parts := []string{}
	if len(done) > 0 {
		parts = append(parts, "completed: "+strings.Join(done, ","))
	}
	if len(inProgress) > 0 {
		parts = append(parts, "in_progress: "+strings.Join(inProgress, ","))
	}
	if len(skipped) > 0 {
		parts = append(parts, "skipped: "+strings.Join(skipped, ","))
	}
	if len(pending) > 0 {
		parts = append(parts, "pending: "+strings.Join(pending, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "."
}

func (b *planBridge) StepChanged(snapshot []plantool.Step, currentIdx int) {
	b.mu.Lock()
	batch := b.batch
	b.mu.Unlock()

	if b.policy != nil {
		// Gate flips only when batch mode is on. currentIdx >= 0
		// means a step is in progress; -1 means none. In non-batch
		// approval the gate stays closed regardless.
		if batch && currentIdx >= 0 {
			b.policy.SetPreApproved(true)
		} else {
			b.policy.SetPreApproved(false)
		}
	}

	out := make([]agent.PlanStep, len(snapshot))
	for i, st := range snapshot {
		out[i] = agent.PlanStep{Text: st.Text, Status: string(st.Status)}
	}
	b.getAgent().EmitEvent(agent.PlanStepUpdated{Steps: out, CurrentIdx: currentIdx})
}
