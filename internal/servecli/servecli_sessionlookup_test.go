package servecli

// c_7lsngm: the /resume read seam — servecli's adapter from the router's
// LookupSession hook onto the session store. The router tests cover the
// DECISION (bind here, keep project, refuse bad ids); this file covers the
// ADAPTER: it answers the session's recorded cwd, errors on an unknown id,
// and survives concurrent calls (Handle runs one goroutine per inbound).

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestSessionLookupResolvesCWD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "") // isolate from a dev-machine override

	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/some-project"
	fixture := &session.Session{
		SchemaVersion: 3,
		ID:            "20261120-090000-lookuplookup01",
		Model:         "deepseek-chat",
		CWD:           want,
		Messages:      []deepseek.Message{{Role: deepseek.RoleUser, Content: "lookup me"}},
	}
	if err := st.Save(fixture); err != nil {
		t.Fatal(err)
	}

	lookup := sessionLookup()
	got, err := lookup(fixture.ID)
	if err != nil {
		t.Fatalf("lookup(%s): %v", fixture.ID, err)
	}
	if got != want {
		t.Errorf("lookup(%s) = %q, want the session's recorded cwd %q", fixture.ID, got, want)
	}

	// An unknown id is an error, not an empty cwd: the router turns the
	// error into "找不到会话" and leaves the binding alone, while an empty
	// cwd would be read as "keep the current project" (the legacy shape).
	if _, err := lookup("20260101-000000-no-such-session"); err == nil {
		t.Error("unknown session id must error, got nil")
	}
	// Traversal-shaped ids die in the store's own id rule.
	if _, err := lookup("../../etc/passwd"); err == nil || !strings.Contains(err.Error(), "invalid id") {
		t.Errorf("traversal-shaped id = %v, want the store's invalid-id error", err)
	}
}

// TestSessionLookupConcurrent: two conversations can /resume at the same
// time (Serve dispatches each inbound on its own goroutine), so the lazy
// store resolution must be race-free — the -race build is the assertion.
func TestSessionLookupConcurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")

	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "20261120-10000" + string(rune('a'+i)) + "-concurrent00"
		sess := &session.Session{
			SchemaVersion: 3,
			ID:            ids[i],
			Model:         "deepseek-chat",
			CWD:           filepath.Join(home, ids[i]),
			Messages:      []deepseek.Message{{Role: deepseek.RoleUser, Content: "c"}},
		}
		if err := st.Save(sess); err != nil {
			t.Fatal(err)
		}
	}

	lookup := sessionLookup()
	var wg sync.WaitGroup
	errs := make([]error, n)
	got := make([]string, n)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = lookup(ids[i])
		}(i)
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil {
			t.Fatalf("concurrent lookup(%s): %v", ids[i], errs[i])
		}
		if want := filepath.Join(home, ids[i]); got[i] != want {
			t.Errorf("lookup(%s) = %q, want %q", ids[i], got[i], want)
		}
	}
}
