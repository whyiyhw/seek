// Package think implements the `Think` tool: a DeepSeek-specific bridge
// that asks a V4 model to reason harder than a normal chat turn would
// (PRD §4.8.2 Level 1).
//
// Post-V4 (2026-01), DeepSeek collapsed reasoning into a parameter:
// instead of switching to a separate `deepseek-reasoner` model, we
// call ModelV4Flash with Thinking.Type="enabled" and
// ReasoningEffort="high". Cheaper, larger context (1M), and the
// thinking-mode side of V4 still returns `reasoning_content`
// alongside the final content — same field, same stripping rule.
//
// The tool still runs a FRESH, history-less call so the reasoning
// pass isn't contaminated by the calling chat's tool schemas and the
// prior assistant's reasoning_content isn't accidentally echoed back
// (DeepSeek rejects requests that retain it; see
// StripReasoningContent).
//
// The reasoning + final answer are returned together as the tool
// result, which then enters the next chat turn as a normal `tool`
// role message. From the calling chat model's perspective Think
// looks like any other information-retrieval tool.
package think

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "task":    {"type": "string", "description": "The question or task to reason about. Be specific and self-contained — the reasoner sees no prior conversation."},
    "reflect": {"type": "boolean", "description": "Set to true for a self-review pass on recent work. The system prompt is then framed as evaluation rather than planning."},
    "context": {"type": "string", "description": "Optional context (e.g. code snippets, the diff under review). Pasted verbatim into the reasoner's user message."}
  },
  "required": ["task"],
  "additionalProperties": false
}`)

const description = "Call deepseek-reasoner to think hard about a problem. Returns the reasoning trace and the final answer as a single string. Use for: multi-step planning before complex edits; self-review (reflect=true) after a non-trivial change; any decision where the chat model is likely to be wrong on the first pass. DeepSeek-only."

type Args struct {
	Task    string `json:"task"`
	Reflect bool   `json:"reflect,omitempty"`
	Context string `json:"context,omitempty"`
}

type Tool struct {
	client *deepseek.Client
}

func New(c *deepseek.Client) Tool { return Tool{client: c} }

func (Tool) Name() string            { return "think" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

const (
	planSystem    = "You are a careful step-by-step reasoner. Decompose the user's task into concrete steps and identify the most important decisions and risks. End with a section labelled 'Answer:' containing the actionable conclusion."
	reflectSystem = "You are a code-review reasoner. The user will give you recent work (and the original goal). Identify the highest-impact issues, missing cases, or risks, ordered by severity. End with a section labelled 'Answer:' containing the concrete fixes (or 'looks correct' if there are none)."

	// reasoningCap and contentCap bound how much a single Think result
	// can balloon the next chat turn's prompt. Reasoner traces routinely
	// run 2-5K tokens; we surface the head only and instruct the model
	// to re-call Think for specifics if needed.
	reasoningCap = 4000
	contentCap   = 4000
)

// Execute runs think in non-streaming mode. The agent prefers
// ExecuteStream when wiring a streaming-capable tool, so this is the
// fallback path for callers (and tests) that don't want a delta
// channel — same semantics, same returned string.
func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	a, sys, userMsg, err := parseArgs(raw)
	if err != nil {
		return "", err
	}
	_ = a

	resp, err := t.client.Chat(ctx, buildRequest(sys, userMsg))
	if err != nil {
		return "", fmt.Errorf("think: reasoner call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("think: reasoner returned no choices")
	}
	msg := resp.Choices[0].Message
	return formatResult(msg.ReasoningContent, msg.Content, resp.Usage), nil
}

// ExecuteStream is the same call as Execute but uses ChatStream so the
// reasoner's chain-of-thought reaches the TUI as it arrives. Deltas
// land on `deltas` with Reasoning=true for reasoning_content chunks
// and Reasoning=false for final-answer content chunks. The returned
// string is byte-identical to what Execute would have produced for the
// same model output, so the tool result message in history is
// unaffected by whether the agent took the streaming path.
//
// The caller (agent.dispatchTool) owns the deltas channel and closes
// it after we return.
func (t Tool) ExecuteStream(ctx context.Context, raw json.RawMessage, deltas chan<- tools.StreamDelta) (string, error) {
	_, sys, userMsg, err := parseArgs(raw)
	if err != nil {
		return "", err
	}

	stream, err := t.client.ChatStream(ctx, buildRequest(sys, userMsg))
	if err != nil {
		return "", fmt.Errorf("think: reasoner call: %w", err)
	}

	var (
		reasoning strings.Builder
		content   strings.Builder
		usage     deepseek.Usage
	)
	for ev := range stream {
		switch ev.Type {
		case deepseek.EventReasoningDelta:
			reasoning.WriteString(ev.Delta)
			select {
			case deltas <- tools.StreamDelta{Delta: ev.Delta, Reasoning: true}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		case deepseek.EventDelta:
			content.WriteString(ev.Delta)
			select {
			case deltas <- tools.StreamDelta{Delta: ev.Delta, Reasoning: false}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		case deepseek.EventDone:
			usage = ev.Usage
		}
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	return formatResult(reasoning.String(), content.String(), usage), nil
}

// parseArgs unmarshals and validates the tool's JSON arguments,
// returning the structured Args plus the assembled (system prompt,
// user message) pair both Execute paths use. Pulled out so Execute
// and ExecuteStream stay byte-identical in their wire formatting —
// which is what keeps the prefix cache happy when an agent toggles
// between the two paths.
func parseArgs(raw json.RawMessage) (Args, string, string, error) {
	var a Args
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, "", "", fmt.Errorf("think: bad arguments: %w", err)
	}
	if a.Task == "" {
		return a, "", "", fmt.Errorf("think: task is required")
	}
	sys := planSystem
	if a.Reflect {
		sys = reflectSystem
	}
	userParts := []string{a.Task}
	if a.Context != "" {
		userParts = append(userParts, "\n--- context ---\n"+a.Context)
	}
	return a, sys, strings.Join(userParts, "\n"), nil
}

func buildRequest(sys, userMsg string) *deepseek.ChatRequest {
	return &deepseek.ChatRequest{
		Model: deepseek.ModelV4Flash,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: sys},
			{Role: deepseek.RoleUser, Content: userMsg},
		},
		Thinking:        &deepseek.ThinkingMode{Type: "enabled"},
		ReasoningEffort: "high",
	}
}

func formatResult(reasoning, content string, u deepseek.Usage) string {
	var sb strings.Builder

	sb.WriteString("=== Think (deepseek-reasoner) ===\n")
	sb.WriteString(fmt.Sprintf("usage: prompt %d, completion %d, cache hit %d / miss %d\n\n",
		u.PromptTokens, u.CompletionTokens, u.PromptCacheHitTokens, u.PromptCacheMissTokens))

	sb.WriteString("--- reasoning ---\n")
	sb.WriteString(clip(reasoning, reasoningCap))
	sb.WriteString("\n\n--- answer ---\n")
	sb.WriteString(clip(content, contentCap))
	return sb.String()
}

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + fmt.Sprintf("\n…[truncated %d chars; re-call think with a narrower task for specifics]", len(s)-limit)
}
