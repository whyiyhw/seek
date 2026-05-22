package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectID_DeterministicAndShape(t *testing.T) {
	a := projectID("/Users/whyiyhw/code/github/seek")
	b := projectID("/Users/whyiyhw/code/github/seek")
	if a != b {
		t.Errorf("projectID should be deterministic, got %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("projectID should be 16 chars, got %d (%q)", len(a), a)
	}
	if !isValidProjectID(a) {
		t.Errorf("projectID output should be valid, got %q", a)
	}

	other := projectID("/Users/whyiyhw/code/github/other")
	if other == a {
		t.Errorf("different paths should yield different IDs")
	}
}

func TestIsValidProjectID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"a3b9f1c2e8d4a7b6", true},
		{"0123456789abcdef", true},
		{"a3b9f1c2e8d4a7b", false},   // 15 chars
		{"a3b9f1c2e8d4a7b66", false}, // 17 chars
		{"a3b9f1c2e8d4A7b6", false},  // uppercase A
		{"a3b9f1c2e8d4z7b6", false},  // non-hex
		{"", false},
		{"        ", false},
	}
	for _, c := range cases {
		if got := isValidProjectID(c.id); got != c.want {
			t.Errorf("isValidProjectID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

func TestAtomicWrite_CreatesParentAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "deeper", "file.txt")

	if err := atomicWrite(target, []byte("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read after first write: %v", err)
	}
	if string(got) != "first" {
		t.Errorf("first read = %q, want %q", got, "first")
	}

	if err := atomicWrite(target, []byte("second")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ = os.ReadFile(target)
	if string(got) != "second" {
		t.Errorf("second read = %q, want %q", got, "second")
	}

	// No leftover .tmp file after successful write.
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("expected no .tmp leftover, got err=%v", err)
	}
}
