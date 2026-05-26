package tui

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Tests in this file pin two bugs the v0.3.x review found but were
// still unfixed when we got here:
//
//   - cmdSetup runs mid-stream (sweep finding 3) — every other modal-
//     opening command checks m.streaming; cmdSetup was missing the gate.
//   - resume turn-count reconstruction sums ToolCalls across ALL message
//     roles (B6) — only assistant turns can carry tool_calls; counting
//     a RoleUser/RoleTool/RoleSystem message's tool_calls inflates the
//     status-bar count for malformed sessions.
//
// Pair (RED test, then GREEN fix) — these tests should fail against
// the pre-fix code, pass after the fix. They stay around as the
// regression dam.

// TestCmdSetup_RefusesMidStream pins the contract: cmdSetup must
// refuse with a "wait for current turn" notice when m.streaming is
// true, mirroring cmdNew/cmdBranch/cmdCompact/cmdUpgrade/cmdDistill.
// Without this guard the picker opens mid-stream, the user lands in
// setupKeyEntry, and an Esc-cancel hijack (already guarded in
// handleStreamEnd, see streamend_test.go) becomes one of several
// stacked surprises.
func TestCmdSetup_RefusesMidStream(t *testing.T) {
	t.Parallel()

	m := testModel().Streaming().BuildPtr()

	res := cmdSetup(m, "")

	if !strings.Contains(res.text, "wait") || !strings.Contains(res.text, "turn") {
		t.Errorf("expected 'wait for the current turn' notice, got %q", res.text)
	}
	if m.modelPickerOpen {
		t.Error("cmdSetup must NOT open the picker mid-stream")
	}
	if m.pickerPurpose == "setup-provider" {
		t.Error("cmdSetup must NOT set pickerPurpose mid-stream")
	}
}

// TestCmdSetup_OpensPickerWhenNotStreaming is the positive control:
// cmdSetup outside a stream still opens the provider picker as
// designed. Without this, the guard above could regress into "always
// refuse" and we'd miss it.
func TestCmdSetup_OpensPickerWhenNotStreaming(t *testing.T) {
	t.Parallel()

	m := testModel().BuildPtr()

	cmdSetup(m, "")

	if !m.modelPickerOpen {
		t.Error("cmdSetup should open the model picker when not streaming")
	}
	if m.pickerPurpose != "setup-provider" {
		t.Errorf("pickerPurpose = %q, want %q", m.pickerPurpose, "setup-provider")
	}
}

// TestNew_TurnCountIgnoresNonAssistantToolCalls pins review finding
// B6: a session whose user/tool/system messages somehow carry a
// non-empty ToolCalls slice must NOT inflate m.toolCalls. Only
// RoleAssistant messages can legitimately carry tool_calls (DeepSeek
// API contract). Counting other roles' synthetic ToolCalls is a
// defensive miss that the original loop did unconditionally.
func TestNew_TurnCountIgnoresNonAssistantToolCalls(t *testing.T) {
	t.Parallel()

	// Synthetic malformed session: a RoleUser message with a bogus
	// ToolCalls slice (shouldn't happen in practice, but the role
	// filter is what guards against malformed input on resume).
	sess := &session.Session{
		ID: "test",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "hi", ToolCalls: []deepseek.ToolCall{
				{Function: deepseek.ToolCallFunc{Name: "bogus"}}, // must NOT count
			}},
			{Role: deepseek.RoleAssistant, Content: "ok", ToolCalls: []deepseek.ToolCall{
				{Function: deepseek.ToolCallFunc{Name: "real1"}},
				{Function: deepseek.ToolCallFunc{Name: "real2"}},
			}},
			{Role: deepseek.RoleTool, Content: "result", ToolCalls: []deepseek.ToolCall{
				{Function: deepseek.ToolCallFunc{Name: "bogus2"}}, // must NOT count
			}},
		},
	}

	m := New(Options{
		Tracker: cache.New(),
		Model:   "deepseek-chat",
		Session: sess,
	})

	// Only the RoleAssistant's 2 tool calls should count.
	if m.toolCalls != 2 {
		t.Errorf("toolCalls = %d, want 2 (only RoleAssistant's tool_calls should count; the RoleUser and RoleTool synthetic entries are malformed input)",
			m.toolCalls)
	}
	if m.turns != 1 {
		t.Errorf("turns = %d, want 1 (one assistant message)", m.turns)
	}
}

// TestNew_TurnCountCountsRealAssistantToolCalls is the positive
// control: a well-formed session with multiple assistant turns and
// their tool_calls is summed correctly. Without this, the role filter
// could regress into "filter everything" and tests would still pass.
func TestNew_TurnCountCountsRealAssistantToolCalls(t *testing.T) {
	t.Parallel()

	sess := &session.Session{
		ID: "test",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "do stuff"},
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{Function: deepseek.ToolCallFunc{Name: "read"}},
			}},
			{Role: deepseek.RoleTool, Content: "file content"},
			{Role: deepseek.RoleAssistant, Content: "here you go", ToolCalls: []deepseek.ToolCall{
				{Function: deepseek.ToolCallFunc{Name: "edit"}},
				{Function: deepseek.ToolCallFunc{Name: "bash"}},
			}},
		},
	}

	m := New(Options{
		Tracker: cache.New(),
		Model:   "deepseek-chat",
		Session: sess,
	})

	if m.toolCalls != 3 {
		t.Errorf("toolCalls = %d, want 3 (1 from first assistant + 2 from second)",
			m.toolCalls)
	}
	if m.turns != 2 {
		t.Errorf("turns = %d, want 2", m.turns)
	}
}
