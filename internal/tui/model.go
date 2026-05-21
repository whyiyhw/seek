// Package tui implements seek's interactive front-end as an inline
// Bubble Tea program (no alt-screen).
//
// Architecture (M4.5 — inline mode):
//
//   scrollback (terminal-native, immutable once committed)
//   ├── welcome banner             ← printed once before tea starts
//   ├── > user prompt              ← committed via tea.Println on submit
//   ├── [tool] read main.go → ok   ← committed via tea.Println on ToolExecEnd
//   ├── ▸ seek: complete reply…    ← committed via tea.Println on MessageEnd
//   └── > next prompt
//
//   live region (View(), redrawn each Update — volatile)
//   ├── ↳ read("main.go") …        ← active tools, removed on completion
//   ├── ▸ seek: streaming text…    ← growing assistant response
//   ├── ──────────────────
//   ├── > _                        ← input
//   └── status: …                  ← single status line
//
// The split lets us reuse the terminal's native scrollback for history
// (no in-memory viewport, no PgUp/PgDn juggling, mouse selection works
// across any committed content) while keeping a fixed-position input
// area and a live status line.
package tui

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/agent"
	"golang.org/x/term"
)

// Options bundles everything cmd/seek hands the TUI. Hooks let in-app
// slash commands (/reset, /model, /yolo) update host-owned state.
type Options struct {
	Agent   *agent.Agent
	Tracker *cache.Tracker
	Model   string
	Yolo    bool
	CWD     string
	Ctx     context.Context // cancelled on SIGINT

	// GlamourStyle is pre-detected by cmd/seek so we don't trigger an
	// OSC 11 query under bubbletea (see PRD §4.9 / pitfalls #5).
	GlamourStyle string

	RebuildAgent func() (*agent.Agent, error)
	SetModel     func(string)
	SetYolo      func(bool)
}

// activeTool is a tool whose ToolExecStart has fired but ToolExecEnd
// hasn't yet. Rendered inline in the live region with a spinner and an
// elapsed-time tail (e.g. "think(...) · 12s") so long-running reasoner
// calls don't look like the program froze.
type activeTool struct {
	callID  string
	name    string
	args    string
	started time.Time
}

type Model struct {
	opts Options

	input   textarea.Model
	spinner spinner.Model

	// Live (volatile) state — everything in here gets cleared once the
	// turn commits to scrollback. View() reads only from these fields.
	curContent   string
	curReasoning string
	activeTools  []activeTool

	// Prompt history for ↑/↓ (M4.5 next step — buffer wired up here so
	// the field exists from the start, navigation lands in a follow-up
	// commit).
	promptHistory []string

	stream    <-chan agent.Event
	streaming bool

	// cancelStream cancels the context that backs the in-flight
	// agent.Prompt. Triggered by Esc; cleared on streamEndMsg.
	cancelStream context.CancelFunc
	// userCanceled distinguishes "user pressed Esc" from "stream
	// ended naturally" so streamEndMsg can print an interrupt notice.
	userCanceled bool

	turns     int
	toolCalls int

	width, height int
	ready         bool
	now           time.Time

	showReasoning bool

	// md renders committed assistant messages as Markdown before they
	// go to scrollback. Initialised on first WindowSizeMsg.
	md      *glamour.TermRenderer
	mdWidth int

	lastErr error
}

// New constructs the initial Model. The caller should pass it to
// tea.NewProgram WITHOUT tea.WithAltScreen() (see PRD §4.9).
func New(opts Options) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask seek anything — Enter to send, Ctrl+J for newline, Ctrl+C to quit."
	ta.Prompt = "▌ "
	ta.SetHeight(3)
	ta.SetWidth(80)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Focus()
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colourTool)

	return Model{
		opts:    opts,
		input:   ta,
		spinner: sp,
		now:     time.Now(),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		tickStatusEvery(time.Minute),
		initialSizeCmd(),
		m.spinner.Tick,
	)
}

// initialSizeCmd queries the terminal directly and emits a synthetic
// WindowSizeMsg. See pitfalls #1 — bubbletea's own first WindowSizeMsg
// can be delayed or dropped depending on the terminal.
func initialSizeCmd() tea.Cmd {
	return func() tea.Msg {
		w, h, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || w <= 0 || h <= 0 {
			w, h = 80, 24
		}
		return tea.WindowSizeMsg{Width: w, Height: h}
	}
}

func tickStatusEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// waitForAgentEvent reads one event from the agent's channel and emits
// an agentEventMsg. When the channel closes it emits streamEndMsg.
func waitForAgentEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamEndMsg{}
		}
		return agentEventMsg{Event: ev}
	}
}

// printWelcomeBanner is called by cmd/seek BEFORE the bubbletea program
// starts. We use a plain fmt.Println rather than tea.Println so that
// the banner is established before tea takes over the terminal —
// otherwise tea would treat it as part of the live region.
func PrintWelcomeBanner(opts Options) {
	tier := pricing.CurrentTier(time.Now())
	fmt.Println()
	fmt.Println(styleAssistantLabel.Render("seek") + " · " + styleMuted.Render(opts.CWD))
	fmt.Println(styleMuted.Render(fmt.Sprintf(
		"model %s · tier %s · yolo %v · style %s",
		opts.Model, pricing.TierLabel(tier), opts.Yolo, opts.GlamourStyle)))
	fmt.Println(styleMuted.Render("type /help for commands · Ctrl+C to quit"))
	fmt.Println()
}
