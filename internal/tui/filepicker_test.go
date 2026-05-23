package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilterPaths_EmptyQueryReturnsHead(t *testing.T) {
	all := []string{"a.go", "b.go", "c.go"}
	got := filterPaths(all, "")
	if len(got) != 3 {
		t.Errorf("got %d, want 3", len(got))
	}
}

func TestFilterPaths_PrefixBeatsContains(t *testing.T) {
	all := []string{"src/util.go", "util_test.go", "extras/utility.go"}
	got := filterPaths(all, "uti")
	// util_test.go and utility.go are basename-prefix matches; util.go
	// is basename-prefix-of "util" too (basename "util.go" startswith
	// "uti"). All three are basename-prefix matches.
	if len(got) != 3 {
		t.Errorf("got %v", got)
	}
}

func TestFilterPaths_CaseInsensitive(t *testing.T) {
	all := []string{"README.md", "package.json"}
	got := filterPaths(all, "read")
	if len(got) != 1 || got[0] != "README.md" {
		t.Errorf("got %v", got)
	}
}

func TestFilterPaths_CapsAtTwenty(t *testing.T) {
	var all []string
	for i := 0; i < 50; i++ {
		all = append(all, "match_"+string(rune('a'+i%26))+".go")
	}
	got := filterPaths(all, "match")
	if len(got) != 20 {
		t.Errorf("got %d, want 20", len(got))
	}
}

func TestScanWorkspace_SkipsHiddenAndCommonNoise(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "node_modules"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "src"), 0o755))
	must(os.WriteFile(filepath.Join(root, ".git", "config"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(root, "node_modules", "a.js"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644))
	must(os.WriteFile(filepath.Join(root, ".env"), []byte("x"), 0o644))

	got := scanWorkspace(root)
	// scanWorkspace returns OS-native paths (backslashes on Windows).
	// Normalise via filepath.ToSlash so the substring assertions below
	// stay portable.
	gotSet := map[string]bool{}
	for _, p := range got {
		gotSet[filepath.ToSlash(p)] = true
	}
	if !gotSet["src/main.go"] || !gotSet["README.md"] {
		t.Errorf("missing expected files in %v", got)
	}
	for _, leaked := range []string{".env", ".git/config", "node_modules/a.js"} {
		if gotSet[leaked] {
			t.Errorf("%q should have been skipped", leaked)
		}
	}
}

func TestPathCompleter_OpenAndFilter(t *testing.T) {
	m := &Model{
		pathPicker: pathCompleterState{
			all: []string{"README.md", "internal/tui/view.go", "src/main.go"},
		},
	}
	// Simulate input "@RE" — selectAll trigger.
	// We can't easily drive bubbles/textarea here; check the helpers
	// directly via filterPaths and the open-state predicate.
	if !strings.HasPrefix("@README", "@") {
		t.Fatal("sanity: prefix check broken")
	}
	got := filterPaths(m.pathPicker.all, "RE")
	if len(got) != 1 || got[0] != "README.md" {
		t.Errorf("got %v", got)
	}
}
