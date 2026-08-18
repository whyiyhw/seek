package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/checkpoint"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/hookscli"
	"github.com/whyiyhw/seek/internal/keymap"
	"github.com/whyiyhw/seek/internal/memorycli"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillcli"
	"github.com/whyiyhw/seek/internal/skillstats"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/upgrade"
)

// cmdResult captures the effect of a slash command. The text (if any)
// is Println'd to scrollback; quit/clear translate to bubbletea Cmds.
// Returning a struct rather than mutating m.history directly lets the
// command logic stay pure and trivially testable in inline mode where
// there is no in-process history buffer.
//
// `extra` carries arbitrary follow-up tea.Cmds — used by handlers like
// /compact that kick off an async LLM call and want a tea.Msg routed
// back into Update() when it finishes.
type cmdResult struct {
	text  string
	quit  bool
	clear bool
	extra tea.Cmd
}

type command struct {
	names       []string // first entry is canonical, rest are aliases
	usage       string
	description string
	// subcommands, when non-empty, drives generic "/<cmd> <verb>" Tab
	// completion (a second-level verb picker) — see updateCommandMenu's
	// generic branch. Leave nil for commands that take freeform args or
	// own a bespoke picker (/model, /effort, /skill). Mirrors the verbs
	// the handler's CLI accepts.
	subcommands []modelChoice
	handler     func(m *Model, args string) cmdResult
}

func allCommands() []command {
	return []command{
		{names: []string{"/help", "/?"}, usage: "/help", description: "Show this help.", handler: cmdHelp},
		{names: []string{"/keys"}, usage: "/keys", description: "Show the active keymap (including any ~/.seek/keybindings.toml overrides).", handler: cmdKeys},
		{names: []string{"/clear", "/new"}, usage: "/clear", description: "Start a fresh conversation (saves the current session, opens a new one, clears the screen). Ctrl+L if you only want to blank the visible terminal without resetting state.", handler: cmdNew},
		{names: []string{"/model"}, usage: "/model [id]", description: "Switch the active model. No args opens a picker; pass an id to skip it (e.g. /model deepseek-v4-pro).", handler: cmdModel},
		{names: []string{"/exit", "/quit", "/q"}, usage: "/exit", description: "Save the current session and quit seek.", handler: cmdQuit},
		{names: []string{"/effort"}, usage: "/effort [off|high|max]", description: "Set DeepSeek reasoning_effort for this session. No args opens a picker. off clears the override; high/max force Thinking on and tune the depth. Think tool runs one level above.", handler: cmdEffort},
		{names: []string{"/yolo"}, usage: "/yolo", description: "Toggle --yolo for the rest of this session.", handler: cmdYolo},
		{names: []string{"/plan"}, usage: "/plan", description: "Toggle plan mode (read-only exploration) for the rest of this session.", handler: cmdPlan},
		{names: []string{"/goal"}, usage: "/goal [<condition> | clear]", description: "Work autonomously until a condition is met: each turn a cheap model judges whether <condition> holds; if not, seek continues. No args shows status; `clear` stops. Esc also stops.", handler: cmdGoal},
		{names: []string{"/review"}, usage: "/review [branch]", description: "Code review working-tree changes (shorthand for /code-review quick). No args opens a picker; pass a branch name to diff against it instead.", handler: cmdReview},
		{names: []string{"/code-review"}, usage: "/code-review [quick|thorough] [--fix] [--comment] [branch]", description: "Code-review the diff at a chosen effort level (quick: terse, high-confidence blocking bugs; thorough: exhaustive incl. long-tail design findings). --fix applies findings via plan-mode propose; --comment posts inline PR comments (needs gh).", handler: cmdCodeReview},
		{names: []string{"/branch"}, usage: "/branch", description: "Fork this session: new ID, parent link, copy of history. Parent left intact on disk.", handler: cmdBranch},
		{names: []string{"/compact"}, usage: "/compact", description: "Summarise prior history into one message to free up context.", handler: cmdCompact},
		{names: []string{"/distill"}, usage: "/distill", description: "Thinking-mode-extract project-level decisions from this session into M memory (per-candidate y/n/e review).", handler: cmdDistill},
		{names: []string{"/skill"}, usage: "/skill <verb> [args]", description: "Use or manage skills. `use <name>` arms next message (or fire with inline task); install/list/status/stats/uninstall/update/help mirror the CLI.", handler: cmdSkillCLI},
		{names: []string{"/skills"}, usage: "/skills [--used]", description: "List loaded skills, or --used to see which were called in this session.", handler: cmdSkills},
		{names: []string{"/memory"}, usage: "/memory <verb> [args]", description: "Inspect project memory (mirrors the `seek memory` CLI: list, show, search, archive).", subcommands: []modelChoice{
			{"list", "list — every memory entry with scope and age"},
			{"show", "show <name> — full text of one entry"},
			{"search", "search <query> — substring search across entries"},
			{"archive", "archive <name> — retire an entry (kept on disk)"},
		}, handler: cmdMemoryCLI},
		{names: []string{"/hooks"}, usage: "/hooks [verb]", description: "Inspect shell hooks (mirrors `seek hooks` CLI: list, check, trust, audit). Default verb = list.", subcommands: []modelChoice{
			{"list", "list — configured shell hooks and their events"},
			{"check", "check — dry-run validate hook scripts (bash -n)"},
			{"trust", "trust <id> — mark a hook trusted to run"},
			{"audit", "audit — recent hook trust/exec decisions"},
		}, handler: cmdHooksCLI},
		{names: []string{"/steer", "/s"}, usage: "/steer [text]", description: "Interrupt the assistant and send new instructions. Text arg submits immediately; bare command promotes the queued message to an interrupt.", handler: cmdSteer},
		{names: []string{"/setup"}, usage: "/setup", description: "Re-run the API-key wizard. Saves to ~/.seek/config.json.", handler: cmdSetup},
		{names: []string{"/diagnose"}, usage: "/diagnose", description: "Print a diagnostic report with version, OS, provider, config and session metadata — paste into bug reports.", handler: cmdDiagnose},
		{names: []string{"/upgrade"}, usage: "/upgrade [--force] [--dry-run]", description: "Download the latest release and replace this binary in place.", handler: cmdUpgrade},
		// v3 柱 A checkpoint surface (PRD docs/prd/feature-checkpoint.md §4.2).
		{names: []string{"/checkpoints"}, usage: "/checkpoints", description: "List git checkpoints for this session (the per-turn working-tree snapshots seek wrote before destructive actions).", handler: cmdCheckpoints},
		// v5 柱 G subagent surface (PRD docs/prd/feature-subagent.md §4.2).
		// M11.0 ships a read-only text table; interactive picker
		// (Enter for detail / `k` to kill) lands in a v0.6.x dot.
		{names: []string{"/agents"}, usage: "/agents", description: "List subagents in this project: spawn time, type, status, turns, tokens, description. Read-only in M11.0; killing active subagents from here lands with the picker upgrade.", handler: cmdAgents},
		// v5 柱 G M11.1 worktree surface (feature-subagent.md §3.8 + §4.2).
		// Read-only listing; create/cleanup go through the
		// enter/exit_worktree tools or `seek worktree gc` CLI.
		{names: []string{"/worktrees"}, usage: "/worktrees", description: "List seek-managed git worktrees: ID, branch, path. Includes worktrees from prior sessions (reads `git worktree list --porcelain` and filters to ~/.seek/projects/<pid>/worktrees/).", handler: cmdWorktrees},
		{names: []string{"/restore"}, usage: "/restore [last|<turn>]", description: "Restore the working tree to a git checkpoint. No args = last; pass a turn number for a specific one. HEAD is NOT moved; --force needed if working tree is dirty.", handler: cmdRestore},
		{names: []string{"/undo"}, usage: "/undo [<path>]", description: "Undo the most recent write/edit (file checkpoint). With a path: undo the most recent change to that file. Refuses if the file was modified externally; pass --force to override.", handler: cmdUndo},
		{names: []string{"/redo"}, usage: "/redo [<path>]", description: "Redo the most recently undone write/edit. Truncated by a fresh write/edit (classic editor semantics).", handler: cmdRedo},
	}
}

// dispatchCommand parses a /-prefixed input and runs the matching
// command. Returns true if input was a command (handled), false if the
// caller should treat input as a normal prompt to send to the agent.
func dispatchCommand(m *Model, input string) (handled bool, cmd tea.Cmd) {
	if !strings.HasPrefix(input, "/") {
		return false, nil
	}

	parts := strings.SplitN(input, " ", 2)
	name := strings.TrimSpace(parts[0])
	var args string
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	for _, c := range allCommands() {
		for _, n := range c.names {
			if n == name {
				res := c.handler(m, args)
				var printCmd tea.Cmd
				if res.text != "" {
					printCmd = m.appendHistory(res.text)
				}
				baseCmd := resultToCmd(res)
				if printCmd == nil {
					return true, baseCmd
				}
				if baseCmd == nil {
					return true, printCmd
				}
				return true, tea.Sequence(printCmd, baseCmd)
			}
		}
	}

	text := styleMuted.Render(fmt.Sprintf("unknown command %s — try /help", name))
	return true, m.appendHistory(text)
}

func resultToCmd(r cmdResult) tea.Cmd {
	// r.text is committed to history by dispatchCommand BEFORE this is
	// called — bubbletea tea.Println is no longer used for committed
	// content under alt-screen mode (where it would land in the unseen
	// main screen and only flush on exit). Only clear/quit/extra remain
	// as actual bubbletea commands.
	var cmds []tea.Cmd
	if r.clear {
		cmds = append(cmds, tea.ClearScreen)
	}
	if r.quit {
		cmds = append(cmds, tea.Quit)
	}
	if r.extra != nil {
		cmds = append(cmds, r.extra)
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// helpTopicChoices is the curated picker shown when /help is invoked
// without arguments. Mirrors the /model /effort picker pattern.
func helpTopicChoices() []modelChoice {
	return []modelChoice{
		{"all", "Show all help (commands + keys + about)"},
		{"commands", "List all slash commands"},
		{"keys", "Show key bindings"},
		{"about", "About seek — version, license, links"},
	}
}

func cmdHelp(m *Model, args string) cmdResult {
	if args != "" {
		// Args path: show the specified help topic directly.
		m.helpOverlayOpen = true
		m.helpContent = buildHelpTopic(m.keymap(), args)
		return cmdResult{}
	}

	// No args: open the topic picker — mirrors /model (no arg → picker).
	m.modelPickerFiltered = helpTopicChoices()
	m.modelPickerSelected = 0
	m.modelPickerOpen = true
	m.pickerPurpose = "help-topic"
	return cmdResult{}
}

// cmdKeys opens the help overlay scoped to the keys section. Mirrors
// `/help keys` but is a first-class command so users with custom
// keybindings have a shortcut that bypasses the topic picker.
// PRD docs/prd/feature-tui-ergonomics.md §5.2.
func cmdKeys(m *Model, _ string) cmdResult {
	m.helpOverlayOpen = true
	m.helpContent = buildHelpTopic(m.keymap(), "keys")
	return cmdResult{}
}

// buildHelpTopic returns the help overlay content for the given topic.
// Falls back to the combined view for unknown topics. km is the active
// keymap — passed in (rather than read from a global) so /help reflects
// the user's live customisations from ~/.seek/keybindings.toml.
func buildHelpTopic(km *keymap.KeyMap, topic string) string {
	var sb strings.Builder
	switch topic {
	case "all":
		sb.WriteString(styleHeader.Render("seek — help"))
		sb.WriteString("\n\n")
		sb.WriteString(buildHelpCommandsSection())
		sb.WriteString("\n")
		sb.WriteString(buildHelpKeysSection(km))
	case "commands":
		sb.WriteString(buildHelpCommandsSection())
	case "keys":
		sb.WriteString(buildHelpKeysSection(km))
	case "about":
		sb.WriteString(buildHelpAboutSection())
	default:
		sb.WriteString(styleHeader.Render("seek — help"))
		sb.WriteString("\n\n")
		sb.WriteString(buildHelpCommandsSection())
		sb.WriteString("\n")
		sb.WriteString(buildHelpKeysSection(km))
	}
	return sb.String()
}

func buildHelpCommandsSection() string {
	var sb strings.Builder
	sb.WriteString(styleStatusOffPeak.Render(" Commands "))
	sb.WriteString("\n")
	cmds := allCommands()
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].usage < cmds[j].usage })
	for _, c := range cmds {
		names := strings.Join(c.names, ", ")
		sb.WriteString(fmt.Sprintf("  %-22s  %s\n", names, c.description))
	}
	return sb.String()
}

