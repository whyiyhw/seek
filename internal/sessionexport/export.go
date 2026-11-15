// Package sessionexport renders a persisted seek session (JSONL
// transcript, see internal/session) as a human-readable markdown
// conversation log:
//
//   - one `## <Role>` section per message, separated by `---`;
//   - tool calls folded to one summary line each (tool name + the
//     top-level argument pairs, truncated);
//   - tool results folded to their first line, with a total-char
//     note when the result was longer;
//   - image attachments as placeholder lines (the export never
//     inlines asset bytes);
//   - session metadata + timestamps preserved in the header block.
//
// Render is a pure function of the Session — no I/O, no clocks —
// so it is deterministic and cheap to test. The CLI surface
// (`seek session export`, internal/sessioncli) is one consumer;
// the serve web panel is the intended second (a "download
// transcript" button can call Render directly without going
// through the CLI layer).
//
// PredictedNext (suggested-reply artifacts) is deliberately NOT
// rendered: it is UI chrome, not conversation content.
package sessionexport

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Fold limits. Tool traffic is the bulk of a transcript and the
// whole point of the export is a readable log, so both the
// argument summary and the result line are bounded. Rune-based,
// never byte-slicing (transcripts are routinely CJK).
const (
	argsMax     = 160 // per tool-call summary line
	argValueMax = 60  // per individual argument value
	argsKeysMax = 3   // argument pairs shown before "+N more"
	resultMax   = 200 // per tool-result folded line
	imageRefMax = 120 // per image placeholder reference
)

// Render returns the markdown transcript of sess. Nil-safe: a nil
// session renders as the empty string.
func Render(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	var b strings.Builder
	writeHeader(&b, sess)
	names := toolNames(sess.Messages)
	for i := range sess.Messages {
		writeMessage(&b, &sess.Messages[i], names)
	}
	return b.String()
}

