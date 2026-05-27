// Package suggester is the v4 柱 D "suggested reply" subsystem:
// after each main LLM turn ends, it runs a side-channel inference to
// predict what the user is likely to say next, and on mispredict
// injects a calibration system note into the next turn's messages so
// the model can self-correct.
//
// See PRD docs/prd/feature-suggested-reply.md.
//
// Design contract:
//
//   - Side-channel: the prediction call uses its own DeepSeek client +
//     a Flash-tier model + a tight max_tokens cap. The main transcript
//     is never modified by this package — the predicted text lives
//     on the assistant message's PredictedNext field, which
//     `deepseek.StripReasoningContent` clears before every API call.
//   - Best-effort: Suggest never returns an error. ctx timeout / API
//     failure / refusal all collapse to "". The TUI just doesn't show
//     a placeholder.
//   - Calibration injection: InjectCalibration is a pure function on
//     []deepseek.Message — called from the agent's MessagePreparer
//     hook just before each ChatRequest. It inserts a synthetic system
//     message ONLY when the immediately-prior assistant turn's
//     PredictedNext fails normalizedMatch against the latest user
//     message; otherwise it returns msgs unchanged.
package suggester

import (
	"context"
	"strings"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// defaultModel is the model used for prediction calls. Flash tier —
// cheap + low latency — because predictions are speculative UX hints,
// not load-bearing decisions.
const defaultModel = "deepseek-v4-flash"

// maxPredictionTokens caps the prediction length. 80 tokens ≈ one
// sentence in English / one short clause in Chinese; anything longer
// is the model going off the rails and gets discarded post-hoc.
const maxPredictionTokens = 80

// systemPrompt instructs the prediction model. Kept short and explicit
// so the cheap Flash model has zero room to embellish.
const systemPrompt = `You predict the user's next message in a coding-assistant conversation.

Output ONLY the predicted next message text — no quotes, no explanation,
no "I think the user would say…" preamble. 1 short sentence, ≤ 15 words.

If the prior assistant turn ended in a multiple-choice question
("[A] do X, [B] do Y"), predict the choice the user is most likely
to make + minimal expansion ("A" / "A 方案" / "用 A").

If the prior assistant turn ended ambiguously (no clear next step),
output an empty line.

Never assume facts not in the transcript.`

// predictNextSentinel is the synthetic user message appended at the
// end of the transcript to trigger the prediction model. Kept as a
// short literal token so the Flash model can recognise the intent
// without burning context on a long instruction.
const predictNextSentinel = "<predict-next/>"

// Predictor runs side-channel "what will the user say next" calls.
// Constructed once at startup; safe for concurrent use (the embedded
// DeepSeek client is concurrent-safe).
type Predictor struct {
	client *deepseek.Client
	model  string
}

// Option configures a Predictor.
type Option func(*Predictor)

// WithModel overrides the default prediction model.
func WithModel(model string) Option {
	return func(p *Predictor) { p.model = model }
}

// New constructs a Predictor. client must be non-nil; the predictor
// shares it with the main agent (the main client's transport, retries,
// auth all transparently apply to side-channel calls too).
func New(client *deepseek.Client, opts ...Option) *Predictor {
	p := &Predictor{
		client: client,
		model:  defaultModel,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Suggest predicts the user's next message given the conversation
// transcript. Returns "" on:
//   - nil predictor (caller can short-circuit safely)
//   - ctx canceled / timed out (5s budget at call site)
//   - API error
//   - empty / nonsensical prediction (post-processed to "")
//
// Never returns an error; the prediction is best-effort UX, never a
// load-bearing decision.
func (p *Predictor) Suggest(ctx context.Context, history []deepseek.Message) string {
	if p == nil || p.client == nil {
		return ""
	}

	// Build the prediction-call message list: side-channel system prompt,
	// then full transcript (with PredictedNext + ReasoningContent stripped
	// by the shared helper so it round-trips cleanly), then the sentinel
	// user message that triggers the prediction.
	prepared := deepseek.StripReasoningContent(history)
	msgs := make([]deepseek.Message, 0, len(prepared)+2)
	msgs = append(msgs, deepseek.Message{
		Role:    deepseek.RoleSystem,
		Content: systemPrompt,
	})
	msgs = append(msgs, prepared...)
	msgs = append(msgs, deepseek.Message{
		Role:    deepseek.RoleUser,
		Content: predictNextSentinel,
	})

	resp, err := p.client.Chat(ctx, &deepseek.ChatRequest{
		Model:     p.model,
		Messages:  msgs,
		MaxTokens: maxPredictionTokens,
	})
	if err != nil {
		// Silent failure — UX hint, not load-bearing.
		return ""
	}
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return cleanPrediction(resp.Choices[0].Message.Content)
}

// cleanPrediction trims, single-lines, and length-caps the raw model
// output. Defends against the cheap Flash model occasionally
// over-explaining ("Sure! I think the user would say: 'A'") or
// returning multi-line junk.
//
// Rules:
//   - Strip whitespace, take first non-empty line
//   - Drop wrapping quotes (single, double, smart quotes)
//   - Reject anything longer than 200 runes (model went off the rails)
//   - Reject the literal sentinel echoed back
//   - Return "" if nothing useful remains
func cleanPrediction(raw string) string {
	if raw == "" {
		return ""
	}
	// First non-empty line.
	var line string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			line = l
			break
		}
	}
	if line == "" {
		return ""
	}
	// Drop wrapping quotes — the model sometimes ignores the
	// "no quotes" instruction.
	line = strings.TrimSpace(line)
	for _, pair := range [][2]string{
		{`"`, `"`},
		{`'`, `'`},
		{"“", "”"},
		{"‘", "’"},
		{"「", "」"},
	} {
		if strings.HasPrefix(line, pair[0]) && strings.HasSuffix(line, pair[1]) && len(line) > len(pair[0])+len(pair[1]) {
			line = strings.TrimSpace(line[len(pair[0]) : len(line)-len(pair[1])])
		}
	}
	if line == "" || line == predictNextSentinel {
		return ""
	}
	if len([]rune(line)) > 200 {
		return ""
	}
	return line
}

// PredictionTimeout is the default ctx budget callers should give
// Suggest. Long enough for a Flash call + minor variance; short
// enough that a user who starts typing within ~5s gets their key
// events serviced without the prediction goroutine still pending.
const PredictionTimeout = 5 * time.Second
