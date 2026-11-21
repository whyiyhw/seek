// Package agent is the DeepSeek-aware agent runtime: an LLM call + tool
// dispatch loop that emits a stream of typed Events on a channel.
//
// Two provider paths coexist (PRD §4.1):
//   - DeepSeek first-class: Config.Client != nil → full specialised path
//     with cache stats, reasoner stripping, and streaming tool-call assembly.
//   - Second-tier via llm.Provider: Config.Provider != nil → translates
//     deepseek.Message ↔ llm.Message and drives the generic ChatStream API.
//     DeepSeek-exclusive features (cache hit ratio, FIM, Reasoner) are not
//     available on this path; the TUI renders a banner to make that clear.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/modelcap"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// Config configures a new Agent. Exactly one of Client or Provider must
// be set; they are mutually exclusive provider paths.
type Config struct {
	// Client is the DeepSeek first-class path (PRD §4.1). Set this for
	// DeepSeek models; all DeepSeek-specific features are available.
	Client *deepseek.Client
	// Provider is the second-tier path (Anthropic / OpenAI / Gemini /
	// compatible). DeepSeek-exclusive features are unavailable on this path.
	Provider llm.Provider

	Model        string          // defaults to deepseek.ModelV41Flash / provider default
	SystemPrompt string          // optional
	Tools        *tools.Registry // optional — nil means no tools
	MaxTurns     int             // safety bound; defaults to 200
	MaxTokens    int             // completion token cap per call; 0 → defaultMaxTokens
	AutoContinue bool            // if true, inject "continue" on text-only turns so the model resumes mid-task without user input

	// Effort, when non-empty, overrides the per-request
	// reasoning_effort sent to DeepSeek and force-enables Thinking
	// regardless of ShouldEnableThinking(Model). Values: "" (no
	// override) | "high" | "max". Sourced from the session header
	// so a /effort selection persists across resume. Only honoured
	// on the DeepSeek path (Client != nil) — second-tier providers
	// expose their own thinking knobs out of band.
	Effort string

	// SafetyCheck, when non-empty, is sent as
	// deepseek.ChatRequest.SafetyCheck on every DeepSeek-lane request
	// ("high" — the only documented level; cmd/seek maps
	// config.SafetyCheckLevel() into it once per session). Empty = the
	// key is absent from the wire body so text-only requests stay
	// byte-identical (prefix-cache discipline). DeepSeek lane only: the
	// second-tier llm.ChatRequest has no such field. A fixed session
	// posture, not a per-turn knob — changing it mid-conversation would
	// alter every subsequent request body (the discipline /lang follows).
	//
	// When the check flags a response, runTurnDeepSeek records the
	// in-band safetyRefusalMarker instead of failing the turn.
	SafetyCheck string

	// ModeLabel is the current permission mode label for per-message
	// injection. Empty string → no reminder (the system-prompt tool
	// descriptions already cover Ask/Deny behaviour). Non-empty values
	// ("yolo", "plan") append a short mode reminder to every
	// user turn so the model immediately knows when /yolo or /plan
	// has changed the policy without invalidating the prefix
	// cache (the reminder is at the tail of the user message — only
	// those few bytes differ across turns).
	ModeLabel string

	// SessionID, when non-nil, is read before every second-tier LLM
	// request and sent as ChatRequest.SessionID — which the openai
	// client emits as x-opencode-session, the OpenCode Go lane's
	// per-conversation routing/prompt-cache contract. A func, not a
	// string: /compact forks the session under a new ID while the
	// agent object lives on, so the value must be read live at each
	// call (same pattern as cmd/seek's planBridge.sessionID). nil or
	// "" = no session identity (deepseek path, --no-save, tests) and
	// the header is simply not sent.
	SessionID func() string

	// InitialMessages, if non-empty, seeds the agent's history.
	// Used by --resume / --continue to restore a saved session. The
	// SystemPrompt is still placed first; InitialMessages are
	// appended after, in order.
	InitialMessages []deepseek.Message

	// ImageLoader, when set, materialises Asset-only image parts
	// (feature-vision) into data: URLs on every DeepSeek-path request
	// — the send-time sibling of StripReasoningContent's cleanup
	// station. nil disables resolution: messages keep their Asset
	// references and the request fails fast at marshal time if any
	// unresolved images are present (defensive — main wires this
	// whenever a session exists).
	ImageLoader deepseek.ImageLoader

	// FileResolver, when set, materialises Asset-referenced documents
	// into live Files API ids on every DeepSeek-path request
	// (feature-deepseek-app-api F4) — lazily uploading a part that has
	// none and re-uploading one whose recorded expiry has passed. It is
	// the send-time sibling of ImageLoader and carries the session's
	// transport cache, so one instance must live as long as the
	// session's agent (cmd/seek builds it once). nil leaves file parts
	// untouched: live ids still send, anything unhealable degrades to an
	// in-band note inside ResolveFiles.
	FileResolver *deepseek.FileResolver

	// Hooks, when non-nil, receive lifecycle callbacks: PrePrompt /
	// PreToolUse decorators (synchronous, ordered, can mutate the
	// request) and PreTurn / PostTurn / PostToolUse observers
	// (read-only). nil is a zero-cost no-op — Registry's dispatch
	// helpers are nil-receiver safe.
	//
	// Session lifecycle hooks (SessionStart / SessionEnd) are fired
	// by the caller (typically cmd/seek/main.go), not by the agent —
	// the agent doesn't know when its host program is "done".
	Hooks *hooks.Registry

	// PrepareMessages, when non-nil, runs as the final transform on
	// the message slice immediately before every ChatRequest is sent
	// — AFTER deepseek.StripReasoningContent has cleaned up
	// reasoning_content / predicted_next, but BEFORE the bytes leave
	// the process.
	//
	// Used by v4 柱 D (internal/suggester.InjectCalibration) to insert
	// a synthetic system note before the latest user message when the
	// prior turn's prediction missed. Kept generic — any future
	// "tweak messages just before send" feature can use the same
	// hook without modifying agent internals.
	//
	// Contract: must be PURE (no side effects); returning the input
	// unchanged is a valid no-op; must not mutate the input slice
	// (caller may reuse it later).
	PrepareMessages func([]deepseek.Message) []deepseek.Message
}

// Completion-token budgets. defaultMaxTokens is the historical cap
// (4096 → 8192 → 16384, each predating thinking-mode semantics).
// effortMaxMaxTokens aligns /effort max with DeepSeek's own implicit
// budget: when max_tokens is omitted the API defaults to 8K
// non-thinking / 64K thinking (api-docs.deepseek.com, Chat
// Completions). Reasoning tokens draw from the same budget as content
// (pitfalls.md "side-channel suggested-reply"), so a max-effort turn
// under the 16K cap routinely truncates with all budget spent on
// hidden reasoning and an empty content field.
const (
	defaultMaxTokens   = 16384
	effortMaxMaxTokens = 65536
)

// Agent holds the persistent state for one conversation. It is NOT safe for
// concurrent calls to Prompt; one Prompt at a time per Agent.
type Agent struct {
	mu       sync.RWMutex
	cfg      Config
	messages []deepseek.Message

	// explicitMaxTokens records whether Config.MaxTokens carried a
	// real user value at New() time (vs the <=0 → default fill). It
	// gates turnMaxTokens' effort linkage: an explicit --max-tokens
	// must keep winning over any effort-derived budget.
	explicitMaxTokens bool

	// currentEvents is the event sink for the in-flight Prompt call.
	// Set at the start of Prompt's goroutine, cleared (via defer) when
	// the goroutine exits. EmitEvent reads this field to forward
	// out-of-band events from tools (e.g. propose's approve/adjust/
	// cancel side effects). nil when no Prompt is active — EmitEvent
	// is then a no-op.
	//
	// Concurrency: Prompt is single-threaded by contract (see type
	// comment); the goroutine that sets/clears this field is the same
	// one that dispatches tools and therefore the same one that calls
	// EmitEvent. No mutex needed.
	currentEvents chan<- Event
}

// SetModel changes the active model. Safe to call between (not during) turns.
// The change takes effect on the next Prompt call.
func (a *Agent) SetModel(model string) {
	a.cfg.Model = model
}

