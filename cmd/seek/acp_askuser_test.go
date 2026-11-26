package main

// Unit tests for the ACP ask lane's pure halves (acp_askuser.go): the
// form builder and the answer mapper. The transport round-trip is
// covered end to end in acp_askuser_e2e_test.go; here we pin the
// mapping decisions that would otherwise be invisible — which answer a
// given form reply becomes, and what the emitted schema looks like.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/askuser"
)

func singleChoiceQuestion() askuser.Question {
	return askuser.Question{
		Question: "Which scope?",
		Options: []askuser.Option{
			{ID: "user", Label: "user", Description: "private"},
			{ID: "project", Label: "project"},
		},
	}
}

func multiChoiceQuestion() askuser.Question {
	return askuser.Question{
		Question:    "Which features?",
		MultiSelect: true,
		Options: []askuser.Option{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Beta"},
			{ID: "c", Label: "Gamma"},
		},
	}
}

// TestBuildElicitationParams_SingleChoice pins the single-select shape:
// one `string` property using oneOf (so the client renders labels, not
// ids) plus the free-text twin, and NO required list (the twin is an
// alternative, so requiring either would make the other unanswerable).
func TestBuildElicitationParams_SingleChoice(t *testing.T) {
	b := askuser.Batch{Questions: []askuser.Question{singleChoiceQuestion()}}
	p := buildElicitationParams("sess-1", 1, b)

	if p.SessionID != "sess-1" || p.Mode != acp.ElicitationModeForm {
		t.Fatalf("params = %+v, want session sess-1 / mode form", p)
	}
	if p.RequestedSchema.Type != "object" {
		t.Errorf("schema type = %q, want object", p.RequestedSchema.Type)
	}
	if len(p.RequestedSchema.Required) != 0 {
		t.Errorf("required = %v, want empty (free text is the alternative)", p.RequestedSchema.Required)
	}

	q1, ok := p.RequestedSchema.Properties["q1"]
	if !ok {
		t.Fatalf("missing q1; props = %v", keysOf(p.RequestedSchema.Properties))
	}
	if q1.Type != "string" {
		t.Errorf("q1.type = %q, want string", q1.Type)
	}
	if len(q1.OneOf) != 2 || q1.OneOf[0].Const != "user" || q1.OneOf[0].Title != "user" {
		t.Errorf("q1.oneOf = %+v, want the two options as consts", q1.OneOf)
	}
	if q1.OneOf[0].Description != "private" {
		t.Errorf("q1.oneOf[0].description = %q, want private (the option description)", q1.OneOf[0].Description)
	}
	if q1.Enum != nil {
		t.Errorf("q1.enum = %v, want nil (oneOf is the titled form)", q1.Enum)
	}

	other, ok := p.RequestedSchema.Properties["q1_other"]
	if !ok {
		t.Fatalf("missing q1_other; props = %v", keysOf(p.RequestedSchema.Properties))
	}
	if other.Type != "string" || other.Title == "" {
		t.Errorf("q1_other = %+v, want a titled string field", other)
	}
}

// TestBuildElicitationParams_MultiChoice pins the array shape: enum of
// ids on items, bounded by minItems/maxItems, and the option labels
// folded into the description (an array property has no per-item title).
func TestBuildElicitationParams_MultiChoice(t *testing.T) {
	b := askuser.Batch{Questions: []askuser.Question{multiChoiceQuestion()}}
	p := buildElicitationParams("sess-1", 1, b)

	q1 := p.RequestedSchema.Properties["q1"]
	if q1.Type != "array" {
		t.Fatalf("q1.type = %q, want array", q1.Type)
	}
	if q1.OneOf != nil {
		t.Errorf("q1.oneOf = %v, want nil for a multi-select", q1.OneOf)
	}
	if q1.Items == nil || q1.Items.Type != "string" || len(q1.Items.Enum) != 3 {
		t.Fatalf("q1.items = %+v, want string enum of 3", q1.Items)
	}
	if q1.MinItems != 1 || q1.MaxItems != 3 {
		t.Errorf("bounds = %d..%d, want 1..3", q1.MinItems, q1.MaxItems)
	}
	if q1.Description == "" {
		t.Error("array field needs a description carrying the option labels; ids alone are unreadable")
	}
}

