package tui

// The skill segment of /distill (feature-skill-distill M-SD1). The M
// review flow lives in distill.go; this file is its skill-draft twin.
// Same modal shape, different accept semantics:
//
//   - M candidate 'y'  → Project.Add (distill.go)
//   - skill candidate 'y' → skillmgr.DraftStore.WriteDraft — a DRAFT
//     under ~/.seek/skills/.drafts/, invisible to every session. The
//     distillation pipeline NEVER installs; installing is the human's
//     explicit `seek skill install <dir>` (the review gate, PRD §6).
//
// Scheduling: cmdDistill fires both reasoner passes in parallel. Whose
// result arrives first opens its modal; the other's result waits in
// pendingSkillCandidates / pendingMCandidates until the open review
// closes, then chains. In practice M returns first and the user
// reviews ⚡ entries, then 🛠 entries — the PRD's "skill candidates
// are reviewed after the M candidates" sequencing without a serial
// second round-trip.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/whyiyhw/seek/internal/skilldistill"
	"github.com/whyiyhw/seek/internal/skillmgr"
)

// skillDistillDoneMsg fires when the skill-extraction reasoner pass
// returns. rejected carries the funnel's drops (shape validation,
// evidence verification, L2 mechanical dedup) with reasons — printed
// as muted lines so the funnel stays observable.
type skillDistillDoneMsg struct {
	candidates []skilldistill.SkillCandidate
	rejected   []skilldistill.Rejection
	err        error
}

// handleSkillDistillDone is the entry point for the async skill pass.
// Err → one error line; gate empty / funnel rejected everything →
// one muted line (the funnel working is not a failure); survivors →
// queue for review, chained after any still-open M review.
func (m *Model) handleSkillDistillDone(msg skillDistillDoneMsg) []tea.Cmd {
	m.skillDistilling = false

	if msg.err != nil {
		return []tea.Cmd{m.appendHistory(styleErr.Render("  ! skill distill failed: " + msg.err.Error()))}
	}
	for _, r := range msg.rejected {
		m.appendHistory(styleMuted.Render("  · skill candidate rejected — " + r.String()))
	}
	if len(msg.candidates) == 0 {
		if len(msg.rejected) == 0 {
			return []tea.Cmd{m.appendHistory(styleMuted.Render("  · skill distill: no reusable procedure found in this session"))}
		}
		return nil
	}

	if m.distillReviewOpen {
		// M review in progress — hold the skill candidates; the M
		// review's exit chains into opening this one.
		m.pendingSkill = append(m.pendingSkill, msg.candidates...)
		return []tea.Cmd{m.appendHistory(styleMuted.Render(fmt.Sprintf(
			"  · skill distill: %d draft candidate(s) queued behind the memory review", len(msg.candidates))))}
	}
	return m.openSkillReview(msg.candidates)
}

// openSkillReview starts (or chains into) the skill review modal.
func (m *Model) openSkillReview(candidates []skilldistill.SkillCandidate) []tea.Cmd {
	m.skillCandidates = candidates
	m.skillIdx = 0
	m.skillSaved = 0
	m.skillDropped = 0
	m.skillReviewOpen = true
	m.input.Blur()
	return []tea.Cmd{m.appendHistory(styleMuted.Render(fmt.Sprintf(
		"  · skill distill: %d draft candidate(s) — [y] save draft  [n] drop  [q] quit", len(candidates))))}
}

// handleSkillDistillKey is the skill review modal's handler. y/n/q
// only — no edit mode in the first delivery: a draft is an ordinary
// file, editing it post-save (`seek skill status` / any editor) is the
// natural place, and the modal's job here is the accept/drop decision.
func (m Model) handleSkillDistillKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if !m.skillReviewOpen {
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c", "esc":
		return m.exitSkillReview(true)
	}
	if len(msg.Text) != 1 {
		return m, nil
	}
	switch strings.ToLower(msg.Text) {
	case "y":
		return m.skillAcceptCurrent()
	case "n":
		return m.skillDropCurrent()
	case "q":
		return m.exitSkillReview(true)
	}
	return m, nil
}

