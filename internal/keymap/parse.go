package keymap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// fileSchema mirrors the on-disk keybindings.toml. Schema:
//
//	[bindings]
//	submit     = "ctrl+enter"
//	clear-screen = "ctrl+x"
//
// Everything outside [bindings] is currently ignored (room for future
// keys like [profiles.vim] without breaking forward-compat). The
// underlying map type lets us preserve action names verbatim for
// "unknown action" warnings — toml struct tagging would silently drop
// them.
type fileSchema struct {
	Bindings map[string]string `toml:"bindings"`
}

// Load parses a keybindings.toml file and applies user overrides on
// top of the defaults. Validation errors emit human-readable lines
// on warn (one per problem) but do NOT return an error — Load always
// returns a usable KeyMap. The returned bool indicates whether the
// file was applied (true) or fully rejected back to defaults (false).
//
// Rejection happens on:
//   - hard toml syntax error → whole file ignored (PRD §4.4 rule 1)
//   - two actions on the same key → whole file ignored (rule 4)
//
// Soft errors (unknown action, reserved key, unparseable key, missing
// value) only drop the offending row and warn; other rows still apply.
//
// Pass io.Discard for warn to suppress warnings (the test suite does this).
func Load(path string, warn io.Writer) (*KeyMap, bool) {
	if warn == nil {
		warn = io.Discard
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// No file → silently use defaults. This is the common case for
		// users who never customise keybindings; we don't want a noisy
		// stderr message every TUI launch.
		return NewDefault(), true
	}
	if err != nil {
		fmt.Fprintf(warn, "keybindings: read %s: %v (using defaults)\n", path, err)
		return NewDefault(), false
	}
	return LoadBytes(data, path, warn)
}

// LoadBytes parses keybindings.toml from a byte slice. path is used
// only for warning prefixes; pass "" if not loading from disk. Same
// semantics as Load — always returns a usable KeyMap.
func LoadBytes(data []byte, path string, warn io.Writer) (*KeyMap, bool) {
	if warn == nil {
		warn = io.Discard
	}
	var raw fileSchema
	if _, err := toml.Decode(string(data), &raw); err != nil {
		fmt.Fprintf(warn, "keybindings: toml syntax error in %s: %v (using defaults)\n", displayPath(path), err)
		return NewDefault(), false
	}

	km := NewDefault()

	// Walk overrides in sorted order so warning output and the conflict
	// "first wins" rule are deterministic across runs — important for
	// reproducible test assertions.
	keys := make([]string, 0, len(raw.Bindings))
	for k := range raw.Bindings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type pending struct {
		action Action
		key    string
	}
	var accepted []pending
	conflicts := false

	for _, actionName := range keys {
		key := strings.TrimSpace(raw.Bindings[actionName])
		actionName = strings.TrimSpace(actionName)
		if !IsKnownAction(actionName) {
			fmt.Fprintf(warn, "keybindings: unknown action %q in %s (available: %s)\n",
				actionName, displayPath(path), knownActionsList())
			continue
		}
		if key == "" {
			fmt.Fprintf(warn, "keybindings: action %q has empty value in %s\n", actionName, displayPath(path))
			continue
		}
		key = normalizeKey(key)
		if IsReservedKey(key) {
			fmt.Fprintf(warn, "keybindings: reserved key %q cannot be rebound (%s = %q in %s)\n",
				key, actionName, key, displayPath(path))
			continue
		}
		if !looksLikeKnownKey(key) {
			fmt.Fprintf(warn, "keybindings: unrecognised key %q for action %q in %s (see `seek keys actions` for examples)\n",
				key, actionName, displayPath(path))
			continue
		}
		accepted = append(accepted, pending{Action(actionName), key})
	}

	// Conflict detection: same key claimed by two different actions.
	// Per PRD §4.4 rule 4 this invalidates the entire file rather than
	// resolving by "first wins" — the user must explicitly fix the
	// conflict, otherwise the resolved keymap depends on map iteration
	// order which is unpredictable.
	byKey := make(map[string][]Action)
	for _, p := range accepted {
		byKey[p.key] = append(byKey[p.key], p.action)
	}
	for key, actions := range byKey {
		if len(actions) > 1 {
			names := make([]string, len(actions))
			for i, a := range actions {
				names[i] = string(a)
			}
			fmt.Fprintf(warn, "keybindings: key %q bound to multiple actions (%s) in %s — rejecting whole file\n",
				key, strings.Join(names, ", "), displayPath(path))
			conflicts = true
		}
	}
	if conflicts {
		return NewDefault(), false
	}

	// Apply accepted overrides. Each override removes the default's
	// reverse-lookup entry first so a rebind doesn't leave a stale
	// "old key → action" pointer in byKey.
	for _, p := range accepted {
		oldKey := km.byAction[p.action]
		delete(km.byKey, oldKey)
		km.byAction[p.action] = p.key
		km.byKey[p.key] = p.action
		km.source[p.action] = "user"
	}
	return km, true
}

