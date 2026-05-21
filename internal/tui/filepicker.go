package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pathScanLimit bounds how many paths we'll ever load into memory for
// the @-completer. Big repos can have hundreds of thousands of files;
// loading them all defeats the "instant menu" goal and bloats RSS.
const pathScanLimit = 5000

// skipDir returns true if a directory entry should be excluded from the
// @-completer scan. Keep this list tight — every entry slows the warm-
// up scan, but every wrong inclusion clutters the menu.
func skipDir(name string) bool {
	switch name {
	case ".git", ".svn", ".hg",
		"node_modules", "vendor",
		".idea", ".vscode",
		"dist", "build", "target",
		"__pycache__", ".pytest_cache",
		".next", ".cache",
		".DS_Store":
		return true
	}
	// Hidden by default (covers .env, .venv, .gradle, etc.). Users who
	// want to @-reference dotfiles can still type the path manually.
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

// scanWorkspace walks root once and returns up to pathScanLimit
// repo-relative file paths. Sorted for stable display order. Called
// from cmd/seek (before tui.Run) so the warm-up doesn't show up as
// "the TUI took a second to appear" — but it's cheap enough that even
// inline at TUI start is fine. We keep it here so the cost is owned
// by the consumer that needs it.
func scanWorkspace(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Unreadable directory — skip its subtree but keep going.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if skipDir(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		out = append(out, rel)
		if len(out) >= pathScanLimit {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// pathCompleterState is the @-completer's bookkeeping. Lives on Model.
type pathCompleterState struct {
	all        []string // sorted full list, populated once
	filtered   []string
	selected   int
	open       bool
	tokenStart int    // byte offset in input.Value() where "@" appears
	token      string // the partial text after "@" (may be empty)
}

// updatePathCompleter recomputes whether the @-menu is open based on
// the current input value and cursor position. We approximate "cursor
// position" with "end of input" since bubbles/textarea doesn't expose
// a robust offset and the common case is typing at the end.
//
// Open conditions:
//   - input contains "@"
//   - the segment between the LAST "@" and the end of input has no
//     whitespace (i.e. the user is mid-token)
//
// On open, we populate filtered with paths whose basename starts with
// the token (case-insensitive). If filtered comes back empty we keep
// open=true with an empty list so the View shows "(no matches)".
func (m *Model) updatePathCompleter() {
	v := m.input.Value()
	idx := strings.LastIndex(v, "@")
	if idx < 0 {
		m.pathPicker.open = false
		m.pathPicker.filtered = nil
		return
	}
	// Token starts AFTER the @. If anything between @ and EOL is
	// whitespace, the user is done with this token.
	after := v[idx+1:]
	if strings.ContainsAny(after, " \t\n") {
		m.pathPicker.open = false
		m.pathPicker.filtered = nil
		return
	}
	m.pathPicker.open = true
	m.pathPicker.tokenStart = idx
	m.pathPicker.token = after
	m.pathPicker.filtered = filterPaths(m.pathPicker.all, after)
	if m.pathPicker.selected >= len(m.pathPicker.filtered) {
		m.pathPicker.selected = 0
	}
}

// filterPaths returns up to 20 paths whose basename starts with query
// (case-insensitive). Rank is: basename prefix > path substring
// contains; within each tier the underlying sort.Strings order wins.
//
// 20 is a soft cap to keep the dropdown short; longer lists feel
// useless once they scroll off-screen.
func filterPaths(all []string, query string) []string {
	const cap = 20
	if query == "" {
		// Empty query: just return the first cap entries so the menu
		// shows *something* the moment "@" is typed.
		if len(all) > cap {
			return all[:cap]
		}
		return all
	}
	q := strings.ToLower(query)
	var prefix, contains []string
	for _, p := range all {
		base := strings.ToLower(filepath.Base(p))
		if strings.HasPrefix(base, q) {
			prefix = append(prefix, p)
			if len(prefix) >= cap {
				return prefix
			}
			continue
		}
		if strings.Contains(strings.ToLower(p), q) {
			contains = append(contains, p)
		}
	}
	out := prefix
	for _, p := range contains {
		if len(out) >= cap {
			break
		}
		out = append(out, p)
	}
	return out
}

// applyPathCompletion replaces the "@token" segment with the selected
// path. Called when the user presses Tab while the picker is open.
func (m *Model) applyPathCompletion() {
	if !m.pathPicker.open || len(m.pathPicker.filtered) == 0 {
		return
	}
	picked := m.pathPicker.filtered[m.pathPicker.selected]
	v := m.input.Value()
	// Replace from "@" to end-of-token with "@<path> ".
	before := v[:m.pathPicker.tokenStart]
	m.input.SetValue(before + "@" + picked + " ")
	m.pathPicker.open = false
	m.pathPicker.filtered = nil
	m.pathPicker.selected = 0
}