// buildHelpKeysSection renders the live keymap (action → key) into the
// help overlay. The table is sourced from km.Snapshot() so users see
// their actual bindings (including any ~/.seek/keybindings.toml
// overrides), not a stale hard-coded list. PRD §4.5.
func buildHelpKeysSection(km *keymap.KeyMap) string {
	var sb strings.Builder
	sb.WriteString(styleStatusOffPeak.Render(" Keys "))
	sb.WriteString("\n")
	// Non-rebindable keys (textarea native + bracketed paste) — list
	// first so users know what NOT to try rebinding via keybindings.toml.
	sb.WriteString(fmt.Sprintf("  %-22s  %s\n", "Ctrl+J", "Insert newline in input (every terminal)"))
	sb.WriteString(fmt.Sprintf("  %-22s  %s\n", "Shift+Enter", "Insert newline (terminals reporting modifiers; else sends)"))
	sb.WriteString(fmt.Sprintf("  %-22s  %s\n", "Tab", "Picker accept / completion (reserved)"))
	// Rebindable actions: render from snapshot so user overrides surface.
	for _, b := range km.Snapshot() {
		label := b.Key
		if b.Source == "user" {
			label += " *"
		}
		sb.WriteString(fmt.Sprintf("  %-22s  %s\n", label, b.Description))
	}
	sb.WriteString("\n")
	sb.WriteString(styleMuted.Render("  * = customised in ~/.seek/keybindings.toml"))
	sb.WriteString("\n")
	sb.WriteString(styleMuted.Render("  See `seek keys actions` for the full rebindable list."))
	sb.WriteString("\n")
	return sb.String()
}

func buildHelpAboutSection() string {
	var sb strings.Builder
	sb.WriteString(styleHeader.Render("seek"))
	sb.WriteString("\n\n")
	sb.WriteString(styleMuted.Render("  Terminal AI coding agent"))
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("  Version: %s\n", VersionString()))
	sb.WriteString("  License: MIT\n")
	sb.WriteString("  \n")
	sb.WriteString("  https://github.com/whyiyhw/seek\n")
	return sb.String()
}

// cmdNew is also bound to /clear — both names invoke the same
// reset-then-blank behavior. This matches the dominant AI-CLI
// convention (Claude Code, etc.) where /clear means "start over",
// not the shell's "wipe visible terminal". The pure-blank use case
// is still available via Ctrl+L (see update_key.go), which calls
// tea.ClearScreen without touching session / agent / counters.
func cmdNew(m *Model, _ string) cmdResult {
	if m.streaming {
		return cmdResult{text: styleMuted.Render("/new: wait for the current turn to finish")}
	}
	if m.opts.RebuildAgent == nil {
		return cmdResult{text: styleMuted.Render("/new unsupported (rebuild hook not wired)")}
	}
	// Save the current session before starting fresh.
	m.persistSession()

	// Try to build the new agent FIRST — if RebuildAgent fails
	// (transient config / network error), abort without touching
	// the in-memory session or tracker state so the user can
	// retry from a clean state.
	newAgent, err := m.opts.RebuildAgent()
	if err != nil {
		return cmdResult{text: styleErr.Render("/new failed: " + err.Error())}
	}

	// Create a brand-new session (clean slate, no parent link).
	// Guard against --no-save mode where session persistence is off:
	// don't silently convert an ephemeral run to a persisted one.
	if m.opts.Session != nil && m.opts.Store != nil {
		sess := session.New(m.opts.Model, m.opts.CWD, "", m.opts.Yolo, m.opts.Plan)
		m.opts.Session = sess
		// The Save below is best-effort; the session will also be saved
		// on the next auto-save cycle.
		_ = m.opts.Store.Save(sess)
	}

	// Reset the usage tracker so the new session starts clean.
	m.opts.Tracker = cache.New()
	m.opts.Agent = newAgent

	// Reload skills so /skills shows any newly installed skills.
	// RebuildAgent already reloads skills for the system prompt, but
	// opts.Skills is a separate reference that cmdSkills reads from.
	if m.opts.CWD != "" {
		if reloaded, _, lerr := skill.Load(skill.LoadOptions{ProjectDir: m.opts.CWD}); lerr == nil && reloaded != nil {
			m.opts.Skills = reloaded
		}
	}

	m.resetSessionCounters()
	// Conversation-scoped resets — UNIQUE to /clear because /clear is
	// "fresh conversation", not "same conversation, different storage"
	// (which is what /compact and /branch are). See resetSessionCounters'
	// doc for the split.
	m.opts.PlanSubstate = ""
	// A brand-new session + a fresh tracker mean the exit summary's
	// totals no longer include prior runs — drop the resume marker.
	m.resumed = false
	// Clear prompt-recall state. Two reasons: (1) the welcome banner's
	// gate in View() is `turns==0 && len(promptHistory)==0`, so leaving
	// prior prompts here would hide the fresh-conversation banner; (2)
	// up-arrow recall belongs to the conversation that produced it —
	// "fresh conversation" is the documented contract of /clear.
	m.promptHistory = nil
	m.historyIdx = -1
	m.savedDraft = ""
	return cmdResult{clear: true, text: styleMuted.Render("new conversation — previous session saved")}
}

// modelChoice is one row in the /model picker — wire-name + human
// description. Kept here (not in pricing/budget) because this is a
// UI concern: the picker wants a curated list, not the full pricing
// table.
type modelChoice struct {
	id          string
	description string
}

// knownModelsForProvider returns the picker candidates for the active
// provider. Empty list means "no curated list for this provider" —
// callers should fall back to the freeform `/model <id>` form (used
// for --provider=compatible where the model id is whatever the
// endpoint declares).
//
// We deliberately hard-code these here rather than syncing with
// internal/pricing / internal/budget — those tables index by model id
// for cost / context-window lookups, but the picker wants a curated,
// ordered, human-labelled list. Decoupling lets each table evolve
// at its own pace.
func knownModelsForProvider(providerName string) []modelChoice {
	switch strings.ToLower(providerName) {
	case "", "deepseek":
		// The retired deepseek-chat / deepseek-reasoner aliases were
		// removed server-side on 2026-07-24; the picker surfaces only
		// the explicit V4 IDs so users see what they're actually
		// buying instead of relying on DeepSeek's server-side alias
		// routing (which has silently demoted reasoner→V4-Flash
		// in the past).
		return []modelChoice{
			{"deepseek-v4-flash", "DeepSeek V4-Flash — fast chat + tools (default)"},
			{"deepseek-v4-pro", "DeepSeek V4-Pro — Thinking-enabled reasoning (explicit)"},
		}
	case "anthropic":
		return []modelChoice{
			{"claude-sonnet-4-20250514", "Claude Sonnet 4 — flagship"},
			{"claude-3-5-sonnet-20241022", "Claude 3.5 Sonnet — previous gen"},
		}
	case "openai":
		return []modelChoice{
			{"gpt-4o", "GPT-4o — flagship"},
			{"gpt-4o-mini", "GPT-4o mini — fast / cheap"},
		}
	case "gemini":
		return []modelChoice{
			{"gemini-2.0-flash", "Gemini 2.0 Flash — fast"},
			{"gemini-1.5-pro", "Gemini 1.5 Pro — large context (2M)"},
		}
	default:
		return nil
	}
}

func cmdModel(m *Model, args string) cmdResult {
	// Args path: keep the original "type the id directly" flow for
	// power users and for providers we don't curate (compatible).
	if args != "" {
		prev := m.opts.Model
		m.opts.Model = args
		if m.opts.SetModel != nil {
			m.opts.SetModel(args)
		}
		if m.opts.Agent != nil {
			m.opts.Agent.SetModel(args)
		}
		return cmdResult{text: styleMuted.Render(fmt.Sprintf("model: %s → %s (effective on next prompt)", prev, args))}
	}

	// No args: open the picker if we have a curated list for this
	// provider; otherwise fall back to the old "print current + usage"
	// message (compatible endpoints have freeform model ids).
	models := knownModelsForProvider(m.opts.ProviderName)
	if len(models) == 0 {
		return cmdResult{text: styleMuted.Render(fmt.Sprintf(
			"current model: %s\nusage: /model <id>  (no curated list for provider %q)",
			m.opts.Model, m.opts.ProviderName))}
	}
	m.modelPickerFiltered = models
	m.modelPickerSelected = 0
	// Preselect the row matching the current model so Enter without
	// any arrow-key motion is a safe no-op.
	for i, mc := range models {
		if mc.id == m.opts.Model {
			m.modelPickerSelected = i
			break
		}
	}
	m.modelPickerOpen = true
	m.pickerPurpose = "model"
	return cmdResult{}
}

