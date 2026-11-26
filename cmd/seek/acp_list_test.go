package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// The session/list acceptance fixtures: two STORE STATES (has history /
// empty store) against the real transcript store, plus the persistence-
// off posture. Together with internal/acp's dispatch tests these pin
// the whole path: advertisement → dispatch → store scan → wire shape.

// TestACPBackend_ListSessions_HasHistory is the primary fixture: two
// stored transcripts come back newest-first, each with its id, the cwd
// recorded in the header, a title derived from the first user message,
// and an RFC3339 mtime — and every listed id is loadable (same store
// session/load reads).
func TestACPBackend_ListSessions_HasHistory(t *testing.T) {
	b, store := loadTestBackend(t)
	cwd := mustGetwd(t)

	older := session.New(deepseek.ModelV41Flash, cwd, "sys", false, false)
	older.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "older first question"}}
	if err := store.Save(older); err != nil {
		t.Fatal(err)
	}
	newer := session.New(deepseek.ModelV41Flash, cwd, "sys", false, false)
	newer.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "newer question\nsecond line ignored"},
		{Role: deepseek.RoleAssistant, Content: "answer"},
	}
	if err := store.Save(newer); err != nil {
		t.Fatal(err)
	}
	// Save→Touch stamps each write with now; the second save is
	// strictly later (ns clock), so newest-first is deterministic.

	res, err := b.ListSessions(acp.ListSessionsParams{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want the two stored transcripts", res.Sessions)
	}
	// Newest first (Store.List order).
	first, second := res.Sessions[0], res.Sessions[1]
	if first.SessionID != newer.ID || second.SessionID != older.ID {
		t.Fatalf("order = [%s %s], want newest-first [%s %s]", first.SessionID, second.SessionID, newer.ID, older.ID)
	}
	if first.Title != "newer question" {
		t.Fatalf("title = %q, want first line of the first user message", first.Title)
	}
	if second.Title != "older first question" {
		t.Fatalf("title = %q", second.Title)
	}
	if first.Cwd != cwd {
		t.Fatalf("cwd = %q, want the transcript's recorded cwd %q", first.Cwd, cwd)
	}
	// mtime: RFC3339-parseable and equal to the header's UpdatedAt
	// (RFC3339 carries second precision; Touch's ns field floors).
	gotTime, err := time.Parse(time.RFC3339, first.UpdatedAt)
	if err != nil {
		t.Fatalf("updatedAt %q is not RFC3339: %v", first.UpdatedAt, err)
	}
	if !gotTime.Equal(newer.UpdatedAt.Truncate(time.Second)) {
		t.Fatalf("updatedAt = %v, want %v", gotTime, newer.UpdatedAt)
	}
	// 同源: every listed id loads through the same store session/load reads.
	for _, s := range res.Sessions {
		if _, err := store.Load(s.SessionID); err != nil {
			t.Fatalf("listed id %s not loadable: %v", s.SessionID, err)
		}
	}
	// The wire shape: sessions serialises as an array of objects with
	// the schema's camelCase field names.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"sessionId"`, `"cwd"`, `"title"`, `"updatedAt"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("wire result missing %s: %s", key, raw)
		}
	}
}

// TestACPBackend_ListSessions_HeaderTitle: the generated header title
// wins over the derived first user line on the ACP lane too (no sessmeta
// override there — renames are a serve-side UI fact).
func TestACPBackend_ListSessions_HeaderTitle(t *testing.T) {
	b, store := loadTestBackend(t)
	cwd := mustGetwd(t)
	sess := session.New(deepseek.ModelV41Flash, cwd, "sys", false, false)
	sess.Title = "生成的 ACP 标题"
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "原始首行"}}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	res, err := b.ListSessions(acp.ListSessionsParams{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want 1", res.Sessions)
	}
	if got := res.Sessions[0].Title; got != "生成的 ACP 标题" {
		t.Fatalf("title = %q, want generated header title", got)
	}
}

// TestACPBackend_ListSessions_CwdFilter: with p.Cwd set, only
// transcripts recorded for that directory are listed; a cwd nothing
// matches yields the EMPTY ARRAY (not null, not an error).
func TestACPBackend_ListSessions_CwdFilter(t *testing.T) {
	b, store := loadTestBackend(t)
	cwd := mustGetwd(t)
	sess := session.New(deepseek.ModelV41Flash, cwd, "sys", false, false)
	sess.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "in project"}}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	res, err := b.ListSessions(acp.ListSessionsParams{Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != 1 || res.Sessions[0].SessionID != sess.ID {
		t.Fatalf("filtered listing = %+v, want exactly %s", res.Sessions, sess.ID)
	}
	res, err = b.ListSessions(acp.ListSessionsParams{Cwd: "/definitely/not/it"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions == nil || len(res.Sessions) != 0 {
		t.Fatalf("no-match listing must be an empty array, got %+v", res.Sessions)
	}
}

// TestACPBackend_ListSessions_EmptyStore is the second fixture state:
// a fresh store with zero transcripts answers an empty listing.
func TestACPBackend_ListSessions_EmptyStore(t *testing.T) {
	b, _ := loadTestBackend(t)
	res, err := b.ListSessions(acp.ListSessionsParams{})
	if err != nil {
		t.Fatalf("empty store must list cleanly: %v", err)
	}
	if res.Sessions == nil || len(res.Sessions) != 0 {
		t.Fatalf("empty store listing = %+v, want []", res.Sessions)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"sessions":[]}` {
		t.Fatalf("wire shape = %s, want {\"sessions\":[]}", raw)
	}
}

// TestACPBackend_ListSessions_PersistenceOff: under --no-save there is
// no store — list must say so rather than pretending, and the
// capability must not be advertised.
func TestACPBackend_ListSessions_PersistenceOff(t *testing.T) {
	b, _ := loadTestBackend(t)
	b.store = nil
	if _, err := b.ListSessions(acp.ListSessionsParams{}); err == nil {
		t.Fatal("list with persistence off must error")
	}
	caps := b.Initialize(acp.InitializeParams{ProtocolVersion: 1}).AgentCapabilities
	if caps.SessionCapabilities != nil {
		t.Fatalf("sessionCapabilities must not be advertised without a store: %+v", caps)
	}
}

// TestACPBackend_Initialize_AdvertisesList: with a store present the
// capability object is on — the object-presence shape the ACP schema
// defines (see internal/acp's wire-shape test for the byte-level pin).
func TestACPBackend_Initialize_AdvertisesList(t *testing.T) {
	b, _ := loadTestBackend(t)
	caps := b.Initialize(acp.InitializeParams{ProtocolVersion: 1}).AgentCapabilities
	if caps.SessionCapabilities == nil || caps.SessionCapabilities.List == nil {
		t.Fatalf("sessionCapabilities.list must be advertised: %+v", caps)
	}
	if !caps.LoadSession {
		t.Fatal("loadSession advertisement regressed")
	}
}
