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
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
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

	Model        string          // defaults to deepseek.ModelV4Flash / provider default
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

	// ModeLabel is the current permission mode label for per-message
	// injection. Empty string → no reminder (the system-prompt tool
	// descriptions already cover Ask/Deny behaviour). Non-empty values
	// ("yolo", "plan") append a short mode reminder to every
	// user turn so the model immediately knows when /yolo or /plan
	// has changed the policy without invalidating the prefix
	// cache (the reminder is at the tail of the user message — only
	// those few bytes differ across turns).
	ModeLabel string

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

// Agent holds the persistent state for one conversation. It is NOT safe for
// concurrent calls to Prompt; one Prompt at a time per Agent.
type Agent struct {
	mu       sync.RWMutex
	cfg      Config
	messages []deepseek.Message

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
			cfg.Model = deepseek.ModelV4Flash
		}
		// Provider callers must set Model explicitly — no universal default.
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 200
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 16384
	}
	a := &Agent{cfg: cfg}
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
// PRD docs/prd/feature-suggested-reply.md §4.5.
func (a *Agent) AttachPredictedNext(text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == deepseek.RoleAssistant && len(a.messages[i].ToolCalls) == 0 {
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
const workflowReminder = "\n\n[Workflow: read→grep→read(limit≤50). edit→read first.]"

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
func (a *Agent) Summarise(ctx context.Context) (string, deepseek.Usage, error) {
	if a.cfg.Provider != nil {
		return a.summariseLLM(ctx)
	}
	history := append([]deepseek.Message{}, a.messages...)
	history = append(history, deepseek.Message{
		Role:    deepseek.RoleUser,
		Content: summariserPrompt,
	})

	req := &deepseek.ChatRequest{
		Model: a.cfg.Model,
		// Images stripped: the briefing only needs prior TEXT (the
		// attached-image markers live in Content), and this side-channel
		// call must not pay for — or depend on resolving — image bytes.
		Messages: deepseek.WithoutImages(deepseek.StripReasoningContent(history)),
	}
	resp, err := a.cfg.Client.Chat(ctx, req)
	if err != nil {
		return "", deepseek.Usage{}, err
	}
	if len(resp.Choices) == 0 {
		return "", resp.Usage, errors.New("agent: summarise returned no choices")
	}
	return resp.Choices[0].Message.Content, resp.Usage, nil
}

// summariseLLM is the second-tier path for Summarise: uses ChatStream
// (no non-streaming equivalent in the llm.Provider interface).
func (a *Agent) summariseLLM(ctx context.Context) (string, deepseek.Usage, error) {
	msgs := msgsToLLM(a.messages)
	msgs = append(msgs, llm.Message{Role: "user", Content: summariserPrompt})
	req := llm.ChatRequest{
		Model:     a.cfg.Model,
		Messages:  msgs,
		MaxTokens: a.cfg.MaxTokens,
	}

	stream, err := a.cfg.Provider.ChatStream(ctx, req)
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
	out := make(chan Event, 32)

	go func() {
		defer close(out)
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
		a.appendMessage(userMsg)
		out <- AgentStart{}

		var (
			totalUsage     deepseek.Usage
			totalToolCalls int
			turns          int
		)

		for turn := 0; turn < a.cfg.MaxTurns; turn++ {
			turns = turn + 1
			out <- TurnStart{Index: turn}
			a.cfg.Hooks.NotifyPreTurn(ctx, hooks.PreTurnEvent{
				Index:   turn,
				History: append([]deepseek.Message{}, a.messages...),
			})

			// Retry loop: transient stream interruptions (unexpected
			// EOF, SSE decode failures) that cut off mid-tool-call are
			// safe to retry — the model's tool_call_ids are unverified,
			// so the partial turn is NOT committed to history. We match
			// the stream-layer retry budget (1 retry = 2 attempts) and
			// backoff. Plain text streams cut off are NOT retried here;
			// the partial text is already visible and the user can say
			// "continue".
			const agentStreamRetries = 1
			const agentRetryBackoff = 500 * time.Millisecond

			var (
				assistant deepseek.Message
				usage     deepseek.Usage
				finish    string
				err       error
			)

			for attempt := 0; attempt <= agentStreamRetries; attempt++ {
				assistant, usage, finish, err = a.runTurn(ctx, out)
				if err != nil {
					break
				}

				// Only retry when the stream died while the model was
				// emitting tool calls.
				canRetry := len(assistant.ToolCalls) > 0 &&
					finish != "tool_calls" &&
					(strings.HasPrefix(finish, "stream_error:") ||
						strings.HasPrefix(finish, "decode_error:")) &&
					attempt < agentStreamRetries &&
					ctx.Err() == nil

				if !canRetry {
					break
				}

				// Brief backoff so the upstream can recover.
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case <-time.After(agentRetryBackoff):
				}
				if err != nil {
					break
				}
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
					out <- AgentEnd{Usage: totalUsage, Turns: turns - 1, ToolCalls: totalToolCalls}
					return
				}
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
			if len(assistant.ToolCalls) > 0 && finish != "tool_calls" {
				out <- ErrorEvent{Err: fmt.Errorf(
					"agent: refusing to commit turn — assistant emitted %d tool_call(s) but stream ended with finish_reason=%q",
					len(assistant.ToolCalls), finish)}
				return
			}

			a.appendMessage(assistant)
			out <- MessageEnd{Message: assistant}

			// Surface a visible notice when the completion was cut by
			// the token limit so the user knows the response is
			// incomplete rather than seeing a silent truncation.
			if finish == "length" {
				out <- ErrorEvent{Err: fmt.Errorf(
					"agent: response truncated (finish_reason=length, max_tokens=%d) — use /compact to free context or ask me to continue",
					a.cfg.MaxTokens)}
			}

			toolCount := len(assistant.ToolCalls)
			if toolCount == 0 || finish != "tool_calls" {
				out <- TurnEnd{Index: turn, Usage: usage, ToolCalls: 0}
				a.cfg.Hooks.NotifyPostTurn(ctx, hooks.PostTurnEvent{
					Index:     turn,
					Usage:     usage,
					ToolCalls: 0,
					Finish:    finish,
				})
				if a.cfg.AutoContinue && finish == "stop" && turn < a.cfg.MaxTurns-1 {
					a.appendMessage(deepseek.Message{
						Role:    deepseek.RoleUser,
						Content: "continue" + modeReminder(a.cfg.ModeLabel),
					})
					continue
				}
				break
			}

			totalToolCalls += toolCount

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
				wg.Add(1)
				go func() {
					defer wg.Done()
					for _, i := range seqIdx {
						tc := assistant.ToolCalls[i]
						result, terr := a.dispatchTool(ctx, tc, out)
						toolMsgs[i] = buildToolResultMsg(tc.ID, result, terr)
					}
				}()
			}
			wg.Wait()
			for _, msg := range toolMsgs {
				out <- MessageStart{Message: msg}
				a.appendMessage(msg)
				out <- MessageEnd{Message: msg}
			}

			out <- TurnEnd{Index: turn, Usage: usage, ToolCalls: toolCount}
			a.cfg.Hooks.NotifyPostTurn(ctx, hooks.PostTurnEvent{
				Index:     turn,
				Usage:     usage,
				ToolCalls: toolCount,
				Finish:    finish,
			})
		}

		out <- AgentEnd{Usage: totalUsage, Turns: turns, ToolCalls: totalToolCalls}
	}()

	return out
}

