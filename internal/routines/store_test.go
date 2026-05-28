package routines

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestJob_RoundTripJSON pins the wire-format round-trip.
// Schedule serialises as its raw string; loading re-parses and
// validates. A jobs.jsonl entry written today must remain
// readable after any future refactor that touches MarshalJSON.
func TestJob_RoundTripJSON(t *testing.T) {
	s, err := ParseSchedule("@every 5m")
	if err != nil {
		t.Fatal(err)
	}
	original := Job{
		Name:        "morning-report",
		Schedule:    s,
		Prompt:      "Summarise PRs.",
		ProjectRoot: "/Users/x/proj",
		Created:     time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC),
		NextRun:     time.Date(2026, 5, 28, 9, 5, 0, 0, time.UTC),
		MaxRuns:     0,
		Yolo:        true,
		Notify:      "always",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Job
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.Name != original.Name {
		t.Errorf("Name lost: %q", loaded.Name)
	}
	if loaded.Schedule.Every != original.Schedule.Every {
		t.Errorf("Schedule.Every lost: %v", loaded.Schedule.Every)
	}
	if loaded.Schedule.Raw != original.Schedule.Raw {
		t.Errorf("Schedule.Raw lost: %q", loaded.Schedule.Raw)
	}
}

// TestJob_UnmarshalBadScheduleSurfacesError: corrupt jobs.jsonl
// entry (e.g. user hand-edited and broke the schedule) must
// surface a clear error naming the job + the bad value rather
// than panicking the loader.
func TestJob_UnmarshalBadScheduleSurfacesError(t *testing.T) {
	bad := `{"name":"x","schedule":"every 5m","prompt":"y"}` // missing @ on every
	var j Job
	err := json.Unmarshal([]byte(bad), &j)
	if err == nil {
		t.Fatal("expected error on bad schedule")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should name the job: %v", err)
	}
}

// TestValidateName_HappyAndSad covers the canonical shape.
// Names hit the filesystem (runs/<name>.lock); slashes /
// spaces / dots would all break that.
func TestValidateName_HappyAndSad(t *testing.T) {
	for _, ok := range []string{
		"a", "abc", "ab-cd", "ab_cd", "morning-report",
		"schedule_wakeup-20260601-103412-abcdef",
		strings.Repeat("x", 64),
	} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"", "-leading-dash", "_leading-underscore",
		"with space", "with/slash", "with.dot",
		strings.Repeat("x", 65), // 65 chars > 64 cap
		"emoji-🎉",               // non-ASCII
	} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) should reject", bad)
		}
	}
}

