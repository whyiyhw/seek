package tui

import tea "github.com/charmbracelet/bubbletea"

// normalizeControlRunes rewrites single-rune control characters that
// arrived as character payloads into their key-event equivalents:
// '\n'/'\r' → KeyEnter, '\b' → KeyBackspace.
//
// Why: on Windows, terminal/IME bridges can deliver Enter and Backspace
// as CHARACTER events instead of VK_RETURN/VK_BACK — Windows Terminal
// re-injects the IME-commit key alongside the committed text
// (microsoft/terminal#20039/#20471), and ConPTY synthesizes character
// events for input it cannot map to a virtual key. bubbletea translates
// those faithfully as KeyRunes, and the textarea then inserts a literal
// newline for Enter ("Enter became shift+enter") and ignores '\b'
// ("backspace is dead") — the exact signature of a stuck-IME report
// where typing still works. A raw control rune has no legitimate source
// in non-paste keyboard input: users cannot type \n, and every
// sanctioned newline path (the ctrl+j binding, the conhost paste guard,
// bracketed paste) delivers its own distinct message shape.
//
// Paste content is real payload — a pasted newline is content, not an
// Enter — so paste messages pass through untouched.
func normalizeControlRunes(msg tea.KeyMsg) tea.KeyMsg {
	if msg.Paste || len(msg.Runes) != 1 {
		return msg
	}
	switch msg.Runes[0] {
	case '\n', '\r':
		return tea.KeyMsg{Type: tea.KeyEnter}
	case '\b':
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return msg
}
