package subagent

// Resume 回灌 (feature-orchestration PRD §4.1 D3, milestone M-O1.3):
// when a parent session is -resume'd, background children that reached
// a terminal state in a PRIOR process — without their completion
// notice ever being ingested into the parent transcript — get that
// notice rehydrated from disk and delivered on the first turn.
//
// Everything is rebuilt from the event source, zero in-memory
// carry-over (the P1-b event-sourcing posture): the fold of
// subagents.jsonl selects the candidates, the child's transcript
// tail supplies the summary, and delivery rides the SAME surfaces as
// live notices — the Manager's inbox + the Notifier Prepend + the
// `notified` consumption marker. No new delivery path exists, so the
// live and resumed paths cannot drift apart.
//
// Consumption marking is what makes the whole thing idempotent: the
// Notifier appends a `notified` event per delivered notice, the fold
// reads it back as Notified=true, and a repeated resume folds past
// the same row without re-enqueuing it (验收: 重复 resume 不重注).

import (
	"sort"
)

// recoverNotices folds the project index and returns the pending
// notice queue for parentSid's background children that reached a
// terminal state without a `notified` marker — the D3 resume-backfill
// selection, as a pure reader over the event source + child
// transcripts (no Manager, no writes; the Manager wrapper below owns
// enqueueing).
//
// Selection rules (PRD D3):
//
//   - Background children ONLY (s.Background). Sync results were
//     returned as the calling turn's tool result — there is no
//     undelivered notice to rehydrate.
//   - This parent session ONLY (s.ParentSid == parentSid). A fresh
//     session in the same project never inherits another session's
//     queue; -resume keeps the session id, so the resumed parent
//     finds exactly its own children.
//   - Terminal + un-notified:
//     completed → summary notice rebuilt from the child transcript's
//     last assistant message (FormatBackgroundResumed); if the
//     transcript is gone/unreadable the run degrades to a lost note
//     (记录在、transcript 失联).
//     failed / orphaned → one-line status note, never a summary
//     (没有可信 summary): failed reuses the live D2 failure format
//     verbatim (same reason enum), orphaned gets the lost note.
//     killed / promoted are skipped — user-visible terminal actions
//     with no delivery debt.
//
// Notices come back in terminal-time FIFO (EndedAt ascending), the
// D2 multi-completion ordering, so the first turn after resume reads
// oldest-finished-first. Caller enqueues them onto the inbox in this
// order.
func recoverNotices(projectAbsPath, parentSid string) ([]BackgroundNotice, error) {
	folded, err := ListProject(projectAbsPath, ListFilter{ParentSid: parentSid})
	if err != nil {
		return nil, err
	}
	// Select first, order second: terminal-time FIFO (EndedAt
	// ascending, the D2 multi-completion ordering — oldest finished
	// reads first on the resumed turn). EndedAt is always set for
	// terminal states (the fold takes it from the terminal event's
	// TS); stable sort keeps fold order (StartedAt desc) for equal
	// timestamps so the result is deterministic on hand-crafted
	// fixtures too.
	var picked []Subagent
	for _, s := range folded {
		if !s.Background || s.Notified {
			continue
		}
		switch s.Status {
		case StatusCompleted, StatusFailed, StatusOrphaned:
			picked = append(picked, s)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool {
		return picked[i].EndedAt.Before(picked[j].EndedAt)
	})
	var out []BackgroundNotice
	for _, s := range picked {
		switch s.Status {
		case StatusCompleted:
			summary, rerr := LastAssistant(projectAbsPath, s.ParentSid, s.SubSid)
			if rerr != nil || summary == "" {
				// Real I/O errors degrade the same as a missing file:
				// the record is there, the summary is not recoverable.
				// (LastAssistant's read is already tolerance-first —
				// a hard error here means the path itself is broken.)
				out = append(out, BackgroundNotice{
					SubSid:      s.SubSid,
					Description: s.Description,
					Status:      s.Status,
					Summary:     FormatBackgroundLost(s.SubSid, s.Status, s.Description),
				})
				continue
			}
			out = append(out, BackgroundNotice{
				SubSid:      s.SubSid,
				Description: s.Description,
				Status:      s.Status,
				Summary:     FormatBackgroundResumed(s.SubSid, s.ResumedFrom, summary, s.Tokens),
			})
		case StatusFailed:
			// Same wire format the live D2 delivery would have shown —
			// a resumed failure notice is indistinguishable from a
			// live one, which is the point.
			out = append(out, BackgroundNotice{
				SubSid:      s.SubSid,
				Description: s.Description,
				Status:      s.Status,
				Summary: FormatBackgroundFailed(s.SubSid, s.Reason,
					"terminal state found on resume; failed runs backfill status only"),
			})
		case StatusOrphaned:
			out = append(out, BackgroundNotice{
				SubSid:      s.SubSid,
				Description: s.Description,
				Status:      s.Status,
				Summary:     FormatBackgroundLost(s.SubSid, s.Status, s.Description),
			})
		}
	}
	return out, nil
}

// RecoverNotices rehydrates the resume-backfill queue onto the
// Manager's notice inbox: fold the index for THIS parent session's
// background children that are terminal-but-un-notified, enqueue
// their notices (summary or status note per D3), and let the normal
// delivery path take over — the Notifier's next OnPrePrompt (the
// first Prompt after resume) prepends them as <context> messages and
// appends the `notified` marker per notice.
//
// Call ONCE at resume assembly (cmd/seek wires it right after
// OrphanRecover, so crash residue is folded to orphaned BEFORE this
// scan — orphaned rows then take the status-note path). Calling it
// on a fresh session is a harmless no-op: the ParentSid filter finds
// nothing. Repeated calls after delivery are equally no-ops — the
// `notified` events the fold reads back are the idempotency marker.
//
// Returns how many notices were enqueued (0 = nothing pending) and
// the first index-read error, if any. A non-zero count surfaces in
// cmd/seek as a stderr hint ("N background notices pending — they
// will be delivered on your next message"); the TUI's bg-done badge
// (M-O1.2) shows the pending state in the meantime.
func (m *Manager) RecoverNotices() (int, error) {
	parentSid := m.opts.ParentSidFn()
	if parentSid == "" {
		// Same posture as begin step 5: no active parent session means
		// there is nothing to recover for (and nothing to crash over).
		return 0, nil
	}
	notices, err := recoverNotices(m.opts.ProjectAbsPath, parentSid)
	if err != nil {
		return 0, err
	}
	for _, nt := range notices {
		m.enqueueNotice(nt)
	}
	return len(notices), nil
}
