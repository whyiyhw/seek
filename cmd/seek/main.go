// Command seek is the DeepSeek-first coding agent CLI.
//
// M3 wires in cache.Tracker (session-level prefix-cache stats), pricing
// (off-peak tier awareness + per-call cost), and the Think tool that
// bridges the chat loop into V4-Flash thinking mode. Interactive TUI lands
// in M4; full think-then-chat skill arrives with skill loading in M5.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/checkpoint"
	"github.com/whyiyhw/seek/internal/checkpointcli"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/internal/hookscli"
	"github.com/whyiyhw/seek/internal/keymap"
	"github.com/whyiyhw/seek/internal/keyscli"
	"github.com/whyiyhw/seek/internal/suggester"
	"github.com/whyiyhw/seek/internal/sysprompt"
	"github.com/whyiyhw/seek/internal/mcpconfig"
	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/memorycli"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/projectmd"
	seekrpc "github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillcli"
	"github.com/whyiyhw/seek/internal/skillstats"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/tools"
	agenttool "github.com/whyiyhw/seek/internal/tools/agent"
	askusertool "github.com/whyiyhw/seek/internal/tools/askuser"
	"github.com/whyiyhw/seek/internal/tools/bash"
	"github.com/whyiyhw/seek/internal/tools/edit"
	"github.com/whyiyhw/seek/internal/tools/fimcomplete"
	gittool "github.com/whyiyhw/seek/internal/tools/git"
	"github.com/whyiyhw/seek/internal/tools/grep"
	"github.com/whyiyhw/seek/internal/tools/listdir"
	"github.com/whyiyhw/seek/internal/tools/mcptool"
	"github.com/whyiyhw/seek/internal/tools/memorytool"
	plantool "github.com/whyiyhw/seek/internal/tools/plan"
	"github.com/whyiyhw/seek/internal/tools/propose"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/internal/tools/skillinstall"
	"github.com/whyiyhw/seek/internal/tools/skilltool"
	"github.com/whyiyhw/seek/internal/tools/think"
	"github.com/whyiyhw/seek/internal/tools/webfetch"
	"github.com/whyiyhw/seek/internal/tools/write"
	"github.com/whyiyhw/seek/internal/tui"
	"github.com/whyiyhw/seek/internal/upgrade"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
	"github.com/whyiyhw/seek/pkg/llm/compatible"
	anthropicprov "github.com/whyiyhw/seek/pkg/llm/provider/anthropic"
	geminiprov "github.com/whyiyhw/seek/pkg/llm/provider/gemini"
	openaiprov "github.com/whyiyhw/seek/pkg/llm/provider/openai"

	"github.com/muesli/termenv"
)

// System prompt assembly (template literal, Compose, ModeLabel)
// lives in internal/sysprompt. Two callers consume it: cmd/seek (this
// file, root agent) and internal/subagent (v5 柱 G, subagents). PRD
// docs/prd/feature-subagent.md §3.6.1 specifies the composition rules
// and the byte-determinism invariant that keeps DeepSeek's prefix
// cache hot.

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

// ----- v3 checkpoint glue -----

// checkpointStderrSink routes Manager warnings to os.Stderr — the
// default for non-TUI launches. TUI swaps to its own buffered sink
// via Options when ready.
type checkpointStderrSink struct{}

func (checkpointStderrSink) Warn(m string) { fmt.Fprintln(os.Stderr, m) }

// checkpointSnapshotter is the adapter the write/edit tools see.
// Lives here so internal/tools/{write,edit} don't have to import
// internal/checkpoint (the dependency would also fight tests that
// pass nil snapshotters).
type checkpointSnapshotter struct{ m *checkpoint.Manager }

func (c checkpointSnapshotter) SnapshotFile(path, tool, callID string) error {
	return c.m.SnapshotFile(path, tool, callID)
}
func (c checkpointSnapshotter) FinaliseSnapshot(path string, after []byte) error {
	return c.m.FinaliseSnapshot(path, after)
}

// checkpointHook adapts *checkpoint.Manager to the hooks observer
// interfaces. Register once on the hooks.Registry; the dispatcher
// detects PreTurn / SessionEnd by interface satisfaction.
type checkpointHook struct{ m *checkpoint.Manager }

func (h *checkpointHook) OnPreTurn(ctx context.Context, ev hooks.PreTurnEvent) {
	h.m.OnPreTurn(ctx, nil)
}
func (h *checkpointHook) OnSessionEnd(ctx context.Context, ev hooks.SessionEndEvent) {
	h.m.OnSessionEnd(ctx, nil)
}

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
	// pendingProblem / pendingWhyNow carry the propose args from the
	// in-flight Execute call into Approved, which only receives steps
	// and batch. RecordProposalContext sets them before sink.Approved
	// fires; Approved clears them after consuming.
	pendingProblem string
	pendingWhyNow  string
	getAgent       func() *agent.Agent
	planTool       *plantool.Tool
	policy         *permission.Policy
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

func (b *planBridge) Approved(steps []string, batch bool) {
	b.mu.Lock()
	b.batch = batch
	// Snapshot the approved steps for duplicate detection on
	// subsequent propose calls. We copy so a later mutation by
	// callers can't shift our reference.
	b.lastApproved = append(b.lastApproved[:0], steps...)
	// Consume the in-flight propose context (set by OnProposeStart).
	// We hold these only until the next propose, so consuming here
	// avoids stale data leaking into the next adjust loop.
	problem := b.pendingProblem
	whyNow := b.pendingWhyNow
	b.pendingProblem = ""
	b.pendingWhyNow = ""
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
			Steps:          steps,
			WhyNow:         whyNow,
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

// ----- v5 柱 G subagent glue -----

// subagentRunnerOpts captures everything buildSubagentRunner needs
// to close over. Kept as a named struct (rather than a long
// parameter list) so future additions don't churn the call site.
type subagentRunnerOpts struct {
	client    *deepseek.Client
	provider  llm.Provider
	parentReg *tools.Registry
	hooksReg  *hooks.Registry
	maxTokens int
	maxTurns  int
	getModel  func() string
	getEffort func() string
}

// buildSubagentRunner returns the production subagent.Runner that
// wires a child pkg/agent.Agent and drains its events into the
// child cache.Tracker.
//
// LIMITATION (M11.0): the child agent reuses the parent's Tool
// instances by NAME (filtered via the template's ToolNames). Those
// instances hold the PARENT's permission.Policy at construction,
// so the child's tightened Policy (per PRD §2.3) is NOT consulted
// by individual tool Check() calls. Net safety comes from two
// places:
//
//   - The per-template ToolNames whitelist (Filter excludes
//     write/edit/bash from the explore subagent's child Registry,
//     so those tools literally aren't reachable).
//   - The system prompt instructing the model to honour the
//     subagent's intended workflow ("plan-analyze mode") even
//     when its tool surface technically allows mutation.
//
// Practical consequences:
//
//   - `explore` subagents are hard-safe (no mutating tools at all).
//   - `general-purpose` subagents see the parent's permission
//     level — which is INTENDED, since they inherit. No leak.
//   - `plan` subagents trust the system prompt to keep them out of
//     write/edit/bash. A future commit will reconstruct per-spawn
//     Registries with the child Policy threaded through so the
//     Workflow=PlanAnalyze restriction is hard-enforced at the
//     tool gate too. Tracked as M11.x in the v5 roadmap.
func buildSubagentRunner(opts subagentRunnerOpts) subagent.Runner {
	return func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
		// Filter parent registry by ToolNames. The Filter the
		// Manager performs already strips `agent` and `ask_user`
		// universally; here we just project parent's registered
		// instances onto the survivor name list.
		childReg := tools.New()
		for _, name := range job.ToolNames {
			if t := opts.parentReg.Lookup(name); t != nil {
				childReg.Add(t)
			}
		}

		// Build child agent. No InitialMessages (subagent gets a
		// fresh context per PRD §2.1); no PrepareMessages (suggester
		// disabled for subagents per PRD §3.5). Hooks ARE shared
		// with the parent — pre_tool deny / post_tool observer /
		// audit log all apply to subagent tool calls (PRD §5.3).
		sub, err := agent.New(agent.Config{
			Client:       opts.client,
			Provider:     opts.provider,
			Model:        opts.getModel(),
			Effort:       opts.getEffort(),
			SystemPrompt: job.SystemPrompt,
			Tools:        childReg,
			MaxTokens:    opts.maxTokens,
			MaxTurns:     opts.maxTurns,
			Hooks:        opts.hooksReg,
		})
		if err != nil {
			return subagent.RunnerResult{}, fmt.Errorf("subagent agent.New: %w", err)
		}

		// Drain events. Per-turn Usage rolls into the child
		// Tracker, which has already been AdoptChild'd by the
		// parent — so the cost shows up in the status bar
		// automatically without further plumbing here.
		var (
			totalUsage deepseek.Usage
			turnCount  int
			lastErr    error
		)
		events := sub.Prompt(ctx, job.UserPrompt)
		for ev := range events {
			switch e := ev.(type) {
			case agent.TurnEnd:
				job.Tracker.Record(e.Usage, opts.getModel(), pricing.CurrentTier(time.Now()))
			case agent.AgentEnd:
				totalUsage = e.Usage
				turnCount = e.Turns
			case agent.ErrorEvent:
				lastErr = e.Err
			}
		}
		if lastErr != nil {
			return subagent.RunnerResult{}, lastErr
		}
		if ctx.Err() != nil {
			return subagent.RunnerResult{}, ctx.Err()
		}

		// Extract summary: the most recent assistant message
		// without tool calls (i.e. the terminal assistant turn).
		// pkg/agent.Prompt only ends when this exists (or on
		// MaxTurns / ctx cancel / error — which we handled
		// above), so finding nothing here is a contract violation
		// — surface it as an error so Manager classifies it as
		// spawn_error rather than wrapping an empty Summary.
		msgs := sub.Messages()
		var summary string
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == deepseek.RoleAssistant && len(msgs[i].ToolCalls) == 0 {
				summary = msgs[i].Content
				break
			}
		}
		if summary == "" {
			return subagent.RunnerResult{}, fmt.Errorf("subagent produced no terminal assistant message")
		}

		return subagent.RunnerResult{
			Summary: summary,
			Tokens: subagent.Tokens{
				Prompt:     totalUsage.PromptTokens,
				Completion: totalUsage.CompletionTokens,
				CacheHit:   totalUsage.PromptCacheHitTokens,
			},
			Turns: turnCount,
		}, nil
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}
}

