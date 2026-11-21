// Package methods hosts the asset-plane RPC method implementations
// (feature-web-frontend §3.3: method code is transport-agnostic and
// lives here; each transport hangs it off its own registry — the serve
// process's POST /rpc today, the stdio Server when the IDE-line
// refactor adopts internal/rpc.Registry). All methods are read-only:
// the write plane stays closed (inspect-rpc §2).
//
// W4.2 delivered the session set; W4.3 the memory/project/stats sets
// — same shape throughout (one type per family, RegisterAll).
package methods

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ---------- the session method set (W4.2) ----------

// Sessions is the session/list|get|messages family over one sessions
// store.
type Sessions struct {
	resolve func() (*session.Store, error)

	once  sync.Once
	store *session.Store
	err   error
}

// NewSessions binds the family to an already-resolved store (tests,
// embedders with their own root).
func NewSessions(store *session.Store) *Sessions {
	return &Sessions{resolve: func() (*session.Store, error) { return store, nil }}
}

// NewSessionsFromEnv defers store resolution to the first dispatch via
// session.NewStore (the SEEK_SESSIONS_DIR / SEEK_HOME / ~/.seek ladder
// — the same env the serve process hands its seek subprocesses, so
// both ends of the two-process read share one root). Deferral keeps
// the wiring phase free of filesystem side effects: constructing the
// frontend must not create or touch the sessions dir (buildFrontends
// runs in tests). A resolution failure is sticky and surfaces as
// -32603 on every call — the chat plane must not die because the
// sessions dir is unreadable.
func NewSessionsFromEnv() *Sessions {
	return &Sessions{resolve: session.NewStore}
}

func (s *Sessions) open() (*session.Store, error) {
	s.once.Do(func() { s.store, s.err = s.resolve() })
	return s.store, s.err
}

// RegisterAll wires the three methods into r (the §3.2 phase
// contract: register during construction, dispatch after).
func (s *Sessions) RegisterAll(r *rpc.Registry) {
	r.Register("session/list", s.list)
	r.Register("session/get", s.get)
	r.Register("session/messages", s.messages)
}

// session/list — every session in the store, newest first (the
// store's List order). include_subagents (inspect-rpc §3.1 sketch) is
// NOT a param here: subagent transcripts live in the project tree
// (~/.seek/projects/<pid>/sessions/...), not the flat store, and their
// lane is W3.1's GET /subagents + M12.1's subagent/*. Params decoding
// is lenient below, so the flag arriving today is a no-op, not a
// -32602.

type sessionSummary struct {
	// Field names follow the inspect-rpc §3.1 sketch (sid / title /
	// started_at / last_at / turns / model); tool_calls / parent_id /
	// cwd ride along additively — result-field additions are always
	// contract-OK. Note the stdio session/list ships `id`/`updated_at`
	// (its own switch, untouched per §3.3); this family is the
	// panel-facing shape the IDE line converges on when it adopts the
	// registry.
	SID       string    `json:"sid"`
	Title     string    `json:"title"`
	StartedAt time.Time `json:"started_at"`
	LastAt    time.Time `json:"last_at"`
	Turns     int       `json:"turns"`
	ToolCalls int       `json:"tool_calls"`
	Model     string    `json:"model"`
	ParentID  string    `json:"parent_id,omitempty"`
	CWD       string    `json:"cwd,omitempty"`
	// CostUSD is the header Usage priced server-side at the tier in
	// effect at query time (c_6xktw4: the sidebar's per-session cost
	// column). Same rule as stats/cache — rate cards live only in
	// internal/pricing, the page never prices (§2.5).
	CostUSD float64 `json:"cost_usd"`
}

type sessionListResult struct {
	Sessions []sessionSummary `json:"sessions"`
}

func (s *Sessions) list(ctx context.Context, params json.RawMessage) (any, error) {
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	// Per-file load errors are deliberately dropped, matching the
	// stdio list and ListProject's rule: one corrupt header must not
	// hide the rest of the store from a browse view.
	infos, _, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make([]sessionSummary, 0, len(infos))
	tier := pricing.CurrentTier(time.Now())
	for _, info := range infos {
		out = append(out, sessionSummary{
			SID:       info.ID,
			Title:     firstUserTitle(store.Dir(), info.ID),
			StartedAt: info.CreatedAt,
			LastAt:    info.UpdatedAt,
			Turns:     info.Turns,
			ToolCalls: info.ToolCalls,
			Model:     info.Model,
			ParentID:  info.ParentID,
			CWD:       info.CWD,
			CostUSD:   pricing.Cost(info.Model, tier, info.Usage),
		})
	}
	return sessionListResult{Sessions: out}, nil
}

// session/get — header + counters, no messages (the messages body is
// session/messages' business). Reading only line 1 keeps this cheap on
// long transcripts.

type sessionGetResult struct {
	Header          *session.Session `json:"header"` // Messages always nil
	Turns           int              `json:"turns"`
	CumulativeUsage deepseek.Usage   `json:"cumulative_usage"`
}

