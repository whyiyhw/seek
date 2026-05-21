package tui

import "github.com/charmbracelet/lipgloss"

// Palette is the colour set shared across the TUI. Kept small and
// boring on purpose — most differentiation comes from layout, not
// chroma.
var (
	colourUser       = lipgloss.Color("117") // soft cyan
	colourAssistant  = lipgloss.Color("250") // off-white
	colourTool       = lipgloss.Color("180") // amber
	colourToolErr    = lipgloss.Color("203") // red
	colourReasoning  = lipgloss.Color("242") // dim grey
	colourMuted      = lipgloss.Color("241")
	colourAccent     = lipgloss.Color("177") // magenta-ish — for highlights
	colourOk         = lipgloss.Color("114") // green
	colourStatusBg   = lipgloss.Color("236")
	colourStatusFg   = lipgloss.Color("252")
	colourBannerFg   = lipgloss.Color("16")
	colourBannerBg   = lipgloss.Color("114")
)

var (
	styleUserLabel = lipgloss.NewStyle().Foreground(colourUser).Bold(true)
	styleUserText  = lipgloss.NewStyle().Foreground(colourUser)

	styleAssistantLabel = lipgloss.NewStyle().Foreground(colourAccent).Bold(true)
	styleAssistantText  = lipgloss.NewStyle().Foreground(colourAssistant)

	styleToolLine  = lipgloss.NewStyle().Foreground(colourTool)
	styleToolError = lipgloss.NewStyle().Foreground(colourToolErr)

	styleReasoning = lipgloss.NewStyle().Foreground(colourReasoning).Italic(true)

	styleMuted = lipgloss.NewStyle().Foreground(colourMuted)
	styleErr   = lipgloss.NewStyle().Foreground(colourToolErr).Bold(true)

	styleStatusBar = lipgloss.NewStyle().
			Background(colourStatusBg).
			Foreground(colourStatusFg)

	styleStatusOffPeak = lipgloss.NewStyle().
				Background(colourBannerBg).
				Foreground(colourBannerFg).
				Bold(true).
				Padding(0, 1)

	styleHeader = lipgloss.NewStyle().
			Foreground(colourAccent).
			Bold(true)
)