// SetLane swaps the whole provider lane — the (Provider, Client, Model)
// triple — in one step. Provider and Client are mutually exclusive by
// construction (nil Provider ⇒ first-class DeepSeek Client lane), so a
// mid-provider switch (deepseek ↔ glm) must rewrite both halves together
// or runTurn's dispatch (cfg.Provider != nil) would read a torn lane.
// Same contract as SetModel: safe to call between (not during) turns;
// takes effect on the next Prompt call. The ACP backend is the caller —
// the transport layer rejects session/set_config_option while a prompt
// is in flight, so no concurrent turn can observe the swap (see
// internal/acp/server.go dispatch for the happens-before argument).
// Downstream readers that resolve the lane at call time (runTurn,
// summariseLLM) follow automatically; consumers that captured a client
// reference at construction (subagent routes) deliberately do NOT.
func (a *Agent) SetLane(provider llm.Provider, client *deepseek.Client, model string) {
	a.cfg.Provider = provider
	a.cfg.Client = client
	a.cfg.Model = model
}

// Model returns the active model id. Mirrors Effort()'s role: callers
// that need the CURRENT model at a moment other than construction
// (e.g. the ACP backend routing pasted images, runPrint picking the
// vision gate) read it off the agent instead of holding a second copy.
func (a *Agent) Model() string {
	return a.cfg.Model
}

// SetEffort changes the reasoning_effort override. Empty string clears
// the override (model default behaviour). Safe to call between (not
// during) turns; takes effect on the next Prompt call.
func (a *Agent) SetEffort(effort string) {
	a.cfg.Effort = effort
}

// Effort returns the current reasoning_effort override ("" when none).
// Exposed so adjacent components (e.g. the think tool) can mirror the
// session-level setting without holding a second copy of the state.
func (a *Agent) Effort() string {
	return a.cfg.Effort
}

// turnMaxTokens returns the completion cap for a DeepSeek turn request.
// An explicit --max-tokens wins unchanged; otherwise the cap tracks the
// reasoning effort — /effort max gets the 64K thinking budget DeepSeek
// itself would apply when max_tokens is omitted, every other effort
// state keeps the historical 16K default. Read inside the request-
// building block under the same between-turns contract as SetEffort.
func (a *Agent) turnMaxTokens() int {
	if a.explicitMaxTokens {
		return a.cfg.MaxTokens
	}
	if a.cfg.Effort == "max" {
		return effortMaxMaxTokens
	}
	return defaultMaxTokens
}

// SetModeLabel sets the per-message mode reminder label. Safe between
// turns; takes effect on the next Prompt call. Call with "" to clear
// (no reminder — the system-prompt tool descriptions suffice for
// Ask/Deny behaviour).
func (a *Agent) SetModeLabel(label string) {
	a.cfg.ModeLabel = label
}

// EmitEvent forwards an out-of-band event from a tool's Execute path
// into the active Prompt's event channel. Used by tools whose normal
// `Result` return value is insufficient to convey their side effects
// — propose's approve/adjust/cancel is the canonical case: the TUI
// needs to switch permission policy / mode reminder, which can't be
// derived from the tool result string alone.
//
// Safe to call from inside a tool's Execute. Outside an active Prompt
// the call is a no-op (currentEvents is nil); the agent loop's
// single-threaded contract (see Agent type comment) means there's no
// race between checking and using the field.
//
// The send is blocking — same semantics as the agent's own internal
// event emissions (ToolExecStart / ToolExecEnd / etc.). A slow
// consumer therefore back-pressures the tool's Execute, which is the
// intended behaviour.
func (a *Agent) EmitEvent(e Event) {
	ch := a.currentEvents
	if ch == nil {
		return
	}
	ch <- e
}

// modeReminder returns a per-message mode reminder suffix for the
// current permission mode. Empty string = no reminder needed (the
// system-prompt tool descriptions already cover Ask/Deny behaviour).
// The reminder is placed at the end of the user turn so recency bias
// makes it effective immediately after a /yolo or /plan toggle.
//
// Plan substates ("plan-analyze" / "plan-execute") are emitted by the
// plan-mode workflow in PRD docs/prd/feature-plan-mode.md. The TUI
// flips the label on PlanProposalApproved / PlanProposalAdjustRequested
// events from the propose tool. The literal "plan" label is preserved
// for back-compat with sessions and tests that pre-date the substate
// split — it's equivalent to plan-analyze.
func modeReminder(label string) string {
	switch label {
	case "yolo":
		return "\n\n[Mode: yolo — write, edit, and bash are unrestricted.]"
	case "plan":
		return "\n\n[Mode: plan — read-only. Do not call write, edit, or bash; produce a plan instead.]"
	case "plan-analyze":
		return "\n\n[Mode: plan-analyze — read context to define the problem and design a solution. Call propose(problem, steps) when you have enough context; use ask_user to clarify ambiguity. No writes until the user approves a plan.]"
	case "plan-execute":
		return "\n\n[Mode: plan-execute — the user approved your plan. Execute it step by step, narrating progress in chat. If the user signals disagreement, first summarize what's already done in chat, then call propose() again to re-plan. Stay within the approved scope; if you need to do something the plan didn't cover, re-propose before doing it.]"
	default:
		return ""
	}
}

// New constructs an Agent and seeds the system prompt (if any).
func New(cfg Config) (*Agent, error) {
	if cfg.Client == nil && cfg.Provider == nil {
		return nil, errors.New("agent: Config.Client or Config.Provider is required")
	}
	if cfg.Client != nil && cfg.Provider != nil {
		return nil, errors.New("agent: Config.Client and Config.Provider are mutually exclusive")
	}
	if cfg.Model == "" {
		if cfg.Client != nil {
			cfg.Model = deepseek.ModelV41Flash
		}
		// Provider callers must set Model explicitly — no universal default.
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 200
	}
	// Record explicitness BEFORE the default fill — an explicit
	// --max-tokens must keep winning over the effort linkage in
	// turnMaxTokens.
	explicitMaxTokens := cfg.MaxTokens > 0
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}
	a := &Agent{cfg: cfg, explicitMaxTokens: explicitMaxTokens}
	if cfg.SystemPrompt != "" {
		a.messages = append(a.messages, deepseek.Message{
			Role:    deepseek.RoleSystem,
			Content: cfg.SystemPrompt,
		})
	}
	// Restore prior conversation when resuming a session. We skip any
	// system message in the seed (we already added the current one
	// above; the saved one was for a previous run and may have
	// drifted — e.g. yolo state changed, CWD changed).
	for _, m := range cfg.InitialMessages {
		if m.Role == deepseek.RoleSystem {
			continue
		}
		a.messages = append(a.messages, m)
	}
	return a, nil
}

// appendMessage appends one or more messages to the agent's history under a
// write lock. Safe to call from the Prompt goroutine while Messages() is being
// read by the TUI goroutine.
func (a *Agent) appendMessage(msgs ...deepseek.Message) {
	a.mu.Lock()
	a.messages = append(a.messages, msgs...)
	a.mu.Unlock()
}

// Messages returns a copy of the current conversation history.
func (a *Agent) Messages() []deepseek.Message {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]deepseek.Message, len(a.messages))
	copy(out, a.messages)
	return out
}

// AttachPredictedNext writes text onto the PredictedNext field of the
// most recent text-content assistant message (i.e. the one that ended
// the last turn — len(ToolCalls)==0). No-op when no such message
// exists. Used by v4 柱 D's TUI suggester integration to persist a
// side-channel prediction so it round-trips through session save +
// resume and so the next turn's calibration check can read it back.
//
// PRD docs/prd/archive/feature-suggested-reply.md §4.5.
func (a *Agent) AttachPredictedNext(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, v := range slices.Backward(a.messages) {
		if v.Role == deepseek.RoleAssistant && len(v.ToolCalls) == 0 {
			// Write through the index: v is a copy of the slice element.
			a.messages[i].PredictedNext = text
			return
		}
	}
}

// Reset replaces the agent's non-system message history with the
// provided slice. The configured SystemPrompt is re-prepended verbatim;
// any system messages in `history` are dropped (mirrors New()'s rule
// — system role belongs to the agent config, not the conversation).
//
// Used by /compact to swap a long history for a short summary, and
// any future "rewind" UI. NOT safe to call while a Prompt is in flight.
func (a *Agent) Reset(history []deepseek.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = a.messages[:0]
	if a.cfg.SystemPrompt != "" {
		a.messages = append(a.messages, deepseek.Message{
			Role:    deepseek.RoleSystem,
			Content: a.cfg.SystemPrompt,
		})
	}
	for _, m := range history {
		if m.Role == deepseek.RoleSystem {
			continue
		}
		a.messages = append(a.messages, m)
	}
}

// workflowReminder is appended to every user message before it is sent to
// the LLM. Placing it at the end of the user turn keeps it in the recent
// context (recency bias makes it more effective than a system-prompt-only
// rule) while remaining a CONSTANT string so DeepSeek's prefix cache is
// not perturbed — the bytes are identical across every turn.
const workflowReminder = "\n\n[Workflow: grep→read(offset,limit≤50). edit→read first.]"

