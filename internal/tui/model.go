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
	"os"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
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

	// ApprovalCh delivers per-call approval requests from the
	// permission policy. nil = no inline approval (e.g. --yolo at
	// startup); the TUI just won't listen.
	ApprovalCh <-chan permission.ApprovalRequest

	// Session + Store, when both non-nil, enable auto-save: after
	// every agent stream ends the current Session snapshot is
	// persisted via Store.Save. nil for ephemeral runs (--no-save).
	Session *session.Session
	Store   *session.Store

	// Skills is the loaded skill registry — used by /skills to print
	// the inventory. nil = no skills available; /skills handles that.
	Skills *skill.Set

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

	// Prompt history for ↑/↓. historyIdx == -1 means "at the live
	// input"; while navigating, savedDraft holds whatever the user
	// had typed before they started recalling so ↓-past-the-latest
	// restores it.
	promptHistory []string
	historyIdx    int
	savedDraft    string

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

	// Slash-command menu state. Open when the input starts with "/" and
	// contains no space yet; refreshed in handleKey's default branch
	// after every key that reached the textarea.
	commandMenuOpen     bool
	commandMenuFiltered []command
	commandMenuSelected int

	// pendingApproval, when non-nil, means the agent goroutine is
	// blocked on a permission decision and the TUI is showing an
	// inline y/N prompt. Reply is sent on the channel pointer (which
	// is buffered so we never block).
	pendingApproval *permission.ApprovalRequest

	// pathPicker drives the "@" file-path autocomplete dropdown.
	pathPicker pathCompleterState

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
	// Initial placeholder is filled in by refreshPlaceholder() below
	// once the Model is fully assembled. We set a neutral fallback in
	// case construction errors out mid-flight.
	ta.Placeholder = "Ask seek anything — Enter sends"
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

	m := Model{
		opts:       opts,
		input:      ta,
		spinner:    sp,
		now:        time.Now(),
		historyIdx: -1,
	}
	// Warm-up: scan workspace once for @-completer paths. Cost is
	// O(files), capped by pathScanLimit, runs synchronously here so
	// the first "@" feels instant. For huge repos this may take a
	// couple hundred ms — acceptable on TUI startup.
	if opts.CWD != "" {
		m.pathPicker.all = scanWorkspace(opts.CWD)
	}
	// Seed the placeholder with a state-aware hint (first-turn welcome,
	// or yolo warning if --yolo was passed).
	m.refreshPlaceholder()
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
		tickStatusEvery(time.Minute),
		initialSizeCmd(),
		m.spinner.Tick,
	}
	if m.opts.ApprovalCh != nil {
		cmds = append(cmds, waitForApproval(m.opts.ApprovalCh))
	}
	return tea.Batch(cmds...)
}

// waitForApproval pulls one approval request off the channel and
// emits an approvalRequestMsg. Mirrors waitForAgentEvent — bubbletea's
// "one event per Cmd" polling idiom.
func waitForApproval(ch <-chan permission.ApprovalRequest) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return approvalRequestMsg{req: req}
	}
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

