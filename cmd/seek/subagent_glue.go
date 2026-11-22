package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/whyiyhw/seek/internal/autopilot"
	"github.com/whyiyhw/seek/internal/bgjob"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/fsobserve"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/sandbox"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/tools/bash"
	"github.com/whyiyhw/seek/internal/tools/edit"
	"github.com/whyiyhw/seek/internal/tools/read"
	"github.com/whyiyhw/seek/internal/tools/write"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// ----- v5 柱 G subagent glue -----

// subagentRunnerOpts captures everything buildSubagentRunner needs
// to close over. Kept as a named struct (rather than a long
// parameter list) so future additions don't churn the call site.
type subagentRunnerOpts struct {
	client    *deepseek.Client
	provider  llm.Provider
	parentReg *tools.Registry
	// hooksFn resolves the child's hooks Registry per spawn — a
	// LAZY closure (not a pointer) because the parent's registry
	// keeps growing after Manager construction (memory hook
	// registers later), while the child must NEVER see the
	// background-completion Notifier: a child Prompt runs
	// ApplyPrePrompt too, and an unscoped Notifier would drain the
	// parent's pending background notices into the child's
	// transcript (feature-orchestration §4.1 D2). cmd/seek wires
	// hooksReg.CloneWithout(notifier). nil = no hooks for children.
	hooksFn   func() *hooks.Registry
	maxTokens int
	maxTurns  int
	getModel  func() string
	getEffort func() string
	// routes maps routing lane names (subagent.ProviderDeepSeek /
	// ProviderGLM / ProviderOpenCode — validated in
	// internal/subagent.ResolveRoute) to
	// the backend a child spawned on that lane should use
	// (feature-orchestration.md D7). Built once at startup from
	// config keys: a lane appears here only when its API key exists,
	// so "named provider without key" fails the spawn with a
	// structured error at Runner time instead of silently running on
	// the parent's lane. Nil/empty map = routing params always fail
	// (tests that don't exercise routing).
	routes map[string]subagentRoute
	// bashGuardLane, when non-empty, wraps the child's bash tool with the
	// no-remote guard bound to that lane (v7 柱 N: autopilot subagents deny
	// remote ops). "" = no guard. A lane label rather than a plain func so
	// the refusal names the lane it was actually mounted on (c_ajza7r).
	bashGuardLane string
	// bashSandbox, when true, confines the child's bash to its own
	// worktree via the OS sandbox (v7 柱 O integration; macOS only for
	// now). Each autopilot subagent thus can't write outside the worktree
	// it was given.
	bashSandbox bool
	// bashEnvKeep carries config.BashEnvPassthrough down to the child's
	// bash tool. Subagents run the SAME scrub as the parent — an
	// unattended child is exactly where a leaked credential is least
	// likely to be noticed, so the default is not relaxed here.
	bashEnvKeep []string
	// bgMgr backs the child bash tool's run_in_background. The child's
	// bash is rebuilt with the child (worktree) policy, so it needs the
	// background manager re-attached; nil disables background jobs in the
	// subagent (graceful — run_in_background just errors).
	bgMgr *bgjob.Manager
}

// laneGuard binds the no-remote check to the lane it is being mounted on:
// the shape bash.Tool.WithDeny wants, with the lane named in the refusal
// (c_ajza7r — a refusal must not claim a posture its lane doesn't have).
func laneGuard(lane string) func(command string) (bool, string) {
	return func(command string) (bool, string) {
		return autopilot.IsRemoteMutatingOnLane(lane, command)
	}
}

// subagentRoute is one lane entry in subagentRunnerOpts.routes.
// Exactly one of client / provider is set, mirroring agent.Config's
// two-path contract: client = first-class DeepSeek, provider = a
// pkg/llm lane (GLM rides pkg/llm/compatible with the pinned
// bigmodel.cn endpoint).
type subagentRoute struct {
	client   *deepseek.Client
	provider llm.Provider
}

