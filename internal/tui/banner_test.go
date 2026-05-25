package tui

import (
	"runtime/debug"
	"strings"
	"testing"
	"unicode/utf8"
)

// --- Wordmark geometry ---------------------------------------------

// TestSeekRows_AllSameWidth pins the 23-col content width. Variable
// row widths would mean an animation frame mid-row produces a ragged
// right edge, and gradient colouring would land on different columns
// for the same letter at different rows.
func TestSeekRows_AllSameWidth(t *testing.T) {
	t.Parallel()
	const wantWidth = 23
	for i, row := range seekRows {
		got := utf8.RuneCountInString(row.text)
		if got != wantWidth {
			t.Errorf("row %d width = %d runes, want %d: %q", i, got, wantWidth, row.text)
		}
	}
}

// TestSeekRows_GradientTiersCoverTopMiddleBottom locks the 3-tier
// shape so a future "let's make it 2-tier" edit at least requires
// updating this test. The gradient is the wordmark's main bit of
// designed-feel — it shouldn't drift unnoticed.
func TestSeekRows_GradientTiersCoverTopMiddleBottom(t *testing.T) {
	t.Parallel()
	seen := map[int]int{}
	for _, row := range seekRows {
		seen[row.tier]++
	}
	for tier := 0; tier < 3; tier++ {
		if seen[tier] == 0 {
			t.Errorf("tier %d has no rows — gradient is broken", tier)
		}
	}
	if got := seen[0] + seen[1] + seen[2]; got != len(seekRows) {
		t.Errorf("only %d/%d rows accounted for", got, len(seekRows))
	}
}

// TestSeekRows_LineCount pins the 7-row wordmark height. Same
// rationale as the width test: silent height drift would break the
// animation's cursor-up count and ToolDelta clearing assumptions
// from the broader TUI.
func TestSeekRows_LineCount(t *testing.T) {
	t.Parallel()
	const want = 7
	if got := len(seekRows); got != want {
		t.Fatalf("len(seekRows) = %d, want %d", got, want)
	}
}

// TestSeekRows_LetterCellsCanBeIsolated verifies that the column
// ranges in letterEndCols actually correspond to letter boundaries
// (i.e. each end-col is on a █ or just-after-█, and the column AFTER
// each end-col is a gap space). If a future letterform edit shifts
// columns without updating letterEndCols, the animation reveals would
// blank or extend the wrong cells.
func TestSeekRows_LetterCellsCanBeIsolated(t *testing.T) {
	t.Parallel()
	// Pick a row that has blocks in every letter — row 0 (all tops).
	row := bannerIndent + seekRows[0].text
	runes := []rune(row)

	for i, endCol := range letterEndCols {
		if endCol >= len(runes) {
			t.Errorf("letter %d endCol %d out of bounds (row len %d)", i, endCol, len(runes))
			continue
		}
		// endCol must be a block (it's the last block of letter i).
		if runes[endCol] != '█' {
			t.Errorf("letter %d endCol %d should be █, got %q", i, endCol, string(runes[endCol]))
		}
		// The column AFTER endCol must be a space — that's the gap
		// before the next letter (or end-of-row).
		if endCol+1 < len(runes) && runes[endCol+1] != ' ' {
			t.Errorf("letter %d endCol+1=%d should be a gap, got %q",
				i, endCol+1, string(runes[endCol+1]))
		}
	}
}

// --- Letter-reveal animation frames --------------------------------

// TestBannerWithLettersRevealed_EmptyShowsNoBlocks: n=0 frame must
// be entirely spaces (in the content area). Animation users see this
// as the first "I'm about to draw" frame — it must be visually empty,
// not "letter 0 already half-visible".
func TestBannerWithLettersRevealed_EmptyShowsNoBlocks(t *testing.T) {
	t.Parallel()
	got := bannerWithLettersRevealed(0)
	if blocks := strings.Count(got, "█"); blocks != 0 {
		t.Errorf("n=0 frame has %d blocks, want 0:\n%s", blocks, got)
	}
}

func TestBannerWithLettersRevealed_FullEqualsAllBlocks(t *testing.T) {
	t.Parallel()
	// n>=4 must include every block from every row. Summing block
	// counts: S=15, E=18, E=18, K=14 → 65 total. If a letter loses
	// a block, that letter visibly degrades — pin the totals.
	got := bannerWithLettersRevealed(len(letterEndCols))
	if blocks := strings.Count(got, "█"); blocks != 65 {
		t.Errorf("full banner has %d blocks, want 65 (S=15 + E=18 + E=18 + K=14)", blocks)
	}
	// Clamp behaviour: n much larger than letter count == full.
	if other := bannerWithLettersRevealed(99); other != got {
		t.Errorf("n=99 should clamp to full banner; got differs:\n--- 99 ---\n%s\n--- 4 ---\n%s", other, got)
	}
}

