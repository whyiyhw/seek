package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/memorycli"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skillcli"
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
	handler     func(m *Model, args string) cmdResult
}

func allCommands() []command {
	return []command{
		{names: []string{"/help", "/?"}, usage: "/help", description: "Show this help.", handler: cmdHelp},
		{names: []string{"/clear"}, usage: "/clear", description: "Clear the visible screen (scrollback preserved by your terminal).", handler: cmdClear},
		{names: []string{"/new"}, usage: "/new", description: "Start a fresh conversation (saves the current session first).", handler: cmdNew},
		{names: []string{"/model"}, usage: "/model [id]", description: "Switch the active model. No args opens a picker; pass an id to skip it (e.g. /model deepseek-v4-pro).", handler: cmdModel},
		{names: []string{"/effort"}, usage: "/effort [off|high|max]", description: "Set DeepSeek reasoning_effort for this session. No args opens a picker. off clears the override; high/max force Thinking on and tune the depth. Think tool runs one level above.", handler: cmdEffort},
		{names: []string{"/lang"}, usage: "/lang [en|zh|auto]", description: "Set response language preference. No args opens a picker. Switching invalidates the prefix cache; effective after /new.", handler: cmdLang},
		{names: []string{"/yolo"}, usage: "/yolo", description: "Toggle --yolo for the rest of this session.", handler: cmdYolo},
		{names: []string{"/plan"}, usage: "/plan", description: "Toggle plan mode (read-only exploration) for the rest of this session.", handler: cmdPlan},
		{names: []string{"/branch"}, usage: "/branch", description: "Fork this session: new ID, parent link, copy of history. Parent left intact on disk.", handler: cmdBranch},
		{names: []string{"/compact"}, usage: "/compact", description: "Summarise prior history into one message to free up context.", handler: cmdCompact},
		{names: []string{"/distill"}, usage: "/distill", description: "Thinking-mode-extract project-level decisions from this session into M memory (per-candidate y/n/e review).", handler: cmdDistill},
		{names: []string{"/skills"}, usage: "/skills", description: "List loaded skills with source paths.", handler: cmdSkills},
		{names: []string{"/skill"}, usage: "/skill <verb> [args]", description: "Manage skill packages (mirrors the `seek skill` CLI: install, uninstall, update, list, status, stats, help).", handler: cmdSkillCLI},
		{names: []string{"/memory"}, usage: "/memory <verb> [args]", description: "Inspect project memory (mirrors the `seek memory` CLI: list, show, search, archive).", handler: cmdMemoryCLI},
		{names: []string{"/setup"}, usage: "/setup", description: "Re-run the API-key wizard. Saves to ~/.seek/config.json.", handler: cmdSetup},
		{names: []string{"/upgrade"}, usage: "/upgrade [--force] [--dry-run]", description: "Download the latest release and replace this binary in place.", handler: cmdUpgrade},
		{names: []string{"/exit", "/quit", "/q"}, usage: "/exit", description: "Quit seek.", handler: cmdQuit},
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
				m.scrollbackLines += scrollbackLineCount(res.text)
				return true, resultToCmd(res)
			}
		}
	}

	text := styleMuted.Render(fmt.Sprintf("unknown command %s — try /help", name))
	m.scrollbackLines += scrollbackLineCount(text)
	return true, resultToCmd(cmdResult{text: text})
}

