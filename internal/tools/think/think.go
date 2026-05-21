// Package think implements the `Think` tool: a DeepSeek-specific bridge
// from the main chat loop into deepseek-reasoner (PRD §4.8.2 Level 1).
//
// The tool runs a fresh, history-less reasoner call so that:
//   - reasoner's no-tools / no-temperature constraints don't interact
//     with the main chat loop's tools and parameters, and
//   - the prior assistant's `reasoning_content` field is never sent back
//     to the API (DeepSeek rejects requests that retain it).
//
// The reasoning + final answer are returned together as the tool result,
// which then enters the next deepseek-chat turn as a normal `tool` role
// message. From the chat model's perspective Think looks like any other
// information-retrieval tool.
package think

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

func (t Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("think: bad arguments: %w", err)
	}
	if a.Task == "" {
		return "", fmt.Errorf("think: task is required")
	}

	sys := planSystem
	if a.Reflect {
		sys = reflectSystem
	}

	userParts := []string{a.Task}
	if a.Context != "" {
		userParts = append(userParts, "\n--- context ---\n"+a.Context)
	}

	resp, err := t.client.Chat(ctx, &deepseek.ChatRequest{
		Model: deepseek.ModelReasoner,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: sys},
			{Role: deepseek.RoleUser, Content: strings.Join(userParts, "\n")},
		},
	})
	if err != nil {
		return "", fmt.Errorf("think: reasoner call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("think: reasoner returned no choices")
	}
	msg := resp.Choices[0].Message

	return formatResult(msg.ReasoningContent, msg.Content, resp.Usage), nil
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
