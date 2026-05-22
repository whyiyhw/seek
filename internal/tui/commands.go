package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		{names: []string{"/reset"}, usage: "/reset", description: "Start a fresh conversation (resets the agent's message history).", handler: cmdReset},
		{names: []string{"/model"}, usage: "/model <id>", description: "Switch the active model. Example: /model deepseek-reasoner", handler: cmdModel},
		{names: []string{"/yolo"}, usage: "/yolo", description: "Toggle --yolo for the rest of this session.", handler: cmdYolo},
		{names: []string{"/branch"}, usage: "/branch", description: "Fork this session: new ID, parent link, copy of history. Parent left intact on disk.", handler: cmdBranch},
		{names: []string{"/compact"}, usage: "/compact", description: "Summarise prior history into one message to free up context.", handler: cmdCompact},
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
				return true, resultToCmd(c.handler(m, args))
			}
		}
	}

	return true, resultToCmd(cmdResult{text: styleMuted.Render(fmt.Sprintf("unknown command %s — try /help", name))})
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

func cmdHelp(_ *Model, _ string) cmdResult {
	var sb strings.Builder
	sb.WriteString("commands:\n")
	sorted := allCommands()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].usage < sorted[j].usage })
	for _, c := range sorted {
		sb.WriteString(fmt.Sprintf("  %-22s  %s\n", c.usage, c.description))
	}
	sb.WriteString("\nkeys:\n")
	sb.WriteString("  Enter       send prompt\n")
	sb.WriteString("  Ctrl+J      newline in input\n")
	sb.WriteString("  Ctrl+L      clear visible screen (same as /clear)\n")
	sb.WriteString("  Ctrl+R      toggle reasoning visibility on streaming + committed messages\n")
	sb.WriteString("  Ctrl+C      quit\n")
	sb.WriteString("\nscroll: use your terminal's native scrollback (mouse wheel, Shift+PgUp, etc.) — seek does not capture mouse events.\n")
	return cmdResult{text: sb.String()}
}

func cmdClear(_ *Model, _ string) cmdResult {
	return cmdResult{clear: true}
}

func cmdReset(m *Model, _ string) cmdResult {
	if m.opts.RebuildAgent == nil {
		return cmdResult{text: styleMuted.Render("reset unsupported (rebuild hook not wired)")}
	}
	newAgent, err := m.opts.RebuildAgent()
	if err != nil {
		return cmdResult{text: styleErr.Render("reset failed: " + err.Error())}
	}
	m.opts.Agent = newAgent
	m.turns = 0
	m.toolCalls = 0
	m.curContent = ""
	m.curReasoning = ""
	m.activeTools = nil
	return cmdResult{clear: true, text: styleMuted.Render("agent reset — new conversation")}
}

func cmdModel(m *Model, args string) cmdResult {
	if args == "" {
		return cmdResult{text: styleMuted.Render(fmt.Sprintf(
			"current model: %s\nusage: /model <deepseek-chat|deepseek-reasoner|...>", m.opts.Model))}
	}
	prev := m.opts.Model
	m.opts.Model = args
	if m.opts.SetModel != nil {
		m.opts.SetModel(args)
	}
	return cmdResult{text: styleMuted.Render(fmt.Sprintf("model: %s → %s (effective on next prompt)", prev, args))}
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

func cmdQuit(_ *Model, _ string) cmdResult {
	return cmdResult{quit: true}
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
