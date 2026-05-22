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

// SeekPixelBanner is the pixel-art "seek" wordmark used by the startup
// welcome screen. 17 inner columns × 5 letter rows in a 3×5 bitmap
// font, wrapped in a rounded box (19 × 7 total).
//
// Letterform notes — each letter is 3 columns wide with 1-column gaps:
//   S — full top/bottom bars, short middle bar (cols 1–2), lower-right
//       curve at row 4 col 3.
//   E — full top/bottom bars, short middle bar (cols 1–2), lower-left
//       strut at row 4 col 1. Distinguished from S only by row 4.
//   K — vertical strut on col 1, upper/lower arms at col 2, diagonal
//       endpoints at col 3 on rows 1 and 5.
//
// The block character is U+2588 FULL BLOCK (█). Frame uses light box-
// drawing characters; rounded corners (╭ ╮ ╰ ╯) over straight to look
// less heavy than the previous double-line border.
const SeekPixelBanner = `╭─────────────────╮
│ ███ ███ ███ █ █ │
│ █   █   █   ██  │
│ ██  ██  ██  █   │
│   █ █   █   ██  │
│ ███ ███ ███ █ █ │
╰─────────────────╯`

// pixelBannerTagline is the one-liner that sits under the wordmark.
// Kept short so it doesn't drag the welcome screen out vertically.
const pixelBannerTagline = "  seek · deepseek-first coding agent"

// VersionString returns the build identity for the running binary as a
// single human-readable string. Pulled from runtime/debug.BuildInfo so
// it picks up the module version (for released builds), the short git
// hash, and a `+` suffix when the working tree was dirty at build time.
//
// Examples:
//
//	"v0.1.0 · abc1234"   — clean release
//	"dev · abc1234+"     — local build, uncommitted changes
//	"dev"                — local build, no VCS info
//	"unknown"            — buildinfo unavailable (extremely rare)
//
// This exists at the TUI layer because the banner is the only place it
// matters today; promote to its own package the moment anyone else
// needs it.
func VersionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return formatVersion(info)
}

// formatVersion is the pure-function core of VersionString — broken
// out so tests can construct synthetic BuildInfo values without
// depending on the real build environment.
func formatVersion(info *debug.BuildInfo) string {
	version := info.Main.Version
	// "(devel)" is what go-build emits when there's no tag. Pseudo-
	// versions of the form "v0.0.0-YYYYMMDDHHMMSS-<hash>" mean the
	// same thing (the module hasn't been tagged); we'd rather show
	// "dev" than a 26-character timestamp string. A "+dirty" suffix
	// on the pseudo-version is redundant with our own modified flag.
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
		// "+" marker mirrors how git diff-index shows a dirty tree.
		suffix = "+"
	}
	return fmt.Sprintf("%s · %s%s", version, rev, suffix)
}

// RenderPixelBanner returns the wordmark with lipgloss styling: blocks
// in DeepSeek-brand cyan, frame in muted grey. Per-rune colouring is
// simple and fast enough for a one-time startup print; performance
// here is irrelevant.
func RenderPixelBanner() string {
	return renderBannerString(SeekPixelBanner)
}

func renderBannerString(raw string) string {
	block := lipgloss.NewStyle().Foreground(colourUser)
	frame := lipgloss.NewStyle().Foreground(colourMuted)

	var out strings.Builder
	for i, line := range strings.Split(raw, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		for _, r := range line {
			if r == '█' {
				out.WriteString(block.Render(string(r)))
			} else {
				out.WriteString(frame.Render(string(r)))
			}
		}
	}
	return out.String()
}

// letterEndCols are the (1-indexed, rune-counted) right-edges of each
// letter cell inside the 17-col content row. Used by
// bannerWithLettersRevealed to mask out cells beyond the current
// animation frame. See banner geometry comment on SeekPixelBanner.
var letterEndCols = [4]int{4, 8, 12, 16} // S, E, E, K

