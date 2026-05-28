package suggester

import (
	"regexp"
	"strings"
)

// tailScanRunes is how far back from the end of the assistant message
// ShouldPredict looks for "invite a response" signals. 300 runes
// covers a typical final paragraph without dragging in earlier
// paragraphs that aren't representative of the turn's closing intent.
const tailScanRunes = 300

// questionRe catches the most reliable "I'm asking the user something"
// marker: a literal `?` (ASCII or Chinese) anywhere in the tail.
// Generous on purpose — most prompts that genuinely invite a response
// end with one of these, and the cost of a false positive is just an
// extra Flash-tier prediction call that cleanPrediction may discard.
var questionRe = regexp.MustCompile(`[?？]`)

// multiChoiceRe catches `[A]` / `[B]` / `1)` / `2)` / `Option A` /
// `选项 A` style enumerations. Any of these patterns in the tail
// implies the assistant is offering picks and a Tab-completable
// next user message ("用 A" / "B 方案") is plausible.
var multiChoiceRe = regexp.MustCompile(`\[[A-Da-d]\]|\b[A-D]\)|\bOption\s+[A-D]\b|选项\s*[ABCD]`)

// intentPhrases trigger ShouldPredict when found (case-insensitive,
// substring) in the tail. Curated for typical "委婉询问 / shall I"
// phrasings. English + Chinese coverage roughly matches seek's user
// base; extend rather than fork when new languages land.
var intentPhrases = []string{
	"shall i",
	"should i",
	"would you",
	"do you want",
	"want me to",
	"let me know",
	"which would",
	"please confirm",
	"请告诉我",
	"需要我",
	"要不要",
	"你想",
	"希望我",
	"你需要",
}

// ShouldPredict reports whether the assistant turn that just ended
// looks like it invites a user response — i.e. is worth spending a
// side-channel Flash call to predict the next message.
//
// Returns false on turns that read as definitive endings ("done",
// "all tests pass", a final tool result with no follow-up question),
// so the TUI doesn't display a placeholder the model basically
// invented from nothing.
//
// Cheap heuristic — runs without an LLM call so it can gate the
// predictor's API invocation entirely. False positives are tolerated
// (we'll just fire a prediction that cleanPrediction may drop as
// generic noise); false negatives are graceful (no placeholder, same
// outcome as before this gate existed).
//
// PRD docs/prd/feature-suggested-reply.md §3 (trigger conditions) +
// follow-up dogfood observation: "predicting after every turn shows
// placeholders on obviously-finished conversations" — see commit
// log for the threshold-design discussion.
func ShouldPredict(lastAssistantContent string) bool {
	if lastAssistantContent == "" {
		return false
	}
	tail := tailRunes(lastAssistantContent, tailScanRunes)
	if questionRe.MatchString(tail) {
		return true
	}
	if multiChoiceRe.MatchString(tail) {
		return true
	}
	lower := strings.ToLower(tail)
	for _, p := range intentPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// tailRunes returns the last n runes of s. UTF-8-safe — byte-based
// s[len(s)-n:] would slice mid-codepoint on CJK input.
func tailRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}
