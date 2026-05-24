// Package tui implements seek's interactive front-end as an inline
// Bubble Tea program (no alt-screen).
//
// Architecture (M4.5 — inline mode):
//
//	scrollback (terminal-native, immutable once committed)
//	├── welcome banner             ← printed once before tea starts
//	├── > user prompt              ← committed via tea.Println on submit
//	├── [tool] read main.go → ok   ← committed via tea.Println on ToolExecEnd
//	├── ▸ seek: complete reply…    ← committed via tea.Println on MessageEnd
//	└── > next prompt
//
//	live region (View(), redrawn each Update — volatile)
//	├── ↳ read("main.go") …        ← active tools, removed on completion
//	├── ▸ seek: streaming text…    ← growing assistant response
//	├── ──────────────────
//	├── > _                        ← input
//	└── status: …                  ← single status line
//
// The split lets us reuse the terminal's native scrollback for history
// (no in-memory viewport, no PgUp/PgDn juggling, mouse selection works
// across any committed content) while keeping a fixed-position input
// area and a live status line.
package tui

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/memory"
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
	Plan    bool
	CWD     string
	Ctx     context.Context // cancelled on SIGINT

	// GlamourStyle is pre-detected by cmd/seek so we don't trigger an
	// OSC 11 query under bubbletea (see PRD §4.9 / pitfalls #5).
	GlamourStyle string

	// Theme is the resolved color theme: "dark" or "light" (never "auto").
	// Controls both glamour and lipgloss palettes.
	Theme string

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

	// ProviderName, when non-empty, means a second-tier provider is active
	// (Anthropic, OpenAI, Gemini, or a compatible endpoint). The TUI
	// renders a banner warning that DeepSeek-exclusive features are disabled.
	// Empty string = DeepSeek (no banner).
	ProviderName string

	RebuildAgent func() (*agent.Agent, error)
	SetModel     func(string)
	SetYolo      func(bool)
	SetPlan      func(bool)
	// SetEffort updates the host-owned sessionEffort. The TUI mirrors the
	// new value into m.opts.Effort + Session.Effort (via persistSession)
	// so the status bar refreshes and the next save captures the choice.
	// nil disables /effort; the command surfaces an unsupported message.
	SetEffort func(string)

	// Effort mirrors the session's reasoning_effort override ("" |
	// "high" | "max"). Read by the status bar and the /effort command;
	// written by /effort through SetEffort.
	Effort string

	// SetLang updates the host-owned sessionLang. The TUI mirrors the
	// new value into m.opts.Lang + Session.Lang so the next save
	// captures the choice. nil disables /lang; the command surfaces
	// an unsupported message.
	SetLang func(string)

	// Lang mirrors the session's response language preference ("" |
	// "en" | "zh"). Read by /lang; written through SetLang.
	Lang string

	// MemoryProject is the M-layer handle for this session. nil = memory
	// is unavailable (e.g. --no-save, load failure); /distill surfaces a
	// helpful error instead of running.
	MemoryProject *memory.Project

	// Distiller runs the reasoner pass that turns history → candidates.
	// nil disables /distill (same effect as MemoryProject being nil).
	Distiller *memory.Distiller

	// ObserveResultChan receives async memory_observe filter results
	// from background goroutines. nil = observe unavailable.
	ObserveResultChan <-chan memory.ObserveResult
}

// activeTool is a tool whose ToolExecStart has fired but ToolExecEnd
// hasn't yet. Rendered inline in the live region with a spinner and an
// elapsed-time tail (e.g. "think(...) · 12s") so long-running reasoner
// calls don't look like the program froze.
//
// When ToolExecEnd fires, the tool is NOT removed immediately — instead
// completionTokens is set and a cleanupToolMsg is queued, giving View()
// one frame to show the final token count on the live line.
type activeTool struct {
	callID           string
	name             string
	args             string
	started          time.Time
	completionTokens int // set at ToolExecEnd; 0 before that
}