// applyModelChoice consumes the picker selection. Called from
// handleKey's modelPicker branch on Tab / Enter accept; branches on
// m.pickerPurpose because two flows share the same picker UI.
func (m *Model) applyModelChoice(idx int) {
	if idx < 0 || idx >= len(m.modelPickerFiltered) {
		return
	}
	choice := m.modelPickerFiltered[idx]
	purpose := m.pickerPurpose

	// Always close the picker before branching — both flows finish with
	// the dropdown gone.
	m.modelPickerOpen = false
	m.modelPickerFiltered = nil
	m.modelPickerSelected = 0
	m.pickerPurpose = ""

	// Generic verb completion ("/memory list ", "/hooks trust "): the
	// command name rides in the purpose suffix. Completion-style insert
	// (no dispatch) with a trailing space so the user keeps composing
	// args, mirroring the skill-verb flow below. Re-run the menu so the
	// new value's trailing-space state is recomputed (picker closes).
	if name, ok := strings.CutPrefix(purpose, subcmdPurposePrefix); ok {
		m.input.SetValue(name + " " + choice.id + " ")
		m.updateCommandMenu()
		return
	}

	switch purpose {
	case "setup-provider":
		// Selected a provider for /setup — move into key-entry mode.
		// The textarea now collects the API key; Enter saves, Esc
		// cancels (handled in handleKey).
		m.setupProvider = choice.id
		m.setupKeyEntry = true
		m.input.Reset()

	case "effort":
		// Map the displayed "off" label back to the wire-empty value;
		// "high" / "max" pass through verbatim.
		value := choice.id
		if value == "off" {
			value = ""
		}
		m.applyEffortChoice(value)
		m.input.Reset()

	case "model", "":
		// Switch the active model. Status bar's "model:" segment
		// updates on the next View() frame; an explicit Println would
		// require a tea.Cmd return — left for follow-up.
		m.opts.Model = choice.id
		if m.opts.SetModel != nil {
			m.opts.SetModel(choice.id)
		}
		if m.opts.Agent != nil {
			m.opts.Agent.SetModel(choice.id)
		}
		// Clear the textarea — the user got to the picker by typing
		// "/model " or "/model<Enter>", and after accept the leftover
		// "/model " would otherwise sit there until they backspace it
		// or submit it as garbage to the agent.
		m.input.Reset()

	case "skill-verb":
		// Completion-style insert (no command dispatch): replace the
		// textarea contents with `/skill <verb> ` so the user keeps
		// composing. The trailing space matters — for verb `use` it
		// re-triggers updateCommandMenu which immediately opens the
		// skill-name picker, giving the user a seamless two-step
		// completion without any extra keystrokes.
		m.input.SetValue("/skill " + choice.id + " ")
		// Re-run the menu state machine so the inserted text immediately
		// drives the next picker (use→name handoff). Without this the
		// user would see "/skill use " sitting in the textarea with no
		// dropdown until the next keystroke.
		m.updateCommandMenu()

	case "skill-name":
		// Completion-style insert: `/skill use <name> ` with trailing
		// space. The space puts the cursor in the inline-task position
		// so the user can type a task immediately — the common follow-
		// up gesture. submit() runs TrimSpace before dispatch, so
		// Enter-to-arm still works without the user manually removing
		// the trailing whitespace.
		//
		// We do NOT need to call updateCommandMenu here: applyModelChoice
		// already cleared modelPickerOpen above (line ~439), and the
		// trailing space means the NEXT updateCommandMenu invocation
		// (on the next keystroke) will see `tail` containing a space and
		// route to the "user past name → close picker" branch —
		// preventing the "picker re-opens on the same row" loop that
		// the no-space version was working around.
		m.input.SetValue("/skill use " + choice.id + " ")

	case "help-topic":
		// Selected a help topic from /help picker. Build the overlay
		// content for the chosen topic and open the dismissable help
		// overlay. Mirrors how /model picker accepts into a direct
		// action (model switch).
		m.helpOverlayOpen = true
		m.helpContent = buildHelpTopic(m.keymap(), choice.id)
		m.input.Reset()
	}
}

// skillVerbChoices is the curated picker shown after the user types
// `/skill ` (with a trailing space). Mirrors the subcommands accepted
// by skillcli.Run plus the TUI-only `use` verb. Order is "the verbs
// you'll reach for most often" first — list/status above the heavier
// install/update operations.
//
// Descriptions stay short so the picker fits without wrapping; the
// inline status line in the picker pane is the right depth-of-detail
// for "which verb does what". Anything richer belongs in /skill help.
func skillVerbChoices() []modelChoice {
	return []modelChoice{
		{"use", "use <name> [task] — arm next message, or fire immediately with a task"},
		{"list", "list — show loaded skills with sources and descriptions"},
		{"status", "status <name> — diagnostic detail for one skill"},
		{"stats", "stats — historical call counts across all sessions"},
		{"install", "install <source> — local path / git URL / https tarball"},
		{"update", "update <name>|--all — refetch installed skills"},
		{"uninstall", "uninstall <name> — remove a skill from ~/.seek/skills/"},
		{"help", "help — full skill CLI help"},
	}
}

// skillNameChoices returns one picker row per loaded skill — name +
// (truncated) description. Used by the second-level picker when the
// user has typed `/skill use ` and is choosing which skill to arm /
// fire. Returns nil when no skills are loaded so the caller can fall
// through to "no candidates → don't open".
//
// The description is collapsed to one line and clipped: the picker
// row is single-line per established convention, and a multi-line
// skill description would push the rest of the picker off-screen.
func skillNameChoices(set *skill.Set) []modelChoice {
	if set == nil || set.Len() == 0 {
		return nil
	}
	out := make([]modelChoice, 0, set.Len())
	for _, sk := range set.List() {
		desc := strings.ReplaceAll(sk.Description, "\n", " ")
		if len(desc) > 64 {
			desc = desc[:61] + "..."
		}
		out = append(out, modelChoice{id: sk.Name, description: sk.Name + " — " + desc})
	}
	return out
}

// filterChoicesByPrefix narrows a curated picker to entries whose id
// has the given (lower-cased) prefix. Empty prefix returns the input
// unchanged. Used by the /skill <verb> and /skill use <name> pickers
// so typing past the trigger space narrows candidates instead of
// stranding the user with a fixed list — the existing /model picker
// doesn't filter, but verb / skill-name lists are short and curated
// enough that the IDE-style narrowing feels right here.
func filterChoicesByPrefix(choices []modelChoice, prefix string) []modelChoice {
	if prefix == "" {
		return choices
	}
	p := strings.ToLower(prefix)
	var out []modelChoice
	for _, c := range choices {
		if strings.HasPrefix(strings.ToLower(c.id), p) {
			out = append(out, c)
		}
	}
	return out
}

// effortChoices is the curated picker for /effort. Order matters: the
// picker preselects the row matching the current setting, so listing
// "off" first means a fresh session lands on the safe default rather
// than on an expensive level. We deliberately omit "low" / "medium" —
// they are not part of DeepSeek's documented V4 levels and the user
// settled on the three-rung surface in design discussion.
func effortChoices() []modelChoice {
	return []modelChoice{
		{"off", "off — no override; uses the model's default thinking behaviour"},
		{"high", "high — force Thinking on; reasoning_effort=high"},
		{"max", "max — force Thinking on; reasoning_effort=max (slowest / most expensive)"},
	}
}

// applyEffortChoice writes the new /effort value through SetEffort and
// updates the live agent so the next prompt picks up the change. Pulled
// out of cmdEffort so both the arg-path and picker-path land here with
// identical semantics (mirrors applyModelChoice).
func (m *Model) applyEffortChoice(value string) {
	prev := m.opts.Effort
	m.opts.Effort = value
	if m.opts.SetEffort != nil {
		m.opts.SetEffort(value)
	}
	if m.opts.Agent != nil {
		m.opts.Agent.SetEffort(value)
	}
	if m.opts.Session != nil {
		// Persist to the in-memory session immediately. The on-disk
		// header is rewritten by the next persistSession (auto-save
		// after the next TurnEnd, or /branch / /new). Updating Session
		// here keeps the Save call cheap and keeps SetEffort callers
		// from needing to know about Store at all.
		m.opts.Session.Effort = value
	}
	// Refresh the placeholder hint — when effort flips between off and
	// non-off, the right-hand "effort:" indicator in the textarea help
	// line should reflect reality without waiting for the next render
	// trigger from a keystroke.
	m.refreshPlaceholder()
	_ = prev // reserved for a future "/effort: high → max" Println
}

func cmdEffort(m *Model, args string) cmdResult {
	if m.opts.SetEffort == nil {
		return cmdResult{text: styleMuted.Render("/effort unavailable in this build (SetEffort hook not wired)")}
	}

	// Arg path: accept "off" | "high" | "max" verbatim, reject anything
	// else with a usage hint. "off" is normalised to "" on the wire so
	// the JSONL header drops the field via omitempty when no override
	// is active — keeps resumed sessions byte-clean.
	if args != "" {
		var value string
		switch strings.ToLower(args) {
		case "off", "none", "":
			value = ""
		case "high":
			value = "high"
		case "max":
			value = "max"
		default:
			return cmdResult{text: styleMuted.Render(fmt.Sprintf(
				"/effort: unknown level %q — try off|high|max", args))}
		}
		prev := displayEffort(m.opts.Effort)
		m.applyEffortChoice(value)
		return cmdResult{text: styleMuted.Render(fmt.Sprintf(
			"effort: %s → %s (effective on next prompt)", prev, displayEffort(value)))}
	}

	// No args: open the picker. Preselect the row matching the current
	// setting so Enter without motion is a no-op.
	choices := effortChoices()
	m.modelPickerFiltered = choices
	m.modelPickerSelected = 0
	current := m.opts.Effort
	if current == "" {
		current = "off"
	}
	for i, c := range choices {
		if c.id == current {
			m.modelPickerSelected = i
			break
		}
	}
	m.modelPickerOpen = true
	m.pickerPurpose = "effort"
	return cmdResult{}
}

// displayEffort formats the wire value ("" | "high" | "max") for
// human-facing strings. "" is shown as "off" so messages stay readable.
func displayEffort(v string) string {
	if v == "" {
		return "off"
	}
	return v
}

// setupProviderChoices returns the provider list shown by /setup. We
// pull it from cmd/seek's wizard table indirectly — but tui can't
// import cmd/seek, so this list lives here and stays in sync via
// code review. The four provider names match config.KeyFor's
// canonical IDs.
func setupProviderChoices() []modelChoice {
	return []modelChoice{
		{"deepseek", "DeepSeek — full feature set (recommended)"},
		{"anthropic", "Anthropic Claude"},
		{"openai", "OpenAI GPT"},
		{"gemini", "Google Gemini"},
	}
}

