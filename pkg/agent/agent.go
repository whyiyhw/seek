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

	Model        string          // defaults to deepseek.ModelChat / provider default
	SystemPrompt string          // optional
	Tools        *tools.Registry // optional — nil means no tools
	MaxTurns     int             // safety bound; defaults to 200
	MaxTokens    int             // completion token cap per call; 0 → defaultMaxTokens
	AutoContinue bool            // if true, inject "continue" on text-only turns so the model resumes mid-task without user input

	// InitialMessages, if non-empty, seeds the agent's history.
	// Used by --resume / --continue to restore a saved session. The
	// SystemPrompt is still placed first; InitialMessages are
	// appended after, in order.
	InitialMessages []deepseek.Message

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
}

// Agent holds the persistent state for one conversation. It is NOT safe for
// concurrent calls to Prompt; one Prompt at a time per Agent.
type Agent struct {
	cfg      Config
	messages []deepseek.Message
}

// SetModel changes the active model. Safe to call between (not during) turns.
// The change takes effect on the next Prompt call.
func (a *Agent) SetModel(model string) {
	a.cfg.Model = model
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

// Messages returns a copy of the current conversation history.
func (a *Agent) Messages() []deepseek.Message {
	out := make([]deepseek.Message, len(a.messages))
	copy(out, a.messages)
	return out
}

// Reset replaces the agent's non-system message history with the
// provided slice. The configured SystemPrompt is re-prepended verbatim;
// any system messages in `history` are dropped (mirrors New()'s rule
// — system role belongs to the agent config, not the conversation).
//
// Used by /compact to swap a long history for a short summary, and
// any future "rewind" UI. NOT safe to call while a Prompt is in flight.
func (a *Agent) Reset(history []deepseek.Message) {
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
const workflowReminder = "\n\n[Workflow rule: before asserting any fact about code behaviour or values, grep/read the source to confirm. Do not rely on memory from earlier turns. No grep/read evidence → label it a guess. Before calling edit, read the target lines first to capture exact whitespace — never guess indent from memory.]"

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
		Model:    a.cfg.Model,
		Messages: deepseek.StripReasoningContent(history),
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
	req := llm.ChatRequest{Model: a.cfg.Model, Messages: msgs}

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
//  3. If finish_reason="tool_calls": dispatch tools sequentially, append
//     tool result messages, loop.
//  4. Otherwise: terminate.
func (a *Agent) Prompt(ctx context.Context, userText string) <-chan Event {
	out := make(chan Event, 32)

	go func() {
		defer close(out)

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
			a.messages = append(a.messages, m)
		}
		a.messages = append(a.messages, deepseek.Message{
			Role:    deepseek.RoleUser,
			Content: prePrompt.UserText + workflowReminder,
		})
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

			assistant, usage, finish, err := a.runTurn(ctx, out)
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
			// state.
			if len(assistant.ToolCalls) > 0 && finish != "tool_calls" {
				out <- ErrorEvent{Err: fmt.Errorf(
					"agent: refusing to commit turn — assistant emitted %d tool_call(s) but stream ended with finish_reason=%q",
					len(assistant.ToolCalls), finish)}
				return
			}

			a.messages = append(a.messages, assistant)
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
					a.messages = append(a.messages, deepseek.Message{
						Role:    deepseek.RoleUser,
						Content: "continue",
					})
					continue
				}
				break
			}

			totalToolCalls += toolCount

			if allReadOnly(assistant.ToolCalls, a.cfg.Tools) {
				// All calls are read-only: dispatch concurrently and
				// collect results into a pre-sized slice (safe — each
				// goroutine writes to its own index). MessageStart/End
				// are emitted in original order after Wait so the
				// conversation prefix stays deterministic.
				toolMsgs := make([]deepseek.Message, len(assistant.ToolCalls))
				var wg sync.WaitGroup
				for i, tc := range assistant.ToolCalls {
					wg.Add(1)
					go func(i int, tc deepseek.ToolCall) {
						defer wg.Done()
						result, terr := a.dispatchTool(ctx, tc, out)
						msg := deepseek.Message{
							Role:       deepseek.RoleTool,
							ToolCallID: tc.ID,
							Content:    result,
						}
						if terr != nil {
							msg.Content = fmt.Sprintf("tool error: %v", terr)
						}
						toolMsgs[i] = msg
					}(i, tc)
				}
				wg.Wait()
				for _, msg := range toolMsgs {
					out <- MessageStart{Message: msg}
					a.messages = append(a.messages, msg)
					out <- MessageEnd{Message: msg}
				}
			} else {
				for _, tc := range assistant.ToolCalls {
					result, terr := a.dispatchTool(ctx, tc, out)
					toolMsg := deepseek.Message{
						Role:       deepseek.RoleTool,
						ToolCallID: tc.ID,
						Content:    result,
					}
					if terr != nil {
						toolMsg.Content = fmt.Sprintf("tool error: %v", terr)
					}
					out <- MessageStart{Message: toolMsg}
					a.messages = append(a.messages, toolMsg)
					out <- MessageEnd{Message: toolMsg}
				}
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
	req := &deepseek.ChatRequest{
		Model:     a.cfg.Model,
		Messages:  deepseek.StripReasoningContent(a.messages),
		MaxTokens: a.cfg.MaxTokens,
	}
	// V4 shipped Thinking as a request parameter rather than a separate
	// model id, but DeepSeek kept the old "deepseek-reasoner" alias
	// alive and silently routes it to a V4 model. Without explicitly
	// setting Thinking the alias falls back to a non-reasoning V4 (in
	// practice V4-Flash), which is the bug users hit when they pick
	// "reasoner" expecting CoT and get fast-chat behaviour. Opt
	// reasoning models in here so the picker label matches reality.
	if deepseek.ShouldEnableThinking(a.cfg.Model) {
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
	req := llm.ChatRequest{
		Model:    a.cfg.Model,
		Messages: msgsToLLM(a.messages),
		Tools:    toolsToLLM(a.cfg.Tools),
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

// allReadOnly returns true when every tool call in tcs is backed by a
// tools.ReadOnlyTool — meaning the batch can be dispatched concurrently
// without ordering constraints. Returns false for batches of size < 2
// (no parallelism benefit) and when reg is nil.
func allReadOnly(tcs []deepseek.ToolCall, reg *tools.Registry) bool {
	if reg == nil || len(tcs) < 2 {
		return false
	}
	for _, tc := range tcs {
		t := reg.Lookup(tc.Function.Name)
		if _, ok := t.(tools.ReadOnlyTool); !ok {
			return false
		}
	}
	return true
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
