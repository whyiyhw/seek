package methods

// session/rename + session/archive — the W5 c_rqcuaa write pair. Both
// mutate ONLY the serve-owned presentation state (internal/sessmeta:
// ~/.seek/serve/sessions-meta.json + its audit JSONL); the session
// JSONL the child seek process owns is never touched (the §13.1
// transcript-ownership pin). Existence is checked against the real
// store so a stale UI row can't mint overrides for ghosts.

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
)

// validateTitle applies the sidebar's one-line title shape to a
// user-supplied rename: first line only (multi-line pastes flatten,
// same as the derived first-user-message label), trimmed, 1..80
// runes. Explicit -32602 beats silent truncation for a typed value.
func validateTitle(title string) (string, error) {
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	if i := strings.IndexByte(title, '\r'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", rpc.Errorf(rpc.CodeInvalidParams, "title is required")
	}
	if utf8.RuneCountInString(title) > session.TitleMaxRunes {
		return "", rpc.Errorf(rpc.CodeInvalidParams, "title: %d runes, max %d", utf8.RuneCountInString(title), session.TitleMaxRunes)
	}
	return title, nil
}

// session/rename — params {sid, title}. Setting the title to a
// first-line-empty value after trim is invalid; an explicit "" is
// reserved for withdrawing the override… except the wire keeps that
// unreachable on purpose: the UI never sends it and a "" would read
// as a mistake, not intent. Concurrency: sessmeta serialises
// read-modify-write under one mutex, last rename per sid wins, no
// torn merges (c_rqcuaa acceptance ①).

type sessionRenameParams struct {
	SID   string `json:"sid"`
	Title string `json:"title"`
}

type sessionRenameResult struct {
	SID   string `json:"sid"`
	Title string `json:"title"` // the stored override (post-flatten)
}

func (s *Sessions) rename(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionRenameParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if err := checkSID(p.SID); err != nil {
		return nil, err
	}
	title, err := validateTitle(p.Title)
	if err != nil {
		return nil, err
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	if _, err := store.LoadHeader(p.SID); err != nil {
		return nil, mapReadError(p.SID, err) // -32001 for ghosts
	}
	meta, err := s.openMeta()
	if err != nil {
		return nil, err
	}
	e, err := meta.SetTitle(p.SID, title)
	if err != nil {
		return nil, err
	}
	return sessionRenameResult{SID: p.SID, Title: e.Title}, nil
}

// session/archive — params {sid, archived?} (absent = true; the
// method's own name is the default intent). Idempotent by
// construction: the second archive of an already-archived session
// writes nothing, logs nothing, and answers changed=false
// (c_rqcuaa acceptance ①). Unarchive is archived:false. The list
// filter lives in session/list's include_archived.

type sessionArchiveParams struct {
	SID      string `json:"sid"`
	Archived *bool  `json:"archived,omitempty"` // pointer: explicit false must survive
}

type sessionArchiveResult struct {
	SID      string `json:"sid"`
	Archived bool   `json:"archived"`
	Changed  bool   `json:"changed"`
}

func (s *Sessions) archive(ctx context.Context, params json.RawMessage) (any, error) {
	var p sessionArchiveParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if err := checkSID(p.SID); err != nil {
		return nil, err
	}
	to := true
	if p.Archived != nil {
		to = *p.Archived
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	if _, err := store.LoadHeader(p.SID); err != nil {
		return nil, mapReadError(p.SID, err)
	}
	meta, err := s.openMeta()
	if err != nil {
		return nil, err
	}
	e, changed, err := meta.SetArchived(p.SID, to)
	if err != nil {
		return nil, err
	}
	return sessionArchiveResult{SID: p.SID, Archived: e.Archived, Changed: changed}, nil
}
