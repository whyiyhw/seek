package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Candidate is one distillation result — a project-level decision the
// reasoner thought worth committing to M. Users review candidates one
// by one (y/n/e) before they land in memory.jsonl.
//
// JSON tags match what the LLM emits and what memory.Entry expects, so
// an approved Candidate can be inlined into an Entry without renaming.
type Candidate struct {
	Name    string   `json:"name"`
	Tagline string   `json:"tagline"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
}

// DefaultMaxCandidates is the per-distillation cap from PRD §6 — "≤3"
// because more than that overwhelms the user's review pass and the
// model starts grasping at straws.
const DefaultMaxCandidates = 3

// DistillSystemPrompt frames thinking mode's task. Bilingual reflects
// PRD §6's intent and keeps the rule list specific enough that the
// model rejects user-trait noise instead of leaking it into M.
//
// The "respond with [] if nothing qualifies" instruction is load-bearing:
// without it, the model invents weak candidates to fill the quota.
const DistillSystemPrompt = `You are a session-distillation engine.

You will read the conversation history of one seek session and extract
project-specific, actionable decisions worth preserving in this
project's memory (M layer). Future sessions in this project should be
able to skip rediscovering these.

REQUIREMENTS for every candidate:
- name: kebab-case slug, ≤40 chars (e.g. session-storage-format)
- tagline: one specific sentence (NOT generic — "uses JSONL for
  prefix-cache" is good; "improves performance" is bad)
- content: full rationale, ≤500 words. Cover what was decided, why,
  what alternatives were rejected, and any non-obvious constraints
- tags: optional categorisation (architecture, testing, etc.)

REJECT (these belong elsewhere or nowhere):
- User preferences ("user likes terse explanations") — that's the L
  (soul) layer, written only by ` + "`seek -dream`" + `, never by /distill
- Session-scoped facts ("we ran 'go test' and 7 passed")
- Generic programming truths ("Go uses interfaces for polymorphism")
- Decisions made but later reverted in the same session

If no candidates qualify, return an empty array. DO NOT invent weak
candidates to fill the quota.

Respond with ONLY a JSON array of objects matching the schema. No
prose, no markdown fences, no commentary. The orchestrator will
parse your output directly.`

// BuildDistillUserMessage renders the user-role message that goes to
// thinking mode. The system prompt is fixed (DistillSystemPrompt); this
// is the per-call variant carrying the conversation transcript + the
// candidate cap.
//
// The transcript is rendered as plain prose ("user: ...", "assistant:
// ...") rather than re-serialising the deepseek.Message objects. The
// reasoner doesn't need tool_call structure for distillation — it needs
// to read what happened.
func BuildDistillUserMessage(history []deepseek.Message, maxCandidates int) string {
	if maxCandidates <= 0 {
		maxCandidates = DefaultMaxCandidates
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Extract up to %d candidates from this session.\n\n", maxCandidates)
	sb.WriteString("=== conversation ===\n")
	for _, m := range history {
		// System messages aren't conversation — skip. The reasoner
		// already has its own framing in DistillSystemPrompt.
		if m.Role == deepseek.RoleSystem {
			continue
		}
		switch m.Role {
		case deepseek.RoleTool:
			fmt.Fprintf(&sb, "[tool result %s]: %s\n", m.ToolCallID, truncate(m.Content, 800))
		default:
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "  [tool call: %s(%s)]\n", tc.Function.Name, truncate(tc.Function.Arguments, 200))
			}
		}
	}
	sb.WriteString("=== end conversation ===\n\n")
	sb.WriteString("Respond with a JSON array of candidates. Empty array if nothing qualifies.")
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ParseCandidates extracts candidates from thinking mode's raw response.
// Tolerant of formatting variations the model produces despite the
// "JSON only" instruction: markdown ` ```json ` fences, single object
// instead of an array, leading "Here are the candidates:" prose.
//
// Returns (nil, nil) for an explicit empty array — that's a valid
// "nothing to distill" verdict. Returns an error for non-JSON garbage
// so the caller can show the raw text and let the user retry.
func ParseCandidates(raw string) ([]Candidate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("distill: empty response")
	}
	raw = stripCodeFence(raw)
	raw = trimLeadingProse(raw)

	// Single object case: wrap in an array so the unmarshal path is
	// uniform. Detected by leading "{" without surrounding "[".
	if strings.HasPrefix(raw, "{") {
		var c Candidate
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, fmt.Errorf("distill: single-object parse: %w", err)
		}
		if c.Name == "" {
			return nil, nil
		}
		return []Candidate{c}, nil
	}

	if !strings.HasPrefix(raw, "[") {
		return nil, fmt.Errorf("distill: expected JSON array or object, got %q", truncate(raw, 60))
	}

	var out []Candidate
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("distill: array parse: %w", err)
	}
	return out, nil
}

