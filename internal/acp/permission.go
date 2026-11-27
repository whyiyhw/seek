// permission.go is the protocol half of session/request_permission
// (feature-acp.md backlog ①): the wire types seek SENDS and the outcome
// decoding for what the client ANSWERS. The permission.Policy/askFn
// wiring lives one layer up in cmd/seek (acp_approval.go — the third
// approval lane after the TUI's askuser and print mode's
// --approval-stdin); this package stays domain-free by design, same
// layering as session.go's Reanchorer.
//
// Wire contract reverse-engineered from the @agentclientprotocol/sdk
// v1 schema bundled on this machine (zcode-acp bridge,
// dist/schema/types.gen.d.ts — the pitfall lesson "reverse the local
// SDK schema FIRST" applied): request params carry sessionId + a
// toolCall update (toolCallId/kind/title/content) + options
// [{optionId,name,kind}]; the response result is
// {"outcome":{"outcome":"selected","optionId":…}} or
// {"outcome":{"outcome":"cancelled"}}. The pre-1.0 draft's bare-string
// outcome ({"outcome":"allow_once"}) is still accepted on decode —
// older clients exist in the wild and lenience here costs nothing.
package acp

import (
	"encoding/json"
)

// MethodRequestPermission is the wire method for a client-side approval
// prompt (schema: CLIENT_METHODS.session_request_permission). Two lanes
// call it: the permission gate (acp_approval.go) and — since card
// c_7bqiq5 — the ask_user FALLBACK for clients that do not implement
// `elicitation/create`. The constant exists so those two call sites can
// never drift apart on the spelling.
const MethodRequestPermission = "session/request_permission"

// Permission option kinds — the vocabulary both sides speak. seek
// offers exactly three (allow_once / allow_always / reject_once);
// reject_always exists in the schema but permission.Policy has no
// per-Kind deny-list to map it onto, so it is neither offered nor
// honoured (an echo of it decodes to an unknown optionId → deny).
const (
	OptionAllowOnce   = "allow_once"
	OptionAllowAlways = "allow_always"
	OptionRejectOnce  = "reject_once"
)

// RequestPermissionParams is session/request_permission's request
// params (schema: RequestPermissionRequest). Options must contain at
// least one entry per the schema; PermissionOptions() builds seek's
// standard three.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  PermissionToolCall `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionToolCall is the toolCall subject (schema: ToolCallUpdate
// subset — toolCallId is the only required field). The id is minted by
// the asker ("perm-<n>"), not a real tool call id: the permission gate
// fires before a tool-call id exists on the ACP side, and the schema
// only requires the field to be present and unique.
type PermissionToolCall struct {
	ToolCallID string                `json:"toolCallId"`
	Kind       string                `json:"kind,omitempty"` // "execute"|"edit"|"read"|"other"
	Title      string                `json:"title,omitempty"`
	Content    []ToolCallContentWrap `json:"content,omitempty"`
}

// ToolCallContentWrap is one entry of toolCall.content (schema:
// ToolCallContent's "content" variant — a wrapper around a Content
// block; seek sends text blocks with the command / path / diff).
type ToolCallContentWrap struct {
	Type    string       `json:"type"` // "content"
	Content ContentBlock `json:"content"`
}

// PermissionOption is one selectable answer (schema: PermissionOption).
// OptionId is what the client echoes back in the selected outcome; Kind
// is the UI hint (allow_once/allow_always/reject_once/…).
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// PermissionOptions is the standard three-way menu seek offers.
func PermissionOptions() []PermissionOption {
	return []PermissionOption{
		{OptionID: OptionAllowOnce, Name: "Allow", Kind: OptionAllowOnce},
		{OptionID: OptionAllowAlways, Name: "Always allow", Kind: OptionAllowAlways},
		{OptionID: OptionRejectOnce, Name: "Reject", Kind: OptionRejectOnce},
	}
}

// PermissionDecision is the decoded answer to one permission request.
// Allow/Reject carry WHICH advertised option the user picked (the
// optionId); Cancelled covers both the protocol's cancelled outcome and
// every no-answer ending (the asker maps those before decoding).
type PermissionDecision int

const (
	DecisionAllowOnce PermissionDecision = iota
	DecisionAllowAlways
	DecisionReject
	DecisionCancelled
)

// ParsePermissionOutcome decodes a session/request_permission reply
// body (the Call result). Shapes accepted:
//
//	{"outcome":{"outcome":"selected","optionId":"allow_once"}}  (v1 schema)
//	{"outcome":{"outcome":"cancelled"}}                          (v1 schema)
//	{"outcome":"allow_once"}                                     (legacy draft)
//
// A selected optionId outside the offered three is a rejection — the
// same posture as --approval-stdin ("anything else is a denial"): the
// client was handed a closed menu, so an exotic echo is a client bug,
// not a new permission.
func ParsePermissionOutcome(raw []byte) PermissionDecision {
	var body struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Outcome) == 0 {
		return DecisionCancelled
	}
	// Nested-object form first (current schema): {"outcome":…,"optionId":…}.
	var nested struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId"`
	}
	if err := json.Unmarshal(body.Outcome, &nested); err == nil && nested.Outcome != "" {
		switch nested.Outcome {
		case "cancelled":
			return DecisionCancelled
		case "selected":
			return classifyOptionID(nested.OptionID)
		}
	}
	// Legacy bare-string form: {"outcome":"allow_once"}.
	var legacy string
	if err := json.Unmarshal(body.Outcome, &legacy); err == nil && legacy != "" {
		return classifyOptionID(legacy)
	}
	return DecisionCancelled
}

func classifyOptionID(id string) PermissionDecision {
	switch id {
	case OptionAllowOnce:
		return DecisionAllowOnce
	case OptionAllowAlways:
		return DecisionAllowAlways
	case OptionRejectOnce:
		return DecisionReject
	default:
		return DecisionReject // unknown echo: deny (see doc comment)
	}
}

// ParsePermissionSelection decodes a session/request_permission reply into
// the RAW optionId the client echoed, without classifying it.
//
// Why this exists alongside ParsePermissionOutcome: the approval lane hands
// the client a closed menu of three fixed ids, so an exotic echo there is a
// client bug and "deny" is the honest reading. The ask_user FALLBACK (card
// c_7bqiq5) instead hands over the QUESTION's own option ids, so the id is
// the answer — interpreting it against the approval vocabulary would turn
// every one of them into a denial.
//
// second=false means "no usable selection" (the protocol's cancelled
// outcome, a malformed body, or a client error); callers treat that as the
// cancelled answer, the same posture as the form channel's decline.
func ParsePermissionSelection(raw []byte) (optionID string, ok bool) {
	var body struct {
		Outcome json.RawMessage `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Outcome) == 0 {
		return "", false
	}
	var nested struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId"`
	}
	if err := json.Unmarshal(body.Outcome, &nested); err == nil && nested.Outcome != "" {
		if nested.Outcome != "selected" || nested.OptionID == "" {
			return "", false
		}
		return nested.OptionID, true
	}
	// Legacy bare-string form: the string IS the option id.
	var legacy string
	if err := json.Unmarshal(body.Outcome, &legacy); err == nil && legacy != "" {
		return legacy, true
	}
	return "", false
}
