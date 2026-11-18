package deepseek

import (
	"encoding/json"
	"strings"
	"testing"
)

// textOnlyRequest is the canonical "nothing exotic on this turn" body:
// model + one user message + a completion cap. Every optional feature
// (tools, thinking, images, files, safety_check) stays off.
func textOnlyRequest() ChatRequest {
	return ChatRequest{
		Model:     ModelV41Flash,
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		MaxTokens: 16,
	}
}

// TestChatRequest_TextOnlyBytesUnchanged is the prefix-cache guard for
// SC1 (safety_check, docs/prd/feature-deepseek-app-api.md §5): adding an
// optional request field must NOT perturb the marshalled bytes of an
// ordinary text-only request. Key order here comes from ChatRequest's
// custom MarshalJSON (vision.go), which forces `messages` last — the
// pinned string is the exact pre-SC1 shape, so a field that forgot
// `omitempty` fails this test instead of silently costing cache misses
// on every turn.
func TestChatRequest_TextOnlyBytesUnchanged(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(textOnlyRequest())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"` + ModelV41Flash + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	if string(got) != want {
		t.Errorf("text-only request bytes drifted (prefix cache would miss):\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(string(got), "safety_check") {
		t.Error("safety_check must be absent from the body when off")
	}
}

// TestChatRequest_SafetyCheckWireShape pins the documented request shape
// `"safety_check": {"level": "high"}` — object form, not a bare string —
// and that enabling it adds exactly that key, changing no other bytes.
func TestChatRequest_SafetyCheckWireShape(t *testing.T) {
	t.Parallel()
	off, err := json.Marshal(textOnlyRequest())
	if err != nil {
		t.Fatal(err)
	}
	req := textOnlyRequest()
	req.SafetyCheck = &SafetyCheck{Level: SafetyCheckHigh}
	on, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	const key = `"safety_check":{"level":"high"},`
	if !strings.Contains(string(on), key) {
		t.Fatalf("body missing %s:\n%s", key, on)
	}
	// Removing the one added key must reproduce the off body byte for
	// byte — anything else means the feature touched unrelated bytes.
	if stripped := strings.Replace(string(on), key, "", 1); stripped != string(off) {
		t.Errorf("enabling safety_check changed more than one key:\n on(-key): %s\n      off: %s", stripped, off)
	}

	// Round-trip both shapes: the object decodes back faithfully, and an
	// absent key decodes to nil (off) — never a zero-level struct that
	// would be forwarded as `{"level":""}`.
	var onReq, offReq ChatRequest
	if err := json.Unmarshal(on, &onReq); err != nil {
		t.Fatal(err)
	}
	if onReq.SafetyCheck == nil || onReq.SafetyCheck.Level != SafetyCheckHigh {
		t.Errorf("round-trip SafetyCheck = %+v, want level %q", onReq.SafetyCheck, SafetyCheckHigh)
	}
	if err := json.Unmarshal(off, &offReq); err != nil {
		t.Fatal(err)
	}
	if offReq.SafetyCheck != nil {
		t.Errorf("absent safety_check must decode to nil, got %+v", offReq.SafetyCheck)
	}
}
