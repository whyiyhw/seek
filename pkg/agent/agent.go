// Package agent is the DeepSeek-aware agent runtime: an LLM call + tool
// dispatch loop that emits a stream of typed Events on a channel.
//
// M1 wires the agent directly to *deepseek.Client. A future milestone (M6)
// will introduce a thin pkg/llm.Provider routing layer for the second-tier
// Anthropic/OpenAI/Gemini providers — but DeepSeek-specific code paths
// (cache stats, reasoner handoff, FIM) stay rooted here, not in the
// generic interface (PRD §4.1).
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Config configures a new Agent.
type Config struct {
	Client       *deepseek.Client
	Model        string          // defaults to deepseek.ModelChat
	SystemPrompt string          // optional
	Tools        *tools.Registry // optional — nil means no tools
	MaxTurns     int             // safety bound; defaults to 8

	// InitialMessages, if non-empty, seeds the agent's history.
	// Used by --resume / --continue to restore a saved session. The
	// SystemPrompt is still placed first; InitialMessages are
	// appended after, in order.
	InitialMessages []deepseek.Message
}

// Agent holds the persistent state for one conversation. It is NOT safe for
// concurrent calls to Prompt; one Prompt at a time per Agent.
type Agent struct {
	cfg      Config
	messages []deepseek.Message
}

// New constructs an Agent and seeds the system prompt (if any).
func New(cfg Config) (*Agent, error) {
	if cfg.Client == nil {
		return nil, errors.New("agent: Config.Client is required")
	}
	if cfg.Model == "" {
		cfg.Model = deepseek.ModelChat
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 8
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

// Prompt appends a user message and runs the agent loop. Events are emitted
// on the returned channel; the channel is closed when the loop terminates
// (final answer reached, max turns hit, ctx cancelled, or fatal error).
//
// The loop:
//   1. Append the user message.
//   2. Call ChatStream; assemble assistant message + any tool_call deltas.
//   3. If finish_reason="tool_calls": dispatch tools sequentially, append
//      tool result messages, loop.
//   4. Otherwise: terminate.
func (a *Agent) Prompt(ctx context.Context, userText string) <-chan Event {
	out := make(chan Event, 32)

	go func() {
		defer close(out)

		a.messages = append(a.messages, deepseek.Message{
			Role:    deepseek.RoleUser,
			Content: userText,
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

			assistant, usage, finish, err := a.runTurn(ctx, out)
			if err != nil {
				out <- ErrorEvent{Err: err}
				return
			}
			totalUsage = sumUsage(totalUsage, usage)

			a.messages = append(a.messages, assistant)
			out <- MessageEnd{Message: assistant}

			toolCount := len(assistant.ToolCalls)
			if toolCount == 0 || finish != "tool_calls" {
				out <- TurnEnd{Index: turn, Usage: usage, ToolCalls: 0}
				break
			}

			totalToolCalls += toolCount

			// M1: sequential tool dispatch. Parallel via errgroup lands
			// with the parallel-execution work in a later milestone.
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

			out <- TurnEnd{Index: turn, Usage: usage, ToolCalls: toolCount}
		}

		out <- AgentEnd{Usage: totalUsage, Turns: turns, ToolCalls: totalToolCalls}
	}()

	return out
}

// runTurn streams one LLM call and assembles the final assistant message.
// It emits MessageStart on the first delta and MessageDelta for every text
// chunk; MessageEnd is emitted by the caller after history is updated.
func (a *Agent) runTurn(ctx context.Context, out chan<- Event) (deepseek.Message, deepseek.Usage, string, error) {
	req := &deepseek.ChatRequest{
		Model:    a.cfg.Model,
		Messages: deepseek.StripReasoningContent(a.messages),
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

	return assistant, usage, finish, nil
}

// dispatchTool emits ToolExecStart/End around a single Registry.Dispatch.
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

	result, err := a.cfg.Tools.Dispatch(ctx, tc.Function.Name, args)
	out <- ToolExecEnd{
		CallID: tc.ID,
		Name:   tc.Function.Name,
		Result: result,
		Err:    err,
	}
	return result, err
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
