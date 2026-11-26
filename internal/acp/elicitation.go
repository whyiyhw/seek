// elicitation.go is the protocol half of `elicitation/create` — the ACP
// method that lets an agent ask the human a structured question (a form)
// through the client, instead of dying on "no interactive channel".
//
// Why this exists: ask_user / propose were born ModeDisabled and only the
// TUI ever opted in, so every headless lane answers ErrDisabled ("rephrase
// as plain text"). That posture is right for print/rpc/goal — there is no
// human — but WRONG for the ACP lane, which always has one: the editor.
// What ACP lacked until this method was a way to reach them; with
// `elicitation/create` (form mode, gated on
// `clientCapabilities.elicitation.form`) it no longer does.
//
// Same layering as permission.go: this file owns the WIRE TYPES and the
// outcome decoding; the askuser.Policy wiring lives one layer up in
// cmd/seek (acp_askuser.go). Wire shape read from the locally bundled SDK
// (@agentclientprotocol/sdk 1.9.0, schema/types.gen.d.ts — the pitfall
// lesson "reverse the local SDK schema FIRST" applied).
package acp

import "encoding/json"

// MethodElicitationCreate is the wire method an agent calls to ask the
// client for structured user input (schema: CLIENT_METHODS.elicitation_create
// → "elicitation/create").
const MethodElicitationCreate = "elicitation/create"

// ElicitationModeForm is the only mode seek sends. The schema also has
// "url" (direct the user to a link) — meaningless for a multiple-choice
// question, and a client that only advertises `form` would reject it.
const ElicitationModeForm = "form"

// Elicitation action vocabulary (schema: CreateElicitationResponse).
// "accept" carries content matching the requested schema; "decline" is a
// deliberate refusal; "cancel" is the user backing out. seek maps decline
// and cancel identically (Cancelled), because the askuser.Answer shape has
// one "no answer" bit — but the distinction is kept on the wire so a
// client's own UX can differ.
const (
	ElicitationAccept  = "accept"
	ElicitationDecline = "decline"
	ElicitationCancel  = "cancel"
)

// ClientCapabilities is the subset of the client's initialize params seek
// reads. Decoding is lenient (unknown fields are ignored by
// encoding/json), so a client with more capabilities than this struct
// describes loses nothing.
//
// The struct is deliberately small: seek only ever gates on
// `elicitation.form`. fs/terminal are carried because they are the two
// neighbours every client sends and having them here makes the trace
// readable — nothing reads them yet.
type ClientCapabilities struct {
	Elicitation *ElicitationCapabilities `json:"elicitation,omitempty"`
	Terminal    bool                     `json:"terminal,omitempty"`
}

// ElicitationCapabilities mirrors the schema: an OBJECT whose sub-fields
// advertise each mode, where presence ({} on the wire) means supported.
// Null and omitted are equivalent and both mean "not advertised".
type ElicitationCapabilities struct {
	Form *ElicitationFormCapabilities `json:"form,omitempty"`
	URL  *ElicitationURLCapabilities  `json:"url,omitempty"`
}

// ElicitationFormCapabilities is the on-switch for form elicitation. It
// has no fields — supplying `{}` IS the advertisement (schema comment:
// "Supplying {} explicitly advertises form support").
type ElicitationFormCapabilities struct{}

// ElicitationURLCapabilities is the URL-mode on-switch. seek never sends
// url elicitations; the field exists so a decode round-trip keeps what the
// client said.
type ElicitationURLCapabilities struct{}

// SupportsElicitationForm reports whether the client advertised form-based
// elicitation. This is the gate for the whole ask_user lane on ACP —
// advertising the capability is the client's promise that it will render
// the form and answer, and a request sent to a client that did not would
// be answered -32601 (method not found) or hang.
//
// Mirrors the zcode bridge's own check
// (`this.clientCapabilities.elicitation?.form != null`), which is the
// reference implementation for this protocol in the same fleet.
func (c ClientCapabilities) SupportsElicitationForm() bool {
	return c.Elicitation != nil && c.Elicitation.Form != nil
}

// CreateElicitationParams is elicitation/create's request params. Flattened
// from the schema's intersection: session scope (sessionId, optional
// toolCallId) + mode discriminator + message + the form schema.
type CreateElicitationParams struct {
	// SessionID is the session the question belongs to (schema:
	// ElicitationSessionScope.sessionId).
	SessionID string `json:"sessionId"`
	// ToolCallID optionally ties the form to a tool call (schema:
	// ElicitationSessionScope.toolCallId). seek omits it: the askuser tool
	// has no ACP-visible tool-call id at the point the question is raised.
	ToolCallID string `json:"toolCallId,omitempty"`
	// Mode is the elicitation mode discriminator; always "form" here.
	Mode string `json:"mode"`
	// Message is the human-readable prompt shown above the form.
	Message string `json:"message"`
	// RequestedSchema is the JSON Schema describing the fields.
	RequestedSchema ElicitationSchema `json:"requestedSchema"`
}

