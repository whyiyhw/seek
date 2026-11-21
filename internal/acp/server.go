// Package acp implements the agent side of the Agent Client Protocol
// (ACP, agentclientprotocol.com) — the standard protocol Zed and other
// editors use to drive a coding agent (v7 柱 P). It lets seek run as
// `seek acp`, spoken to by any ACP client over stdio.
//
// Transport is JSON-RPC 2.0 over newline-delimited stdio (same shape as
// seek's MCP client, pkg/mcp — json.Encoder/Decoder, no Content-Length
// framing). This file is the transport + dispatch core (M-P.1): it owns
// the read loop, method routing, response/notification writing, and
// per-session cancellation. The mapping to seek's agent (session/prompt →
// Agent.Prompt event stream, request_permission → askuser) is injected as
// a Backend so this layer is testable with a fake (no real agent).
package acp

import (
	"context"
	"encoding/json"
	"io"
	"sync"
)

// --- JSON-RPC 2.0 envelope (newline-delimited) ----------------------

type request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"` // absent ⇒ notification
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// --- ACP method types (minimal subset for the handshake + prompt) ---

type InitializeParams struct {
	ProtocolVersion int `json:"protocolVersion"`
}

type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
}

type AgentCapabilities struct {
	LoadSession bool `json:"loadSession"`
	// PromptCapabilities tells the client what content types it may put in
	// a session/prompt. Advertising image lets editors (Zed) offer image
	// attachment; seek sends those natively to the multimodal lane
	// (feature-vision; M-P.5's OCR-to-text is long gone). Omitted when zero
	// so a text-only agent stays silent.
	PromptCapabilities PromptCapabilities `json:"promptCapabilities"`
}

// PromptCapabilities is the subset of ACP prompt content types seek
// accepts. Audio / embedded-context are not handled (seek is text-only;
// image is OCR'd, not sent as bytes).
type PromptCapabilities struct {
	Image bool `json:"image,omitempty"`
}

type NewSessionParams struct {
	Cwd string `json:"cwd"`
}

// LoadSessionParams is session/load's request (ACP schema
// LoadSessionRequest). SessionID is the handle the agent minted at
// session/new — for seek that is the durable transcript id, because a
// resumable handle must outlive the process. Cwd is where the session
// runs; seek re-anchors both cwd conventions to it exactly as
// session/new does. MCPServers / AdditionalDirectories are decoded for
// schema fidelity and ignored: seek has no MCP passthrough yet and
// multi-root sessions are not supported, so silently honouring half of
// them would be worse than ignoring all of them.
type LoadSessionParams struct {
	SessionID             string          `json:"sessionId"`
	Cwd                   string          `json:"cwd"`
	MCPServers            json.RawMessage `json:"mcpServers,omitempty"`
	AdditionalDirectories []string        `json:"additionalDirectories,omitempty"`
}

// LoadSessionResult is session/load's response. Every field is optional
// in the schema; seek returns the config menu so the client's model
// dropdown reflects the RESUMED session rather than the process default.
type LoadSessionResult struct {
	ConfigOptions []ConfigOption `json:"configOptions,omitempty"`
}

type NewSessionResult struct {
	SessionID string `json:"sessionId"`
	// ConfigOptions is the session-scoped configuration menu (model
	// selector). Non-nil ⇒ ACP clients (paseo) render a model dropdown
	// and may follow up with session/set_config_option; nil/null ⇒ the
	// client filters the session-configure capability and shows nothing
	// (the pre-C behaviour). Wire shape reverse-engineered from the
	// @agentclientprotocol/sdk schema + the zcode-acp-server reference
	// bridge; see SetConfigOptionParams for the switch direction.
	ConfigOptions []ConfigOption `json:"configOptions"`
}

// ConfigOption is one entry of the session configuration menu carried on
// session/new (and echoed back, with an updated CurrentValue, on every
// session/set_config_option response). Schema: SessionConfigOption with
// the select variant flattened — id/name/description/category plus
// type:"select" with currentValue/options inlined on the same object.
// seek deliberately serves ONLY the select variant: the boolean variant
// (an on/off toggle) exists in the schema but nothing in seek needs it,
// and a client sending one would fail SetConfigOptionParams decoding
// (loud, by design) rather than half-work.
type ConfigOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Category is UX-only metadata ("model" groups it with model pickers
	// in client UIs; the zcode bridge uses the same category string).
	Category     string               `json:"category,omitempty"`
	Type         string               `json:"type"` // "select"
	CurrentValue string               `json:"currentValue"`
	Options      []ConfigSelectOption `json:"options"`
}

// ConfigSelectOption is one selectable value of a select-type
// ConfigOption. Value is the wire id echoed back by the client in
// session/set_config_option; Name is the human label.
type ConfigSelectOption struct {
	Value string `json:"value"`
	Name  string `json:"name"`
}

