// Package permission gates dangerous tool actions.
//
// The policy is two orthogonal axes:
//
//   - Preference — what the user has signed up for: Deny (no dangerous
//     actions), Ask (consult askFn per call), Yolo (allow everything).
//     This is the "user intent" axis, driven by --yolo / --deny flags
//     and the /yolo TUI toggle.
//
//   - Workflow — what ceremony the user has entered: None (normal
//     usage), PlanAnalyze (read-only investigation), PlanExecute (plan
//     approved, executing). This is the "workflow state" axis, driven
//     by /plan and the propose tool's approval event.
//
// Why two axes. Earlier versions packed both into a single `Mode` enum,
// which forced contradictions ("plan + yolo" was inexpressible) and
// invented orphan flags (preApproved hung off Policy with no clear
// owner). Splitting them lets Yolo + PlanAnalyze coexist (the
// workflow's read-only contract still holds — workflow trumps pref
// when in conflict) and gives preApproved a clear owner (it only
// makes sense inside WorkflowPlanExecute). See
// docs/prd/feature-permission-refactor.md for the full design.
//
// Denials are returned as plain errors so the agent can feed them back
// to the LLM as a tool result. The model then knows to ask the user
// instead of retrying blindly.
package permission

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// Kind is the category of a guarded action.
type Kind string

const (
	KindBash           Kind = "bash"
	KindWrite          Kind = "write"
	KindEdit           Kind = "edit"
	KindRead           Kind = "read"
	KindMemoryRemember Kind = "memory_remember"
	// KindSkillInstall guards the skill_commit tool — the moment a
	// staged skill package gets moved into ~/.seek/skills/<name>/.
	// Fetching to /tmp is not gated; only the actual install step is,
	// because that's the irreversible filesystem mutation that puts
	// model-influenced content on the user's machine.
	KindSkillInstall Kind = "skill_install"
	// KindGit is the read-only git tool. Both PlanAnalyze workflow
	// and ModeAsk allow it unconditionally — the tool's subcommand
	// whitelist enforces the read-only guarantee at construction.
	KindGit Kind = "git"
)

// Action describes one attempt to perform a guarded operation. The
// fields here drive Check's gating decision. Anything that exists
// purely so the TUI can render a richer y/N prompt lives in the
// nested Display struct — Check never reads it.
type Action struct {
	Kind    Kind
	Path    string // for write/edit
	Command string // for bash; only first ~80 chars are shown in errors

	// ReadOnly is set by the bash tool when the command matches the
	// read-only inspector whitelist (go vet / go list / npm ls / …)
	// and contains no shell metacharacters. The plan-analyze workflow
	// gate honours this flag: a ReadOnly bash call is allowed even in
	// the otherwise-read-only workflow so the model can answer "does
	// this still compile?" / "what's in the module graph?" without
	// leaving the substate. Other workflows / prefs ignore the flag
	// — PlanExecute and Ask route bash through the askFn callback;
	// Yolo allows everything; Deny denies everything. See
	// internal/tools/bash/readonly.go for the whitelist and metachar
	// lockout.
	ReadOnly bool

	// Display carries optional rendering hints used by the TUI's y/N
	// approval prompt. Check NEVER consults these fields — they're
	// pass-through data for the askFn callback. Tools that surface
	// richer-than-path/command context (edit diff, memory tagline,
	// skill source) populate the relevant sub-fields; tools that
	// don't leave Display zero-valued.
	Display Display
}

// Display is the bag of "what does the user see in the y/N prompt?"
// fields. Lives separate from Action's gating data so a reader of
// permission.go can tell at a glance which fields drive decisions
// (Action) vs. which only drive rendering (Display).
//
// Adding a new tool that needs richer y/N text: append a field here
// rather than to Action. Removing one: ripple through TUI's
// renderApprovalPrompt only.
type Display struct {
	// Diff is an optional unified diff string populated by the edit
	// tool. When non-empty the TUI renders it alongside the y/N
	// prompt so the user can see exactly what will change.
	Diff string

	// MemoryName / MemoryTagline are populated by memory_remember so
	// the TUI can render "save memory: NAME — TAGLINE" alongside the
	// y/N prompt. The full content body is intentionally NOT here —
	// name + tagline is enough decision context, and content can be
	// paragraphs.
	MemoryName    string
	MemoryTagline string

	// SkillName / SkillSource / SkillTarget are populated by
	// skill_commit so the TUI can render "install <name> from
	// <source> to <target>?" — the three things the user actually
	// needs to decide on. Body / scripts content stays out: the
	// model has already inspected them via read/grep before getting
	// here, and dumping the full body into the approval prompt would
	// push it off-screen.
	SkillName   string
	SkillSource string
	SkillTarget string
}

