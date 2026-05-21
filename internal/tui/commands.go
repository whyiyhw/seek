package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// command is a single /-prefixed in-app command.
type command struct {
	names       []string // first entry is canonical, rest are aliases
	usage       string   // includes the leading slash and any args
	description string
	handler     func(m *Model, args string) tea.Cmd
}

// allCommands returns the slash-command registry. It's a function (not
// a top-level var) so cmdHelp can read the list without hitting Go's
// initialisation-cycle restriction (the help handler is itself one of
// the entries).
func allCommands() []command {
	return []command{
	{
		names:       []string{"/help", "/?"},
		usage:       "/help",
		description: "Show this help.",
		handler:     cmdHelp,
	},
	{
		names:       []string{"/clear"},
		usage:       "/clear",
		description: "Clear the visible conversation. Agent state preserved.",
		handler:     cmdClear,
	},
	{
		names:       []string{"/reset"},
		usage:       "/reset",
		description: "Start a fresh conversation (resets the agent's message history).",
		handler:     cmdReset,
	},
	{
		names:       []string{"/model"},
		usage:       "/model <id>",
		description: "Switch the active model. Example: /model deepseek-reasoner",
		handler:     cmdModel,
	},
	{
		names:       []string{"/yolo"},
		usage:       "/yolo",
		description: "Toggle --yolo for the rest of this session (bash + writes outside CWD).",
		handler:     cmdYolo,
	},
		{
			names:       []string{"/exit", "/quit", "/q"},
			usage:       "/exit",
			description: "Quit seek.",
			handler:     cmdQuit,
		},
	}
}

// dispatchCommand parses a /-prefixed input and runs the matching
// command. Returns true if input was a command (even an unknown one);
// false means the caller should treat input as a normal prompt.
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
				return true, c.handler(m, args)
			}
		}
	}

	m.history = append(m.history, historyItem{
		role: "system",
		text: fmt.Sprintf("unknown command %s — try /help", name),
	})
	return true, nil
}

func cmdHelp(m *Model, _ string) tea.Cmd {
	var sb strings.Builder
	sb.WriteString("commands:\n")
	// Sort by canonical name for stable output.
	sorted := allCommands()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].usage < sorted[j].usage })
	for _, c := range sorted {
		sb.WriteString(fmt.Sprintf("  %-22s  %s\n", c.usage, c.description))
	}
	sb.WriteString("\nkeys:\n")
	sb.WriteString("  Enter       send prompt\n")
	sb.WriteString("  Ctrl+J      newline in input\n")
	sb.WriteString("  Ctrl+L      same as /clear\n")
	sb.WriteString("  Ctrl+R      toggle reasoning visibility for assistant messages\n")
	sb.WriteString("  Ctrl+C      quit\n")
	m.history = append(m.history, historyItem{role: "system", text: sb.String()})
	return nil
}

func cmdClear(m *Model, _ string) tea.Cmd {
	m.history = nil
	m.lastErr = nil
	m.curContent = ""
	m.curReasoning = ""
	m.viewport.SetContent(welcomeText(m.opts))
	m.viewport.GotoTop()
	return nil
}

func cmdReset(m *Model, _ string) tea.Cmd {
	cmdClear(m, "")
	if m.opts.RebuildAgent != nil {
		newAgent, err := m.opts.RebuildAgent()
		if err != nil {
			m.history = append(m.history, historyItem{
				role: "system",
				text: "reset failed: " + err.Error(),
			})
			return nil
		}
		m.opts.Agent = newAgent
		m.turns = 0
		m.toolCalls = 0
		m.history = append(m.history, historyItem{
			role: "system",
			text: "agent reset — new conversation",
		})
	} else {
		m.history = append(m.history, historyItem{
			role: "system",
			text: "reset unsupported (rebuild hook not wired)",
		})
	}
	return nil
}

func cmdModel(m *Model, args string) tea.Cmd {
	if args == "" {
		m.history = append(m.history, historyItem{
			role: "system",
			text: fmt.Sprintf("current model: %s\nusage: /model <deepseek-chat|deepseek-reasoner|...>", m.opts.Model),
		})
		return nil
	}
	prev := m.opts.Model
	m.opts.Model = args
	if m.opts.SetModel != nil {
		m.opts.SetModel(args)
	}
	m.history = append(m.history, historyItem{
		role: "system",
		text: fmt.Sprintf("model: %s → %s (effective on next prompt)", prev, args),
	})
	return nil
}

func cmdYolo(m *Model, _ string) tea.Cmd {
	m.opts.Yolo = !m.opts.Yolo
	if m.opts.SetYolo != nil {
		m.opts.SetYolo(m.opts.Yolo)
	}
	state := "off"
	if m.opts.Yolo {
		state = "on"
	}
	m.history = append(m.history, historyItem{
		role: "system",
		text: "yolo " + state,
	})
	return nil
}

func cmdQuit(_ *Model, _ string) tea.Cmd {
	return tea.Quit
}

// Side-effect hooks that let commands influence state owned by
// cmd/seek (agent rebuild, model switch, yolo toggle) live on Options.
// If a hook is nil the corresponding command becomes a no-op with a
// friendly message.