// TestBuildElicitationParams_Batch pins the batch → one-form mapping:
// every question gets its own q<i> pair, indices are 1-based, and the
// header (when present) is folded into the field title.
func TestBuildElicitationParams_Batch(t *testing.T) {
	q2 := singleChoiceQuestion()
	q2.Header = "Storage"
	b := askuser.Batch{Questions: []askuser.Question{singleChoiceQuestion(), q2}}
	p := buildElicitationParams("sess-1", 1, b)

	for _, k := range []string{"q1", "q1_other", "q2", "q2_other"} {
		if _, ok := p.RequestedSchema.Properties[k]; !ok {
			t.Errorf("missing %s; props = %v", k, keysOf(p.RequestedSchema.Properties))
		}
	}
	if len(p.RequestedSchema.Properties) != 4 {
		t.Errorf("props = %v, want exactly 4 (2 questions × 2 fields)", keysOf(p.RequestedSchema.Properties))
	}
	if title := p.RequestedSchema.Properties["q2"].Title; title != "Storage — Which scope?" {
		t.Errorf("q2 title = %q, want the header prefix", title)
	}
}

// TestAnswersFromElicitation maps every reply shape onto the
// per-question Answer contract. The rows that matter:
//   - select → ChosenIDs
//   - free text → FreeText
//   - both → both (the client can send them together)
//   - untouched field → Cancelled for THAT question only
//   - declined / cancelled / malformed → every question Cancelled
//   - an id outside the offered set → dropped, not invented
func TestAnswersFromElicitation(t *testing.T) {
	b := askuser.Batch{Questions: []askuser.Question{singleChoiceQuestion(), multiChoiceQuestion()}}

	cases := []struct {
		name string
		raw  string
		want []askuser.Answer
	}{
		{
			name: "select + multi-select",
			raw:  `{"action":"accept","content":{"q1":"project","q2":["a","c"]}}`,
			want: []askuser.Answer{
				{ChosenIDs: []string{"project"}},
				{ChosenIDs: []string{"a", "c"}},
			},
		},
		{
			name: "free text only",
			raw:  `{"action":"accept","content":{"q1_other":"  something else  "}}`,
			want: []askuser.Answer{
				{FreeText: "something else"},
				{Cancelled: true},
			},
		},
		{
			name: "free text wins over a simultaneous pick (TUI exclusivity)",
			raw:  `{"action":"accept","content":{"q1":"user","q1_other":"but also this","q2":["b"]}}`,
			want: []askuser.Answer{
				{FreeText: "but also this"},
				{ChosenIDs: []string{"b"}},
			},
		},
		{
			name: "untouched field cancels only itself",
			raw:  `{"action":"accept","content":{"q2":["a"]}}`,
			want: []askuser.Answer{
				{Cancelled: true},
				{ChosenIDs: []string{"a"}},
			},
		},
		{
			name: "unknown id is dropped (closed menu)",
			raw:  `{"action":"accept","content":{"q1":"nosuch","q2":["a","zzz"]}}`,
			want: []askuser.Answer{
				{Cancelled: true},
				{ChosenIDs: []string{"a"}},
			},
		},
		{
			name: "decline cancels everything",
			raw:  `{"action":"decline"}`,
			want: []askuser.Answer{{Cancelled: true}, {Cancelled: true}},
		},
		{
			name: "cancel cancels everything",
			raw:  `{"action":"cancel"}`,
			want: []askuser.Answer{{Cancelled: true}, {Cancelled: true}},
		},
		{
			name: "malformed body cancels everything",
			raw:  `garbage`,
			want: []askuser.Answer{{Cancelled: true}, {Cancelled: true}},
		},
	}

	for _, c := range cases {
		got := answersFromElicitation(b, acp.ParseElicitationResponse([]byte(c.raw)))
		if len(got) != len(c.want) {
			t.Fatalf("%s: len = %d, want %d", c.name, len(got), len(c.want))
		}
		for i := range c.want {
			if !sameAnswer(got[i], c.want[i]) {
				t.Errorf("%s: answer %d = %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// TestAnswersFromElicitation_AlwaysFullLength pins the length contract
// the tool layer relies on (it indexes answers by question).
func TestAnswersFromElicitation_AlwaysFullLength(t *testing.T) {
	b := askuser.Batch{Questions: []askuser.Question{
		singleChoiceQuestion(), singleChoiceQuestion(), singleChoiceQuestion(),
	}}
	for _, raw := range []string{`{}`, `{"action":"decline"}`, `{"action":"accept","content":{"q2":"user"}}`} {
		got := answersFromElicitation(b, acp.ParseElicitationResponse([]byte(raw)))
		if len(got) != 3 {
			t.Fatalf("%s: len = %d, want 3", raw, len(got))
		}
	}
}

// TestAcpAskTimeoutFromEnv pins the env resolution (override / bogus /
// absent) — the bound that stops a client which ignores the request from
// hanging the turn.
func TestAcpAskTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		val  string
		want time.Duration
	}{
		{"", acpAskTimeoutDefault},
		{"5", 5 * time.Second},
		{"  7  ", 7 * time.Second},
		{"bogus", acpAskTimeoutDefault},
		{"0", acpAskTimeoutDefault},
		{"-3", acpAskTimeoutDefault},
	}
	for _, c := range cases {
		got := acpAskTimeoutFromEnv(func(string) string { return c.val })
		if got != c.want {
			t.Errorf("acpAskTimeoutFromEnv(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

// TestACPAskUser_NoTurnCancels pins the "asked outside a turn" ending:
// the callback must return a full-length cancelled slice rather than
// block or panic — a tool can only be reached inside a turn, but a
// future caller might not be, and hanging is the worst answer.
func TestACPAskUser_NoTurnCancels(t *testing.T) {
	a := newACPAskUser(nil, time.Second)
	got := a.askBatch(askuser.Batch{Questions: []askuser.Question{singleChoiceQuestion(), multiChoiceQuestion()}})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for i, ans := range got {
		if !ans.Cancelled {
			t.Errorf("answer %d = %+v, want Cancelled (no active turn)", i, ans)
		}
	}
	// v1 path delegates to the batch form, so it cancels identically.
	if ans := a.ask(singleChoiceQuestion()); !ans.Cancelled {
		t.Errorf("v1 ask = %+v, want Cancelled", ans)
	}
}

// TestACPAskUser_CancelledCtxCancels pins the ctx ending: a turn that is
// already cancelled must not send a request at all.
func TestACPAskUser_CancelledCtxCancels(t *testing.T) {
	a := newACPAskUser(nil, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the turn died before asking
	defer cancel()
	a.beginTurn(ctx, "sess-1")
	defer a.endTurn()

	got := a.askBatch(askuser.Batch{Questions: []askuser.Question{singleChoiceQuestion()}})
	if len(got) != 1 || !got[0].Cancelled {
		t.Fatalf("answers = %+v, want one Cancelled", got)
	}
}

// TestElicitationParams_MarshalRoundTrip guards the schema against a
// field rename: what we build must decode back into the same shape the
// SDK validates (mode/message/requestedSchema with object type).
func TestElicitationParams_MarshalRoundTrip(t *testing.T) {
	b := askuser.Batch{Questions: []askuser.Question{singleChoiceQuestion(), multiChoiceQuestion()}}
	raw, err := json.Marshal(buildElicitationParams("s", 1, b))
	if err != nil {
		t.Fatal(err)
	}
	var back acp.CreateElicitationParams
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if back.Mode != acp.ElicitationModeForm || back.RequestedSchema.Type != "object" {
		t.Fatalf("round trip lost shape: %+v", back)
	}
	if len(back.RequestedSchema.Properties) != 4 {
		t.Fatalf("round trip props = %v, want 4", keysOf(back.RequestedSchema.Properties))
	}
	if got := back.RequestedSchema.Properties["q2"].Items; got == nil || len(got.Enum) != 3 {
		t.Fatalf("round trip q2 items = %+v, want 3-value enum", got)
	}
}

// --- helpers -------------------------------------------------------

func sameAnswer(a, b askuser.Answer) bool {
	if a.Cancelled != b.Cancelled || a.FreeText != b.FreeText {
		return false
	}
	if len(a.ChosenIDs) != len(b.ChosenIDs) {
		return false
	}
	for i := range a.ChosenIDs {
		if a.ChosenIDs[i] != b.ChosenIDs[i] {
			return false
		}
	}
	return true
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
