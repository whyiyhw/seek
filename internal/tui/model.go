// Package tui implements seek's interactive Bubble Tea front-end.
//
// Layout (a typical 24×80 terminal):
//
//	┌─────────────────────────────────────────────────┐
//	│ seek · /path/to/cwd                              │   header (1 row)
//	│                                                 │
//	│  > you                                          │   viewport
//	│  prompt text                                    │   (height - 5)
//	│                                                 │
//	│  ▸ assistant                                    │
//	│  streamed reply ...                             │
//	│  ↳ tool: read("README.md") → 1.2 kB             │
//	│                                                 │
//	├─────────────────────────────────────────────────┤
//	│ > _                                             │   input (3 rows)
//	├─────────────────────────────────────────────────┤
//	│ seek deepseek-chat ○ idle  …  cache 70.8%  ...  │   status (1 row)
//	└─────────────────────────────────────────────────┘
package tui

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/agent"
)

// Options bundles everything cmd/seek hands the TUI.
type Options struct {
	Agent   *agent.Agent
	Tracker *cache.Tracker
	Model   string
	Yolo    bool
	CWD     string
	Ctx     context.Context // cancelled on SIGINT
}

// historyItem is one rendered conversation entry. We keep this richer
// than a plain string so the view can re-render with current width on
// resize and so reasoning can be folded.
type historyItem struct {
	role      string // "user" | "assistant" | "tool" | "system"
	text      string
	reasoning string // assistant items only
	toolName  string
	toolArgs  string
	toolErr   bool
}

type Model struct {
	opts Options

	input    textarea.Model
	viewport viewport.Model

	history       []historyItem
	curContent    string // accumulating assistant text
	curReasoning  string // accumulating CoT
	curToolActive map[string]string // call_id → "name(args)"

	stream    <-chan agent.Event
	streaming bool

	turns     int
	toolCalls int

	width, height int
	ready         bool

	now time.Time

	lastErr error
}

// New constructs the initial Model. The caller should pass it to
// tea.NewProgram(... tea.WithAltScreen()).
func New(opts Options) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask seek anything — Enter to send, Ctrl+C to quit."
	ta.Prompt = "▌ "
	ta.SetHeight(3)
	ta.SetWidth(80)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Focus()

	// Suppress textarea's default Enter-inserts-newline so we can use
	// Enter to submit. Shift+Enter handling is added when terminal keymap
	// support catches up; for now Ctrl+J inserts a newline.
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")

	vp := viewport.New(80, 10)
	vp.SetContent(welcomeText(opts))

	return Model{
		opts:          opts,
		input:         ta,
		viewport:      vp,
		curToolActive: map[string]string{},
		now:           time.Now(),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, tickStatusEvery(time.Minute))
}

// tickStatusEvery returns a Cmd that re-fires every d as a
// statusTickMsg, keeping the tier countdown live.
func tickStatusEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// waitForAgentEvent reads one event from the agent channel and emits an
// agentEventMsg. When the channel closes it emits streamEndMsg.
func waitForAgentEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamEndMsg{}
		}
		return agentEventMsg{Event: ev}
	}
}

func welcomeText(opts Options) string {
	tier := pricing.CurrentTier(time.Now())
	hostInfo := opts.CWD
	if h, err := os.Hostname(); err == nil && h != "" {
		hostInfo = h + ":" + opts.CWD
	}
	return styleAssistantLabel.Render("seek") + " · " +
		styleMuted.Render(hostInfo) + "\n" +
		styleMuted.Render(fmt.Sprintf("model %s · tier %s · yolo %v",
			opts.Model, pricing.TierLabel(tier), opts.Yolo)) + "\n\n" +
		styleMuted.Render("Type a prompt below and press Enter. Ctrl+J for newline. Ctrl+C to quit.")
}
