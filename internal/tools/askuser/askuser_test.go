package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	auser "github.com/whyiyhw/seek/internal/askuser"
)

func TestExecute_SingleSelect(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer {
		return auser.Answer{ChosenIDs: []string{"backup"}}
	})

	out, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Overwrite or backup?",
		"options": [
			{"id": "backup", "label": "Backup then overwrite"},
			{"id": "force", "label": "Overwrite without backup"}
		]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.ChosenIDs) != 1 || res.ChosenIDs[0] != "backup" {
		t.Errorf("ChosenIDs=%v, want [backup]", res.ChosenIDs)
	}
	if res.FreeText != "" {
		t.Errorf("FreeText should be empty for structured pick, got %q", res.FreeText)
	}
}

func TestExecute_MultiSelect(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(q auser.Question) auser.Answer {
		if !q.MultiSelect {
			t.Errorf("expected multi_select=true to propagate")
		}
		return auser.Answer{ChosenIDs: []string{"a", "c"}}
	})

	out, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Which features?",
		"multi_select": true,
		"options": [
			{"id": "a", "label": "Feature A"},
			{"id": "b", "label": "Feature B"},
			{"id": "c", "label": "Feature C"}
		]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res result
	_ = json.Unmarshal([]byte(out), &res)
	if len(res.ChosenIDs) != 2 || res.ChosenIDs[0] != "a" || res.ChosenIDs[1] != "c" {
		t.Errorf("ChosenIDs=%v, want [a c]", res.ChosenIDs)
	}
}

func TestExecute_FreeTextAnswer(t *testing.T) {
	// User picked "Other" — TUI returns FreeText instead of ids.
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer {
		return auser.Answer{FreeText: "actually I want something else entirely"}
	})

	out, _ := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Pick",
		"options": [{"id": "a", "label": "a"}, {"id": "b", "label": "b"}]
	}`))
	var res result
	_ = json.Unmarshal([]byte(out), &res)
	if res.FreeText == "" {
		t.Errorf("FreeText should be propagated, got empty")
	}
	if len(res.ChosenIDs) != 0 {
		t.Errorf("ChosenIDs should be empty when FreeText is used, got %v", res.ChosenIDs)
	}
}

func TestExecute_Cancelled(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer { return auser.Answer{Cancelled: true} })

	out, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Pick",
		"options": [{"id": "a", "label": "a"}, {"id": "b", "label": "b"}]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res result
	_ = json.Unmarshal([]byte(out), &res)
	if !res.Cancelled {
		t.Errorf("Cancelled flag should be true in result, got %+v", res)
	}
}

func TestExecute_RejectsInvalidOptionCount(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer { return auser.Answer{} })

	// 1 option — below the min.
	_, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Pick",
		"options": [{"id": "only", "label": "only"}]
	}`))
	if err == nil {
		t.Fatal("expected error for 1 option (min 2)")
	}
}

func TestExecute_RejectsReservedOtherID(t *testing.T) {
	// Model tries to inject an "other" row — must be rejected
	// because the TUI auto-appends its own Other.
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer { return auser.Answer{} })

	_, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Pick",
		"options": [
			{"id": "a", "label": "a"},
			{"id": "other", "label": "my-other"}
		]
	}`))
	if err == nil {
		t.Fatal("expected error when model uses reserved 'other' id")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error should mention reservation, got: %v", err)
	}
}

func TestExecute_DisabledPolicySurfacesError(t *testing.T) {
	// Non-interactive mode (-p print) → policy disabled. The model
	// gets ErrDisabled back and should rephrase as plain text.
	policy := auser.New(auser.ModeDisabled)
	_, err := New(policy).Execute(context.Background(), json.RawMessage(`{
		"question": "Pick",
		"options": [{"id": "a", "label": "a"}, {"id": "b", "label": "b"}]
	}`))
	if err == nil {
		t.Fatal("disabled policy should surface an error")
	}
	if !errors.Is(err, auser.ErrDisabled) {
		t.Errorf("expected wrapped ErrDisabled, got %v", err)
	}
}

func TestExecute_MissingRequiredFields(t *testing.T) {
	policy := auser.New(auser.ModeAsk)
	policy.SetAskFn(func(_ auser.Question) auser.Answer { return auser.Answer{} })

	cases := []string{
		// No question.
		`{"options": [{"id":"a","label":"a"},{"id":"b","label":"b"}]}`,
		// No options.
		`{"question": "x"}`,
	}
	for _, raw := range cases {
		_, err := New(policy).Execute(context.Background(), json.RawMessage(raw))
		if err == nil {
			t.Errorf("expected error on raw=%s", raw)
		}
	}
}