// buildSubagentRoutes constructs the routing table for per-agent
// model routing (feature-orchestration.md D6/D7): deepseek always
// (reusing the parent's client when the parent lane IS deepseek, else
// building one from the config key), glm when GLM_API_KEY /
// providers.glm is configured, opencode when OPENCODE_API_KEY /
// providers.opencode is. Explicit names only — no key-probing
// chain (the "paseo provider 首次无头冒烟" pitfall): a lane is in the
// table iff its key exists.
//
// baseURLFlag is the parent's --base-url: the GLM and OpenCode routes
// resolve their endpoints through the same <lane>Endpoint precedence as
// the startup lane, so a Coding Plan preset or a proxy/mirror override
// (config/env or flag) also governs subagents — otherwise explore/plan
// children silently bill the wrong endpoint.
func buildSubagentRoutes(parentClient *deepseek.Client, baseURLFlag string) map[string]subagentRoute {
	cfg, _ := config.Load()
	routes := make(map[string]subagentRoute)
	if parentClient != nil {
		routes[subagent.ProviderDeepSeek] = subagentRoute{client: parentClient}
	} else if k := config.KeyFor(cfg, "deepseek"); k != "" {
		// Parent session runs a second-tier lane; deepseek routing is
		// still available when its key is configured.
		routes[subagent.ProviderDeepSeek] = subagentRoute{client: deepseek.New(deepseek.WithAPIKey(k))}
	}
	if k := config.KeyFor(cfg, "glm"); k != "" {
		routes[subagent.ProviderGLM] = subagentRoute{
			provider: newGLMProvider(cfg, k, baseURLFlag),
		}
	}
	if k := config.KeyFor(cfg, "opencode"); k != "" {
		routes[subagent.ProviderOpenCode] = subagentRoute{
			provider: newOpenCodeProvider(cfg, k, baseURLFlag),
		}
	} else if p, err := newOpenCodeOAuthProvider(cfg, baseURLFlag); err == nil {
		// Browser-login credentials count for children too — otherwise a
		// parent on the OAuth lane couldn't route explore/plan subagents
		// to it, which is the lane's main cost argument.
		routes[subagent.ProviderOpenCode] = subagentRoute{provider: p}
	}
	return routes
}

// buildSubagentRunner returns the production subagent.Runner that
// wires a child pkg/agent.Agent and drains its events into the
// child cache.Tracker.
//
// LIMITATION (M11.0): the child agent reuses the parent's Tool
// instances by NAME (filtered via the template's ToolNames). Those
// instances hold the PARENT's permission.Policy at construction,
// so the child's tightened Policy (per PRD §2.3) is NOT consulted
// by individual tool Check() calls. Net safety comes from one
// place: the per-template ToolNames whitelist.
//
// Practical consequences (after C.2 narrowing of the plan template
// in commit <this branch>):
//
//   - `explore` subagents are hard-safe (whitelist: read / grep /
//     list_dir / git / webfetch — no mutating tools at
//     all).
//   - `plan` subagents are hard-safe via the same whitelist
//     (after C.2: plan and explore share an identical tool
//     subset; only the Extra clause framing differs).
//   - `general-purpose` subagents see the parent's permission
//     level — which is INTENDED, since they inherit pref+workflow.
//     No leak.
//
// A future commit (v0.6.x dot release) will reconstruct per-spawn
// Registries with the child Policy threaded through so PRD §2.3
// monotonic-收紧 promise is hard-enforced at every tool gate —
// at which point `plan` can re-acquire access to the `propose`
// tool (currently dropped because propose's sink binds to the
// parent's session). See feature-subagent.md §9 "v0.6.x dot:
// per-spawn Registry 重建".
// writeSubagentTranscript persists the subagent's session
// JSONL to job.SessionDir/transcript.jsonl per PRD
// feature-subagent.md §3.3. PRD promised it; M11.0 production
// wiring missed it (audit G1). Without this `seek -resume
// <sub-sid>`, the /agents detail view (v0.6.x dot), and
// feature-inspect-rpc all have nothing to read.
//
// Best-effort: a write failure logs to stderr but does NOT
// fail the spawn. The subagent ran successfully by the time
// we reach here (Manager.MarkRun has already classified it as
// completed); losing the transcript shouldn't poison the
// parent's tool result.
//
// Extracted from buildSubagentRunner's closure so unit tests
// can exercise the file landing without spinning up the full
// pkg/agent.Agent + DeepSeek client + per-platform
// subprocess machinery.
func writeSubagentTranscript(job subagent.RunnerJob, msgs []deepseek.Message, model string, usage deepseek.Usage, turns int) {
	subSess := session.New(model, job.Policy.Cwd(), job.SystemPrompt, true, false)
	subSess.ID = job.SubSid
	subSess.Messages = msgs
	subSess.Turns = turns
	subSess.Usage = usage
	transcriptPath := filepath.Join(job.SessionDir, "transcript.jsonl")
	if err := session.SaveTo(transcriptPath, subSess); err != nil {
		fmt.Fprintf(os.Stderr, "subagent transcript save (%s): %v\n", job.SubSid, err)
	}
}

