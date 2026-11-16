package subagent

// ListProject tests (web task panel W3.1 acceptance): table-driven
// folds over HAND-MADE subagents.jsonl files — raw JSON lines written
// to disk, not appendEvent structs, so the fixtures pin the on-disk
// event shapes verbatim: the pre-P1 柱 G set (started/completed/failed/
// killed/orphaned/promoted) plus the P1 additions D1 (background,
// resumed_from) and D3 (notified). A future event kind the folder
// doesn't know must stay transparent (ignored, not an error).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// seedProjectIndex points SEEK_HOME at a temp root, resolves the
// subagents.jsonl path for project, and writes lines (each a raw JSON
// event or deliberate garbage). Returns the project path whose index
// ListProject must resolve to the same file.
func seedProjectIndex(t *testing.T, lines ...string) string {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	project := t.TempDir() // any abs path; ProjectID hashes it
	index, err := paths.SubagentsIndex(project)
	if err != nil {
		t.Fatalf("SubagentsIndex: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(index), 0o755); err != nil {
		t.Fatalf("mkdir index dir: %v", err)
	}
	if err := os.WriteFile(index, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	return project
}

// wantSid is the subset of Subagent the table asserts on; zero-valued
// fields mean "don't care" except where the case names them via
// check extras.
type listWant struct {
	sid         string
	status      Status
	background  bool
	notified    bool
	resumedFrom string
	reason      string
	tokens      Tokens
}

func TestListProject_TableDriven(t *testing.T) {
	// 柱 G existing shapes + P1 D1/D3 shapes, one index per case.
	cases := []struct {
		name  string
		lines []string
		want  []listWant
	}{
		{
			name: "pillar G sync lifecycle completed with tokens",
			lines: []string{
				`{"event":"started","sub_sid":"s1","ts":"2026-01-01T10:00:00Z","parent_sid":"p1","parent_turn":4,"type":"explore","description":"find X"}`,
				`{"event":"completed","sub_sid":"s1","ts":"2026-01-01T10:05:00Z","tokens":{"prompt":100,"completion":20,"cache_hit":80}}`,
			},
			want: []listWant{{sid: "s1", status: StatusCompleted,
				tokens: Tokens{Prompt: 100, Completion: 20, CacheHit: 80}}},
		},
		{
			name: "pillar G failed carries reason",
			lines: []string{
				`{"event":"started","sub_sid":"s2","ts":"2026-01-01T11:00:00Z","parent_sid":"p1","type":"general-purpose"}`,
				`{"event":"failed","sub_sid":"s2","ts":"2026-01-01T11:02:00Z","reason":"max_turns_exceeded"}`,
			},
			want: []listWant{{sid: "s2", status: StatusFailed, reason: "max_turns_exceeded"}},
		},
		{
			name: "pillar G killed / orphaned / promoted terminal kinds",
			lines: []string{
				`{"event":"started","sub_sid":"k","ts":"2026-01-01T12:00:00Z","parent_sid":"p1","type":"explore"}`,
				`{"event":"killed","sub_sid":"k","ts":"2026-01-01T12:01:00Z"}`,
				`{"event":"started","sub_sid":"o","ts":"2026-01-01T12:02:00Z","parent_sid":"p1","type":"explore"}`,
				`{"event":"orphaned","sub_sid":"o","ts":"2026-01-01T12:03:00Z"}`,
				`{"event":"started","sub_sid":"m","ts":"2026-01-01T12:04:00Z","parent_sid":"p1","type":"plan"}`,
				`{"event":"promoted","sub_sid":"m","ts":"2026-01-01T12:05:00Z"}`,
			},
			want: []listWant{
				{sid: "m", status: StatusPromoted},
				{sid: "o", status: StatusOrphaned},
				{sid: "k", status: StatusKilled},
			},
		},
		{
			name: "D1 background spawn folds mode; D3 notified annotates without lifecycle change",
			lines: []string{
				`{"event":"started","sub_sid":"bg1","ts":"2026-01-02T09:00:00Z","parent_sid":"p1","type":"explore","description":"scan deps","background":true}`,
				`{"event":"completed","sub_sid":"bg1","ts":"2026-01-02T09:10:00Z","tokens":{"prompt":50,"completion":10,"cache_hit":0}}`,
				`{"event":"notified","sub_sid":"bg1","ts":"2026-01-02T09:11:00Z","parent_turn":7}`,
			},
			want: []listWant{{sid: "bg1", status: StatusCompleted, background: true, notified: true,
				tokens: Tokens{Prompt: 50, Completion: 10}}},
		},
		{
			name: "D1 background still running stays active",
			lines: []string{
				`{"event":"started","sub_sid":"bg2","ts":"2026-01-02T10:00:00Z","parent_sid":"p1","type":"general-purpose","background":true}`,
			},
			want: []listWant{{sid: "bg2", status: StatusActive, background: true}},
		},
		{
			name: "D1 continuation seeds resumed_from lineage",
			lines: []string{
				`{"event":"started","sub_sid":"run1","ts":"2026-01-03T08:00:00Z","parent_sid":"p1","type":"explore"}`,
				`{"event":"completed","sub_sid":"run1","ts":"2026-01-03T08:05:00Z"}`,
				`{"event":"started","sub_sid":"run2","ts":"2026-01-03T09:00:00Z","parent_sid":"p1","type":"explore","resumed_from":"run1"}`,
			},
			want: []listWant{
				{sid: "run2", status: StatusActive, resumedFrom: "run1"},
				{sid: "run1", status: StatusCompleted},
			},
		},
		{
			name: "pre-P1 index (no background key) folds sync",
			lines: []string{
				`{"event":"started","sub_sid":"old","ts":"2026-01-01T10:00:00Z","parent_sid":"p1","type":"explore"}`,
				`{"event":"completed","sub_sid":"old","ts":"2026-01-01T10:05:00Z"}`,
			},
			want: []listWant{{sid: "old", status: StatusCompleted}},
		},
		{
			name: "malformed line skipped, rest folds",
			lines: []string{
				`{"event":"started","sub_sid":"good","ts":"2026-01-01T10:00:00Z","parent_sid":"p1","type":"explore"}`,
				`this is not json`,
				`{"event":"completed","sub_sid":"good","ts":"2026-01-01T10:05:00Z"}`,
			},
			want: []listWant{{sid: "good", status: StatusCompleted}},
		},
		{
			name: "terminal without started skipped; duplicate started keeps first",
			lines: []string{
				`{"event":"completed","sub_sid":"ghost","ts":"2026-01-01T10:00:00Z"}`,
				`{"event":"started","sub_sid":"d","ts":"2026-01-01T10:01:00Z","parent_sid":"p1","type":"explore","description":"first"}`,
				`{"event":"started","sub_sid":"d","ts":"2026-01-01T10:02:00Z","parent_sid":"other","type":"plan","description":"dup"}`,
			},
			want: []listWant{{sid: "d", status: StatusActive, resumedFrom: ""}},
		},
		{
			name: "unknown future event kind is transparent",
			lines: []string{
				`{"event":"started","sub_sid":"u","ts":"2026-01-01T10:00:00Z","parent_sid":"p1","type":"explore"}`,
				`{"event":"paused","sub_sid":"u","ts":"2026-01-01T10:01:00Z","until":"2027-01-01T00:00:00Z"}`,
				`{"event":"completed","sub_sid":"u","ts":"2026-01-01T10:05:00Z"}`,
			},
			want: []listWant{{sid: "u", status: StatusCompleted}},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := seedProjectIndex(t, c.lines...)
			got, err := ListProject(project, ListFilter{})
			if err != nil {
				t.Fatalf("ListProject: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("folded %d records, want %d: %+v", len(got), len(c.want), got)
			}
			for i, w := range c.want {
				s := got[i]
				if s.SubSid != w.sid {
					t.Errorf("record[%d].SubSid = %q, want %q", i, s.SubSid, w.sid)
				}
				if s.Status != w.status {
					t.Errorf("%s: Status = %s, want %s", w.sid, s.Status, w.status)
				}
				if s.Background != w.background {
					t.Errorf("%s: Background = %v, want %v", w.sid, s.Background, w.background)
				}
				if s.Notified != w.notified {
					t.Errorf("%s: Notified = %v, want %v", w.sid, s.Notified, w.notified)
				}
				if s.ResumedFrom != w.resumedFrom {
					t.Errorf("%s: ResumedFrom = %q, want %q", w.sid, s.ResumedFrom, w.resumedFrom)
				}
				if s.Reason != w.reason {
					t.Errorf("%s: Reason = %q, want %q", w.sid, s.Reason, w.reason)
				}
				if w.tokens != (Tokens{}) && s.Tokens != w.tokens {
					t.Errorf("%s: Tokens = %+v, want %+v", w.sid, s.Tokens, w.tokens)
				}
			}
		})
	}
}

// TestListProject_ParentFilter: ?parent= is the endpoint's filter —
// only that parent's rows survive, newest-first order preserved.
func TestListProject_ParentFilter(t *testing.T) {
	project := seedProjectIndex(t,
		`{"event":"started","sub_sid":"a1","ts":"2026-01-01T10:00:00Z","parent_sid":"sessA","type":"explore"}`,
		`{"event":"completed","sub_sid":"a1","ts":"2026-01-01T10:05:00Z"}`,
		`{"event":"started","sub_sid":"b1","ts":"2026-01-01T11:00:00Z","parent_sid":"sessB","type":"explore"}`,
		`{"event":"started","sub_sid":"a2","ts":"2026-01-01T12:00:00Z","parent_sid":"sessA","type":"plan","background":true}`,
	)

	got, err := ListProject(project, ListFilter{ParentSid: "sessA"})
	if err != nil {
		t.Fatalf("ListProject: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows for sessA, want 2: %+v", len(got), got)
	}
	// Newest first: a2 (12:00) before a1 (10:00).
	if got[0].SubSid != "a2" || got[1].SubSid != "a1" {
		t.Errorf("order = [%s %s], want [a2 a1] (newest first)", got[0].SubSid, got[1].SubSid)
	}

	// Unknown parent folds to an empty — non-nil — projection.
	none, err := ListProject(project, ListFilter{ParentSid: "nope"})
	if err != nil {
		t.Fatalf("ListProject(unknown parent): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unknown parent returned %d rows, want 0", len(none))
	}
}

// TestListProject_MissingIndex: a project that never spawned folds to
// an empty slice (not nil — it must JSON-serialise as [], the shape
// the panel and the aligned subagent/list method expect).
func TestListProject_MissingIndex(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	project := t.TempDir()

	got, err := ListProject(project, ListFilter{})
	if err != nil {
		t.Fatalf("ListProject on project without index: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("got %+v, want empty non-nil slice", got)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "[]" {
		t.Errorf("empty projection marshals to %s, want []", b)
	}
}

// TestListProject_FieldsSeededOnce pins the immutable-field provenance:
// everything after `started` (even a duplicate started) must not
// rewrite the seeded row, and started_at survives into the projection
// for the panel's timestamps.
func TestListProject_FieldsSeededOnce(t *testing.T) {
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	project := seedProjectIndex(t,
		`{"event":"started","sub_sid":"s","ts":"2026-01-01T10:00:00Z","parent_sid":"p1","parent_turn":3,"type":"explore","description":"seeded","worktree_path":"/tmp/wt"}`,
		`{"event":"notified","sub_sid":"s","ts":"2026-01-01T10:06:00Z","parent_turn":9}`,
	)
	got, err := ListProject(project, ListFilter{})
	if err != nil {
		t.Fatalf("ListProject: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	s := got[0]
	if s.ParentSid != "p1" || s.ParentTurn != 3 || s.Type != TypeExplore ||
		s.Description != "seeded" || s.WorktreePath != "/tmp/wt" {
		t.Errorf("seeded fields drifted: %+v", s)
	}
	if !s.StartedAt.Equal(start) {
		t.Errorf("StartedAt = %v, want %v", s.StartedAt, start)
	}
	// notified's parent_turn annotates delivery, it does NOT rewrite
	// the seeded ParentTurn (which names the spawn turn).
	if s.ParentTurn != 3 {
		t.Errorf("ParentTurn rewritten by notified: %d, want 3", s.ParentTurn)
	}
}