// ElicitationSchema is the form description (schema: ElicitationSchema) —
// a JSON Schema object whose properties must be primitive types. seek only
// ever emits `string` (single-select via oneOf, free text) and `array`
// (multi-select), which is the subset the schema calls out for
// multiple-choice questions.
type ElicitationSchema struct {
	// Type is the schema discriminator; always "object".
	Type string `json:"type"`
	// Title is an optional form title.
	Title string `json:"title,omitempty"`
	// Properties is the field set, keyed by field name.
	Properties map[string]ElicitationPropertySchema `json:"properties,omitempty"`
	// Required names the fields the client must fill. seek leaves it empty
	// on purpose: the free-text "other" field is an ALTERNATIVE to the
	// enumerated one, so requiring the enum would make "type your own"
	// unanswerable.
	Required []string `json:"required,omitempty"`
}

// ElicitationPropertySchema is one form field. One Go struct covers the
// schema's variants (string / number / integer / boolean / array) rather
// than a union type: seek emits two of them, and the unused fields fall
// out via omitempty.
type ElicitationPropertySchema struct {
	// Type is the JSON Schema type: "string" or "array" here.
	Type string `json:"type"`
	// Title is the field label.
	Title string `json:"title,omitempty"`
	// Description is secondary text under the label.
	Description string `json:"description,omitempty"`
	// Enum lists allowed values for an untitled single-select.
	Enum []string `json:"enum,omitempty"`
	// OneOf lists TITLED single-select options (schema: EnumOption). seek
	// prefers this over Enum for single-select because it carries the
	// human label and the muted description.
	OneOf []EnumOption `json:"oneOf,omitempty"`
	// Items describes array elements (multi-select).
	Items *ElicitationItems `json:"items,omitempty"`
	// MinItems / MaxItems bound an array field.
	MinItems int `json:"minItems,omitempty"`
	MaxItems int `json:"maxItems,omitempty"`
}

// EnumOption is one titled choice (schema: EnumOption) — the const the
// client echoes back, plus the label the human reads.
type EnumOption struct {
	Const       string `json:"const"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// ElicitationItems describes the elements of an array field (schema:
// MultiSelectItems → StringMultiSelectItems & {type:"string"}).
type ElicitationItems struct {
	Type string   `json:"type"`
	Enum []string `json:"enum,omitempty"`
}

// ElicitationResponse is the decoded reply to one elicitation/create call.
// Content is kept raw and read through the typed accessors: the schema
// allows string / number / boolean / array-of-string values, and seek only
// ever asks for two of those shapes, so decoding into `any` and classifying
// on read keeps one parser instead of four.
type ElicitationResponse struct {
	// Action is "accept" / "decline" / "cancel" (or "" for an unknown
	// spelling, which callers must treat as no answer).
	Action string
	// Content is the accepted form payload; nil unless Action == accept.
	Content map[string]json.RawMessage
}

// Accepted reports whether the user submitted the form.
func (r ElicitationResponse) Accepted() bool { return r.Action == ElicitationAccept }

// String returns content[key] as a string, or "" when absent / not a
// string. A non-string value is a client bug, not a new answer type: the
// field was declared as a string.
func (r ElicitationResponse) String(key string) string {
	raw, ok := r.Content[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// Strings returns content[key] as a string slice, or nil when absent / not
// an array. Elements that are not strings are dropped rather than failing
// the whole answer — a partially-garbled multi-select still tells us what
// the user picked.
func (r ElicitationResponse) Strings(key string) []string {
	raw, ok := r.Content[key]
	if !ok {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// ParseElicitationResponse decodes one elicitation/create reply body (the
// Call result). A malformed body or an unknown action decodes to an empty
// Action, which every caller treats as "no answer" — the same
// conservative posture as ParsePermissionOutcome ("anything unrecognised
// is a refusal").
func ParseElicitationResponse(raw []byte) ElicitationResponse {
	var body struct {
		Action  string                     `json:"action"`
		Content map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ElicitationResponse{}
	}
	switch body.Action {
	case ElicitationAccept:
		return ElicitationResponse{Action: ElicitationAccept, Content: body.Content}
	case ElicitationDecline, ElicitationCancel:
		return ElicitationResponse{Action: body.Action}
	default:
		return ElicitationResponse{}
	}
}
