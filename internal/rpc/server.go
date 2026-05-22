// Package rpc implements a JSON-RPC 2.0 server over stdio.
//
// seek --rpc starts this server so host processes (IDE extensions, scripts)
// can drive the agent programmatically without a terminal.
//
// Protocol: JSON-RPC 2.0 (https://www.jsonrpc.org/specification)
//
// # Methods (host → seek)
//
//	agent/prompt    run a prompt; streams events back as notifications
//	agent/info      return server capabilities and current state
//	session/list    list saved sessions
//
// # Notifications (seek → host, during agent/prompt)
//
//	agent/event     one per agent event; params shape is eventLine
//
// # Example session
//
//	→ {"jsonrpc":"2.0","id":1,"method":"agent/info"}
//	← {"jsonrpc":"2.0","id":1,"result":{"version":"0.9.0","model":"deepseek-chat","yolo":false}}
//
//	→ {"jsonrpc":"2.0","id":2,"method":"agent/prompt","params":{"text":"hello"}}
//	← {"jsonrpc":"2.0","method":"agent/event","params":{"type":"turn_start","index":0}}
//	← {"jsonrpc":"2.0","method":"agent/event","params":{"type":"text_delta","delta":"Hi!"}}
//	← {"jsonrpc":"2.0","method":"agent/event","params":{"type":"turn_end","index":0,...}}
//	← {"jsonrpc":"2.0","id":2,"result":{"turns":1,"tool_calls":0,...}}
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
)

// Server is a JSON-RPC 2.0 server over a pair of stdio streams.
type Server struct {
	ag      *agent.Agent
	tracker *cache.Tracker
	store   *session.Store
	sess    *session.Session
	model   string
	yolo    bool

	mu  sync.Mutex
	enc *json.Encoder
}

// New creates a new RPC server. store and sess may be nil when session
// persistence is disabled (--no-save).
func New(ag *agent.Agent, tracker *cache.Tracker, store *session.Store, sess *session.Session, model string, yolo bool) *Server {
	return &Server{
		ag:      ag,
		tracker: tracker,
		store:   store,
		sess:    sess,
		model:   model,
		yolo:    yolo,
	}
}

// Serve reads JSON-RPC 2.0 request objects (one per line) from in and
// writes response/notification objects to out. It blocks until ctx is
// cancelled or in reaches EOF.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.enc = json.NewEncoder(out)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4<<20), 4<<20) // 4 MiB max line

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return nil // clean EOF
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.sendError(0, codeParseError, "parse error: "+err.Error())
			continue
		}
		if req.JSONRPC != "2.0" {
			s.sendError(req.ID, codeInvalidRequest, `jsonrpc must be "2.0"`)
			continue
		}
		s.dispatch(ctx, req)
	}
}

// dispatch routes a parsed request to the appropriate handler.
func (s *Server) dispatch(ctx context.Context, req rpcRequest) {
	switch req.Method {
	case "agent/prompt":
		s.handlePrompt(ctx, req)
	case "agent/info":
		s.handleInfo(req)
	case "session/list":
		s.handleSessionList(req)
	default:
		s.sendError(req.ID, codeMethodNotFound, "unknown method: "+req.Method)
	}
}

// ---------- method: agent/prompt ----------

type promptParams struct {
	Text string `json:"text"`
}

