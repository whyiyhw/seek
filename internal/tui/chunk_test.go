package tui

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestChunkCommit_TriggersAtThreshold drives the long-reply chunk
// path: once the assistant live block passes chunkThreshold rendered
// rows, applyAgentEvent must commit the segment to scrollback (one
// cmd) and reset the live buffers.
func TestChunkCommit_TriggersAtThreshold(t *testing.T) {
	m := testModel().BuildPtr()
	m.width = 80
	m.chunkThreshold = 12

	// 9 lines of 70 chars each → 9 wrapped rows + 1 label = 11 < 12.
	for i := 0; i < 9; i++ {
		cmds := m.applyAgentEvent(agent.MessageDelta{Delta: strings.Repeat("x", 70) + "\n"})
		if len(cmds) != 0 {
			t.Fatalf("delta %d: got %d cmds, want 0 (below threshold)", i, len(cmds))
		}
	}
	if m.chunked {
		t.Fatal("chunked must stay false below threshold")
	}

	// One more line → 10 wrapped rows + 1 label = 12 >= 12 → commit.
	cmds := m.applyAgentEvent(agent.MessageDelta{Delta: strings.Repeat("y", 70) + "\n"})
	if len(cmds) != 1 {
		t.Fatalf("expected 1 commit cmd, got %d", len(cmds))
	}
	if !m.chunked {
		t.Error("chunked must be true after a chunk commit")
	}
	if m.curContent != "" {
		t.Errorf("curContent must be reset after chunk commit, got %q", m.curContent)
	}
	// A fresh delta after the commit starts the next segment without
	// re-committing (it is below the threshold again).
	cmds = m.applyAgentEvent(agent.MessageDelta{Delta: strings.Repeat("z", 70) + "\n"})
	if len(cmds) != 0 {
		t.Fatalf("delta after commit must not re-commit immediately, got %d cmds", len(cmds))
	}
}

// TestChunkCommit_FenceDefersSplit: an unclosed ``` code block defers
// the split until the fence closes (so the segment boundary never
// lands mid-code-block).
func TestChunkCommit_FenceDefersSplit(t *testing.T) {
	m := testModel().BuildPtr()
	m.width = 80
	m.chunkThreshold = 12

	// 13 lines starting with an open fence → rows >= threshold but
	// fence unclosed → defer.
	lines := "```\n" + strings.Repeat("code\n", 12)
	cmds := m.applyAgentEvent(agent.MessageDelta{Delta: lines})
	if len(cmds) != 0 {
		t.Fatalf("unclosed fence must defer the split, got %d cmds", len(cmds))
	}
	// Closing the fence → split fires.
	cmds = m.applyAgentEvent(agent.MessageDelta{Delta: "```\n"})
	if len(cmds) != 1 {
		t.Fatalf("after fence close, expected 1 commit cmd, got %d", len(cmds))
	}
	if !m.chunked {
		t.Error("chunked must be true after the deferred split")
	}
}

// TestChunkCommit_HardLimitBypassesFence: past 2x the threshold an
// unclosed fence cannot stall chunking (a pathological single code
// block must not be able to freeze the live region).
func TestChunkCommit_HardLimitBypassesFence(t *testing.T) {
	m := testModel().BuildPtr()
	m.width = 80
	m.chunkThreshold = 12

	lines := "```\n" + strings.Repeat("code\n", 40) // 42 rows, fence still open
	cmds := m.applyAgentEvent(agent.MessageDelta{Delta: lines})
	if len(cmds) != 1 {
		t.Fatalf("hard limit must force the split despite unclosed fence, got %d cmds", len(cmds))
	}
}

// TestChunkCommit_MessageEndUsesFullContent: after chunking, the goal
// judge must receive the FULL assembled message, not the final
// segment (m.curContent holds only the tail once chunked).
func TestChunkCommit_MessageEndUsesFullContent(t *testing.T) {
	m := testModel().BuildPtr()
	m.chunked = true
	m.curContent = "final segment"
	m.applyAgentEvent(agent.MessageEnd{
		Message: deepseek.Message{Role: deepseek.RoleAssistant, Content: "full content"},
	})
	if m.lastAssistantText != "full content" {
		t.Errorf("lastAssistantText = %q, want the full assembled message", m.lastAssistantText)
	}
}

// TestRenderAssistantBlockLabel pins the continuation label variant.
func TestRenderAssistantBlockLabel(t *testing.T) {
	out := stripANSI(renderAssistantBlockLabel("hi", "", false, 80, nil, "▸ seek (续)"))
	if !strings.Contains(out, "▸ seek (续)") {
		t.Errorf("continuation label not rendered: %q", out)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("content missing: %q", out)
	}
	plain := stripANSI(renderAssistantBlock("hi", "", false, 80, nil))
	if !strings.Contains(plain, "▸ seek") || strings.Contains(plain, "续") {
		t.Errorf("plain renderer must keep the base label only: %q", plain)
	}
}

// TestUnclosedFence pins the odd/even fence counting.
func TestUnclosedFence(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"no fence here", false},
		{"```\ncode\n```", false},
		{"```\ncode", true},
		{"text ``` still open", true},
	}
	for _, c := range cases {
		if got := unclosedFence(c.in); got != c.want {
			t.Errorf("unclosedFence(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestStreamDeltaBytes_CountsReasoning pins the estimate's accounting
// contract: reasoning deltas count towards streamDeltaBytes so the
// ↓~Xtok indicator keeps moving during the thinking phase (effort:max)
// and tracks Usage.CompletionTokens, which includes reasoning tokens.
func TestStreamDeltaBytes_CountsReasoning(t *testing.T) {
	m := testModel().BuildPtr()

	m.applyAgentEvent(agent.MessageDelta{Delta: "think…", Reasoning: true})
	if m.streamDeltaBytes != len("think…") {
		t.Errorf("reasoning delta not counted: got %d, want %d", m.streamDeltaBytes, len("think…"))
	}

	before := m.streamDeltaBytes
	m.applyAgentEvent(agent.MessageDelta{Delta: "answer"})
	if m.streamDeltaBytes != before+len("answer") {
		t.Errorf("content delta must add on top of reasoning bytes: got %d, want %d", m.streamDeltaBytes, before+len("answer"))
	}
}
