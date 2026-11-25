package autopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// DecomposeClient is the subset of *deepseek.Client the decomposer
// needs — the same narrowing as verifier.JudgeClient. *deepseek.Client
// satisfies it directly; the cron lane's subscription provider
// satisfies it through cmd/seek's llmChatClient adapter (c_kdblc4: an
// unattended run must ride a flat-rate lane, and the DeepSeek API key
// is metered).
type DecomposeClient interface {
	Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error)
}

// DeepSeekDecomposer is the production Decomposer: one chat call asks the
// model to split a goal into ≤max independent, worktree-isolatable tasks,
// returned as a JSON array. It is the ONLY model freedom in an autopilot
// run's control flow (PRD §4 D1); everything after is deterministic Go.
type DeepSeekDecomposer struct {
	client DecomposeClient
	model  string
}

func NewDeepSeekDecomposer(c DecomposeClient, model string) DeepSeekDecomposer {
	return DeepSeekDecomposer{client: c, model: model}
}

func (d DeepSeekDecomposer) Decompose(ctx context.Context, goal string, max int) ([]Task, error) {
	resp, err := d.client.Chat(ctx, &deepseek.ChatRequest{
		Model: d.model,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: decomposeSystemPrompt(max)},
			{Role: deepseek.RoleUser, Content: goal},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("decomposer returned no choices")
	}
	return parseTasks(resp.Choices[0].Message.Content, max)
}

func decomposeSystemPrompt(max int) string {
	return fmt.Sprintf(`You are the planner for an autonomous, unattended coding run.
Decompose the user's goal into AT MOST %d INDEPENDENT, scoped tasks. Each task
is handed to a separate worker running in its own isolated git worktree, with
NO coordination between workers — so prefer tasks that touch disjoint files and
don't depend on each other's output.

Output ONLY a JSON array, no prose and no code fences:
[{"title": "<short label>", "prompt": "<self-contained instruction for one worker>", "done_when": ["<checkable completion condition>", "..."]}]

Rules:
- Fewer, well-scoped, non-overlapping tasks beat many entangled ones.
- If the goal is a single unit of work, return exactly one task.
- Each "prompt" must be self-contained: the worker sees only it, not the others.
- Each task carries 1-6 "done_when" conditions an INDEPENDENT verifier will
  check after the worker finishes: each one checkable against something
  concrete (a test, a command + expected output, a grep target, an invariant).
  A task without checkable completion conditions is unverifiable — write them
  or split the task until you can.`, max)
}

// parseTasks extracts a JSON task array from the model's reply, tolerating
// surrounding prose or ```json fences (models add them despite instruction).
// It clamps to max and assigns deterministic IDs; tasks with an empty
// prompt are dropped.
func parseTasks(content string, max int) ([]Task, error) {
	start := strings.IndexByte(content, '[')
	end := strings.LastIndexByte(content, ']')
	if start < 0 || end < start {
		return nil, fmt.Errorf("decomposer reply has no JSON array: %.120q", content)
	}
	var raw []struct {
		Title    string   `json:"title"`
		Prompt   string   `json:"prompt"`
		DoneWhen []string `json:"done_when"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &raw); err != nil {
		return nil, fmt.Errorf("decomposer reply not valid JSON: %w", err)
	}

	out := make([]Task, 0, len(raw))
	for _, r := range raw {
		prompt := strings.TrimSpace(r.Prompt)
		if prompt == "" {
			continue // a task with no instruction is unusable
		}
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = firstLine(prompt)
		}
		// done_when: trimmed, empties dropped, capped — the verifier
		// judges exactly these strings. A model that omits them
		// produces a task whose verdict will land on doubt (nothing
		// checkable), which is the honest outcome, not a parse error.
		// Omitted stays nil (empty-but-non-nil would read as "came
		// back empty after trimming" — a different fact).
		var dw []string
		for _, d := range r.DoneWhen {
			if d = strings.TrimSpace(d); d != "" {
				dw = append(dw, d)
			}
			if len(dw) >= maxDoneWhenPerTask {
				break
			}
		}
		out = append(out, Task{
			ID:       fmt.Sprintf("task-%d", len(out)+1),
			Title:    title,
			Prompt:   prompt,
			DoneWhen: dw,
		})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
