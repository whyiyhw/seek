package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/memory"
)

// handleDistillDone is the entry point for the async reasoner result.
// On error or no candidates we print a single muted line to scrollback
// and stay idle. On a non-empty candidate list we open the review modal
// and the next keypress flows into handleDistillKey.
func (m *Model) handleDistillDone(msg distillDoneMsg) []tea.Cmd {
	m.distilling = false

	if msg.err != nil {
		return []tea.Cmd{m.appendHistory(styleErr.Render("  ! distill failed: " + msg.err.Error()))}
	}
	if len(msg.candidates) == 0 {
		return []tea.Cmd{m.appendHistory(styleMuted.Render("  · distill: the reasoner found nothing project-specific worth saving"))}
	}
	m.distillCandidates = msg.candidates
	m.distillIdx = 0
	m.distillSaved = 0
	m.distillDropped = 0
	m.distillEditing = false
	m.distillReviewOpen = true
	m.input.Blur()
	return []tea.Cmd{m.appendHistory(styleMuted.Render(fmt.Sprintf("  · distill: %d candidate(s) — review with [y] save  [n] drop  [e] edit  [q] quit", len(msg.candidates))))}
}

// handleDistillKey is the review-modal key handler. Two sub-modes:
//
//   - distillEditing == false: y/n/e/q decide the current candidate.
//   - distillEditing == true: the main input area is the content
//     editor; Enter commits the edit (saves the candidate), Esc
//     cancels back to the y/n/e/q prompt.
//
// Ctrl+C in either sub-mode aborts the review entirely (same exit as
// 'q'). The review state persists across keys until exhausted; nothing
// re-enters this handler once distillReviewOpen flips back to false.
func (m Model) handleDistillKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.distillReviewOpen {
		return m, nil
	}

	if m.distillEditing {
		return m.handleDistillEditKey(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		return m.exitDistillReview(true)
	case tea.KeyEsc:
		// Esc behaves the same as 'q' — abort. Less surprising than
		// "Esc does nothing here" in a modal context.
		return m.exitDistillReview(true)
	case tea.KeyEnter:
		// Enter on the review prompt with no key first is ambiguous —
		// reject it so the user must explicitly say y/n/e/q.
		return m, nil
	}

	if len(msg.Runes) != 1 {
		return m, nil
	}
	switch strings.ToLower(string(msg.Runes)) {
	case "y":
		return m.distillAcceptCurrent()
	case "n":
		return m.distillDropCurrent()
	case "e":
		return m.enterDistillEdit()
	case "q":
		return m.exitDistillReview(true)
	}
	return m, nil
}

// distillAcceptCurrent saves the current candidate into M, advances the
// pointer, and closes the modal if that was the last one. Project.Add
// failures are surfaced to scrollback but don't kill the review — the
// user can still answer y/n/e for the remaining candidates.
func (m Model) distillAcceptCurrent() (tea.Model, tea.Cmd) {
	if m.distillIdx >= len(m.distillCandidates) {
		return m.exitDistillReview(false)
	}
	cand := m.distillCandidates[m.distillIdx]
	var cmds []tea.Cmd
	if err := saveDistillCandidate(m.opts.MemoryProject, cand); err != nil {
		// Commit and continue. The candidate is NOT counted as saved.
		cmds = append(cmds, m.appendHistory(styleErr.Render(fmt.Sprintf("  ! save %q failed: %v", cand.Name, err))))
		m.distillDropped++
	} else {
		m.distillSaved++
	}
	m.distillIdx++
	if m.distillIdx >= len(m.distillCandidates) {
		m2, cmd := m.exitDistillReview(false)
		cmds = append(cmds, cmd)
		return m2, tea.Batch(cmds...)
	}
	return m, tea.Batch(cmds...)
}

func (m Model) distillDropCurrent() (tea.Model, tea.Cmd) {
	if m.distillIdx >= len(m.distillCandidates) {
		return m.exitDistillReview(false)
	}
	m.distillDropped++
	m.distillIdx++
	if m.distillIdx >= len(m.distillCandidates) {
		return m.exitDistillReview(false)
	}
	return m, nil
}

// enterDistillEdit switches to edit-mode: the main input area becomes
// a multi-line content editor prefilled with the candidate's current
// Content. Enter commits, Esc cancels back to the review prompt with
// no changes.
func (m Model) enterDistillEdit() (tea.Model, tea.Cmd) {
	if m.distillIdx >= len(m.distillCandidates) {
		return m.exitDistillReview(false)
	}
	m.distillEditing = true
	m.input.SetValue(m.distillCandidates[m.distillIdx].Content)
	// Five rows is enough to comfortably edit a 300-word content body
	// without scrolling; reduces to 3 (default) when edit-mode exits.
	m.input.SetHeight(5)
	m.input.Focus()
	return m, nil
}

