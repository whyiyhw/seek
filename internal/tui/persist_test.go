package tui

import (
	"os"
	"testing"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
)

// Tests in this file pin persistSession's nil-guard contract — review
// finding C3 (Tracker nil-deref) and A3 (Agent.Messages race). The
// fixes were applied before this conversation; without these tests
// nothing prevents a regression from re-introducing either.

// persistSessionTestFixture wires a real session.Store rooted at a
// tempdir + the supplied AgentClient/Tracker, so each test exercises
// the actual code path (no Store mocking) while staying hermetic.
func persistSessionTestFixture(t *testing.T) *session.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", dir)
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	return store
}

// TestPersistSession_NilTrackerDoesNotPanic pins review finding C3.
// The guard at line 416 checks Session/Store/Agent but NOT Tracker.
// Before the fix at line 422, `m.opts.Tracker.Cumulative()` would
// nil-deref when an external caller built Options without a Tracker.
// Production cmd/seek wires one, but the guard is defensive — and
// the alternative is "test harness sets it up by accident" which is
// exactly the bug the next test breaks.
func TestPersistSession_NilTrackerDoesNotPanic(t *testing.T) {
	store := persistSessionTestFixture(t)
	fa := newFakeAgent()

	m := testModel().WithAgent(fa).WithStore(store).WithCustomState(func(m *Model) {
		m.opts.Session = session.New("deepseek-v4-flash", t.TempDir(), "", false, false)
		m.opts.Tracker = nil // the bug: previously panicked here
	}).BuildPtr()

	// Must not panic. The Usage field stays at its zero value when
	// Tracker is nil — saved sessions just don't carry usage stats,
	// which matches the --no-save / ephemeral-run posture.
	m.persistSession()

	// And the session WAS written despite the nil tracker — counters
	// + messages still get persisted.
	loaded, err := store.Load(m.opts.Session.ID)
	if err != nil {
		t.Fatalf("Load after persistSession: %v", err)
	}
	if loaded.ID != m.opts.Session.ID {
		t.Errorf("loaded session ID = %q, want %q", loaded.ID, m.opts.Session.ID)
	}
}

// TestPersistSession_GuardsNilDependencies covers the three other
// nil-guards (Session / Store / Agent). Any of them missing should
// short-circuit to a clean no-op, NOT panic. Bug surface = the dyad
// of fresh-session creation: cmd/seek wires all four in production,
// but tests / SDK consumers / --no-save paths may not.
func TestPersistSession_GuardsNilDependencies(t *testing.T) {
	// Sub-tests can't t.Parallel here because each one calls t.Setenv,
	// which Go's testing package forbids in parallel tests. Cheap
	// enough to run serially.
	cases := []struct {
		name  string
		patch func(*Model)
	}{
		{"nil Session", func(m *Model) { m.opts.Session = nil }},
		{"nil Store", func(m *Model) { m.opts.Store = nil }},
		{"nil Agent", func(m *Model) { m.opts.Agent = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Construct a fully-wired model, then null out the field
			// the test names. Mirrors the bug profile: it's the
			// missing-field case that has to be the safe one.
			dir := t.TempDir()
			t.Setenv("SEEK_SESSIONS_DIR", dir)
			store, err := session.NewStore()
			if err != nil {
				t.Fatalf("session.NewStore: %v", err)
			}
			m := testModel().
				WithAgent(newFakeAgent()).
				WithStore(store).
				WithCustomState(func(m *Model) {
					m.opts.Session = session.New("deepseek-v4-flash", t.TempDir(), "", false, false)
					m.opts.Tracker = cache.New()
					tc.patch(m)
				}).BuildPtr()

			// Must not panic.
			m.persistSession()
		})
	}
}

// TestPersistSession_HappyPathRoundtrip is the positive control: with
// every dependency wired the session should land on disk and the
// counters/messages should round-trip. Pins the "save did work, this
// isn't a silent no-op" half of the nil-guard contract.
func TestPersistSession_HappyPathRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", dir)
	store, err := session.NewStore()
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}

	fa := newFakeAgent()
	sess := session.New("deepseek-v4-flash", t.TempDir(), "", false, false)

	m := testModel().WithAgent(fa).WithStore(store).WithCustomState(func(m *Model) {
		m.opts.Session = sess
		m.turns = 7
		m.toolCalls = 12
	}).BuildPtr()

	m.persistSession()
	// File presence check: Save writes <id>.jsonl atomically; if it
	// didn't, the round-trip below would already fail, but a direct
	// stat call gives a clearer failure message.
	if _, err := os.Stat(dir + "/" + sess.ID + ".jsonl"); err != nil {
		t.Fatalf("session file not on disk after persistSession: %v", err)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Turns != 7 || loaded.ToolCalls != 12 {
		t.Errorf("counters did not round-trip: turns=%d toolCalls=%d, want 7/12",
			loaded.Turns, loaded.ToolCalls)
	}
}
