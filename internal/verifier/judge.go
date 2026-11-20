// judge.go — the judge session. The Judge interface is the seam:
// production uses DeepSeekJudge (one fresh-context chat call), tests
// inject fakes. The judge receives ONLY the evidence bundle — never
// the worker transcript, never the task prompt's framing beyond the
// assertions themselves — so "the worker said it worked" has no path
// into the judgment.
package verifier

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// JudgeClient is the subset of *deepseek.Client the judge needs.
// Narrowed so tests can record or fail the call without a real
// backend (and so this package never grows a second provider path —
// v8 §3: the judge rides the same first-class DeepSeek lane the
// decomposer does).
type JudgeClient interface {
	Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error)
}

// Judge renders a verdict over a bundle. Implementations must honour
// ctx cancellation.
type Judge interface {
	Judge(ctx context.Context, model, bundle string) (Judgement, error)
}

// Judgement is a judge session's raw output before clamping.
type Judgement struct {
	Assertions []JudgedAssertion
	Note       string
	// Tokens is the judge call's burn (prompt + completion) — the
	// verdict's own cost line on the R1 evidence surface.
	Tokens int
}

// JudgedAssertion is the judge's row for one assertion. Evidence ids
// reference the bundle's [T1]/[D]/[S] blocks; the clamp in
// clampAssertions grounds them.
type JudgedAssertion struct {
	Index    int      `json:"index"`
	Verdict  string   `json:"verdict"`
	Evidence []string `json:"evidence"`
	Note     string   `json:"note"`
}

// DeepSeekJudge is the production judge: a SINGLE chat call with the
// compile-time system prompt (prompt.go) and the bundle as the user
// message. Fresh context by construction — the messages array starts
// and ends with this exchange; nothing from any prior session is in
// it.
type DeepSeekJudge struct {
	Client JudgeClient
}

// Judge runs the single call and parses the reply.
func (d *DeepSeekJudge) Judge(ctx context.Context, model, bundle string) (Judgement, error) {
	if d.Client == nil {
		return Judgement{}, fmt.Errorf("verifier: no judge client configured")
	}
	resp, err := d.Client.Chat(ctx, &deepseek.ChatRequest{
		Model: model,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: SystemPrompt},
			{Role: deepseek.RoleUser, Content: bundle},
		},
	})
	if err != nil {
		return Judgement{}, fmt.Errorf("judge call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Judgement{}, fmt.Errorf("judge returned no choices")
	}
	assertions, note, err := parseJudgement(resp.Choices[0].Message.Content)
	if err != nil {
		return Judgement{}, err
	}
	return Judgement{
		Assertions: assertions,
		Note:       note,
		Tokens:     resp.Usage.PromptTokens + resp.Usage.CompletionTokens,
	}, nil
}

// judgeReply mirrors the judge's JSON output contract.
type judgeReply struct {
	Assertions []JudgedAssertion `json:"assertions"`
	Note       string            `json:"overall_note"`
}

// parseJudgement extracts the judgement JSON from the reply,
// tolerating surrounding prose or ```json fences (same tolerance as
// autopilot.parseTasks — models add chrome despite instructions).
func parseJudgement(content string) ([]JudgedAssertion, string, error) {
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end < start {
		return nil, "", fmt.Errorf("judge reply has no JSON object: %.120q", content)
	}
	var reply judgeReply
	if err := json.Unmarshal([]byte(content[start:end+1]), &reply); err != nil {
		return nil, "", fmt.Errorf("judge reply not valid JSON: %w", err)
	}
	return reply.Assertions, strings.TrimSpace(reply.Note), nil
}

// buildBundle renders the user message: the assertion list plus every
// mechanical evidence block. This is the judge's entire world.
func buildBundle(req Request, vd *Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task under judgment: %s\n\n", req.TaskTitle)

	b.WriteString("done_when assertions to verify (address EVERY one by its number):\n")
	if len(req.Assertions) == 0 {
		b.WriteString("  (none provided)\n")
	}
	for i, a := range req.Assertions {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, a)
	}

	b.WriteString("\nMechanical evidence (collected deterministically, before any judgment):\n\n")
	if len(vd.Tests) == 0 && vd.Diff.Evidence == "" && vd.Scope == nil {
		b.WriteString("  (no test commands, no deliverable, no scope root — nothing to check against)\n")
	}
	for _, t := range vd.Tests {
		b.WriteString(t.Evidence)
		b.WriteString("\n\n")
	}
	if vd.Diff.Evidence != "" {
		b.WriteString(vd.Diff.Evidence)
		b.WriteString("\n\n")
	}
	if vd.Scope != nil {
		b.WriteString(vd.Scope.Evidence)
		b.WriteString("\n\n")
	}

	b.WriteString(`Return ONLY the JSON object per the system prompt's contract.`)
	return b.String()
}
