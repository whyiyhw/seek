package tui

import (
	"strings"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// renderReplayHistory returns the joined scrollback lines for a
// session's prior messages (user + assistant + tool results; system
// role skipped). The result is suitable for direct stdout write BEFORE
// `tea.NewProgram` starts: the bytes go into the terminal's native
// scrollback as one batch, no per-message redraw cycles.
//
// Empty session / no qualifying messages → empty string.
//
// Why pre-bubbletea: doing this from inside the Update loop (via
// tea.Println per message) triggers a full clear+reprint cycle for
// EACH message. A 100-message resume becomes 100 redraws and the
// scrollback fills with visible flicker. Writing once to stdout
// before bubbletea takes over avoids the redraw loop entirely.
//
// Renderers (renderUserBlock / renderAssistantBlock / renderToolResultLine)
// are SHARED with the live commit path. Every divergence the v0.3.x
// review caught — bypassed Markdown, spurious `▸ seek` on tool-only
// turns, untruncated args, duplicated 🛠/↳ rows — disappears because
// both paths run the same code now.
func renderReplayHistory(sess *session.Session, showReasoning bool, width int, style string) string {
	if sess == nil || len(sess.Messages) == 0 {
		return ""
	}

	// Build a markdown renderer once for the whole replay. newMarkdownRenderer
	// clamps width <20 up to 20, so width==0 (terminal size not known yet
	// at --resume time before NewProgram) still produces a usable renderer.
	md := newMarkdownRenderer(width, style)

	var lines []string
	// toolCallMap tracks ToolCallID → original ToolCall from the preceding
	// assistant message. Cleared on every new user/assistant message that
	// carries its own tool_calls (the API never interleaves tool results
	// from different assistant turns).
	var toolCallMap map[string]deepseek.ToolCall

	for _, msg := range sess.Messages {
		switch msg.Role {
		case deepseek.RoleSystem:
			continue
		case deepseek.RoleUser:
			toolCallMap = nil // new user turn resets tracking context
			// width=0 → no app-side wrap; renderUserBlock lets the
			// terminal wrap natively at whatever the user's current
			// width is.
			lines = append(lines, renderUserBlock(msg.Content, 0))
		case deepseek.RoleAssistant:
			// Index this assistant's tool calls by ID so subsequent
			// RoleTool messages can render name(args) → N bytes.
			toolCallMap = make(map[string]deepseek.ToolCall, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				toolCallMap[tc.ID] = tc
			}
			// Pure tool-call turns (no narrative) → renderAssistantBlock
			// returns "" — matches the live MessageEnd skip path. The
			// `↳ tool(...)` lines emitted by the RoleTool branch below
			// already carry the signal.
			if line := renderAssistantBlock(msg.Content, msg.ReasoningContent, showReasoning, width, md); line != "" {
				lines = append(lines, line)
			}
		case deepseek.RoleTool:
			// Render the tool result via the SAME function the live
			// ToolExecEnd path uses. Session doesn't record duration or
			// completion-token counts, so both are 0 — the helper
			// already drops sub-100ms durations and zero-token tails.
			// Errors aren't recorded as RoleTool messages either (the
			// live path would have written them mid-stream and they're
			// not in session.Messages), so err is always nil here.
			if tc, ok := toolCallMap[msg.ToolCallID]; ok {
				args := truncateOneLine(tc.Function.Arguments, 80)
				lines = append(lines, renderToolResultLine(tc.Function.Name, args, msg.Content, nil, 0, 0))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}