// ApprovalRequest is what the TUI consumes when Preference == PrefAsk
// needs a user answer. The host (cmd/seek) glues the policy's askFn to
// a channel of these and the TUI reads from that channel.
//
// Reply MUST receive exactly one value — true to allow, false to deny.
// askFn blocks on Reply, so a missing reply hangs the agent.
type ApprovalRequest struct {
	Action Action
	Reply  chan<- bool
}

// Preference is the user's standing posture toward dangerous actions.
// It's the "how strict am I" axis — chosen once at startup, toggled
// via /yolo, persisted in session JSONL.
type Preference int

const (
	// PrefDeny refuses dangerous actions outright. The default for
	// non-interactive launches (print mode), since there's no user to
	// ask. Returns a denial message instructing the model to surface
	// the request to a human.
	PrefDeny Preference = iota
	// PrefAsk consults the askFn callback for each dangerous action.
	// The default for the interactive TUI.
	PrefAsk
	// PrefYolo permits every action. Set by --yolo or by an "always
	// approve" answer at an inline prompt.
	PrefYolo
)

// Workflow is the ceremony the user has currently entered. It's
// orthogonal to Preference: a workflow imposes its own constraints
// (e.g. PlanAnalyze is read-only regardless of PrefYolo).
type Workflow int

const (
	// WorkflowNone is normal usage — no workflow ceremony, the
	// preference fully drives gating.
	WorkflowNone Workflow = iota
	// WorkflowPlanAnalyze is the read-only investigation substate of
	// /plan, set when the user enters /plan mode. Writes / bash
	// (other than read-only inspectors) / memory writes / skill
	// installs are denied. Reads / git / read-only bash are allowed.
	WorkflowPlanAnalyze
	// WorkflowPlanExecute is the post-approval substate of /plan,
	// set by the TUI on PlanProposalApproved. Workflow constraints
	// drop (pref takes over), but per-step batch pre-approval may
	// short-circuit individual writes via preApproved.
	WorkflowPlanExecute
)

// Policy is the per-process permission policy. Construct via New.
//
// Concurrency: the pref / workflow / askFn / preApproved fields can be
// updated at runtime from the TUI goroutine while tool dispatch may
// concurrently be in Check on the agent goroutine. The mutex serialises
// those transitions so concurrent Check + Set* is race-free. `cwd` is
// set at construction and never changes; the mutex covers it anyway
// because it's cheap and avoids a footgun if that assumption changes.
//
// preApproved is the per-step batch-approval flag used by the
// WorkflowPlanExecute substate. When the user approves a propose()
// call with "auto-approve writes per step", the `plan` tool sink
// toggles this flag true on plan(action="start", …) and false on
// plan(action="complete"|"skip", …). Check, while in WorkflowPlanExecute,
// treats preApproved as a fast-path "yes" for KindWrite/KindEdit/KindBash,
// skipping the askFn callback. Esc / Ctrl+C cancellation paths AND
// /plan-off explicitly reset preApproved so a half-finished step
// never leaves the gate unlocked across the next user prompt.
// SetWorkflow ALSO resets preApproved — moving between workflows
// always invalidates step state.
type Policy struct {
	mu       sync.RWMutex
	pref     Preference
	workflow Workflow
	cwd      string // absolute path; used to decide "inside vs outside"
	askFn    func(Action) bool

	// exec holds WorkflowPlanExecute-specific transient state. Its
	// fields have no meaning under any other workflow; SetWorkflow
	// zeros it on every transition (defense in depth). Grouped as
	// a sub-struct so a reader can tell at a glance that these
	// fields are workflow-scoped, not top-level Policy state.
	exec workflowExecState

	// onDestructive is the v3 checkpoint hook (PRD docs/prd/
	// feature-checkpoint.md §5). When Check decides a destructive
	// action (write / edit / mutating bash) will go through, it
	// fires this callback BEFORE returning nil. The callback is
	// the safety net's only entry point into the permission gate.
	// Wired by cmd/seek/main.go after the Manager is constructed.
	//
	// Why a callback rather than an interface coupling permission
	// to internal/checkpoint: keeps permission's import surface
	// minimal and lets tests use a no-op without dragging in the
	// real checkpoint machinery.
	onDestructive func(a Action)
}

