package tui

import tea "charm.land/bubbletea/v2"

// normalizeControlRunes rewrites single-rune control characters that
// arrived as character payloads into their key-event equivalents:
// '\n'/'\r' → KeyEnter, '\b' → KeyBackspace.
//
// Why: on Windows, terminal/IME bridges can deliver Enter and Backspace
// as CHARACTER events instead of VK_RETURN/VK_BACK — Windows Terminal
// re-injects the IME-commit key alongside the committed text
// (microsoft/terminal#20039/#20471), and ConPTY synthesizes character
// events for input it cannot map to a virtual key. bubbletea translates
// those faithfully as character payloads (Text), and the textarea then
// inserts a literal newline for Enter ("Enter became shift+enter") and
// ignores '\b' ("backspace is dead") — the exact signature of a
// stuck-IME report where typing still works. A raw control rune has no
// legitimate source in non-paste keyboard input: users cannot type \n,
// and every sanctioned newline path (the ctrl+j binding, the conhost
// paste guard, bracketed paste) delivers its own distinct message shape.
//
// In v2, paste arrives as a dedicated PasteMsg rather than a key with a
// Paste flag, so no paste passthrough is needed here anymore — pasted
// content never enters the key path.
func normalizeControlRunes(msg tea.KeyPressMsg) tea.KeyPressMsg {
	if len(msg.Text) != 1 {
		return msg
	}
	switch msg.Text[0] {
	case '\n', '\r':
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case '\b':
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	return msg
}