// skillAcceptCurrent writes the current candidate as a draft via
// DraftStore. Render failures (a candidate whose SKILL.md won't
// parse) surface as an error line and count as dropped — the review
// continues so one bad candidate can't strand the rest.
func (m Model) skillAcceptCurrent() (tea.Model, tea.Cmd) {
	if m.skillIdx >= len(m.skillCandidates) {
		return m.exitSkillReview(false)
	}
	cand := m.skillCandidates[m.skillIdx]

	dir, err := saveSkillDraft(m, cand)
	var cmds []tea.Cmd
	if err != nil {
		cmds = append(cmds, m.appendHistory(styleErr.Render(fmt.Sprintf("  ! draft %q failed: %v", cand.Name, err))))
		m.skillDropped++
	} else {
		m.skillSaved++
		cmds = append(cmds, m.appendHistory(styleMuted.Render(fmt.Sprintf(
			"  · skill draft saved: %s\n    install (human gate): seek skill install %s [--project]", dir, dir))))
	}
	m.skillIdx++
	if m.skillIdx >= len(m.skillCandidates) {
		m2, cmd := m.exitSkillReview(false)
		cmds = append(cmds, cmd)
		return m2, tea.Batch(cmds...)
	}
	return m, tea.Batch(cmds...)
}

func (m Model) skillDropCurrent() (tea.Model, tea.Cmd) {
	if m.skillIdx >= len(m.skillCandidates) {
		return m.exitSkillReview(false)
	}
	m.skillDropped++
	m.skillIdx++
	if m.skillIdx >= len(m.skillCandidates) {
		return m.exitSkillReview(false)
	}
	return m, nil
}

// exitSkillReview tears down the skill modal and prints the summary.
// abort=true (q / Esc / Ctrl+C) skips the remaining candidates on
// purpose — the suffix says so, mirroring the M review's exit.
func (m Model) exitSkillReview(aborted bool) (tea.Model, tea.Cmd) {
	remaining := len(m.skillCandidates) - m.skillIdx
	m.skillReviewOpen = false
	m.skillCandidates = nil
	m.skillIdx = 0
	m.input.Focus()

	var suffix string
	if aborted && remaining > 0 {
		suffix = fmt.Sprintf(", %d skipped", remaining)
	}
	cmds := []tea.Cmd{m.appendHistory(styleMuted.Render(fmt.Sprintf(
		"  · skill distill review done: %d draft(s) saved, %d dropped%s",
		m.skillSaved, m.skillDropped, suffix)))}

	// Chain: if the M pass finished while the skill review was open,
	// its candidates were held in distill.go's pending queue — open
	// them now. (cmdDistill normally completes M first, so this path
	// only triggers on an unusual round-trip ordering.)
	if len(m.pendingMCandidates) > 0 {
		cands := m.pendingMCandidates
		m.pendingMCandidates = nil
		m2, cmd := m.openMReviewFromPending(cands)
		cmds = append(cmds, cmd)
		return m2, tea.Batch(cmds...)
	}
	return m, tea.Batch(cmds...)
}

// openMReviewFromPending opens the M review with candidates that
// arrived during a skill review. Kept tiny (and on Model value
// semantics like every other handler) — it reuses handleDistillDone's
// opening logic minus the rejection reporting, which already printed
// when the M pass landed.
func (m Model) openMReviewFromPending(cands memoryCandidates) (tea.Model, tea.Cmd) {
	m.distillCandidates = cands
	m.distillIdx = 0
	m.distillSaved = 0
	m.distillDropped = 0
	m.distillEditing = false
	m.distillReviewOpen = true
	m.input.Blur()
	return m, m.appendHistory(styleMuted.Render(fmt.Sprintf(
		"  · distill: %d candidate(s) — review with [y] save  [n] drop  [e] edit  [q] quit", len(cands))))
}