func buildSubagentRunner(opts subagentRunnerOpts) subagent.Runner {
	return func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
		// Per-agent model routing (feature-orchestration.md D7):
		// job.Provider non-empty → resolve the lane's backend from
		// the route table; empty → inherit the parent instances and
		// the live session model (the pre-routing path, unchanged).
		// The effective model feeds agent.Config, Tracker.Record
		// (per-lane pricing) and the transcript header alike.
		client, provider, model := opts.client, opts.provider, opts.getModel()
		if job.Provider != "" {
			r, ok := opts.routes[job.Provider]
			if !ok {
				return subagent.RunnerResult{}, fmt.Errorf(
					"provider %q has no configured API key — set its env var / config entry, or omit provider to inherit the parent lane", job.Provider)
			}
			client, provider = r.client, r.provider
			model = job.Model // non-empty by ResolveRoute contract
		}

		// Filter parent registry by ToolNames. The Filter the
		// Manager performs already strips `agent` and `ask_user`
		// universally; here we just project parent's registered
		// instances onto the survivor name list.
		// The policy-bearing tools (read/write/edit/bash) MUST be rebuilt
		// with the CHILD policy — for isolation:"worktree" that policy's
		// CWD is the worktree, so the subagent's file/shell ops stay inside
		// it. Reusing the parent's instances (which embed the parent policy
		// = main tree) silently breaks worktree isolation — found by
		// autopilot e2e (a subagent's edit hit the MAIN README). Other
		// tools (grep/git/…) are read-only or policy-free, so reusing
		// the parent instance is fine.
		childReg := tools.New()
		// Per-subagent observation set, NOT the parent's. A subagent
		// works in its own worktree; inheriting the parent's "already
		// read" set would let it blind-overwrite a same-named file in a
		// tree it never looked at — the exact isolation bug the child
		// policy exists to prevent.
		childObserved := fsobserve.New()
		for _, name := range job.ToolNames {
			switch name {
			case "read":
				childReg.Add(read.New(job.Policy).WithObserver(childObserved))
			case "write":
				childReg.Add(write.New(job.Policy).WithObserver(childObserved))
			case "edit":
				childReg.Add(edit.New(job.Policy).WithObserver(childObserved))
			case "bash":
				// 柱 N: deny remote ops; 柱 O: OS-sandbox to this worktree.
				bt := bash.New(job.Policy).WithEnvPassthrough(opts.bashEnvKeep)
				if opts.bgMgr != nil {
					bt = bt.WithBackground(opts.bgMgr)
				}
				if opts.bashGuardLane != "" {
					// Bind the guard to the lane this run is on so the
					// refusal names it (c_ajza7r).
					bt = bt.WithDeny(laneGuard(opts.bashGuardLane))
				}
				if opts.bashSandbox {
					if wt := job.Policy.CWD(); wt != "" && sandbox.Available() {
						bt = bt.WithSandbox(sandbox.Options{WritableDirs: []string{wt}})
					}
				}
				childReg.Add(bt)
			default:
				if t := opts.parentReg.Lookup(name); t != nil {
					childReg.Add(t)
				}
			}
		}

		// Build child agent. InitialMessages is nil for fresh spawns
		// (subagent gets a fresh context per PRD §2.1); continuations
		// (Manager.Continue) seed job.PriorMessages loaded from the
		// prior run's transcript — context continuity for follow-up
		// instructions. No PrepareMessages (suggester disabled for
		// subagents per PRD §3.5). Hooks ARE shared with the parent —
		// pre_tool deny / post_tool observer / audit log all apply to
		// subagent tool calls (PRD §5.3) — resolved through hooksFn
		// so parent-only hooks (the background Notifier) stay
		// excluded (see subagentRunnerOpts).
		var childHooks *hooks.Registry
		if opts.hooksFn != nil {
			childHooks = opts.hooksFn()
		}
		sub, err := agent.New(agent.Config{
			Client:          client,
			Provider:        provider,
			Model:           model,
			Effort:          opts.getEffort(),
			SystemPrompt:    job.SystemPrompt,
			InitialMessages: job.PriorMessages,
			Tools:           childReg,
			MaxTokens:       opts.maxTokens,
			MaxTurns:        opts.maxTurns,
			Hooks:           childHooks,
			// The child's conversation identity is its sub-sid (fresh
			// per run, continuations included) — a subagent on the
			// OpenCode lane thus gets its own stable x-opencode-session,
			// distinct from the parent's and from sibling spawns.
			SessionID: func() string { return job.SubSid },
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
				// Effective model, not the parent's — a GLM-routed child
				// prices at the GLM card (pricing.Cost keys off the model
				// id; NoOffPeakDiscount keeps flat-rate lanes flat).
				job.Tracker.Record(e.Usage, model, pricing.CurrentTier(time.Now()))
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
		for _, msg := range slices.Backward(msgs) {
			if msg.Role == deepseek.RoleAssistant && len(msg.ToolCalls) == 0 {
				summary = msg.Content
				break
			}
		}
		if summary == "" {
			return subagent.RunnerResult{}, fmt.Errorf("subagent produced no terminal assistant message")
		}

		// G1 fix: persist the subagent's transcript per PRD
		// feature-subagent.md §3.3. Extracted to a helper so
		// the persistence logic is unit-testable without
		// spinning up the full Runner closure.
		writeSubagentTranscript(job, msgs, model, totalUsage, turnCount)

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
