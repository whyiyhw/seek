package routines

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// makeFile writes a marker file at path with a specific mtime.
// Tests construct a synthetic history without actually waiting
// real wall-clock time.
func makeFile(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func listNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

// TestGCRuns_KeepRecentTrimsOlder pins the count axis: with 5
// files and KeepRecent=3, the 2 oldest go. Names embed timestamps
// to make the expected outcome unambiguous.
func TestGCRuns_KeepRecentTrimsOlder(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	// 5 files, mtimes spaced 1 minute apart, names embed mtime
	// so the assertion below is readable.
	for i := range 5 {
		mt := now.Add(time.Duration(-i) * time.Minute)
		name := mt.Format("150405") + "-run.jsonl"
		makeFile(t, filepath.Join(dir, name), mt)
	}

	removed, err := GCRuns(GCRunsOptions{
		Dir:        dir,
		KeepRecent: 3,
		MaxAge:     -1, // age-axis disabled — isolate count axis
		Now:        now,
	})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2 (5 files, keep 3, drop 2)", removed)
	}
	got := listNames(t, dir)
	if len(got) != 3 {
		t.Errorf("survivors = %d, want 3: %v", len(got), got)
	}
}

// TestGCRuns_MaxAgeTrimsOlder pins the age axis: 10 files, all
// within keepRecent, but half are older than maxAge.
func TestGCRuns_MaxAgeTrimsOlder(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	// 10 files. First 5 are 1 hour old; last 5 are 31 days old.
	for i := range 5 {
		makeFile(t, filepath.Join(dir, "fresh-"+itoa(i)+".jsonl"), now.Add(-1*time.Hour))
	}
	for i := range 5 {
		makeFile(t, filepath.Join(dir, "stale-"+itoa(i)+".jsonl"), now.Add(-31*24*time.Hour))
	}

	removed, err := GCRuns(GCRunsOptions{
		Dir:        dir,
		KeepRecent: -1, // count axis disabled
		MaxAge:     30 * 24 * time.Hour,
		Now:        now,
	})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 5 {
		t.Errorf("removed = %d, want 5", removed)
	}
	got := listNames(t, dir)
	for _, name := range got {
		if !strings.HasPrefix(name, "fresh-") {
			t.Errorf("survivor %q is stale — age axis missed it", name)
		}
	}
}

// TestGCRuns_TwoAxisCombined verifies both axes contribute:
// KeepRecent could keep 10, MaxAge could drop 2 of those 10. The
// combined sweep drops MAX(2 by-age, 0 over-count) = 2.
func TestGCRuns_TwoAxisCombined(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	// 8 files: 6 within both bounds, 2 stale (> maxAge but
	// would otherwise survive the count axis since 8 < 10).
	for i := range 6 {
		makeFile(t, filepath.Join(dir, "ok-"+itoa(i)+".jsonl"), now.Add(time.Duration(-i)*time.Hour))
	}
	makeFile(t, filepath.Join(dir, "stale-1.jsonl"), now.Add(-2*time.Hour-31*24*time.Hour))
	makeFile(t, filepath.Join(dir, "stale-2.jsonl"), now.Add(-31*24*time.Hour))

	removed, err := GCRuns(GCRunsOptions{
		Dir:        dir,
		KeepRecent: 10,
		MaxAge:     30 * 24 * time.Hour,
		Now:        now,
	})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2 (two stale files only)", removed)
	}
}

// TestGCRuns_PreservesLocks is the load-bearing safety pin: runs/
// holds BOTH <id>.jsonl history files AND <job>.lock advisory
// locks. GC must NEVER delete locks — that would race with a
// concurrent goroutine holding the lock via flock.
func TestGCRuns_PreservesLocks(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	// Stale lock (would be trimmed if suffix filter is broken).
	makeFile(t, filepath.Join(dir, "myjob.lock"), now.Add(-365*24*time.Hour))
	// One stale jsonl to make sure GC actually ran.
	makeFile(t, filepath.Join(dir, "old-run.jsonl"), now.Add(-31*24*time.Hour))

	removed, err := GCRuns(GCRunsOptions{
		Dir:    dir,
		MaxAge: 30 * 24 * time.Hour,
		Now:    now,
	})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only the .jsonl)", removed)
	}
	got := listNames(t, dir)
	if !slices.Contains(got, "myjob.lock") {
		t.Errorf("LOCK FILE WAS DELETED — survivors: %v", got)
	}
}

