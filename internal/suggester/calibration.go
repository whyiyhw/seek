package suggester

import (
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// calibrationTemplate is the wire-format text inserted as a system
// message right before the LAST user message in the next-turn prompt
// when the prior turn's prediction missed. Kept stable + literal so
// the model recognises the pattern across turns and so future log
// readers / pitfall investigations can grep for "[calibration]".
//
// PRD docs/prd/feature-suggested-reply.md §4.6.
const calibrationTemplate = "[calibration] Prior turn predicted the user would say: %q. They actually said: %q. Update your model of the user's intent for this turn."

// actualPrefixLen is the prefix of the user's actual message included
// in the calibration note. Long enough to ground the model in the
// real intent, short enough that long pasted prompts don't blow up
// the calibration note size.
const actualPrefixLen = 60

// InjectCalibration inspects msgs for a mispredict signal and, if
// present, returns a copy with a synthetic system message inserted
// right before the LAST user message. If the signal is absent
// (prediction matched, no prediction, or no user message at the end),
// returns msgs unchanged.
//
// Mispredict signal: the most recent prior assistant message has
// non-empty PredictedNext AND the trailing user message fails
// normalizedMatch against that prediction.
//
// This function is the agent's MessagePreparer hook implementation
// for v4 柱 D. It is called on every ChatRequest construction; the
// no-signal path is O(1) (scan backwards from the tail until first
// non-tool message, exit).
//
// The injected system note is NOT persisted — sessions store only
// `predicted_next` on the assistant message, and the calibration
// is reconstructed from there + the user message every time we
// rebuild the ChatRequest. Single source of truth.
func InjectCalibration(msgs []deepseek.Message) []deepseek.Message {
	predicted, actual, ok := findMispredict(msgs)
	if !ok {
		return msgs
	}
	note := deepseek.Message{
		Role:    deepseek.RoleSystem,
		Content: fmt.Sprintf(calibrationTemplate, predicted, truncateRunes(actual, actualPrefixLen)),
	}
	// Insert the note immediately before the last user message — that
	// is, at index len(msgs)-1 (which is the user's position). Builds
	// a new slice so msgs's underlying array isn't mutated.
	out := make([]deepseek.Message, 0, len(msgs)+1)
	out = append(out, msgs[:len(msgs)-1]...)
	out = append(out, note)
	out = append(out, msgs[len(msgs)-1])
	return out
}

// findMispredict walks back from the tail of msgs looking for the
// pattern: <assistant with non-empty PredictedNext> ... <user>, where
// the assistant is the most recent NON-TOOL message before the user.
// Returns (predicted, actual, true) only when a mispredict is detected;
// (_, _, false) otherwise.
//
// "Most recent non-tool assistant" handles the tool-call interleave
// pattern: a turn may end with assistant→tool→tool→user (rare but
// possible in this codebase). We only consult the assistant text turn
// for prediction matching; tool messages don't carry predictions.
func findMispredict(msgs []deepseek.Message) (predicted, actual string, ok bool) {
	if len(msgs) < 2 {
		return "", "", false
	}
	last := msgs[len(msgs)-1]
	if last.Role != deepseek.RoleUser {
		return "", "", false
	}
	// Find the most recent assistant message looking backwards from
	// index len(msgs)-2. Skip tool messages (tool results) and skip
	// assistant messages whose content was just a tool call dispatch
	// (tool-call-only assistants don't end the turn).
	for i := len(msgs) - 2; i >= 0; i-- {
		m := msgs[i]
		if m.Role == deepseek.RoleTool {
			continue
		}
		if m.Role != deepseek.RoleAssistant {
			// Hit a user / system message before finding an assistant
			// — no prediction to compare against (prior turn was the
			// session start or something equally cold).
			return "", "", false
		}
		// First assistant we hit. If it has a prediction AND it
		// didn't match the user's actual message, that's our signal.
		if m.PredictedNext == "" {
			return "", "", false
		}
		if normalizedMatch(last.Content, m.PredictedNext) {
			return "", "", false
		}
		return m.PredictedNext, last.Content, true
	}
	return "", "", false
}

// truncateRunes returns s truncated to at most n runes. Rune-aware
// so a CJK truncation lands on a character boundary, not in the
// middle of a UTF-8 sequence. Adds "…" suffix when truncated.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimSpace(string(runes[:n])) + "…"
}