func run() error {
	// Skill subcommand surface (PRD v2 §5.1). Dispatched ahead of
	// every global flag and provider/session probe so `seek skill
	// install ./foo` doesn't need API keys, doesn't load sessions,
	// doesn't touch ~/.seek/projects/. The first positional arg is
	// the discriminator — flag.Parse() would already have consumed
	// it if we waited.
	if len(os.Args) >= 2 && os.Args[1] == "skill" {
		return skillcli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) >= 2 && os.Args[1] == "memory" {
		return memorycli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// v3 checkpoint subcommand (PRD docs/prd/feature-checkpoint.md §4.1).
	// Dispatched ahead of flag.Parse so the user can pass --session etc.
	// without colliding with the top-level binary flags.
	if len(os.Args) >= 2 && os.Args[1] == "checkpoint" {
		return checkpointcli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) >= 2 && os.Args[1] == "undo" {
		return checkpointcli.RunUndo(os.Args[2:], os.Stdout, os.Stderr)
	}
	if len(os.Args) >= 2 && os.Args[1] == "redo" {
		return checkpointcli.RunRedo(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek hooks ...` — list / check / trust / audit. Dispatched
	// before global flag parse for the same reason as `skill`:
	// hooks queries don't need API keys, sessions, or a project
	// directory. See PRD docs/prd/feature-shell-hooks.md §4.1.
	if len(os.Args) >= 2 && os.Args[1] == "hooks" {
		return hookscli.Run(os.Args[2:], os.Stdout, os.Stderr)
	}
	// `seek keys ...` — list / check / actions. Same rationale: keymap
	// queries don't need API keys, sessions, or a project directory.
	// PRD §5.1 wants exit code 2 for validation errors (so CI can
	// distinguish "bad config" from "system failure"); keyscli signals
	// this via ErrUsage which we unwrap and route to os.Exit(2) here.
	// See PRD docs/prd/feature-tui-ergonomics.md §5.1.
	if len(os.Args) >= 2 && os.Args[1] == "keys" {
		if err := keyscli.Run(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			if errors.Is(err, keyscli.ErrUsage) {
				os.Exit(2)
			}
			return err
		}
		return nil
	}

	var (
		prompt        = flag.String("p", "", "prompt text; if non-empty (or stdin is piped) seek runs in print mode and exits")
		model         = flag.String("model", "", "model id; default depends on provider (deepseek-v4-flash for DeepSeek, etc.)")
		maxTurns      = flag.Int("max-turns", 200, "safety bound on agent loop iterations")
		maxTokens     = flag.Int("max-tokens", 0, "completion token cap per call; 0 → default (16384)")
		autoContinue  = flag.Bool("auto-continue", false, "inject 'continue' on text-only turns so the model resumes mid-task without user input")
		yolo          = flag.Bool("yolo", false, "allow bash + writes outside CWD without prompting")
		plan          = flag.Bool("plan", false, "read-only exploration: no bash/writes/edits; produce a plan to review before executing")
		jsonOut       = flag.Bool("json", false, "emit agent events as JSONL on stdout (implies print mode)")
		resume        = flag.String("resume", "", "load a saved session by ID (see seek -list)")
		cont          = flag.Bool("continue", false, "load the most-recently-updated session")
		noSave        = flag.Bool("no-save", false, "do not persist this session to disk")
		noSuggest     = flag.Bool("no-suggest", false, "disable v4 柱 D suggested-reply (predictor + UI placeholder + calibration injection); equivalent to suggest_reply=false in ~/.seek/config.json")
		list          = flag.Bool("list", false, "list saved sessions and exit")
		noProj        = flag.Bool("no-project-md", false, "do not auto-load AGENTS.md from the project tree")
		providerFlag  = flag.String("provider", "", "LLM provider: deepseek (default) | anthropic | openai | gemini | compatible")
		baseURL       = flag.String("base-url", "", "base URL for --provider=compatible (OpenAI-compatible endpoint)")
		providerName  = flag.String("provider-name", "Compatible", "display name for --provider=compatible")
		themeFlag     = flag.String("theme", "auto", "color theme: auto|dark|light")
		rpcMode       = flag.Bool("rpc", false, "run as a JSON-RPC 2.0 server over stdio (for IDE integrations)")
		benchmarkTask = flag.String("benchmark", "", "run a benchmark task (self-hosting | fim-patch) and report metrics")
		benchmarkOut  = flag.String("benchmark-out", "", "write benchmark JSON report to this file (default: stdout)")
		showVersion   = flag.Bool("version", false, "print version info and exit")
		upgradeFlag   = flag.Bool("upgrade", false, "download the latest release from GitHub and replace this binary")
		upgradeForce  = flag.Bool("upgrade-force", false, "with -upgrade: proceed even when the current build is a dev build (overwrites local builds)")
		upgradeDryRun = flag.Bool("upgrade-dry-run", false, "with -upgrade: download + verify checksum but do not replace the binary")
		upgradeCheck  = flag.Bool("upgrade-check", false, "check for a newer release on GitHub and print the result; never modifies the binary")
		installFlag   = flag.Bool("install", false, "add seek to the user PATH (Windows)")
		dreamFlag     = flag.Bool("dream", false, "M→L distillation: scan project memory, print L-pending candidates without writing")
		dreamWrite    = flag.Bool("dream-write", false, "with -dream: actually append the candidates to ~/.seek/soul.md's Pending section")
		// v3 柱 A: keep the per-session file checkpoint directory
		// past SessionEnd. Default off — file checkpoints are
		// "this-session-only" by design (see feature-checkpoint
		// PRD §3.2). Power users debugging across resumes set this.
		keepCheckpoints = flag.Bool("keep-checkpoints", false, "preserve <session>/checkpoints/ across session end (default: cleaned)")
	)
	flag.Parse()

	// --yolo and --plan are mutually exclusive.
	if *yolo && *plan {
		return fmt.Errorf("--yolo and --plan are mutually exclusive")
	}

	// -version / -upgrade short-circuit before any provider / session
	// machinery is touched: these subcommands don't need API keys.
	if *showVersion {
		fmt.Println(tui.VersionString())
		return nil
	}
	if *upgradeFlag || *upgradeDryRun || *upgradeForce {
		return runUpgrade(*upgradeForce, *upgradeDryRun)
	}
	if *upgradeCheck {
		return runUpgradeCheck()
	}

	// -install short-circuit before any provider machinery.
	if *installFlag {
		return runInstall()
	}

	// Best-effort cleanup of a stale ".old" file left by a previous
	// Windows upgrade. No-op on Unix.
	if exe, err := os.Executable(); err == nil {
		upgrade.CleanupStaleOld(exe)
	}

	// Validate --theme before doing anything else.
	switch strings.ToLower(*themeFlag) {
	case "auto", "dark", "light":
	default:
		return fmt.Errorf("--theme must be auto, dark, or light (got %q)", *themeFlag)
	}

	// Session store is needed for -list / -resume / -continue and for
	// auto-save. Construct early so we can short-circuit on -list
	// before hitting the API-key check.
	store, err := session.NewStore()
	if err != nil {
		return err
	}

	if *list {
		return printSessionList(store)
	}

	// First-run setup wizard. Only fires when there's truly no auth
	// anywhere (env + config both empty) AND stdin is a TTY (otherwise
	// scripts get an honest error instead of a hung interactive prompt).
	// User flags like --provider don't suppress the wizard — if the
	// chosen provider's key isn't anywhere, buildProvider would error
	// out anyway, and an interactive paste is friendlier than a 1-liner
	// "X is not set" line.
	if *providerFlag == "" && shouldTriggerWizard() {
		// The wizard runs before the SIGINT-bound ctx is established
		// (that lives further down, after auth is resolved). Using a
		// background ctx is fine — the wizard itself is driven by
		// bufio.Scanner, and pingDeepSeek derives its own 10s timeout.
		if _, werr := runSetupWizard(context.Background(), os.Stdin, os.Stderr); werr != nil {
			return werr
		}
	}

	// First-run PATH nudge on Windows TUI launches — runs after the setup
	// wizard but before agent/session machinery so the user isn't kept
	// waiting through a full init only to hit an stdin prompt.
	if willUseTUI(*jsonOut, *prompt, *benchmarkTask, *rpcMode, *dreamFlag) {
		if err := maybeWindowsPATHPrompt(); err != nil {
			return err
		}
	}

	// Provider detection. --provider overrides auto-detect; otherwise we
	// check env vars in priority order: DeepSeek first, then second-tier.
	provider, dsClient, provLabel, modelDefault, err := buildProvider(
		*providerFlag, *baseURL, *providerName,
	)
	if err != nil {
		return err
	}

	// Resolve which session we're operating on. Priority:
	//   -resume <id>  → load that session (error if missing)
	//   -continue     → load latest (error if no sessions yet)
	//   else          → new session, fresh history
	var loaded *session.Session
	if *resume != "" {
		loaded, err = store.Load(*resume)
		if err != nil {
			return fmt.Errorf("resume %s: %w", *resume, err)
		}
	} else if *cont {
		loaded, err = store.Latest()
		if err != nil {
			return fmt.Errorf("continue: %w", err)
		}
		if loaded == nil {
			return fmt.Errorf("continue: no saved sessions in %s", store.Dir())
		}
	}
	// Defensive repair: older sessions written before the orphan-
	// tool_calls fix may have a trailing assistant tool_calls message
	// with no matching tool results. The API rejects that on the next
	// turn — repair drops the offending tail so the user can continue
	// instead of being permanently locked out of the session.
	if loaded != nil {
		if n := loaded.Repair(); n > 0 {
			fmt.Fprintf(os.Stderr,
				"session %s: repaired %d trailing message(s) with orphan tool_calls — re-ask your last question if needed\n",
				loaded.ID, n)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// If we loaded a session, its Model/Yolo override the flag defaults
	// (sessions are sticky — resuming with different settings would be
	// surprising). The flags can still override explicitly: set them
	// AFTER -resume on the command line and they win.
	if loaded != nil {
		if *model == modelDefault {
			// User didn't override the model flag; honour the saved one.
			*model = loaded.Model
		}
		if !*yolo {
			// Inherit yolo state from the saved session if user didn't
			// pass --yolo explicitly.
			*yolo = loaded.Yolo
		}
		if !*plan {
			// Inherit plan state from the saved session if user didn't
			// pass --plan explicitly.
			*plan = loaded.Plan
		}
	}
	// Print mode (-p / piped stdin) can't realistically interrupt to
	// ask, so it stays in Deny preference unless --yolo is explicit.
	// The TUI path overrides to Ask further down so per-call approval
	// kicks in. --yolo wins on pref; --plan enters the plan workflow
	// (they're orthogonal so could in principle co-exist; cmdYolo/
	// cmdPlan enforce mutual exclusion at the UI layer).
	initialPref := permission.PrefDeny
	if *yolo {
		initialPref = permission.PrefYolo
	}
	initialWorkflow := permission.WorkflowNone
	if *plan {
		initialWorkflow = permission.WorkflowPlanAnalyze
	}
	policy, err := permission.New(cwd, initialPref)
	if err != nil {
		return err
	}
	if initialWorkflow != permission.WorkflowNone {
		policy.SetWorkflow(initialWorkflow)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// -dream short-circuits before session / agent / TUI setup. Needs a
	// DeepSeek client only (the thinking-mode call uses the Chat API);
	// second-tier providers don't currently expose a non-streaming Chat
	// on the llm.Provider interface, so -dream is DeepSeek-only for now.
	if *dreamFlag {
		if dsClient == nil {
			return fmt.Errorf("-dream requires a DeepSeek API key (DEEPSEEK_API_KEY); set one or use --provider=deepseek explicitly")
		}
		return runDream(ctx, dsClient, *dreamWrite)
	}

	if *model == "" {
		*model = modelDefault
	}
	sessionModel := *model
	// sessionEffort mirrors session.Effort: "" (model default) | "high" |
	// "max". The /effort TUI command updates it through SetEffort below;
	// the think tool reads it through its effortFunc closure so its
	// bumped-by-one-level rule (see think.bumpEffort) reflects the
	// session-level choice at call time.
	// Default is "max" — the deepest reasoning level.
	var sessionEffort = "max"
	tracker := cache.New()

	// Project-level AGENTS.md, if present. Walks up from cwd. Failures
	// (permission denied on a real file) are reported but non-fatal —
	// the rest of seek still works without project instructions.
	var projMD projectmd.Result
	if !*noProj {
		pm, perr := projectmd.Load(cwd)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "project-md:", perr)
		}
		projMD = pm
	}

	// Load skills before the system prompt is rendered — the manifest
	// is appended below. Errors are non-fatal: a malformed user skill
	// shouldn't lock the agent out of running.
	skills, skillStats, _ := skill.Load(skill.LoadOptions{ProjectDir: cwd})
	for _, err := range skillStats.Errors {
		fmt.Fprintln(os.Stderr, "skills:", err)
	}
	if summary := skillStats.FormatLoadSummary(); summary != "" {
		fmt.Fprintln(os.Stderr, summary)
	}

	// memProject + activeSession are forward-declared so the Skill tool's
	// stats EnvFn can read them by reference — both are set further
	// down (memProject by memory.LoadOrCreate, activeSession by
	// session.New / Store.Load). Until those run the env fn returns
	// empty strings, which skillstats omits from the JSONL anyway.
	var memProject *memory.Project
	var activeSession *session.Session

	// Wire the skill call-stats writer (PRD v2 §4.3). Failure to
	// resolve the path is non-fatal — we just disable stats for this
	// session rather than refusing to start.
	var statsWriter *skillstats.Writer
	if path, err := paths.UserSkillStats(); err == nil {
		statsWriter = skillstats.New(path)
	}
	statsEnv := func() skilltool.Env {
		env := skilltool.Env{
			Model:    *model,
			Provider: provLabel,
		}
		if activeSession != nil {
			env.SessionID = activeSession.ID
		}
		if memProject != nil {
			env.ProjectID = memProject.ID
		}
		return env
	}

	// askPolicy holds the callback for ask_user. Constructed here
	// (before tool registration) so askusertool.New can capture it;
	// the actual channel + SetAskFn wiring happens later once ctx
	// and the TUI options are ready.
	askPolicy := askuser.New(askuser.ModeAsk)

	// Resolve the absolute project root early — the checkpoint
	// Manager (constructed next) needs it, and downstream code
	// (memory init, system prompt rendering) re-uses the same
	// value via the variable.
	abs, _ := filepath.Abs(cwd)

	// v3 checkpoint Manager (feature-checkpoint.md). Constructed
	// BEFORE tool registration so write.WithSnapshotter / edit
	// .WithSnapshotter can bind. Built with the session id we
	// either inherit (resume) or pre-generate (fresh). The pre-
	// generation only commits a string — the actual session
	// header is written later by store.Save.
	//
	// --no-save disables checkpoint entirely (ephemeral run; no
	// session id means no scoped storage path). Same intent as
	// --no-save's memory / session opt-outs.
	var ckMgr *checkpoint.Manager
	if !*noSave {
		sid := ""
		if loaded != nil {
			sid = loaded.ID
		} else {
			// Construct the session early so we get a stable ID
			// for the checkpoint scope. activeSession is reused
			// below — the fresh-session branch at line ~870 only
			// runs when loaded == nil AND activeSession is nil,
			// so this pre-emptive construction is safe.
			activeSession = session.New(*model, abs, "", *yolo, *plan)
			sid = activeSession.ID
		}
		ckMgr = checkpoint.New(checkpoint.Config{
			SessionID:  sid,
			ProjectAbs: abs,
			CWD:        abs,
			Sink:       checkpointStderrSink{},
			KeepOnExit: *keepCheckpoints,
		})
		// Hook into the permission gate so destructive actions
		// fire MaybeCreateGit before they hit the filesystem.
		policy.SetOnDestructive(func(a permission.Action) {
			ckMgr.MaybeCreateGit(ctx, a)
		})
	}

	reg := tools.New().
		Add(read.New(policy)).
		Add(grep.New()).
		Add(listdir.New()).
		Add(write.New(policy).WithSnapshotter(checkpointSnapshotter{m: ckMgr})).
		Add(edit.New(policy).WithSnapshotter(checkpointSnapshotter{m: ckMgr})).
		Add(bash.New(policy)).
		Add(gittool.New()).
		Add(skilltool.NewWithStats(skills, statsWriter, statsEnv)).
		Add(skillinstall.NewFetch()).
		Add(skillinstall.NewCommit(policy)).
		Add(askusertool.New(askPolicy))

	// webfetch: opt-out via SEEK_NO_WEBFETCH for air-gapped /
	// privacy-sensitive sessions. SEEK_WEBFETCH_ALLOW_HTTP opens
	// the http:// scheme for internal docs sites. See PRD
	// docs/prd/feature-webfetch.md.
	if !envBoolTrue("SEEK_NO_WEBFETCH") {
		wfOpts := webfetch.DefaultOptions()
		if envBoolTrue("SEEK_WEBFETCH_ALLOW_HTTP") {
			wfOpts.AllowHTTP = true
		}
		reg.Add(webfetch.New(wfOpts))
	}

	// DeepSeek-exclusive tools: FIM and Reasoner are only available
	// when using the DeepSeek client directly.
	if dsClient != nil {
		reg.Add(fimcomplete.New(dsClient, *model)).
			Add(think.New(dsClient, func() string { return sessionModel }, func() string { return sessionEffort }))
	}

	// Load MCP servers and register their tools. Errors are non-fatal:
	// a broken MCP server should not prevent seek from starting.
	if mcpCfg, err := mcpconfig.Load(); err != nil {
		fmt.Fprintln(os.Stderr, "mcp config:", err)
	} else if len(mcpCfg.MCPServers) > 0 {
		servers := make(map[string]mcptool.ServerConfig, len(mcpCfg.MCPServers))
		for name, e := range mcpCfg.MCPServers {
			servers[name] = mcptool.ServerConfig{
				Command: e.Command,
				Args:    e.Args,
				Env:     mcptool.EnvMapToSlice(e.Env),
			}
		}
		existing := make(map[string]bool, len(reg.Names()))
		for _, n := range reg.Names() {
			existing[n] = true
		}
		lr := mcptool.LoadServers(ctx, servers, existing)
		for _, e := range lr.Errors {
			fmt.Fprintln(os.Stderr, "mcp:", e)
		}
		for _, b := range lr.Bridges {
			reg.Add(b)
		}
		if len(lr.Bridges) > 0 {
			fmt.Fprintf(os.Stderr, "mcp: loaded %d tool(s)\n", len(lr.Bridges))
		}
	}

	// M layer + L layer. Without session persistence (--no-save) we
	// also skip memory persistence — they share the "this is a
	// throwaway run" intent. Load failures are non-fatal: a broken
	// or read-only ~/.seek/projects/ degrades the session (no memory
	// injection, no recall, no remember) but should not block startup.
	// memProject is forward-declared above (the Skill stats env fn
	// captures it by reference); only the assignment lives here.
	var memSoul *memory.Soul
	if !*noSave {
		if proj, err := memory.LoadOrCreate(abs); err != nil {
			fmt.Fprintln(os.Stderr, "memory:", err)
		} else {
			memProject = proj
		}
		if soul, err := memory.LoadSoul(); err != nil {
			fmt.Fprintln(os.Stderr, "memory soul:", err)
		} else {
			memSoul = soul
		}
		reg.Add(memorytool.NewRecall(memProject)).
			Add(memorytool.NewRemember(memProject, policy)).
			Add(memorytool.NewArchive(memProject)).
			Add(memorytool.NewAmend(memProject))
	}

	// Project instructions go BEFORE the skill manifest inside Compose:
	// they describe "how this repo expects you to work" while skills are
	// workflow templates. Ordering matches the model's likely reading
	// priority.
	systemPrompt := sysprompt.Compose(sysprompt.Header{
		Cwd:            abs,
		ProjectSection: projMD.Section(),
		SkillManifest:  skills.Manifest(),
	}, sysprompt.ModeLabel(initialPref, initialWorkflow))

	// Build (or restore) the persistence session. -no-save makes
	// activeSession nil so the TUI auto-save no-ops. activeSession
	// is forward-declared above (Skill stats env fn captures it);
	// only the assignment lives here.
	//
	// v3 checkpoint: a fresh activeSession may already exist —
	// pre-constructed earlier so the checkpoint Manager could
	// bind a stable session id. In that case we just back-fill
	// the SystemPrompt that wasn't known at construction time.
	var initialMsgs []deepseek.Message
	if !*noSave {
		if loaded != nil {
			activeSession = loaded
			initialMsgs = loaded.Messages
			// Replay accumulated stats into the tracker so the status
			// bar shows cumulative figures, not just this run's. The
			// session file stores only aggregate Usage (no per-turn
			// breakdown), so we attribute the whole thing to the
			// current model+tier — an approximation when a session
			// spans multiple models or tier transitions. Subsequent
			// turns are priced accurately at their own (model, tier).
			if loaded.Usage.TotalTokens > 0 {
				tracker.SetBase(loaded.Usage, *model, pricing.CurrentTier(time.Now()))
			}
		} else if activeSession == nil {
			activeSession = session.New(*model, abs, systemPrompt, *yolo, *plan)
		} else {
			// Pre-constructed by the checkpoint Manager bootstrap;
			// back-fill the SystemPrompt now that it's known.
			activeSession.SystemPrompt = systemPrompt
		}
		// A resumed session may carry an /effort selection from the prior
		// run — restore it before the agent is built so the very first
		// turn after --continue honours the user's choice.
		// NB: guard on loaded, not activeSession — activeSession is always
		// non-nil (either loaded or a fresh session.New), so checking it
		// would overwrite the "max" default with the empty string from a
		// brand-new session (see commit <fill>).
		if loaded != nil {
			sessionEffort = activeSession.Effort
		}

	}

	// Lifecycle hooks. v1 memory plugs in PrePromptHook (inject L+M
	// <context> blocks) + SessionStartObserver (run GC) from one
	// struct registered into the same registry — see internal/memory.
	// Session-lifecycle hooks (SessionStart / SessionEnd) are fired
	// from main.go because the agent doesn't know when its host
	// program is "done".
	hooksReg := hooks.NewRegistry()

	// v3 柱 A: register the checkpoint Manager as a SessionEnd /
	// PreTurn observer so file-checkpoint state is cleaned up on
	// shutdown and per-turn git-checkpoint arming refreshes each
	// turn. Wrapped in a small adapter so the Manager's specific
	// signatures match the hooks Observer interfaces.
	if ckMgr != nil {
		hooksReg.Register(&checkpointHook{m: ckMgr})
	}

	// v3 pillar B — user-configurable shell hooks. Gate() reads
	// ~/.seek/hooks.toml (no trust prompt) and <project>/.seek/hooks.toml
	// (trust-on-first-visit + sha256-change re-prompt), then merges +
	// static-checks. The CLI/print paths run without a TrustPrompt so
	// project hooks stay dormant until the user trusts them from a
	// TUI session; this matches the PRD §3.5 "no bash before trust"
	// guarantee — the only way `bash -c` runs is via the StaticCheck
	// inside Gate (`bash -n`, safe) and via ShellRunner at dispatch
	// time AFTER Register, which only happens when HasHooks is true.
	// v3 柱 C — user-customisable keybindings. Load BEFORE constructing
	// the TUI Options so the loaded KeyMap (or default fallback) is
	// available at Options.Keymap. Warnings from a malformed
	// keybindings.toml go to stderr; the file is silently ignored if
	// absent (common case). PRD docs/prd/feature-tui-ergonomics.md §4.4.
	userKeymapPath, _ := paths.UserKeybindings()
	userKeymap, _ := keymap.Load(userKeymapPath, os.Stderr)

	userHooksPath, _ := paths.UserHooksToml()
	projectHooksPath := paths.ProjectHooksToml(abs)
	trustPath, _ := paths.TrustedProjectsJSON()
	auditPath, _ := paths.HooksAuditLog()
	trustStore, err := hooksconfig.NewTrustStore(trustPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hooks:", err)
	}
	// stdinTrustPrompt asks the user y/N before any project-level
	// `bash -c` can fire. Detects TTY internally — piped/non-TTY
	// stdin auto-refuses with a friendly warning rather than hang.
	// Per PRD §3.5: this is the ONLY thing standing between a freshly
	// cloned repo's hooks.toml and arbitrary shell execution; we run
	// it BEFORE constructing ShellRunner so the contract "no `bash -c`
	// before trust" holds even when the file is malicious.
	hookCfg, hookWarnings := hooksconfig.Gate(
		userHooksPath, projectHooksPath, abs, trustStore,
		newStdinTrustPrompt(),
		hooksconfig.DefaultSyntaxChecker,
	)
	for _, w := range hookWarnings {
		fmt.Fprintln(os.Stderr, w)
	}
	var shellRunner *hooks.ShellRunner
	if !hookCfg.IsEmpty() {
		auditLog, alerr := hooksconfig.NewAuditLog(auditPath)
		if alerr != nil {
			fmt.Fprintln(os.Stderr, "hooks: audit log:", alerr)
		}
		shellRunner = hooks.NewShellRunner(hookCfg,
			hooks.WithAuditLog(auditLog),
			hooks.WithVersion(tui.VersionString()),
			hooks.WithProjectContext(paths.ProjectID(abs), abs),
		)
		if shellRunner.HasHooks() {
			hooksReg.Register(shellRunner)
		}
	}

	// v4 柱 D suggested-reply: a single switch (`--no-suggest` CLI flag
	// trumps `suggest_reply: true|false` in ~/.seek/config.json; absent
	// config field defaults to enabled). When disabled, both the
	// Predictor and the InjectCalibration message-preparer are nil —
	// `agent.runTurn*` no-ops the PrepareMessages hook on nil, and the
	// TUI suggester goroutine is gated on Options.Suggester != nil.
	// PRD docs/prd/feature-suggested-reply.md §4.7.
	appCfg, _ := config.Load()
	suggestEnabled := !*noSuggest && appCfg.SuggestReplyEnabled() && dsClient != nil
	var predictor *suggester.Predictor
	var prepareMessages func([]deepseek.Message) []deepseek.Message
	if suggestEnabled {
		predictor = suggester.New(dsClient)
		prepareMessages = suggester.InjectCalibration
	}

	// v5 柱 G subagent orchestrator (feature-subagent PRD §3.7).
	// Subagent persistence (index + transcript dir) needs an
	// activeSession; in --no-save we skip subagent wiring entirely
	// so the LLM can't spawn what we can't track. The `agent` tool
	// is simply absent from the registry in that mode.
	//
	// Closures: ParentSidFn reads activeSession.ID live so /compact
	// (forks a new session) is reflected; SkillManifestFn reads
	// skills.Manifest() live so skill_commit + /new shows up in
	// the next subagent's system prompt; ParentToolNamesFn reads
	// reg.Names() live so plan/propose tools added below are
	// available to subagents that want them (e.g. plan template).
	if !*noSave && activeSession != nil {
		subagentMgr, smerr := subagent.NewManager(subagent.ManagerOpts{
			ProjectAbsPath: abs,
			ParentTracker:  tracker,
			ParentPolicy:   policy,
			ParentSidFn: func() string {
				if activeSession == nil {
					return ""
				}
				return activeSession.ID
			},
			ProjectSectionFn:  func() string { return projMD.Section() },
			SkillManifestFn:   func() string { return skills.Manifest() },
			ParentToolNamesFn: func() []string { return reg.Names() },
			Runner: buildSubagentRunner(subagentRunnerOpts{
				client:    dsClient,
				provider:  provider,
				parentReg: reg,
				hooksReg:  hooksReg,
				maxTokens: *maxTokens,
				maxTurns:  *maxTurns,
				getModel:  func() string { return sessionModel },
				getEffort: func() string { return sessionEffort },
			}),
		})
		if smerr != nil {
			return fmt.Errorf("subagent manager: %w", smerr)
		}
		reg.Add(agenttool.New(subagentMgr))

		// On startup, scan the index for `started`-without-terminal
		// sub_sids (subagents whose owning seek process crashed
		// mid-run). Marks them `orphaned` so /agents doesn't show
		// stale "active" rows after a crash recover. Best-effort —
		// failure here doesn't block startup; the panel just shows
		// the stale row until next clean shutdown.
		if indexPath, perr := paths.SubagentsIndex(abs); perr == nil {
			if _, oerr := subagent.OrphanRecover(indexPath); oerr != nil {
				fmt.Fprintln(os.Stderr, "subagent: orphan recover:", oerr)
			}
		}
	}

	ag, err := agent.New(agent.Config{
		Client:          dsClient,
		Provider:        provider,
		Model:           *model,
		Effort:          sessionEffort,
		SystemPrompt:    systemPrompt,
		Tools:           reg,
		MaxTokens:       *maxTokens,
		MaxTurns:        *maxTurns,
		AutoContinue:    *autoContinue,
		InitialMessages: initialMsgs,
		Hooks:           hooksReg,
		PrepareMessages: prepareMessages,
	})
	if err != nil {
		return err
	}

	// Register the plan + propose tools AFTER agent.New so both Sinks
	// can route into ag.EmitEvent. Same deferred-registration pattern
	// as memorytool.NewObserve below (the Registry's tool list isn't
	// frozen until Wire() runs on the first Prompt). The plan tool is
	// constructed first because the propose sink Seeds it on approval
	// — both tools share that *plan.Tool pointer for the program's
	// lifetime. See PRD docs/prd/feature-plan-mode.md for the full
	// plan-mode flow.
	bridge := &planBridge{
		getAgent:        func() *agent.Agent { return ag },
		policy:          policy,
		projectAbs:      abs,
		artifactEnabled: !*noSave,
		sessionID: func() string {
			// activeSession may flip on /compact (forked session
			// with new ID) — close over the variable, not the
			// current value, so the next Approved picks up the
			// live ID. Empty string in --no-save mode signals "do
			// not write artifacts" to Approved's gate (defensive;
			// artifactEnabled above is the primary gate).
			if activeSession == nil {
				return ""
			}
			return activeSession.ID
		},
	}
	planTool := plantool.New(bridge)
	bridge.planTool = planTool
	reg.Add(planTool)
	reg.Add(propose.New(askPolicy, bridge))

	// On resume, rebuild the plan task list from the transcript so the
	// TUI shows the user where they left off. Reconstruction here only
	// populates `restoredPlanSteps` for the initial tui.Options seed;
	// the planTool itself is also Restored so subsequent plan() calls
	// from the model see the correct in-memory state. Reconstruction
	// is a pure-read scan — no events emitted (no agent Prompt active
	// yet, EmitEvent would no-op anyway).
	var (
		restoredPlanSteps  []agent.PlanStep
		restoredPlanCurIdx = -1
	)
	if loaded != nil && len(initialMsgs) > 0 {
		steps, cur := plantool.ReconstructFromTranscript(initialMsgs)
		if len(steps) > 0 {
			planTool.Restore(steps, cur)
			restoredPlanSteps = make([]agent.PlanStep, len(steps))
			for i, st := range steps {
				restoredPlanSteps[i] = agent.PlanStep{Text: st.Text, Status: string(st.Status)}
			}
			restoredPlanCurIdx = cur
		}
	}

	// Register the memory hook AFTER agent.New so the M5.7 auto-distill
	// HistoryProvider can close over ag.Messages. The Registry stores a
	// pointer and dispatches at call-time, so deferring registration
	// past agent.New is safe — no events fire until NotifySessionStart
	// below.
	var memHook *memory.Hook
	if memProject != nil || memSoul != nil {
		memHook = &memory.Hook{
			Project:    memProject,
			Soul:       memSoul,
			ResultChan: make(chan memory.ObserveResult, 20),
		}
		if memProject != nil && dsClient != nil {
			memHook.Distiller = &memory.Distiller{Client: dsClient}
			memHook.HistoryProvider = ag.Messages
			memHook.Dreamer = &memory.Dreamer{Client: dsClient}

			// Register memory_observe tool gated on $SEEK_AUTO_DISTILL.
			// Default (unset / 1/true/yes/on) → registered so the model
			// can save decisions in real time. Set to 0/false/no/off to
			// disable (PRD §6 v2).
			if autoDistillEnabled() {
				enqueue := memHook.ObserveEnqueue()
				reg.Add(memorytool.NewObserve(memProject, enqueue))
			}
		}
		hooksReg.Register(memHook)
	}

	var observeResultChan <-chan memory.ObserveResult
	if memHook != nil {
		observeResultChan = memHook.ResultChan
	}

	var sessionID string
	if activeSession != nil {
		sessionID = activeSession.ID
	}
	hooksReg.NotifySessionStart(ctx, hooks.SessionStartEvent{
		ID:      sessionID,
		Model:   *model,
		CWD:     abs,
		Resumed: loaded != nil,
	})
	defer func() {
		hooksReg.NotifySessionEnd(context.Background(), hooks.SessionEndEvent{
			ID:    sessionID,
			Usage: tracker.Cumulative(),
		})
		// Flush the shell-hook audit log on the way out. SessionEnd
		// observers in ShellRunner write one final audit row; Close
		// makes sure the file's fsync isn't lost to a fast exit.
		if shellRunner != nil {
			_ = shellRunner.Close()
		}
	}()

	// Benchmark mode: short-circuit before normal routing. Forces --yolo
	// so the agent can run bash/go-test without interactive approval.
	if *benchmarkTask != "" {
		*yolo = true
		policy.SetPref(permission.PrefYolo)
		return runBenchmark(ctx, *benchmarkTask, *benchmarkOut,
			ag, tracker, *model, activeSession, store)
	}

	if *rpcMode {
		return runRPC(ctx, ag, tracker, *model, *yolo, *plan, activeSession, store)
	}

	// Route: --rpc → JSON-RPC 2.0 server; -json / -p / piped stdin → print; otherwise TUI.
	if *jsonOut || *prompt != "" || stdinIsPiped() {
		text, err := resolvePrompt(*prompt)
		if err != nil {
			return err
		}
		if text == "" {
			return fmt.Errorf("empty prompt (pass -p or pipe text on stdin)")
		}
		if *jsonOut {
			return runJSON(ctx, ag, tracker, *model, *yolo, *plan, text, activeSession, store)
		}
		return runPrint(ctx, ag, tracker, *model, *yolo, *plan, text, activeSession, store)
	}

	// Now that we know we're entering the TUI, upgrade the policy
	// from Deny → Ask unless --yolo was passed. This is what gives
	// us inline y/N prompts on bash and out-of-CWD writes. --plan
	// doesn't change pref (it sets the workflow); upgrading pref to
	// Ask here is correct for --plan too — plan-execute uses Ask for
	// per-call gating after approval.
	if !*yolo {
		policy.SetPref(permission.PrefAsk)
	}

	// Approval channel: askFn pushes a request, blocks on its reply.
	// Buffered so a slow TUI doesn't deadlock a fast tool dispatcher
	// (the agent loop is sequential today, but the buffer is cheap).
	approvalCh := make(chan permission.ApprovalRequest, 4)
	policy.SetAskFn(func(a permission.Action) bool {
		resp := make(chan bool, 1)
		select {
		case approvalCh <- permission.ApprovalRequest{Action: a, Reply: resp}:
		case <-ctx.Done():
			return false
		}
		select {
		case ok := <-resp:
			return ok
		case <-ctx.Done():
			return false
		}
	})

	// ask_user channel: same pattern as approval, but the reply
	// type is the structured askuser.Answer (chosen ids OR free
	// text OR cancelled) rather than a bool. Buffer 4 mirrors the
	// approval channel — never has more than one in flight today,
	// the buffer is a defence against future parallelism. askPolicy
	// itself was constructed earlier (before tool registration) so
	// the askuser tool could capture it; SetAskFn registers the
	// real callback here, now that ctx + askUserCh are in scope.
	askUserCh := make(chan askuser.Request, 4)
	askPolicy.SetAskFn(func(q askuser.Question) askuser.Answer {
		resp := make(chan askuser.Answer, 1)
		select {
		case askUserCh <- askuser.Request{Question: q, Reply: resp}:
		case <-ctx.Done():
			return askuser.Answer{Cancelled: true}
		}
		select {
		case ans := <-resp:
			return ans
		case <-ctx.Done():
			return askuser.Answer{Cancelled: true}
		}
	})

	sessionModel = *model

	// Resolve the effective theme for the TUI.
	effectiveTheme := strings.ToLower(*themeFlag)
	glamourStyle := detectGlamourStyle(effectiveTheme)
	// If auto, resolve to the concrete dark/light value.
	if effectiveTheme == "auto" {
		effectiveTheme = glamourStyle
	}

	// /distill needs both the Project (where saved candidates land)
	// and a Distiller (the thinking-mode round-trip). The Distiller is only
	// constructible when we have a DeepSeek client — second-tier
	// providers don't currently expose a Chat method on the same
	// interface, so /distill is DeepSeek-only for now.
	var distiller *memory.Distiller
	if memProject != nil && dsClient != nil {
		distiller = &memory.Distiller{Client: dsClient}
	}

	return tui.Run(tui.Options{
		Agent:                 ag,
		Tracker:               tracker,
		Model:                 sessionModel,
		Effort:                sessionEffort,
		Yolo:                  policy.Yolo(),
		Plan:                  policy.Plan(),
		PlanSteps:             restoredPlanSteps,
		PlanCurrentIdx:        restoredPlanCurIdx,
		RevokePlanPreApproval: func() { policy.SetPreApproved(false) },
		CWD:                   abs,
		Ctx:                   ctx,
		Theme:                 effectiveTheme,
		GlamourStyle:          glamourStyle,
		ApprovalCh:            approvalCh,
		AskUserCh:             askUserCh,
		Session:               activeSession,
		Store:                 store,
		Checkpoint:            ckMgr,
		Keymap:                userKeymap,
		Suggester:             predictor,
		Skills:                skills,
		ProviderName:          provLabel,
		MemoryProject:         memProject,
		Distiller:             distiller,
		ObserveResultChan:     observeResultChan,

		RebuildAgent: func() (*agent.Agent, error) {
			// /reset rebuilds the agent; we have to re-apply project
			// instructions AND the skill manifest, otherwise the model
			// would forget both after a reset. AGENTS.md is loaded
			// once at startup and reused (re-reading on /reset would
			// surprise users who edit the file mid-session — we want
			// the file's behaviour to be "loaded at launch", not "hot-
			// reloaded"; documented behaviour is easier to reason
			// about than clever).
			//
			// Skills, however, ARE hot-reloaded here so that newly
			// installed skills (via skill_commit) appear in the system
			// prompt manifest after /new without requiring a full restart.
			if freshSkills, _, lerr := skill.Load(skill.LoadOptions{ProjectDir: cwd}); lerr == nil && freshSkills != nil {
				skills = freshSkills
			}
			sp := sysprompt.Compose(sysprompt.Header{
				Cwd:            abs,
				ProjectSection: projMD.Section(),
				SkillManifest:  skills.Manifest(),
			}, sysprompt.ModeLabel(policy.Pref(), policy.Workflow()))
			newAg, err := agent.New(agent.Config{
				Client:       dsClient,
				Provider:     provider,
				Model:        sessionModel,
				Effort:       sessionEffort,
				SystemPrompt: sp,
				Tools:        reg,
				MaxTokens:    *maxTokens,
				MaxTurns:     *maxTurns,
				AutoContinue: *autoContinue,
			})
			if err != nil {
				return nil, err
			}
			// Keep the closure-captured ag in sync so SetYolo /
			// SetPlan callbacks update the live agent's
			// ModeLabel, not a stale copy.
			ag = newAg
			// Rebuilt system prompt already has the correct mode
			// label — clear per-message reminder.
			ag.SetModeLabel("")
			return newAg, nil
		},
		SetModel: func(m string) { sessionModel = m },
		SetEffort: func(e string) {
			// Mirror into the closures the think tool and agent.Config
			// builds read from. The TUI separately calls Agent.SetEffort
			// on the live agent so the change is visible on the very
			// next prompt without a /reset / RebuildAgent.
			sessionEffort = e
		},
		SetYolo: func(y bool) {
			// policy.SetPref takes effect immediately for every
			// tool's permission.Check call. The agent's per-message
			// modeReminder keeps the model in sync without touching
			// the system prompt (prefix-cache safe).
			if y {
				policy.SetPref(permission.PrefYolo)
				ag.SetModeLabel("yolo")
			} else {
				policy.SetPref(permission.PrefAsk)
				ag.SetModeLabel("")
			}
		},
		SetPlan: func(p bool) {
			// /plan toggles the Workflow axis (not Pref). Entering
			// /plan starts in the analyze substate (plan mode v2;
			// PRD §2.4). The legacy "plan" label still works in
			// modeReminder for back-compat.
			if p {
				policy.SetWorkflow(permission.WorkflowPlanAnalyze)
				ag.SetModeLabel("plan-analyze")
			} else {
				policy.SetWorkflow(permission.WorkflowNone)
				ag.SetModeLabel("")
			}
		},
		SetPlanSubstate: func(s string) {
			// Driven by propose tool events flowing through
			// applyAgentEvent. "execute" moves the workflow to
			// PlanExecute (pref is untouched — typically Ask, so
			// per-call gating applies unless preApproved is set by
			// plan-tool start/complete). "" is symmetric with
			// SetPlan(false).
			switch s {
			case "execute":
				policy.SetWorkflow(permission.WorkflowPlanExecute)
				ag.SetModeLabel("plan-execute")
			case "analyze":
				policy.SetWorkflow(permission.WorkflowPlanAnalyze)
				ag.SetModeLabel("plan-analyze")
			case "":
				policy.SetWorkflow(permission.WorkflowNone)
				ag.SetModeLabel("")
			}
		},
	})
}

// stdinIsPiped returns true when stdin is not a terminal (i.e. data is
// being piped or redirected in). When false, seek launches the TUI.
func stdinIsPiped() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice == 0
}

// runPrint preserves the M3 print-mode behaviour: stream to stdout,
// tool indicators + stats footer to stderr. Suitable for piping.
// The session is saved after every TurnEnd so a crash or interrupt
// mid-run preserves progress up to the last completed turn.
func runPrint(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, text string, activeSession *session.Session, store *session.Store) error {
	tier := pricing.CurrentTier(time.Now())
	nextTier, nextAt := pricing.NextTransition(time.Now())
	fmt.Fprintf(os.Stderr, "\x1b[2mtier: %s → next %s at %s\x1b[0m\n",
		pricing.TierLabel(tier),
		pricing.TierLabel(nextTier),
		nextAt.In(pricing.Shanghai).Format("2006-01-02 15:04 MST"))

	start := time.Now()
	var (
		firstByte time.Duration
		gotFirst  bool
		turns     int
		toolCalls int
	)

	// saveTurn snapshots the current agent state to disk. Called after
	// each TurnEnd so an interrupt preserves progress. Failures are
	// warnings only — the answer already printed to stdout.
	saveTurn := func() {
		if activeSession == nil || store == nil {
			return
		}
		activeSession.Messages = ag.Messages()
		activeSession.Turns = turns
		activeSession.ToolCalls = toolCalls
		activeSession.Usage = tracker.Cumulative()
		activeSession.Model = model
		activeSession.Yolo = yolo
		activeSession.Plan = plan
		// In non-TUI runs (runPrint / runJSON / runRPC) there is no
		// /effort command, so the agent's Effort is constant for the
		// lifetime of the process. Reading it off the agent keeps the
		// helper signature stable instead of growing another param.
		activeSession.Effort = ag.Effort()
		if err := store.Save(activeSession); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save session after turn %d: %v\n", turns, err)
		}
	}

	for ev := range ag.Prompt(ctx, text) {
		switch e := ev.(type) {
		case agent.MessageDelta:
			if !gotFirst {
				firstByte = time.Since(start)
				gotFirst = true
			}
			if e.Reasoning {
				fmt.Fprint(os.Stderr, "\x1b[2m"+e.Delta+"\x1b[0m")
			} else {
				fmt.Print(e.Delta)
			}

		case agent.ToolExecStart:
			fmt.Fprintf(os.Stderr, "\n\x1b[36m[tool] → %s %s\x1b[0m\n", e.Name, truncate(e.Args, 200))

		case agent.ToolExecEnd:
			if e.Err != nil {
				fmt.Fprintf(os.Stderr, "\x1b[31m[tool] ← %s ERROR: %v\x1b[0m\n", e.Name, e.Err)
			} else {
				fmt.Fprintf(os.Stderr, "\x1b[36m[tool] ← %s (%d bytes)\x1b[0m\n", e.Name, len(e.Result))
			}

		case agent.TurnEnd:
			tracker.Record(e.Usage, model, pricing.CurrentTier(time.Now()))
			turns++
			toolCalls += e.ToolCalls
			saveTurn()

		case agent.AgentEnd:
			// turns/toolCalls already accumulated via TurnEnd above.

		case agent.ErrorEvent:
			fmt.Println()
			return e.Err
		}
	}

	fmt.Println()
	c := tracker.Cumulative()
	// Cumulative cost is summed from per-turn locked-in amounts in the
	// tracker, not re-derived from cumulative tokens at the current
	// (model, tier). Matters when the session straddled a /model
	// switch or the 00:30/08:30 tier boundary; see internal/cache doc.
	cost := tracker.CumulativeCost()

	fmt.Fprintf(os.Stderr, "\n--- seek stats ---\n")
	fmt.Fprintf(os.Stderr, "yolo:         %v\n", yolo)
	fmt.Fprintf(os.Stderr, "plan:         %v\n", plan)
	fmt.Fprintf(os.Stderr, "model:        %s\n", model)
	fmt.Fprintf(os.Stderr, "tier:         %s\n", pricing.TierLabel(tier))
	fmt.Fprintf(os.Stderr, "turns:        %d\n", turns)
	fmt.Fprintf(os.Stderr, "tool calls:   %d\n", toolCalls)
	fmt.Fprintf(os.Stderr, "ttfb:         %s\n", firstByte.Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "elapsed:      %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Fprintf(os.Stderr, "prompt tok:   %d (cache hit %d / miss %d, ratio %s)\n",
		c.PromptTokens, c.PromptCacheHitTokens, c.PromptCacheMissTokens, deepseek.FormatHitRatio(c))
	fmt.Fprintf(os.Stderr, "completion:   %d tok\n", c.CompletionTokens)
	fmt.Fprintf(os.Stderr, "est. cost:    %s (saved ~%d input tok via cache)\n",
		pricing.FormatCost(cost), tracker.SavedTokens())

	if activeSession != nil {
		fmt.Fprintf(os.Stderr, "session:      %s (--resume to continue)\n", activeSession.ID)
	}
	return nil
}

// jsonLine is the flat envelope for every JSONL event. Fields are
// omitempty so absent data doesn't clutter the output. Consumers should
// branch on Type; all other fields are type-specific.
//
// Type values (stable contract — breaking changes = major version bump):
//
//	agent_start      — one per run
//	turn_start       — one per LLM call; index is 0-based
//	text_delta       — incremental assistant text; delta is the new chunk
//	reasoning_delta  — incremental CoT text from thinking-mode responses
//	tool_start       — a tool call is about to execute; id/name/args set
//	tool_delta       — intermediate output from a streaming tool (think)
//	tool_end         — tool finished; result set on success, error on failure
//	turn_end         — LLM call settled; token counts + tool_calls count
//	agent_end        — run complete; cumulative stats; session_id if saved
//	error            — fatal error; message is the error string
type jsonLine struct {
	Type  string `json:"type"`
	Index int    `json:"index,omitempty"`
	// text_delta / reasoning_delta / tool_delta
	Delta     string `json:"delta,omitempty"`
	Reasoning bool   `json:"reasoning,omitempty"`
	// tool_start / tool_delta / tool_end
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	// error field for tool_end and error events
	Error string `json:"error,omitempty"`
	// turn_end / agent_end token accounting
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	CacheHitTokens   int `json:"cache_hit_tokens,omitempty"`
	ToolCalls        int `json:"tool_calls,omitempty"`
	// agent_end only
	Turns     int    `json:"turns,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// runJSON is the machine-readable output mode: one JSON object per line
// on stdout. Human-readable diagnostics (tier, stats footer) go to
// stderr so stdout stays parse-clean.
func runJSON(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, text string, activeSession *session.Session, store *session.Store) error {
	enc := json.NewEncoder(os.Stdout)

	emit := func(line jsonLine) {
		_ = enc.Encode(line) // json.Encoder always writes a trailing \n
	}

	var (
		turns     int
		toolCalls int
	)

	saveTurn := func() {
		if activeSession == nil || store == nil {
			return
		}
		activeSession.Messages = ag.Messages()
		activeSession.Turns = turns
		activeSession.ToolCalls = toolCalls
		activeSession.Usage = tracker.Cumulative()
		activeSession.Model = model
		activeSession.Yolo = yolo
		activeSession.Plan = plan
		// In non-TUI runs (runPrint / runJSON / runRPC) there is no
		// /effort command, so the agent's Effort is constant for the
		// lifetime of the process. Reading it off the agent keeps the
		// helper signature stable instead of growing another param.
		activeSession.Effort = ag.Effort()
		if err := store.Save(activeSession); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to save session after turn %d: %v\n", turns, err)
		}
	}

	emit(jsonLine{Type: "agent_start"})

	for ev := range ag.Prompt(ctx, text) {
		switch e := ev.(type) {

		case agent.TurnStart:
			emit(jsonLine{Type: "turn_start", Index: e.Index})

		case agent.MessageDelta:
			t := "text_delta"
			if e.Reasoning {
				t = "reasoning_delta"
			}
			emit(jsonLine{Type: t, Delta: e.Delta})

		case agent.ToolExecStart:
			emit(jsonLine{Type: "tool_start", ID: e.CallID, Name: e.Name, Args: e.Args})

		case agent.ToolDelta:
			emit(jsonLine{Type: "tool_delta", ID: e.CallID, Name: e.Name, Delta: e.Delta, Reasoning: e.Reasoning})

		case agent.ToolExecEnd:
			line := jsonLine{Type: "tool_end", ID: e.CallID, Name: e.Name}
			if e.Err != nil {
				line.Error = e.Err.Error()
			} else {
				line.Result = e.Result
				line.Bytes = len(e.Result)
			}
			emit(line)

		case agent.TurnEnd:
			tracker.Record(e.Usage, model, pricing.CurrentTier(time.Now()))
			turns++
			toolCalls += e.ToolCalls
			emit(jsonLine{
				Type:             "turn_end",
				Index:            e.Index,
				PromptTokens:     e.Usage.PromptTokens,
				CompletionTokens: e.Usage.CompletionTokens,
				CacheHitTokens:   e.Usage.PromptCacheHitTokens,
				ToolCalls:        e.ToolCalls,
			})
			saveTurn()

		case agent.ErrorEvent:
			emit(jsonLine{Type: "error", Error: e.Err.Error()})
			return e.Err
		}
	}

	c := tracker.Cumulative()
	end := jsonLine{
		Type:             "agent_end",
		Turns:            turns,
		ToolCalls:        toolCalls,
		PromptTokens:     c.PromptTokens,
		CompletionTokens: c.CompletionTokens,
		CacheHitTokens:   c.PromptCacheHitTokens,
	}
	if activeSession != nil {
		end.SessionID = activeSession.ID
	}
	emit(end)
	return nil
}

// runRPC starts a JSON-RPC 2.0 server over stdin/stdout. The server accepts
// requests for agent/prompt, agent/info, and session/list methods. Suitable
// for IDE integrations and scripted automation that need more control than the
// simple -p / --json modes.
func runRPC(ctx context.Context, ag *agent.Agent, tracker *cache.Tracker, model string, yolo, plan bool, activeSession *session.Session, store *session.Store) error {
	fmt.Fprintf(os.Stderr, "seek rpc: listening on stdin (JSON-RPC 2.0)\n")
	srv := seekrpc.New(ag, tracker, store, activeSession, model, yolo)
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}

// printSessionList renders the saved-sessions inventory to stdout
// for -list. Tabular but plain-text (no lipgloss) so it pipes cleanly
// into grep / awk.
func printSessionList(store *session.Store) error {
	infos, loadErrs, err := store.List()
	if err != nil {
		return err
	}
	for _, le := range loadErrs {
		fmt.Fprintf(os.Stderr, "warning: skipped unreadable session: %v\n", le)
	}
	if len(infos) == 0 {
		fmt.Println("no saved sessions in", store.Dir())
		return nil
	}
	fmt.Printf("%-25s  %-22s  %-10s  %5s  %5s  %s\n",
		"ID", "UPDATED (UTC)", "MODEL", "TURNS", "TOOLS", "PARENT")
	for _, s := range infos {
		parent := "-"
		if s.ParentID != "" {
			parent = s.ParentID
		}
		fmt.Printf("%-25s  %-22s  %-10s  %5d  %5d  %s\n",
			s.ID,
			s.UpdatedAt.Format("2006-01-02 15:04:05"),
			truncate(s.Model, 10),
			s.Turns,
			s.ToolCalls,
			parent)
	}
	return nil
}

func resolvePrompt(flagPrompt string) (string, error) {
	if flagPrompt != "" {
		return flagPrompt, nil
	}
	stat, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Don't split a multi-byte character at the boundary.
	// The loop caps at 3 iterations (max continuation bytes in a 4-byte rune).
	b := []byte(s[:n])
	for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return string(b) + "…"
}

// detectGlamourStyle picks "dark" or "light" for the TUI's Markdown
// renderer. We do this BEFORE entering bubbletea's alt-screen so that
// termenv's OSC 11 background-colour query/response handshake
// completes synchronously while we still own stdin. If we let glamour
// do the equivalent under bubbletea, the terminal's response (e.g.
// "]11;rgb:fae0/fae0/fae0\[1;1R") leaks straight into the textarea as
// garbage text.
//
// --theme overrides the detection. SEEK_STYLE=dark|light is a fallback
// when --theme=auto (the default).
func detectGlamourStyle(theme string) string {
	if theme == "dark" || theme == "light" {
		return theme
	}
	if v := os.Getenv("SEEK_STYLE"); v != "" {
		return v
	}
	if termenv.NewOutput(os.Stdout).HasDarkBackground() {
		return "dark"
	}
	return "light"
}

// runUpgrade resolves the latest GitHub release, downloads the asset
// for this platform, verifies its sha256 against the release's
// checksums.txt, and atomically replaces this binary. dryRun stops
// after the checksum verification step.
func runUpgrade(force, dryRun bool) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	_, err := upgrade.Run(ctx, upgrade.Options{
		Owner:    "whyiyhw",
		Repo:     "seek",
		Current:  tui.VersionString(),
		AllowDev: force,
		DryRun:   dryRun,
		Stderr:   os.Stderr,
		Stdout:   os.Stdout,
	})
	// ErrAlreadyLatest is not a failure; the orchestrator already
	// printed a friendly note to stderr.
	if err == upgrade.ErrAlreadyLatest {
		return nil
	}
	return err
}

// runUpgradeCheck prints whether a newer release is available, without
// downloading or modifying anything. Exits 0 in both "up to date" and
// "newer available" cases; non-zero only on transport / parse errors.
func runUpgradeCheck() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	rel, err := upgrade.Check(ctx, upgrade.Options{
		Owner:   "whyiyhw",
		Repo:    "seek",
		Current: tui.VersionString(),
	})
	if err != nil {
		return err
	}
	if rel == nil {
		fmt.Printf("seek is up to date (%s)\n", tui.VersionString())
		return nil
	}
	fmt.Printf("seek update available: %s → %s\n", tui.VersionString(), rel.TagName)
	fmt.Println("Run `seek -upgrade` to install.")
	return nil
}

// buildProvider selects and constructs the LLM provider based on the
// --provider flag, env vars, and ~/.seek/config.json (in that order).
// Returns:
//
//	provider    llm.Provider — non-nil for second-tier providers
//	dsClient    *deepseek.Client — non-nil for DeepSeek (first-class path)
//	provLabel   string — human name for TUI banner ("" = DeepSeek, no banner)
//	modelDefault string — sensible default model for the chosen provider
//
// Auth resolution (per provider): the canonical env var beats the
// config-file entry — see config.KeyFor. That order means CI and
// short-lived `KEY=... seek` invocations always win over what got
// written to disk by a previous setup wizard.
func buildProvider(provFlag, baseURLFlag, provName string) (
	provider llm.Provider, dsClient *deepseek.Client, provLabel, modelDefault string, err error,
) {
	// Load config once; ignore parse errors here so a malformed file
	// degrades to "env-only" rather than blocking startup. (Save() is
	// the place that aggressively reports config issues.)
	cfg, _ := config.Load()

	// Determine effective provider name. Order:
	//   1. --provider flag (explicit user intent)
	//   2. DeepSeek if its key is anywhere (env or config)
	//   3. Second-tier whose key is set, if no DeepSeek key
	//   4. cfg.DefaultProvider (the setup wizard writes this)
	//   5. "deepseek" as final fallback (errors out later if no key)
	if provFlag == "" {
		deepseekHas := config.KeyFor(cfg, "deepseek") != ""
		switch {
		case deepseekHas:
			provFlag = "deepseek"
		case config.KeyFor(cfg, "anthropic") != "":
			provFlag = "anthropic"
		case config.KeyFor(cfg, "openai") != "":
			provFlag = "openai"
		case config.KeyFor(cfg, "gemini") != "":
			provFlag = "gemini"
		case cfg.DefaultProvider != "":
			provFlag = cfg.DefaultProvider
		default:
			provFlag = "deepseek"
		}
	}

	switch provFlag {
	case "deepseek":
		apiKey := config.KeyFor(cfg, "deepseek")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no DeepSeek API key — set DEEPSEEK_API_KEY or run seek once to use the setup wizard")
		}
		return nil, deepseek.New(deepseek.WithAPIKey(apiKey)), "", deepseek.ModelV4Flash, nil

	case "anthropic":
		apiKey := config.KeyFor(cfg, "anthropic")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no Anthropic API key — set ANTHROPIC_API_KEY or save one via the setup wizard")
		}
		return anthropicprov.New(apiKey), nil, "Anthropic", "claude-sonnet-4-20250514", nil

	case "openai":
		apiKey := config.KeyFor(cfg, "openai")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no OpenAI API key — set OPENAI_API_KEY or save one via the setup wizard")
		}
		return openaiprov.New(apiKey), nil, "OpenAI", "gpt-4o", nil

	case "gemini":
		apiKey := config.KeyFor(cfg, "gemini")
		if apiKey == "" {
			return nil, nil, "", "", fmt.Errorf("no Gemini API key — set GEMINI_API_KEY or save one via the setup wizard")
		}
		return geminiprov.New(apiKey), nil, "Gemini", "gemini-2.0-flash", nil

	case "compatible":
		// Compatible endpoints don't have a canonical env var, so we
		// accept OPENAI_API_KEY or DEEPSEEK_API_KEY (common shapes for
		// vLLM/Ollama deployments) before checking config under the
		// provider's display name.
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("DEEPSEEK_API_KEY")
		}
		if apiKey == "" {
			apiKey = config.KeyFor(cfg, provName)
		}
		if baseURLFlag == "" {
			return nil, nil, "", "", fmt.Errorf("--base-url is required for --provider=compatible")
		}
		return compatible.New(apiKey, baseURLFlag, provName), nil, provName, "", nil

	default:
		return nil, nil, "", "", fmt.Errorf("unknown --provider %q; valid: deepseek | anthropic | openai | gemini | compatible", provFlag)
	}
}

// autoDistillEnabled returns true when $SEEK_AUTO_DISTILL is unset or set to
// a truthy value (1/true/yes/on). Controls memory_observe tool registration;
// default enabled because real-time notifications provide the safety net
// (PRD §6 v2).
func autoDistillEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SEEK_AUTO_DISTILL")))
	if v == "" {
		return true // default: enabled
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// envBoolTrue returns true when the named env var is set to a truthy
// value (1/true/yes/on). Empty/unset is false. Use for kill-switch /
// opt-in flags where "absent = disabled" is the safer default.
func envBoolTrue(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
