package fsobserve

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bump rewrites a file such that its stat token is guaranteed to differ.
// Filesystems vary in mtime granularity (HFS+ was 1s), so we change the
// size too rather than relying on the clock.
func bump(t *testing.T, path, content string) {
	t.Helper()
	time.Sleep(10 * time.Millisecond)
	writeFile(t, path, content)
}

func TestCheck_NewFileIsAlwaysAllowed(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "brand-new.go")
	if got := s.Check(path); got != StatusOK {
		t.Errorf("Check(nonexistent) = %v, want StatusOK — creating a file has nothing to clobber", got)
	}
}

func TestCheck_ExistingUnreadFileIsRefused(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "existing.go")
	writeFile(t, path, "package main\n")

	if got := s.Check(path); got != StatusUnseen {
		t.Errorf("Check(existing, never read) = %v, want StatusUnseen", got)
	}
}

func TestCheck_ObservedFileIsAllowed(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "seen.go")
	writeFile(t, path, "package main\n")

	s.Observe(path)
	if got := s.Check(path); got != StatusOK {
		t.Errorf("Check(observed, unchanged) = %v, want StatusOK", got)
	}
}

func TestCheck_ExternallyModifiedFileIsStale(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "raced.go")
	writeFile(t, path, "package main\n")
	s.Observe(path)

	bump(t, path, "package main\n\nfunc addedByaColleague() {}\n")

	if got := s.Check(path); got != StatusStale {
		t.Errorf("Check(observed, then externally changed) = %v, want StatusStale", got)
	}
}

// TestCheck_ReObserveClearsStale is the read → edit → write path: the
// tool that just changed the file re-observes it, so the model's own
// edit must not trip the stale check on a subsequent write.
func TestCheck_ReObserveClearsStale(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "edited.go")
	writeFile(t, path, "package main\n")
	s.Observe(path)

	bump(t, path, "package main\n// edited by the model\n")
	if got := s.Check(path); got != StatusStale {
		t.Fatalf("precondition: want StatusStale, got %v", got)
	}

	s.Observe(path) // what edit/write do after mutating
	if got := s.Check(path); got != StatusOK {
		t.Errorf("Check after re-observe = %v, want StatusOK", got)
	}
}

func TestCheck_ForgetMakesFileUnseenAgain(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "gone.go")
	writeFile(t, path, "x")
	s.Observe(path)
	s.Forget(path)

	if got := s.Check(path); got != StatusUnseen {
		t.Errorf("Check after Forget = %v, want StatusUnseen", got)
	}
}

func TestCheck_DirectoryIsNotOurProblem(t *testing.T) {
	s := New()
	dir := t.TempDir()
	if got := s.Check(dir); got != StatusOK {
		t.Errorf("Check(dir) = %v, want StatusOK — the write itself gives a better error", got)
	}
}

func TestCheck_ObservationIsPerPath(t *testing.T) {
	s := New()
	dir := t.TempDir()
	a := filepath.Join(dir, "a.go")
	b := filepath.Join(dir, "b.go")
	writeFile(t, a, "a")
	writeFile(t, b, "b")

	s.Observe(a)
	if got := s.Check(a); got != StatusOK {
		t.Errorf("Check(a) = %v, want StatusOK", got)
	}
	if got := s.Check(b); got != StatusUnseen {
		t.Errorf("Check(b) = %v, want StatusUnseen — observing a must not vouch for b", got)
	}
}

func TestObserve_MissingPathIsSilent(t *testing.T) {
	s := New()
	path := filepath.Join(t.TempDir(), "never-existed.go")
	s.Observe(path) // must not panic
	if got := s.Check(path); got != StatusOK {
		t.Errorf("Check = %v, want StatusOK", got)
	}
}

// TestNilStore_IsFullyPermissive keeps the guard opt-in: a tool built
// without a Store must behave exactly as it did before this package.
func TestNilStore_IsFullyPermissive(t *testing.T) {
	var s *Store
	path := filepath.Join(t.TempDir(), "x.go")
	writeFile(t, path, "content")

	if got := s.Check(path); got != StatusOK {
		t.Errorf("nil Store Check = %v, want StatusOK", got)
	}
	s.Observe(path) // must not panic
	s.Forget(path)  // must not panic
}

