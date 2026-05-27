// Package keymap is the v3 柱 C user-customisable keybindings subsystem
// (PRD docs/prd/feature-tui-ergonomics.md).
//
// Design contract:
//
//   - A closed set of named Actions (see actions.go) — users can rebind
//     them but cannot invent new ones, and they cannot bind keys to
//     "arbitrary shell commands". This trades off flexibility for
//     learnability and cross-platform consistency.
//   - Bindings load from ~/.seek/keybindings.toml (user-level only —
//     keybindings are personal muscle memory, NOT project state).
//   - Resolve(tea.KeyMsg) Action is the dispatch primitive used by the
//     TUI's update_key.go. Returns ActionNone for keys with no binding;
//     callers fall through to default textarea handling.
//   - Parse errors emit stderr warnings + fallback to defaults. A
//     conflict (two actions on the same key) invalidates the whole
//     file (PRD §4.4) — silent partial failure would surprise users.
package keymap

// Action is a named, rebindable behaviour exposed by the TUI. The
// constant value is the user-facing identifier (matches the toml key
// in keybindings.toml and the column in `seek keys actions`).
type Action string

// All actions exposed by the TUI. Order in AllActions() matters for
// `seek keys actions` and the /help overlay — keep it stable.
const (
	// ActionNone is returned by Resolve when no binding matches.
	// Callers use this to fall through to default handling (e.g.
	// forwarding the key to the textarea).
	ActionNone Action = ""

	ActionSubmit          Action = "submit"
	ActionSteer           Action = "steer"
	ActionInterrupt       Action = "interrupt"
	ActionCancel          Action = "cancel"
	ActionCycleMode       Action = "cycle-mode"
	ActionClearScreen     Action = "clear-screen"
	ActionToggleReasoning Action = "toggle-reasoning"
	ActionToggleHelp      Action = "toggle-help"
	ActionHistoryPrev     Action = "history-prev"
	ActionHistoryNext     Action = "history-next"
)

// ActionInfo describes one Action — its identifier, default key, and
// one-line description. The /help overlay and `seek keys actions`
// command render directly from this table.
type ActionInfo struct {
	Action      Action
	Default     string
	Description string
}

// AllActions returns the closed set of rebindable actions in stable
// display order. Adding a new action here also requires updating the
// dispatch logic in internal/tui/update_key.go.
func AllActions() []ActionInfo {
	return []ActionInfo{
		{ActionSubmit, "enter", "Submit the current input (or queue while streaming)"},
		{ActionSteer, "alt+enter", "Interrupt the current stream and inject the input as a steer"},
		{ActionInterrupt, "ctrl+c", "Quit seek (saves the session first)"},
		{ActionCancel, "esc", "Cancel: stream / review-entry / setup-wizard / open picker"},
		{ActionCycleMode, "shift+tab", "Cycle permission mode: ask → plan → yolo"},
		{ActionClearScreen, "ctrl+l", "Clear the visible terminal (scrollback preserved)"},
		{ActionToggleReasoning, "ctrl+r", "Toggle whether reasoning chunks render in the transcript"},
		{ActionToggleHelp, "?", "Open the help overlay (only when input is empty)"},
		{ActionHistoryPrev, "up", "Previous prompt from history (only when input is empty)"},
		{ActionHistoryNext, "down", "Next prompt from history"},
	}
}

// defaultBindings returns the action → key map seeded from
// AllActions. Internal helper for NewDefault.
func defaultBindings() map[Action]string {
	out := make(map[Action]string, 10)
	for _, info := range AllActions() {
		out[info.Action] = info.Default
	}
	return out
}

// reservedKeys lists keys that must NEVER be rebound to a non-default
// action because doing so breaks the textarea or picker UI. Rebinding
// these is rejected at parse time (PRD §2 "不做什么" + §4.4 rule 5).
//
// Rationale per key:
//   - tab: M9.5 Tab completion + all picker accept handlers
//   - backspace: textarea native edit
//   - space: textarea native edit
//   - pgup / pgdn / home / end: terminal-native scrollback
//
// Note: enter, esc, up, down are NOT here — they ARE rebindable as
// the source for ActionSubmit / ActionCancel / ActionHistoryPrev /
// ActionHistoryNext. Their special "fall through to textarea when
// input is non-empty" behaviour is handled by the dispatch code in
// update_key.go, not by the keymap layer.
var reservedKeys = map[string]struct{}{
	"tab":       {},
	"backspace": {},
	"space":     {},
	"pgup":      {},
	"pgdn":      {},
	"home":      {},
	"end":       {},
}

// IsReservedKey reports whether key cannot be rebound. Used by the
// parser to reject `clear-screen = "tab"` etc.
func IsReservedKey(key string) bool {
	_, ok := reservedKeys[key]
	return ok
}

// IsKnownAction reports whether name is a valid Action identifier.
// Used by the parser to detect typos like "submmit".
func IsKnownAction(name string) bool {
	for _, info := range AllActions() {
		if string(info.Action) == name {
			return true
		}
	}
	return false
}
