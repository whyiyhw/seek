package askuser

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	auser "github.com/whyiyhw/seek/internal/askuser"
)

// TestExecute_V1SingleQuestion_BackwardCompat is the load-bearing
// pin: v1 schema input must produce v1 result shape (no `answers`
// wrapper). Any future refactor that wraps every result in
// `{answers: [...]}` would break every skill / prompt trained on
// v1. This test catches that.
func TestExecute_V1SingleQuestion_BackwardCompat(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer {
		return auser.Answer{ChosenIDs: []string{"yes"}}
	})

	out, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Proceed?",
		"options": [{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// v1 shape: flat {chosen_ids, ...} — must NOT contain "answers"
	// wrapper key.
	if strings.Contains(out, `"answers"`) {
		t.Errorf("v1 result must NOT wrap in {answers: [...]} — got: %s", out)
	}
	var res result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("v1 result must parse as flat result type: %v\n  output: %s", err, out)
	}
	if len(res.ChosenIDs) != 1 || res.ChosenIDs[0] != "yes" {
		t.Errorf("ChosenIDs = %v, want [yes]", res.ChosenIDs)
	}
}

// TestExecute_V2Batch_TwoQuestions exercises the v2 batch form
// happy path: 2 independent questions, answers come back in
// {answers: [...]} with index alignment preserved.
func TestExecute_V2Batch_TwoQuestions(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskBatchFn(func(b auser.Batch) []auser.Answer {
		if len(b.Questions) != 2 {
			t.Errorf("expected 2 questions, got %d", len(b.Questions))
		}
		return []auser.Answer{
			{ChosenIDs: []string{"react"}},
			{ChosenIDs: []string{"tailwind"}},
		}
	})

	out, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"questions": [
			{
				"question": "Framework?",
				"options": [{"id":"react","label":"React"},{"id":"vue","label":"Vue"}]
			},
			{
				"question": "Styling?",
				"options": [{"id":"tailwind","label":"Tailwind"},{"id":"css","label":"CSS Modules"}]
			}
		]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var res batchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("v2 result must parse as batchResult: %v\n  output: %s", err, out)
	}
	if len(res.Answers) != 2 {
		t.Fatalf("answers len = %d, want 2", len(res.Answers))
	}
	if res.Answers[0].ChosenIDs[0] != "react" {
		t.Errorf("answer[0] = %v, want react", res.Answers[0].ChosenIDs)
	}
	if res.Answers[1].ChosenIDs[0] != "tailwind" {
		t.Errorf("answer[1] = %v, want tailwind", res.Answers[1].ChosenIDs)
	}
}

// TestExecute_V2BatchFive_Rejected pins the schema's maxItems=4
// enforcement at the validation layer (the JSON Schema constraint
// is advisory — Execute re-validates).
func TestExecute_V2BatchFive_Rejected(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskBatchFn(func(_ auser.Batch) []auser.Answer {
		t.Fatal("AskBatch should not be called when validation fails")
		return nil
	})

	// 5 questions — over the limit.
	raw := `{"questions":[` +
		`{"question":"q1","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]},` +
		`{"question":"q2","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]},` +
		`{"question":"q3","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]},` +
		`{"question":"q4","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]},` +
		`{"question":"q5","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}` +
		`]}`
	_, err := New(policy).Execute(context.Background(), json.RawMessage(raw))
	if err == nil {
		t.Fatal("expected error for 5-question batch (max=4)")
	}
	if !strings.Contains(err.Error(), "1-4 questions") {
		t.Errorf("error should mention the 1-4 limit: %v", err)
	}
}

// TestExecute_OptionPreview_FlowsThrough confirms the new
// `preview` field reaches the askuser internal layer. Without
// this, models could send preview content that gets silently
// dropped — a worse failure mode than rejecting unknown fields.
func TestExecute_OptionPreview_FlowsThrough(t *testing.T) {
	var captured auser.Question
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(q auser.Question) auser.Answer {
		captured = q
		return auser.Answer{ChosenIDs: []string{"glass"}}
	})

	_, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Style?",
		"options": [
			{"id":"glass","label":"Glassmorphism","preview":"┌──┐\n│░░│\n└──┘"},
			{"id":"flat","label":"Flat","description":"Plain","preview":"[ flat ]"}
		]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(captured.Options) != 2 {
		t.Fatalf("captured options len = %d, want 2", len(captured.Options))
	}
	if captured.Options[0].Preview != "┌──┐\n│░░│\n└──┘" {
		t.Errorf("option[0].Preview not propagated: %q", captured.Options[0].Preview)
	}
	if captured.Options[1].Preview != "[ flat ]" {
		t.Errorf("option[1].Preview not propagated: %q", captured.Options[1].Preview)
	}
}