func cmdSetup(m *Model, _ string) cmdResult {
	// Refuse mid-stream — same gate as every other modal-opening
	// command (/clear, /branch, /compact, /upgrade, /distill). Without
	// this guard the picker opens during a live turn and the user
	// lands in setupKeyEntry while agent events keep firing in the
	// background; combined with the handleStreamEnd hijack class
	// (see streamend_test.go) this is how a chat prompt ends up
	// saved as an API key in config.json.
	if m.streaming {
		return cmdResult{text: styleMuted.Render("/setup: wait for the current turn to finish")}
	}
	// Open the picker pre-loaded with provider choices and tagged
	// with the setup purpose so applyModelChoice routes correctly.
	m.modelPickerFiltered = setupProviderChoices()
	m.modelPickerSelected = 0
	// Preselect the currently-active provider when we can identify
	// it. m.opts.ProviderName is non-empty only for second-tier
	// providers; empty string means DeepSeek.
	want := strings.ToLower(m.opts.ProviderName)
	if want == "" {
		want = "deepseek"
	}
	for i, p := range m.modelPickerFiltered {
		if p.id == want {
			m.modelPickerSelected = i
			break
		}
	}
	m.modelPickerOpen = true
	m.pickerPurpose = "setup-provider"
	return cmdResult{text: styleMuted.Render("setup: choose a provider (Tab/Enter to accept · Esc to cancel)")}
}

// finishSetup is called by handleKey when the user presses Enter on
// the key-entry textarea. Saves the key to ~/.seek/config.json (perms
// 0600) and clears setup state. Returns a Println for the scrollback.
//
// Errors surface as muted/error text via the returned tea.Cmd. Verify
// step is deliberately skipped here (the standalone wizard validates
// with a ping; doing the same here would need a tea.Cmd and async
// state — a follow-up if real users find post-setup typo'd keys
// painful enough).
func (m *Model) finishSetup(key string) tea.Cmd {
	provider := m.setupProvider
	m.setupKeyEntry = false
	m.setupProvider = ""

	if key == "" {
		return m.appendHistory(styleMuted.Render("  setup: empty key — cancelled."))
	}

	cfg, err := config.Load()
	if err != nil {
		return m.appendHistory(styleErr.Render("  setup: load config: " + err.Error()))
	}
	config.SetKey(&cfg, provider, key)
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = provider
	}
	if err := config.Save(cfg); err != nil {
		return m.appendHistory(styleErr.Render("  setup: save: " + err.Error()))
	}
	path, _ := config.Path()
	return m.appendHistory(styleMuted.Render(fmt.Sprintf(
		"  setup: saved %s key to %s — restart seek or /new to apply",
		provider, path)))
}

// cancelSetup clears in-flight setup state without saving. Called
// by handleKey when Esc is pressed in key-entry mode.
func (m *Model) cancelSetup() tea.Cmd {
	m.setupKeyEntry = false
	m.setupProvider = ""
	m.input.Reset()
	return m.appendHistory(styleMuted.Render("  setup: cancelled (no changes)"))
}

// cmdDiagnose collects version, OS, provider, config and session metadata
// into a Markdown block the user can paste into a GitHub bug report.
func cmdDiagnose(m *Model, _ string) cmdResult {
	var b strings.Builder
	b.WriteString("```\n")

	// 1. Version
	fmt.Fprintf(&b, "seek:     %s\n", VersionString())

	// 2. OS / Arch
	fmt.Fprintf(&b, "os:       %s/%s\n", runtime.GOOS, runtime.GOARCH)

	// 3. Provider + model
	provider := m.opts.ProviderName
	if provider == "" {
		provider = "deepseek"
	}
	fmt.Fprintf(&b, "provider: %s\n", provider)
	fmt.Fprintf(&b, "model:    %s\n", m.opts.Model)

	// 4. Mode
	mode := "normal"
	if m.opts.Yolo {
		mode = "yolo"
	} else if m.opts.Plan {
		mode = "plan"
		if m.opts.PlanSubstate != "" {
			mode += " (" + m.opts.PlanSubstate + ")"
		}
	}
	fmt.Fprintf(&b, "mode:     %s\n", mode)

	// 5. Theme
	fmt.Fprintf(&b, "theme:    %s\n", m.opts.Theme)

	// 6. Config — provider names only, no keys
	cfg, _ := config.Load()
	if cfg.DefaultProvider != "" {
		fmt.Fprintf(&b, "default_provider: %s\n", cfg.DefaultProvider)
	}
	var providerNames []string
	for name := range cfg.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	if len(providerNames) > 0 {
		fmt.Fprintf(&b, "configured_providers: %s\n", strings.Join(providerNames, ", "))
	}

	// 7. Session
	if sess := m.opts.Session; sess != nil {
		fmt.Fprintf(&b, "session:  %s\n", sess.ID)
		fmt.Fprintf(&b, "turns:    %d\n", sess.Turns)
		fmt.Fprintf(&b, "tool_calls: %d\n", sess.ToolCalls)
		if !sess.CreatedAt.IsZero() {
			fmt.Fprintf(&b, "created:  %s\n", sess.CreatedAt.Format(time.RFC3339))
		}
	} else {
		b.WriteString("session:  (none — ephemeral)\n")
	}

	// 8. Working directory
	fmt.Fprintf(&b, "cwd:      %s\n", m.opts.CWD)

	b.WriteString("```\n")
	b.WriteString("_Generated by /diagnose — redact any sensitive paths before pasting into a bug report._")
	return cmdResult{text: styleMuted.Render(b.String())}
}

func cmdYolo(m *Model, _ string) cmdResult {
	wasPlan := m.opts.Plan
	m.opts.Yolo = !m.opts.Yolo
	// Yolo and Plan are mutually exclusive — toggle off Plan when
	// entering yolo so the permission policy and status bar badge
	// are consistent. Fire the opposing callback so side effects
	// (policy, mode label) are symmetric with cycleMode.
	if m.opts.Yolo {
		m.opts.Plan = false
		if wasPlan && m.opts.SetPlan != nil {
			m.opts.SetPlan(false)
		}
	}
	if m.opts.SetYolo != nil {
		m.opts.SetYolo(m.opts.Yolo)
	}
	// Yolo state directly affects placeholder priority — refresh so
	// the warning appears/disappears immediately.
	m.refreshPlaceholder()
	state := "off"
	if m.opts.Yolo {
		state = "on"
	}
	return cmdResult{text: styleMuted.Render("yolo " + state)}
}

func cmdPlan(m *Model, _ string) cmdResult {
	wasYolo := m.opts.Yolo
	m.opts.Plan = !m.opts.Plan
	// Plan and Yolo are mutually exclusive — toggle off Yolo when
	// entering plan so the permission policy and status bar badge
	// are consistent. Fire the opposing callback so side effects
	// (policy, mode label) are symmetric with cycleMode.
	if m.opts.Plan {
		m.opts.Yolo = false
		// Entering /plan always starts in the analyze substate; the
		// propose tool flips it to "execute" on approval. Mirrors
		// PRD §2.1 state machine.
		m.opts.PlanSubstate = "analyze"
		if wasYolo && m.opts.SetYolo != nil {
			m.opts.SetYolo(false)
		}
	} else {
		// Leaving /plan clears the substate so the status bar stops
		// rendering "PLAN:..." and any stale substate doesn't leak
		// into the next /plan entry. Also drop the task list — it's
		// scoped to the active plan workflow.
		m.opts.PlanSubstate = ""
		m.opts.PlanSteps = nil
		m.opts.PlanCurrentIdx = -1
		// Revoke any in-flight batch pre-approval so the gate closes
		// cleanly on /plan-off. policy.SetMode in cmd/seek's SetPlan
		// callback also clears the flag, but doing it here too keeps
		// the TUI honest if the host wiring ever changes.
		if m.opts.RevokePlanPreApproval != nil {
			m.opts.RevokePlanPreApproval()
		}
	}
	if m.opts.SetPlan != nil {
		m.opts.SetPlan(m.opts.Plan)
	}
	m.refreshPlaceholder()
	state := "off"
	if m.opts.Plan {
		state = "on"
	}
	return cmdResult{text: styleMuted.Render("plan " + state)}
}

// cmdReview is the shorthand for /code-review at the default (quick)
// effort with no flags. Its entire arg is treated as a branch name so a
// branch literally called "quick"/"thorough"/etc. still diffs correctly
// (only /code-review parses the leading token as an effort level).
func cmdReview(m *Model, args string) cmdResult {
	return m.startCodeReview("quick", false, false, strings.TrimSpace(args))
}

// cmdCodeReview parses [low|medium|high|max] [--fix] [--comment] [branch]
// and starts a review. Parse errors are surfaced as tool-result text so
// the user can correct the invocation — we never silently default a
// misspelled effort or swallow an unknown flag.
func cmdCodeReview(m *Model, args string) cmdResult {
	effort, fix, comment, branch, err := parseCodeReviewArgs(args)
	if err != nil {
		return cmdResult{text: styleErr.Render("code-review: " + err.Error())}
	}
	return m.startCodeReview(effort, fix, comment, branch)
}

// normalizeEffort folds an effort token into one of the two canonical
// levels (quick | thorough), or returns "" if the token isn't an effort
// word. The legacy 4-level vocabulary (low/medium/high/max) is accepted
// as an alias: a 2026-05 eval (eval/cases/code-review-effort-*) found
// DeepSeek doesn't reliably separate four prompt-framing levels — within-
// run variance swamped the signal — so the feature collapsed to two. Old
// invocations and the model's Claude-Code-trained low/medium/high
// vocabulary still map cleanly, so we don't error on them.
func normalizeEffort(tok string) string {
	switch tok {
	case "quick", "low", "medium":
		return "quick"
	case "thorough", "high", "max":
		return "thorough"
	}
	return ""
}

// parseCodeReviewArgs splits /code-review arguments into effort + flags +
// branch. effort defaults to "quick". The first standalone effort token
// (quick|thorough, or a legacy low|medium|high|max alias) sets the level;
// --fix / --comment are the flags; the first remaining token is the
// branch. A second effort token, a second branch token, or an unknown
// --flag is an error.
func parseCodeReviewArgs(args string) (effort string, fix, comment bool, branch string, err error) {
	effort = "quick"
	effortSet := false
	for _, tok := range strings.Fields(args) {
		switch {
		case tok == "--fix":
			fix = true
		case tok == "--comment":
			comment = true
		case strings.HasPrefix(tok, "--"):
			return "", false, false, "", fmt.Errorf("unknown flag %q (valid flags: --fix, --comment)", tok)
		case normalizeEffort(tok) != "":
			if effortSet {
				return "", false, false, "", fmt.Errorf("effort already set to %q; unexpected %q", effort, tok)
			}
			effort, effortSet = normalizeEffort(tok), true
		default:
			if branch != "" {
				return "", false, false, "", fmt.Errorf("unexpected argument %q (already diffing against %q)", tok, branch)
			}
			branch = tok
		}
	}
	return effort, fix, comment, branch, nil
}

