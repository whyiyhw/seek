package projectmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func writeMD(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_FoundAtCwd(t *testing.T) {
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "AGENTS.md"), "# rules\n- be careful\n")
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path == "" {
		t.Fatalf("expected to find AGENTS.md, got empty path")
	}
	if !strings.Contains(got.Content, "be careful") {
		t.Errorf("content = %q", got.Content)
	}
	if got.Bytes == 0 {
		t.Errorf("Bytes not set")
	}
}

func TestLoad_FoundAtParent(t *testing.T) {
	// AGENTS.md at <root>, cwd at <root>/src/pkg/sub — should walk up.
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "AGENTS.md"), "parent rules\n")
	deep := filepath.Join(root, "src", "pkg", "sub")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Load(deep)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path == "" || !strings.Contains(got.Content, "parent rules") {
		t.Errorf("did not walk up; got=%+v", got)
	}
}

func TestLoad_NearestWins(t *testing.T) {
	// When both <root>/AGENTS.md and <root>/sub/AGENTS.md exist, the
	// nearer one wins — a subproject can override its parent's rules.
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "AGENTS.md"), "outer rules\n")
	sub := filepath.Join(root, "sub")
	writeMD(t, filepath.Join(sub, "AGENTS.md"), "inner rules\n")

	got, err := Load(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Content, "inner rules") {
		t.Errorf("expected inner to win, got %q", got.Content)
	}
	if strings.Contains(got.Content, "outer rules") {
		t.Errorf("outer leaked through: %q", got.Content)
	}
}

func TestLoad_NoneFoundReturnsEmpty(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "" || got.Content != "" {
		t.Errorf("expected empty result, got %+v", got)
	}
}

func TestLoad_AscendBounded(t *testing.T) {
	// Place AGENTS.md at <root>, then descend deeper than maxAscend.
	// We expect Load NOT to find it — the cap exists so seek doesn't
	// scan the entire filesystem from a deep working dir.
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "AGENTS.md"), "out of reach\n")

	deep := root
	for i := 0; i < maxAscend+2; i++ {
		deep = filepath.Join(deep, "x")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Load(deep)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "" {
		t.Errorf("ascend cap not enforced: found %s from depth %d", got.Path, maxAscend+2)
	}
}

func TestLoad_TruncatesOversizedFile(t *testing.T) {
	root := t.TempDir()
	// Write maxBytes + 100 bytes of 'a' followed by a sentinel that
	// MUST not survive truncation.
	big := strings.Repeat("a", maxBytes+10) + "SENTINEL"
	writeMD(t, filepath.Join(root, "AGENTS.md"), big)

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncate {
		t.Errorf("Truncate flag not set on oversized file")
	}
	if strings.Contains(got.Content, "SENTINEL") {
		t.Errorf("content past the cap leaked through")
	}
	if !strings.Contains(got.Content, "truncated") {
		t.Errorf("missing truncation marker: %q", got.Content[len(got.Content)-200:])
	}
}

func TestSection_EmptyForMissing(t *testing.T) {
	if got := (Result{}).Section(); got != "" {
		t.Errorf("empty Result.Section = %q, want empty string", got)
	}
}

func TestSection_LabelsSource(t *testing.T) {
	r := Result{Content: "x", Path: "/tmp/AGENTS.md"}
	out := r.Section()
	for _, want := range []string{"# Project instructions", "/tmp/AGENTS.md", "x"} {
		if !strings.Contains(out, want) {
			t.Errorf("Section missing %q:\n%s", want, out)
		}
	}
}

func TestLoad_Truncates4ByteEmojiAtBoundary(t *testing.T) {
	root := t.TempDir()
	// Emoji "🚀" (U+1F680) is 4 bytes in UTF-8: F0 9F 9A 80.
	// Fill to maxBytes-3, then append the emoji → total = maxBytes+1.
	body := strings.Repeat("a", maxBytes-3) + "🚀"
	writeMD(t, filepath.Join(root, "AGENTS.md"), body)

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncate {
		t.Errorf("Truncate flag not set")
	}
	if !utf8.ValidString(got.Content) {
		t.Errorf("Content is not valid UTF-8 after truncation (raw bytes): %#v",
			[]byte(got.Content))
	}
	if strings.Contains(got.Content, "🚀") {
		t.Errorf("4-byte emoji leaked through truncation")
	}
}

func TestLoad_TruncatesMultiByteAtBoundary(t *testing.T) {
	root := t.TempDir()
	// Fill to maxBytes-2 then append a 3-byte Chinese character "界" (U+754C).
	// Total = maxBytes+1 → triggers truncation.
	// Byte-level cut at maxBytes would split "界" into 0xE7 0x95 | 0x8C.
	body := strings.Repeat("a", maxBytes-2) + "界"
	writeMD(t, filepath.Join(root, "AGENTS.md"), body)

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncate {
		t.Errorf("Truncate flag not set")
	}
	// The entire Content must be valid UTF-8 — no broken runes.
	if !utf8.ValidString(got.Content) {
		t.Errorf("Content is not valid UTF-8 after truncation (raw bytes): %#v",
			[]byte(got.Content))
	}
	// The multi-byte character "界" should be completely removed,
	// not partially present as garbled bytes.
	if strings.Contains(got.Content, "界") {
		t.Errorf("Multi-byte character leaked through truncation")
	}
}

func TestLoad_TruncatesExactBoundary_NoMultiByteSplit(t *testing.T) {
	root := t.TempDir()
	// Exactly maxBytes bytes of ASCII — no multi-byte boundary issue.
	body := strings.Repeat("x", maxBytes)
	writeMD(t, filepath.Join(root, "AGENTS.md"), body)

	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Truncate {
		t.Errorf("Truncate flag should be false for exact boundary")
	}
	if !utf8.ValidString(got.Content) {
		t.Errorf("Content is not valid UTF-8")
	}
	if len(got.Content) != maxBytes {
		t.Errorf("Content length = %d, want %d", len(got.Content), maxBytes)
	}
}
