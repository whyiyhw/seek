// Package methods hosts the asset-plane RPC method implementations
// (feature-web-frontend §3.3: method code is transport-agnostic and
// lives here; each transport hangs it off its own registry — the serve
// process's POST /rpc today, the stdio Server when the IDE-line
// refactor adopts internal/rpc.Registry). The W4.2–W4.6 sets are
// read-only; the write plane opened with approval/decide (c_n37tnr,
// frontend-native) and session/rename|archive (W5 c_rqcuaa, the
// serve-owned presentation state in internal/sessmeta — never the
// child-owned session JSONL, inspect-rpc §2's asset-plane concern
// does not apply to serve-side UI facts).
//
// W4.2 delivered the session set; W4.3 the memory/project/stats sets
// — same shape throughout (one type per family, RegisterAll)
package methods

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/sessmeta"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ---------- the session method set (W4.2 + W5 rename/archive) ----------

// Sessions is the session/list|get|messages family over one sessions
// store, plus the W5 rename|archive write pair over a sessmeta store.
type Sessions struct {
	resolve func() (*session.Store, error)

	once  sync.Once
	store *session.Store
	err   error

	// metaResolve serves rename/archive and list's override lookup.
	// Nil (the NewSessions seam) = no meta attached: reads stay at
	// W4.2 behaviour byte-for-byte, the write pair answers -32603 —
	// embedders that only browse keep hermetic constructors.
	metaResolve func() (*sessmeta.Store, error)

	metaOnce sync.Once
	meta     *sessmeta.Store
	metaErr  error
}

// NewSessions binds the family to an already-resolved store (tests,
// embedders with their own root) with NO meta store — the read-only
// W4.2 shape.
func NewSessions(store *session.Store) *Sessions {
	return &Sessions{resolve: func() (*session.Store, error) { return store, nil }}
}

// NewSessionsWithMeta adds the rename|archive pair: store for the
// session-existence checks, meta for the serve-owned overrides.
func NewSessionsWithMeta(store *session.Store, meta *sessmeta.Store) *Sessions {
	s := NewSessions(store)
	s.metaResolve = func() (*sessmeta.Store, error) { return meta, nil }
	return s
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
	return &Sessions{
		resolve:     session.NewStore,
		metaResolve: sessmeta.Open, // same SEEK_HOME ladder as the store
	}
}

func (s *Sessions) open() (*session.Store, error) {
	s.once.Do(func() { s.store, s.err = s.resolve() })
	return s.store, s.err
}

// openMeta resolves the meta store for the write pair. Sticky like
// open(): a resolution failure surfaces on every rename/archive.
func (s *Sessions) openMeta() (*sessmeta.Store, error) {
	if s.metaResolve == nil {
		return nil, rpc.Errorf(rpc.CodeInternalError, "session meta store not wired (read-only family)")
	}
	s.metaOnce.Do(func() { s.meta, s.metaErr = s.metaResolve() })
	return s.meta, s.metaErr
}

// metaOverrides is list's read-side view: the map when the meta store
// is wired and healthy, nil otherwise. A browse view degrades to the
// derived W4.2 list rather than dying because a side file is
// unreadable (sessmeta's own corruption posture, read leg).
func (s *Sessions) metaOverrides() map[string]sessmeta.Entry {
	if s.metaResolve == nil {
		return nil
	}
	meta, err := s.openMeta()
	if err != nil {
		return nil
	}
	m, err := meta.All()
	if err != nil {
		return nil
	}
	return m
}

// RegisterAll wires the five methods into r (the §3.2 phase
// contract: register during construction, dispatch after).
func (s *Sessions) RegisterAll(r *rpc.Registry) {
	r.Register("session/list", s.list)
	r.Register("session/get", s.get)
	r.Register("session/messages", s.messages)
	r.Register("session/rename", s.rename)
	r.Register("session/archive", s.archive)
}

// session/list — every session in the store, newest first (the
// store's List order). include_subagents (inspect-rpc §3.1 sketch) is
// NOT a param here: subagent transcripts live in the project tree
// (~/.seek/projects/<pid>/sessions/...), not the flat store, and their
// lane is W3.1's GET /subagents + M12.1's subagent/*. Params decoding
// is lenient below, so the flag arriving today is a no-op, not a
// -32602. W5 c_rqcuaa adds include_archived (default false — archived
// sessions are a filtered-out list bit, never deleted files) and the
// per-summary archived flag; renames override the derived title.

type sessionListParams struct {
	IncludeArchived bool `json:"include_archived,omitempty"`
}

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
	Archived  bool      `json:"archived,omitempty"` // W5: only true rows carry it
}

type sessionListResult struct {
	Sessions []sessionSummary `json:"sessions"`
}

func (s *Sessions) list(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	meta := s.metaOverrides()
	// Per-file load errors are deliberately dropped, matching the
	// stdio list and ListProject's rule: one corrupt header must not
	// hide the rest of the store from a browse view.
	infos, _, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make([]sessionSummary, 0, len(infos))
	for _, info := range infos {
		title := firstUserTitle(store.Dir(), info.ID)
		archived := false
		if e, ok := meta[info.ID]; ok {
			if e.Title != "" {
				title = e.Title
			}
			archived = e.Archived
		}
		if archived && !p.IncludeArchived {
			continue
		}
		out = append(out, sessionSummary{
			SID:       info.ID,
			Title:     title,
			StartedAt: info.CreatedAt,
			LastAt:    info.UpdatedAt,
			Turns:     info.Turns,
			ToolCalls: info.ToolCalls,
			Model:     info.Model,
			ParentID:  info.ParentID,
			CWD:       info.CWD,
			Archived:  archived,
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

// firstUserTitle delegates to the store package's derivation (extracted
// there 2026-11-21 when ACP's session/list needed the same label):
// titles are a transcript fact, and two list surfaces must not grow two
// title styles.
func firstUserTitle(dir, id string) string {
	return session.FirstUserTitle(dir, id)
}
