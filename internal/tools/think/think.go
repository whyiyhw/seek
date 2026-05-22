// Package think implements the `Think` tool: a DeepSeek-specific bridge
// that asks a V4 model to reason harder than a normal chat turn would
// (PRD §4.8.2 Level 1).
//
// The tool uses the caller's current model (via modelFunc) with
// Thinking.Type="enabled" and ReasoningEffort="high", so the reasoning
// depth and pricing match what the user has selected. When the current
// model is V4-Pro the think call uses V4-Pro; when it's V4-Flash the
// think call uses V4-Flash — no hardcoded model default.
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
    "task":    {"type": "string", "description": "The question or task to reason about. Be specific and self-contained — the thinker sees no prior conversation."},
    "reflect": {"type": "boolean", "description": "Set to true for a self-review pass on recent work. The system prompt is then framed as evaluation rather than planning."},
    "context": {"type": "string", "description": "Optional context (e.g. code snippets, the diff under review). Pasted verbatim into the reasoner's user message."}
  },
  "required": ["task"],
  "additionalProperties": false
}`)

const description = "Call a DeepSeek model in thinking mode to reason hard about a problem. Returns the reasoning trace and the final answer as a single string. Use for: multi-step planning before complex edits; self-review (reflect=true) after a non-trivial change; any decision where the chat model is likely to be wrong on the first pass. DeepSeek-only."

type Args struct {
	Task    string `json:"task"`
	Reflect bool   `json:"reflect,omitempty"`
	Context string `json:"context,omitempty"`
}

type Tool struct {
	client    *deepseek.Client
	modelFunc func() string
}

// New creates a Think tool. modelFunc is called at execution time to
// determine which DeepSeek model to use for the reasoning call — it's
// a function so the model can change at runtime (e.g. via /model).
// If modelFunc returns "" or nil, ModelV4Flash is used as fallback.
func New(c *deepseek.Client, modelFunc func() string) Tool {
	return Tool{client: c, modelFunc: modelFunc}
}

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
// fallback path for callers (and tests) that don't want a push
// callback — same semantics, same returned string.
func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	sys, userMsg, err := parseArgs(raw)
	if err != nil {
		return "", err
	}

	resp, err := t.client.Chat(ctx, t.buildRequest(sys, userMsg))
	if err != nil {
		return "", fmt.Errorf("think: reasoner call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("think: reasoner returned no choices")
	}
	msg := resp.Choices[0].Message
	return formatResult(msg.ReasoningContent, msg.Content, resp.Usage, t.modelName()), nil
}

// ExecuteStream is the same call as Execute but uses ChatStream so the
// reasoner's chain-of-thought reaches the TUI as it arrives. push is
// called once per delta: Reasoning=true for reasoning_content chunks,
// Reasoning=false for final-answer content chunks. A non-nil error
// from push means ctx was cancelled — we propagate it instead of
// blocking on the next delta.
//
// The returned string is byte-identical to what Execute would have
// produced for the same model output, so the tool result message in
// history is unaffected by which dispatch path the agent took.
func (t Tool) ExecuteStream(ctx context.Context, raw json.RawMessage, push func(tools.StreamDelta) error) (string, error) {
	sys, userMsg, err := parseArgs(raw)
	if err != nil {
		return "", err
	}

	stream, err := t.client.ChatStream(ctx, t.buildRequest(sys, userMsg))
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
			if err := push(tools.StreamDelta{Delta: ev.Delta, Reasoning: true}); err != nil {
				return "", err
			}
		case deepseek.EventDelta:
			content.WriteString(ev.Delta)
			if err := push(tools.StreamDelta{Delta: ev.Delta, Reasoning: false}); err != nil {
				return "", err
			}
		case deepseek.EventDone:
			usage = ev.Usage
		}
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	return formatResult(reasoning.String(), content.String(), usage, t.modelName()), nil
}

// parseArgs unmarshals and validates the tool's JSON arguments and
// returns the assembled (system prompt, user message) pair. Shared
// between Execute and ExecuteStream so request bytes match across
// the two paths — the cache prefix stays stable when the agent
// toggles between them.
func parseArgs(raw json.RawMessage) (sys, userMsg string, err error) {
	var a Args
	if err := tools.UnmarshalStrict("think", raw, &a, "task", "reflect", "context"); err != nil {
		return "", "", err
	}
	if a.Task == "" {
		return "", "", tools.MissingField("think", "task", raw, "task", "reflect", "context")
	}
	sys = planSystem
	if a.Reflect {
		sys = reflectSystem
	}
	userParts := []string{a.Task}
	if a.Context != "" {
		userParts = append(userParts, "\n--- context ---\n"+a.Context)
	}
	return sys, strings.Join(userParts, "\n"), nil
}

// modelName returns the current model to use for the think call. Falls
// back to deepseek-v4-flash if modelFunc is nil or returns empty string.
func (t Tool) modelName() string {
	if t.modelFunc != nil {
		if m := t.modelFunc(); m != "" {
			return m
		}
	}
	return deepseek.ModelV4Flash
}

func (t Tool) buildRequest(sys, userMsg string) *deepseek.ChatRequest {
	model := t.modelName()
	return &deepseek.ChatRequest{
		Model: model,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: sys},
			{Role: deepseek.RoleUser, Content: userMsg},
		},
		Thinking:        &deepseek.ThinkingMode{Type: "enabled"},
		ReasoningEffort: "high",
	}
}

func formatResult(reasoning, content string, u deepseek.Usage, model string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("=== Think (%s · thinking) ===\n", model))
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
