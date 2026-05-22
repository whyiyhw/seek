package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withMemoryEnv sets SEEK_HOME to a tempdir so internal/paths resolves
// ~/.seek under test isolation. Returns both the project cwd and the
// SEEK_HOME root so tests can poke either side directly.
func withMemoryEnv(t *testing.T) (cwd, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("SEEK_HOME", home)
	cwd = t.TempDir()
	return cwd, home
}

func TestLoadOrCreate_FreshProject_WritesManifestAndPointer(t *testing.T) {
	cwd, home := withMemoryEnv(t)

	p, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !isValidProjectID(p.ID) {
		t.Errorf("ID %q not valid", p.ID)
	}
	wantDir := filepath.Join(home, "projects", p.ID)
	if p.Dir != wantDir {
		t.Errorf("Dir = %q, want %q", p.Dir, wantDir)
	}

	// Manifest persisted with FirstSeen ~= now.
	manifest := filepath.Join(p.Dir, "manifest.json")
	if _, err := os.Stat(manifest); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
	if p.Manifest.FirstSeen.IsZero() {
		t.Errorf("FirstSeen should be set on fresh project")
	}
	if p.Manifest.ProjectID != p.ID {
		t.Errorf("Manifest.ProjectID = %q, want %q", p.Manifest.ProjectID, p.ID)
	}

	// Pointer file written under the project's .seek/.
	pointer := filepath.Join(cwd, ".seek", "project-id")
	data, err := os.ReadFile(pointer)
	if err != nil {
		t.Fatalf("project-id pointer not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != p.ID {
		t.Errorf("pointer content = %q, want %q", data, p.ID)
	}

	// No entries on a fresh project.
	if got := p.Entries(); len(got) != 0 {
		t.Errorf("fresh project should have 0 entries, got %d", len(got))
	}
}

func TestLoadOrCreate_ReloadPreservesFirstSeen(t *testing.T) {
	cwd, _ := withMemoryEnv(t)

	first, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	originalFirst := first.Manifest.FirstSeen
	originalLast := first.Manifest.LastSeen

	// Sleep enough to make a second-resolution timestamp differ.
	time.Sleep(10 * time.Millisecond)

	second, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}
	if !second.Manifest.FirstSeen.Equal(originalFirst) {
		t.Errorf("FirstSeen drifted: %v → %v", originalFirst, second.Manifest.FirstSeen)
	}
	if !second.Manifest.LastSeen.After(originalLast) {
		t.Errorf("LastSeen should advance: %v → %v", originalLast, second.Manifest.LastSeen)
	}
}