// workflowExecState collects all fields that only make sense while
// Policy.workflow == WorkflowPlanExecute. Today it's just preApproved
// (the per-step batch-approval gate); future PlanExecute extensions
// (e.g. step-count limits, time budgets) belong here too.
type workflowExecState struct {
	// preApproved is the per-step batch-approval flag. True between
	// plan(start=N) and plan(complete=N) when the user approved a
	// plan with "auto-approve writes per step". Check honours it as
	// a fast-path for bash/write/edit WHILE workflow is PlanExecute;
	// other workflows ignore it. Cleared by plan(complete) /
	// plan(skip), Esc cancellation, /plan-off, and SetWorkflow.
	preApproved bool
}

// New returns a Policy. cwd should be the project root (typically
// os.Getwd() at start-up). Initial workflow is WorkflowNone; callers
// who start in plan mode (--plan flag) should follow with
// SetWorkflow(WorkflowPlanAnalyze).
func New(cwd string, pref Preference) (*Policy, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("permission: resolve cwd: %w", err)
	}
	return &Policy{pref: pref, cwd: abs}, nil
}

// SetAskFn registers a callback consulted for each dangerous action
// when the policy is in PrefAsk. The callback is expected to BLOCK
// until the user answers (or the surrounding context cancels). It is
// called from the tool's goroutine, NOT the TUI's — so the callback
// can safely use blocking channel ops to coordinate with the UI.
func (p *Policy) SetAskFn(fn func(Action) bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.askFn = fn
}

// SetOnDestructive registers the v3 checkpoint hook. Called from
// Check IMMEDIATELY before returning nil for destructive actions
// (write / edit / mutating bash). Idempotent / cheap: the callback
// itself handles "have I already checkpointed this turn?".
//
// Concurrency: writer side under p.mu (mirrors SetAskFn); reader
// side snapshots under RLock in Check, so a hot-swap mid-turn is
// safe.
func (p *Policy) SetOnDestructive(fn func(a Action)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onDestructive = fn
}

// Pref returns the current preference.
func (p *Policy) Pref() Preference {
	if p == nil {
		return PrefDeny
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pref
}

// SetPref updates the preference. Does NOT touch workflow — the two
// axes are independent. TUI's cmdYolo enforces mutual exclusion at
// the UI layer if desired.
func (p *Policy) SetPref(pref Preference) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pref = pref
}

// Workflow returns the current workflow.
func (p *Policy) Workflow() Workflow {
	if p == nil {
		return WorkflowNone
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.workflow
}

// SetWorkflow updates the workflow. Any workflow transition resets
// preApproved — the per-step batch-approval flag only makes sense
// within a single PlanExecute episode, so moving away from (or even
// re-entering) plan-execute always invalidates the previous step
// state. This is the "default safe" invariant.
func (p *Policy) SetWorkflow(w Workflow) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.workflow = w
	// Zero ALL workflow-execute state on transition, not just the
	// preApproved field. Future fields added to workflowExecState
	// inherit the safe-by-default reset behaviour without anyone
	// having to remember to extend this line.
	p.exec = workflowExecState{}
}

// SetPreApproved toggles the per-step batch-approval flag. Called
// from the plan tool's sink in cmd/seek when (a) a propose-approved
// batch plan enters a step (plan(start=…)) → true, and (b) the step
// completes or skips → false. Also reset on Esc / Ctrl+C from the
// TUI and on /plan-off, so a half-finished step does not silently
// pre-approve the next user prompt's writes.
//
// The flag is only consulted when Workflow == WorkflowPlanExecute;
// setting it under any other workflow is a no-op effectively (Check
// only honours it inside that workflow).
func (p *Policy) SetPreApproved(b bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exec.preApproved = b
}

// PreApproved reports the current per-step pre-approval flag — read
// by tests; production code goes through Check.
func (p *Policy) PreApproved() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exec.preApproved
}

// Cwd returns the policy's working directory (set at construction;
// immutable post-construction). Added in v5 to support Spawn —
// subagents under isolation:"worktree" run at the worktree path
// rather than the parent's cwd, so the spawn API must thread cwd
// through explicitly rather than inheriting implicitly.
func (p *Policy) Cwd() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cwd
}