// startCodeReview is the shared core behind /review and /code-review. It
// stashes effort+flags on the model (so the picker / branch-entry paths
// can rebuild the prompt) and either opens the branch picker, runs a
// working-tree review, or diffs against the named branch.
//
// We do NOT force plan mode — the prompt instructs read-only review
// unless --fix is set, in which case the model proposes fixes through
// the existing plan-mode gate (still per-call y/N). Keeping the current
// permission mode lets the model use bash for git diff/log in ModeAsk
// (with approval) or ModeYolo, which is far more efficient than grep/read
// over every changed file. An explicit /plan toggle is respected.
func (m *Model) startCodeReview(effort string, fix, comment bool, branch string) cmdResult {
	m.reviewEffort, m.reviewFix, m.reviewComment = effort, fix, comment

	if branch == "" {
		// No branch: open a picker so the user can choose between
		// working-tree review or diffing against a specific branch.
		choices := reviewChoices(m.opts.CWD)
		if len(choices) == 0 {
			// Not a git repo or no branches at all — fall back to a
			// generic working-directory review.
			return submitOrSteer(m, codeReviewPrompt(effort, fix, comment,
				"Review the code in the current working directory.", ""))
		}
		m.modelPickerFiltered = choices
		m.modelPickerSelected = 0
		m.modelPickerOpen = true
		m.pickerPurpose = "review"
		return cmdResult{}
	}

	// branch named: diff against it. If it matches the current branch,
	// fall back to working-tree review (diff against yourself is a no-op).
	// Use a 5-second timeout so a hung git doesn't freeze the TUI.
	gitCtx, gitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer gitCancel()
	current := currentGitBranch(gitCtx, m.opts.CWD)
	if current != "" && branch == current {
		if changes, ok := gatherChangedFiles(m.opts.CWD); ok {
			return submitOrSteer(m, codeReviewPrompt(effort, fix, comment,
				"Review the current git working-tree changes.", "Changed files:\n"+changes))
		}
		return submitOrSteer(m, codeReviewPrompt(effort, fix, comment,
			"Review the code in the current working directory.", ""))
	}
	if diffContent, ok := gatherBranchDiff(m.opts.CWD, branch); ok {
		return submitOrSteer(m, codeReviewPrompt(effort, fix, comment,
			"Review the git diff between the current branch and "+branch+".", diffContent))
	}
	return cmdResult{text: styleErr.Render("no diff found between current branch and " + branch)}
}

// reviewChoices builds the picker options for /review.
// The first option is "Review working tree changes" (if there are any).
// Then the list of local branches to diff against, followed by
// "Type a branch name…" for manual entry.
//
// Each git invocation is wrapped with a short timeout (2 s) to prevent
// a hung git process from freezing the TUI. If any command times out
// the function returns nil and the caller falls back gracefully.
func reviewChoices(cwd string) []modelChoice {
	if cwd == "" {
		return nil
	}

	// Short timeout so a hung git (network fs, auth prompt) doesn't
	// freeze the TUI. Called from updateCommandMenu on the UI goroutine.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Check if this is a git repo at all.
	gitCmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	gitCmd.Dir = cwd
	if err := gitCmd.Run(); err != nil {
		return nil
	}

	var choices []modelChoice

	// Option 1: working tree changes (if any).
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	statusCmd.Dir = cwd
	if out, err := statusCmd.Output(); err == nil && len(strings.TrimSpace(string(out))) > 0 {
		choices = append(choices, modelChoice{
			id:          "working-tree",
			description: "Review uncommitted changes in the working tree",
		})
	}

	// Option 2..N: local branches.
	branchCmd := exec.CommandContext(ctx, "git", "branch", "--format=%(refname:short)")
	branchCmd.Dir = cwd
	if out, err := branchCmd.Output(); err == nil {
		var branches []string
		for _, b := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if b != "" {
				branches = append(branches, b)
			}
		}
		sort.Strings(branches)
		current := currentGitBranch(ctx, cwd)
		for _, b := range branches {
			if b == current {
				continue // diff against yourself is a no-op
			}
			choices = append(choices, modelChoice{
				id:          "branch:" + b,
				description: "Diff against " + b,
			})
		}
	}

	// Last option: manual entry.
	choices = append(choices, modelChoice{
		id:          "type-branch",
		description: "Type a branch name…",
	})

	return choices
}

// codeReviewPrompt builds the prompt that /code-review (and its /review
// alias) submits. scope is a one-line description of what to review
// ("Review the current git working-tree changes." etc.); context is the
// changed-files summary or full diff to append, or "" when none.
//
// The "use the code-review skill" instruction lives inline here:
// programmatic submissions must bake the trigger into the prompt text
// because they bypass the user-typed submit path. The skill body carries
// the methodology; this prompt carries the chosen effort framing + flags
// so the model applies the right precision/recall.
func codeReviewPrompt(effort string, fix, comment bool, scope, context string) string {
	var b strings.Builder
	b.WriteString(`Please use the "code-review" skill for this task.` + "\n\n")
	b.WriteString(scope + " ")
	b.WriteString("Examine the changes for bugs, security vulnerabilities, " +
		"style violations, and design problems. Categorise findings by severity. ")

	b.WriteString("Effort level: " + effort + " — ")
	switch effort {
	case "thorough":
		b.WriteString("exhaustive recall: cast a wide net across correctness, security, performance, " +
			"design, edge cases, concurrency, error paths, and test gaps. You may include uncertain " +
			"findings — label each with a confidence — and surface long-tail design/maintainability " +
			"issues, not just the blocking bugs.")
	default: // quick
		b.WriteString("precision-first: report only the blocking bugs you are highly confident are real. " +
			"Stay terse; skip speculative or style-level nits. An empty result is a valid answer.")
	}

	if fix {
		b.WriteString(" --fix is set: after the read-only review, propose the mechanically-fixable " +
			"findings via the propose tool and apply them on approval (per-call y/N still applies).")
	} else {
		b.WriteString(" Do NOT write or edit files — this is a read-only review.")
	}
	if comment {
		b.WriteString(" --comment is set: post findings as inline PR comments via the gh CLI; " +
			"pre-check that gh is installed and authenticated first, and if it is not, print the " +
			"findings in chat instead.")
	}

	if context != "" {
		b.WriteString("\n\n" + context)
	}
	return b.String()
}

// gatherChangedFiles runs git status --porcelain and collects a diff-stat
// summary for the combined changes from HEAD. Returns a compact summary
// string, or false when git isn't available / cwd is empty / the directory
// isn't a git repo — callers should fall back to a generic prompt.
//
// For the rare case where changes are fully staged and the working tree
// has been reverted (git diff HEAD --stat is empty but staged changes
// exist), the function falls back to git diff --cached --stat.
//
// NOTE: git status and diff are separate invocations, so there is a
// TOCTOU race — the working tree could change between calls. This is
// acceptable for an interactive /review command where the user isn't
// simultaneously editing files.
func gatherChangedFiles(cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}

	gitCmd := func(args ...string) (string, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = cwd
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		s := strings.TrimSpace(string(out))
		return s, s != ""
	}

	summary, ok := gitCmd("status", "--porcelain")
	if !ok {
		return "", false
	}

	// Diff stat: git diff HEAD --stat gives the combined diff from
	// HEAD (includes both staged and unstaged).
	stat, statOk := gitCmd("diff", "HEAD", "--stat")
	if !statOk {
		// Fallback: staged-only changes when working tree was
		// reverted to match HEAD (git diff HEAD is empty but
		// git diff --cached shows the staged work).
		// If both fail, stat stays empty — we still return the
		// status summary.
		stat, _ = gitCmd("diff", "--cached", "--stat")
	}

	var sb strings.Builder
	sb.WriteString(summary)
	if stat != "" {
		sb.WriteString("\n\n")
		sb.WriteString(stat)
	}
	return sb.String(), true
}

