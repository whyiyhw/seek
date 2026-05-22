package diff

import (
	"strings"
	"testing"
)

func TestUnified_Identical(t *testing.T) {
	if got := Unified("abc\n", "abc\n", "f.go"); got != "" {
		t.Errorf("identical content should return empty, got %q", got)
	}
}

func TestUnified_SingleLineChange(t *testing.T) {
	old := "line1\nline2\nline3\n"
	new_ := "line1\nLINE2\nline3\n"
	out := Unified(old, new_, "f.go")

	if !strings.Contains(out, "--- a/f.go") {
		t.Error("missing old header")
	}
	if !strings.Contains(out, "+++ b/f.go") {
		t.Error("missing new header")
	}
	if !strings.Contains(out, "-line2") {
		t.Error("missing deletion marker")
	}
	if !strings.Contains(out, "+LINE2") {
		t.Error("missing insertion marker")
	}
	// Context lines
	if !strings.Contains(out, " line1") {
		t.Error("missing context line before change")
	}
	if !strings.Contains(out, " line3") {
		t.Error("missing context line after change")
	}
}

func TestUnified_Insertion(t *testing.T) {
	old := "a\nb\nc\n"
	new_ := "a\nb\nNEW\nc\n"
	out := Unified(old, new_, "f.go")
	if !strings.Contains(out, "+NEW") {
		t.Errorf("missing inserted line: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			t.Errorf("unexpected deletion line in pure insertion: %q\nfull output:\n%s", line, out)
		}
	}
}

func TestUnified_Deletion(t *testing.T) {
	old := "a\nb\nc\n"
	new_ := "a\nc\n"
	out := Unified(old, new_, "f.go")
	if !strings.Contains(out, "-b") {
		t.Errorf("missing deleted line: %s", out)
	}
}

func TestUnified_HunkHeader(t *testing.T) {
	// Change on line 5 of a 10-line file — hunk should reference that region.
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = strings.Repeat("x", i+1)
	}
	old := strings.Join(lines, "\n") + "\n"
	lines[4] = "CHANGED"
	new_ := strings.Join(lines, "\n") + "\n"
	out := Unified(old, new_, "f.go")
	if !strings.Contains(out, "@@") {
		t.Errorf("missing @@ hunk header: %s", out)
	}
}

func TestUnified_TwoDistantHunks(t *testing.T) {
	// Changes far apart should produce two separate hunks.
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = "line"
	}
	old := strings.Join(lines, "\n") + "\n"
	lines[1] = "CHANGED_EARLY"
	lines[28] = "CHANGED_LATE"
	new_ := strings.Join(lines, "\n") + "\n"
	out := Unified(old, new_, "f.go")
	count := strings.Count(out, "@@")
	if count < 2 {
		t.Errorf("expected ≥2 @@ hunks for distant changes, got %d:\n%s", count, out)
	}
}

func TestUnified_ContextBoundary(t *testing.T) {
	// Change at line 1 — context should not produce negative line refs.
	old := "FIRST\nline2\nline3\n"
	new_ := "CHANGED\nline2\nline3\n"
	out := Unified(old, new_, "f.go")
	if !strings.Contains(out, "-FIRST") {
		t.Errorf("missing deletion: %s", out)
	}
	if !strings.Contains(out, "+CHANGED") {
		t.Errorf("missing insertion: %s", out)
	}
}

func TestUnified_NoTrailingNewline(t *testing.T) {
	// Files without trailing newline should not crash.
	old := "abc"
	new_ := "xyz"
	out := Unified(old, new_, "f.go")
	if !strings.Contains(out, "-abc") || !strings.Contains(out, "+xyz") {
		t.Errorf("unexpected output for no-trailing-newline: %s", out)
	}
}

func TestUnified_EmptyOld(t *testing.T) {
	out := Unified("", "new content\n", "f.go")
	if !strings.Contains(out, "+new content") {
		t.Errorf("expected insertion from empty: %s", out)
	}
}

func TestUnified_EmptyNew(t *testing.T) {
	out := Unified("old content\n", "", "f.go")
	if !strings.Contains(out, "-old content") {
		t.Errorf("expected deletion to empty: %s", out)
	}
}
