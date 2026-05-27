package keymap

import (
	tea "github.com/charmbracelet/bubbletea"
)

// KeyMap is the resolved (action → key) and (key → action) tables.
// Construct via NewDefault() or LoadFromFile(); never mutate after
// construction (Resolve is called from the TUI key-dispatch hot path).
//
// The two maps are kept in sync at build time so Resolve is a single
// hashmap lookup and Snapshot is an O(actions) walk.
type KeyMap struct {
	byAction map[Action]string
	byKey    map[string]Action

	// Source records the origin of each binding so /help and `seek keys
	// list` can label "default" vs "user". Same keyset as byAction.
	source map[Action]string // "default" | "user"
}

// NewDefault returns a KeyMap with all actions bound to their default
// keys. Used when no keybindings.toml exists or when the file is
// rejected by the parser (validation errors → full fallback).
func NewDefault() *KeyMap {
	km := &KeyMap{
		byAction: defaultBindings(),
		byKey:    make(map[string]Action, 10),
		source:   make(map[Action]string, 10),
	}
	for action, key := range km.byAction {
		km.byKey[key] = action
		km.source[action] = "default"
	}
	return km
}

// Resolve returns the Action bound to msg's canonical key string, or
// ActionNone if no action is bound. The canonical form is bubbletea's
// own KeyMsg.String() (e.g. "ctrl+c", "alt+enter", "shift+tab", "?")
// — same string the user writes in keybindings.toml.
func (km *KeyMap) Resolve(msg tea.KeyMsg) Action {
	if km == nil {
		return ActionNone
	}
	if action, ok := km.byKey[msg.String()]; ok {
		return action
	}
	return ActionNone
}

// KeyFor returns the currently bound key for action, or "" if action
// is unknown. Used by the /help overlay to render the live keymap.
func (km *KeyMap) KeyFor(action Action) string {
	if km == nil {
		return ""
	}
	return km.byAction[action]
}

// SourceFor returns "default" or "user" depending on the origin of
// action's current binding. Empty string for unknown actions. Used
// by `seek keys list` to flag user overrides.
func (km *KeyMap) SourceFor(action Action) string {
	if km == nil {
		return ""
	}
	return km.source[action]
}

// Binding is one row in a Snapshot — surfaces enough metadata for
// the /help overlay and `seek keys list --json` to render without
// re-reading the package's internal tables.
type Binding struct {
	Action      Action
	Key         string
	Source      string // "default" | "user"
	Description string
}

// Snapshot returns the current bindings in the canonical AllActions
// display order. The /help overlay calls this on every render so
// user changes show up without a TUI restart.
func (km *KeyMap) Snapshot() []Binding {
	out := make([]Binding, 0, 10)
	for _, info := range AllActions() {
		out = append(out, Binding{
			Action:      info.Action,
			Key:         km.byAction[info.Action],
			Source:      km.source[info.Action],
			Description: info.Description,
		})
	}
	return out
}
