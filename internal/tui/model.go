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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/checkpoint"
	"github.com/whyiyhw/seek/internal/keymap"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"

	"golang.org/x/term"
)

// AgentClient is the subset of *agent.Agent that the TUI actually
// calls. Defined here (consumer-side interface, Go convention) so
// tests can substitute a fakeAgent without spinning up the real HTTP
// client + event loop. *agent.Agent implements this structurally —
// production callers in cmd/seek pass `*agent.Agent` as before with
// zero changes.
//
// Keep this interface minimal — every method added here is one more
// hop the fake has to implement. Only add a method when production
// code in this package calls it; methods like SetModeLabel that are
// called from cmd/seek belong on the concrete *agent.Agent, not here.
type AgentClient interface {
	Prompt(ctx context.Context, text string) <-chan agent.Event
	Messages() []deepseek.Message
	Reset(history []deepseek.Message)
	Summarise(ctx context.Context) (string, deepseek.Usage, error)
	SetModel(string)
	SetEffort(string)
	SetLang(string)
}

// Options bundles everything cmd/seek hands the TUI. Hooks let in-app
// slash commands (/reset, /model, /yolo) update host-owned state.
type Options struct {
	Agent   AgentClient
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

	// AskUserCh delivers per-call ask_user requests from the
	// askuser.Policy. nil = the ask_user tool is unavailable; the
	// tool returns ErrDisabled when called.
	AskUserCh <-chan askuser.Request

	// Session + Store, when both non-nil, enable auto-save: after
	// every agent stream ends the current Session snapshot is
	// persisted via Store.Save. nil for ephemeral runs (--no-save).
	Session *session.Session
	Store   *session.Store

	// Checkpoint is the v3 safety-net Manager (PRD docs/prd/
	// feature-checkpoint.md). nil for ephemeral runs / disabled.
	// The TUI uses it to back /checkpoints, /restore, /undo, /redo
	// without re-resolving the session each call.
	Checkpoint *checkpoint.Manager

	// Keymap is the v3 柱 C user-customisable keybindings table
	// (PRD docs/prd/feature-tui-ergonomics.md §4). nil = use the
	// hard-coded defaults from internal/keymap.NewDefault(); the TUI
	// resolves through m.keymap() which always returns non-nil so
	// existing tests don't need to construct one.
	Keymap *keymap.KeyMap

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
	// SetPlanSubstate notifies the host about plan-mode substate
	// transitions triggered by the propose tool (PRD §2.5):
	//
	//   "analyze" → permission stays ModePlan, reminder = plan-analyze
	//   "execute" → permission flips to ModeAsk,   reminder = plan-execute
	//   ""        → equivalent to SetPlan(false)
	//
	// The host (cmd/seek) is responsible for the actual permission /
	// mode-label side effects; this callback is just the signal.
	// nil = no host integration (status bar still updates locally).
	SetPlanSubstate func(string)

	// PlanSubstate mirrors the live substate ("" | "analyze" |
	// "execute"). Updated by /plan (cmdPlan sets "analyze" on entry,
	// "" on exit) and by the propose tool's events flowing through
	// applyAgentEvent. Read by the status bar; only meaningful when
	// Plan=true.
	PlanSubstate string

	// PlanSteps is the live task list owned by the `plan` tool. Seeded
	// when the user approves a propose() call and mutated by every
	// plan(start|complete|skip) call. Rendered as a fixed block at
	// the top of the live region whenever non-empty; the status bar
	// shows "done/total" alongside the PLAN:EXEC badge.
	PlanSteps []agent.PlanStep
	// PlanCurrentIdx is the 0-based index of the in_progress step, or
	// -1 when no step is active.
	PlanCurrentIdx int

	// RevokePlanPreApproval is called when the user Esc's a stream
	// (or /plan-off's mid-execute) so the host can clear the
	// permission policy's per-step pre-approval flag. Without this
	// hook, an Esc'd batch step would leave the gate open across the
	// next user prompt — the user would expect that prompt to gate
	// writes again. nil = no host wiring (e.g. tests); the TUI just
	// won't call it.
	RevokePlanPreApproval func()
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
// completionTokens, completed, and finished are populated at
// ToolExecEnd; before then they're zero. The slot is NOT removed
// from activeTools at that point — it stays visible (rendered with
// a ✓ marker and locked-in duration) until handleStreamEnd clears
// the whole list at turn end.
//
// Why keep finished slots around: removing them per-tool causes the
// live region to lose 1 row each time a tool ends, then gain it back
// when the next tool starts — the input visibly twitches up and down
// throughout a multi-tool turn. Holding the slot until stream end
// makes the active-tool area monotonically non-decreasing within a
// turn (and collapses once, at the end), which is what users actually
// want to see.
type activeTool struct {
	callID           string
	name             string
	args             string
	started          time.Time
	completed        time.Time // zero = still running; set at ToolExecEnd
	completionTokens int       // set at ToolExecEnd; 0 before that
	finished         bool      // ToolExecEnd has fired for this slot
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

	// pendingSkill, when non-empty, names a skill that the user has
	// "armed" via `/skill use <name>` (no extra args). The next
	// user-typed message (and ONLY the next one — slash commands and
	// programmatic prompts like /review do not consume the arm) gets
	// wrapped with a "Please use the X skill" preamble before going to
	// the agent. Consumed via consumeArm; cleared on /skill use clear,
	// on send, and on /reset.
	pendingSkill string

	// pastedContent stores the full content of a multi-line paste when
	// the textarea display is folded to a placeholder. The marker text
	// stays in the input until Enter is pressed, at which point the
	// marker is replaced with pastedContent before submission.
	// Empty = not folded.
	pastedContent   string
	pastedLineCount int

	// lastInputRunesAt records when the main textarea last received typed
	// or pasted runes. Enter within pasteEnterGap of this timestamp is
	// treated as an intra-paste newline, not submit (Windows CRLF paste).
	lastInputRunesAt time.Time

	// streamStartTime is set in submit() and used to compute elapsed
	// time for the live streaming indicator. Zero when not streaming.
	streamStartTime time.Time
	// streamDeltaBytes accumulates the byte length of non-reasoning
	// MessageDelta chunks in the current stream. Used to estimate
	// completion token count before the final Usage arrives.
	streamDeltaBytes int

	turns     int
	toolCalls int

	width, height int
	ready         bool
	now           time.Time

	showReasoning bool

	// bannerFrame drives the welcome-banner letter-reveal animation.
	// 0 = blank, 1 = S, 2 = SE, 3 = SEE, 4 = SEEK (full). Advanced
	// by bannerTickMsg; stops at 4. View() renders the animated
	// wordmark when turns == 0, regardless of frame.
	bannerFrame int

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

	// reviewBranchEntry is set when the user selects "Type a branch name…"
	// from the /review picker. The textarea is focused; the next Enter
	// submits /review <typed-text> (dispatched through dispatchCommand
	// so no dedicated handler is needed). Esc cancels back to idle.
	reviewBranchEntry bool

	// pendingApproval, when non-nil, means the agent goroutine is
	// blocked on a permission decision and the TUI is showing an
	// inline y/N prompt. Reply is sent on the channel pointer (which
	// is buffered so we never block).
	pendingApproval *permission.ApprovalRequest

	// pendingQuestion, when non-nil, means the agent goroutine is
	// blocked on an ask_user request and the TUI is showing a
	// choice picker. Mutually exclusive with pendingApproval (only
	// one tool blocks at a time). Reply travels through req.Reply,
	// which is buffered to 1 so we never deadlock the TUI on send.
	pendingQuestion *askuser.Request

	// pendingQuestionSelected tracks which option indexes the user
	// has toggled on in multi-select mode. Single-select doesn't
	// use it; Enter on the highlighted row commits via the cursor
	// index alone.
	pendingQuestionSelected map[int]bool

	// pendingQuestionCursor is the highlighted row index. Bounds:
	// [0, len(options)] — the extra slot is the auto-appended
	// "Other" row, which sits at index len(options).
	pendingQuestionCursor int

	// pendingQuestionFreeText, when true, means the user picked the
	// "Other" row and the textarea is now collecting their free-text
	// reply. Enter on the textarea sends the typed content as
	// Answer.FreeText.
	pendingQuestionFreeText bool

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

	// helpOverlayOpen means the user has invoked /help and we are showing
	// a dismissable help overlay in the live region (instead of committing
	// the help text to scrollback where it would mix with conversation).
	// helpContent holds the formatted content to display. Dismissed on
	// Esc or q; cleared on the next keypress when the overlay is open.
	helpOverlayOpen bool
	helpContent     string
}

// New constructs the initial Model. The caller should pass it to
// tea.NewProgram without tea.WithAltScreen() — inline mode is the
// load-bearing architectural decision; see Run() for the rationale.
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
	// In the resume case (--resume / --continue), the loaded session
	// carries turn and tool-call counts from the prior run. Propagate
	// them to the Model so the post-/clear welcome header knows there's
	// history (m.turns > 0) and re-renders the pixel banner. Without
	// this, m.turns stays 0 even for a session with dozens of prior
	// turns, and the post-/clear branch skips the header.
	//
	// NOTE: we reconstruct turns/toolCalls from the message list rather
	// than trusting the saved Session.Turns/ToolCalls. A pre-existing
	// bug (fixed concurrently) could save corrupted counts after a
	// resume-then-one-turn cycle — the messages are always the ground
	// truth, the counters are a derived cache.
	if opts.Session != nil {
		t := 0
		tc := 0
		for _, msg := range opts.Session.Messages {
			if msg.Role == deepseek.RoleAssistant {
				t++
				// Only assistant turns can legitimately carry tool_calls
				// (DeepSeek API contract). Counting other roles' synthetic
				// ToolCalls slices would inflate the status bar's tool
				// counter for any malformed session — defensive filter
				// against corrupt input on --resume.
				tc += len(msg.ToolCalls)
			}
		}
		m.turns = t
		m.toolCalls = tc
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
	if m.opts.AskUserCh != nil {
		cmds = append(cmds, waitForAskUser(m.opts.AskUserCh))
	}
	if m.opts.ObserveResultChan != nil {
		cmds = append(cmds, waitForObserveResult(m.opts.ObserveResultChan))
	}
	if c := versionCheckCmd("whyiyhw", "seek", VersionString()); c != nil {
		cmds = append(cmds, c)
	}
	// Start the welcome-banner letter-reveal animation on a fresh
	// session. The first tick fires synchronously (delay=0) so
	// frame 0 renders immediately; subsequent ticks advance one
	// letter at a time until all four letters are revealed.
	if m.turns == 0 {
		cmds = append(cmds, tickBannerEvery(0))
	}
	// Resume replay happens BEFORE tea.NewProgram in Run() — see
	// renderReplayHistory + the Run() call site. Doing it from inside
	// Update via tea.Println triggers one redraw per message and floods
	// the screen at startup for long sessions; the pre-tea stdout
	// write lands the whole history in scrollback as one batch.
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

// waitForAskUser is the ask_user counterpart of waitForApproval. Same
// "one msg per Cmd" idiom; re-armed after every reply so the next
// tool call gets picked up.
func waitForAskUser(ch <-chan askuser.Request) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return askUserRequestMsg{req: req}
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

func tickBannerEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return bannerTickMsg{} })
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