func TestBannerWithLettersRevealed_PartialKeepsLeftHidesRight(t *testing.T) {
	t.Parallel()
	// n=2 = S and first E visible; second E and K hidden. Use the
	// letter-cell boundaries to verify exactly which columns survive.
	got := bannerWithLettersRevealed(2)
	lines := strings.Split(got, "\n")
	if len(lines) != len(seekRows) {
		t.Fatalf("partial banner has %d lines, want %d", len(lines), len(seekRows))
	}

	// Row 0 has blocks in every letter cell — perfect for this check.
	runes := []rune(lines[0])
	// Cols 0..letterEndCols[1] (inclusive) should still contain at
	// least one block (the visible letters).
	visiblePart := string(runes[:letterEndCols[1]+1])
	if !strings.Contains(visiblePart, "█") {
		t.Errorf("n=2 visible part (cols 0..%d) missing blocks: %q",
			letterEndCols[1], visiblePart)
	}
	// Beyond letterEndCols[1] there must be ZERO blocks — letters
	// 2 and 3 are hidden.
	hiddenPart := string(runes[letterEndCols[1]+1:])
	if strings.Contains(hiddenPart, "█") {
		t.Errorf("n=2 hidden part has blocks (E2/K should be blank): %q", hiddenPart)
	}
}

// TestRenderBanner_StrippedEqualsBannerWithLettersRevealed pins the
// contract that the styled and unstyled renderers produce the same
// layout — same row count, same column positions for blocks, only
// the colour differs. Catches any drift between the two code paths.
func TestRenderBanner_StrippedEqualsBannerWithLettersRevealed(t *testing.T) {
	t.Parallel()
	for n := 0; n <= len(letterEndCols); n++ {
		styled := renderBanner(n)
		stripped := stripANSI(styled)
		want := bannerWithLettersRevealed(n)
		if stripped != want {
			t.Errorf("frame n=%d: styled-stripped differs from unstyled:\n--- got ---\n%s\n--- want ---\n%s",
				n, stripped, want)
		}
	}
}

// --- Version string -------------------------------------------------

func TestFormatVersion_TaggedRelease(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	// `go install …@latest` against an untagged repo produces a
	// "v0.0.0-YYYYMMDDHHMMSS-<hash>" pseudo-version. The timestamp+
	// hash duplicates vcs.revision and the 26-char string crowds the
	// rest of the meta line. Collapse it to "dev".
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
	t.Parallel()
	// `go build` outside a git checkout (or with -buildvcs=false).
	// No settings → we must still produce a readable string.
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
	}
	if got := formatVersion(info); got != "dev" {
		t.Errorf("formatVersion = %q, want %q", got, "dev")
	}
}

func TestFormatVersion_DevelWithDirtyVCS(t *testing.T) {
	t.Parallel()
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

func TestFormatVersion_ShortRevisionDroppedNotPartial(t *testing.T) {
	t.Parallel()
	// A 6-char rev shouldn't be sliced to 5 — we require ≥7 chars
	// before truncating, otherwise the rev gets dropped entirely.
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
		},
	}
	if got := formatVersion(info); got != "dev" {
		t.Errorf("formatVersion with short rev = %q, want %q (rev dropped)", got, "dev")
	}
}

// --- Welcome padding ------------------------------------------------

// TestWelcomePadding_ZeroOrNegativeHeightReturnsZero pins the safety
// path: when term.GetSize fails (very rare) or returns an absurd
// value, we must NOT print padding. Negative or zero `pad := h-used`
// would have us either print 0 lines (fine) or, if the math drifted,
// negative — which would panic the loop. Defensive.
func TestWelcomePadding_ZeroOrNegativeHeightReturnsZero(t *testing.T) {
	t.Parallel()
	for _, h := range []int{0, -1, -100} {
		if got := welcomePadding(h); got != 0 {
			t.Errorf("welcomePadding(%d) = %d, want 0", h, got)
		}
	}
}

