package askuser

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidate_AcceptsValidQuestion(t *testing.T) {
	q := Question{
		Question: "Pick one",
		Options: []Option{
			{ID: "a", Label: "Option A"},
			{ID: "b", Label: "Option B"},
		},
	}
	if err := Validate(q); err != nil {
		t.Errorf("valid question rejected: %v", err)
	}
}

func TestValidate_RejectsEmptyQuestion(t *testing.T) {
	q := Question{Question: "", Options: []Option{{ID: "a", Label: "x"}, {ID: "b", Label: "y"}}}
	if err := Validate(q); err == nil {
		t.Fatal("empty question text should be rejected")
	}
}

func TestValidate_RejectsTooFewOptions(t *testing.T) {
	q := Question{Question: "x", Options: []Option{{ID: "only", Label: "only"}}}
	err := Validate(q)
	if err == nil {
		t.Fatal("1 option should be rejected (min 2)")
	}
	if !strings.Contains(err.Error(), "2-4") {
		t.Errorf("error should state the range, got: %v", err)
	}
}

func TestValidate_RejectsTooManyOptions(t *testing.T) {
	q := Question{Question: "x", Options: make([]Option, 5)}
	for i := range q.Options {
		q.Options[i] = Option{ID: string(rune('a' + i)), Label: "x"}
	}
	if err := Validate(q); err == nil {
		t.Fatal("5 options should be rejected (max 4)")
	}
}

func TestValidate_RejectsDuplicateIDs(t *testing.T) {
	q := Question{
		Question: "x",
		Options: []Option{
			{ID: "dup", Label: "a"},
			{ID: "dup", Label: "b"},
		},
	}
	err := Validate(q)
	if err == nil {
		t.Fatal("duplicate ids must be rejected")
	}
	if !strings.Contains(err.Error(), "unique") {
		t.Errorf("error should explain uniqueness, got: %v", err)
	}
}

func TestValidate_RejectsReservedOtherID(t *testing.T) {
	// The TUI auto-appends an "Other" row, so a model trying to
	// add its own "other" would silently collide. The validation
	// catches it with a clear message.
	q := Question{
		Question: "x",
		Options: []Option{
			{ID: "other", Label: "mine"},
			{ID: "a", Label: "a"},
		},
	}
	err := Validate(q)
	if err == nil {
		t.Fatal("reserved id 'other' must be rejected")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error should mention 'reserved', got: %v", err)
	}
}

func TestValidate_RejectsMissingLabel(t *testing.T) {
	q := Question{
		Question: "x",
		Options: []Option{
			{ID: "a", Label: ""},
			{ID: "b", Label: "b"},
		},
	}
	if err := Validate(q); err == nil {
		t.Fatal("missing label must be rejected — picker rows can't render with no text")
	}
}

func TestPolicy_AskRoundtrip(t *testing.T) {
	p := New(ModeAsk)
	got := make(chan Question, 1)
	p.SetAskFn(func(q Question) Answer {
		got <- q
		return Answer{ChosenIDs: []string{"a"}}
	})

	q := Question{
		Question: "Pick",
		Options:  []Option{{ID: "a", Label: "a"}, {ID: "b", Label: "b"}},
	}
	ans, err := p.Ask(q)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(ans.ChosenIDs) != 1 || ans.ChosenIDs[0] != "a" {
		t.Errorf("expected ChosenIDs=[a], got %v", ans.ChosenIDs)
	}
	// Callback received the same question.
	select {
	case received := <-got:
		if received.Question != "Pick" {
			t.Errorf("callback got question %q, want 'Pick'", received.Question)
		}
	case <-time.After(time.Second):
		t.Fatal("callback never fired")
	}
}

func TestPolicy_DisabledMode(t *testing.T) {
	p := New(ModeDisabled)
	_, err := p.Ask(Question{Question: "x", Options: []Option{{ID: "a", Label: "a"}, {ID: "b", Label: "b"}}})
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("expected ErrDisabled, got %v", err)
	}
}

func TestPolicy_NoCallbackRegistered(t *testing.T) {
	// Programming error case: tool calls Ask but main forgot to
	// SetAskFn. Surface a clear error rather than panic.
	p := New(ModeAsk)
	_, err := p.Ask(Question{Question: "x", Options: []Option{{ID: "a", Label: "a"}, {ID: "b", Label: "b"}}})
	if !errors.Is(err, ErrNoCallback) {
		t.Errorf("expected ErrNoCallback, got %v", err)
	}
}

func TestPolicy_ConcurrentAskAndSetMode(t *testing.T) {
	// Regression guard: SetMode mutates the same fields Ask reads.
	// The mutex serialises them; -race must come back clean.
	p := New(ModeAsk)
	p.SetAskFn(func(_ Question) Answer { return Answer{ChosenIDs: []string{"a"}} })

	var wg sync.WaitGroup
	q := Question{Question: "x", Options: []Option{{ID: "a", Label: "a"}, {ID: "b", Label: "b"}}}
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = p.Ask(q)
		}()
		go func() {
			defer wg.Done()
			p.SetMode(ModeAsk) // bounces back and forth from the same value, but exercises the lock
		}()
	}
	wg.Wait()
}
