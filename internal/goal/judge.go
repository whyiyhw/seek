package goal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// DeepSeekJudge is the production Judge: one CHEAP chat call (default a
// small/fast model) reads the goal condition + the latest turn's work and
// returns a JSON verdict. It's the only model freedom in the goal loop's
// control flow; everything else is deterministic Go. Mirrors autopilot's
// DeepSeekDecomposer (separate client.Chat call, robust JSON parse) — and
// crucially is its OWN call, never folded into the main conversation, so
// it can't perturb the worker's prefix cache.
type DeepSeekJudge struct {
	client *deepseek.Client
	model  string
}

func NewDeepSeekJudge(c *deepseek.Client, model string) DeepSeekJudge {
	return DeepSeekJudge{client: c, model: model}
}

func (j DeepSeekJudge) Judge(ctx context.Context, condition string, last TurnResult) (Verdict, error) {
	resp, err := j.client.Chat(ctx, &deepseek.ChatRequest{
		Model: j.model,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: judgeSystemPrompt},
			{Role: deepseek.RoleUser, Content: judgeUserPrompt(condition, last)},
		},
	})
	if err != nil {
		return Verdict{}, err
	}
	if len(resp.Choices) == 0 {
		return Verdict{}, fmt.Errorf("judge returned no choices")
	}
	return parseVerdict(resp.Choices[0].Message.Content)
}

const judgeSystemPrompt = `You are a STRICT completion judge for an autonomous coding loop.
Given a GOAL CONDITION and the latest turn's work, decide whether the condition
is now FULLY satisfied.

Output ONLY a JSON object, no prose and no code fences:
{"met": <true|false>, "reason": "<one sentence>", "hint": "<next step if not met, else empty>"}

Rules:
- Be conservative: set met=true ONLY if the condition is clearly, fully satisfied.
- If you can't tell, met=false.
- "hint" should be a concrete next action when met=false; empty string when met=true.`

func judgeUserPrompt(condition string, last TurnResult) string {
	work := strings.TrimSpace(last.Text)
	if work == "" {
		work = "(the latest turn produced no text output)"
	}
	return fmt.Sprintf("GOAL CONDITION:\n%s\n\nLATEST TURN'S WORK (tools run this turn: %d):\n%s",
		condition, last.ToolCalls, work)
}

// parseVerdict extracts a JSON verdict object from the model's reply,
// tolerating surrounding prose or ```json fences (models add them despite
// instruction). A reply with no JSON object is an error — the driver
// treats that as not-met and continues, so a malformed judge reply can
// never falsely report the goal as met.
func parseVerdict(content string) (Verdict, error) {
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return Verdict{}, fmt.Errorf("judge reply has no JSON object: %.120q", content)
	}
	var raw struct {
		Met    bool   `json:"met"`
		Reason string `json:"reason"`
		Hint   string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &raw); err != nil {
		return Verdict{}, fmt.Errorf("judge reply not valid JSON: %w", err)
	}
	return Verdict{
		Met:    raw.Met,
		Reason: strings.TrimSpace(raw.Reason),
		Hint:   strings.TrimSpace(raw.Hint),
	}, nil
}
