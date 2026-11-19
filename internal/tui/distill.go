package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skilldistill"
	"github.com/whyiyhw/seek/internal/skillmgr"
)

// memoryCandidates keeps the pending-hold plumbing readable — the M
// review's candidate slice type, held while the skill review modal
// owns the keyboard (see pendingMCandidates).
type memoryCandidates = []memory.Candidate

// handleDistillDone is the entry point for the async reasoner result.
// On error or no candidates we print a single muted line to scrollback
// and stay idle. On a non-empty candidate list we open the review modal
// and the next keypress flows into handleDistillKey.
//
// Skill-segment interleave: /distill also runs a skill pass in
// parallel (distill_skill.go). If that review modal is somehow still
// open when the M result lands (M round-trip slower than skill — rare
// but possible), the M candidates are held in pendingMCandidates and
// opened when the skill review closes, mirroring how the skill pass
// waits behind an open M review.
func (m *Model) handleDistillDone(msg distillDoneMsg) []tea.Cmd {
	m.distilling = false

	if msg.err != nil {
		return []tea.Cmd{m.appendHistory(styleErr.Render("  ! distill failed: " + msg.err.Error()))}
	}
	if len(msg.candidates) == 0 {
		return []tea.Cmd{m.appendHistory(styleMuted.Render("  · distill: the reasoner found nothing project-specific worth saving"))}
	}
	if m.skillReviewOpen {
		m.pendingMCandidates = msg.candidates
		return []tea.Cmd{m.appendHistory(styleMuted.Render(fmt.Sprintf(
			"  · distill: %d memory candidate(s) queued behind the skill review", len(msg.candidates))))}
	}
	m.distillCandidates = msg.candidates
	m.distillIdx = 0
	m.distillSaved = 0
	m.distillDropped = 0
	m.distillSkilled = 0
	m.distillEditing = false
	// Pay-once cache is per-review: idx values recycle across reviews
	// (every review restarts at 0), so a stale entry would hand the
	// PREVIOUS review's conversion to this review's candidate 0.
	m.distillConvertCache = nil
	m.distillReviewOpen = true
	m.input.Blur()
	return []tea.Cmd{m.appendHistory(styleMuted.Render(fmt.Sprintf("  · distill: %d candidate(s) — review with [y] save  [n] drop  [e] edit  [s] → skill  [q] quit", len(msg.candidates))))}
}