// TestExecute_V1HeaderFieldFlowsThrough covers the new v1-form
// `header` field — optional but must reach the internal Question
// when set.
func TestExecute_V1HeaderFieldFlowsThrough(t *testing.T) {
	var captured auser.Question
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(q auser.Question) auser.Answer {
		captured = q
		return auser.Answer{ChosenIDs: []string{"a"}}
	})

	_, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Pick one",
		"header": "Storage",
		"options": [{"id":"a","label":"A"},{"id":"b","label":"B"}]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if captured.Header != "Storage" {
		t.Errorf("Header = %q, want %q", captured.Header, "Storage")
	}
}

// TestExecute_NeitherV1NorV2_Rejected covers the empty-payload
// case: model called the tool with no question / questions at all.
// Should fail in validation with a clear message rather than
// silently no-op or hang on an empty picker.
func TestExecute_NeitherV1NorV2_Rejected(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer {
		t.Fatal("Ask should not be called for an empty payload")
		return auser.Answer{}
	})
	_, err := New(policy).Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for empty payload")
	}
}

// TestExecute_V2BatchValidationPropagatesPerQuestionError pins
// the error-message format from ValidateBatch — "question N:
// <inner err>". Without index pointers the model can't tell
// which question is malformed and just blindly retries.
func TestExecute_V2BatchValidationPropagatesPerQuestionError(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskBatchFn(func(_ auser.Batch) []auser.Answer {
		t.Fatal("AskBatch should not run when validation fails")
		return nil
	})
	// Q1 is valid, Q2 has a duplicate option id.
	raw := `{"questions":[
		{"question":"q1","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]},
		{"question":"q2","options":[{"id":"x","label":"X"},{"id":"x","label":"X2"}]}
	]}`
	_, err := New(policy).Execute(context.Background(), json.RawMessage(raw))
	if err == nil {
		t.Fatal("expected validation error for duplicate option id in Q2")
	}
	if !strings.Contains(err.Error(), "question 1") {
		t.Errorf("error should reference 'question 1' (0-indexed Q2): %v", err)
	}
	if !strings.Contains(err.Error(), "unique") {
		t.Errorf("error should mention uniqueness of option ids: %v", err)
	}
}

// TestExecute_V2BatchVsV1Precedence: when the LLM hedged and
// sent BOTH a top-level `question` and `questions` array, v2
// wins (the explicit array form is the model's stronger
// intent). Documented behavior per Execute's branching logic.
func TestExecute_V2BatchVsV1Precedence(t *testing.T) {
	var batchCalled, singleCalled bool
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer {
		singleCalled = true
		return auser.Answer{}
	})
	policy.SetAskBatchFn(func(b auser.Batch) []auser.Answer {
		batchCalled = true
		ans := make([]auser.Answer, len(b.Questions))
		for i := range ans {
			ans[i] = auser.Answer{ChosenIDs: []string{"x"}}
		}
		return ans
	})

	_, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "ignored when questions is set",
		"options": [{"id":"a","label":"A"},{"id":"b","label":"B"}],
		"questions": [
			{"question":"real","options":[{"id":"x","label":"X"},{"id":"y","label":"Y"}]}
		]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !batchCalled {
		t.Error("batch callback should fire when questions[] is set")
	}
	if singleCalled {
		t.Error("single callback should NOT fire when questions[] is set")
	}
}

// TestExecute_V2BatchConcurrent stresses AskBatch under -race.
// 10 concurrent Execute calls each pushing a 2-question batch
// must aggregate cleanly without state corruption.
func TestExecute_V2BatchConcurrent(t *testing.T) {
	var callCount int
	var mu sync.Mutex
	policy := auser.New(auser.ModeAsk)
	policy.SetAskBatchFn(func(b auser.Batch) []auser.Answer {
		mu.Lock()
		callCount++
		mu.Unlock()
		ans := make([]auser.Answer, len(b.Questions))
		for i := range ans {
			ans[i] = auser.Answer{ChosenIDs: []string{"ok"}}
		}
		return ans
	})

	raw := json.RawMessage(`{
		"questions": [
			{"question":"q1","options":[{"id":"ok","label":"A"},{"id":"b","label":"B"}]},
			{"question":"q2","options":[{"id":"ok","label":"A"},{"id":"b","label":"B"}]}
		]
	}`)

	const N = 10
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for range N {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := New(policy).Execute(context.Background(), raw); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Execute failed: %v", err)
	}
	if callCount != N {
		t.Errorf("callback fired %d times, want %d", callCount, N)
	}
}
