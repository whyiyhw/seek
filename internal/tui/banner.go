package tui

import (
	"image/color"
	"runtime/debug"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/whyiyhw/seek/internal/buildinfo"
)

// seekRow is one line of the wordmark plus its colour tier.
//
// Layout — each letter is 5×7, with 1-col gaps between letters; the
// whole wordmark is 23 chars wide before the 2-col left indent that
// makes the banner align with the meta line below.
//
// S has a 3-wide middle bar (cols 1–3) with the right side of the
// curve at row 6 ending early (trailing space) and the top arch
// starting late (leading space) — this is what reads as "S" rather
// than a literal rectangle. E uses a 4-wide middle bar so it sits
// distinguishable from a square. K's diagonals stair-step pixel-by-
// pixel in 5 cols (anything narrower loses legibility).
type seekRow struct {
	text string // 23-char content row, blocks (█) on coloured tier
	tier int    // 0 = lightest (top), 1 = mid, 2 = deepest (bottom)
}

var seekRows = []seekRow{
	{" ████ █████ █████ █   █", 0},
	{"█     █     █     █  █ ", 0},
	{"█     █     █     █ █  ", 0},
	{" ███  ████  ████  ██   ", 1},
	{"    █ █     █     █ █  ", 2},
	{"    █ █     █     █  █ ", 2},
	{"████  █████ █████ █   █", 2},
}

// gradientCyan is the 3-tier light→deep cyan ramp the wordmark uses.
// Index matches seekRow.tier. The middle tier (80) reads as the
// transition; rows above use the brand cyan (117 = colourUser), rows
// below use the deeper cyan to give the wordmark visual weight at the
// bottom.
var gradientCyan = [3]color.Color{
	lipgloss.Color("117"), // top
	lipgloss.Color("80"),  // middle
	lipgloss.Color("38"),  // bottom
}

// bannerIndent is the 2-col left margin applied to every wordmark row.
// Pinned as a const so animation-frame math and tests can rely on
// "letter cell i starts at column = bannerIndent + i*(5+1)".
const bannerIndent = "  "

// letterEndCols are the (0-indexed, rune-counted) last column of each
// letter cell IN THE INDENTED ROW. Used by bannerWithLettersRevealed
// to drive the letter-reveal animation: at frame n, blocks beyond
// letterEndCols[n-1] are masked to spaces.
//
//	col 2..6   = S      (letter 0)
//	col 8..12  = E      (letter 1)
//	col 14..18 = E      (letter 2)
//	col 20..24 = K      (letter 3)
var letterEndCols = [4]int{6, 12, 18, 24}

// renderBanner is the workhorse — `n` letters visible (clamped to
// the wordmark's letter count). Per-rune styling because each row
// uses a different colour tier and only the █ runes need colouring.
func renderBanner(n int) string {
	if n > len(letterEndCols) {
		n = len(letterEndCols)
	}
	cutoff := -1
	if n > 0 {
		cutoff = letterEndCols[n-1]
	}

	var out strings.Builder
	for i, row := range seekRows {
		if i > 0 {
			out.WriteByte('\n')
		}
		style := lipgloss.NewStyle().Foreground(gradientCyan[row.tier])
		// Convert to []rune so the loop index matches letterEndCols
		// (which counts runes, not bytes — █ is 3 UTF-8 bytes, so
		// a string-range loop would give byte indices that drift
		// out of sync with the cutoff after the first letter).
		runes := []rune(bannerIndent + row.text)
		for j, r := range runes {
			// Render as a coloured block only when the rune IS a
			// block AND it's within the revealed cutoff. n=0 sets
			// cutoff to -1 so the whole content area blanks; n>=4
			// sets it past the last column so everything renders.
			if r == '█' && j <= cutoff {
				out.WriteString(style.Render(string(r)))
			} else {
				out.WriteByte(' ')
			}
		}
	}
	return out.String()
}

// bannerWithLettersRevealed returns the wordmark for animation frame
// n, as raw text (no ANSI colour). Used by tests that want to assert
// on layout without dealing with escape codes. The string is a
// `\n`-joined block of rows including the indent.
func bannerWithLettersRevealed(n int) string {
	if n > len(letterEndCols) {
		n = len(letterEndCols)
	}
	cutoff := -1
	if n > 0 {
		cutoff = letterEndCols[n-1]
	}

	var lines []string
	for _, row := range seekRows {
		runes := []rune(bannerIndent + row.text)
		for j, r := range runes {
			if r == '█' && j <= cutoff {
				continue // keep
			}
			runes[j] = ' '
		}
		lines = append(lines, string(runes))
	}
	return strings.Join(lines, "\n")
}

// Creator is the human-readable author credit shown in the welcome banner.
const Creator = "whyiyhw"

// VersionString returns the build identity for the running binary as
// a single human-readable string ("v0.1.0 · abc1234", "dev · abc1234+",
// "dev", "unknown").
//
// The implementation moved to internal/buildinfo (card c_ni7mzw) the
// day a non-TUI surface — the web page's About row, via
// controlplane.ServeSettings.Version — needed the same string: the rule
// belongs to build identity, not to the render stack. This wrapper
// stays because it is what cmd/seek, the banner and the upgrade checks
// already call (and because it keeps this package's public surface
// unchanged). One formatting rule, two names.
func VersionString() string { return buildinfo.Version() }

// formatVersion is the pure-function core of VersionString, kept as a
// thin alias for the tests that construct synthetic BuildInfo
// (banner_test's four shapes).
func formatVersion(info *debug.BuildInfo) string { return buildinfo.Format(info) }

// pixelBannerMinWidth is the minimum terminal width (in cells) at
// which the 5×7 pixel wordmark renders cleanly. The full banner is
// ~30 cells of glyph + 2 of indent; below this width View() falls
// back to a one-line text banner. Empirically derived — the pixel
// banner row is 26 chars, leaving 4 cells of breathing room.
const pixelBannerMinWidth = 30