// writeHeader emits the session metadata block. Timestamps are
// the session-level Created/Updated pair (messages carry no
// per-message timestamps in the JSONL format, so these are the
// only timestamps the transcript has).
func writeHeader(b *strings.Builder, sess *session.Session) {
	fmt.Fprintf(b, "# seek session %s\n\n", sess.ID)
	fmt.Fprintf(b, "- **Model**: %s\n", sess.Model)
	fmt.Fprintf(b, "- **Created**: %s\n", sess.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(b, "- **Updated**: %s\n", sess.UpdatedAt.UTC().Format(time.RFC3339))
	if sess.CWD != "" {
		fmt.Fprintf(b, "- **CWD**: %s\n", sess.CWD)
	}
	if sess.ParentID != "" {
		fmt.Fprintf(b, "- **Branched from**: %s\n", sess.ParentID)
	}
	fmt.Fprintf(b, "- **Turns**: %d · **Tool calls**: %d\n", sess.Turns, sess.ToolCalls)
	if u := sess.Usage; u.TotalTokens > 0 {
		fmt.Fprintf(b, "- **Tokens**: prompt %s · completion %s · total %s",
			comma(u.PromptTokens), comma(u.CompletionTokens), comma(u.TotalTokens))
		if u.PromptCacheHitTokens > 0 {
			fmt.Fprintf(b, " · cache hit %s", comma(u.PromptCacheHitTokens))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
	if sess.SystemPrompt != "" {
		fmt.Fprintf(b, "<details>\n<summary>System prompt · %s chars</summary>\n\n", comma(len(sess.SystemPrompt)))
		b.WriteString(sess.SystemPrompt)
		if !strings.HasSuffix(sess.SystemPrompt, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n</details>\n\n")
	}
}

// toolNames indexes tool_call_id → function name across the whole
// transcript so tool-role messages (which carry only the id) can
// be labelled with the tool that produced them. Lazy allocation:
// transcripts without tool calls never pay for the map.
func toolNames(msgs []deepseek.Message) map[string]string {
	var names map[string]string
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID == "" {
				continue
			}
			if names == nil {
				names = make(map[string]string, len(m.ToolCalls))
			}
			names[tc.ID] = tc.Function.Name
		}
	}
	return names
}

func writeMessage(b *strings.Builder, m *deepseek.Message, names map[string]string) {
	switch m.Role {
	case deepseek.RoleUser:
		b.WriteString("---\n\n## User\n\n")
		if m.Content == "" && len(m.Images) == 0 {
			b.WriteString("*(empty)*\n\n")
			return
		}
		writeContent(b, m.Content)
		writeImages(b, m.Images)
	case deepseek.RoleAssistant:
		b.WriteString("---\n\n## Assistant\n\n")
		if m.Content == "" && m.ReasoningContent == "" && len(m.ToolCalls) == 0 {
			b.WriteString("*(empty)*\n\n")
			return
		}
		if m.ReasoningContent != "" {
			writeFence(b, "thinking", m.ReasoningContent)
		}
		writeContent(b, m.Content)
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(b, "- **%s** · `%s`\n", tc.Function.Name, summarizeArgs(tc.Function.Arguments))
		}
		if len(m.ToolCalls) > 0 {
			b.WriteString("\n")
		}
	case deepseek.RoleTool:
		name := m.Name
		if name == "" {
			name = names[m.ToolCallID]
		}
		if name == "" {
			name = "tool"
		}
		label := name
		if m.ToolCallID != "" {
			label += " (" + m.ToolCallID + ")"
		}
		fmt.Fprintf(b, "---\n\n## Tool · %s\n\n", label)
		writeToolResult(b, m.Content)
	case deepseek.RoleSystem:
		b.WriteString("---\n\n## System\n\n")
		writeContent(b, m.Content)
	default:
		fmt.Fprintf(b, "---\n\n## %s\n\n", m.Role)
		writeContent(b, m.Content)
	}
}

func writeContent(b *strings.Builder, content string) {
	if content == "" {
		return
	}
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// writeImages renders image attachments as placeholder lines. The
// asset bytes live under the project assets dir and may not travel
// with the exported file, so a reference is all an export can
// honestly promise.
func writeImages(b *strings.Builder, imgs []deepseek.ImagePart) {
	for _, img := range imgs {
		ref := img.Asset
		if ref == "" {
			ref = img.URL
		}
		if ref == "" {
			ref = "(unnamed)"
		}
		fmt.Fprintf(b, "*[image: %s]*\n\n", truncate(ref, imageRefMax))
	}
}

// writeFence wraps content in a code fence long enough to not
// collide with any fence appearing inside the content itself
// (reasoning transcripts routinely contain ``` blocks).
func writeFence(b *strings.Builder, info, content string) {
	fence := "```"
	for strings.Contains(content, fence) {
		fence += "`"
	}
	fmt.Fprintf(b, "%s%s\n%s\n%s\n\n", fence, info, content, fence)
}

// writeToolResult folds a tool result to its first non-empty line,
// bounded by resultMax runes, noting the full size when content
// was dropped.
func writeToolResult(b *strings.Builder, content string) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		b.WriteString("> *(empty output)*\n\n")
		return
	}
	folded := truncate(firstLine(trimmed), resultMax)
	total := utf8.RuneCountInString(trimmed)
	if total > utf8.RuneCountInString(folded) {
		fmt.Fprintf(b, "> %s *(… %s chars total)*\n\n", folded, comma(total))
	} else {
		fmt.Fprintf(b, "> %s\n\n", folded)
	}
}

// summarizeArgs folds a tool call's raw JSON arguments into
// `key=value` pairs: up to argsKeysMax keys, sorted for
// determinism, each value bounded by argValueMax runes, the whole
// line bounded by argsMax. Non-JSON arguments fall back to the
// raw string, truncated.
func summarizeArgs(raw string) string {
	if raw == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || len(obj) == 0 {
		return truncate(strings.TrimSpace(raw), argsMax)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	shown := keys
	if len(keys) > argsKeysMax {
		shown = keys[:argsKeysMax]
	}
	parts := make([]string, 0, len(shown))
	for _, k := range shown {
		parts = append(parts, k+"="+truncate(fmt.Sprint(obj[k]), argValueMax))
	}
	s := strings.Join(parts, " ")
	if len(keys) > len(shown) {
		s += fmt.Sprintf(" …(+%d more)", len(keys)-len(shown))
	}
	return truncate(s, argsMax)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "\r")
}

// truncate bounds s to n runes, marking the cut with an ellipsis.
// Rune-safe: never splits a multi-byte character mid-sequence.
func truncate(s string, n int) string {
	if n <= 1 || utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// comma renders n with thousands separators (1,234,567) — the
// token and char counts in the header/fold notes read easier.
func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