// String returns a human-readable name for the Preference value. Used
// in Spawn error messages and tests; production code paths compare
// against the named constants directly.
func (p Preference) String() string {
	switch p {
	case PrefDeny:
		return "deny"
	case PrefAsk:
		return "ask"
	case PrefYolo:
		return "yolo"
	}
	return fmt.Sprintf("Preference(%d)", int(p))
}

// String returns a human-readable name for the Workflow value.
func (w Workflow) String() string {
	switch w {
	case WorkflowNone:
		return "none"
	case WorkflowPlanAnalyze:
		return "plan-analyze"
	case WorkflowPlanExecute:
		return "plan-execute"
	}
	return fmt.Sprintf("Workflow(%d)", int(w))
}

// Restriction tightens a Policy when spawning a subagent. Each field
// is a nil-pointer "inherit from parent" by default; setting the
// pointer pins that axis. Spawn enforces monotonic-only restriction
// (values may move toward MORE restrictive but never less), so even
// caller mistakes can't accidentally elevate a child's permissions.
//
// See docs/prd/feature-subagent.md §3.5 for the full rule set.
type Restriction struct {
	// Pref pins the child's Preference. nil = inherit parent's pref.
	// Permitted transitions:
	//   parent PrefDeny  → only PrefDeny
	//   parent PrefAsk   → PrefDeny | PrefAsk (never PrefYolo)
	//   parent PrefYolo  → any pref
	Pref *Preference

	// Workflow pins the child's Workflow. nil = inherit parent's
	// workflow. Permitted transitions:
	//   parent PlanAnalyze  → MUST be PlanAnalyze (terminal read-only)
	//   parent PlanExecute  → PlanExecute | PlanAnalyze
	//   parent None         → any workflow
	Workflow *Workflow
}

// Spawn returns a new Policy for a subagent rooted at the given cwd.
//
// cwd is REQUIRED — subagents under isolation:"worktree" run at the
// worktree path, not the parent's cwd, so it cannot be inherited
// implicitly. For isolation:"none" the caller passes parent.Cwd()
// explicitly. (Policy.cwd is set at construction and never changes
// post-hoc; that invariant is preserved by always taking cwd on
// Spawn.)
//
// Returns an error if Restriction would loosen any axis; never
// returns a Policy looser than the receiver. The error message names
// both parent and intended child values so the orchestrator can
// surface a clear hint to the caller.
//
// IMPORTANT — preApproved is NEVER inherited. Even when the child
// stays in WorkflowPlanExecute, the per-step batch-approval gate
// must be re-established inside the child's own plan flow. This
// prevents a parent step's auto-approve window from silently
// extending into a fresh subagent (docs/prd/feature-subagent.md
// §3.5).
//
// askFn / onDestructive are NOT carried over — the orchestrator wires
// them on the child after construction (typically wrapping the parent
// askFn with a "[subagent: <description>]" prefix decorator;
// onDestructive points to the child's own checkpoint manager). The
// child gets its own *sync.RWMutex so concurrent operations on parent
// and child never block each other.
func (p *Policy) Spawn(cwd string, r Restriction) (*Policy, error) {
	if p == nil {
		return nil, fmt.Errorf("permission: Spawn on nil parent")
	}
	p.mu.RLock()
	parentPref := p.pref
	parentWorkflow := p.workflow
	p.mu.RUnlock()

	childPref := parentPref
	if r.Pref != nil {
		childPref = *r.Pref
	}
	childWorkflow := parentWorkflow
	if r.Workflow != nil {
		childWorkflow = *r.Workflow
	}

	// Monotonic-only on Preference. Since the enum is ordered
	// Deny=0 < Ask=1 < Yolo=2 with strictness decreasing, "loosening"
	// is simply childPref > parentPref.
	if childPref > parentPref {
		return nil, fmt.Errorf("permission: Spawn would loosen Preference (parent=%s, child=%s)", parentPref, childPref)
	}

	// Monotonic-only on Workflow. Unlike Preference, this axis is
	// not totally ordered — PlanAnalyze and PlanExecute are siblings
	// under None, and PlanAnalyze is the strictly-tighter substate
	// of PlanExecute. Enumerate explicitly rather than using integer
	// comparison.
	switch parentWorkflow {
	case WorkflowPlanAnalyze:
		if childWorkflow != WorkflowPlanAnalyze {
			return nil, fmt.Errorf("permission: Spawn under %s must stay %s (got %s)", parentWorkflow, parentWorkflow, childWorkflow)
		}
	case WorkflowPlanExecute:
		if childWorkflow != WorkflowPlanExecute && childWorkflow != WorkflowPlanAnalyze {
			return nil, fmt.Errorf("permission: Spawn under %s cannot escape to %s", parentWorkflow, childWorkflow)
		}
	case WorkflowNone:
		// Any child workflow allowed.
	}

	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("permission: Spawn resolve cwd: %w", err)
	}

	// Construct a fresh Policy. Independent mu so parent and child
	// can be operated on concurrently. exec is zero — preApproved is
	// not inherited (see method doc).
	return &Policy{
		pref:     childPref,
		workflow: childWorkflow,
		cwd:      abs,
	}, nil
}

