package tui

import "strings"

// updateCommandMenu recomputes the slash-command dropdown state from
// the current input value. Called after every textarea-bound key.
//
// State machine:
//
//	"/"             → command menu (filters as you type)
//	"/model "       → model picker (typing "/model<space>" hands off
//	                  to the model dropdown; the user no longer needs
//	                  to commit with Enter just to see what's available)
//	anything else   → both closed
//
// The handoff at "/model " keeps the screen from going visually
// empty in the half-second between "/" menu (closes on first space)
// and an Enter that opens the model picker explicitly.
func (m *Model) updateCommandMenu() {
	v := strings.TrimRight(m.input.Value(), "\n")

	// Branch 1: "<cmd><space>..." — auto-open pickers for model / effort.
	// Close any open command menu first; they're mutually exclusive.
	// Stale cleanup (Branch 2 below) handles closing when the user backspaces.
	switch {
	case strings.HasPrefix(v, "/model ") || v == "/model ":
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "model" {
			m.modelPickerFiltered = knownModelsForProvider(m.opts.ProviderName)
			if len(m.modelPickerFiltered) == 0 {
				return
			}
			m.modelPickerSelected = 0
			for i, mc := range m.modelPickerFiltered {
				if mc.id == m.opts.Model {
					m.modelPickerSelected = i
					break
				}
			}
			m.modelPickerOpen = true
			m.pickerPurpose = "model"
		}
		return

	case strings.HasPrefix(v, "/effort ") || v == "/effort ":
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "effort" {
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
		}
		return

	case strings.HasPrefix(v, "/help ") || v == "/help ":
		// Auto-open the help topic picker when the user types "/help "
		// (trailing space). Mirrors the /model /effort pattern.
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "help-topic" {
			choices := helpTopicChoices()
			m.modelPickerFiltered = choices
			m.modelPickerSelected = 0
			m.modelPickerOpen = true
			m.pickerPurpose = "help-topic"
		}
		return

	case strings.HasPrefix(v, "/review "):
		// Auto-open the review picker when the user types "/review "
		// (trailing space). Mirrors the /model /effort pattern.
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		if !m.modelPickerOpen || m.pickerPurpose != "review" {
			choices := reviewChoices(m.opts.CWD)
			if len(choices) == 0 {
				return
			}
			m.modelPickerFiltered = choices
			m.modelPickerSelected = 0
			m.modelPickerOpen = true
			m.pickerPurpose = "review"
		}
		return

	// `/skill use <partial>` — second-level picker for loaded skill
	// names. Check this BEFORE the `/skill ` branch below: prefix
	// "/skill use " also matches "/skill " naively, and we want the
	// name picker to win once the user has committed to the `use`
	// verb. We close the picker once the user types a space after the
	// name (i.e. they're moving on to the inline task), so candidates
	// don't keep showing up under unrelated text.
	case strings.HasPrefix(v, "/skill use ") || v == "/skill use ":
		tail := strings.TrimPrefix(v, "/skill use ")
		if strings.Contains(tail, " ") {
			// User has moved past the name into the inline-task
			// position. Close any stale name picker and fall through
			// so the live region returns to plain composition.
			if m.modelPickerOpen && m.pickerPurpose == "skill-name" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			m.commandMenuOpen = false
			m.commandMenuFiltered = nil
			m.commandMenuSelected = 0
			return
		}
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		all := skillNameChoices(m.opts.Skills)
		filtered := filterChoicesByPrefix(all, tail)
		if len(filtered) == 0 {
			// Nothing to pick. Close any prior open picker; the user
			// keeps typing into a plain textarea (cmdSkillUse will
			// give a clear error if they Enter with an unknown name).
			if m.modelPickerOpen && m.pickerPurpose == "skill-name" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			return
		}
		m.modelPickerFiltered = filtered
		// Reset selection to top — keystrokes that change the filter
		// shouldn't strand the highlight on a row that's no longer
		// in the candidate set.
		m.modelPickerSelected = 0
		m.modelPickerOpen = true
		m.pickerPurpose = "skill-name"
		return

	// `/skill <verb-partial>` — first-level picker for sub-verbs.
	// Triggered by the space after `/skill`; closes once the user has
	// typed past the verb (any second space). The handoff into the
	// name picker for `use` happens on the next updateCommandMenu
	// cycle: applyModelChoice replaces the textarea contents with
	// "/skill use " which then matches the branch above.
	case strings.HasPrefix(v, "/skill ") || v == "/skill ":
		tail := strings.TrimPrefix(v, "/skill ")
		if strings.Contains(tail, " ") {
			// Past the verb — close.
			if m.modelPickerOpen && m.pickerPurpose == "skill-verb" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			m.commandMenuOpen = false
			m.commandMenuFiltered = nil
			m.commandMenuSelected = 0
			return
		}
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		filtered := filterChoicesByPrefix(skillVerbChoices(), tail)
		if len(filtered) == 0 {
			if m.modelPickerOpen && m.pickerPurpose == "skill-verb" {
				m.modelPickerOpen = false
				m.modelPickerFiltered = nil
				m.modelPickerSelected = 0
				m.pickerPurpose = ""
			}
			return
		}
		m.modelPickerFiltered = filtered
		m.modelPickerSelected = 0
		m.modelPickerOpen = true
		m.pickerPurpose = "skill-verb"
		return
	}

	// Branch 2: not in a known auto-open state but a stale auto-opened picker
	// is still showing (e.g. user backspaced the space). Close it.
	if m.modelPickerOpen && (m.pickerPurpose == "model" || m.pickerPurpose == "effort" || m.pickerPurpose == "review" || m.pickerPurpose == "skill-verb" || m.pickerPurpose == "skill-name" || m.pickerPurpose == "help-topic") {
		m.modelPickerOpen = false
		m.modelPickerFiltered = nil
		m.modelPickerSelected = 0
		m.pickerPurpose = ""
	}

	// Branch 3: standard slash-command menu (no space yet).
	if !strings.HasPrefix(v, "/") || strings.Contains(v, " ") {
		m.commandMenuOpen = false
		m.commandMenuFiltered = nil
		m.commandMenuSelected = 0
		return
	}
	m.commandMenuOpen = true
	m.commandMenuFiltered = filterCommands(allCommands(), v)
	if m.commandMenuSelected >= len(m.commandMenuFiltered) {
		m.commandMenuSelected = 0
	}
}