// summariserPrompt is appended as a user turn for the one-shot Chat
// call that produces a /compact summary. Tuned for ~400 words — long
// enough to preserve goals and decisions, short enough that the
// compacted history fits comfortably in the cache prefix.
const summariserPrompt = `Summarise the conversation so far into a compact briefing that lets a fresh assistant pick up where we left off:
- The user's overall goal and any constraints they mentioned.
- Key files, commands, or decisions made — name them explicitly.
- Outstanding questions or next steps.

Keep it under ~400 words. Bullet points where they help. Do not narrate that you are summarising; output only the briefing.`

// Summarise runs a one-shot non-tool Chat over the current history and
// returns the model's summary plus usage. The agent's own history is
// NOT modified — callers decide whether to swap in via Reset(). Tools
// are intentionally omitted from the request: we want prose, not a
// function call.
//
// When the history overflows the model's context window — the state
// /compact exists to rescue — the full-history request itself cannot
// fit, so the call degrades instead of failing: oversized bodies are
// truncated and the oldest messages dropped until the request fits
// (compact_rescue.go). Fitting histories go out byte-identical to the
// pre-rescue shape.
func (a *Agent) Summarise(ctx context.Context) (string, deepseek.Usage, error) {
	if a.cfg.Provider != nil {
		return a.summariseLLM(ctx)
	}
	history := append([]deepseek.Message{}, a.messages...)
	history = append(history, deepseek.Message{
		Role:    deepseek.RoleUser,
		Content: summariserPrompt,
	})

	// Attachments stripped: the briefing only needs prior TEXT (the
	// attached-image / attached-document markers live in Content),
	// and this side-channel call must not pay for — or depend on
	// resolving — image bytes or document ids.
	msgs := deepseek.WithoutFiles(deepseek.WithoutImages(deepseek.StripReasoningContent(history)))
	budgetTokens := rescueBudget(a.cfg.Model, a.cfg.MaxTokens)
	req := &deepseek.ChatRequest{
		Model:    a.cfg.Model,
		Messages: prepareSummariseMessages(msgs, budgetTokens),
	}
	resp, err := a.cfg.Client.Chat(ctx, req)
	if isContextOverflowError(err) {
		// The estimator said the request fit; the API disagrees. Retry
		// once at half budget — one halving absorbs far more than the
		// estimator's worst-case drift, and both attempts failing
		// surfaces the error unchanged. Re-derives from the original
		// msgs so the elision note is never doubled.
		retry := *req
		retry.Messages = prepareSummariseMessages(msgs, budgetTokens/2)
		resp, err = a.cfg.Client.Chat(ctx, &retry)
	}
	if err != nil {
		return "", deepseek.Usage{}, err
	}
	if len(resp.Choices) == 0 {
		return "", resp.Usage, errors.New("agent: summarise returned no choices")
	}
	return resp.Choices[0].Message.Content, resp.Usage, nil
}

// summariseLLM is the second-tier path for Summarise: uses ChatStream
// (no non-streaming equivalent in the llm.Provider interface). Carries
// the same compact-overflow rescue as the DeepSeek path — the deadlock
// shape (full history through an overflowing window) is lane-agnostic.
func (a *Agent) summariseLLM(ctx context.Context) (string, deepseek.Usage, error) {
	src := append([]deepseek.Message{}, a.messages...)
	src = append(src, deepseek.Message{Role: deepseek.RoleUser, Content: summariserPrompt})
	budgetTokens := rescueBudget(a.cfg.Model, a.cfg.MaxTokens)

	req := llm.ChatRequest{
		Model:     a.cfg.Model,
		Messages:  msgsToLLM(prepareSummariseMessages(src, budgetTokens)),
		MaxTokens: a.cfg.MaxTokens,
		SessionID: a.sessionID(),
	}
	summary, usage, err := drainSummariseStream(ctx, a.cfg.Provider, req)
	if isContextOverflowError(err) {
		// Same reactive story as the DeepSeek path: one retry at half
		// budget, re-derived from the original messages.
		retry := req
		retry.Messages = msgsToLLM(prepareSummariseMessages(src, budgetTokens/2))
		summary, usage, err = drainSummariseStream(ctx, a.cfg.Provider, retry)
	}
	return summary, usage, err
}

// drainSummariseStream runs one second-tier ChatStream summariser
// request to completion, collecting text deltas and usage.
func drainSummariseStream(ctx context.Context, p llm.Provider, req llm.ChatRequest) (string, deepseek.Usage, error) {
	stream, err := p.ChatStream(ctx, req)
	if err != nil {
		return "", deepseek.Usage{}, err
	}
	var sb strings.Builder
	var inputTokens, outputTokens int
	for ev := range stream {
		switch e := ev.(type) {
		case llm.TextDelta:
			sb.WriteString(e.Delta)
		case llm.TurnDone:
			inputTokens = e.InputTokens
			outputTokens = e.OutputTokens
		case llm.ErrorEvent:
			return "", deepseek.Usage{}, e.Err
		}
	}
	if ctx.Err() != nil {
		return "", deepseek.Usage{}, ctx.Err()
	}
	usage := deepseek.Usage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      inputTokens + outputTokens,
	}
	return sb.String(), usage, nil
}

// Prompt appends a user message and runs the agent loop. Events are emitted
// on the returned channel; the channel is closed when the loop terminates
// (final answer reached, max turns hit, ctx cancelled, or fatal error).
//
// The loop:
//  1. Append the user message.
//  2. Call ChatStream; assemble assistant message + any tool_call deltas.
//  3. If finish_reason="tool_calls": dispatch the batch PARTITIONED —
//     read-only calls concurrently, the rest sequentially in original
//     order, the two streams overlapping — then append tool result
//     messages in original call order, loop.
//  4. Otherwise: terminate.
//
// images (feature-vision) are attached to THIS user message only —
// the API accepts images on user messages exclusively, and this is
// the single user-message construction site. Callers that don't pass
// images are unaffected (variadic keeps every existing call site
// source-compatible).
func (a *Agent) Prompt(ctx context.Context, userText string, images ...deepseek.ImagePart) <-chan Event {
	return a.PromptFiles(ctx, userText, images, nil)
}