// currentGitBranch returns the current branch name, or "" on error.
// Callers are responsible for providing a timeout context (5 s is typical).
func currentGitBranch(ctx context.Context, cwd string) string {
	if cwd == "" {
		return ""
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// maxDiffBytes is the maximum inline diff size for branch-review
// prompts. Diffs larger than this are summarised as a stat only,
// and the model is told to fetch individual file diffs via bash.
// 15 KiB ≈ 4–5K tokens, leaving room for system prompt + analysis
// in a long session (especially for models with 32K-64K context).
const maxDiffBytes = 15000

// gatherBranchDiff runs a git diff target...HEAD in cwd and returns a
// compact stat summary plus (if under maxDiffBytes) the full diff.
// The bool is false on error or empty diff.
//
// The stat is obtained from a dedicated git diff --stat invocation
// (accurate even for binary files, renames, and other edge cases).
// Large diffs (>maxDiffBytes) truncate the inline diff to the stat
// summary only, preventing token-limit blowups.
//
// target must not start with "-" to prevent flag injection.
func gatherBranchDiff(cwd, target string) (string, bool) {
	if cwd == "" || target == "" {
		return "", false
	}
	// Reject flag-like branch names (e.g. "--all") to prevent git
	// from interpreting them as options rather than revisions.
	if strings.HasPrefix(target, "-") {
		return "", false
	}

	// Full diff (may be large). Each git invocation uses its own timeout
	// so an earlier slow command doesn't eat into the budget of later ones.
	diffCtx, diffCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer diffCancel()
	diffCmd := exec.CommandContext(diffCtx, "git", "diff", target+"...HEAD")
	diffCmd.Dir = cwd
	out, err := diffCmd.Output()
	if err != nil {
		return "", false
	}
	diff := strings.TrimSpace(string(out))
	if diff == "" {
		return "", false
	}

	// Accurate stat from the CLI (handles binary files, renames, etc.).
	statCtx, statCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer statCancel()
	statCmd := exec.CommandContext(statCtx, "git", "diff", "--stat", target+"...HEAD")
	statCmd.Dir = cwd
	statOut, err := statCmd.Output()
	if err != nil {
		return "", false
	}
	stat := strings.TrimSpace(string(statOut))

	var sb strings.Builder
	sb.WriteString("Diff against " + target + ":\n")
	sb.WriteString(stat)
	if len(diff) <= maxDiffBytes {
		sb.WriteString("\n\n")
		sb.WriteString(diff)
	} else {
		sb.WriteString("\n\n(diff is too large to include inline — " +
			"use bash with git diff/git show to inspect individual files)")
	}
	return sb.String(), true
}

// steerStream stashes text as a pending steer and cancels the current
// stream so streamEndMsg can submit it. Sets userCanceled=false so the
// cancellation is treated as a steer, not a user-initiated stop.
// Shared helper used by cmdReview and the Alt+Enter path in update.go.
func steerStream(m *Model, text string) {
	m.pendingSteerText = text
	if m.cancelStream != nil {
		m.userCanceled = false
		m.cancelStream()
	}
}

// submitOrSteer submits a prompt directly (if not streaming) or stashes
// it as a steer text (if streaming). Shared helper used by cmdReview and
// any future one-shot commands that need to submit text.
func submitOrSteer(m *Model, prompt string) cmdResult {
	if m.streaming {
		steerStream(m, prompt)
		return cmdResult{}
	}
	newM, cmd := m.submit(prompt)
	*m = newM.(Model)
	return cmdResult{extra: cmd}
}

// cmdSteer handles /steer [text]. With a text arg it submits immediately
// (or steers the current stream). Without an arg it promotes the queued
// message (if any) to an interrupt.
func cmdSteer(m *Model, args string) cmdResult {
	if args != "" {
		return submitOrSteer(m, args)
	}
	// Bare /steer: promote queue to steer, or info if already steering.
	switch {
	case m.pendingSteerText != "":
		return cmdResult{text: styleMuted.Render("  ↪ already steering — waiting for the current turn to drain")}
	case m.queuedText != "":
		text := m.queuedText
		m.queuedText = ""
		steerStream(m, text)
		return cmdResult{text: styleMuted.Render("  ↰ promoted queue to interrupt")}
	default:
		return cmdResult{text: styleErr.Render("steer: nothing to steer (type /steer <message> or queue a message first with Enter)")}
	}
}

// handleReviewPick processes a selection from the /review picker and
// returns the (possibly updated) model and a tea.Cmd. Called from
// handleKey when pickerPurpose == "review".
func (m Model) handleReviewPick() (Model, tea.Cmd) {
	idx := m.modelPickerSelected
	if idx < 0 || idx >= len(m.modelPickerFiltered) {
		// Invalid state — close picker and return.
		m.modelPickerOpen = false
		m.modelPickerFiltered = nil
		m.modelPickerSelected = 0
		m.pickerPurpose = ""
		return m, nil
	}

	choice := m.modelPickerFiltered[idx].id
	m.modelPickerOpen = false
	m.modelPickerFiltered = nil
	m.modelPickerSelected = 0
	m.pickerPurpose = ""
	m.input.Reset()

	switch {
	case choice == "working-tree":
		changes, ok := gatherChangedFiles(m.opts.CWD)
		if !ok {
			return m, m.appendHistory(styleErr.Render("review: no changes to review"))
		}
		res := submitOrSteer(&m, codeReviewPrompt(m.reviewEffort, m.reviewFix, m.reviewComment,
			"Review the current git working-tree changes.", "Changed files:\n"+changes))
		return m, res.extra

	case strings.HasPrefix(choice, "branch:"):
		branch := strings.TrimPrefix(choice, "branch:")
		diffContent, ok := gatherBranchDiff(m.opts.CWD, branch)
		if !ok {
			return m, m.appendHistory(styleErr.Render("review: no diff found between current branch and " + branch))
		}
		res := submitOrSteer(&m, codeReviewPrompt(m.reviewEffort, m.reviewFix, m.reviewComment,
			"Review the git diff between the current branch and "+branch+".", diffContent))
		return m, res.extra

	case choice == "type-branch":
		// Enter key-entry mode: user types a branch name and hits Enter.
		m.reviewBranchEntry = true
		m.input.Reset()
		return m, nil
	}

	return m, nil
}

func cmdQuit(m *Model, _ string) cmdResult {
	m.persistSession()
	return cmdResult{quit: true}
}

// cmdSkillCLI mirrors the `seek skill ...` CLI (PRD v2 §5.2) inside
// the TUI. Args are whitespace-split into a tokens slice and handed
// to skillcli.Run with buffered IO; the captured stdout / stderr are
// rendered as scrollback text.
//
// Limitation (documented): args are whitespace-split. Skill names
// can't contain spaces (kebab-case regex enforces that), and install
// sources with spaces in the path are rare. If a user hits one,
// they can drop to the real CLI — same dispatcher, same behaviour.
//
// Long operations (git clone, HTTPS download) block the TUI for the
// duration of the call. Acceptable because the user deliberately
// typed /skill install — for any v3 polish round we can promote to
// a tea.Cmd with a spinner, but keeping it synchronous in v2 keeps
// the implementation honest about what the dispatcher does.
func cmdSkillCLI(m *Model, args string) cmdResult {
	tokens := strings.Fields(args)
	// `use` is TUI-only — the CLI dispatcher has no concept of an
	// in-session conversation to apply a skill to. Intercept before
	// delegating to skillcli.Run so the user gets the arm/fire
	// behaviour from /skill use instead of an unknown-verb error.
	if len(tokens) >= 1 && tokens[0] == "use" {
		return cmdSkillUse(m, tokens[1:])
	}
	var stdout, stderr bytes.Buffer
	err := skillcli.Run(tokens, &stdout, &stderr)
	var b strings.Builder
	if s := strings.TrimRight(stdout.String(), "\n"); s != "" {
		b.WriteString(s)
	}
	if s := strings.TrimRight(stderr.String(), "\n"); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleMuted.Render(s))
	}
	if err != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleMuted.Render(err.Error()))
	}
	return cmdResult{text: b.String()}
}

// cmdSkillUse is the TUI-only `use` verb. Two modes, distinguished by
// whether the user typed extra text after the name:
//
//   - `/skill use <name>`          → fire immediately with no extra task.
//     Submits "Please use the X skill" so the model calls the Skill tool,
//     fetches the body, and follows its instructions.
//   - `/skill use <name> <task>`   → fire immediately with inline task.
//     Wraps the task and submits as if the user had typed it.
//
// Validation:
//   - No skills loaded → error.
//   - Unknown name      → error with the list of loaded names so the
//     user can fix a typo without another round-trip.
func cmdSkillUse(m *Model, tokens []string) cmdResult {
	if len(tokens) == 0 {
		return cmdResult{text: styleErr.Render("/skill use: missing skill name (try /skill list)")}
	}
	if m.opts.Skills == nil || m.opts.Skills.Len() == 0 {
		return cmdResult{text: styleErr.Render("/skill use: no skills loaded")}
	}
	name := tokens[0]
	if m.opts.Skills.Get(name) == nil {
		var names []string
		for _, sk := range m.opts.Skills.List() {
			names = append(names, sk.Name)
		}
		return cmdResult{text: styleErr.Render(fmt.Sprintf("/skill use: %q not found. Available: %s", name, strings.Join(names, ", ")))}
	}

	// Inline task present → immediate fire with user-supplied task.
	if len(tokens) > 1 {
		task := strings.Join(tokens[1:], " ")
		wrapped := fmt.Sprintf("Please use the %q skill for the following task:\n\n%s", name, task)
		return submitOrSteer(m, wrapped)
	}

	// Bare name → immediate fire with no extra task.
	// The model sees "Please use the X skill", calls the Skill tool to fetch
	// the body, and follows its instructions — no user task needed.
	wrapped := fmt.Sprintf("Please use the %q skill.", name)
	return submitOrSteer(m, wrapped)
}

// cmdMemoryCLI mirrors the `seek memory ...` CLI inside the TUI. Same
// wiring as cmdSkillCLI: whitespace-split args, buffered IO, rendered as
// scrollback.
//
// Limitation (documented): args are whitespace-split via strings.Fields,
// so values containing spaces (e.g. -tagline "hello world") are split into
// separate tokens. The underlying CLI's FlagSet only sees the first token,
// causing unexpected parse errors. This matches the identical limitation
// in cmdSkillCLI — both use the same dispatcher pattern.
// cmdHooksCLI mirrors `seek hooks ...` inside the TUI. Default verb
// (no args) is `list` for consistency with the PRD §4.2 UX
// requirement that /hooks be equivalent to `seek hooks list` for the
// bare command. All other verbs are forwarded verbatim.
func cmdHooksCLI(_ *Model, args string) cmdResult {
	tokens := strings.Fields(args)
	if len(tokens) == 0 {
		tokens = []string{"list"}
	}
	var stdout, stderr bytes.Buffer
	err := hookscli.Run(tokens, &stdout, &stderr)
	var b strings.Builder
	if s := strings.TrimRight(stdout.String(), "\n"); s != "" {
		b.WriteString(s)
	}
	if s := strings.TrimRight(stderr.String(), "\n"); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleMuted.Render(s))
	}
	if err != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleMuted.Render(err.Error()))
	}
	return cmdResult{text: b.String()}
}

func cmdMemoryCLI(_ *Model, args string) cmdResult {
	tokens := strings.Fields(args)
	var stdout, stderr bytes.Buffer
	err := memorycli.Run(tokens, &stdout, &stderr)
	var b strings.Builder
	if s := strings.TrimRight(stdout.String(), "\n"); s != "" {
		b.WriteString(s)
	}
	if s := strings.TrimRight(stderr.String(), "\n"); s != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleMuted.Render(s))
	}
	if err != nil {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleMuted.Render(err.Error()))
	}
	return cmdResult{text: b.String()}
}

// cmdSkills prints the loaded skill inventory grouped by source. The
// model already sees the manifest in its system prompt, so this command
// exists for humans who want to verify what got loaded.
//
// With --used, switches to "which skills were actually called in this
// session", aggregated from ~/.seek/skills/.stats.jsonl filtered by the
// current Session.ID. This is the in-session counterpart to `seek skill
// stats` (which aggregates across all sessions).
//
// Kept alongside /skill (mirror of CLI list) because /skills is the
// pre-v2 muscle memory; deleting it would break existing user habits.
func cmdSkills(m *Model, args string) cmdResult {
	if strings.TrimSpace(args) == "--used" {
		return cmdSkillsUsed(m)
	}
	if m.opts.Skills == nil || m.opts.Skills.Len() == 0 {
		return cmdResult{text: styleMuted.Render("no skills loaded")}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("loaded %d skill(s):\n", m.opts.Skills.Len()))
	for _, sk := range m.opts.Skills.List() {
		fmt.Fprintf(&b, "  %-24s  %s\n", sk.Name, sk.Source)
		fmt.Fprintf(&b, "    %s\n", sk.Description)
	}
	return cmdResult{text: strings.TrimRight(b.String(), "\n")}
}

// cmdSkillsUsed renders the per-skill call count for the current session.
// Data comes from .stats.jsonl filtered by Session.ID. Returns a muted
// "no skills called yet" line when this session hasn't invoked any skill,
// so the user can tell "session id mismatch" apart from "stats file
// missing" by reading the message.
func cmdSkillsUsed(m *Model) cmdResult {
	if m.opts.Session == nil {
		return cmdResult{text: styleMuted.Render("/skills --used: needs a session (running with --no-save?)")}
	}
	path, err := paths.UserSkillStats()
	if err != nil {
		return cmdResult{text: styleErr.Render("/skills --used: " + err.Error())}
	}
	entries, err := skillstats.Read(path)
	if err != nil {
		return cmdResult{text: styleErr.Render("/skills --used: " + err.Error())}
	}

	sid := m.opts.Session.ID
	counts := map[string]int{}
	lastTS := map[string]string{}
	for _, e := range entries {
		if e.SessionID != sid {
			continue
		}
		counts[e.Name]++
		if e.TS > lastTS[e.Name] {
			lastTS[e.Name] = e.TS
		}
	}
	if len(counts) == 0 {
		return cmdResult{text: styleMuted.Render("no skills called in this session yet")}
	}

	// Sort by count desc, then name asc for ties — same ordering rule
	// as `seek skill stats` so habits transfer.
	type row struct {
		name  string
		count int
		last  string
	}
	rows := make([]row, 0, len(counts))
	for n, c := range counts {
		rows = append(rows, row{name: n, count: c, last: lastTS[n]})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count != rows[j].count {
			return rows[i].count > rows[j].count
		}
		return rows[i].name < rows[j].name
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("skills called this session (%d):\n", len(rows)))
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-24s  %d call(s)   last %s\n", r.name, r.count, r.last)
	}
	return cmdResult{text: strings.TrimRight(b.String(), "\n")}
}