// stripCodeFence removes a leading ```json ... ``` or ``` ... ```
// wrapper if the model emitted one. Idempotent on un-fenced input.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the first line (```json or ```).
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return strings.TrimPrefix(s, "```")
	}
	// Drop the trailing ``` (and any trailing prose after it).
	if end := strings.LastIndex(s, "```"); end >= 0 {
		s = s[:end]
	}
	return strings.TrimSpace(s)
}

// trimLeadingProse drops any text before the first '[' or '{' so a
// "Here are the candidates: [...]" preamble doesn't break parsing.
func trimLeadingProse(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '[' || s[i] == '{' {
			return s[i:]
		}
	}
	return s
}

// chatClient is the minimum surface Distiller needs from a DeepSeek
// client. Defined as an interface so tests can plug in a fake without
// spinning up an httptest server for one call.
type chatClient interface {
	Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error)
}

// Distiller orchestrates one /distill pass: prompt construction →
// thinking-mode call → response parse → return Candidate slice.
//
// Model defaults to ModelV4Flash with Thinking enabled per PRD §6 —
// distillation explicitly asks for chain-of-thought because picking
// which decisions are worth preserving is exactly the reasoning
// work thinking mode is good at. (Pre-2026-05 this used the
// deepseek-reasoner alias, which routes to the same backend; the
// switch is so callers survive the 2026-07-24 alias sunset.)
type Distiller struct {
	Client chatClient
	Model  string // default deepseek.ModelV4Flash (+ Thinking)
	Max    int    // default DefaultMaxCandidates
}

// Distill runs the full pipeline. The returned Candidates have NOT been
// reviewed by the user yet — the caller (TUI) prompts for each one.
func (d *Distiller) Distill(ctx context.Context, history []deepseek.Message) ([]Candidate, error) {
	if d.Client == nil {
		return nil, errors.New("distill: Client is required")
	}
	model := d.Model
	if model == "" {
		model = deepseek.ModelV4Flash
	}
	maxN := d.Max
	if maxN <= 0 {
		maxN = DefaultMaxCandidates
	}

	req := &deepseek.ChatRequest{
		Model: model,
		Messages: deepseek.StripReasoningContent([]deepseek.Message{
			{Role: deepseek.RoleSystem, Content: DistillSystemPrompt},
			{Role: deepseek.RoleUser, Content: BuildDistillUserMessage(history, maxN)},
		}),
	}
	// Distillation is a single-shot reasoning call — opt the model into
	// thinking mode explicitly. The legacy deepseek-reasoner alias used
	// to provide this implicitly; the explicit V4 names do not.
	if deepseek.ShouldEnableThinking(model) || model == deepseek.ModelV4Flash {
		req.Thinking = &deepseek.ThinkingMode{Type: "enabled"}
	}

	resp, err := d.Client.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("distill: chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("distill: reasoner returned no choices")
	}
	return ParseCandidates(resp.Choices[0].Message.Content)
}