// saveSkillDraft renders the candidate and writes it via DraftStore,
// with the provenance sidecar: manual path ⇒ status "reviewable"
// (the user IS the recurrence evidence), single source, L2 trace from
// the funnel (grey-zone warnings survive into the sidecar so a later
// reviewer sees why it nearly died).
func saveSkillDraft(m Model, cand skilldistill.SkillCandidate) (string, error) {
	md, err := skilldistill.RenderSKILLMD(cand)
	if err != nil {
		return "", err
	}

	genModel := ""
	if m.opts.SkillDistiller != nil {
		genModel = m.opts.SkillDistiller.Model
	}
	if genModel == "" {
		genModel = skilldistill.DefaultModel
	}
	side := skillmgr.DraftSidecar{
		Status:         skillmgr.DraftStatusReviewable,
		Recurrence:     1,
		SuggestedScope: "project", // manual path: one session, one project (PRD §5.4)
		Sources:        []skillmgr.DraftSource{currentDraftSource(m)},
		Evidence:       cand.Evidence,
		Generator: &skillmgr.DraftGenerator{
			Model:     genModel,
			PromptRev: skilldistill.PromptRev,
		},
	}
	verdict := "PASS"
	if cand.L2Note != "" {
		verdict = "GREY"
	}
	side.FilterTrace = []skillmgr.FilterTraceEntry{{
		Stage:   "L2",
		Verdict: verdict,
		Reason:  cand.L2Note,
	}}

	store := &skillmgr.DraftStore{UserDir: m.opts.DraftsUserDir}
	return store.WriteDraft(md, side)
}

// currentDraftSource records which session produced the draft. Session
// ID and project ID come from the TUI's own handles; missing handles
// (tests, --no-save) degrade to empty strings rather than blocking the
// write — provenance is best-effort, the draft itself is the point.
func currentDraftSource(m Model) skillmgr.DraftSource {
	src := skillmgr.DraftSource{At: time.Now().UTC().Format(time.RFC3339)}
	if m.opts.Session != nil {
		src.SessionID = m.opts.Session.ID
	}
	if m.opts.MemoryProject != nil {
		src.ProjectID = m.opts.MemoryProject.ID
	}
	return src
}

// renderSkillReview draws the skill-candidate modal. First screen is
// description (the manifest trigger — the highest-leverage field,
// PRD §1.3) + evidence (verbatim transcript excerpts — the
// anti-hallucination check the human runs by eye) + body below.
func (m Model) renderSkillReview() string {
	if !m.skillReviewOpen || m.skillIdx >= len(m.skillCandidates) {
		return ""
	}
	cand := m.skillCandidates[m.skillIdx]

	var sb strings.Builder
	header := fmt.Sprintf("🛠 skill candidate %d/%d: %s",
		m.skillIdx+1, len(m.skillCandidates), cand.Name)
	sb.WriteString(styleApprovalHeader.Render(header))
	sb.WriteString("\n")

	for line := range strings.SplitSeq(strings.TrimRight(cand.Description, "\n"), "\n") {
		sb.WriteString(styleMuted.Render("  " + line))
		sb.WriteString("\n")
	}
	if cand.L2Note != "" {
		sb.WriteString(styleErr.Render("  ⚠ " + cand.L2Note))
		sb.WriteString("\n")
	}
	if len(cand.Evidence) > 0 {
		sb.WriteString("\n")
		sb.WriteString(styleMuted.Render("  evidence (verbatim from this session):"))
		sb.WriteString("\n")
		for _, ev := range cand.Evidence {
			for line := range strings.SplitSeq(strings.TrimRight(ev, "\n"), "\n") {
				sb.WriteString("    | ")
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}
	}
	sb.WriteString("\n")
	for line := range strings.SplitSeq(strings.TrimRight(cand.Body, "\n"), "\n") {
		sb.WriteString("    ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(styleMuted.Render("  [y] save draft  [n] drop  [q] quit review (install stays manual: seek skill install <dir>)"))
	sb.WriteString("\n")
	return sb.String()
}

// startSkillDistillCmd runs the skill-extraction round-trip as a
// tea.Cmd. Same 90s bound and parent-ctx propagation as the M pass —
// one reasoner round-trip, no tool loop.
func startSkillDistillCmd(m *Model) tea.Cmd {
	distiller := m.opts.SkillDistiller
	history := m.opts.Agent.Messages()
	parentCtx := m.opts.Ctx
	skills := m.opts.Skills
	manifest := ""
	if skills != nil {
		manifest = skills.Manifest()
	}
	var mIndex []skilldistill.MEntry
	if m.opts.MemoryProject != nil {
		for _, e := range m.opts.MemoryProject.Index() {
			mIndex = append(mIndex, skilldistill.MEntry{Name: e.Name, Tagline: e.Tagline})
		}
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parentCtx, 90*time.Second)
		defer cancel()
		kept, rejected, err := distiller.Extract(ctx, history, manifest, mIndex, skills)
		return skillDistillDoneMsg{candidates: kept, rejected: rejected, err: err}
	}
}
