package askuser

import (
	"strings"
	"testing"
)

// TestValidateBatch_AcceptsRange covers the 1..4 question bound.
func TestValidateBatch_AcceptsRange(t *testing.T) {
	mkOpts := func() []Option {
		return []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}
	}
	for n := 1; n <= 4; n++ {
		qs := make([]Question, n)
		for i := range qs {
			qs[i] = Question{Question: "q", Options: mkOpts()}
		}
		if err := ValidateBatch(Batch{Questions: qs}); err != nil {
			t.Errorf("n=%d: ValidateBatch rejected valid batch: %v", n, err)
		}
	}
}

func TestValidateBatch_RejectsZero(t *testing.T) {
	if err := ValidateBatch(Batch{}); err == nil {
		t.Fatal("empty batch must be rejected")
	} else if !strings.Contains(err.Error(), "1-4") {
		t.Errorf("error should mention 1-4 range: %v", err)
	}
}

func TestValidateBatch_RejectsOverFour(t *testing.T) {
	qs := make([]Question, 5)
	for i := range qs {
		qs[i] = Question{Question: "q", Options: []Option{
			{ID: "a", Label: "A"}, {ID: "b", Label: "B"},
		}}
	}
	if err := ValidateBatch(Batch{Questions: qs}); err == nil {
		t.Fatal("5-question batch must be rejected")
	}
}

func TestValidateBatch_PropagatesPerQuestionErrorsWithIndex(t *testing.T) {
	qs := []Question{
		{Question: "q1", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
		{Question: "", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}}, // empty
	}
	err := ValidateBatch(Batch{Questions: qs})
	if err == nil {
		t.Fatal("expected error from empty question text in Q2")
	}
	if !strings.Contains(err.Error(), "question 1") {
		t.Errorf("error must reference 'question 1' (0-indexed Q2): %v", err)
	}
}