// ErrDenied is returned when an action is blocked by policy. Callers should
// surface its message verbatim to the LLM — it includes the specific reason
// and the override instructions ("run with --yolo …").
var ErrDenied = errors.New("permission denied")

// Check evaluates an Action against the Policy. Nil = allowed.
//
// Resolution order:
//  1. Workflow gate — if the current workflow imposes a hard
//     constraint on this Action, it wins regardless of pref. This is
//     how PlanAnalyze stays read-only even when pref is Yolo.
//  2. PlanExecute fast-path — if workflow is PlanExecute and
//     preApproved is true, writes/edits/bash auto-pass (per-step
//     batch approval granted at propose() time).
//  3. Preference gate — Yolo allows all; Deny rejects dangerous;
//     Ask consults askFn for dangerous and allows safe.
func (p *Policy) Check(a Action) error {
	if p == nil {
		return fmt.Errorf("%w: no policy configured", ErrDenied)
	}
	// Snapshot the mutable fields under the lock and release before
	// any potentially slow work (isWithin does I/O via filepath.Abs;
	// askFn blocks on the user). Holding the lock across either would
	// turn a brief read-side lock into a session-long write barrier.
	p.mu.RLock()
	pref := p.pref
	workflow := p.workflow
	cwd := p.cwd
	askFn := p.askFn
	preApproved := p.exec.preApproved
	onDestructive := p.onDestructive
	p.mu.RUnlock()

	// 0. ReadOnly bash short-circuit. The bash tool sets a.ReadOnly
	//    only when the command matches its strict whitelist (`go vet`,
	//    `npm ls`, `which`, etc.) AND has zero shell metacharacters
	//    (`;`, `|`, `&&`, `>`, …). By construction such a command has
	//    no side effects, so honour the flag regardless of workflow
	//    or pref — saves the user from y/N prompts on `go vet ./...`
	//    in Ask mode, and lets PrefDeny (print mode) still run safe
	//    inspectors. The flag is advisory-from-the-tool: only the
	//    bash tool's readonly.go is the authorised setter; no other
	//    Kind should ever set it.
	if a.Kind == KindBash && a.ReadOnly {
		return nil
	}

	var decision error
	// 1. Workflow dispatch. PlanAnalyze is TERMINAL — its rule set is
	//    a complete decision (every Kind either allowed or denied,
	//    no fall-through). PlanExecute is non-terminal: it just adds
	//    the preApproved fast-path before falling through to the
	//    pref gate. WorkflowNone falls straight to the pref gate.
	//
	//    Workflow runs first because workflow ceremonies are user-
	//    chosen safety boundaries that trump pref. PrefYolo +
	//    PlanAnalyze MUST still be read-only — that's plan mode's
	//    raison d'être.
	terminal := false
	switch workflow {
	case WorkflowPlanAnalyze:
		decision = planAnalyzeGate(a, cwd)
		terminal = true
	case WorkflowPlanExecute:
		// preApproved fast-path: per-step batch approval granted at
		// propose() time. Bash / write / edit auto-pass while the
		// flag is set. The user retained scope-level veto at the
		// propose() picker and per-step boundary control via plan
		// (start) / plan(complete).
		if preApproved {
			switch a.Kind {
			case KindBash, KindWrite, KindEdit:
				decision = nil
				terminal = true
			}
		}
		// fall through to pref gate when not pre-approved
	}

	if !terminal {
		// 2. Preference gate. Reached for WorkflowNone, or PlanExecute
		//    where preApproved didn't short-circuit.
		decision = prefGate(pref, a, cwd, askFn)
	}

	// 3. Checkpoint hook. Fire ONLY on success and ONLY for
	//    destructive actions. Read / git / read-only bash are out of
	//    scope (read-only bash already short-circuited at step 0).
	//    The Manager owns the "one per turn" gate — we just fire
	//    every time and let it decide. Wrapped in a recover'd helper
	//    so a buggy hook can't take down the permission gate.
	if decision == nil && onDestructive != nil && isDestructiveAction(a) {
		safeFireDestructive(onDestructive, a)
	}
	return decision
}