func TestValidateNotify(t *testing.T) {
	for _, ok := range []string{"", "always", "on_failure", "never"} {
		if err := ValidateNotify(ok); err != nil {
			t.Errorf("ValidateNotify(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"sometimes", "ALWAYS", "yes"} {
		if err := ValidateNotify(bad); err == nil {
			t.Errorf("ValidateNotify(%q) should reject", bad)
		}
	}
}

// ----- Store -----

// newTestStore returns a Store whose jobs.jsonl lives in a
// tempdir. SEEK_HOME redirect is irrelevant since we use
// OpenStoreAt explicitly; tempdir auto-cleans at test end.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return OpenStoreAt(filepath.Join(dir, "jobs.jsonl"))
}

func TestStore_ListOnMissingFile(t *testing.T) {
	s := newTestStore(t)
	jobs, err := s.List()
	if err != nil {
		t.Fatalf("List on missing file: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected empty list, got %d entries", len(jobs))
	}
}

func TestStore_CreateAndList(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	j := Job{Name: "x", Schedule: sched, Prompt: "do x"}
	if err := s.Create(j, CreateOptions{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	jobs, _ := s.List()
	if len(jobs) != 1 {
		t.Fatalf("len = %d, want 1", len(jobs))
	}
	if jobs[0].Name != "x" {
		t.Errorf("Name = %q", jobs[0].Name)
	}
	// Defaults filled in.
	if jobs[0].Created.IsZero() {
		t.Error("Created not auto-set")
	}
	if jobs[0].NextRun.IsZero() {
		t.Error("NextRun not auto-set")
	}
	if jobs[0].LastStatus != StatusScheduled {
		t.Errorf("LastStatus = %q, want scheduled", jobs[0].LastStatus)
	}
	if jobs[0].Notify != NotifyAlways {
		t.Errorf("Notify default = %q, want always", jobs[0].Notify)
	}
}

// TestStore_CreateDuplicateRequiresForce: silent overwrite was
// PRD §8 risk row #11 — user types `create` twice by accident
// and loses their prompt. Default behaviour is ErrJobExists.
func TestStore_CreateDuplicateRequiresForce(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	j1 := Job{Name: "x", Schedule: sched, Prompt: "first"}
	if err := s.Create(j1, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	j2 := Job{Name: "x", Schedule: sched, Prompt: "second"}
	err := s.Create(j2, CreateOptions{})
	if !errors.Is(err, ErrJobExists) {
		t.Errorf("Create duplicate should ErrJobExists, got %v", err)
	}
	// Original still there.
	got, _ := s.Get("x")
	if got.Prompt != "first" {
		t.Errorf("duplicate Create overwrote without --force; prompt = %q", got.Prompt)
	}
}

// TestStore_CreateForceOverwritesPreservingHistory: --force
// path REPLACES Prompt + Schedule but PRESERVES Created /
// RunCount / LastRun. "Edit by recreate" must not reset
// run accounting.
func TestStore_CreateForceOverwritesPreservingHistory(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	j := Job{Name: "x", Schedule: sched, Prompt: "first"}
	if err := s.Create(j, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Simulate some runs.
	if err := s.MarkRun("x", "run-1", StatusCompleted, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get("x")

	// Re-create with --force + different prompt + different schedule.
	newSched, _ := ParseSchedule("@hourly")
	j2 := Job{Name: "x", Schedule: newSched, Prompt: "second"}
	if err := s.Create(j2, CreateOptions{Force: true}); err != nil {
		t.Fatalf("Create --force: %v", err)
	}
	after, _ := s.Get("x")

	if after.Prompt != "second" {
		t.Errorf("Prompt = %q, want second (force should overwrite)", after.Prompt)
	}
	if after.Schedule.Raw != "@hourly" {
		t.Errorf("Schedule.Raw = %q, want @hourly", after.Schedule.Raw)
	}
	if !after.Created.Equal(before.Created) {
		t.Errorf("Created changed on force: %v → %v", before.Created, after.Created)
	}
	if after.RunCount != before.RunCount {
		t.Errorf("RunCount reset on force: %d → %d", before.RunCount, after.RunCount)
	}
	if after.LastRunID != before.LastRunID {
		t.Errorf("LastRunID lost on force: %q → %q", before.LastRunID, after.LastRunID)
	}
}

// TestStore_Get covers happy path + ErrJobNotFound.
func TestStore_Get(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get("nope"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Get on missing = %v, want ErrJobNotFound", err)
	}
	sched, _ := ParseSchedule("@every 5m")
	_ = s.Create(Job{Name: "x", Schedule: sched, Prompt: "y"}, CreateOptions{})
	got, err := s.Get("x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "x" {
		t.Errorf("Name = %q", got.Name)
	}
}

// TestStore_Delete removes by name; missing returns
// ErrJobNotFound.
func TestStore_Delete(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	_ = s.Create(Job{Name: "x", Schedule: sched, Prompt: "y"}, CreateOptions{})
	if err := s.Delete("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("x"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Get after Delete = %v, want ErrJobNotFound", err)
	}
	if err := s.Delete("x"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Delete twice = %v, want ErrJobNotFound", err)
	}
}

// TestStore_MarkRunAdvancesNextRun: after each fire, NextRun
// jumps strictly ahead of the just-fired moment.
func TestStore_MarkRunAdvancesNextRun(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	_ = s.Create(Job{Name: "x", Schedule: sched, Prompt: "y"}, CreateOptions{})

	before, _ := s.Get("x")
	ran := time.Now().UTC()
	if err := s.MarkRun("x", "run-1", StatusCompleted, "", ran); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get("x")

	if !after.NextRun.After(ran) {
		t.Errorf("NextRun (%v) should be strictly after ranAt (%v)", after.NextRun, ran)
	}
	if !after.NextRun.After(before.NextRun) {
		t.Errorf("NextRun didn't advance: %v → %v", before.NextRun, after.NextRun)
	}
	if after.RunCount != 1 {
		t.Errorf("RunCount = %d, want 1", after.RunCount)
	}
	if after.LastStatus != StatusCompleted {
		t.Errorf("LastStatus = %q", after.LastStatus)
	}
	if after.LastRunID != "run-1" {
		t.Errorf("LastRunID = %q", after.LastRunID)
	}
}

// TestStore_MarkRunAutoDeletesAtMaxRuns: load-bearing for
// schedule_wakeup (max_runs=1). Without this, `seek cron
// list` fills up with completed one-shot wakeups
// (feature-routines.md §8 risk row).
func TestStore_MarkRunAutoDeletesAtMaxRuns(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	_ = s.Create(Job{Name: "wakeup", Schedule: sched, Prompt: "p", MaxRuns: 1}, CreateOptions{})

	if err := s.MarkRun("wakeup", "run-1", StatusCompleted, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("wakeup"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("max_runs=1 job should auto-delete after fire; Get = %v", err)
	}
}

// TestStore_MarkRunMaxRunsCountsUp: max_runs > 1 fires that
// many times before deletion.
func TestStore_MarkRunMaxRunsCountsUp(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	_ = s.Create(Job{Name: "j", Schedule: sched, Prompt: "p", MaxRuns: 3}, CreateOptions{})

	for i := 1; i <= 2; i++ {
		if err := s.MarkRun("j", "r", StatusCompleted, "", time.Now().UTC()); err != nil {
			t.Fatalf("MarkRun #%d: %v", i, err)
		}
		_, err := s.Get("j")
		if err != nil {
			t.Fatalf("Get after MarkRun #%d: %v", i, err)
		}
	}
	// 3rd run hits max → delete.
	if err := s.MarkRun("j", "r", StatusCompleted, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("j"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("max_runs=3 job should be gone after 3rd fire; got %v", err)
	}
}

// TestStore_MarkRunMissingJob: race with Delete returns
// ErrJobNotFound.
func TestStore_MarkRunMissingJob(t *testing.T) {
	s := newTestStore(t)
	err := s.MarkRun("nope", "r", StatusCompleted, "", time.Now().UTC())
	if !errors.Is(err, ErrJobNotFound) {
		t.Errorf("MarkRun on missing = %v, want ErrJobNotFound", err)
	}
}

// TestStore_AtomicRewriteSurvivesPartial: the write-tmp-rename
// dance leaves jobs.jsonl in the prior committed state if the
// rewrite is interrupted. We can't easily simulate power loss
// in a Go test, but we CAN verify that a successful Create
// produces a clean .jsonl + no orphan .tmp on disk.
func TestStore_AtomicRewriteSurvivesPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	s := OpenStoreAt(path)
	sched, _ := ParseSchedule("@every 5m")
	if err := s.Create(Job{Name: "x", Schedule: sched, Prompt: "y"}, CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// After Create returns, no .tmp file should linger.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") || strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("leftover tmp file after successful Create: %s", e.Name())
		}
	}
}

// TestStore_MalformedLineSkipped: a hand-edited bad line in
// jobs.jsonl shouldn't make the whole file unreadable.
// Matches the tolerance subagent.foldEvents has.
func TestStore_MalformedLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.jsonl")
	// Manually write: one good entry, one garbage, one good.
	sched, _ := ParseSchedule("@every 5m")
	good1, _ := json.Marshal(Job{Name: "a", Schedule: sched, Prompt: "p"})
	good2, _ := json.Marshal(Job{Name: "b", Schedule: sched, Prompt: "p"})
	content := string(good1) + "\nthis is not json\n" + string(good2) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s := OpenStoreAt(path)
	jobs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs (malformed skipped), got %d", len(jobs))
	}
}

// TestStore_SortedByName: List output is deterministic — same
// jobs.jsonl, same order, regardless of disk insertion. Useful
// for /agents-style table rendering.
func TestStore_SortedByName(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	for _, name := range []string{"zebra", "alpha", "mango"} {
		_ = s.Create(Job{Name: name, Schedule: sched, Prompt: "p"}, CreateOptions{})
	}
	jobs, _ := s.List()
	want := []string{"alpha", "mango", "zebra"}
	for i, j := range jobs {
		if j.Name != want[i] {
			t.Errorf("position %d: got %q, want %q", i, j.Name, want[i])
		}
	}
}

// TestStore_ConcurrentCreateInProcess: same-process mu
// serialises callers. -race catches any field tearing.
func TestStore_ConcurrentCreateInProcess(t *testing.T) {
	s := newTestStore(t)
	sched, _ := ParseSchedule("@every 5m")
	const N = 20
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		go func(n int) {
			j := Job{
				Name:     "j" + string(rune('a'+n%26)),
				Schedule: sched,
				Prompt:   "p",
			}
			errCh <- s.Create(j, CreateOptions{Force: true})
		}(i)
	}
	for i := 0; i < N; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent Create: %v", err)
		}
	}
	jobs, _ := s.List()
	if len(jobs) == 0 {
		t.Error("no jobs after concurrent Create")
	}
}

// TestStore_NilSafe defends nil-receiver paths.
func TestStore_NilSafe(t *testing.T) {
	var s *Store
	if _, err := s.List(); err == nil {
		t.Error("nil Store List should error")
	}
	if err := s.Create(Job{}, CreateOptions{}); err == nil {
		t.Error("nil Store Create should error")
	}
	if err := s.Delete("x"); err == nil {
		t.Error("nil Store Delete should error")
	}
	if err := s.MarkRun("x", "r", StatusCompleted, "", time.Now()); err == nil {
		t.Error("nil Store MarkRun should error")
	}
	if _, err := s.Get("x"); err == nil {
		t.Error("nil Store Get should error")
	}
}