func TestExplain_NamesTheRecoveryStep(t *testing.T) {
	unseen := Explain(StatusUnseen, "/repo/main.go")
	if !strings.Contains(unseen, "/repo/main.go") {
		t.Error("unseen message does not name the path")
	}
	if !strings.Contains(unseen, "read") {
		t.Error("unseen message does not tell the model to read the file")
	}

	stale := Explain(StatusStale, "/repo/main.go")
	if !strings.Contains(stale, "changed on disk") {
		t.Errorf("stale message does not explain what happened: %q", stale)
	}
	if !strings.Contains(stale, "read") {
		t.Error("stale message does not name the recovery step")
	}

	if got := Explain(StatusOK, "/x"); got != "" {
		t.Errorf("Explain(StatusOK) = %q, want empty", got)
	}
}

func TestStore_ConcurrentObserveAndCheck(t *testing.T) {
	s := New()
	dir := t.TempDir()
	paths := make([]string, 20)
	for i := range paths {
		paths[i] = filepath.Join(dir, string(rune('a'+i))+".go")
		writeFile(t, paths[i], "x")
	}

	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			s.Observe(p)
			_ = s.Check(p)
		}(p)
	}
	wg.Wait()

	for _, p := range paths {
		if got := s.Check(p); got != StatusOK {
			t.Errorf("Check(%s) = %v, want StatusOK", p, got)
		}
	}
}

// ---- file-identity half of the token (dev:ino) ----

// TestCheck_RenameOverIsDetected is the case size+mtime alone can miss.
// Formatters, code generators and `git checkout` do not modify files in
// place — they write a temp file and rename it over the target. The
// content is different but the size can easily be identical, and on a
// filesystem with coarse mtime granularity the timestamp can be too.
// The inode always changes.
func TestCheck_RenameOverIsDetected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file identity is unavailable off Unix; the token degrades to size+mtime by design")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "formatted.go")
	writeFile(t, target, "aaaa\n")

	s := New()
	s.Observe(target)

	// Simulate gofmt/git: stage a same-size replacement and rename over.
	tmp := filepath.Join(dir, "staged.tmp")
	writeFile(t, tmp, "bbbb\n")
	// Force identical size AND mtime so ONLY the inode differs.
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		t.Fatalf("test setup failed to make size+mtime identical: before=(%d,%v) after=(%d,%v)",
			fi.Size(), fi.ModTime(), after.Size(), after.ModTime())
	}

	if got := s.Check(target); got != StatusStale {
		t.Errorf("Check after a same-size same-mtime rename-over = %v, want StatusStale "+
			"(the inode changed; this is exactly what dev:ino is for)", got)
	}
}

func TestCheck_InPlaceModificationStillDetected(t *testing.T) {
	// Adding identity must not weaken the ordinary case: an in-place
	// edit keeps the inode and changes size/mtime.
	dir := t.TempDir()
	target := filepath.Join(dir, "edited.go")
	writeFile(t, target, "short\n")
	s := New()
	s.Observe(target)

	bump(t, target, "a good deal longer than before\n")
	if got := s.Check(target); got != StatusStale {
		t.Errorf("Check after in-place modification = %v, want StatusStale", got)
	}
}

// ---- Plan: the shape the write tool consumes ----

func TestPlan_AbsentTargetIsGuardedCreate(t *testing.T) {
	s := New()
	d := s.Plan(filepath.Join(t.TempDir(), "new.go"))
	if d.Status != StatusOK {
		t.Errorf("Status = %v, want StatusOK", d.Status)
	}
	if !d.Guarded {
		t.Error("Guarded = false; an absent target must still be created exclusively")
	}
	if d.Exists {
		t.Error("Exists = true for an absent target")
	}
}

func TestPlan_ObservedTargetCarriesToken(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "seen.go")
	writeFile(t, target, "x\n")
	s := New()
	s.Observe(target)

	d := s.Plan(target)
	if d.Status != StatusOK || !d.Guarded || !d.Exists {
		t.Fatalf("Plan = %+v, want OK/guarded/exists", d)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Matches(fi) {
		t.Error("Matches(current stat) = false for an unchanged file")
	}

	bump(t, target, "changed\n")
	fi2, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if d.Matches(fi2) {
		t.Error("Matches(changed stat) = true; the token did not detect the change")
	}
}

func TestPlan_NilStoreIsUnguarded(t *testing.T) {
	var s *Store
	dir := t.TempDir()
	target := filepath.Join(dir, "x.go")
	writeFile(t, target, "existing\n")

	d := s.Plan(target)
	if d.Status != StatusOK {
		t.Errorf("Status = %v, want StatusOK", d.Status)
	}
	if d.Guarded {
		t.Error("Guarded = true for a nil Store — an unconfigured tool must write unconditionally")
	}
}

func TestPlan_DirectoryIsUnguarded(t *testing.T) {
	s := New()
	d := s.Plan(t.TempDir())
	if d.Guarded {
		t.Error("Guarded = true for a directory; the write's own error is the better message")
	}
}