// TestGCRuns_SkipsSubdirs pins the IsRegular filter: a subdir in
// runs/ (a future per-job subdirectory feature, perhaps) must not
// be traversed or removed by the GC.
func TestGCRuns_SkipsSubdirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir-shaped.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Plain file alongside to confirm GC still works.
	makeFile(t, filepath.Join(dir, "real.jsonl"), time.Now().Add(-365*24*time.Hour))

	removed, err := GCRuns(GCRunsOptions{
		Dir:    dir,
		MaxAge: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (real.jsonl only; subdir skipped)", removed)
	}
	// Subdir survives.
	got := listNames(t, dir)
	if !slices.Contains(got, "subdir-shaped.jsonl") {
		t.Errorf("subdir was deleted: survivors = %v", got)
	}
}

// TestGCRuns_MissingDirIsNotError covers fresh-install ordering:
// Tick may sweep runs/ before any cron job has fired (dir doesn't
// exist yet). Must return (0, nil), not propagate.
func TestGCRuns_MissingDirIsNotError(t *testing.T) {
	removed, err := GCRuns(GCRunsOptions{
		Dir: filepath.Join(t.TempDir(), "never-created"),
	})
	if err != nil {
		t.Errorf("missing dir surfaced as error: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

// TestGCRuns_DefaultsApplied pins the zero-value semantics in
// GCRunsOptions: KeepRecent=0 + MaxAge=0 → use package defaults.
// We don't materialise 100 fake files; instead we rely on the
// behaviour that with defaults applied, 5 files (none old) all
// survive.
func TestGCRuns_DefaultsApplied(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		makeFile(t, filepath.Join(dir, "ok-"+itoa(i)+".jsonl"), now.Add(time.Duration(-i)*time.Minute))
	}
	removed, err := GCRuns(GCRunsOptions{Dir: dir, Now: now})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d on 5-file dir with defaults — defaults are wrong or applied wrong", removed)
	}
}

func TestGCRuns_DisabledBothAxes(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 {
		makeFile(t, filepath.Join(dir, "x-"+itoa(i)+".jsonl"), time.Now().Add(-365*24*time.Hour))
	}
	// Both axes disabled — should be a no-op even if files are
	// 1 year old.
	removed, err := GCRuns(GCRunsOptions{Dir: dir, KeepRecent: -1, MaxAge: -1})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d with both axes disabled, want 0", removed)
	}
}

func TestGCRuns_EmptyDirRequired(t *testing.T) {
	_, err := GCRuns(GCRunsOptions{Dir: ""})
	if err == nil {
		t.Error("GCRuns with empty Dir must return error")
	}
}

// TestGCMalformedTriggers_OnlyJSONSuffix verifies the parallel
// safety property: the .malformed/ dir might one day contain
// per-trigger sidecar files (reason.txt etc.); GC must only sweep
// the .json payloads, not those.
func TestGCMalformedTriggers_OnlyJSONSuffix(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	makeFile(t, filepath.Join(dir, "old.json"), now.Add(-15*24*time.Hour))
	makeFile(t, filepath.Join(dir, "old.txt"), now.Add(-15*24*time.Hour)) // sidecar, must survive

	removed, err := GCMalformedTriggers(GCMalformedOptions{
		Dir:    dir,
		MaxAge: 14 * 24 * time.Hour,
		Now:    now,
	})
	if err != nil {
		t.Fatalf("GCMalformedTriggers: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (.json only)", removed)
	}
	got := listNames(t, dir)
	if !slices.Contains(got, "old.txt") {
		t.Errorf("sidecar was deleted: survivors = %v", got)
	}
}

// TestGCRuns_TieBreakByName covers the same-mtime case: two files
// with identical second-resolution mtime (1Hz filesystem clock)
// must have a deterministic outcome — the lex-greater name wins
// (approximates "later id").
func TestGCRuns_TieBreakByName(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	mt := now.Add(-1 * time.Minute)
	makeFile(t, filepath.Join(dir, "a-run.jsonl"), mt)
	makeFile(t, filepath.Join(dir, "b-run.jsonl"), mt)
	makeFile(t, filepath.Join(dir, "c-run.jsonl"), mt)

	removed, err := GCRuns(GCRunsOptions{
		Dir:        dir,
		KeepRecent: 1,
		MaxAge:     -1,
		Now:        now,
	})
	if err != nil {
		t.Fatalf("GCRuns: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	// "c-run.jsonl" is the lex-greater; it should be the
	// survivor when mtimes are identical.
	got := listNames(t, dir)
	if len(got) != 1 || got[0] != "c-run.jsonl" {
		t.Errorf("survivor = %v, want [c-run.jsonl] (lex-greater wins on mtime tie)", got)
	}
}

// itoa keeps the test bodies dense — saves repeated strconv.Itoa
// noise on every fake filename. Pure delegation.
func itoa(i int) string { return strconv.Itoa(i) }