// handleDistillEditKey is the edit-mode key handler. Enter commits the
// edited content into the candidate and treats it as accepted (saves
// to M, advances). Esc cancels the edit and returns to y/n/e/q without
// changing anything.
func (m Model) handleDistillEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		// Abort the whole review including the in-flight edit.
		return m.exitDistillReview(true)
	case tea.KeyEsc:
		m.distillEditing = false
		m.input.Reset()
		m.input.SetHeight(3)
		m.input.Blur()
		return m, nil
	case tea.KeyEnter:
		// Commit: replace the candidate's Content with the edit and
		// route through the same accept path so Project.Add fires and
		// counters update consistently.
		edited := strings.TrimSpace(m.input.Value())
		if edited == "" {
			// Empty content after edit — treat as drop. Saving an entry
			// with empty content would be useless and Add() would warn.
			m.distillEditing = false
			m.input.Reset()
			m.input.SetHeight(3)
			m.input.Blur()
			return m.distillDropCurrent()
		}
		m.distillCandidates[m.distillIdx].Content = edited
		m.distillEditing = false
		m.input.Reset()
		m.input.SetHeight(3)
		m.input.Blur()
		return m.distillAcceptCurrent()
	}
	// Forward everything else to the textarea so typing / arrow keys /
	// backspace work normally.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// exitDistillReview tears down the modal and prints a one-line summary
// to scrollback. aborted=true means the user hit q / Ctrl+C / Esc; the
// summary suffix reflects that so the user knows remaining candidates
// were skipped on purpose.
func (m Model) exitDistillReview(aborted bool) (tea.Model, tea.Cmd) {
	remaining := len(m.distillCandidates) - m.distillIdx
	m.distillReviewOpen = false
	m.distillEditing = false
	m.distillCandidates = nil
	m.distillIdx = 0
	m.input.SetHeight(3)
	m.input.Focus()

	var suffix string
	if aborted && remaining > 0 {
		suffix = fmt.Sprintf(", %d skipped", remaining)
	}
	return m, (&m).appendHistory(styleMuted.Render(fmt.Sprintf(
		"  · distill review done: %d saved, %d dropped%s",
		m.distillSaved, m.distillDropped, suffix)))
}

// saveDistillCandidate copies an approved candidate into M. The
// memory.Entry fields it doesn't touch (timestamps, recall counts)
// are filled in by Project.Add.
func saveDistillCandidate(p *memory.Project, c memory.Candidate) error {
	if p == nil {
		return fmt.Errorf("memory project unavailable")
	}
	return p.Add(memory.Entry{
		Name:    c.Name,
		Tagline: c.Tagline,
		Content: c.Content,
		Tags:    c.Tags,
	})
}

// renderDistillReview draws the modal shown while the user is reviewing
// distillation candidates. Two variants — y/n/e/q prompt and the
// in-place content editor (when distillEditing is true). Both share the
// "candidate N/M: name — tagline" header so the user always knows where
// they are in the queue.
func (m Model) renderDistillReview() string {
	if !m.distillReviewOpen || m.distillIdx >= len(m.distillCandidates) {
		return ""
	}
	cand := m.distillCandidates[m.distillIdx]

	var sb strings.Builder
	header := fmt.Sprintf("⚡ distill candidate %d/%d: %s",
		m.distillIdx+1, len(m.distillCandidates), cand.Name)
	sb.WriteString(styleApprovalHeader.Render(header))
	sb.WriteString("\n")

	if cand.Tagline != "" {
		sb.WriteString(styleMuted.Render("  " + cand.Tagline))
		sb.WriteString("\n")
	}

	if m.distillEditing {
		// Edit mode: textarea is rendered up top via m.input.View()
		// (the View() pass already draws it). Show only the footer
		// hint and the live preview boundary so it's clear where
		// content ends.
		sb.WriteString("\n")
		sb.WriteString(styleMuted.Render("  Editing content — [Enter] save  [Esc] cancel"))
		sb.WriteString("\n")
		return sb.String()
	}

	// Standard review prompt: show the full content body indented so
	// the user can decide informed.
	sb.WriteString("\n")
	for _, line := range strings.Split(strings.TrimRight(cand.Content, "\n"), "\n") {
		sb.WriteString("    ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if len(cand.Tags) > 0 {
		sb.WriteString(styleMuted.Render("  tags: " + strings.Join(cand.Tags, ", ")))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(styleMuted.Render("  [y] save  [n] drop  [e] edit  [q] quit review"))
	sb.WriteString("\n")
	return sb.String()
}