// PromptFiles is Prompt with document parts joined on the same user
// message (feature-deepseek-app-api F3): images and files are sibling
// attachment families, legal on user messages only. Prompt keeps its
// variadic signature for every existing caller; this slice-shaped
// sibling is the one new attachment surfaces route through (Go allows
// only one variadic, so the sibling is the non-breaking shape).
func (a *Agent) PromptFiles(ctx context.Context, userText string, images []deepseek.ImagePart, files []deepseek.FilePart) <-chan Event {
	out := make(chan Event, 32)

	go func() {
		defer close(out)
		// Stop observer (feature-orchestration D15): fires ONCE per
		// Prompt() round after the reply fully settles — final answer,
		// max turns, ctx cancel, or fatal error. Distinct granularity
		// from PostTurn (per model turn).
		//
		// Defer order (LIFO): currentEvents cleared → panic recover →
		// Stop → close(out). Stop still lands before the channel closes
		// (callers see the hook side effect) and after the sink is
		// detached; the recover sits between them only so a recovered
		// panic can set stopFinish="error" before the observer reads it.
		// The defer closes over these vars, so it reads their FINAL
		// values (passing the values inline would capture the zero
		// value — the classic defer-argument trap).
		settledTurns := 0
		stopFinish := "max_turns"
		defer func() {
			hookCtx := ctx
			if hookCtx == nil {
				hookCtx = context.Background()
			}
			// WithoutCancel: a cancelled turn must still report its
			// Stop event — the hook observes the outcome, it does not
			// inherit the cancellation that produced it.
			a.cfg.Hooks.NotifyStop(context.WithoutCancel(hookCtx), hooks.StopEvent{
				Turns:  settledTurns,
				Finish: stopFinish,
			})
		}()
		// Panic containment (c_vfgeam termination inventory): a panic
		// anywhere in the turn used to take the whole process down — on
		// the unattended ACP lane that is a silent death the client can
		// only attribute to a timeout. Recovered here it becomes a
		// first-class ErrorEvent, which every consumer already routes:
		// the TUI error view, and the ACP stopReason classifier
		// (internal_error, stack in the detail). Registered after the
		// Stop observer so LIFO runs it FIRST — a recovered panic still
		// reaches NotifyStop with Finish="error", and both land before
		// close(out).
		defer func() {
			if r := recover(); r != nil {
				stopFinish = "error"
				out <- ErrorEvent{Err: fmt.Errorf("agent: recovered from panic: %v\n%s", r, debug.Stack())}
			}
		}()
		// Expose the channel so tools can EmitEvent during their
		// Execute (e.g. propose pushes Plan* events on user choice).
		// Cleared before close(out) runs (LIFO defer order) so a
		// stale EmitEvent can't write to a soon-to-be-closed channel.
		a.currentEvents = out
		defer func() { a.currentEvents = nil }()

		// PrePromptHook: memory injection, context blocks, prompt
		// rewriting. Decorators are synchronous and ordered; an error
		// from any hook aborts the prompt before the user message is
		// committed to history.
		prePrompt, err := a.cfg.Hooks.ApplyPrePrompt(ctx, hooks.PrePromptIn{
			UserText: userText,
			History:  append([]deepseek.Message{}, a.messages...),
		})
		if err != nil {
			stopFinish = "error"
			out <- ErrorEvent{Err: err}
			return
		}
		for _, m := range prePrompt.Prepend {
			a.appendMessage(m)
		}
		userContent := prePrompt.UserText + modeReminder(a.cfg.ModeLabel) + workflowReminder
		userMsg := deepseek.Message{
			Role:    deepseek.RoleUser,
			Content: userContent,
		}
		if len(images) > 0 {
			userMsg.Images = images
		}
		if len(files) > 0 {
			userMsg.Files = files
		}
		a.appendMessage(userMsg)
		out <- AgentStart{}

		var (
			totalUsage     deepseek.Usage
			totalToolCalls int
			turns          int
			// lastFinish carries the most recent COMPLETED turn's finish
			// reason out of the loop for AgentEnd.Finish (turn-local
			// `finish` goes out of scope each iteration).
			lastFinish string
			// emptyPlaceholders counts CONSECUTIVE turns that closed with
			// emptyResponseMarker. Any turn that produces real content
			// resets it (see the reset below the retry loop).
			emptyPlaceholders int
			// truncationStreak counts CONSECUTIVE turns whose tool_calls
			// were salvaged after a finish_reason="length" cut. Any turn
			// that was not truncated resets it (same rule).
			truncationStreak int
		)

		for turn := 0; turn < a.cfg.MaxTurns; turn++ {
			turns = turn + 1
			out <- TurnStart{Index: turn}
			a.cfg.Hooks.NotifyPreTurn(ctx, hooks.PreTurnEvent{
				Index:   turn,
				History: append([]deepseek.Message{}, a.messages...),
			})

			// Retry loop. Two shapes are retryable within the same
			// budget (1 retry = 2 attempts) and backoff:
			//   1. stream cuts (unexpected EOF, SSE decode failures)
			//      that died mid-tool-call — the tool_call_ids are
			//      unverified, so the partial turn is NOT committed to
			//      history;
			//   2. a clean empty response (errEmptyResponse) — the
			//      server completed the stream with nothing in it.
			//      DeepSeek's guidance for empty completions is to
			//      retry, and runTurn returns the sentinel only when
			//      NOTHING reached the UI, so a retry cannot duplicate
			//      visible output.
			// Plain text streams cut off are NOT retried here; the
			// partial text is already visible and the user can say
			// "continue".
			const agentStreamRetries = 1
			const agentRetryBackoff = 500 * time.Millisecond

			var (
				assistant deepseek.Message
				usage     deepseek.Usage
				finish    string
				err       error
				// truncatedCalls is set by the finish=length salvage
				// below and read by the settle branch, so the nudge can
				// name how many calls were dropped.
				truncatedCalls int
			)

			for attempt := 0; attempt <= agentStreamRetries; attempt++ {
				assistant, usage, finish, err = a.runTurn(ctx, out)

				canRetry := false
				switch {
				case err != nil:
					// Clean empty response — safe to re-issue.
					canRetry = errors.Is(err, errEmptyResponse)
				default:
					// Only retry when the stream died while the model
					// was emitting tool calls.
					canRetry = len(assistant.ToolCalls) > 0 &&
						finish != "tool_calls" &&
						(strings.HasPrefix(finish, "stream_error:") ||
							strings.HasPrefix(finish, "decode_error:"))
				}
				canRetry = canRetry && attempt < agentStreamRetries && ctx.Err() == nil

				if !canRetry {
					break
				}

				// Brief backoff so the upstream can recover.
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-time.After(agentRetryBackoff):
				}
				if ctx.Err() != nil {
					break
				}
			}

			// Empty-completion exhaustion (card c_jdel6u): the retry
			// budget is spent and the upstream still produced nothing.
			// Returning an error here used to kill the whole turn — in
			// practice the whole session, since the run that hit it was
			// mid-task and half its context was unrecoverable (11-20
			// opencode:deepseek-v4.1-flash lane, 8+ occurrences).
			//
			// Instead record emptyResponseMarker as a REAL assistant
			// message and let the turn end normally. Two properties
			// matter:
			//   - the transcript stays valid: an assistant message with
			//     non-empty content satisfies DeepSeek's "content or
			//     tool_calls must be set" contract, so the next request
			//     is a legal continuation instead of a 400;
			//   - it is visible: the sentinel guarantees nothing streamed,
			//     so the UI block was never opened — open it and emit the
			//     marker as a delta (the TUI commits deltas, not
			//     MessageEnd's content, same as safetyRefusalMarker).
			placeholderTurn := false
			reasoningOnly := err != nil && errors.Is(err, errReasoningOnlyEmpty)
			if err != nil && ctx.Err() == nil &&
				(errors.Is(err, errEmptyResponse) || reasoningOnly) {
				// Reasoning-only (card c_7kco3z, defect 2): the thinking
				// block already streamed and was really billed, so
				// runTurn has ALREADY booked the marker as the content
				// and emitted the delta (it is the only place that
				// knows whether the block was open — same shape as
				// safetyRefusalMarker). Here we only do the loop
				// bookkeeping: no MessageStart, or the TUI would render
				// a second assistant block.
				if !reasoningOnly {
					assistant = deepseek.Message{
						Role:    deepseek.RoleAssistant,
						Content: emptyResponseMarker,
					}
					out <- MessageStart{Message: assistant}
					out <- MessageDelta{Delta: emptyResponseMarker}
				}
				// Keep usage (it was really billed) but the response is
				// complete-as-far-as-it-goes: finish=stop, no error.
				finish = "stop"
				err = nil
				placeholderTurn = true
			}
			if placeholderTurn {
				emptyPlaceholders++
			} else {
				// Any turn that produced something (text, tool_calls)
				// resets the streak: the gate is about CONSECUTIVE
				// placeholders, not a lifetime tally.
				emptyPlaceholders = 0
			}

			if err != nil {
				// User-initiated cancellation (Esc / Ctrl+C) is the
				// common case here. We must NOT append the partial
				// assistant message: if it contains tool_calls (even
				// half-streamed ones), the conversation history ends
				// up with orphan tool_call_ids that have no matching
				// tool result messages, and DeepSeek rejects every
				// subsequent turn with "tool_calls must be followed
				// by tool messages". Drop the turn entirely and let
				// the user continue from the prior valid state.
				if errors.Is(err, context.Canceled) || ctx.Err() != nil {
					stopFinish = "canceled"
					out <- AgentEnd{Usage: totalUsage, Turns: turns - 1, ToolCalls: totalToolCalls}
					return
				}
				stopFinish = "error"
				out <- ErrorEvent{Err: err}
				return
			}
			totalUsage = sumUsage(totalUsage, usage)

			// Invariant: tool_calls in the assistant message MUST be
			// confirmed by finish_reason="tool_calls". Anything else
			// (server hang-up before [DONE], SSE decode error, server
			// emitting finish="stop" alongside tool_calls) means the
			// tool_call_ids are unverified and persisting them would
			// orphan them — DeepSeek then rejects every subsequent
			// turn with "An assistant message with 'tool_calls' must
			// be followed by tool messages …". Drop the whole turn
			// and surface an error; the user can retry.
			//
			// The ctx-cancel path above is the gentle case (no error
			// surfaced because the user already knows they hit Esc);
			// this branch handles every other path to the same bad
			// state (including exhausted retries).
			//
			// finish_reason="length" is the ONE salvageable variant (card
			// c_7kco3z): the stream ended cleanly and the API told us why
			// — the output budget ran out mid-tool-call (11-21 session
			// 628903ab on the flash lane: two big Write calls hit the 16K
			// default). The unverified calls are still dropped (invariant
			// unchanged), but the turn is booked with a marker and the
			// loop nudges the model to resend smaller instead of killing
			// the session. Every other mismatched finish (stop/hang-up/
			// decode error) keeps the original death sentence.
			truncationSalvage := false
			if len(assistant.ToolCalls) > 0 && finish != "tool_calls" {
				if finish != "length" {
					stopFinish = "error"
					out <- ErrorEvent{Err: fmt.Errorf(
						"agent: refusing to commit turn — assistant emitted %d tool_call(s) but stream ended with finish_reason=%q",
						len(assistant.ToolCalls), finish)}
					return
				}
				truncationSalvage = true
				truncatedCalls = len(assistant.ToolCalls)
				// Drop the unverified calls, keep the text that really
				// streamed; if the budget went entirely into arguments
				// the block is open but blank, so stamp the in-band
				// marker (wire format, same discipline as
				// emptyResponseMarker) and stream it as a delta —
				// MessageStart already fired on the first tool_call
				// delta, so re-opening it would double the block.
				assistant.ToolCalls = nil
				if assistant.Content == "" {
					assistant.Content = truncationMarker
					out <- MessageDelta{Delta: truncationMarker}
				}
			}

			a.appendMessage(assistant)
			out <- MessageEnd{Message: assistant}

			// Surface a visible notice when the completion was cut by
			// the token limit so the user knows the response is
			// incomplete rather than seeing a silent truncation.
			if finish == "length" {
				out <- ErrorEvent{Err: fmt.Errorf(
					"agent: response truncated (finish_reason=length, max_tokens=%d) — use /compact to free context or ask me to continue",
					a.turnMaxTokens())}
			}

			toolCount := len(assistant.ToolCalls)
			if toolCount == 0 || finish != "tool_calls" {
				lastFinish = finish
				out <- TurnEnd{Index: turn, Usage: usage, ToolCalls: 0}
				a.cfg.Hooks.NotifyPostTurn(ctx, hooks.PostTurnEvent{
					Index:     turn,
					Usage:     usage,
					ToolCalls: 0,
					Finish:    finish,
				})
				settledTurns++
				// Truncated-tool_calls salvage (card c_7kco3z, defect 1):
				// the marker (or the text that did stream) is already
				// committed, so history is a legal continuation. Nudge
				// the model instead of dying: the point of the self-heal
				// is the corrective signal ("resend, one call at a time,
				// smaller arguments"), not carrying on silently. Two in
				// a row means the model can't fit its plan into the
				// budget at all — stop loudly rather than pad forever.
				if truncationSalvage {
					truncationStreak++
					if truncationStreak >= truncationHardStop {
						out <- ErrorEvent{Err: fmt.Errorf(
							"agent: 连续 %d 次 tool_calls 被输出上限截断（max_tokens=%d，已丢弃未确认调用并按 %s 记账）—— 输出预算不足以承载工具调用，停止本回合",
							truncationStreak, a.turnMaxTokens(), truncationMarker)}
						return
					}
					if turn < a.cfg.MaxTurns-1 {
						a.appendMessage(deepseek.Message{
							Role: deepseek.RoleUser,
							Content: fmt.Sprintf(
								"上回合 tool_calls 被输出上限截断（max_tokens=%d），共 %d 个调用未被执行。重发时一次一个调用、参数拆小（大文件分块写、大 JSON 分段提交），不要在同一条回复里堆多个大参数调用。",
								a.turnMaxTokens(), truncatedCalls) + modeReminder(a.cfg.ModeLabel),
						})
					}
					continue
				}
				if !placeholderTurn {
					// A turn that produced real text resets the streak; a
					// placeholder turn is not productive output, so
					// truncation → placeholder → truncation still counts
					// as two consecutive truncations upstream.
					truncationStreak = 0
				}
				// Placeholder turn: the marker is already committed above,
				// so history is non-empty and the next request is legal.
				// Keep going without asking the user — the point of the
				// self-heal is that a flaky upstream stops costing a whole
				// session. Give up after emptyPlaceholderHardStop
				// CONSECUTIVE placeholders: at that point the upstream is
				// not flaking, it is broken, and silently padding the
				// transcript forever is worse than a clear error.
				if placeholderTurn {
					if emptyPlaceholders >= emptyPlaceholderHardStop {
						out <- ErrorEvent{Err: fmt.Errorf(
							"agent: 连续空补全占位 %d 次 —— 上游持续返回空补全（已按 %s 记账），停止本回合",
							emptyPlaceholders, emptyResponseMarker)}
						return
					}
					// The placeholder is an assistant message, so history
					// now ENDS with assistant. DeepSeek rejects a request
					// whose last message is assistant unless beta prefix
					// mode is on ("The last message … must be a user
					// message, or an assistant message with prefix mode
					// on"), so the continuation has to be a user turn —
					// exactly what AutoContinue does. Without it the
					// self-heal would trade a 502-ish empty response for
					// a 400.
					if turn < a.cfg.MaxTurns-1 {
						a.appendMessage(deepseek.Message{
							Role:    deepseek.RoleUser,
							Content: "continue" + modeReminder(a.cfg.ModeLabel),
						})
					}
					continue
				}
				if a.cfg.AutoContinue && finish == "stop" && turn < a.cfg.MaxTurns-1 {
					a.appendMessage(deepseek.Message{
						Role:    deepseek.RoleUser,
						Content: "continue" + modeReminder(a.cfg.ModeLabel),
					})
					continue
				}
				// The round settles HERE: this turn produced no tool
				// calls, so Prompt() is about to return. finish is the
				// model's own reason ("stop" for a real answer,
				// "length" for a truncated one).
				stopFinish = finish
				if stopFinish == "" {
					stopFinish = "stop"
				}
				break
			}

			totalToolCalls += toolCount
			// A real tool-call turn is a normal productive turn: the
			// truncation streak is about CONSECUTIVE cuts.
			truncationStreak = 0

			// Partitioned dispatch: read-only calls run concurrently
			// (one goroutine each, writing its own index) while every
			// other call runs on ONE sequential goroutine in original
			// order — mutating tools keep their relative order among
			// themselves and permission prompts stay naturally serial.
			// The two streams overlap: the model issuing calls in one
			// batch declares them independent. Results append in
			// ORIGINAL call order after Wait so the conversation
			// prefix stays deterministic. StreamingTool safety: the
			// streaming tools are not ReadOnlyTool-marked (pinned by
			// TestNoToolStreamsAndIsReadOnly), so ToolDelta never fires
			// from the concurrent set and the TUI's shared live
			// buffers always have at most one writer.
			toolMsgs := make([]deepseek.Message, len(assistant.ToolCalls))
			partitioned := len(assistant.ToolCalls) >= 2
			var seqIdx []int
			var wg sync.WaitGroup
			for i, tc := range assistant.ToolCalls {
				if partitioned && readOnlyCall(tc, a.cfg.Tools) {
					wg.Add(1)
					go func(i int, tc deepseek.ToolCall) {
						defer wg.Done()
						result, terr := a.dispatchTool(ctx, tc, out)
						toolMsgs[i] = buildToolResultMsg(tc.ID, result, terr)
					}(i, tc)
				} else {
					seqIdx = append(seqIdx, i)
				}
			}
			if len(seqIdx) > 0 {
				wg.Go(func() {
					for _, i := range seqIdx {
						tc := assistant.ToolCalls[i]
						result, terr := a.dispatchTool(ctx, tc, out)
						toolMsgs[i] = buildToolResultMsg(tc.ID, result, terr)
					}
				})
			}
			wg.Wait()
			for _, msg := range toolMsgs {
				out <- MessageStart{Message: msg}
				a.appendMessage(msg)
				out <- MessageEnd{Message: msg}
			}

			lastFinish = finish
			out <- TurnEnd{Index: turn, Usage: usage, ToolCalls: toolCount}
			a.cfg.Hooks.NotifyPostTurn(ctx, hooks.PostTurnEvent{
				Index:     turn,
				Usage:     usage,
				ToolCalls: toolCount,
				Finish:    finish,
			})
			settledTurns++
		}

		out <- AgentEnd{Usage: totalUsage, Turns: turns, ToolCalls: totalToolCalls, Finish: lastFinish}
	}()

	return out
}