func resultToCmd(r cmdResult) tea.Cmd {
	var cmds []tea.Cmd
	if r.text != "" {
		cmds = append(cmds, tea.Println(r.text))
	}
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

func cmdHelp(m *Model, _ string) cmdResult {
	m.helpOverlayOpen = true
	return cmdResult{}
}

func cmdClear(_ *Model, _ string) cmdResult {
	return cmdResult{clear: true}
}

func cmdNew(m *Model, _ string) cmdResult {
	if m.opts.RebuildAgent == nil {
		return cmdResult{text: styleMuted.Render("/new unsupported (rebuild hook not wired)")}
	}
	// Save the current session before starting fresh.
	m.persistSession()

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

	newAgent, err := m.opts.RebuildAgent()
	if err != nil {
		return cmdResult{text: styleErr.Render("/new failed: " + err.Error())}
	}
	m.opts.Agent = newAgent
	m.turns = 0
	m.toolCalls = 0
	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
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
		// The legacy "deepseek-reasoner" alias still works via
		// /model deepseek-reasoner and --model deepseek-reasoner
		// (see pkg/deepseek.ShouldEnableThinking), but the picker
		// surfaces the explicit V4 name so users see what they're
		// actually buying instead of relying on DeepSeek's server-side
		// alias routing (which has silently demoted reasoner→V4-Flash
		// in the past).
		return []modelChoice{
			{"deepseek-chat", "DeepSeek V4-Flash — fast chat + tools (default)"},
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

	case "lang":
		selected := choice.id
		if selected == "auto" {
			selected = ""
		}
		m.applyLangChoice(selected)
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
	}
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

func cmdLang(m *Model, args string) cmdResult {
	if m.opts.SetLang == nil {
		return cmdResult{text: styleMuted.Render("/lang unavailable in this build (SetLang hook not wired)")}
	}

	// Arg path: accept "en" | "zh" | "auto" directly.
	if args != "" {
		var value string
		switch strings.ToLower(args) {
		case "en":
			value = "en"
		case "zh":
			value = "zh"
		case "auto", "":
			value = ""
		default:
			return cmdResult{text: styleMuted.Render(fmt.Sprintf(
				"/lang: unknown %q — try en|zh|auto", args))}
		}
		prev := displayLang(m.opts.Lang)
		m.applyLangChoice(value)
		return cmdResult{text: styleMuted.Render(fmt.Sprintf(
			"language: %s → %s (effective on next prompt)", prev, displayLang(value)))}
	}

	// No args: open the picker. Preselect the row matching the current
	// setting so Enter without motion is a no-op.
	choices := langChoices()
	m.modelPickerFiltered = choices
	m.modelPickerSelected = 0
	current := m.opts.Lang
	if current == "" {
		current = "auto"
	}
	for i, c := range choices {
		if c.id == current {
			m.modelPickerSelected = i
			break
		}
	}
	m.modelPickerOpen = true
	m.pickerPurpose = "lang"
	return cmdResult{}
}

// langChoices is the curated picker for /lang.
func langChoices() []modelChoice {
	return []modelChoice{
		{"auto", "auto — detect from system locale"},
		{"en", "en — English"},
		{"zh", "zh — 中文"},
	}
}

// displayLang formats the wire value ("" | "en" | "zh") for display.
// "" is shown as "auto".
func displayLang(v string) string {
	if v == "" {
		return "auto"
	}
	return v
}

// applyLangChoice writes the new /lang value through SetLang and
// persists to the in-memory session. Mirrors applyEffortChoice.
func (m *Model) applyLangChoice(value string) {
	prev := m.opts.Lang
	m.opts.Lang = value
	if m.opts.SetLang != nil {
		m.opts.SetLang(value)
	}
	if m.opts.Agent != nil {
		m.opts.Agent.SetLang(value)
	}
	if m.opts.Session != nil {
		m.opts.Session.Lang = value
	}
	_ = prev // reserved for a future Println

	m.refreshPlaceholder()
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
		line := styleMuted.Render("  setup: empty key — cancelled.")
		m.scrollbackLines += scrollbackLineCount(line)
		return tea.Println(line)
	}

	cfg, err := config.Load()
	if err != nil {
		line := styleErr.Render("  setup: load config: " + err.Error())
		m.scrollbackLines += scrollbackLineCount(line)
		return tea.Println(line)
	}
	config.SetKey(&cfg, provider, key)
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = provider
	}
	if err := config.Save(cfg); err != nil {
		line := styleErr.Render("  setup: save: " + err.Error())
		m.scrollbackLines += scrollbackLineCount(line)
		return tea.Println(line)
	}
	path, _ := config.Path()
	line := styleMuted.Render(fmt.Sprintf(
		"  setup: saved %s key to %s — restart seek or /new to apply",
		provider, path))
	m.scrollbackLines += scrollbackLineCount(line)
	return tea.Println(line)
}

// cancelSetup clears in-flight setup state without saving. Called
// by handleKey when Esc is pressed in key-entry mode.
func (m *Model) cancelSetup() tea.Cmd {
	m.setupKeyEntry = false
	m.setupProvider = ""
	m.input.Reset()
	line := styleMuted.Render("  setup: cancelled (no changes)")
	m.scrollbackLines += scrollbackLineCount(line)
	return tea.Println(line)
}

func cmdYolo(m *Model, _ string) cmdResult {
	m.opts.Yolo = !m.opts.Yolo
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
	m.opts.Plan = !m.opts.Plan
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

func cmdQuit(_ *Model, _ string) cmdResult {
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
func cmdSkillCLI(_ *Model, args string) cmdResult {
	tokens := strings.Fields(args)
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

// cmdMemoryCLI mirrors the `seek memory ...` CLI inside the TUI. Same
// wiring as cmdSkillCLI: whitespace-split args, buffered IO, rendered as
// scrollback.
//
// Limitation (documented): args are whitespace-split via strings.Fields,
// so values containing spaces (e.g. -tagline "hello world") are split into
// separate tokens. The underlying CLI's FlagSet only sees the first token,
// causing unexpected parse errors. This matches the identical limitation
// in cmdSkillCLI — both use the same dispatcher pattern.
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
// Kept alongside /skill (mirror of CLI list) because /skills is the
// pre-v2 muscle memory; deleting it would break existing user habits.
func cmdSkills(m *Model, _ string) cmdResult {
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
	m.turns = 0
	m.toolCalls = 0

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
	m.scrollbackLines += scrollbackLineCount(line)
	return []tea.Cmd{tea.Println(line)}
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