// filterCommands returns the subset of cmds whose canonical name OR
// any alias starts with prefix. Empty prefix → return everything.
// Order is preserved (allCommands() order is intentional).
func filterCommands(cmds []command, prefix string) []command {
	if prefix == "" || prefix == "/" {
		return cmds
	}
	var out []command
	for _, c := range cmds {
		for _, name := range c.names {
			if strings.HasPrefix(name, prefix) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// longestCommonCandidatePrefix returns the longest string that is a
// prefix of every CANONICAL name (`c.names[0]`) in cmds. Used by Tab
// completion to fill multi-candidate input to the unambiguous prefix
// (readline/bash semantics): `/ski` with candidates [/skill, /skills]
// returns `/skill`; the user sees visible progress and the picker
// stays open for further disambiguation.
//
// Returns "" if cmds is empty or no shared prefix exists (different
// first character). Compares bytes — all slash commands are ASCII,
// so codepoint boundaries aren't a concern.
func longestCommonCandidatePrefix(cmds []command) string {
	if len(cmds) == 0 {
		return ""
	}
	lcp := cmds[0].names[0]
	for _, c := range cmds[1:] {
		lcp = commonPrefixBytes(lcp, c.names[0])
		if lcp == "" {
			return ""
		}
	}
	return lcp
}

// commonPrefixBytes returns the longest byte-level common prefix of a
// and b. Safe for ASCII (slash command names); for arbitrary UTF-8 use
// a rune-aware variant.
func commonPrefixBytes(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}
