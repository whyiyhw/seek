package rpc

// Transport-agnostic method registry + JSON-RPC 2.0 envelope types +
// error model: feature-inspect-rpc §3.2/§3.5, absorbed by feature-web-
// frontend P3 (its §3.3). This file is the single authority for
// data-plane methods — hosted by the serve process POST /rpc
// (internal/controlplane/webfrontend_rpc.go). The stdio Server keeps
// its own frozen 3-method switch in server.go forever: the IDE line
// was closed 2026-11-16 (ACP covers IDE drive; see the status header
// of docs/prd/feature-inspect-rpc.md) — stdio never adopts this
// registry and never grows its method set.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Standard JSON-RPC 2.0 error codes (spec §5.1; inspect-rpc §3.5).
const (
	CodeParseError     = -32700 // the payload is not valid JSON
	CodeInvalidRequest = -32600 // not a JSON-RPC 2.0 envelope
	CodeMethodNotFound = -32601 // method not in the registry
	CodeInvalidParams  = -32602 // params failed the method's validation
	CodeInternalError  = -32603 // unexpected internal error
)

// Application error range -32001..-32099 (inspect-rpc §3.5). Codes are
// contracts once shipped: reuse only with the reserved meaning, never
// renumber. W4.2/W4.3 method sets emit these.
const (
	CodeSessionNotFound  = -32001
	CodeMemoryNotFound   = -32002
	CodeProjectNotFound  = -32003
	CodePermissionDenied = -32004 // reserved; the read-only phase never emits it
	CodeRateLimited      = -32005
	// CodeSubagentNotFound — subagent/get found no folded row for the
	// requested sub_sid in the resolved project's index (inspect-rpc
	// §6 #12 close-out). A distinct code from -32001 because the id
	// space is different: a sub_sid is not a session id, and the panel
	// must not tell the user to go look for a transcript that was never
	// a session of its own.
	CodeSubagentNotFound = -32006
)

// Error is the JSON-RPC error object. Method implementations return
// *Error for coded failures; hosts report any other error as
// CodeInternalError. Data is optional structured diagnostics (§3.5) —
// paths, suggestions — for developer views, not the default panel.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("json-rpc %d: %s", e.Code, e.Message) }

// Errorf builds a coded *Error.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Method is one RPC method handler. Implementations should be pure (or
// read-only side-effecting at most — never mutate state via RPC in the
// asset-plane phase). Return *Error for coded failures.
type Method func(ctx context.Context, params json.RawMessage) (any, error)

// Registry holds the method dispatch table (inspect-rpc §3.2 shape).
// Construct once at server start, Register during construction,
// Dispatch after: safe for concurrent reads once the Register phase
// ends — deliberately unguarded, matching the PRD contract. Register
// after the first Dispatch is a wiring bug.
type Registry struct {
	methods map[string]Method
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{methods: make(map[string]Method)} }

// Register adds m under name; a later Register for the same name
// replaces the earlier method (construction-time wiring, last wire
// wins). Nil methods are a wiring bug and panic here, on the startup
// path — never on a request path.
func (r *Registry) Register(name string, m Method) {
	if m == nil {
		panic("rpc: Register nil method: " + name)
	}
	r.methods[name] = m
}

// Dispatch routes one call. Unknown names answer *Error
// CodeMethodNotFound; handlers' *Error passes through verbatim
// (wrappings included — hosts must use errors.As, not type-assert).
func (r *Registry) Dispatch(ctx context.Context, name string, params json.RawMessage) (any, error) {
	m, ok := r.methods[name]
	if !ok {
		return nil, Errorf(CodeMethodNotFound, "method not found: %s", name)
	}
	return m(ctx, params)
}

// Request is one JSON-RPC 2.0 request envelope. ID stays raw so
// string/number/null ids round-trip verbatim; its absence marks a
// notification (no response expected, spec §4).
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Validate applies the envelope rules beyond JSON shape: jsonrpc must
// be exactly "2.0" and method non-empty. Params shape is deliberately
// NOT checked here — it is the method's business and answers
// CodeInvalidParams (§3.2: params validation lives per-method).
func (req *Request) Validate() error {
	if req.JSONRPC != "2.0" {
		return Errorf(CodeInvalidRequest, `jsonrpc must be "2.0"`)
	}
	if req.Method == "" {
		return Errorf(CodeInvalidRequest, "method is required")
	}
	return nil
}

// IsNotification reports whether the request carries no id and thus
// expects no response.
func (req *Request) IsNotification() bool { return len(req.ID) == 0 }

// Response is the reply envelope: exactly one of Result / Error is
// set. Result is pre-marshaled raw JSON so falsy values (0, "", false,
// nil) survive omitempty — a nil result marshals as "result":null as
// the spec requires the member to exist.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// NewResult marshals a success response. Failing to marshal a method's
// result is the host's signal to answer CodeInternalError instead.
func NewResult(id json.RawMessage, result any) (*Response, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Response{JSONRPC: "2.0", ID: id, Result: raw}, nil
}

// NewError maps any method/dispatch failure onto the error envelope:
// *Error (possibly wrapped) keeps its code and message; anything else
// becomes CodeInternalError. The id is whatever the host could recover
// (json.RawMessage("null") when the request id is unknowable, spec §5.1).
func NewError(id json.RawMessage, err error) *Response {
	var e *Error
	if !errors.As(err, &e) {
		e = Errorf(CodeInternalError, "%s", err.Error())
	}
	return &Response{JSONRPC: "2.0", ID: id, Error: e}
}