// bannerWithLettersRevealed returns the banner with letters 0..n-1
// rendered as blocks and the rest blanked to spaces (frame stays
// intact). n==0 returns just the empty frame; n>=4 returns the full
// SeekPixelBanner unchanged.
//
// Driven by the constants above so a letter-count change in the
// wordmark only needs updates here, not at every call site.
func bannerWithLettersRevealed(n int) string {
	if n >= len(letterEndCols) {
		return SeekPixelBanner
	}

	cutoff := -1 // negative = blank everything inside the frame
	if n > 0 {
		cutoff = letterEndCols[n-1]
	}

	lines := strings.Split(SeekPixelBanner, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		if i == 0 || i == len(lines)-1 {
			out[i] = line // borders unchanged
			continue
		}
		runes := []rune(line)
		// Inner content runs from index 1 to len(runes)-2; index 0 and
		// the last index are the left/right frame chars.
		for j := 1; j < len(runes)-1; j++ {
			if j > cutoff && runes[j] == '█' {
				runes[j] = ' '
			}
		}
		out[i] = string(runes)
	}
	return strings.Join(out, "\n")
}

// animateBanner reveals the wordmark letter-by-letter using raw ANSI
// cursor-up + reprint. ~80ms per letter, ~320ms total — enough to feel
// intentional, short enough that repeat invocations don't drag.
//
// MUST only be called when stdout is a TTY (no animation when piped).
// Caller is responsible for that check.
func animateBanner() {
	const frameDelay = 80 * time.Millisecond
	const bannerLines = 7 // matches SeekPixelBanner row count

	// Hide cursor during the animation so the user doesn't see it
	// jumping around; restore at the end (and best-effort restore via
	// defer in case panic).
	fmt.Print("\x1b[?25l")
	defer fmt.Print("\x1b[?25h")

	for n := 0; n <= len(letterEndCols); n++ {
		if n > 0 {
			// Move back up to the top of the previously-drawn banner
			// so this frame overwrites it cleanly. \r returns to col
			// 0; \x1b[<k>A goes up k lines.
			fmt.Printf("\r\x1b[%dA", bannerLines)
		}
		fmt.Println(renderBannerString(bannerWithLettersRevealed(n)))
		if n < len(letterEndCols) {
			time.Sleep(frameDelay)
		}
	}
}

// shouldAnimate decides whether to play the welcome animation. Returns
// false when stdout isn't a TTY (piped, print mode, dumb terminal) or
// when SEEK_NO_ANIM is set (CI, scripts, slow remote sessions).
func shouldAnimate() bool {
	if os.Getenv("SEEK_NO_ANIM") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// PrintPixelWelcomeBanner prints the welcome screen. Called by
// tui.Run BEFORE bubbletea takes over so the lines land in scrollback
// above the live region.
//
// Layout:
//
//	<blank>
//	<wordmark>                  ← animated when stdout is a TTY
//	<tagline>
//	  <version · hash>
//	<blank>
//	  cwd … · model … · tier … [· YOLO] [· 🌙 in Nm]
//	  type /help for commands · Ctrl+C to quit
//	<blank>
func PrintPixelWelcomeBanner(opts Options) {
	tier := pricing.CurrentTier(time.Now())
	_, nextAt := pricing.NextTransition(time.Now())

	muted := lipgloss.NewStyle().Foreground(colourMuted)

	fmt.Println()
	if shouldAnimate() {
		animateBanner()
	} else {
		fmt.Println(RenderPixelBanner())
	}
	fmt.Println(muted.Render(pixelBannerTagline))
	fmt.Println(muted.Render("  " + VersionString()))
	fmt.Println()

	// Session metadata: one compact line, indented 2 cols so it visually
	// aligns with the wordmark's interior.
	meta := fmt.Sprintf("cwd %s  ·  model %s  ·  tier %s",
		opts.CWD,
		opts.Model,
		pricing.TierLabel(tier))
	if opts.Yolo {
		meta += "  ·  YOLO"
	}
	// Off-peak countdown, when relevant.
	if tier != pricing.TierOffPeak {
		dur := nextAt.Sub(time.Now())
		if dur > 0 {
			mins := int(dur.Minutes())
			meta += fmt.Sprintf("  ·  🌙 in %dm", mins)
		}
	}
	fmt.Println(muted.Render("  " + meta))
	fmt.Println(muted.Render("  type /help for commands  ·  Ctrl+C to quit"))
	fmt.Println()
}