// cleanupToolMsg is sent after ToolExecEnd to remove a finished tool
// from activeTools on the next frame, so View() renders one frame with
// the final token count visible.
type cleanupToolMsg struct {
	callID string
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

	// queuedText holds a user message submitted via Enter while a stream
	// is in flight. Auto-sent as the next prompt once the agent loop
	// reaches finish_reason=stop (i.e. streamEndMsg fires WITHOUT
	// userCanceled). Cleared by Esc, by being sent, or by being
	// overwritten by a subsequent Enter during the same stream.
	queuedText string
	// pendingSteerText holds a user message submitted via Alt+Enter
	// while a stream is in flight. Triggers cancelStream() immediately;
	// streamEndMsg then submits this text as the next prompt — i.e.
	// the current turn is dropped (Repair() cleans any orphan tool_calls
	// in the agent's history) and the steer message replaces it.
	pendingSteerText string

	// pastedContent stores the full content of a multi-line paste when
	// the textarea display is folded to a placeholder. The marker text
	// stays in the input until Enter is pressed, at which point the
	// marker is replaced with pastedContent before submission.
	// Empty = not folded.
	pastedContent   string
	pastedLineCount int

	// streamStartTime is set in submit() and used to compute elapsed
	// time for the live streaming indicator. Zero when not streaming.
	streamStartTime time.Time
	// streamDeltaBytes accumulates the byte length of non-reasoning
	// MessageDelta chunks in the current stream. Used to estimate
	// completion token count before the final Usage arrives.
	streamDeltaBytes int

	turns     int
	toolCalls int
	// scrollbackLines tracks the total number of lines printed above the
	// live region via tea.Println (including the welcome banner). Used by
	// View() to calculate remaining terminal height for pinning the
	// status bar to the bottom of the window.
	scrollbackLines int

	width, height int
	ready         bool
	now           time.Time

	showReasoning bool

	// helpOverlayOpen is set by /help or the ? key. When true, View
	// renders a floating overlay panel with all commands and keybindings.
	helpOverlayOpen bool

	// Slash-command menu state. Open when the input starts with "/" and
	// contains no space yet; refreshed in handleKey's default branch
	// after every key that reached the textarea.
	commandMenuOpen     bool
	commandMenuFiltered []command
	commandMenuSelected int

	// modelPickerOpen / etc. drive the dropdown that /model and /setup
	// share — same UI shape as the slash-command menu but scoped to
	// "pick one ID from a curated list". Two purposes use it today:
	//
	//   ""              → unused / closed
	//   "model"         → /model: candidates are model IDs of the
	//                     active provider, accept switches the model
	//   "setup-provider"→ /setup: candidates are providers, accept
	//                     moves the flow into key-entry mode
	//
	// The purpose discriminates applyModelChoice's branch.
	modelPickerOpen     bool
	modelPickerFiltered []modelChoice
	modelPickerSelected int
	pickerPurpose       string

	// Setup flow state — entered via /setup. setupKeyEntry == true
	// means the textarea is currently collecting an API key for
	// setupProvider; Enter saves to ~/.seek/config.json, Esc cancels.
	setupKeyEntry bool
	setupProvider string

	// pendingApproval, when non-nil, means the agent goroutine is
	// blocked on a permission decision and the TUI is showing an
	// inline y/N prompt. Reply is sent on the channel pointer (which
	// is buffered so we never block).
	pendingApproval *permission.ApprovalRequest

	// Distill review state. When distillReviewOpen is true, all keys
	// are intercepted by handleDistillKey: y saves the current
	// candidate, n drops it, e enters edit mode (distillEditing
	// true; the main input collects new content), q aborts the
	// review. distillIdx advances on y/n/e-commit; when it reaches
	// len(distillCandidates) the modal closes and a summary line
	// prints to scrollback.
	distillReviewOpen bool
	distillCandidates []memory.Candidate
	distillIdx        int
	distillSaved      int
	distillDropped    int

	// distillEditing means 'e' was pressed on the current candidate
	// and the main input area is collecting an edited content body
	// (prefilled with the candidate's current Content). Enter
	// commits (calls Project.Add with the edited content), Esc
	// cancels and returns to the y/n/e/q prompt.
	distillEditing bool

	// distilling is true while the asynchronous reasoner call for
	// /distill is in-flight. View() renders a spinner line with
	// elapsed time so the user knows the TUI hasn't hung.
	distilling      bool
	distillSince    time.Time
	distillMsgCount int

	// pathPicker drives the "@" file-path autocomplete dropdown.
	pathPicker pathCompleterState

	// md renders committed assistant messages as Markdown before they
	// go to scrollback. Initialised on first WindowSizeMsg.
	md      *glamour.TermRenderer
	mdWidth int

	lastErr error

	// upgradeAvailable is the tag of a newer release found by the
	// startup probe, e.g. "v0.2.0". Empty when up-to-date or the
	// probe was skipped. Surfaced in the status bar as a "↑ <tag>"
	// segment so the user can see they're behind without a popup.
	upgradeAvailable string
}

// isWelcomeScreen returns true when the live region is "idle" —
// no streaming content, no active tools, no pending approval, no
// command menu or path picker showing. In this state we add vertical
// padding to push the input area to the bottom of the terminal.
func (m Model) isWelcomeScreen() bool {
	if m.streaming {
		return false
	}
	if len(m.activeTools) > 0 {
		return false
	}
	if m.pendingApproval != nil {
		return false
	}
	if m.distillReviewOpen {
		return false
	}
	if m.commandMenuOpen || m.pathPicker.open {
		return false
	}
	return true
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
	if m.opts.ObserveResultChan != nil {
		cmds = append(cmds, waitForObserveResult(m.opts.ObserveResultChan))
	}
	if c := versionCheckCmd("whyiyhw", "seek", VersionString()); c != nil {
		cmds = append(cmds, c)
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

// scrollbackLineCount returns the number of terminal lines s occupies
// (used to track scrollback position for status-bar pinning).
func scrollbackLineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