// cmdBranch forks the active session: a new session is created with
// ParentID = current.ID and an independent copy of the message history,
// then the TUI switches m.opts.Session to the fork so all future
// auto-saves write to the new file. The parent file is flushed first
// so it captures everything up to the fork point.
//
// Pure metadata operation — the agent's in-memory history is unchanged
// (it already holds the messages we want on the fork).
func cmdBranch(m *Model, _ string) cmdResult {
	if m.opts.Session == nil || m.opts.Store == nil {
		return cmdResult{text: styleMuted.Render("/branch unavailable — session persistence is off (--no-save)")}
	}
	if m.streaming {
		return cmdResult{text: styleMuted.Render("/branch: wait for the current turn to finish")}
	}

	parent := m.opts.Session
	// Snapshot the parent so the on-disk version reflects the exact
	// state we're forking from. persistSession sources Messages straight
	// from the agent, which is what we want.
	m.persistSession()
	if m.lastErr != nil {
		err := m.lastErr
		m.lastErr = nil
		return cmdResult{text: styleErr.Render("/branch: save parent failed: " + err.Error())}
	}

	child := parent.Fork()
	if err := m.opts.Store.Save(child); err != nil {
		return cmdResult{text: styleErr.Render("/branch: save child failed: " + err.Error())}
	}
	m.opts.Session = child
	// The fork has done zero turns of its own work yet — the inherited
	// messages are history, not credit toward the child's counters.
	m.resetSessionCounters()

	return cmdResult{text: styleMuted.Render(fmt.Sprintf(
		"branched: %s → %s (continuing on new branch; parent intact at %s)",
		parent.ID, child.ID, m.opts.Store.Dir()))}
}

// cmdCompact kicks off the summariser. The actual LLM call runs in the
// returned tea.Cmd so the UI stays responsive; when it finishes it
// posts a compactDoneMsg that Update() handles by swapping the agent's
// history in via Reset().
func cmdCompact(m *Model, _ string) cmdResult {
	if m.streaming {
		return cmdResult{text: styleMuted.Render("/compact: wait for the current turn to finish")}
	}
	msgs := m.opts.Agent.Messages()
	// Anything ≤ 2 is just [system, possibly one user msg] — nothing
	// useful to compress, and would produce a meaningless summary.
	if len(msgs) <= 2 {
		return cmdResult{text: styleMuted.Render("/compact: history is already short — nothing to summarise")}
	}

	return cmdResult{
		text:  styleMuted.Render(fmt.Sprintf("compacting %d messages — calling %s …", len(msgs)-1, m.opts.Model)),
		extra: startCompactCmd(m),
	}
}

// cmdUpgrade triggers the same self-replace flow as `seek -upgrade`
// from inside the TUI. The actual download runs in a tea.Cmd so the
// UI stays responsive; when it finishes an upgradeDoneMsg is routed
// back into Update().
//
// Args: --force allows upgrading from a dev build; --dry-run stops
// after checksum verification. Unknown flags get a usage hint.
func cmdUpgrade(m *Model, args string) cmdResult {
	if m.streaming {
		return cmdResult{text: styleMuted.Render("/upgrade: wait for the current turn to finish")}
	}
	var force, dryRun bool
	for _, tok := range strings.Fields(args) {
		switch tok {
		case "--force", "-force":
			force = true
		case "--dry-run", "-dry-run":
			dryRun = true
		default:
			return cmdResult{text: styleMuted.Render(
				fmt.Sprintf("/upgrade: unknown flag %q — usage: /upgrade [--force] [--dry-run]", tok))}
		}
	}
	prefix := "/upgrade: checking GitHub for the latest release…"
	if dryRun {
		prefix = "/upgrade: dry run — will verify checksum but skip replace"
	}
	return cmdResult{
		text:  styleMuted.Render(prefix),
		extra: startUpgradeCmd(force, dryRun),
	}
}

// startUpgradeCmd builds the tea.Cmd that runs upgrade.Run in the
// background. We use context.Background() rather than m.opts.Ctx
// because cancelling here on Ctrl+C is awkward — most of the work
// is a single HTTP body read and an atomic rename, neither of which
// benefits from cancellation. If the user really wants to stop, they
// can Ctrl+C-quit the TUI; the temp file will be cleaned up on
// Run's defer.
func startUpgradeCmd(force, dryRun bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		// Stderr is suppressed — Run's pretty progress lines would
		// fight with bubbletea's render. We surface only the final
		// success/failure message via upgradeDoneMsg.
		res, err := upgrade.Run(ctx, upgrade.Options{
			Owner:    "whyiyhw",
			Repo:     "seek",
			Current:  VersionString(),
			AllowDev: force,
			DryRun:   dryRun,
			Stderr:   io.Discard,
			Stdout:   io.Discard,
		})
		out := upgradeDoneMsg{}
		switch {
		case err == upgrade.ErrAlreadyLatest:
			out.AlreadyLatest = true
			if res != nil {
				out.NewTag = res.To
			}
		case err != nil:
			out.Err = err
		default:
			out.DryRun = res.DryRun
			out.NewTag = res.To
		}
		return out
	}
}

// handleUpgradeDone renders the result of /upgrade as a one-line
// status into scrollback. On a successful install we add a "restart
// seek to run v…" nudge — the running process still has the OLD
// binary mapped, only the next launch picks up the new one.
func (m *Model) handleUpgradeDone(msg upgradeDoneMsg) []tea.Cmd {
	var line string
	switch {
	case msg.Err != nil:
		line = lipgloss.NewStyle().Foreground(colourToolErr).Render(
			fmt.Sprintf("/upgrade failed: %v", msg.Err))
	case msg.AlreadyLatest:
		line = styleMuted.Render(fmt.Sprintf("/upgrade: already on the latest release (%s)", msg.NewTag))
		// We're on the latest — clear any stale "↑ tag" status hint.
		m.upgradeAvailable = ""
	case msg.DryRun:
		line = styleMuted.Render(fmt.Sprintf("/upgrade dry-run OK: checksum verified for %s (binary not replaced)", msg.NewTag))
	default:
		line = lipgloss.NewStyle().Foreground(colourOk).Render(
			fmt.Sprintf("/upgrade: installed %s — restart seek to run the new binary", msg.NewTag))
		// We just wrote the new binary; clear the nudge so the bar
		// doesn't keep advertising the upgrade after it's done.
		m.upgradeAvailable = ""
	}
	return []tea.Cmd{m.appendHistory(line)}
}

// startCompactCmd returns the tea.Cmd that actually runs Summarise. We
// capture the agent + ctx by value here so the closure is independent
// of any later state changes on m.
func startCompactCmd(m *Model) tea.Cmd {
	ag := m.opts.Agent
	parentCtx := m.opts.Ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	return func() tea.Msg {
		// Bound the summariser to a minute — same Esc-style escape hatch
		// applies via parent ctx cancellation if the user hits Ctrl+C.
		ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
		defer cancel()
		summary, usage, err := ag.Summarise(ctx)
		return compactDoneMsg{summary: summary, usage: usage, err: err}
	}
}

// cmdDistill runs the reasoner pass that extracts ≤3 project-level
// decisions from the current session and surfaces them for y/n/e
// review (PRD §6). Refuses to run mid-stream — the reasoner needs the
// full settled history, not a half-streamed turn. Refuses on
// unavailable memory (--no-save, project load failure) so the user
// gets a clear message instead of a silent no-op.
func cmdDistill(m *Model, _ string) cmdResult {
	if m.streaming {
		return cmdResult{text: styleMuted.Render("/distill: wait for the current turn to finish")}
	}
	if m.opts.Distiller == nil || m.opts.MemoryProject == nil {
		return cmdResult{text: styleMuted.Render(
			"/distill: project memory is unavailable in this session (--no-save or load failure)")}
	}
	msgs := m.opts.Agent.Messages()
	// A bare [system, one user message] history has nothing to
	// distill — the model would just produce noise. Cut early.
	if len(msgs) <= 2 {
		return cmdResult{text: styleMuted.Render(
			"/distill: session is too short to extract decisions yet — keep going and try again later")}
	}

	return cmdResult{
		extra: func() tea.Msg {
			m.distilling = true
			m.distillSince = time.Now()
			m.distillMsgCount = len(msgs) - 1
			return startDistillCmd(m)()
		},
	}
}

// startDistillCmd runs the reasoner round-trip in a tea.Cmd. The 90s
// timeout matches reasoner latency observed in benchmark runs —
// distillation is bounded compute (one round-trip, no tool loop) so a
// minute-and-a-half should be plenty. Parent ctx cancellation
// (SIGINT) propagates so Ctrl+C still kills the call.
func startDistillCmd(m *Model) tea.Cmd {
	distiller := m.opts.Distiller
	history := m.opts.Agent.Messages()
	parentCtx := m.opts.Ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
		defer cancel()
		candidates, err := distiller.Distill(ctx, history)
		return distillDoneMsg{candidates: candidates, err: err}
	}
}

// ----- v3 checkpoint slash commands (feature-checkpoint.md §4.2) -----
//
// Each command is a thin wrapper around the Manager — the same code
// the CLI calls. We deliberately bypass internal/checkpointcli here:
// it sits behind a flag.FlagSet that would intercept "--session" and
// fight TUI conventions. The mapped semantics are identical.

