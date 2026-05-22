package tui

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/pricing"
	"golang.org/x/term"
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
var gradientCyan = [3]lipgloss.Color{
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

// RenderPixelBanner returns the full wordmark with gradient colouring.
// Blocks render in their row's tier colour; everything else stays
// blank (terminal default background).
func RenderPixelBanner() string {
	return renderBanner(len(letterEndCols))
}

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

// animateBanner reveals the wordmark letter-by-letter using raw ANSI
// cursor-up + reprint. ~80ms per letter, ~320ms total — long enough
// to feel intentional, short enough that repeat invocations don't drag.
//
// MUST only be called when stdout is a TTY (no animation when piped).
// Caller is responsible for that check.
func animateBanner() {
	const frameDelay = 80 * time.Millisecond
	const bannerLines = 7 // matches len(seekRows)

	// Hide the cursor during animation so it doesn't bounce around;
	// defer restore covers panic paths too.
	fmt.Print("\x1b[?25l")
	defer fmt.Print("\x1b[?25h")

	for n := 0; n <= len(letterEndCols); n++ {
		if n > 0 {
			// Move back up to the top of the previously-drawn banner
			// so this frame overwrites it cleanly. \r returns to col 0;
			// \x1b[<k>A goes up k lines.
			fmt.Printf("\r\x1b[%dA", bannerLines)
		}
		fmt.Println(renderBanner(n))
		if n < len(letterEndCols) {
			time.Sleep(frameDelay)
		}
	}
}

// shouldAnimate gates the welcome animation. Returns false when
// stdout isn't a TTY (piped, print mode, dumb terminal) or when
// SEEK_NO_ANIM is set (CI, scripts, slow remote sessions).
func shouldAnimate() bool {
	if os.Getenv("SEEK_NO_ANIM") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// VersionString returns the build identity for the running binary as
// a single human-readable string. Pulled from runtime/debug.BuildInfo
// so it picks up the module version, short git hash, and `+` dirty
// marker automatically.
//
// Examples:
//
//	"v0.1.0 · abc1234"   — clean release
//	"dev · abc1234+"     — local build, uncommitted changes
//	"dev"                — local build, no VCS info
//	"unknown"            — buildinfo unavailable (extremely rare)
func VersionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return formatVersion(info)
}

// formatVersion is the pure-function core of VersionString. Split
// out so tests can construct synthetic BuildInfo without depending
// on the real build environment.
func formatVersion(info *debug.BuildInfo) string {
	version := info.Main.Version
	// "(devel)" is go-build's marker for a tag-less build. Pseudo-
	// versions like "v0.0.0-YYYYMMDDHHMMSS-<hash>" mean the same
	// thing — collapse both to "dev" so the line stays readable;
	// the short hash from vcs.revision carries the actual identity.
	switch {
	case version == "" || version == "(devel)":
		version = "dev"
	case strings.HasPrefix(version, "v0.0.0-"):
		version = "dev"
	}

	var (
		rev      string
		modified bool
	)
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) >= 7 {
				rev = s.Value[:7]
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}

	if rev == "" {
		return version
	}
	suffix := ""
	if modified {
		suffix = "+"
	}
	return fmt.Sprintf("%s · %s%s", version, rev, suffix)
}

// PrintPixelWelcomeBanner prints the welcome screen. Called by
// tui.Run BEFORE bubbletea takes over so the lines land in scrollback
// above the live region.
//
// Layout (option-B "pixel art done seriously"):
//
//	<blank>
//	<5×7 wordmark with cyan gradient>     ← animated when stdout is a TTY
//	<blank>
//	  <cwd>
//	  <model · tier [· YOLO] · version>
//	<blank>
//
// No tagline, no help line, no off-peak countdown — the status bar
// at the bottom already carries the live info. Welcome should set
// the brand, not be a dashboard.
func PrintPixelWelcomeBanner(opts Options) {
	tier := pricing.CurrentTier(time.Now())
	muted := lipgloss.NewStyle().Foreground(colourMuted)

	fmt.Println()
	if shouldAnimate() {
		animateBanner()
	} else {
		fmt.Println(RenderPixelBanner())
	}
	fmt.Println()

	// Two compact meta lines. cwd alone on its own line because it's
	// the thing the user actually needs to verify ("am I in the right
	// project?"); model/tier/version share a status line.
	fmt.Println(muted.Render("  " + opts.CWD))

	tierLabel := pricing.TierLabel(tier)
	if tier != pricing.TierOffPeak {
		tierLabel = "☀️ " + tierLabel
	} else {
		tierLabel = "🌙 " + tierLabel
	}
	status := fmt.Sprintf("%s  ·  %s", opts.Model, tierLabel)
	if opts.Yolo {
		status += "  ·  YOLO"
	}
	status += "  ·  " + VersionString()
	fmt.Println(muted.Render("  " + status))
	fmt.Println()
}