// isDestructiveAction is the in-package mirror of the
// `checkpoint.isDestructive` decision, kept here so permission
// doesn't import checkpoint (the dependency would invert).
func isDestructiveAction(a Action) bool {
	switch a.Kind {
	case KindWrite, KindEdit:
		return true
	case KindBash:
		return !a.ReadOnly
	}
	return false
}

// safeFireDestructive runs the registered hook with a panic guard.
// The hook is best-effort: the safety net should never become a
// reliability hazard. recover()'d panics are silently swallowed
// here; the surfacing happens via the hook's own Sink.Warn.
func safeFireDestructive(fn func(Action), a Action) {
	defer func() {
		_ = recover()
	}()
	fn(a)
}

// planAnalyzeGate is the read-only constraint set for plan-analyze:
// reads / git / read-only bash allowed; everything else hard-rejected
// with a model-readable hint.
func planAnalyzeGate(a Action, cwd string) error {
	switch a.Kind {
	case KindRead:
		if a.Path == "" {
			return fmt.Errorf("%w: %s requires a path", ErrDenied, a.Kind)
		}
		inside, err := isWithin(cwd, a.Path)
		if err != nil {
			return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
		}
		if !inside {
			return fmt.Errorf("%w: plan mode: %s outside working directory %q",
				ErrDenied, a.Kind, cwd)
		}
		return nil
	case KindBash:
		// ReadOnly bash already short-circuited at the top of Check
		// (step 0). If we reach here, the command isn't whitelisted
		// or contained shell metachars — deny with the standard
		// guidance pointing at alternatives.
		return fmt.Errorf("%w: plan mode: bash is not allowed for this command — explore with read/grep/list_dir/git, or run a read-only inspector (go vet, go list, npm ls, …) which is whitelisted",
			ErrDenied)
	case KindGit:
		// Git tool is read-only by construction (subcommand whitelist
		// enforced at tool layer). Plan-analyze allows it so the
		// model can inspect history / diffs / blame while producing
		// the plan.
		return nil
	case KindWrite, KindEdit:
		return fmt.Errorf("%w: plan mode: %s is not allowed — produce a plan in your response instead",
			ErrDenied, a.Kind)
	case KindMemoryRemember:
		return fmt.Errorf("%w: plan mode: memory_remember is not allowed",
			ErrDenied)
	case KindSkillInstall:
		return fmt.Errorf("%w: plan mode: skill_install is not allowed — plan mode is read-only",
			ErrDenied)
	default:
		return fmt.Errorf("%w: plan mode: unknown action kind %q", ErrDenied, a.Kind)
	}
}