// appendHistory commits an already-styled string to the terminal's
// native scrollback via tea.Println. Under inline mode, this is the
// entire mechanism: there is no in-app buffer; bubbletea flushes the
// Println immediately above the live region, and the line stays in
// the terminal's scrollback for the rest of the session (and after
// exit). Wheel scroll, PgUp/PgDn, and click-drag selection over the
// committed line are all the terminal's, not the app's.
//
// Trailing newlines are stripped because tea.Println adds its own
// terminating newline; leaving them in would produce a stray blank
// row above the live region after each commit.
//
// DO NOT reintroduce an in-app buffer (the old historyBuf + viewport
// shape). The previous attempt to mirror committed content inside the
// app was load-bearing under alt-screen, but under inline mode it's
// redundant with terminal scrollback AND it dragged in the manual
// scrollbackLines / floor-pin padding that caused the M3-era drift
// bugs documented in docs/pitfalls.md.
//
// Callers should `cmds = append(cmds, m.appendHistory(line))` to
// wire the returned cmd into their tea.Cmd batch. A nil result (empty
// input) is safe to append.
func (m *Model) appendHistory(line string) tea.Cmd {
	line = strings.TrimRight(line, "\n")
	if line == "" {
		return nil
	}
	return tea.Println(line)
}

// resetSessionCounters wipes the per-session counter state and streaming
// live buffers shared by every "new session bucket" entry point:
// cmdNew (/clear), handleCompactDone (/compact), and cmdBranch (/branch).
// All three reset the same fields verbatim before this helper existed —
// adding a new field that needs resetting required touching three call
// sites and forgetting one was the bug pattern (#8 in the v0.3.x review).
//
// bannerFrame is set to "fully revealed" rather than 0 so the wordmark
// doesn't replay its letter-by-letter animation on every /clear; the
// reveal is reserved for first-startup. View()'s banner gate
// (`turns == 0 && len(promptHistory) == 0`) decides whether the banner
// is rendered at all — for compact/branch it is not, because
// promptHistory survives those operations.
//
// NOT included here: pendingSkill, promptHistory/historyIdx/savedDraft,
// opts.PlanSubstate. Those are conversation-scoped — only cmdNew (a
// genuinely fresh conversation) should reset them. Compact/branch are
// continuations of the same conversation and must keep them.
func (m *Model) resetSessionCounters() {
	m.turns = 0
	m.toolCalls = 0
	m.bannerFrame = len(letterEndCols)
	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
}

// consumeArm wraps text with a "Please use the <name> skill" preamble
// when m.pendingSkill is set, then clears the arm. Returns text
// unchanged when no skill is armed. Called at the two user-typed
// submission sites (non-streaming submit and streaming queue/steer);
// programmatic submissions like /review go through submit() directly
// without this wrapper, so the arm survives until a real user message
// arrives.
//
// The wrapper text is deliberately explicit ("Please use the X skill")
// rather than something terser like a sigil — the model needs an
// unambiguous instruction to call the Skill tool first, and the
// natural-language form is what reliably triggers that across both
// DeepSeek and the second-tier providers.
func (m *Model) consumeArm(text string) string {
	if m.pendingSkill == "" {
		return text
	}
	name := m.pendingSkill
	m.pendingSkill = ""
	return fmt.Sprintf("Please use the %q skill for the following task:\n\n%s", name, text)
}
