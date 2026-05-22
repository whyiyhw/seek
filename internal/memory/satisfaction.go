package memory

import (
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// SatisfactionSignal summarises the heuristic verdict for "is this
// session worth auto-distilling without user review". A positive Score
// (above SatisfactionThreshold) means M5.7's auto-distill path is
// allowed to fire — the conversation looks productive enough that
// hallucinated entries are unlikely.
//
// The score is intentionally conservative: PRD §3 originally barred
// auto-distill specifically because of hallucination risk. The signal
// must err on the side of "stay quiet" — false negatives (manual
// /distill still works) cost nothing; false positives pollute M.
type SatisfactionSignal struct {
	Score        float64 // 0.0–1.0
	UserTurns    int     // count of user messages (excluding system)
	HasRejection bool    // any recent user message reads as rejection
	ToolErrors   int     // count of "tool error:" in tool-role messages
}

// SatisfactionThreshold is the score above which AutoDistill fires.
// 0.7 — picked deliberately high; tuning below 0.7 would need real
// usage data to justify.
const SatisfactionThreshold = 0.7

// minSatisfactionTurns is the conversation-length floor. Sessions
// shorter than this won't auto-distill regardless of how clean they
// look — they don't have enough decisions to extract.
const minSatisfactionTurns = 4

// satisfactionRecentWindow is how many of the most-recent user turns
// we check for rejection keywords. We don't scan the whole session
// because early-session "I want X" rejections are usually superseded
// by later decisions — what matters is "did the user leave satisfied".
const satisfactionRecentWindow = 5

// rejectionTokens are case-insensitive substrings that, when present in
// a recent user message, indicate dissatisfaction or correction. The
// list errs on the side of "many false negatives" — we'd rather skip
// auto-distill on a happy session that happened to include "stop" in
// some unrelated context than auto-distill on a session where the
// user actually said "stop, that's wrong".
//
// Bilingual on purpose — seek's users work in CJK as much as English.
var rejectionTokens = []string{
	// English signals
	"that's wrong",
	"thats wrong",
	"you're wrong",
	"youre wrong",
	"undo that",
	"revert",
	"don't do that",
	"dont do that",
	"stop",
	"no, ",
	"no.",
	"actually no",
	"that didn't work",
	"that didnt work",
	"that broke",
	"not what i wanted",
	"not what i meant",
	"try again",
	"redo",
	"misunderstood",
	"backtrack",
	// CJK signals
	"不对", "错了", "撤销", "回退", "不要这样",
	"不是这个意思", "重新", "重做", "再试", "搞错了",
	"反了", "停",
}

// ScoreSatisfaction computes a SatisfactionSignal for the given
// conversation history. Pure function — no I/O, no clock — so it's
// trivially testable and deterministic.
//
// Algorithm (M5.7 v1 — intentionally simple, tune-later):
//
//  1. Count user-role messages (skip system).
//  2. If under minSatisfactionTurns → Score=0 (too short).
//  3. Scan last satisfactionRecentWindow user messages for rejection
//     tokens → HasRejection.
//  4. Count "tool error:" occurrences across tool-role messages.
//  5. Compose:
//     base = 1.0
//     if HasRejection         → base *= 0.0  (hard veto)
//     per tool error          → base -= 0.15 (capped at 0)
//     small bonus for length  → base += min(0.1, (turns-min)/20)
//     clamp to [0, 1]
//
// A session must hit both "long enough" and "no rejection" to clear
// the threshold; tool errors gnaw at an otherwise-clean session
// linearly. This is a heuristic, not a model — it'll need real-usage
// telemetry to refine.
func ScoreSatisfaction(history []deepseek.Message) SatisfactionSignal {
	var sig SatisfactionSignal

	var userMsgs []string
	for _, m := range history {
		switch m.Role {
		case deepseek.RoleUser:
			sig.UserTurns++
			userMsgs = append(userMsgs, m.Content)
		case deepseek.RoleTool:
			if strings.Contains(m.Content, "tool error:") {
				sig.ToolErrors++
			}
		}
	}

	if sig.UserTurns < minSatisfactionTurns {
		return sig // Score stays at zero — too short.
	}

	// Rejection scan: last N user turns only.
	tail := userMsgs
	if len(tail) > satisfactionRecentWindow {
		tail = tail[len(tail)-satisfactionRecentWindow:]
	}
	for _, msg := range tail {
		lower := strings.ToLower(msg)
		for _, tok := range rejectionTokens {
			if strings.Contains(lower, tok) {
				sig.HasRejection = true
				break
			}
		}
		if sig.HasRejection {
			break
		}
	}

	if sig.HasRejection {
		sig.Score = 0 // hard veto
		return sig
	}

	base := 1.0
	base -= 0.15 * float64(sig.ToolErrors)
	bonus := float64(sig.UserTurns-minSatisfactionTurns) / 20.0
	if bonus > 0.1 {
		bonus = 0.1
	}
	if bonus < 0 {
		bonus = 0
	}
	base += bonus
	if base < 0 {
		base = 0
	}
	if base > 1 {
		base = 1
	}
	sig.Score = base
	return sig
}

// IsSatisfied is a convenience that returns whether the signal clears
// SatisfactionThreshold. Callers can read the full signal for
// telemetry / debug; the auto-distill hook only needs the boolean.
func IsSatisfied(s SatisfactionSignal) bool {
	return s.Score >= SatisfactionThreshold
}
