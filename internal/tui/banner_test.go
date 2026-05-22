package tui

import (
	"runtime/debug"
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

// --- Letter-reveal animation frames --------------------------------

// TestBannerWithLettersRevealed_EmptyShowsNoBlocks pins the n=0 frame:
// the frame must be intact but ZERO blocks visible anywhere. If a
// future edit accidentally lets blocks bleed through, this catches it
// before the animation flashes "garbage frame" to users on startup.
func TestBannerWithLettersRevealed_EmptyShowsNoBlocks(t *testing.T) {
	got := bannerWithLettersRevealed(0)
	if blocks := strings.Count(got, "█"); blocks != 0 {
		t.Errorf("n=0 frame has %d blocks, want 0:\n%s", blocks, got)
	}
	// Frame chars must still be there — the animation only blanks
	// content, never the frame.
	if !strings.Contains(got, "╭") || !strings.Contains(got, "╯") {
		t.Errorf("n=0 frame lost border chars:\n%s", got)
	}
}

func TestBannerWithLettersRevealed_FullEqualsSource(t *testing.T) {
	// n>=4 must produce the exact final wordmark. If this drifts,
	// the animation's last frame won't match the static banner the
	// rest of the app shows.
	if got := bannerWithLettersRevealed(4); got != SeekPixelBanner {
		t.Errorf("n=4 frame differs from SeekPixelBanner:\n--- got ---\n%s\n--- want ---\n%s",
			got, SeekPixelBanner)
	}
	// n much larger than the letter count clamps to "full" — the
	// loop in animateBanner relies on this so callers don't have to
	// be precise about the upper bound.
	if got := bannerWithLettersRevealed(99); got != SeekPixelBanner {
		t.Errorf("n=99 should clamp to full, got:\n%s", got)
	}
}

func TestBannerWithLettersRevealed_PartialKeepsLeftHidesRight(t *testing.T) {
	// n=2 = S and E1 visible, E2 and K hidden. This is the middle
	// frame of the animation; visually you'd see "SE" with empty
	// space to the right.
	got := bannerWithLettersRevealed(2)
	lines := strings.Split(got, "\n")

	// Row 1 of the wordmark is "███ ███ ███ █ █" — at n=2 only the
	// first two letter-cells (cols 1-8) keep their blocks; cols 9-17
	// blanket to spaces.
	row1 := lines[1] // index 0 is the top border
	// Inner content of row1: from rune 1 to rune len-2 (skip frame).
	runes := []rune(row1)
	visiblePart := string(runes[1:9]) // cols 1..8 (S + gap + E1)
	hiddenPart := string(runes[9 : len(runes)-1])

	// First 8 cols must contain blocks (S and E1 are visible).
	if !strings.Contains(visiblePart, "█") {
		t.Errorf("n=2 row 1 visible part missing blocks: %q", visiblePart)
	}
	// Cols 9..17 must have NO blocks (E2 and K are hidden).
	if strings.Contains(hiddenPart, "█") {
		t.Errorf("n=2 row 1 hidden part has blocks (E2/K should be blank): %q", hiddenPart)
	}
}

// --- Version string -------------------------------------------------

// TestFormatVersion_TaggedRelease pins the "user installed via
// `go install …@v0.1.0`" path: clean version + short hash, no dirty
// marker. This is the format we expect when shipping releases.
func TestFormatVersion_TaggedRelease(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc1234deadbeef"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	got := formatVersion(info)
	want := "v0.1.0 · abc1234"
	if got != want {
		t.Errorf("formatVersion = %q, want %q", got, want)
	}
}

func TestFormatVersion_PseudoVersionCollapsesToDev(t *testing.T) {
	// Go emits "v0.0.0-YYYYMMDDHHMMSS-<hash>[+dirty]" for un-tagged
	// builds installed via @latest. The timestamp+hash duplicates
	// the vcs.revision we ALREADY show, and the long string just
	// pushes everything else off-screen. Verify we collapse it to
	// "dev" so the visible line stays clean.
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.0.0-20260522030436-1b028a40fcc5+dirty"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1b028a40fcc5"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := formatVersion(info)
	want := "dev · 1b028a4+"
	if got != want {
		t.Errorf("formatVersion = %q, want %q (pseudo-version should collapse)", got, want)
	}
}

func TestFormatVersion_DevelNoVCS(t *testing.T) {
	// "go build" outside a git checkout (or with -buildvcs=false)
	// gives us "(devel)" and no settings. Verify we still produce a
	// readable string — never panic on missing data.
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
	}
	if got := formatVersion(info); got != "dev" {
		t.Errorf("formatVersion = %q, want %q", got, "dev")
	}
}

func TestFormatVersion_DevelWithDirtyVCS(t *testing.T) {
	// Standard local dev experience: working on a feature branch
	// with uncommitted changes. The "+" marker is how we signal
	// "this binary doesn't correspond to a specific commit".
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "1234567abcdef"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := formatVersion(info)
	want := "dev · 1234567+"
	if got != want {
		t.Errorf("formatVersion = %q, want %q", got, want)
	}
}

func TestFormatVersion_ShortRevisionStillTruncates(t *testing.T) {
	// A 6-char revision shouldn't be partially truncated to 5; we
	// require ≥7 chars before slicing, otherwise we drop the rev.
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"}, // only 6 chars
		},
	}
	if got := formatVersion(info); got != "dev" {
		t.Errorf("formatVersion with short rev = %q, want %q (rev dropped)", got, "dev")
	}
}

// --- shouldAnimate environment gate ---------------------------------

// TestShouldAnimate_SkippedWhenEnvSet pins the SEEK_NO_ANIM kill-switch.
// CI and scripted invocations rely on it; if the precedence ever flips
// to "TTY check wins", every script-driven seek run gets a 320ms penalty.
func TestShouldAnimate_SkippedWhenEnvSet(t *testing.T) {
	t.Setenv("SEEK_NO_ANIM", "1")
	if shouldAnimate() {
		t.Errorf("SEEK_NO_ANIM=1 should suppress animation")
	}
}