// Check parses the file and returns a slice of error strings (empty if
// valid). Used by `seek keys check` to dry-run validation for CI. Each
// returned string is one full message, ready to print.
func Check(path string) []string {
	buf := &strings.Builder{}
	_, ok := Load(path, buf)
	out := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if !ok && len(out) == 1 && out[0] == "" {
		// Defensive: if Load returned ok==false but emitted nothing
		// (shouldn't happen but be explicit), surface a generic
		// failure rather than returning an empty slice.
		return []string{fmt.Sprintf("keybindings: %s rejected for unknown reason", displayPath(path))}
	}
	if len(out) == 1 && out[0] == "" {
		return nil
	}
	return out
}

// displayPath shortens path for warning output. Empty path renders as
// "<inline>"; non-empty paths stay verbatim (users want to see the
// full path so they can edit the right file).
func displayPath(path string) string {
	if path == "" {
		return "<inline>"
	}
	return path
}

// normalizeKey lowercases the key string. bubbletea's KeyMsg.String()
// is always lowercase (e.g. "ctrl+c" / "shift+tab"), so we normalise
// user input to match — "Ctrl+C" still works in the toml. Whitespace
// inside the key is preserved so looksLikeKnownKey can reject obvious
// multi-word junk like "weird key with spaces".
func normalizeKey(key string) string {
	return strings.ToLower(key)
}

// knownActionsList returns a comma-separated list of action names for
// the "available: …" hint in unknown-action warnings.
func knownActionsList() string {
	infos := AllActions()
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = string(info.Action)
	}
	return strings.Join(out, ", ")
}

// looksLikeKnownKey is a permissive heuristic — it rejects obvious
// junk (random unicode, missing modifier, etc.) but accepts anything
// that follows bubbletea's "modifier+name" shape. bubbletea itself
// is the final arbiter; this layer just catches the most common
// typos at parse time so the user gets an early warning instead of a
// silent "binding never fires".
//
// Accepted shapes:
//
//	a              single rune
//	?              single rune (special chars allowed)
//	enter          named key
//	ctrl+c         modifier + rune
//	alt+enter      modifier + named key
//	ctrl+shift+a   stacked modifiers
//	f1 … f12       function keys
func looksLikeKnownKey(key string) bool {
	if key == "" {
		return false
	}
	parts := strings.Split(key, "+")
	for i, p := range parts {
		if p == "" {
			return false
		}
		if i < len(parts)-1 {
			// non-final part must be a modifier
			switch p {
			case "ctrl", "alt", "shift", "cmd", "meta", "super":
				continue
			default:
				return false
			}
		}
	}
	// Final part is the actual key. We don't enumerate every named key
	// bubbletea supports (it changes across versions); just reject if
	// the final part contains whitespace or "+" itself.
	last := parts[len(parts)-1]
	if strings.ContainsAny(last, " \t") {
		return false
	}
	return true
}