// prefGate is the preference-driven gate, run AFTER the workflow gate
// has had its say. It handles the classic Yolo / Ask / Deny semantics.
func prefGate(pref Preference, a Action, cwd string, askFn func(Action) bool) error {
	if pref == PrefYolo {
		return nil
	}

	// Is this action even dangerous? Safe actions return nil without
	// consulting askFn.
	dangerous := false
	switch a.Kind {
	case KindBash:
		dangerous = true
	case KindWrite, KindEdit:
		if a.Path == "" {
			return fmt.Errorf("%w: %s requires a path", ErrDenied, a.Kind)
		}
		inside, err := isWithin(cwd, a.Path)
		if err != nil {
			return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
		}
		if !inside {
			dangerous = true
		}
	case KindRead:
		if a.Path == "" {
			return fmt.Errorf("%w: %s requires a path", ErrDenied, a.Kind)
		}
		inside, err := isWithin(cwd, a.Path)
		if err != nil {
			return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
		}
		if !inside {
			dangerous = true
		}
	case KindMemoryRemember:
		if a.Display.MemoryName == "" {
			return fmt.Errorf("%w: memory_remember requires a name", ErrDenied)
		}
		dangerous = true
	case KindSkillInstall:
		if a.Display.SkillName == "" {
			return fmt.Errorf("%w: skill_install requires a skill name", ErrDenied)
		}
		dangerous = true
	case KindGit:
		// Read-only by construction (tool layer enforces a subcommand
		// whitelist). Treated as safe.
		return nil
	default:
		return fmt.Errorf("%w: unknown action kind %q", ErrDenied, a.Kind)
	}

	if !dangerous {
		return nil
	}

	// Dangerous: ask if we can, otherwise deny.
	if pref == PrefAsk && askFn != nil {
		if askFn(a) {
			return nil
		}
		return fmt.Errorf("%w: user declined %s", ErrDenied, a.Kind)
	}

	switch a.Kind {
	case KindBash:
		return fmt.Errorf("%w: bash is gated; re-run seek with --yolo, or run the command yourself: %s",
			ErrDenied, shorten(a.Command, 80))
	case KindMemoryRemember:
		return fmt.Errorf("%w: memory_remember %q is gated; re-run seek with --yolo, or save the entry yourself",
			ErrDenied, a.Display.MemoryName)
	case KindSkillInstall:
		return fmt.Errorf("%w: skill_install %q from %q is gated; re-run seek with --yolo, or install the skill yourself with `seek skill install %s`",
			ErrDenied, a.Display.SkillName, a.Display.SkillSource, a.Display.SkillSource)
	default:
		return fmt.Errorf("%w: %s on %q is outside the working directory %q — re-run with --yolo to allow",
			ErrDenied, a.Kind, a.Path, cwd)
	}
}

// CWD returns the resolved working directory the policy is anchored to.
func (p *Policy) CWD() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cwd
}

// Resolve turns a tool's path argument into the path to actually touch:
// absolute paths are cleaned and returned; RELATIVE paths are anchored to
// this policy's CWD — the project root for the main agent, the WORKTREE
// for an isolation:"worktree" subagent — NOT the process working
// directory. read/edit/write MUST use this; otherwise a worktree subagent
// resolving a relative path (e.g. "README.md") against os.Getwd() edits
// the MAIN tree, silently breaking worktree isolation. (bash/git already
// pin cmd.Dir to CWD, so only the in-process file tools had this hole.)
func (p *Policy) Resolve(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if cwd := p.CWD(); cwd != "" {
		return filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// Yolo reports whether the policy's preference is Yolo. Kept as a
// compat helper for callers that just want the "are writes
// unrestricted?" boolean; new code can use Pref() directly.
func (p *Policy) Yolo() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pref == PrefYolo
}

// Plan reports whether the policy is in any plan workflow (analyze or
// execute). Kept as a compat helper for callers that just want the
// "is /plan on?" boolean — distinguishes analyze vs execute via
// Workflow().
func (p *Policy) Plan() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.workflow != WorkflowNone
}

// isWithin reports whether target resolves to a path inside root (inclusive
// of root itself). Both paths are made absolute and their symlinks are
// resolved before comparison so a symlink inside root that points outside
// is caught.
//
// For non-existent paths (e.g. a new file about to be created) we walk up
// the directory tree until finding an existing ancestor, resolve its
// symlinks, then append the non-existent suffix — preserving the guard.
func isWithin(root, target string) (bool, error) {
	// Resolve the root first so symlinks in root-level paths (e.g.
	// /var → /private/var on macOS) don't cause false denials.
	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, fmt.Errorf("resolve root %q: %w", root, err)
	}

	// Resolve symlinks in the target path, walking up if needed.
	resolved, err := resolveClosest(target)
	if err != nil {
		return false, err
	}
	absTarget, err := filepath.Abs(resolved)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil
	}
	if strings.HasPrefix(rel, "..") {
		return false, nil
	}
	// On non-Unix filesystems Rel can return paths like "..\foo". The
	// prefix check above covers that. Anything else is "inside".
	return true, nil
}

// resolveClosest resolves symlinks on the deepest existing ancestor of path,
// then appends the non-existent suffix. This handles both existing paths
// (full EvalSymlinks) and new paths (partial resolution up to the nearest
// existing directory).
func resolveClosest(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	// Walk up until we find a parent that exists.
	parent := filepath.Dir(path)
	if parent == path {
		// Reached the root without finding anything — return path as-is.
		return path, nil
	}
	resolvedParent, err := resolveClosest(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