// SetConfigOptionParams is the request the client sends when the user
// picks a value in the config dropdown. Schema: sessionId + configId +
// a value that is a BARE STRING for select-type options (no "type"
// field on the wire — the string variant is the default when type is
// absent). seek's multi-provider model menu encodes the choice as
// "<provider>:<model>" (e.g. "glm:glm-5.3"); the cmd/seek backend owns
// the parsing.
type SetConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

// SetConfigOptionResult must carry the FULL option set with the new
// CurrentValue — the schema marks configOptions required on this
// response, and clients re-render their dropdown from it (a partial
// echo would blank the menu).
type SetConfigOptionResult struct {
	ConfigOptions []ConfigOption `json:"configOptions"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Data + MimeType carry an image block's bytes (type=="image"): Data is
	// base64, MimeType e.g. "image/png" (M-P.5). The caller OCRs these to
	// text — seek never forwards image bytes to the model.
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// ImageBlocks returns the prompt's image content blocks (type=="image"
// with non-empty Data), in order. Used by the backend to OCR them into the
// prompt text. M-P.5.
func (p PromptParams) ImageBlocks() []ContentBlock {
	var out []ContentBlock
	for _, c := range p.Prompt {
		if c.Type == "image" && c.Data != "" {
			out = append(out, c)
		}
	}
	return out
}

type PromptResult struct {
	// StopReason is the spec's session/prompt response enum (v1):
	// end_turn | max_tokens | refusal | cancelled (constants in
	// stopreason.go). Internal errors do not appear here — they ride
	// the JSON-RPC error response; the finer-grained classification
	// (silent_timeout / internal_error) and a readable detail live on
	// the _seek_turn_stop termination frame instead.
	StopReason string `json:"stopReason"`
}

// SessionUpdate is the payload of a session/update notification (agent →
// client) carrying a streamed chunk / tool call / plan. Update is the
// opaque ACP update object; the Backend builds it.
type SessionUpdate struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

// Backend is seek's agent behind the protocol. Implemented by the
// cmd/seek adapter (M-P.2); faked in tests.
type Backend interface {
	Initialize(p InitializeParams) InitializeResult
	NewSession(p NewSessionParams) (NewSessionResult, error)
	// LoadSession resumes a previous conversation (session/load). It
	// replays the whole history to the client through send — the schema
	// asks for exactly that ("Stream the entire conversation history back
	// to the client via notifications") — and returns the session's
	// config menu so the client's dropdown reflects the RESUMED lane
	// rather than the process default.
	LoadSession(p LoadSessionParams, send func(SessionUpdate)) (LoadSessionResult, error)
	// Prompt runs one turn. It streams progress by calling send (each call
	// becomes a session/update notification) and returns when the turn
	// ends or ctx is cancelled.
	Prompt(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error)
	// SetConfigOption applies a dropdown pick (session/set_config_option).
	// The server only calls it between turns — a prompt in flight gets the
	// request rejected in dispatch — so implementations may swap turn-lane
	// state (the model/provider) without locks against concurrent turns.
	SetConfigOption(p SetConfigOptionParams) (SetConfigOptionResult, error)
}

// Server speaks ACP over r/w to a single client.
type Server struct {
	enc     *json.Encoder
	dec     *json.Decoder
	backend Backend

	mu      sync.Mutex // serialises writes + guards cancels
	cancels map[string]context.CancelFunc
}

func NewServer(r io.Reader, w io.Writer, b Backend) *Server {
	return &Server{
		enc:     json.NewEncoder(w),
		dec:     json.NewDecoder(r),
		backend: b,
		cancels: map[string]context.CancelFunc{},
	}
}

// Serve runs the read loop until the client disconnects (EOF) or ctx is
// done. Each request is dispatched by method; prompts run in their own
// goroutine so session/cancel can interrupt them.
func (s *Server) Serve(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req request
		if err := s.dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil // client closed — clean shutdown
			}
			return err
		}
		s.dispatch(ctx, req)
	}
}

func (s *Server) dispatch(ctx context.Context, req request) {
	switch req.Method {
	case "initialize":
		var p InitializeParams
		_ = json.Unmarshal(req.Params, &p)
		s.reply(req.ID, s.backend.Initialize(p))
	case "session/new":
		var p NewSessionParams
		_ = json.Unmarshal(req.Params, &p)
		res, err := s.backend.NewSession(p)
		if err != nil {
			s.replyErr(req.ID, -32000, err.Error())
			return
		}
		s.reply(req.ID, res)
	case "session/load":
		// session/load is request/response like session/new, but it also
		// REPLAYS the transcript through session/update notifications
		// before the response — the schema's own words. Ordering is the
		// contract here: all replay notifications are written (under mu,
		// in order) before the result frame, so a client that renders
		// updates as they arrive shows the history and then accepts the
		// loaded session. Running it inline in the read loop is correct
		// because load is not interruptible and holds no lock the prompt
		// path needs; a prompt arriving later is serialised behind it.
		//
		// Loading WHILE a turn runs is rejected (same rule and same race
		// argument as set_config_option): load swaps the agent's whole
		// history via Reset, which would yank the floor out from under the
		// in-flight turn.
		s.mu.Lock()
		busy := len(s.cancels) > 0
		s.mu.Unlock()
		if busy {
			s.replyErr(req.ID, -32000, "session/load rejected: a prompt is in flight (load between turns)")
			return
		}
		var p LoadSessionParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.replyErr(req.ID, -32602, "bad load params: "+err.Error())
			return
		}
		send := func(u SessionUpdate) { s.notify("session/update", u) }
		res, err := s.backend.LoadSession(p, send)
		if err != nil {
			// -32000 (server error) rather than -32601: the method IS
			// handled; the requested session is what failed. Clients
			// distinguish "capability missing" from "load refused".
			s.replyErr(req.ID, -32000, err.Error())
			return
		}
		s.reply(req.ID, res)
	case "session/prompt":
		// Parse + cancel registration happen SYNCHRONOUSLY here, before
		// the goroutine is spawned. That ordering is what makes the
		// set_config_option lane-swap below race-free: the read loop is
		// serial, so by the time a later dispatch runs, this prompt is
		// either visible in s.cancels (swap rejected) or its goroutine
		// was created after the swap completed — and the `go` statement
		// is a happens-before edge, so the turn is guaranteed to see the
		// swapped lane state. Registration used to live inside
		// handlePrompt; moving it here closed the spawn-to-register
		// window (feature-paseo-provider.md C).
		var p PromptParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.replyErr(req.ID, -32602, "bad prompt params: "+err.Error())
			return
		}
		pctx, cancel := context.WithCancel(ctx)
		s.mu.Lock()
		s.cancels[p.SessionID] = cancel
		s.mu.Unlock()
		go s.handlePrompt(pctx, req, p, cancel)
	case "session/set_config_option":
		var p SetConfigOptionParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			// Only the select variant is served (see SetConfigOptionParams)
			// — a boolean payload (type:"boolean", value:true) fails here,
			// loudly, instead of being half-applied.
			s.replyErr(req.ID, -32602, "bad config option params: "+err.Error())
			return
		}
		// Model/provider swaps are between turns only: any prompt in
		// flight (its cancel is registered) rejects the switch. The check
		// is under mu for the same race argument as above — a prompt
		// registered after this point was spawned after dispatch returns,
		// hence sees the post-swap state by construction.
		s.mu.Lock()
		busy := len(s.cancels) > 0
		s.mu.Unlock()
		if busy {
			s.replyErr(req.ID, -32000, "config change rejected: a prompt is in flight (switch models between turns)")
			return
		}
		res, err := s.backend.SetConfigOption(p)
		if err != nil {
			s.replyErr(req.ID, -32000, err.Error())
			return
		}
		s.reply(req.ID, res)
	case "session/cancel":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(req.Params, &p)
		s.cancelSession(p.SessionID)
	default:
		// Unknown method: error if it's a request, ignore if a notification.
		if req.ID != nil {
			s.replyErr(req.ID, -32601, "method not handled: "+req.Method)
		}
	}
}

// handlePrompt is the prompt goroutine body. Params were parsed and the
// cancel registered synchronously in dispatch (see the race note there);
// this func owns the turn itself plus deregistration.
func (s *Server) handlePrompt(ctx context.Context, req request, p PromptParams, cancel context.CancelFunc) {
	defer func() {
		s.mu.Lock()
		delete(s.cancels, p.SessionID)
		s.mu.Unlock()
		cancel()
	}()

	send := func(u SessionUpdate) { s.notify("session/update", u) }
	res, err := s.backend.Prompt(ctx, p, send)
	if err != nil {
		if ctx.Err() != nil {
			s.reply(req.ID, PromptResult{StopReason: "cancelled"})
			return
		}
		s.replyErr(req.ID, -32000, err.Error())
		return
	}
	s.reply(req.ID, res)
}

func (s *Server) cancelSession(id string) {
	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// reply / replyErr / notify all write under mu so an async prompt's
// notifications never interleave with another message mid-encode.
func (s *Server) reply(id *json.RawMessage, result any) {
	if id == nil {
		return // can't reply to a notification
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Result  any              `json:"result"`
	}{"2.0", id, result})
}

func (s *Server) replyErr(id *json.RawMessage, code int, msg string) {
	if id == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      *json.RawMessage `json:"id"`
		Error   rpcError         `json:"error"`
	}{"2.0", id, rpcError{Code: code, Message: msg}})
}

func (s *Server) notify(method string, params any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params})
}

// PromptText joins the text content blocks of a prompt — the bridge from
// ACP's structured prompt to seek's text-only Agent.Prompt.
func (p PromptParams) PromptText() string {
	var b []byte
	for i, c := range p.Prompt {
		if c.Type == "text" && c.Text != "" {
			if i > 0 && len(b) > 0 {
				b = append(b, '\n')
			}
			b = append(b, c.Text...)
		}
	}
	return string(b)
}