func cmdCheckpoints(m *Model, _ string) cmdResult {
	if m.opts.Checkpoint == nil {
		return cmdResult{text: styleMuted.Render("/checkpoints: unavailable (--no-save or session not initialised)")}
	}
	list, err := m.opts.Checkpoint.ListGitCheckpoints()
	if err != nil {
		return cmdResult{text: styleErr.Render("/checkpoints: " + err.Error())}
	}
	if len(list) == 0 {
		return cmdResult{text: styleMuted.Render("no git checkpoints yet — they're written before the first destructive action of each turn")}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", styleMuted.Render(fmt.Sprintf("session %s", m.opts.Checkpoint.SessionID())))
	fmt.Fprintf(&b, "%-5s %-19s %-10s %s\n", "TURN", "TIMESTAMP", "BRANCH", "LABEL")
	for _, c := range list {
		branch := c.Branch
		if branch == "" {
			branch = "-"
		}
		fmt.Fprintf(&b, "%-5d %-19s %-10s %s\n",
			c.Turn,
			c.TS.Local().Format("2006-01-02 15:04:05"),
			truncateForCheckpoint(branch, 10),
			truncateForCheckpoint(c.Label, 64))
	}
	return cmdResult{text: b.String()}
}

func cmdRestore(m *Model, args string) cmdResult {
	if m.opts.Checkpoint == nil {
		return cmdResult{text: styleMuted.Render("/restore: unavailable (--no-save or session not initialised)")}
	}
	tokens := strings.Fields(args)
	turn := 0 // 0 → last
	force := false
	for _, tk := range tokens {
		switch tk {
		case "--force", "-f":
			force = true
		case "last", "latest":
			turn = 0
		default:
			n, err := strconv.Atoi(tk)
			if err != nil || n <= 0 {
				return cmdResult{text: styleErr.Render(fmt.Sprintf("/restore: invalid turn %q (try a positive integer or `last`)", tk))}
			}
			turn = n
		}
	}
	ctx, cancel := context.WithTimeout(m.opts.Ctx, 30*time.Second)
	defer cancel()
	res, err := m.opts.Checkpoint.RestoreGit(ctx, checkpoint.RestoreOptions{Turn: turn, Force: force})
	if err != nil {
		return cmdResult{text: styleErr.Render("/restore: " + err.Error())}
	}
	return cmdResult{text: fmt.Sprintf("restored turn %d (%d file(s) reset); HEAD unchanged at %s",
		res.Checkpoint.Turn, len(res.AffectedFiles), res.Checkpoint.HeadBefore)}
}

func cmdUndo(m *Model, args string) cmdResult {
	if m.opts.Checkpoint == nil {
		return cmdResult{text: styleMuted.Render("/undo: unavailable (--no-save or session not initialised)")}
	}
	tokens := strings.Fields(args)
	path := ""
	force := false
	for _, tk := range tokens {
		switch tk {
		case "--force", "-f":
			force = true
		default:
			if !strings.HasPrefix(tk, "-") {
				path = tk
			}
		}
	}
	results, err := m.opts.Checkpoint.Undo(checkpoint.UndoOptions{Path: path, Force: force})
	if err != nil {
		if len(results) == 0 {
			return cmdResult{text: styleErr.Render("/undo: " + err.Error())}
		}
		// Partial success.
		return cmdResult{text: fmt.Sprintf("/undo: %d step(s) ok; stopped at: %s", len(results), err.Error())}
	}
	if len(results) == 0 {
		return cmdResult{text: styleMuted.Render("/undo: nothing to undo")}
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "undone: %s\n", r.Restored)
	}
	return cmdResult{text: strings.TrimRight(b.String(), "\n")}
}

func cmdRedo(m *Model, args string) cmdResult {
	if m.opts.Checkpoint == nil {
		return cmdResult{text: styleMuted.Render("/redo: unavailable (--no-save or session not initialised)")}
	}
	tokens := strings.Fields(args)
	path := ""
	force := false
	for _, tk := range tokens {
		switch tk {
		case "--force", "-f":
			force = true
		default:
			if !strings.HasPrefix(tk, "-") {
				path = tk
			}
		}
	}
	results, err := m.opts.Checkpoint.Redo(checkpoint.RedoOptions{Path: path, Force: force})
	if err != nil {
		if len(results) == 0 {
			return cmdResult{text: styleErr.Render("/redo: " + err.Error())}
		}
		return cmdResult{text: fmt.Sprintf("/redo: %d step(s) ok; stopped at: %s", len(results), err.Error())}
	}
	if len(results) == 0 {
		return cmdResult{text: styleMuted.Render("/redo: nothing to redo")}
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "redone: %s\n", r.Restored)
	}
	return cmdResult{text: strings.TrimRight(b.String(), "\n")}
}

// truncateForCheckpoint trims a string to n runes with a horizontal-
// ellipsis suffix. Local helper so the checkpoint commands don't pull
// in a heavyweight unicode helper.
func truncateForCheckpoint(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// ----- v5 柱 G subagent slash command (feature-subagent.md §4.2) -----
//
// M11.0 ships a read-only text-table view, same pattern as
// /checkpoints. The fuller picker (Enter for detail / `k` to kill
// active subagents / live refresh as events arrive) is in
// feature-subagent.md §9 "v0.6.x dot" and lands after the M11.1
// worktree work — by then there'll be more state worth navigating.

func cmdAgents(m *Model, args string) cmdResult {
	if m.opts.Subagents == nil {
		return cmdResult{text: styleMuted.Render("/agents: unavailable (--no-save or session not initialised)")}
	}

	// Arg parsing. --all (or -a) widens the filter to the whole
	// project; default scope is the current session only (PRD §4.2
	// "当前 session 已 spawn 的子"). subagents.jsonl is project-
	// scoped on disk and accumulates across sessions, so an
	// unfiltered dump grows unbounded over a project's lifetime.
	showAll := false
	for _, tok := range strings.Fields(args) {
		switch tok {
		case "--all", "-a":
			showAll = true
		default:
			return cmdResult{text: styleErr.Render("/agents: unknown arg " + tok + " (only --all / -a)")}
		}
	}

	// Resolve the current session ID for the default filter. If
	// Session is nil (defensive — Subagents wiring in cmd/seek
	// already requires activeSession), fall back to --all behaviour
	// so the user still sees something rather than a confusing
	// "no subagents" message.
	currentSid := ""
	if m.opts.Session != nil {
		currentSid = m.opts.Session.ID
	}
	filter := subagent.ListFilter{}
	if !showAll && currentSid != "" {
		filter.ParentSid = currentSid
	}
	subs, err := m.opts.Subagents.List(filter)
	if err != nil {
		return cmdResult{text: styleErr.Render("/agents: " + err.Error())}
	}
	if len(subs) == 0 {
		if showAll || currentSid == "" {
			return cmdResult{text: styleMuted.Render("no subagents in this project yet — the model spawns them via the `agent` tool")}
		}
		// Empty in current session — check if there's project-wide
		// history so the hint can point at --all rather than just
		// saying "spawn one".
		all, _ := m.opts.Subagents.List(subagent.ListFilter{})
		if len(all) > 0 {
			return cmdResult{text: styleMuted.Render(fmt.Sprintf("no subagents in this session yet — pass `--all` to see %d from prior sessions in this project", len(all)))}
		}
		return cmdResult{text: styleMuted.Render("no subagents in this project yet — the model spawns them via the `agent` tool")}
	}

	// Column order matches PRD §4.2 — description goes right after
	// status so the user reads "what did this subagent do" without
	// having to scan past numeric noise. Tokens lives at the right
	// edge where number columns conventionally sit. SHORT (random
	// sub-sid suffix) is more readable than the full 22-char
	// timestamped ID and matches the wire-format footer shown in
	// tool results.
	//
	// TURNS column intentionally dropped: the event index
	// (subagents.jsonl) carries Tokens on the completed event but
	// not a turn count — the turn number lives in the wire-format
	// footer the tool result already echoes. Faking "-" for every
	// row burns a 5-char column for zero signal. If we later want
	// turns surfaced here, add a Turns int field to the completed
	// event payload + Subagent struct (see feature-subagent.md
	// §3.4 schema) rather than re-introducing a placeholder.
	//
	// Widths: SUB-SID(8) TYPE(11) STATUS(10) DESCRIPTION(50)
	// TOKENS(right). Total fits a typical 100-col TUI comfortably
	// even with longer descriptions.
	var b strings.Builder

	// Scope banner — tells the user whether they're looking at
	// just this session or the whole project, and how to switch.
	// Muted so it doesn't compete with the data rows.
	if showAll || currentSid == "" {
		fmt.Fprintf(&b, "%s\n", styleMuted.Render(fmt.Sprintf("scope: all project sessions (%d subagents)", len(subs))))
	} else {
		fmt.Fprintf(&b, "%s\n", styleMuted.Render(fmt.Sprintf("scope: current session %s (%d subagents) — pass `--all` for project-wide", shortSubSid(currentSid), len(subs))))
	}
	fmt.Fprintf(&b, "%-8s %-11s %-10s %-50s %8s\n",
		"SUB-SID", "TYPE", "STATUS", "DESCRIPTION", "TOKENS")
	for _, s := range subs {
		tokens := s.Tokens.Prompt + s.Tokens.Completion
		fmt.Fprintf(&b, "%-8s %-11s %-10s %-50s %8d\n",
			truncateForCheckpoint(shortSubSid(s.SubSid), 8),
			truncateForCheckpoint(string(s.Type), 11),
			truncateForCheckpoint(string(s.Status), 10),
			truncateForCheckpoint(s.Description, 50),
			tokens)
	}
	return cmdResult{text: strings.TrimRight(b.String(), "\n")}
}

// shortSubSid mirrors internal/subagent.shortSid — the random
// suffix after the last hyphen — but is duplicated here so the TUI
// doesn't import a private helper. If the wire-format owner
// promotes shortSid to a package-level export, replace this with
// the direct call.
func shortSubSid(subSid string) string {
	if i := strings.LastIndex(subSid, "-"); i >= 0 && i < len(subSid)-1 {
		return subSid[i+1:]
	}
	return subSid
}

// ----- v5 柱 G M11.1 worktree slash command -----
//
// Reads from disk (Manager.ListFromDisk) so worktrees from prior
// seek sessions show up too. The `git worktree list --porcelain`
// roundtrip is bounded (one shell-out per /worktrees invocation,
// no per-row Status to keep the latency low) — change-count is
// LEFT OUT here for the same reason: running git status N times
// would block the TUI on large projects. The user can drill into
// a specific worktree via `cd <path>; git status`.

func cmdWorktrees(m *Model, _ string) cmdResult {
	if m.opts.Worktrees == nil {
		return cmdResult{text: styleMuted.Render("/worktrees: unavailable (not a git repository, or Manager not wired)")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wts, err := m.opts.Worktrees.ListFromDisk(ctx)
	if err != nil {
		return cmdResult{text: styleErr.Render("/worktrees: " + err.Error())}
	}
	if len(wts) == 0 {
		return cmdResult{text: styleMuted.Render("no seek-managed worktrees — created by `agent` tool with isolation=\"worktree\" or direct `enter_worktree` calls")}
	}
	// Sort newest-first by ID (which embeds a timestamp) — same
	// convention as /agents.
	sort.Slice(wts, func(i, j int) bool { return wts[i].ID > wts[j].ID })

	var b strings.Builder
	// Column widths:
	//   WT-ID (22)  BRANCH (30, truncate)  PATH (rest, truncate)
	// PATH last so it can absorb arbitrary terminal width without
	// the layout cracking.
	fmt.Fprintf(&b, "%-22s %-30s %s\n", "WT-ID", "BRANCH", "PATH")
	for _, w := range wts {
		fmt.Fprintf(&b, "%-22s %-30s %s\n",
			truncateForCheckpoint(w.ID, 22),
			truncateForCheckpoint(w.Branch, 30),
			w.Path)
	}
	b.WriteString(styleMuted.Render("tip: `git -C <path> status` for per-tree change counts; `seek worktree gc` to prune rescue stashes"))
	return cmdResult{text: strings.TrimRight(b.String(), "\n")}
}