// errEmptyResponse marks a completed stream that carried no content and
// no tool calls — and emitted no events either. runTurn returns it only
// in that shape, which is what makes it safe for the retry loop in
// Prompt to re-issue the request: nothing reached the screen, so a
// retry cannot duplicate visible output.
//
// The reasoning-only variant (thinking streamed, then the response ended
// with no content) has its own sentinel, errReasoningOnlyEmpty: same text,
// separate identity, so the retry loop keeps re-issuing ONLY the clean
// variant (nothing was rendered, a retry can't duplicate) while Prompt's
// self-heal gate accepts both. finish=length is excluded from both: the
// budget was the problem, and an identical retry would be deterministic
// waste.
var errEmptyResponse = errors.New("agent: model returned an empty response (no content and no tool calls)")

// errReasoningOnlyEmpty marks the sibling shape: thinking streamed (so the
// UI block is already open and the tokens were really billed) and then the
// response ended with no content and no tool calls. Same sentinel text as
// errEmptyResponse, deliberately NOT retried — a retry would render the
// reasoning twice (card c_geyjoo: the flash lane's natural failure pose;
// it used to be a hard error, i.e. a session death sentence). Prompt's
// self-heal gate accepts it alongside errEmptyResponse and books
// emptyResponseMarker as the content.
var errReasoningOnlyEmpty = errors.New("agent: model returned an empty response (no content and no tool calls)")

