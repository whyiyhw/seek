package session

import (
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestListProject covers the M-SD2 read path: bucketing by CWD→
// project ID, oldest-first order, and the fork skip (ParentID set →
// excluded so downstream recurrence counting can't count one
// conversation twice).
func TestListProject(t *testing.T) {
	// No t.Parallel() — newStoreIn uses t.Setenv.
	store := newStoreIn(t)

	mk := func(cwd string, age time.Duration, parent string) *Session {
		s := New("deepseek-v4-flash", cwd, "sys", true, false)
		s.CreatedAt = time.Now().UTC().Add(-age)
		s.UpdatedAt = s.CreatedAt.Add(time.Minute)
		s.ParentID = parent
		return s
	}

	projA := "/work/proj-a"
	projB := "/work/proj-b"
	// Oldest first in proj-a: old-a, mid-a, new-a. Plus noise: a
	// proj-b session and a fork (ParentID set) of mid-a.
	oldA := mk(projA, 3*time.Hour, "")
	oldA.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "old"}}
	midA := mk(projA, 2*time.Hour, "")
	midA.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "mid"}}
	newA := mk(projA, 1*time.Hour, "")
	forkA := mk(projA, 30*time.Minute, midA.ID)
	other := mk(projB, 90*time.Minute, "")
	for _, s := range []*Session{oldA, midA, newA, forkA, other} {
		if err := store.Save(s); err != nil {
			t.Fatalf("save %s: %v", s.ID, err)
		}
	}

	got, err := store.ListProject(paths.ProjectID(projA))
	if err != nil {
		t.Fatalf("ListProject: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3 (fork + other-project excluded): %+v", len(got), got)
	}
	wantOrder := []string{oldA.ID, midA.ID, newA.ID}
	for i, w := range wantOrder {
		if got[i].ID != w {
			t.Errorf("order[%d] = %s, want %s", i, got[i].ID, w)
		}
	}
	if got[0].CWD != projA {
		t.Errorf("CWD not surfaced in SessionInfo: %+v", got[0])
	}
}

func TestListProject_NoneInProject(t *testing.T) {
	// No t.Parallel() — newStoreIn uses t.Setenv.
	store := newStoreIn(t)
	s := New("m", "/elsewhere", "sys", false, false)
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListProject(paths.ProjectID("/work/proj-a"))
	if err != nil {
		t.Fatalf("ListProject: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}