type promptResult struct {
	SessionID        string `json:"session_id,omitempty"`
	Turns            int    `json:"turns"`
	ToolCalls        int    `json:"tool_calls"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	CacheHitTokens   int    `json:"cache_hit_tokens"`
}

func (s *Server) handlePrompt(ctx context.Context, req rpcRequest) {
	var p promptParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			s.sendError(req.ID, codeInvalidParams, "invalid params: "+err.Error())
			return
		}
	}
	if p.Text == "" {
		s.sendError(req.ID, codeInvalidParams, "text is required")
		return
	}

	var turns, toolCalls int

	for ev := range s.ag.Prompt(ctx, p.Text) {
		switch e := ev.(type) {
		case agent.TurnStart:
			s.sendNotify("agent/event", eventLine{Type: "turn_start", Index: e.Index})

		case agent.MessageDelta:
			t := "text_delta"
			if e.Reasoning {
				t = "reasoning_delta"
			}
			s.sendNotify("agent/event", eventLine{Type: t, Delta: e.Delta})

		case agent.ToolExecStart:
			s.sendNotify("agent/event", eventLine{Type: "tool_start", ID: e.CallID, Name: e.Name, Args: e.Args})

		case agent.ToolDelta:
			s.sendNotify("agent/event", eventLine{
				Type:      "tool_delta",
				ID:        e.CallID,
				Name:      e.Name,
				Delta:     e.Delta,
				Reasoning: e.Reasoning,
			})

		case agent.ToolExecEnd:
			line := eventLine{Type: "tool_end", ID: e.CallID, Name: e.Name}
			if e.Err != nil {
				line.Error = e.Err.Error()
			} else {
				line.Result = e.Result
				line.Bytes = len(e.Result)
			}
			s.sendNotify("agent/event", line)

		case agent.TurnEnd:
			s.tracker.Record(e.Usage)
			turns++
			toolCalls += e.ToolCalls
			s.sendNotify("agent/event", eventLine{
				Type:             "turn_end",
				Index:            e.Index,
				PromptTokens:     e.Usage.PromptTokens,
				CompletionTokens: e.Usage.CompletionTokens,
				CacheHitTokens:   e.Usage.PromptCacheHitTokens,
				ToolCalls:        e.ToolCalls,
			})
			s.saveSession(turns, toolCalls)

		case agent.ErrorEvent:
			s.sendError(req.ID, codeInternalError, e.Err.Error())
			return
		}
	}

	c := s.tracker.Cumulative()
	result := promptResult{
		Turns:            turns,
		ToolCalls:        toolCalls,
		PromptTokens:     c.PromptTokens,
		CompletionTokens: c.CompletionTokens,
		CacheHitTokens:   c.PromptCacheHitTokens,
	}
	if s.sess != nil {
		result.SessionID = s.sess.ID
	}
	s.sendResult(req.ID, result)
}

// ---------- method: agent/info ----------

type infoResult struct {
	Version   string `json:"version"`
	Model     string `json:"model"`
	Yolo      bool   `json:"yolo"`
	SessionID string `json:"session_id,omitempty"`
}

func (s *Server) handleInfo(req rpcRequest) {
	r := infoResult{Version: "0.9.0", Model: s.model, Yolo: s.yolo}
	if s.sess != nil {
		r.SessionID = s.sess.ID
	}
	s.sendResult(req.ID, r)
}

// ---------- method: session/list ----------

type sessionListResult struct {
	Sessions []sessionEntry `json:"sessions"`
}

type sessionEntry struct {
	ID        string    `json:"id"`
	Model     string    `json:"model"`
	UpdatedAt time.Time `json:"updated_at"`
	Turns     int       `json:"turns"`
	ToolCalls int       `json:"tool_calls"`
	ParentID  string    `json:"parent_id,omitempty"`
}

func (s *Server) handleSessionList(req rpcRequest) {
	if s.store == nil {
		s.sendResult(req.ID, sessionListResult{Sessions: []sessionEntry{}})
		return
	}
	infos, _, err := s.store.List()
	if err != nil {
		s.sendError(req.ID, codeInternalError, err.Error())
		return
	}
	entries := make([]sessionEntry, len(infos))
	for i, info := range infos {
		entries[i] = sessionEntry{
			ID:        info.ID,
			Model:     info.Model,
			UpdatedAt: info.UpdatedAt,
			Turns:     info.Turns,
			ToolCalls: info.ToolCalls,
			ParentID:  info.ParentID,
		}
	}
	s.sendResult(req.ID, sessionListResult{Sessions: entries})
}

// ---------- session saving ----------

func (s *Server) saveSession(turns, toolCalls int) {
	if s.sess == nil || s.store == nil {
		return
	}
	s.sess.Messages = s.ag.Messages()
	s.sess.Turns = turns
	s.sess.ToolCalls = toolCalls
	s.sess.Usage = s.tracker.Cumulative()
	s.sess.Model = s.model
	s.sess.Yolo = s.yolo
	_ = s.store.Save(s.sess)
}

// ---------- JSON-RPC 2.0 wire types ----------

// Standard JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcNotify struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// eventLine is the params shape for agent/event notifications.
// It mirrors the jsonLine type in cmd/seek for symmetry.
type eventLine struct {
	Type string `json:"type"`
	// turn_start / turn_end
	Index int `json:"index,omitempty"`
	// text_delta / reasoning_delta / tool_delta
	Delta     string `json:"delta,omitempty"`
	Reasoning bool   `json:"reasoning,omitempty"`
	// tool_start / tool_delta / tool_end
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Bytes  int    `json:"bytes,omitempty"`
	Error  string `json:"error,omitempty"`
	// turn_end token accounting
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	CacheHitTokens   int `json:"cache_hit_tokens,omitempty"`
	ToolCalls        int `json:"tool_calls,omitempty"`
}

// ---------- send helpers ----------

func (s *Server) sendResult(id int64, v any) {
	raw, _ := json.Marshal(v)
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: json.RawMessage(raw)})
}

func (s *Server) sendError(id int64, code int, msg string) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}})
}

func (s *Server) sendNotify(method string, params any) {
	s.write(rpcNotify{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *Server) write(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.enc.Encode(v)
}
