package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/pricing"
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

// RenderPixelBanner returns the wordmark with lipgloss styling: blocks
// in DeepSeek-brand cyan, frame in muted grey. Per-rune colouring is
// simple and fast enough for a one-time startup print; performance
// here is irrelevant.
func RenderPixelBanner() string {
	block := lipgloss.NewStyle().Foreground(colourUser)
	frame := lipgloss.NewStyle().Foreground(colourMuted)

	var out strings.Builder
	for i, line := range strings.Split(SeekPixelBanner, "\n") {
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

// PrintPixelWelcomeBanner prints the welcome screen. Called by
// tui.Run BEFORE bubbletea takes over so the lines land in scrollback
// above the live region.
//
// Layout:
//
//	<blank>
//	<wordmark>
//	<tagline>
//	<blank>
//	  cwd … · model … · tier … [· YOLO] [· 🌙 in Nm]
//	  type /help for commands · Ctrl+C to quit
//	<blank>
func PrintPixelWelcomeBanner(opts Options) {
	tier := pricing.CurrentTier(time.Now())
	_, nextAt := pricing.NextTransition(time.Now())

	muted := lipgloss.NewStyle().Foreground(colourMuted)

	fmt.Println()
	fmt.Println(RenderPixelBanner())
	fmt.Println(muted.Render(pixelBannerTagline))
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