// emptyResponseMarker is the in-band assistant content recorded when the
// clean-empty retry budget is exhausted and the upstream still returns
// nothing. Like safetyRefusalMarker it is wire format for the transcript —
// resume replays assistant content verbatim and the next request's history
// carries it as ordinary text, so change the text only with a migration in
// mind (same rule as the `[plan: …]` markers). Non-empty on purpose: an
// assistant message with neither content nor tool_calls makes DeepSeek
// reject the following request with "content or tool_calls must be set",
// which is exactly the 400 this placeholder exists to prevent.
const emptyResponseMarker = "[empty: 上游空补全，已重试1次]"

// emptyPlaceholderHardStop is the number of CONSECUTIVE placeholder turns
// allowed before Prompt gives up. A single flake is worth self-healing
// through; two in a row means the upstream is not flaking, and padding the
// transcript with markers indefinitely only hides a broken lane.
const emptyPlaceholderHardStop = 2

// truncationMarker is the in-band assistant content recorded when a turn
// ended with finish_reason="length" mid-tool_call and no text had
// streamed before the cut. Same wire-format discipline as
// emptyResponseMarker: it lands in the transcript, so change the text
// only with a migration in mind. Its job is to keep the assistant
// message non-empty ("content or tool_calls must be set") and to make
// the truncation visible in the transcript rather than implicit.
const truncationMarker = "[truncated: 输出上限截断，未确认的 tool_calls 已丢弃]"

// truncationHardStop is the number of CONSECUTIVE finish=length salvages
// allowed before Prompt gives up. Signalled upstream triage; two in a
// row means the model cannot fit its next step into the budget at all.
const truncationHardStop = 2

// runTurn routes to the DeepSeek or second-tier path depending on which
// provider was configured.
func (a *Agent) runTurn(ctx context.Context, out chan<- Event) (deepseek.Message, deepseek.Usage, string, error) {
	if a.cfg.Provider != nil {
		return a.runTurnLLM(ctx, out)
	}
	return a.runTurnDeepSeek(ctx, out)
}

// safetyRefusalMarker is the in-band assistant content recorded when
// DeepSeek's model safety check refuses a response instead of producing
// prose. It is wire format for the transcript — resume replays assistant
// content verbatim, and `seek -resume` users (plus internal consumers
// like the /goal judge) read it as "the model declined"; change the text
// only with a migration in mind (same rule as the `[plan: …]` markers).
const safetyRefusalMarker = "[safety: 拒答（safety_check 命中）]"

// isSafetyRefusal reports whether a finish_reason means "the model's
// safety filter stopped this response". DeepSeek documents "refusal"
// (the safety_check hit signal); "content_filter" is the same class of
// terminal outcome on adjacent OpenAI-compatible shapes. Applied in
// runTurnDeepSeek — the lane the safety_check parameter belongs to; the
// second-tier path passes provider stop reasons through untouched.
func isSafetyRefusal(finish string) bool {
	return finish == "refusal" || finish == "content_filter"
}

