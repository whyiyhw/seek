package tui

import "github.com/charmbracelet/lipgloss"

// Palette is the colour set shared across the TUI. Kept small and
// boring on purpose — most differentiation comes from layout, not
// chroma.
//
// Two palettes are defined: one for dark backgrounds and one for
// light backgrounds. SetTheme() picks the right one and rebuilds
// the package-level style vars.
type palette struct {
	User       lipgloss.Color
	Assistant  lipgloss.Color
	Tool       lipgloss.Color
	ToolErr    lipgloss.Color
	Reasoning  lipgloss.Color
	Muted      lipgloss.Color
	Accent     lipgloss.Color
	Ok         lipgloss.Color
	StatusBg   lipgloss.Color
	StatusFg   lipgloss.Color
	BannerFg   lipgloss.Color
	BannerBg   lipgloss.Color
	MenuSel    lipgloss.Color
}

var darkPalette = palette{
	User:      lipgloss.Color("117"), // soft cyan
	Assistant: lipgloss.Color("250"), // off-white
	Tool:      lipgloss.Color("180"), // amber
	ToolErr:   lipgloss.Color("203"), // red
	Reasoning: lipgloss.Color("242"), // dim grey
	Muted:     lipgloss.Color("241"),
	Accent:    lipgloss.Color("177"), // magenta-ish — for highlights
	Ok:        lipgloss.Color("114"), // green
	StatusBg:  lipgloss.Color("236"),
	StatusFg:  lipgloss.Color("252"),
	BannerFg:  lipgloss.Color("16"),
	BannerBg:  lipgloss.Color("114"),
	MenuSel:   lipgloss.Color("177"),
}

var lightPalette = palette{
	User:      lipgloss.Color("33"),  // deeper blue on light bg
	Assistant: lipgloss.Color("235"), // near-black
	Tool:      lipgloss.Color("136"), // darker amber
	ToolErr:   lipgloss.Color("196"), // bright red
	Reasoning: lipgloss.Color("243"), // medium grey
	Muted:     lipgloss.Color("237"), // darker grey
	Accent:    lipgloss.Color("134"), // deeper magenta
	Ok:        lipgloss.Color("70"),  // darker green
	StatusBg:  lipgloss.Color("254"), // light grey
	StatusFg:  lipgloss.Color("235"), // near-black
	BannerFg:  lipgloss.Color("16"),
	BannerBg:  lipgloss.Color("156"), // lighter green
	MenuSel:   lipgloss.Color("134"),
}

var (
	colourUser      = darkPalette.User
	colourAssistant = darkPalette.Assistant
	colourTool      = darkPalette.Tool
	colourToolErr   = darkPalette.ToolErr
	colourReasoning = darkPalette.Reasoning
	colourMuted     = darkPalette.Muted
	colourAccent    = darkPalette.Accent
	colourOk        = darkPalette.Ok
	colourStatusBg  = darkPalette.StatusBg
	colourStatusFg  = darkPalette.StatusFg
	colourBannerFg  = darkPalette.BannerFg
	colourBannerBg  = darkPalette.BannerBg
	colourMenuSel   = darkPalette.MenuSel
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

	// Approval prompt header — high-contrast warning style so the
	// inline y/N chooser is obviously different from a normal
	// streaming line.
	styleApprovalHeader = lipgloss.NewStyle().
				Foreground(colourToolErr).
				Bold(true)

	// Slash-command menu rows. Selected gets the accent colour + bold;
	// non-selected sits on the muted palette so the user's eye is
	// drawn to the highlight rather than the whole list.
	styleMenuSelected = lipgloss.NewStyle().
				Foreground(colourMenuSel).
				Bold(true)
	styleMenuItem = lipgloss.NewStyle().
			Foreground(colourMuted)
)

// SetTheme switches between "dark" and "light" palettes and rebuilds
// all package-level style variables. Must be called once at startup
// (from tui.Run) before any rendering code uses the styles.
func SetTheme(theme string) {
	var p palette
	switch theme {
	case "light":
		p = lightPalette
	default:
		p = darkPalette
	}

	colourUser = p.User
	colourAssistant = p.Assistant
	colourTool = p.Tool
	colourToolErr = p.ToolErr
	colourReasoning = p.Reasoning
	colourMuted = p.Muted
	colourAccent = p.Accent
	colourOk = p.Ok
	colourStatusBg = p.StatusBg
	colourStatusFg = p.StatusFg
	colourBannerFg = p.BannerFg
	colourBannerBg = p.BannerBg
	colourMenuSel = p.MenuSel

	styleUserLabel = lipgloss.NewStyle().Foreground(colourUser).Bold(true)
	styleUserText = lipgloss.NewStyle().Foreground(colourUser)

	styleAssistantLabel = lipgloss.NewStyle().Foreground(colourAccent).Bold(true)
	styleAssistantText = lipgloss.NewStyle().Foreground(colourAssistant)

	styleToolLine = lipgloss.NewStyle().Foreground(colourTool)
	styleToolError = lipgloss.NewStyle().Foreground(colourToolErr)

	styleReasoning = lipgloss.NewStyle().Foreground(colourReasoning).Italic(true)

	styleMuted = lipgloss.NewStyle().Foreground(colourMuted)
	styleErr = lipgloss.NewStyle().Foreground(colourToolErr).Bold(true)

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

	styleApprovalHeader = lipgloss.NewStyle().
				Foreground(colourToolErr).
				Bold(true)

	styleMenuSelected = lipgloss.NewStyle().
				Foreground(colourMenuSel).
				Bold(true)
	styleMenuItem = lipgloss.NewStyle().
			Foreground(colourMuted)
}