func TestLoadOrCreate_RecoversAfterDirectoryMove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	originalCwd := t.TempDir()
	first, err := LoadOrCreate(originalCwd)
	if err != nil {
		t.Fatalf("LoadOrCreate at original cwd: %v", err)
	}

	// Drop a useful entry so we can confirm history survives the move.
	if err := first.Add(Entry{Name: "anchor", Tagline: "anchor entry"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Simulate a project move: copy the .seek/project-id pointer into a
	// new directory, then load from there. The old absolute path's
	// hash won't match, but the pointer file MUST steer us to the
	// same on-disk M.
	newCwd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(newCwd, ".seek"), 0o755); err != nil {
		t.Fatalf("setup new cwd: %v", err)
	}
	pointerSrc, err := os.ReadFile(filepath.Join(originalCwd, ".seek", "project-id"))
	if err != nil {
		t.Fatalf("read original pointer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newCwd, ".seek", "project-id"), pointerSrc, 0o644); err != nil {
		t.Fatalf("write pointer to new cwd: %v", err)
	}

	moved, err := LoadOrCreate(newCwd)
	if err != nil {
		t.Fatalf("LoadOrCreate at moved cwd: %v", err)
	}
	if moved.ID != first.ID {
		t.Errorf("moved project should reuse ID %q, got %q", first.ID, moved.ID)
	}
	if _, ok := moved.Get("anchor"); !ok {
		t.Errorf("moved project should still have the anchor entry")
	}

	// AbsPath in the manifest should now reflect the new location.
	if moved.Manifest.AbsPath == first.Manifest.AbsPath {
		t.Errorf("AbsPath should update to the new location, still %q", moved.Manifest.AbsPath)
	}
}

func TestProject_AddGetTouchRecall_RoundTrip(t *testing.T) {
	cwd, _ := withMemoryEnv(t)

	p, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	entry := Entry{
		Name:    "session-storage-format",
		Tagline: "session uses JSONL not JSON for prefix-cache stability",
		Content: "rationale...",
		Tags:    []string{"architecture", "session"},
	}
	if err := p.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Re-load from disk and verify the entry survives.
	p2, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := p2.Get("session-storage-format")
	if !ok {
		t.Fatal("entry missing after reload")
	}
	if got.Tagline != entry.Tagline {
		t.Errorf("Tagline = %q, want %q", got.Tagline, entry.Tagline)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be set on Add")
	}
	if !got.LastRecalledAt.Equal(got.CreatedAt) {
		t.Errorf("LastRecalledAt should default to CreatedAt on first write, got %v vs %v",
			got.LastRecalledAt, got.CreatedAt)
	}

	// TouchRecall bumps count and timestamp.
	recallAt := time.Now().UTC().Add(time.Hour)
	if err := p2.TouchRecall("session-storage-format", recallAt); err != nil {
		t.Fatalf("TouchRecall: %v", err)
	}
	p3, _ := LoadOrCreate(cwd)
	got, _ = p3.Get("session-storage-format")
	if got.RecallCount != 1 {
		t.Errorf("RecallCount = %d, want 1", got.RecallCount)
	}
	if !got.LastRecalledAt.Equal(recallAt) {
		t.Errorf("LastRecalledAt = %v, want %v", got.LastRecalledAt, recallAt)
	}
}

func TestProject_TouchRecall_MissingReturnsErrNotFound(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	err := p.TouchRecall("nope", time.Now())
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errorsContains(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestProject_Index_SortsByNameAndExcludesStale(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	for _, e := range []Entry{
		{Name: "zeta", Tagline: "z"},
		{Name: "alpha", Tagline: "a"},
		{Name: "middle", Tagline: "m", Stale: true},
		{Name: "beta", Tagline: "b"},
	} {
		if err := p.Add(e); err != nil {
			t.Fatalf("Add %s: %v", e.Name, err)
		}
	}

	idx := p.Index()
	wantNames := []string{"alpha", "beta", "zeta"}
	if len(idx) != len(wantNames) {
		t.Fatalf("Index len = %d, want %d (stale should be filtered)", len(idx), len(wantNames))
	}
	for i, want := range wantNames {
		if idx[i].Name != want {
			t.Errorf("Index[%d].Name = %q, want %q", i, idx[i].Name, want)
		}
	}
}

func TestProject_Index_ByteStableAcrossLoads(t *testing.T) {
	// PRD §8 acceptance #7: same on-disk content → byte-identical
	// PrePromptHook output. Index is the primitive that produces the
	// stable bytes — verify it returns the same slice across reloads.
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	for _, name := range []string{"gamma", "alpha", "beta"} {
		if err := p.Add(Entry{Name: name, Tagline: "t-" + name}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	first := p.Index()

	p2, _ := LoadOrCreate(cwd)
	second := p2.Index()

	if len(first) != len(second) {
		t.Fatalf("Index sizes differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("Index[%d] drift: %+v vs %+v", i, first[i], second[i])
		}
	}
}

func TestProject_Remove_Idempotent(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	if err := p.Add(Entry{Name: "x", Tagline: "x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	removed, err := p.Remove("x")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed {
		t.Errorf("expected Remove to report existing-entry deletion")
	}

	removed, err = p.Remove("x")
	if err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if removed {
		t.Errorf("second Remove should report false (no-op)")
	}
}

func TestProject_CorruptJSONLLineIsDropped(t *testing.T) {
	cwd, home := withMemoryEnv(t)

	p, _ := LoadOrCreate(cwd)
	if err := p.Add(Entry{Name: "good-1", Tagline: "ok"}); err != nil {
		t.Fatalf("Add good-1: %v", err)
	}
	if err := p.Add(Entry{Name: "good-2", Tagline: "ok"}); err != nil {
		t.Fatalf("Add good-2: %v", err)
	}

	// Inject a corrupt line between the two good ones by rewriting the
	// file directly. The bufio.Scanner-based loader should drop the
	// bad line and keep both good entries.
	jsonl := filepath.Join(home, "projects", p.ID, "memory.jsonl")
	data, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	corrupted := append([]byte{}, data...)
	corrupted = append(corrupted[:strings.Index(string(corrupted), "\n")+1], append(
		[]byte("{ this is not valid json }\n"),
		corrupted[strings.Index(string(corrupted), "\n")+1:]...,
	)...)
	if err := os.WriteFile(jsonl, corrupted, 0o644); err != nil {
		t.Fatalf("write corrupted jsonl: %v", err)
	}

	reloaded, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("reload after corruption: %v", err)
	}
	if _, ok := reloaded.Get("good-1"); !ok {
		t.Errorf("good-1 dropped after recovery")
	}
	if _, ok := reloaded.Get("good-2"); !ok {
		t.Errorf("good-2 dropped after recovery")
	}
}

func TestLoadOrCreate_InvalidPointerFallsBackToHash(t *testing.T) {
	cwd, _ := withMemoryEnv(t)

	// Plant a malformed pointer file: not 16 hex chars.
	if err := os.MkdirAll(filepath.Join(cwd, ".seek"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(cwd, ".seek", "project-id"),
		[]byte("not-a-valid-id\n"),
		0o644,
	); err != nil {
		t.Fatalf("write pointer: %v", err)
	}

	p, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	expected := projectID(p.AbsPath)
	if p.ID != expected {
		t.Errorf("invalid pointer should fall back to hash %q, got %q", expected, p.ID)
	}

	// LoadOrCreate should have replaced the pointer with a valid one.
	data, _ := os.ReadFile(filepath.Join(cwd, ".seek", "project-id"))
	if strings.TrimSpace(string(data)) != expected {
		t.Errorf("pointer not repaired; got %q want %q", data, expected)
	}
}

// errorsContains is a tiny shim so this test file doesn't need to
// import errors twice. Mirrors errors.Is.
func errorsContains(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		un, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = un.Unwrap()
	}
	return false
}