func (s *Sessions) get(ctx context.Context, params json.RawMessage) (any, error) {
	var p sidParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if err := checkSID(p.SID); err != nil {
		return nil, err
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	hdr, err := store.LoadHeader(p.SID)
	if err != nil {
		return nil, mapReadError(p.SID, err)
	}
	return sessionGetResult{Header: hdr, Turns: hdr.Turns, CumulativeUsage: hdr.Usage}, nil
}

// session/messages — cursor-paginated message lines, raw bytes from
// the JSONL (unknown fields like predicted_next pass through). The
// cursor is the next message index ("based on line offset", inspect-
// rpc §3.1): stable under appends, opaque enough to change later only
// by mutual agreement of both ends reading this comment.

const (
	messagesDefaultLimit = 50
	messagesMaxLimit     = 500
)

type sessionMessagesParams struct {
	SID    string `json:"sid"`
	Cursor string `json:"cursor,omitempty"`
	Limit  *int   `json:"limit,omitempty"` // pointer: explicit 0 is invalid, not "default"
}

type sessionMessagesResult struct {
	Messages   []json.RawMessage `json:"messages"` // never null: walkable arrays for JS consumers
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (s *Sessions) messages(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionMessagesParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if err := checkSID(p.SID); err != nil {
		return nil, err
	}
	offset := 0
	if p.Cursor != "" {
		n, err := strconv.Atoi(p.Cursor)
		if err != nil || n < 0 {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "cursor %q: want a non-negative message index", p.Cursor)
		}
		offset = n
	}
	limit := messagesDefaultLimit
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > messagesMaxLimit {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "limit %d: want 1..%d", *p.Limit, messagesMaxLimit)
		}
		limit = *p.Limit
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	page, more, err := store.ReadMessagesPage(p.SID, offset, limit)
	if err != nil {
		return nil, mapReadError(p.SID, err)
	}
	if page == nil {
		page = []json.RawMessage{}
	}
	res := sessionMessagesResult{Messages: page}
	if more {
		res.NextCursor = strconv.Itoa(offset + len(page))
	}
	return res, nil
}

// ---------- shared helpers ----------

type sidParams struct {
	SID string `json:"sid"`
}

// decodeParams is the family's params convention: absent params is the
// zero value; a malformed or non-object params is -32602. Unknown
// FIELDS are ignored — the contract allows optional param additions,
// so a client speaking a future flag must not break (§3.2 stability).
func decodeParams(params json.RawMessage, v any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, v); err != nil {
		return rpc.Errorf(rpc.CodeInvalidParams, "invalid params: %v", err)
	}
	return nil
}

// checkSID applies the store's id rule at the RPC boundary: empty or
// path/dot-carrying ids are invalid params (-32602), never store
// errors — mirrors session's own check so a traversal attempt never
// reaches the filesystem.
func checkSID(sid string) error {
	if sid == "" {
		return rpc.Errorf(rpc.CodeInvalidParams, "sid is required")
	}
	if strings.ContainsAny(sid, "/\\.") {
		return rpc.Errorf(rpc.CodeInvalidParams, "invalid sid %q", sid)
	}
	return nil
}

// mapReadError folds store read failures onto the §3.5 model: a
// missing file is session-not-found (-32001, structured data naming
// the sid); anything else stays a plain error and surfaces as -32603
// through the host's errors.As mapping.
func mapReadError(sid string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return &rpc.Error{
			Code:    rpc.CodeSessionNotFound,
			Message: "session " + sid + " not found",
			Data:    map[string]string{"sid": sid},
		}
	}
	return err
}

// ---------- list titling ----------

const (
	titleMaxRunes      = 80
	firstUserScanLines = 32
)

// firstUserTitle flattens a session's first user message into a
// one-line label for session/list (headers carry no title field). The
// scan reads at most firstUserScanLines message lines — real sessions
// open with the user's prompt, and one that does not is not worth a
// whole-file read for a label. Best-effort by design: a missing file,
// a torn tail (concurrent append) or a corrupt line ends the scan
// quietly — a list view must never fail because one file is mid-write.
func firstUserTitle(dir, id string) string {
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 16<<10)
	first := true
	for scanned := 0; ; {
		line, rerr := r.ReadBytes('\n')
		if first {
			first = false
			if rerr != nil {
				return ""
			}
			continue
		}
		trimmed := bytes.TrimSuffix(line, []byte{'\n'})
		if len(trimmed) == 0 {
			if rerr != nil {
				return ""
			}
			continue
		}
		if !json.Valid(trimmed) {
			return "" // torn or corrupt: no label rather than a failed list
		}
		scanned++
		var m struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return ""
		}
		if m.Role == deepseek.RoleUser {
			return oneLineTitle(m.Content)
		}
		if rerr != nil || scanned == firstUserScanLines {
			return ""
		}
	}
}

// oneLineTitle flattens content to its first line, trimmed and capped
// at titleMaxRunes with an ellipsis.
func oneLineTitle(content string) string {
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		content = content[:i]
	}
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) <= titleMaxRunes {
		return content
	}
	runes := []rune(content)
	return string(runes[:titleMaxRunes]) + "…"
}
