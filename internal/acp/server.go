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
	// attachment; seek OCRs those to text (M-P.5). Omitted when zero so a
	// text-only agent stays silent.
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

type NewSessionResult struct {
	SessionID string `json:"sessionId"`
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
	StopReason string `json:"stopReason"` // "end_turn" | "cancelled" | "refusal"
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
	// Prompt runs one turn. It streams progress by calling send (each call
	// becomes a session/update notification) and returns when the turn
	// ends or ctx is cancelled.
	Prompt(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error)
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
	case "session/prompt":
		go s.handlePrompt(ctx, req)
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

func (s *Server) handlePrompt(parent context.Context, req request) {
	var p PromptParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyErr(req.ID, -32602, "bad prompt params: "+err.Error())
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancels[p.SessionID] = cancel
	s.mu.Unlock()
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