// runTurnDeepSeek is the original DeepSeek-specific streaming path.
func (a *Agent) runTurnDeepSeek(ctx context.Context, out chan<- Event) (deepseek.Message, deepseek.Usage, string, error) {
	msgs := deepseek.StripReasoningContent(a.messages)
	// Capability gate on history replay: images are only ever legal on
	// a vision model. The submit-time router guards NEW attachments,
	// but /model can switch a session to a non-vision model while
	// image-bearing messages sit in history — sending them would 400
	// the whole turn. Drop them (in-band markers stay in Content, so
	// the transcript remains self-describing). modelcap is the single
	// capability authority; its static table + the OpenCode catalogue
	// override cover every lane, not just this one.
	if len(msgs) > 0 && !modelcap.Vision(a.cfg.Model) {
		msgs = deepseek.WithoutImages(msgs)
	}
	// feature-vision: materialise Asset references into data URLs at
	// the same send-time station. Load failures degrade in-band inside
	// ResolveImages — they never abort the turn.
	if a.cfg.ImageLoader != nil {
		msgs = deepseek.ResolveImages(msgs, a.cfg.ImageLoader)
	}
	// feature-deepseek-app-api F4: same station for documents — lazily
	// upload a missing file_id, re-upload an expired one from the local
	// asset, degrade in-band on failure. Never aborts the turn.
	if a.cfg.FileResolver != nil {
		msgs = deepseek.ResolveFiles(ctx, msgs, a.cfg.FileResolver)
	}
	if a.cfg.PrepareMessages != nil {
		msgs = a.cfg.PrepareMessages(msgs)
	}
	req := &deepseek.ChatRequest{
		Model:     a.cfg.Model,
		Messages:  msgs,
		MaxTokens: a.turnMaxTokens(),
	}
	// V4 shipped Thinking as a request parameter rather than a separate
	// model id. Pre-V4, DeepSeek routed the "deepseek-reasoner" alias
	// (removed 2026-07-24) to a V4 model without setting Thinking,
	// silently falling back to non-reasoning V4-Flash behaviour — the
	// bug users hit when they picked "reasoner" expecting CoT and got
	// fast-chat. Opt reasoning models in here so the picker label
	// matches reality.
	if deepseek.ShouldEnableThinking(a.cfg.Model) {
		req.Thinking = &deepseek.ThinkingMode{Type: "enabled"}
	}
	// /effort overrides the model-implied default: a user pick of
	// high/max wins over ShouldEnableThinking's silence on V4-Flash,
	// because the explicit intent ("I want this turn to think harder")
	// trumps the model's stock behaviour. Empty Effort = no override.
	if a.cfg.Effort != "" {
		req.ReasoningEffort = a.cfg.Effort
		req.Thinking = &deepseek.ThinkingMode{Type: "enabled"}
	}
	if a.cfg.Tools != nil {
		req.Tools = a.cfg.Tools.Wire()
	}
	// Model safety check (config `safety_check: "high"`, read once at
	// session startup). Absent when off, keeping the body byte-identical
	// to a pre-SC1 request — the prefix cache keys on those bytes.
	if a.cfg.SafetyCheck != "" {
		req.SafetyCheck = &deepseek.SafetyCheck{Level: a.cfg.SafetyCheck}
	}

	stream, err := a.cfg.Client.ChatStream(ctx, req)
	if err != nil {
		return deepseek.Message{}, deepseek.Usage{}, "", err
	}

	assistant := deepseek.Message{Role: deepseek.RoleAssistant}
	started := false

	// Tool-call assembly: DeepSeek streams tool_calls as deltas keyed by
	// `index`. The first delta for an index has id/type/name; subsequent
	// deltas just append to function.arguments.
	pending := map[int]*deepseek.ToolCall{}
	maxIdx := -1

	var (
		usage  deepseek.Usage
		finish string
	)

	for ev := range stream {
		switch ev.Type {
		case deepseek.EventDelta:
			if !started {
				out <- MessageStart{Message: assistant}
				started = true
			}
			assistant.Content += ev.Delta
			out <- MessageDelta{Delta: ev.Delta, Reasoning: false}

		case deepseek.EventReasoningDelta:
			if !started {
				out <- MessageStart{Message: assistant}
				started = true
			}
			assistant.ReasoningContent += ev.Delta
			out <- MessageDelta{Delta: ev.Delta, Reasoning: true}

		case deepseek.EventToolCallDelta:
			if !started {
				out <- MessageStart{Message: assistant}
				started = true
			}
			tc := ev.ToolCall
			cur, ok := pending[tc.Index]
			if !ok {
				cur = &deepseek.ToolCall{Index: tc.Index, Type: "function"}
				pending[tc.Index] = cur
				if tc.Index > maxIdx {
					maxIdx = tc.Index
				}
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" {
				cur.Type = tc.Type
			}
			if tc.Function.Name != "" {
				cur.Function.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				cur.Function.Arguments += tc.Function.Arguments
			}

		case deepseek.EventDone:
			usage = ev.Usage
			finish = ev.FinishReason
		}
	}

	if maxIdx >= 0 {
		assistant.ToolCalls = make([]deepseek.ToolCall, 0, maxIdx+1)
		for i := 0; i <= maxIdx; i++ {
			tc, ok := pending[i]
			if !ok {
				continue
			}
			// Clear Index on the persisted message — it's a stream-only
			// field; storing it would leak into subsequent request bodies.
			tc.Index = 0
			assistant.ToolCalls = append(assistant.ToolCalls, *tc)
		}
	}

	// Surface context cancellation as an explicit error so the agent
	// loop can drop the partial turn (see the user-cancel branch in
	// Prompt). Without this, a stream interrupted by Esc returns a
	// nil error here, the partial assistant message gets appended,
	// and any tool_calls it contains have no matching tool results —
	// poisoning the next API request.
	if err := ctx.Err(); err != nil {
		return assistant, usage, finish, err
	}

	// Safety refusal (config `safety_check: "high"`): the model declined
	// to answer and the API terminated the stream with finish_reason
	// "refusal". That is a legitimate terminal outcome, NOT a stream
	// failure — the documented client contract is to replace whatever
	// was shown with the refusal text. Record the in-band marker as the
	// assistant content and let the turn end normally: no ErrorEvent, no
	// retry (a retried refusal just re-triggers the filter and burns
	// tokens). Placed BEFORE the empty-response guard so a contentless
	// refusal isn't mistaken for a silent model.
	if isSafetyRefusal(finish) {
		assistant.Content = safetyRefusalMarker
		// A refusal has no answer to act on. Drop any half-streamed
		// tool_calls — Prompt's invariant check requires
		// tool_calls ⇔ finish_reason="tool_calls", and a refusal can
		// never carry that pairing.
		assistant.ToolCalls = nil
		if !started {
			out <- MessageStart{Message: assistant}
			started = true
		}
		// Surface the marker in the live stream too: usually nothing
		// streamed before the refusal, so without this delta the TUI
		// would commit an empty assistant block and the turn would end
		// silently on screen. (If a partial answer did stream, the
		// marker follows it as the "this was replaced" notice.)
		out <- MessageDelta{Delta: safetyRefusalMarker}
	}

	// Empty-response guard: the assistant message has nothing to say.
	// Committing it would leave an orphan assistant turn with no content
	// and no tool_calls, which DeepSeek rejects on the next request as
	// "Invalid assistant message: content or tool_calls must be set"
	// (PRD §4.5.1, pitfalls/message-contract.md).
	//
	// Split on `started` (did any event reach `out`?):
	//   - nothing streamed + normal finish → retryable sentinel; the
	//     retry loop re-issues the request, and since attempt 1 emitted
	//     no events there is nothing to duplicate on screen;
	//   - reasoning streamed, then silence → the partial block is
	//     already visible, so it is NOT retried (a retry would
	//     double-render the thinking). Since card c_7kco3z it is no
	//     longer a hard error either: it returns errReasoningOnlyEmpty
	//     after booking emptyResponseMarker as the content, which
	//     Prompt's self-heal gate treats like the clean-empty case. The
	//     reasoning_content stays on the message (it was really billed
	//     and already rendered; the send path strips it, so no wire
	//     cost). The marker delta goes out HERE, next to the block that
	//     is open — emitting a MessageStart would open a second
	//     assistant block in the TUI.
	if assistant.Content == "" && len(assistant.ToolCalls) == 0 {
		if !started && (finish == "" || finish == "stop") {
			return assistant, usage, finish, errEmptyResponse
		}
		// Only the STARTED shape is the reasoning-only salvage. A
		// !started empty with some other finish (e.g. "length") emitted
		// nothing at all — there is no visible block to keep and the
		// old hard error is still the right diagnostic.
		if started {
			assistant.Content = emptyResponseMarker
			out <- MessageDelta{Delta: emptyResponseMarker}
			return assistant, usage, finish, errReasoningOnlyEmpty
		}
		return assistant, usage, finish,
			errors.New("agent: model returned an empty response (no content and no tool calls)")
	}

	if !started {
		// Defensive: unreachable today — every emission path sets
		// `started`, so !started implies the guard above returned. Kept
		// so a future emission channel that forgets to set it still
		// opens the UI's assistant block instead of rendering nothing.
		out <- MessageStart{Message: assistant}
	}

	return assistant, usage, finish, nil
}

// sessionID returns the live conversation identity for
// ChatRequest.SessionID (x-opencode-session on the OpenCode lane); ""
// when Config.SessionID is unset.
func (a *Agent) sessionID() string {
	if a.cfg.SessionID == nil {
		return ""
	}
	return a.cfg.SessionID()
}

// runTurnLLM is the second-tier streaming path using llm.Provider.
// It translates messages to/from the generic format and reassembles the
// assistant turn as a deepseek.Message so the rest of the agent loop
// (history, session save, TUI rendering) is unchanged.
func (a *Agent) runTurnLLM(ctx context.Context, out chan<- Event) (deepseek.Message, deepseek.Usage, string, error) {
	// Same attachment station as the DeepSeek path (see
	// runTurnDeepSeek), in the same order: capability gate → resolve
	// assets → PrepareMessages → convert. The gates are evaluated here,
	// at send time, because /model can swap the model (and ACP the
	// whole lane) between the submit and this request.
	prepared := a.messages
	vision := modelcap.Vision(a.cfg.Model)
	docs := modelcap.Files(a.cfg.Model)
	if len(prepared) > 0 && !vision {
		prepared = deepseek.WithoutImages(prepared)
	}
	if a.cfg.ImageLoader != nil {
		prepared = deepseek.ResolveImages(prepared, a.cfg.ImageLoader)
	}
	// Documents on the second-tier path materialise as data: URLs (the
	// Files API's file_id is DeepSeek-wire-only). The FileResolver's
	// loader is wired on every lane; its Upload half stays nil here.
	if docs && a.cfg.FileResolver != nil && a.cfg.FileResolver.Load != nil {
		prepared = deepseek.ResolveFileData(prepared, a.cfg.FileResolver.Load)
	}
	if a.cfg.PrepareMessages != nil {
		prepared = a.cfg.PrepareMessages(prepared)
	}
	// msgsToLLMWithAttachments is the byte-shape conversion; it shouldn't
	// know about calibration semantics — and it must not decide
	// capability itself (the gates above already did).
	req := llm.ChatRequest{
		Model:     a.cfg.Model,
		Messages:  msgsToLLMWithAttachments(prepared, vision, docs),
		Tools:     toolsToLLM(a.cfg.Tools),
		MaxTokens: a.cfg.MaxTokens,
		SessionID: a.sessionID(),
	}

	stream, err := a.cfg.Provider.ChatStream(ctx, req)
	if err != nil {
		return deepseek.Message{}, deepseek.Usage{}, "", err
	}

	assistant := deepseek.Message{Role: deepseek.RoleAssistant}
	started := false
	var finish string
	var inputTokens, outputTokens, cachedInput int

	for ev := range stream {
		switch e := ev.(type) {
		case llm.TextDelta:
			if !started {
				out <- MessageStart{Message: assistant}
				started = true
			}
			assistant.Content += e.Delta
			out <- MessageDelta{Delta: e.Delta, Reasoning: false}

		case llm.ToolCallDone:
			if !started {
				out <- MessageStart{Message: assistant}
				started = true
			}
			assistant.ToolCalls = append(assistant.ToolCalls, deepseek.ToolCall{
				ID:   e.ID,
				Type: "function",
				Function: deepseek.ToolCallFunc{
					Name:      e.Name,
					Arguments: e.Arguments,
				},
			})

		case llm.TurnDone:
			finish = e.FinishReason
			inputTokens = e.InputTokens
			outputTokens = e.OutputTokens
			cachedInput = e.CachedInputTokens

		case llm.ErrorEvent:
			return assistant, deepseek.Usage{}, "", e.Err
		}
	}

	if ctx.Err() != nil {
		return assistant, deepseek.Usage{}, finish, ctx.Err()
	}

	// Empty-response guard: same rationale AND same two-shape split as
	// runTurnDeepSeek — a clean empty returns the retryable sentinel
	// (nothing was emitted); the started-but-empty shape is not retried
	// and, since card c_7kco3z, returns errReasoningOnlyEmpty with the
	// marker booked as content so Prompt self-heals instead of dying.
	if assistant.Content == "" && len(assistant.ToolCalls) == 0 {
		if !started && (finish == "" || finish == "stop") {
			return assistant, deepseek.Usage{}, finish, errEmptyResponse
		}
		// Same two-shape split as runTurnDeepSeek: only a STARTED block
		// is salvageable; a stream that emitted nothing at all with an
		// odd finish keeps the original hard error.
		if started {
			assistant.Content = emptyResponseMarker
			out <- MessageDelta{Delta: emptyResponseMarker}
			return assistant, deepseek.Usage{}, finish, errReasoningOnlyEmpty
		}
		return assistant, deepseek.Usage{}, finish,
			errors.New("agent: model returned an empty response (no content and no tool calls)")
	}

	if !started {
		// Defensive: see runTurnDeepSeek — unreachable by construction.
		out <- MessageStart{Message: assistant}
	}

	// Map the provider's usage onto deepseek.Usage's cache-hit/miss
	// split so pricing.Cost prices all three buckets. Without this the
	// hit/miss fields stay 0 and Cost — which bills input ONLY via those
	// two buckets — reports input cost $0 on every second-tier turn
	// (the GLM lane's "$0 statistics" bug). Providers that report no
	// cache detail (CachedInputTokens=0) price the full input at the
	// miss rate: conservative, never under-bills.
	cached := min(cachedInput, inputTokens)
	usage := deepseek.Usage{
		PromptTokens:          inputTokens,
		CompletionTokens:      outputTokens,
		TotalTokens:           inputTokens + outputTokens,
		PromptCacheHitTokens:  cached,
		PromptCacheMissTokens: inputTokens - cached,
	}
	return assistant, usage, finish, nil
}

