// Package titlesug derives a short human title for a stored conversation
// with one cheap Flash-tier side-channel call. It is deliberately a
// sibling of internal/suggester (next-reply prediction), not part of it:
// the input window, prompt, output shape and lifecycle are all different,
// and merging the two would force one prompt to evolve with the other.
//
// Design contract:
//
//   - Best-effort: Generate never returns an error. ctx cancel / timeout /
//     API failure / refusal / junk output all collapse to "" — callers
//     leave the derived first-user-line title in place.
//   - Bounded: only the opening turns are sent (maxInputBytes), and the
//     request carries a tight max_tokens cap. A 100K-token Pro transcript
//     must not be replayed into a Flash call just to name it.
//   - Once: the caller persists the returned title into the session
//     header; Generate itself has no state and never writes to disk.
package titlesug

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

const (
	// GenerateTimeout is the ctx budget callers should give Generate.
	// Long enough for one Flash call, short enough that a background
	// naming request can never hold up the next turn.
	GenerateTimeout = 5 * time.Second

	// model is Flash tier: a title is a UX label, not a load-bearing
	// decision. Same lane as the suggested-reply predictor.
	model = deepseek.ModelV41Flash

	// maxOutputTokens keeps the model to a clause. Thinking is disabled
	// explicitly (V4-Flash defaults to thinking-on at the API level,
	// which would drain this budget into reasoning_content).
	maxOutputTokens = 40

	// maxInputBytes bounds how much of the opening transcript reaches
	// the naming call. ~4 KiB is plenty for a task statement and keeps
	// a pasted 200 KB log from becoming a second full-price request.
	maxInputBytes = 4096

	// maxInputMessages caps the opening window at three user→assistant
	// exchanges. A title describes the INITENT, which is settled in the
	// opening turns; later turns are implementation chatter.
	maxInputMessages = 6
)

// systemPrompt is kept literal and short so the cheap model has no room
// to embellish. The "no quotes" rule is repeated by cleanTitle, which
// strips them again if the model ignores it.
const systemPrompt = `You name coding-assistant conversations. Given the opening of a conversation, output ONE short title naming what the user wants done.

Rules:
- Output ONLY the title text: no quotes, no "Title:" prefix, no explanation, no markdown.
- One line. At most 20 Chinese characters or 8 English words.
- Prefer a concrete task ("修复 WebSocket 重连", "add pagination to /users") over a generic label ("request", "help", "question").
- Never invent facts absent from the transcript.`

// Generate asks the model for a one-line title and returns a cleaned,
// length-capped value. Returns "" on any failure or empty result; the
// caller then keeps the derived FirstUserTitle value.
func Generate(ctx context.Context, client *deepseek.Client, history []deepseek.Message) string {
	if client == nil || len(history) == 0 {
		return ""
	}
	opening := openingTranscript(history)
	if opening == "" {
		return ""
	}
	resp, err := client.Chat(ctx, &deepseek.ChatRequest{
		Model: model,
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: systemPrompt},
			{Role: deepseek.RoleUser, Content: opening},
		},
		MaxTokens: maxOutputTokens,
		Thinking:  &deepseek.ThinkingMode{Type: "disabled"},
	})
	if err != nil || resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return cleanTitle(resp.Choices[0].Message.Content)
}

// openingTranscript flattens the first maxInputMessages non-tool text
// messages into a single labelled block. Reasoning content and the
// suggested-reply hint are stripped so no side-channel artifact reaches
// the wire; tool calls/results are skipped (their arguments swamp the
// intent with file paths and JSON).
func openingTranscript(history []deepseek.Message) string {
	cleaned := deepseek.StripReasoningContent(history)
	var b strings.Builder
	used := 0
	count := 0
	for _, m := range cleaned {
		if count >= maxInputMessages {
			break
		}
		text := strings.TrimSpace(m.Content)
		if text == "" || len(m.ToolCalls) > 0 {
			continue // assistant tool dispatch: no intent text to name
		}
		var label string
		switch m.Role {
		case deepseek.RoleUser:
			label = "User: "
		case deepseek.RoleAssistant:
			label = "Assistant: "
		default:
			continue // system prompts are ours, not the user's intent
		}
		if used > 0 {
			b.WriteByte('\n')
			used++ // newline costs a byte too
		}
		b.WriteString(label)
		used += len(label)
		// Rune-wise append so a CJK byte sequence is never cut in half.
		for _, r := range text {
			n := utf8.RuneLen(r)
			if used+n > maxInputBytes {
				return strings.TrimSpace(b.String())
			}
			b.WriteRune(r)
			used += n
		}
		count++
	}
	return strings.TrimSpace(b.String())
}

// cleanTitle normalises the raw model output into a single-line label.
// The rules mirror session.oneLineTitle's output shape (first line,
// trimmed, TitleMaxRunes cap) so a generated title and a derived one
// can't drift into two display styles.
func cleanTitle(raw string) string {
	var line string
	for l := range strings.SplitSeq(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if line == "" {
		return ""
	}
	// The model sometimes wraps the title in quotes despite the prompt.
	for _, pair := range [][2]string{
		{`"`, `"`},
		{`'`, `'`},
		{"“", "”"},
		{"‘", "’"},
		{"「", "」"},
		{"《", "》"},
	} {
		if strings.HasPrefix(line, pair[0]) && strings.HasSuffix(line, pair[1]) &&
			len(line) > len(pair[0])+len(pair[1]) {
			line = strings.TrimSpace(line[len(pair[0]) : len(line)-len(pair[1])])
		}
	}
	// Same cap shape as session.oneLineTitle: ≤TitleMaxRunes runes
	// verbatim, otherwise the first TitleMaxRunes + ellipsis (81 total).
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) <= session.TitleMaxRunes {
		return line
	}
	runes := []rune(line)
	return string(runes[:session.TitleMaxRunes]) + "…"
}
