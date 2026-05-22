package memory

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// build helper that synthesises a session history. roles alternates
// user/assistant unless explicitly tagged.
func session(turns ...string) []deepseek.Message {
	out := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
	}
	for i, t := range turns {
		role := deepseek.RoleUser
		if i%2 == 1 {
			role = deepseek.RoleAssistant
		}
		out = append(out, deepseek.Message{Role: role, Content: t})
	}
	return out
}

func TestScoreSatisfaction_TooShortIsZero(t *testing.T) {
	// Just 2 user turns — below minSatisfactionTurns (4).
	s := ScoreSatisfaction(session("hi", "hello", "do X", "done"))
	if s.UserTurns != 2 {
		t.Fatalf("UserTurns = %d, want 2", s.UserTurns)
	}
	if s.Score != 0 {
		t.Errorf("Score = %v, want 0 for too-short session", s.Score)
	}
	if IsSatisfied(s) {
		t.Errorf("too-short session should not be satisfied")
	}
}

func TestScoreSatisfaction_HappySessionClearsThreshold(t *testing.T) {
	// 4 user turns + 4 assistant, no rejection signals.
	msgs := session(
		"i need to refactor X", "OK here's a plan",
		"go ahead", "done step 1",
		"now step 2", "done step 2",
		"thanks", "you're welcome",
	)
	s := ScoreSatisfaction(msgs)
	if s.UserTurns != 4 {
		t.Fatalf("UserTurns = %d, want 4", s.UserTurns)
	}
	if s.HasRejection {
		t.Errorf("should NOT detect rejection in happy session")
	}
	if !IsSatisfied(s) {
		t.Errorf("happy 4-turn session should clear threshold; score=%v", s.Score)
	}
}

func TestScoreSatisfaction_RejectionVetoesScore(t *testing.T) {
	msgs := session(
		"refactor this", "done",
		"that's wrong, please redo", "OK",
		"better now?", "yes",
		"thanks", "you're welcome",
	)
	s := ScoreSatisfaction(msgs)
	if !s.HasRejection {
		t.Errorf("rejection token should be detected")
	}
	if s.Score != 0 {
		t.Errorf("rejection should hard-veto score to 0; got %v", s.Score)
	}
}

func TestScoreSatisfaction_CJKRejectionDetected(t *testing.T) {
	msgs := session(
		"重构这个", "好的",
		"不对，撤销", "好",
		"再试一次", "OK",
		"现在对了吗", "对",
	)
	s := ScoreSatisfaction(msgs)
	if !s.HasRejection {
		t.Errorf("CJK rejection should be detected; got %+v", s)
	}
}

func TestScoreSatisfaction_OldRejectionOutsideWindowIgnored(t *testing.T) {
	// Rejection in turn 1, then 6 clean turns. Recent-window scan
	// (last 5) doesn't see the rejection, so the session reads as
	// resolved.
	msgs := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
		{Role: deepseek.RoleUser, Content: "that's wrong from the start"},
		{Role: deepseek.RoleAssistant, Content: "fixing"},
		{Role: deepseek.RoleUser, Content: "now do X"},
		{Role: deepseek.RoleAssistant, Content: "done"},
		{Role: deepseek.RoleUser, Content: "do Y"},
		{Role: deepseek.RoleAssistant, Content: "done"},
		{Role: deepseek.RoleUser, Content: "do Z"},
		{Role: deepseek.RoleAssistant, Content: "done"},
		{Role: deepseek.RoleUser, Content: "all good"},
		{Role: deepseek.RoleAssistant, Content: "great"},
		{Role: deepseek.RoleUser, Content: "wrap up please"},
		{Role: deepseek.RoleAssistant, Content: "wrapped"},
	}
	s := ScoreSatisfaction(msgs)
	if s.HasRejection {
		t.Errorf("rejection outside the recent window should NOT count; got %+v", s)
	}
	if !IsSatisfied(s) {
		t.Errorf("a long clean tail should clear threshold even with early rejection; got %v", s.Score)
	}
}

func TestScoreSatisfaction_ToolErrorsErodeScore(t *testing.T) {
	// 4 user turns + several tool errors. Each tool error subtracts
	// 0.15 — 3 errors → ~0.55 score, below threshold (0.7).
	msgs := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
		{Role: deepseek.RoleUser, Content: "do X"},
		{Role: deepseek.RoleAssistant, Content: "trying"},
		{Role: deepseek.RoleTool, Content: "tool error: permission denied"},
		{Role: deepseek.RoleUser, Content: "try again"},
		{Role: deepseek.RoleAssistant, Content: "retrying"},
		{Role: deepseek.RoleTool, Content: "tool error: not found"},
		{Role: deepseek.RoleUser, Content: "and now?"},
		{Role: deepseek.RoleAssistant, Content: "trying"},
		{Role: deepseek.RoleTool, Content: "tool error: out of space"},
		{Role: deepseek.RoleUser, Content: "OK whatever"},
		{Role: deepseek.RoleAssistant, Content: "done"},
	}
	s := ScoreSatisfaction(msgs)
	if s.ToolErrors != 3 {
		t.Fatalf("ToolErrors = %d, want 3", s.ToolErrors)
	}
	// 1.0 - 3*0.15 = 0.55, even without the bonus. Below 0.7 threshold.
	if IsSatisfied(s) {
		t.Errorf("session with 3 tool errors should NOT be satisfied; score=%v", s.Score)
	}
}

func TestScoreSatisfaction_BonusForLongerSessions(t *testing.T) {
	// Both clean sessions clamp to 1.0 due to the base=1.0 + bonus cap.
	// The bonus exists to let tool-error-eroded sessions recover some
	// ground via length. Assert >= so the relationship holds even at
	// the clamp ceiling.
	turns := make([]string, 24)
	for i := range turns {
		turns[i] = "msg " + strings.Repeat("x", 4)
	}
	short := ScoreSatisfaction(session(turns[:8]...))
	long := ScoreSatisfaction(session(turns...))
	if long.Score < short.Score {
		t.Errorf("longer session should not score lower: short=%v long=%v",
			short.Score, long.Score)
	}
}

func TestScoreSatisfaction_BoundedAt1(t *testing.T) {
	// 100 clean turns shouldn't push the score above 1.0.
	turns := make([]string, 100)
	for i := range turns {
		turns[i] = "fine"
	}
	s := ScoreSatisfaction(session(turns...))
	if s.Score > 1.0 {
		t.Errorf("score must be ≤ 1.0, got %v", s.Score)
	}
}

func TestScoreSatisfaction_HardVetoBeforeBonus(t *testing.T) {
	// Build directly so the final rejection lands on a user role
	// regardless of helper alternation parity.
	msgs := []deepseek.Message{{Role: deepseek.RoleSystem, Content: "sys"}}
	for i := 0; i < 50; i++ {
		msgs = append(msgs,
			deepseek.Message{Role: deepseek.RoleUser, Content: "do something"},
			deepseek.Message{Role: deepseek.RoleAssistant, Content: "done"},
		)
	}
	msgs = append(msgs, deepseek.Message{Role: deepseek.RoleUser, Content: "stop, that's wrong"})

	s := ScoreSatisfaction(msgs)
	if !s.HasRejection {
		t.Fatalf("rejection in final user turn not detected: %+v", s)
	}
	if s.Score != 0 {
		t.Errorf("rejection should veto even a long session; got %v", s.Score)
	}
}