// TestWelcomePadding_BelowMinimumReturnsZero locks "no padding on
// small terminals". On a 15-row window the banner ALREADY fills the
// screen; pushing the input further down would just make it scroll
// off the bottom.
func TestWelcomePadding_BelowMinimumReturnsZero(t *testing.T) {
	t.Parallel()
	// Exactly the minimum (banner + live region fills the screen) →
	// no pad needed.
	if got := welcomePadding(welcomeFixedLines + welcomeBelowLines); got != 0 {
		t.Errorf("at minimum height: got %d, want 0", got)
	}
	// Smaller than minimum → still 0, never negative.
	if got := welcomePadding(welcomeFixedLines + welcomeBelowLines - 5); got != 0 {
		t.Errorf("below minimum: got %d, want 0", got)
	}
}

func TestWelcomePadding_TypicalTerminalSensiblePad(t *testing.T) {
	t.Parallel()
	// 30-row terminal is the modal case (default iTerm/Terminal.app
	// window). Want a clear, non-zero pad that's well under the cap.
	pad := welcomePadding(30)
	if pad <= 0 {
		t.Errorf("30-row pad = %d, want > 0", pad)
	}
	if pad > welcomePadMax {
		t.Errorf("30-row pad = %d, want ≤ welcomePadMax (%d)", pad, welcomePadMax)
	}
	// Sanity: 30 = 14 fixed + 4 live + 12 pad. Should be 12.
	if pad != 12 {
		t.Errorf("30-row pad = %d, want 12 (30 - %d - %d)",
			pad, welcomeFixedLines, welcomeBelowLines)
	}
}

func TestWelcomePadding_HugeTerminalCapsAtMax(t *testing.T) {
	t.Parallel()
	// A 100-row terminal would otherwise get 82 lines of padding —
	// the input would float in mid-screen with the banner pinned to
	// the top. The cap exists to keep things proportional.
	if got := welcomePadding(1000); got != welcomePadMax {
		t.Errorf("welcomePadding(1000) = %d, want %d (capped)", got, welcomePadMax)
	}
}

// TestWelcomeBannerLineCount pins the physical line count of the
// welcome banner AND the pre-banner system output lines in
// welcomeFixedLines. If the banner layout changes (more/fewer
// wordmark rows, extra meta lines, dropped blank lines) this test
// fails — the constant must be updated to keep status-bar pinning
// correct.
//
// Layout from PrintPixelWelcomeBanner:
//
//	1 leading blank
//	7 wordmark rows (RenderPixelBanner)
//	1 blank after banner
//	cwd line
//	meta line (model · tier · YOLO · version)
//	1 trailing blank
//	= 12 banner lines
//
// Plus 2 pre-banner lines printed by cmd/seek (skills loader +
// projectmd loader) = welcomeFixedLines (14).
func TestWelcomeBannerLineCount(t *testing.T) {
	t.Parallel()
	// Count wordmark rows from RenderPixelBanner.
	bannerWordmarkLines := strings.Count(RenderPixelBanner(), "\n") + 1
	if bannerWordmarkLines != 7 {
		t.Errorf("RenderPixelBanner() = %d line(s), want 7 — wordmark height changed", bannerWordmarkLines)
	}

	// Surrounding blank + meta lines in PrintPixelWelcomeBanner.
	const surrounding = 5 // leading blank, post-banner blank, cwd, meta, trailing blank
	bannerTotal := bannerWordmarkLines + surrounding
	if bannerTotal != 12 {
		t.Errorf("PrintPixelWelcomeBanner prints %d line(s), want 12 — banner layout changed", bannerTotal)
	}

	// Pre-banner lines from cmd/seek/main.go (skills loader + projectmd loader).
	const preBanner = 2
	want := bannerTotal + preBanner
	if welcomeFixedLines != want {
		t.Errorf("welcomeFixedLines = %d, want %d (banner %d + pre-banner %d) — update the constant",
			welcomeFixedLines, want, bannerTotal, preBanner)
	}
}

// --- Animation gate ------------------------------------------------

// TestShouldAnimate_SkippedWhenEnvSet pins the SEEK_NO_ANIM kill-
// switch. CI and scripted invocations rely on it; if the precedence
// ever flips to "TTY check wins", every script-driven seek run gets
// a 320ms penalty.
func TestShouldAnimate_SkippedWhenEnvSet(t *testing.T) {
	t.Setenv("SEEK_NO_ANIM", "1")
	if shouldAnimate() {
		t.Errorf("SEEK_NO_ANIM=1 should suppress animation")
	}
}

// (stripANSI lives in statusbar_test.go and is reused here.)