// dispatchTool emits ToolExecStart/End around a single tool invocation.
// When the tool implements tools.StreamingTool, intermediate output is
// piped through as ToolDelta events so the TUI can render reasoner
// chain-of-thought live instead of staring at a spinner for 30+ seconds.
func (a *Agent) dispatchTool(ctx context.Context, tc deepseek.ToolCall, out chan<- Event) (string, error) {
	out <- ToolExecStart{
		CallID: tc.ID,
		Name:   tc.Function.Name,
		Args:   tc.Function.Arguments,
	}

	var args json.RawMessage
	if tc.Function.Arguments != "" {
		args = json.RawMessage(tc.Function.Arguments)
	} else {
		args = json.RawMessage("{}")
	}

	if a.cfg.Tools == nil {
		err := fmt.Errorf("agent: no tools registered; LLM requested %q", tc.Function.Name)
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Err: err, Args: tc.Function.Arguments}
		return "", err
	}

	tool := a.cfg.Tools.Lookup(tc.Function.Name)
	if tool == nil {
		err := fmt.Errorf("%w: %s (known: %v)", tools.ErrUnknownTool, tc.Function.Name, a.cfg.Tools.Names())
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Err: err, Args: tc.Function.Arguments}
		return "", err
	}

	// PreToolUseHook: permission gates, audit, secret redaction. Deny
	// short-circuits — the deny text becomes the tool result (mirrors
	// permission.ErrDenied flow). Args rewrite is reserved for narrow
	// uses like redacting secrets out of bash commands.
	preTool, hookErr := a.cfg.Hooks.ApplyPreToolUse(ctx, hooks.PreToolUseIn{
		CallID: tc.ID,
		Name:   tc.Function.Name,
		Args:   args,
	})
	if hookErr != nil {
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Err: hookErr, Args: tc.Function.Arguments}
		return "", hookErr
	}
	if preTool.Deny != "" {
		a.cfg.Hooks.NotifyPostToolUse(ctx, hooks.PostToolUseEvent{
			CallID: tc.ID, Name: tc.Function.Name, Args: args, Result: preTool.Deny,
		})
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Result: preTool.Deny, Args: tc.Function.Arguments}
		return preTool.Deny, nil
	}
	if preTool.Args != nil {
		args = preTool.Args
	}

	var (
		result string
		err    error
	)
	// Panic containment for the tool body (c_vfgeam): tools run on
	// dispatch goroutines of their own (partitioned dispatch), so the
	// main loop's recover never sees a tool panic — without this one a
	// panicking tool kills the whole process. Contained here it degrades
	// to a failed tool call: the model sees the error, retries or
	// works around it, and the turn still terminates observably.
	func() {
		defer func() {
			if r := recover(); r != nil {
				result, err = "", fmt.Errorf("agent: tool %s panicked: %v\n%s",
					tc.Function.Name, r, debug.Stack())
			}
		}()
		if st, ok := tool.(tools.StreamingTool); ok {
			// push is the tool's only way to surface intermediate output.
			// The select makes the call ctx-aware: if the agent's event
			// channel is full AND ctx gets cancelled (Esc), push returns
			// the cancel error so the tool can bail. No pump goroutine
			// needed — the callback IS the pump.
			push := func(d tools.StreamDelta) error {
				select {
				case out <- ToolDelta{
					CallID:    tc.ID,
					Name:      tc.Function.Name,
					Delta:     d.Delta,
					Reasoning: d.Reasoning,
				}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			result, err = st.ExecuteStream(ctx, args, push)
		} else {
			result, err = tool.Execute(ctx, args)
		}
	}()

	a.cfg.Hooks.NotifyPostToolUse(ctx, hooks.PostToolUseEvent{
		CallID: tc.ID,
		Name:   tc.Function.Name,
		Args:   args,
		Result: result,
		Err:    err,
	})
	out <- ToolExecEnd{
		CallID: tc.ID,
		Name:   tc.Function.Name,
		Result: result,
		Err:    err,
		Args:   tc.Function.Arguments,
	}
	return result, err
}

// emptyToolContentPlaceholder is the substitute for tool results that
// resolve to "". DeepSeek strictly requires the `content` field on
// tool-role messages, but `deepseek.Message.Content` is tagged
// `omitempty` — an empty string disappears from the wire body and the
// next API call fails with `messages[N]: missing field 'content'`.
// memory_observe is the canonical offender (returns "" by design as a
// "succeeds silently" signal); any future tool that does the same gets
// the same treatment for free. A short, neutral string keeps the
// LLM's history readable without faking a meaningful return value.
const emptyToolContentPlaceholder = "(no output)"

// ToolErrorPrefix is the wire-format prefix buildToolResultMsg stamps
// on every failed tool result. It is a contract, not prose:
// internal/friction scores a tool message as an Error by scanning for
// this prefix verbatim, so the wording is frozen — additions to the
// error text go after it, never by rewording it (the [plan: approved]
// marker precedent for parsed tool-result strings).
const ToolErrorPrefix = "tool error: "

// buildToolResultMsg constructs the deepseek.Message for a tool result.
// Centralised so the two dispatch paths (concurrent read-only batch +
// sequential mixed batch) can't drift, and so the empty-content guard
// has exactly one home. Errors always win: a non-nil terr formats into
// the message regardless of what result the tool partially produced.
func buildToolResultMsg(callID, result string, terr error) deepseek.Message {
	msg := deepseek.Message{
		Role:       deepseek.RoleTool,
		ToolCallID: callID,
		Content:    result,
	}
	if terr != nil {
		msg.Content = ToolErrorPrefix + fmt.Sprint(terr)
	}
	if msg.Content == "" {
		msg.Content = emptyToolContentPlaceholder
	}
	return msg
}

// readOnlyCall reports whether tc is backed by a tools.ReadOnlyTool —
// eligible for concurrent dispatch in a partitioned batch. Calls that
// don't resolve (nil registry, unknown name) are NOT read-only: they
// take the sequential stream, where dispatchTool surfaces the error.
func readOnlyCall(tc deepseek.ToolCall, reg *tools.Registry) bool {
	if reg == nil {
		return false
	}
	_, ok := reg.Lookup(tc.Function.Name).(tools.ReadOnlyTool)
	return ok
}

func sumUsage(a, b deepseek.Usage) deepseek.Usage {
	return deepseek.Usage{
		PromptTokens:          a.PromptTokens + b.PromptTokens,
		CompletionTokens:      a.CompletionTokens + b.CompletionTokens,
		TotalTokens:           a.TotalTokens + b.TotalTokens,
		PromptCacheHitTokens:  a.PromptCacheHitTokens + b.PromptCacheHitTokens,
		PromptCacheMissTokens: a.PromptCacheMissTokens + b.PromptCacheMissTokens,
	}
}