// handleDistillKey is the review-modal key handler. Four sub-modes:
//
//   - distillEditing == true: the main input area is the content
//     editor; Enter commits the edit (saves the candidate), Esc
//     cancels back to the y/n/e/q prompt.
//   - distillConverting == true: an `s` reasoner round-trip is in
//     flight. Keys are inert except the aborts (q/Esc/Ctrl+C) and
//     `n`, which retreats to the candidate prompt and orphans the
//     in-flight call — its late result msg is dropped by the stale
//     guard (generation counter) in handleDistillConvertDone.
//   - distillConverted != nil: the converted-skill preview. y installs
//     to <project>/.seek/skills (WriteProjectSkill), n backs out to
//     the prompt (y/n/e/s all usable again), q quits.
//   - otherwise: y/n/e/s/q decide the current candidate.
//
// Ctrl+C in every sub-mode aborts the review entirely (same exit as
// 'q'). The review state persists across keys until exhausted; nothing
// re-enters this handler once distillReviewOpen flips back to false.
func (m Model) handleDistillKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if !m.distillReviewOpen {
		return m, nil
	}

	if m.distillEditing {
		return m.handleDistillEditKey(msg)
	}

	if m.distillConverting {
		switch msg.String() {
		case "ctrl+c", "esc":
			return m.exitDistillReview(true)
		}
		if len(msg.Text) == 1 {
			switch strings.ToLower(msg.Text) {
			case "q":
				return m.exitDistillReview(true)
			case "n":
				// §11: `n` retreats from the conversion too — back to
				// the candidate prompt, y/n/e/s live again. The
				// in-flight call is orphaned, not cancelled: bump the
				// generation so its late result stays stale even after
				// an immediate `s` refire on the same idx (idx +
				// converting alone would re-admit it).
				m.distillConverting = false
				m.distillConvertGen++
				return m, nil
			}
		}
		return m, nil
	}

	if m.distillConverted != nil {
		switch msg.String() {
		case "ctrl+c", "esc":
			// Esc keeps its modal-wide meaning: abort the review.
			return m.exitDistillReview(true)
		}
		if len(msg.Text) != 1 {
			return m, nil
		}
		switch strings.ToLower(msg.Text) {
		case "y":
			return m.distillInstallConverted()
		case "n":
			// Back out of the preview — the candidate is still
			// undecided; y (save to M) / e / s all remain open (a new
			// s replays this cached conversion; it does not re-pay).
			m.distillConverted = nil
			m.distillConvertNote = ""
			return m, nil
		case "q":
			return m.exitDistillReview(true)
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		return m.exitDistillReview(true)
	case "esc":
		// Esc behaves the same as 'q' — abort. Less surprising than
		// "Esc does nothing here" in a modal context.
		return m.exitDistillReview(true)
	case "enter":
		// Enter on the review prompt with no key first is ambiguous —
		// reject it so the user must explicitly say y/n/e/q.
		return m, nil
	}

	if len(msg.Text) != 1 {
		return m, nil
	}
	switch strings.ToLower(msg.Text) {
	case "y":
		return m.distillAcceptCurrent()
	case "n":
		return m.distillDropCurrent()
	case "e":
		return m.enterDistillEdit()
	case "s":
		return m.distillStartConvert()
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
func (m Model) handleDistillEditKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// Abort the whole review including the in-flight edit.
		return m.exitDistillReview(true)
	case "esc":
		m.distillEditing = false
		m.input.Reset()
		m.input.SetHeight(3)
		m.input.Blur()
		return m, nil
	case "enter":
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

// ---------- `s`: convert-to-skill fast lane (bridge §3.1, M-B2) ----------

// distillConvertDoneMsg carries the async ConvertToSkill result. idx is
// the candidate index the call was fired for; gen is the conversion
// generation it was fired under — the stale guard in the handler drops
// results whose review closed, advanced past them, or whose generation
// was orphaned by an `n` retreat (same-idx refire would otherwise
// re-admit the old result).
type distillConvertDoneMsg struct {
	idx  int
	gen  int
	cand skilldistill.SkillCandidate
	l2   skilldistill.L2Result
	err  error
}

// distillConvertEntry is the pay-once cache payload (§3.2, 2026-11-19
// 拍板「按候选 idx 缓存」): a completed (err==nil) conversion outcome —
// the converted candidate plus its L2 verdict — replayable by a later
// `s` on the same candidate without a second reasoner round-trip.
// Errors never enter the cache: their messages promise "retry with s",
// and a retry must be able to re-pay.
type distillConvertEntry struct {
	cand skilldistill.SkillCandidate
	l2   skilldistill.L2Result
}

// distillStartConvert is the `s` entry: cross-layer same-name refusal
// first (§2.3 — any of project/user/builtin owning the name means
// "edit the existing skill", never a second copy; the loader's Set is
// already the merged cascade), then the pay-once cache (a completed
// conversion for this idx replays instead of re-paying), and only then
// flip to the converting state and fire one reasoner round-trip. Every
// refusal is a line + stay on the prompt: y/n/e keep working.
func (m Model) distillStartConvert() (tea.Model, tea.Cmd) {
	if m.distillIdx >= len(m.distillCandidates) {
		return m.exitDistillReview(false)
	}
	cand := m.distillCandidates[m.distillIdx]

	if m.opts.SkillDistiller == nil {
		return m, m.appendHistory(styleErr.Render(
			"  ! skill conversion unavailable (no skill distiller) — [y]/[n]/[e] still work"))
	}
	if m.opts.Skills != nil {
		if existing := m.opts.Skills.Get(cand.Name); existing != nil {
			return m, m.appendHistory(styleErr.Render(fmt.Sprintf(
				"  ! skill %q already installed (source %s) — edit the existing skill (`seek skill update` / edit the file) instead of converting this candidate", cand.Name, existing.Source)))
		}
	}
	if entry, ok := m.distillConvertCache[m.distillIdx]; ok {
		cmds := (&m).routeConvertOutcome(m.distillIdx, entry.cand, entry.l2, false)
		return m, tea.Batch(cmds...)
	}

	m.distillConverting = true
	m.distillConvertIdx = m.distillIdx
	m.distillConvertGen++
	return m, startDistillConvertCmd(&m, m.distillIdx, m.distillConvertGen)
}

// startDistillConvertCmd runs the conversion round-trip as a tea.Cmd —
// same 90s bound and parent-ctx propagation as the two distill passes
// (§3.2: one reasoner call, paid only when `s` is pressed and the idx
// cache misses).
func startDistillConvertCmd(m *Model, idx, gen int) tea.Cmd {
	distiller := m.opts.SkillDistiller
	mc := m.distillCandidates[idx] // copied by value below
	skills := m.opts.Skills
	var mIndex []skilldistill.MEntry
	if m.opts.MemoryProject != nil {
		for _, e := range m.opts.MemoryProject.Index() {
			mIndex = append(mIndex, skilldistill.MEntry{Name: e.Name, Tagline: e.Tagline})
		}
	}
	parentCtx := m.opts.Ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
		defer cancel()
		cand := skilldistill.MCandidate{Name: mc.Name, Tagline: mc.Tagline, Content: mc.Content, Tags: mc.Tags}
		sc, l2, err := distiller.ConvertToSkill(ctx, cand, skills, mIndex)
		return distillConvertDoneMsg{idx: idx, gen: gen, cand: sc, l2: l2, err: err}
	}
}

// handleDistillConvertDone routes the conversion result. err prints one
// line and returns to the candidate prompt (degrade, never terminate —
// §3.1); anything else hands over to routeConvertOutcome. The stale
// guard is load-bearing: a result whose review closed (q mid-convert),
// whose candidate index no longer matches, or whose generation was
// orphaned by an `n` retreat would otherwise splice into a different
// turn — idx alone is NOT enough because a fresh review restarts at 0,
// and idx + converting alone are not enough because `n` + a same-idx
// refire re-arms both.
func (m *Model) handleDistillConvertDone(msg distillConvertDoneMsg) []tea.Cmd {
	if !m.distillReviewOpen || !m.distillConverting || msg.idx != m.distillConvertIdx || msg.gen != m.distillConvertGen {
		return nil
	}
	m.distillConverting = false

	if msg.err != nil {
		return []tea.Cmd{m.appendHistory(styleErr.Render("  ! convert to skill failed: " + msg.err.Error()))}
	}
	return m.routeConvertOutcome(msg.idx, msg.cand, msg.l2, true)
}

// routeConvertOutcome is the single router a live result and a cache
// replay share: L2Reject prints its line and stays on the prompt,
// PASS/GREY open the preview with the GREY warning riding along.
// cache=true (live results only) also files the outcome into the
// per-idx pay-once cache — a completed round-trip is never paid twice
// for the same candidate (§3.2). Errors never reach this router, so
// they are never cached and "retry with s" stays honest.
func (m *Model) routeConvertOutcome(idx int, cand skilldistill.SkillCandidate, l2 skilldistill.L2Result, cache bool) []tea.Cmd {
	if cache {
		if m.distillConvertCache == nil {
			m.distillConvertCache = make(map[int]distillConvertEntry)
		}
		m.distillConvertCache[idx] = distillConvertEntry{cand: cand, l2: l2}
	}
	if l2.Verdict == skilldistill.L2Reject {
		return []tea.Cmd{m.appendHistory(styleErr.Render("  ! skill conversion rejected — " + l2.Reason))}
	}
	m.distillConverted = &cand
	m.distillConvertNote = ""
	if l2.Verdict == skilldistill.L2Grey {
		m.distillConvertNote = l2.Reason
	}
	return nil
}

// distillInstallConverted is the preview's y: render the installed-shape
// SKILL.md (keywords = the M candidate's tags) and WriteProjectSkill it
// into <project>/.seek/skills/. No .install.json, no M write — `s` is
// save-AS, not save-AND (§3.5). Render/write failures degrade exactly
// like distillAcceptCurrent's Project.Add failure: error line, count as
// dropped, advance — the review survives.
func (m Model) distillInstallConverted() (tea.Model, tea.Cmd) {
	if m.distillConverted == nil || m.distillIdx >= len(m.distillCandidates) {
		return m.exitDistillReview(false)
	}
	conv := *m.distillConverted
	tags := m.distillCandidates[m.distillIdx].Tags

	var cmds []tea.Cmd
	md, err := skilldistill.RenderProjectSkillMD(conv, tags)
	if err == nil {
		var dir string
		dir, err = skillmgr.WriteProjectSkill(skillmgr.WriteProjectOptions{
			ProjectDir: m.opts.CWD,
			Name:       conv.Name,
			MD:         md,
		})
		if err == nil {
			m.distillSkilled++
			cmds = append(cmds, m.appendHistory(styleMuted.Render(fmt.Sprintf(
				"  · skill installed: %s (takes effect next session)", dir))))
		}
	}
	if err != nil {
		cmds = append(cmds, m.appendHistory(styleErr.Render(fmt.Sprintf(
			"  ! install skill %q failed: %v", conv.Name, err))))
		m.distillDropped++
	}

	m.distillConverted = nil
	m.distillConvertNote = ""
	m.distillIdx++
	if m.distillIdx >= len(m.distillCandidates) {
		m2, cmd := m.exitDistillReview(false)
		cmds = append(cmds, cmd)
		return m2, tea.Batch(cmds...)
	}
	return m, tea.Batch(cmds...)
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
	// Also tear down any `s` state: an in-flight convert's late result
	// must find !distillConverting (stale guard), a preview must not
	// leak into the next review, and the pay-once cache dies with the
	// review that paid for it.
	m.distillConverting = false
	m.distillConvertIdx = 0
	m.distillConverted = nil
	m.distillConvertNote = ""
	m.distillConvertCache = nil
	m.input.SetHeight(3)
	m.input.Focus()

	var suffix string
	if aborted && remaining > 0 {
		suffix = fmt.Sprintf(", %d skipped", remaining)
	}
	summary := fmt.Sprintf("  · distill review done: %d saved, %d dropped", m.distillSaved, m.distillDropped)
	if m.distillSkilled > 0 {
		summary += fmt.Sprintf(", %d → skill", m.distillSkilled)
	}
	cmds := []tea.Cmd{(&m).appendHistory(styleMuted.Render(summary + suffix))}

	// Chain into the skill review if its candidates arrived while the
	// M review was open (the normal /distill flow: M lands first, the
	// skill pass lands mid-review and waits).
	if len(m.pendingSkill) > 0 {
		cands := m.pendingSkill
		m.pendingSkill = nil
		cmds = append(cmds, (&m).openSkillReview(cands)...)
	}
	return m, tea.Batch(cmds...)
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

// distillOverlapWarning is the ⚡ review's dual-store alert
// (feature-memory-skill-bridge §8.4): a memory candidate whose
// name+tagline+content overlaps an installed skill's description at or
// above the L2 reject threshold would store the same knowledge in two
// layers if `y`'d. Pure display — no gate; the y/n/e/s review stays the
// arbiter. Runs the same trigram-Jaccard (via skilldistill.NearestSkill)
// the novelty funnel itself uses, so the number behind this line is the
// one L2 would have computed.
func distillOverlapWarning(cand memory.Candidate, skills *skill.Set) string {
	if skills == nil || skills.Len() == 0 {
		return ""
	}
	name, sim := skilldistill.NearestSkill(cand.Name+" "+cand.Tagline, cand.Content, skills)
	if sim < skilldistill.SimilarityReject {
		return ""
	}
	return fmt.Sprintf("⚠ overlaps installed skill %q — pressing y will double-store; consider s / editing the skill", name)
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

	// Dual-store alert (§8.4) sits under the tagline so both review
	// variants — prompt and edit mode — keep it in view.
	if warn := distillOverlapWarning(cand, m.opts.Skills); warn != "" {
		sb.WriteString(styleErr.Render("  " + warn))
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

	// `s` conversion in flight: spinner line, no action footer — keys
	// are inert until the result lands (§3.1).
	if m.distillConverting {
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "%s %s\n", m.spinner.View(),
			styleMuted.Render("converting to skill …"))
		return sb.String()
	}

	// Converted-skill preview: the REWRITTEN description on top (the
	// field that will live in every future session's manifest — the
	// thing the human is really approving), the GREY warning if any,
	// then the body (= content, verbatim by design).
	if m.distillConverted != nil {
		conv := m.distillConverted
		sb.WriteString("\n")
		sb.WriteString(styleMuted.Render("  skill description (rewritten when-to-use):"))
		sb.WriteString("\n")
		for line := range strings.SplitSeq(strings.TrimRight(conv.Description, "\n"), "\n") {
			sb.WriteString("    ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		if m.distillConvertNote != "" {
			sb.WriteString(styleErr.Render("  ⚠ " + m.distillConvertNote))
			sb.WriteString("\n")
		}
		sb.WriteString(styleMuted.Render("  body (= content, verbatim):"))
		sb.WriteString("\n")
		for line := range strings.SplitSeq(strings.TrimRight(conv.Body, "\n"), "\n") {
			sb.WriteString("    ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(styleMuted.Render("  [y] install to project   [n] back   [q] quit review"))
		sb.WriteString("\n")
		return sb.String()
	}

	// Standard review prompt: show the full content body indented so
	// the user can decide informed.
	sb.WriteString("\n")
	for line := range strings.SplitSeq(strings.TrimRight(cand.Content, "\n"), "\n") {
		sb.WriteString("    ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if len(cand.Tags) > 0 {
		sb.WriteString(styleMuted.Render("  tags: " + strings.Join(cand.Tags, ", ")))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(styleMuted.Render("  [y] save  [n] drop  [e] edit  [s] → skill  [q] quit review"))
	sb.WriteString("\n")
	return sb.String()
}