// runTurn routes to the DeepSeek or second-tier path depending on which
// provider was configured.
func (a *Agent) runTurn(ctx context.Context, out chan<- Event) (deepseek.Message, deepseek.Usage, string, error) {
	if a.cfg.Provider != nil {
		return a.runTurnLLM(ctx, out)
	}
	return a.runTurnDeepSeek(ctx, out)
}

// runTurnDeepSeek is the original DeepSeek-specific streaming path.
func (a *Agent) runTurnDeepSeek(ctx context.Context, out chan<- Event) (deepseek.Message, deepseek.Usage, string, error) {
	msgs := deepseek.StripReasoningContent(a.messages)
	// feature-vision: materialise Asset references into data URLs at
	// the same send-time station. Load failures degrade in-band inside
	// ResolveImages — they never abort the turn.
	if a.cfg.ImageLoader != nil {
		msgs = deepseek.ResolveImages(msgs, a.cfg.ImageLoader)
	}
	if a.cfg.PrepareMessages != nil {
		msgs = a.cfg.PrepareMessages(msgs)
	}
	req := &deepseek.ChatRequest{
		Model:     a.cfg.Model,
		Messages:  msgs,
		MaxTokens: a.cfg.MaxTokens,
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

	if !started {
		// Edge case: server returned nothing before [DONE].
		out <- MessageStart{Message: assistant}
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

	// Empty-response guard: if the model streamed reasoning tokens but
	// no content or tool_calls, the assistant message has nothing to say.
	// Committing it would leave an orphan assistant turn with no content
	// and no tool_calls, which DeepSeek rejects on the next request as
	// "Invalid assistant message: content or tool_calls must be set"
	// (PRD §4.5.1, pitfalls/message-contract.md).
	if assistant.Content == "" && len(assistant.ToolCalls) == 0 {
		return assistant, usage, finish,
			errors.New("agent: model returned an empty response (no content and no tool calls)")
	}

	return assistant, usage, finish, nil
}

// runTurnLLM is the second-tier streaming path using llm.Provider.
// It translates messages to/from the generic format and reassembles the
// assistant turn as a deepseek.Message so the rest of the agent loop
// (history, session save, TUI rendering) is unchanged.
func (a *Agent) runTurnLLM(ctx context.Context, out chan<- Event) (deepseek.Message, deepseek.Usage, string, error) {
	// Apply PrepareMessages on the deepseek.Message form (where
	// calibration injection makes sense) then convert to llm.Message.
	// msgsToLLM is the byte-shape conversion; it shouldn't know about
	// calibration semantics.
	prepared := a.messages
	if a.cfg.PrepareMessages != nil {
		prepared = a.cfg.PrepareMessages(prepared)
	}
	req := llm.ChatRequest{
		Model:     a.cfg.Model,
		Messages:  msgsToLLM(prepared),
		Tools:     toolsToLLM(a.cfg.Tools),
		MaxTokens: a.cfg.MaxTokens,
	}

	stream, err := a.cfg.Provider.ChatStream(ctx, req)
	if err != nil {
		return deepseek.Message{}, deepseek.Usage{}, "", err
	}

	assistant := deepseek.Message{Role: deepseek.RoleAssistant}
	started := false
	var finish string
	var inputTokens, outputTokens int

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

		case llm.ErrorEvent:
			return assistant, deepseek.Usage{}, "", e.Err
		}
	}

	if !started {
		out <- MessageStart{Message: assistant}
	}

	if ctx.Err() != nil {
		return assistant, deepseek.Usage{}, finish, ctx.Err()
	}

	// Empty-response guard: same rationale as runTurnDeepSeek — an
	// assistant message with no content and no tool_calls poisons the
	// next API request.
	if assistant.Content == "" && len(assistant.ToolCalls) == 0 {
		return assistant, deepseek.Usage{}, finish,
			errors.New("agent: model returned an empty response (no content and no tool calls)")
	}

	usage := deepseek.Usage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      inputTokens + outputTokens,
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
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Err: err}
		return "", err
	}

	tool := a.cfg.Tools.Lookup(tc.Function.Name)
	if tool == nil {
		err := fmt.Errorf("%w: %s (known: %v)", tools.ErrUnknownTool, tc.Function.Name, a.cfg.Tools.Names())
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Err: err}
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
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Err: hookErr}
		return "", hookErr
	}
	if preTool.Deny != "" {
		a.cfg.Hooks.NotifyPostToolUse(ctx, hooks.PostToolUseEvent{
			CallID: tc.ID, Name: tc.Function.Name, Args: args, Result: preTool.Deny,
		})
		out <- ToolExecEnd{CallID: tc.ID, Name: tc.Function.Name, Result: preTool.Deny}
		return preTool.Deny, nil
	}
	if preTool.Args != nil {
		args = preTool.Args
	}

	var (
		result string
		err    error
	)
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
		msg.Content = fmt.Sprintf("tool error: %v", terr)
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
