package acp

// Elicitation lane fixtures (acp_askuser.go / elicitation.go): the
// outcome decoder, the capability gate, and the wire shape of the params
// seek emits.
//
// The gate is the load-bearing part: ask_user on the ACP lane is armed
// ONLY when the client advertises `elicitation.form`, so a wrong answer
// here flips an entire lane's behaviour between "ask the human" and
// "tell the model to rephrase".

import (
	"encoding/json"
	"testing"
)

func TestParseElicitationResponse(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantAction string
	}{
		{"accept with content", `{"action":"accept","content":{"q1":"user"}}`, ElicitationAccept},
		{"accept with empty content", `{"action":"accept","content":{}}`, ElicitationAccept},
		{"accept without content", `{"action":"accept"}`, ElicitationAccept},
		{"decline", `{"action":"decline"}`, ElicitationDecline},
		{"cancel", `{"action":"cancel"}`, ElicitationCancel},
		{"unknown action is no answer", `{"action":"_future_mode"}`, ""},
		{"missing action", `{"content":{"q1":"x"}}`, ""},
		{"junk json", `not json`, ""},
		{"empty object", `{}`, ""},
	}
	for _, c := range cases {
		got := ParseElicitationResponse([]byte(c.raw))
		if got.Action != c.wantAction {
			t.Errorf("%s: action = %q, want %q", c.name, got.Action, c.wantAction)
		}
		if got.Action != ElicitationAccept && got.Content != nil {
			t.Errorf("%s: content = %v, want nil for a non-accept reply", c.name, got.Content)
		}
	}
}

// TestElicitationResponse_TypedAccessors pins the two content shapes seek
// asks for, and the conservative posture on everything else: a wrong type
// is an absent answer, never a guessed one.
func TestElicitationResponse_TypedAccessors(t *testing.T) {
	r := ParseElicitationResponse([]byte(`{"action":"accept","content":{
		"q1":"project",
		"q2":["a","b"],
		"q3":42,
		"q4":["a",7,"b"]
	}}`))

	if got := r.String("q1"); got != "project" {
		t.Errorf("String(q1) = %q, want project", got)
	}
	if got := r.String("q3"); got != "" {
		t.Errorf("String(q3) on a number = %q, want empty (type mismatch is not an answer)", got)
	}
	if got := r.String("absent"); got != "" {
		t.Errorf("String(absent) = %q, want empty", got)
	}
	if got := r.Strings("q2"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Strings(q2) = %v, want [a b]", got)
	}
	if got := r.Strings("q1"); got != nil {
		t.Errorf("Strings(q1) on a string = %v, want nil", got)
	}
	if got := r.Strings("absent"); got != nil {
		t.Errorf("Strings(absent) = %v, want nil", got)
	}
}

// TestClientCapabilities_SupportsElicitationForm is the gate table. The
// "null form object" and "absent elicitation" rows are the ones a
// permissive decoder would get wrong (the schema allows both spellings of
// "not supported").
func TestClientCapabilities_SupportsElicitationForm(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"form advertised as empty object", `{"elicitation":{"form":{}}}`, true},
		{"form advertised with url too", `{"elicitation":{"form":{},"url":{}}}`, true},
		{"url only", `{"elicitation":{"url":{}}}`, false},
		{"form explicitly null", `{"elicitation":{"form":null}}`, false},
		{"elicitation null", `{"elicitation":null}`, false},
		{"absent elicitation", `{"fs":{"readTextFile":false}}`, false},
		{"empty object", `{}`, false},
		{"paseo 0.17 baseline", `{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":true}`, false},
	}
	for _, c := range cases {
		var caps ClientCapabilities
		if err := json.Unmarshal([]byte(c.raw), &caps); err != nil {
			t.Fatalf("%s: unmarshal: %v", c.name, err)
		}
		if got := caps.SupportsElicitationForm(); got != c.want {
			t.Errorf("%s: SupportsElicitationForm() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestInitializeParams_SupportsElicitationForm_NilSafe covers the
// initialize-level gate: a client that omits clientCapabilities entirely
// (the shape every pre-existing test and every Paseo build sends) must
// answer false without dereferencing anything.
func TestInitializeParams_SupportsElicitationForm_NilSafe(t *testing.T) {
	if (InitializeParams{ProtocolVersion: 1}).SupportsElicitationForm() {
		t.Error("no capabilities block must not enable the ask lane")
	}
	var p InitializeParams
	if err := json.Unmarshal([]byte(`{"protocolVersion":1}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.SupportsElicitationForm() {
		t.Error("absent clientCapabilities must not enable the ask lane")
	}
	if err := json.Unmarshal([]byte(`{"protocolVersion":1,"clientCapabilities":{"elicitation":{"form":{}}}}`), &p); err != nil {
		t.Fatal(err)
	}
	if !p.SupportsElicitationForm() {
		t.Error("advertised form elicitation must enable the ask lane")
	}
}

// TestCreateElicitationParams_WireShape pins the JSON seek sends: the
// schema's field names (mode / message / requestedSchema / sessionId) and
// the "object" schema discriminator. A rename here breaks every client
// silently — the request just fails validation on their side.
func TestCreateElicitationParams_WireShape(t *testing.T) {
	p := CreateElicitationParams{
		SessionID: "sess-1",
		Mode:      ElicitationModeForm,
		Message:   "pick one",
		RequestedSchema: ElicitationSchema{
			Type: "object",
			Properties: map[string]ElicitationPropertySchema{
				"q1": {Type: "string", OneOf: []EnumOption{{Const: "a", Title: "A"}}},
			},
		},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sessionId", "mode", "message", "requestedSchema"} {
		if _, ok := got[k]; !ok {
			t.Errorf("params missing %q: %s", k, raw)
		}
	}
	if got["mode"] != "form" {
		t.Errorf("mode = %v, want form", got["mode"])
	}
	schema, _ := got["requestedSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Errorf("requestedSchema.type = %v, want object", schema["type"])
	}
	props, _ := schema["properties"].(map[string]any)
	q1, _ := props["q1"].(map[string]any)
	oneOf, _ := q1["oneOf"].([]any)
	if len(oneOf) != 1 {
		t.Fatalf("q1.oneOf = %v, want one entry", q1["oneOf"])
	}
	opt, _ := oneOf[0].(map[string]any)
	if opt["const"] != "a" || opt["title"] != "A" {
		t.Errorf("oneOf[0] = %v, want {const:a title:A}", opt)
	}
	// toolCallId must be omitted when empty — the schema marks it
	// optional and a stray "" is a client-side validation risk.
	if _, ok := got["toolCallId"]; ok {
		t.Errorf("empty toolCallId must be omitted: %s", raw)
	}
}

// TestMethodElicitationCreate pins the wire method string. The SDK's
// CLIENT_METHODS constant is "elicitation/create" — a typo here is a
// -32601 nobody sees until a user asks a question.
func TestMethodElicitationCreate(t *testing.T) {
	if MethodElicitationCreate != "elicitation/create" {
		t.Errorf("MethodElicitationCreate = %q, want elicitation/create", MethodElicitationCreate)
	}
}
