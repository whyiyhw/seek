package tui

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Direct helper tests live in view_test.go alongside the shared
// renderUserBlock / renderAssistantBlock / renderToolResultLine
// functions. Replay-side coverage lives here as integration tests
// against renderReplayHistory — the only function unique to replay.

// --- renderReplayHistory ---------------------------------------------------

func sessionWith(msgs []deepseek.Message) *session.Session {
	return &session.Session{ID: "test-session", Messages: msgs}
}

// countReplayBlocks counts how many message blocks a rendered replay
// string represents. Each user message produces a block starting with
// the "▌ you" label; each assistant block starts with "▸ seek". We use
// stripped (ANSI-free) strings.Count to avoid depending on lipgloss
// styling state in the test.
func countReplayBlocks(s string) int {
	return strings.Count(stripANSI(s), "▌ you") + strings.Count(stripANSI(s), "▸ seek")
}

func TestRenderReplayHistory_UserMessagesIncluded(t *testing.T) {
	t.Parallel()
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleUser, Content: "hello"},
		{Role: deepseek.RoleAssistant, Content: "hi there"},
		{Role: deepseek.RoleUser, Content: "thanks"},
	}), false, 0, "")
	if out == "" {
		t.Fatal("expected non-empty output for a session with messages")
	}
	if got := countReplayBlocks(out); got == 0 {
		t.Error("expected at least one rendered block")
	}
}

func TestRenderReplayHistory_SystemMessagesSkipped(t *testing.T) {
	t.Parallel()
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "You are a bot."},
		{Role: deepseek.RoleUser, Content: "hello"},
	}), false, 0, "")
	if got := countReplayBlocks(out); got != 1 {
		t.Errorf("replay blocks = %d, want 1 (user msg only, system skipped)", got)
	}
}

func TestRenderReplayHistory_ToolMessagesRendered(t *testing.T) {
	t.Parallel()
	// Tool results are now rendered as `↳ name(args) → N bytes` lines.
	// A lone tool result without a preceding assistant with matching
	// tool_calls is still skipped (no toolCallMap entry).
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleTool, Content: "file content", ToolCallID: "c1"},
		{Role: deepseek.RoleAssistant, Content: "done"},
	}), false, 0, "")
	if got := countReplayBlocks(out); got != 1 {
		t.Errorf("replay blocks = %d, want 1 (assistant msg only, lone tool dropped)", got)
	}
}

func TestRenderReplayHistory_EmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	if got := renderReplayHistory(nil, false, 0, ""); got != "" {
		t.Errorf("nil session must return empty string, got %q", got)
	}
	if got := renderReplayHistory(sessionWith(nil), false, 0, ""); got != "" {
		t.Errorf("empty messages must return empty string, got %q", got)
	}
}

func TestRenderReplayHistory_EmptyAssistantSkipped(t *testing.T) {
	t.Parallel()
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleAssistant}, // no content, no tool_calls
		{Role: deepseek.RoleUser, Content: "hi"},
	}), false, 0, "")
	if got := countReplayBlocks(out); got != 1 {
		t.Errorf("replay blocks = %d, want 1 (empty assistant skipped, user counted)", got)
	}
}

func TestRenderReplayHistory_OneBlockPerMessage(t *testing.T) {
	t.Parallel()
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleUser, Content: "u1"},
		{Role: deepseek.RoleAssistant, Content: "a1"},
	}), false, 0, "")
	if got := countReplayBlocks(out); got != 2 {
		t.Errorf("replay blocks = %d, want 2 (1 user + 1 assistant)", got)
	}
}

// TestRenderReplayHistory_ToolCallAssistantNoDuplicate pins the Option-A
// invariant: a tool call must appear EXACTLY ONCE in replay scrollback
// (as the `↳ name(args) → N bytes` line from the RoleTool message). The
// previous implementation rendered tool calls TWICE — once as inline
// `🛠 name(args)` inside the assistant block, again as `↳ ...` from the
// matching tool message. Live path only emits the `↳` form.
func TestRenderReplayHistory_ToolCallAssistantNoDuplicate(t *testing.T) {
	t.Parallel()
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{
				{ID: "c1", Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"x"}`}},
			},
		},
		{Role: deepseek.RoleTool, ToolCallID: "c1", Content: "file content"},
	}), false, 0, "")
	plain := stripANSI(out)
	if got := countReplayBlocks(out); got != 0 {
		t.Errorf("replay blocks = %d, want 0 (tool-call-only assistant has no ▸ seek)", got)
	}
	if !strings.Contains(plain, `↳ read`) {
		t.Errorf("tool result line missing from output:\n%s", plain)
	}
	if strings.Contains(plain, "🛠") {
		t.Errorf("replay must NOT render the inline 🛠 marker (live path doesn't); got:\n%s", plain)
	}
	if strings.Count(plain, "read(") != 1 {
		t.Errorf("tool call should appear exactly once, got %d occurrences:\n%s",
			strings.Count(plain, "read("), plain)
	}
}

func TestRenderReplayHistory_MixedAllTypes(t *testing.T) {
	t.Parallel()
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "prompt"},            // skip
		{Role: deepseek.RoleUser, Content: "q"},                   // 1 block
		{Role: deepseek.RoleAssistant, Content: "a"},              // 1 block
		{Role: deepseek.RoleTool, Content: "t", ToolCallID: "c1"}, // skip (no matching tool_calls in preceding assistant)
		{Role: deepseek.RoleAssistant, Content: "done"},           // 1 block
	}), false, 0, "")
	// 3 counted messages: user + 2× assistant. (system + tool are skipped.)
	if got := countReplayBlocks(out); got != 3 {
		t.Errorf("replay blocks = %d, want 3 (system+tool skipped, user + 2 assistant)", got)
	}
}

func TestRenderReplayHistory_ReasoningPassedThrough(t *testing.T) {
	t.Parallel()
	// showReasoning=false → hidden hint should appear.
	out := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleAssistant, Content: "answer", ReasoningContent: "plan"},
	}), false, 0, "")
	if !strings.Contains(stripANSI(out), "reasoning hidden") {
		t.Errorf("expected hidden-reasoning hint when showReasoning=false; got:\n%s", out)
	}

	// showReasoning=true → reasoning body must appear.
	out2 := renderReplayHistory(sessionWith([]deepseek.Message{
		{Role: deepseek.RoleAssistant, Content: "answer", ReasoningContent: "plan"},
	}), true, 0, "")
	if !strings.Contains(stripANSI(out2), "plan") {
		t.Errorf("expected reasoning body when showReasoning=true; got:\n%s", out2)
	}
}
