package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSeekPixelBanner_Dimensions pins the box geometry. Future edits
// that change the wordmark width or row count have to also update this
// test, which is the point — silent layout drift on the brand mark is
// exactly what you don't want.
func TestSeekPixelBanner_Dimensions(t *testing.T) {
	lines := strings.Split(SeekPixelBanner, "\n")
	const (
		wantLines = 7  // top border + 5 letter rows + bottom border
		wantWidth = 19 // left frame + 17 inner + right frame
	)
	if len(lines) != wantLines {
		t.Fatalf("banner has %d lines, want %d", len(lines), wantLines)
	}
	for i, line := range lines {
		// utf8.RuneCountInString gets visible column count; len() would
		// count bytes and inflate it for the box-drawing characters.
		got := utf8.RuneCountInString(line)
		if got != wantWidth {
			t.Errorf("line %d width = %d runes, want %d: %q", i, got, wantWidth, line)
		}
	}
}

// TestSeekPixelBanner_BorderCharsAreTheRightShape pins the box-drawing
// glyphs in the corners. If someone "fixes" the rounded box back to a
// straight one (or, more likely, a different rounded variant), this
// catches it explicitly.
func TestSeekPixelBanner_BorderCharsAreTheRightShape(t *testing.T) {
	lines := strings.Split(SeekPixelBanner, "\n")
	top, bottom := lines[0], lines[len(lines)-1]
	if !strings.HasPrefix(top, "╭") || !strings.HasSuffix(top, "╮") {
		t.Errorf("top border lost rounded corners: %q", top)
	}
	if !strings.HasPrefix(bottom, "╰") || !strings.HasSuffix(bottom, "╯") {
		t.Errorf("bottom border lost rounded corners: %q", bottom)
	}
	// Content rows must start AND end with the vertical light-line.
	// Catches accidental mixed-frame edits.
	for i := 1; i < len(lines)-1; i++ {
		if !strings.HasPrefix(lines[i], "│") {
			t.Errorf("content line %d missing left vertical: %q", i, lines[i])
		}
		if !strings.HasSuffix(lines[i], "│") {
			t.Errorf("content line %d missing right vertical: %q", i, lines[i])
		}
	}
}

// TestSeekPixelBanner_BlockDistribution sanity-checks each row's block
// (█) count. The values are derived from the 3×5 SEEK letterforms; if
// you change letters, update these. Pre-fix the banner had a row of
// 4 indistinct block clusters that all looked the same — this test
// would have failed that.
func TestSeekPixelBanner_BlockDistribution(t *testing.T) {
	// Per-row block counts (excluding the frame chars). Derived from
	// the wordmark definition: rows 1 & 5 are "███ ███ ███ █ █"
	// (3+3+3+2 = 11 blocks); rows 2 & 4 have the lower bodies
	// (1+1+1+2 = 5 blocks); row 3 has the middle bars (2+2+2+1 = 7).
	want := []int{
		0,  // top border: no blocks
		11, // row 1: SEEK tops
		5,  // row 2: upper bodies
		7,  // row 3: middle bars
		5,  // row 4: lower bodies
		11, // row 5: SEEK bottoms
		0,  // bottom border: no blocks
	}

	lines := strings.Split(SeekPixelBanner, "\n")
	for i, line := range lines {
		got := strings.Count(line, "█")
		if got != want[i] {
			t.Errorf("line %d block count = %d, want %d: %q",
				i, got, want[i], line)
		}
	}
}

// TestSeekPixelBanner_SAndERowsDifferOnlyInRow4 is the letterform
// integrity check. S and E share top/middle/bottom — they only differ
// at row 4 (S has "  █", E has "█  "). If the banner accidentally
// converges them — e.g. someone "simplifies" by making row 4 all
// identical — the eye no longer reads SEEK distinctly.
func TestSeekPixelBanner_SAndERowsDifferOnlyInRow4(t *testing.T) {
	lines := strings.Split(SeekPixelBanner, "\n")
	if len(lines) < 7 {
		t.Fatal("banner missing rows; other tests will surface that")
	}

	// Slice each content line to extract the 4 letter cells.
	// Layout per row: │ + " " + S(3) + " " + E(3) + " " + E(3) + " " + K(3) + " " + │
	// Letter cells start at byte index... but the box chars are
	// multi-byte. Use rune slicing.
	cellAt := func(line string, col int) string {
		runes := []rune(line)
		// cols (0-indexed):
		//  0   1  2 3 4   5  6 7 8   9  10 11 12  13 14 15 16  17  18
		// │   .  S S S   .  E E E   .   E  E  E   .  K  K  K  .   │
		startCol := []int{2, 6, 10, 14}[col] // S, E, E, K
		return string(runes[startCol : startCol+3])
	}

	// Row 4 (index 4): S row 4 should differ from E row 4.
	row4 := lines[4]
	s := cellAt(row4, 0)
	e := cellAt(row4, 1)
	if s == e {
		t.Errorf("S and E collapse to the same shape on row 4 (%q == %q) — wordmark becomes EEEK", s, e)
	}

	// Rows 1, 2, 3, 5: S and E should be IDENTICAL (that's the design).
	// If they ever diverge on these rows, the wordmark stops being a
	// clean E-derived S.
	for _, rowIdx := range []int{1, 2, 3, 5} {
		row := lines[rowIdx]
		if cellAt(row, 0) != cellAt(row, 1) {
			t.Errorf("row %d: S (%q) and E (%q) shouldn't differ on this row",
				rowIdx, cellAt(row, 0), cellAt(row, 1))
		}
	}
}

// TestRenderPixelBanner_PreservesLayout verifies that the styled output
// has the same line count + visible structure as the raw const — i.e.
// lipgloss doesn't accidentally wrap, collapse, or reorder anything.
// We check VISIBLE characters by stripping ANSI escape sequences.
func TestRenderPixelBanner_PreservesLayout(t *testing.T) {
	styled := RenderPixelBanner()
	stripped := stripANSI(styled)
	if stripped != SeekPixelBanner {
		t.Errorf("styled banner doesn't match the source after stripping ANSI:\n--- got ---\n%s\n--- want ---\n%s",
			stripped, SeekPixelBanner)
	}
}

// (stripANSI lives in statusbar_test.go and is reused here.)