// TestAskBatch_UsesBatchCallbackWhenSet is the v2 happy path:
// when both v1 and v2 callbacks exist, the batch callback wins.
func TestAskBatch_UsesBatchCallbackWhenSet(t *testing.T) {
	p := New(ModeAsk)
	p.SetAskFn(func(_ Question) Answer {
		t.Fatal("v1 callback should NOT fire when v2 callback is set")
		return Answer{}
	})
	p.SetAskBatchFn(func(b Batch) []Answer {
		ans := make([]Answer, len(b.Questions))
		for i := range ans {
			ans[i] = Answer{ChosenIDs: []string{"x"}}
		}
		return ans
	})

	b := Batch{Questions: []Question{
		{Question: "q", Options: []Option{{ID: "x", Label: "X"}, {ID: "y", Label: "Y"}}},
	}}
	answers, err := p.AskBatch(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 1 || answers[0].ChosenIDs[0] != "x" {
		t.Errorf("answers = %v, want [{ChosenIDs: [x]}]", answers)
	}
}

// TestAskBatch_FallsBackToV1LoopWhenNoBatchCallback is the
// load-bearing backward-compat pin: any caller that wired only
// SetAskFn (e.g. propose tool) still gets functional multi-
// question behavior — the v1 callback fires once per question
// in sequence.
func TestAskBatch_FallsBackToV1LoopWhenNoBatchCallback(t *testing.T) {
	var v1Calls int
	p := New(ModeAsk)
	p.SetAskFn(func(q Question) Answer {
		v1Calls++
		return Answer{ChosenIDs: []string{q.Options[0].ID}}
	})
	// NB: no SetAskBatchFn

	b := Batch{Questions: []Question{
		{Question: "q1", Options: []Option{{ID: "a1", Label: "A"}, {ID: "b1", Label: "B"}}},
		{Question: "q2", Options: []Option{{ID: "a2", Label: "A"}, {ID: "b2", Label: "B"}}},
	}}
	answers, err := p.AskBatch(b)
	if err != nil {
		t.Fatal(err)
	}
	if v1Calls != 2 {
		t.Errorf("v1 callback fired %d times, want 2 (one per question)", v1Calls)
	}
	if len(answers) != 2 || answers[0].ChosenIDs[0] != "a1" || answers[1].ChosenIDs[0] != "a2" {
		t.Errorf("answers = %v, want [{a1}, {a2}]", answers)
	}
}

// TestAskBatch_MidBatchCancelStopsLoop is the cancel-semantic
// pin: when Q_i returns Cancelled, Q_(i+1..N) are NOT asked and
// get Answer{Cancelled: true} placeholders. Prevents the v1-
// fallback path from accidentally asking every question even
// after the user gave up.
func TestAskBatch_MidBatchCancelStopsLoop(t *testing.T) {
	var asked []string
	p := New(ModeAsk)
	p.SetAskFn(func(q Question) Answer {
		asked = append(asked, q.Question)
		if q.Question == "q2" {
			return Answer{Cancelled: true}
		}
		return Answer{ChosenIDs: []string{q.Options[0].ID}}
	})

	b := Batch{Questions: []Question{
		{Question: "q1", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
		{Question: "q2", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
		{Question: "q3", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
	}}
	answers, err := p.AskBatch(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[1] != "q2" {
		t.Errorf("asked sequence = %v, want [q1, q2] (q3 must not be asked after q2 cancelled)", asked)
	}
	if !answers[2].Cancelled {
		t.Error("answer[2] should be Cancelled when q3 was skipped")
	}
	if answers[0].Cancelled {
		t.Error("answer[0] should keep q1's actual answer, not Cancelled")
	}
}

// TestAskBatch_DefensivePadOnShortCallbackReturn covers a TUI
// bug class: the batch callback returns fewer answers than
// questions (off-by-one in render code etc.). AskBatch pads
// with Cancelled placeholders rather than letting the
// downstream tool layer index-out-of-range.
func TestAskBatch_DefensivePadOnShortCallbackReturn(t *testing.T) {
	p := New(ModeAsk)
	p.SetAskBatchFn(func(_ Batch) []Answer {
		// Bug: returned only 1 answer for a 3-question batch.
		return []Answer{{ChosenIDs: []string{"x"}}}
	})

	b := Batch{Questions: []Question{
		{Question: "q1", Options: []Option{{ID: "x", Label: "X"}, {ID: "y", Label: "Y"}}},
		{Question: "q2", Options: []Option{{ID: "x", Label: "X"}, {ID: "y", Label: "Y"}}},
		{Question: "q3", Options: []Option{{ID: "x", Label: "X"}, {ID: "y", Label: "Y"}}},
	}}
	answers, err := p.AskBatch(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 3 {
		t.Errorf("AskBatch must pad to len(Questions)=3, got %d", len(answers))
	}
	if !answers[1].Cancelled || !answers[2].Cancelled {
		t.Error("padded entries must be Cancelled=true")
	}
}

// TestAskBatch_DefensiveTruncateOnLongCallbackReturn: callback
// returns MORE answers than questions — defensive truncation
// keeps the index alignment intact.
func TestAskBatch_DefensiveTruncateOnLongCallbackReturn(t *testing.T) {
	p := New(ModeAsk)
	p.SetAskBatchFn(func(_ Batch) []Answer {
		return []Answer{
			{ChosenIDs: []string{"a"}},
			{ChosenIDs: []string{"b"}},
			{ChosenIDs: []string{"c"}}, // extra
		}
	})
	b := Batch{Questions: []Question{
		{Question: "q1", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
		{Question: "q2", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
	}}
	answers, err := p.AskBatch(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(answers) != 2 {
		t.Errorf("AskBatch must truncate to len(Questions)=2, got %d", len(answers))
	}
}

func TestAskBatch_DisabledMode(t *testing.T) {
	p := New(ModeDisabled)
	p.SetAskBatchFn(func(_ Batch) []Answer { t.Fatal("must not call"); return nil })
	_, err := p.AskBatch(Batch{Questions: []Question{
		{Question: "q", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
	}})
	if err == nil {
		t.Fatal("disabled policy must reject AskBatch")
	}
}

func TestAskBatch_EmptyBatchRejected(t *testing.T) {
	p := New(ModeAsk)
	p.SetAskBatchFn(func(_ Batch) []Answer { return nil })
	_, err := p.AskBatch(Batch{})
	if err == nil {
		t.Fatal("empty batch must be rejected by AskBatch")
	}
}

// TestAskBatch_NoCallbackRegistered: neither v1 nor v2 callback
// set. Must error rather than nil-deref.
func TestAskBatch_NoCallbackRegistered(t *testing.T) {
	p := New(ModeAsk)
	_, err := p.AskBatch(Batch{Questions: []Question{
		{Question: "q", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}},
	}})
	if err == nil {
		t.Fatal("AskBatch with no registered callback must error")
	}
}
