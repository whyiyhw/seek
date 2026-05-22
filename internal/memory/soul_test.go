package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleSoul = `---
schema_version: 1
updated_at: 2026-05-22T13:15:00Z
---

# User profile

## Stable（已确认 ≥3 次会话无反例）

- **倾向显式错误处理胜过 panic**
  - 来源：proj-seek (3 sessions)

- **代码风格偏简洁**

## Pending（做梦候选）

- **倾向中文回答而非英文**
  - 来源：proj-seek (2 sessions)
`

func writeSoulFile(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	if content != "" {
		if err := os.WriteFile(filepath.Join(home, "soul.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write soul.md: %v", err)
		}
	}
	return home
}

func TestLoadSoul_MissingFileReturnsZero(t *testing.T) {
	writeSoulFile(t, "") // no file
	s, err := LoadSoul()
	if err != nil {
		t.Fatalf("LoadSoul on missing file: %v", err)
	}
	if s.Raw != "" {
		t.Errorf("expected empty Raw on missing file, got %q", s.Raw)
	}
	if s.Stable != "" || s.Pending != "" {
		t.Errorf("expected empty sections on missing file")
	}
	if !strings.HasSuffix(s.Path, "soul.md") {
		t.Errorf("Path should still resolve; got %q", s.Path)
	}
}

func TestLoadSoul_ParsesFrontmatterAndSections(t *testing.T) {
	writeSoulFile(t, sampleSoul)

	s, err := LoadSoul()
	if err != nil {
		t.Fatalf("LoadSoul: %v", err)
	}
	if s.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", s.SchemaVersion)
	}
	want, _ := time.Parse(time.RFC3339, "2026-05-22T13:15:00Z")
	if !s.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", s.UpdatedAt, want)
	}

	if !strings.Contains(s.Stable, "显式错误处理胜过 panic") {
		t.Errorf("Stable body missing expected bullet: %q", s.Stable)
	}
	if !strings.Contains(s.Stable, "代码风格偏简洁") {
		t.Errorf("Stable should include second bullet: %q", s.Stable)
	}
	// Section boundary: Stable must NOT include Pending content.
	if strings.Contains(s.Stable, "倾向中文回答") {
		t.Errorf("Stable bled into Pending: %q", s.Stable)
	}

	if !strings.Contains(s.Pending, "倾向中文回答而非英文") {
		t.Errorf("Pending body missing expected bullet: %q", s.Pending)
	}
}

func TestLoadSoul_NoFrontmatterStillExtractsSections(t *testing.T) {
	body := `# User profile

## Stable

- foo

## Pending

- bar
`
	writeSoulFile(t, body)

	s, err := LoadSoul()
	if err != nil {
		t.Fatalf("LoadSoul: %v", err)
	}
	if s.SchemaVersion != 0 {
		t.Errorf("SchemaVersion should be 0 when no frontmatter, got %d", s.SchemaVersion)
	}
	if !strings.Contains(s.Stable, "foo") {
		t.Errorf("Stable should contain 'foo', got %q", s.Stable)
	}
	if !strings.Contains(s.Pending, "bar") {
		t.Errorf("Pending should contain 'bar', got %q", s.Pending)
	}
}

func TestLoadSoul_OnlyOneSection(t *testing.T) {
	body := `## Stable

- only stable, no pending header
`
	writeSoulFile(t, body)

	s, err := LoadSoul()
	if err != nil {
		t.Fatalf("LoadSoul: %v", err)
	}
	if !strings.Contains(s.Stable, "only stable") {
		t.Errorf("Stable not captured")
	}
	if s.Pending != "" {
		t.Errorf("Pending should be empty, got %q", s.Pending)
	}
}

func TestSoulSave_RoundTripsRaw(t *testing.T) {
	home := writeSoulFile(t, sampleSoul)

	s, err := LoadSoul()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Modify Raw (simulating what `seek -dream` would do) and save.
	s.Raw = "# rewritten\n\n## Stable\n\n- new bullet\n"
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// File on disk should contain exactly the new Raw.
	got, err := os.ReadFile(filepath.Join(home, "soul.md"))
	if err != nil {
		t.Fatalf("read after Save: %v", err)
	}
	if string(got) != s.Raw {
		t.Errorf("Save didn't round-trip; got %q want %q", got, s.Raw)
	}

	// Re-loading parses the new content.
	s2, _ := LoadSoul()
	if !strings.Contains(s2.Stable, "new bullet") {
		t.Errorf("reload didn't see new content: %q", s2.Stable)
	}
}

func TestSoulSave_CreatesParentDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	// Don't create the .seek root — Save should mkdir as needed.
	s := &Soul{Raw: "fresh\n"}
	if err := s.Save(); err != nil {
		t.Fatalf("Save into fresh home: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "soul.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "fresh\n" {
		t.Errorf("Save into fresh home wrote %q, want %q", got, "fresh\n")
	}
}
