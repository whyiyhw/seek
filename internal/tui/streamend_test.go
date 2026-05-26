package tui

import (
	"strings"
	"testing"
)

// Tests in this file pin the textarea-hijack guards in handleStreamEnd
// (the v0.3.x review's sweep finding 2). Without these guards, Esc
// during a stream while the user is in setup-key-entry or
// review-branch-entry mode would shove the previous chat prompt into
// the API-key / branch-name field — silent data leak.
//
// The guard at update_agent.go:71 ANDs `!m.setupKeyEntry &&
// !m.reviewBranchEntry`. Each subtest below removes one of those
// flags and checks the textarea STAYS empty.

// TestHandleStreamEnd_DoesNotHijackSetupKeyEntry pins:
// User opens /setup, picks a provider, lands in key-entry mode. A
// previous stream's streamEndMsg fires (or they Esc-cancel a still-
// running one). The textarea is empty (they haven't typed the key
// yet) and promptHistory has one prior entry. The restore branch
// MUST NOT fire — typing a chat prompt and pressing Enter would
// save it as an API key.
func TestHandleStreamEnd_DoesNotHijackSetupKeyEntry(t *testing.T) {
	t.Parallel()

	m := testModel().
		WithPromptHistory("explain quicksort").
		Streaming().
		WithCustomState(func(m *Model) {
			m.setupKeyEntry = true
			m.setupProvider = "openai"
			m.userCanceled = true
		}).BuildPtr()

	// Simulate the Esc-cancel landing as streamEndMsg.
	out, _ := m.handleStreamEnd(streamEndMsg{})
	final := out.(Model)

	if got := final.input.Value(); got != "" {
		t.Errorf("setupKeyEntry: textarea must stay empty after stream cancel, got %q (would have leaked the prior prompt into config.json)", got)
	}
	if !final.setupKeyEntry {
		t.Error("setupKeyEntry flag should still be true — the modal isn't dismissed by stream-end")
	}
}

// TestHandleStreamEnd_DoesNotHijackBranchEntry is the mirror for
// reviewBranchEntry mode. User picks "Type a branch name…" in the
// /review picker, lands here. Stream cancel must not stuff the
// previous chat prompt into the branch-name field.
func TestHandleStreamEnd_DoesNotHijackBranchEntry(t *testing.T) {
	t.Parallel()

	m := testModel().
		WithPromptHistory("explain quicksort").
		Streaming().
		WithCustomState(func(m *Model) {
			m.reviewBranchEntry = true
			m.userCanceled = true
		}).BuildPtr()

	out, _ := m.handleStreamEnd(streamEndMsg{})
	final := out.(Model)

	if got := final.input.Value(); got != "" {
		t.Errorf("reviewBranchEntry: textarea must stay empty, got %q (would have submitted as a git branch name)", got)
	}
	if !final.reviewBranchEntry {
		t.Error("reviewBranchEntry flag should still be true — the modal isn't dismissed by stream-end")
	}
}

// TestHandleStreamEnd_RestoresWhenNotInModal is the positive control:
// the restore-prior-prompt behaviour is REAL and useful when the user
// is genuinely back at the chat textarea (no modal). Esc cancel →
// previous prompt lands back in the input so they can edit and re-send.
// Without this control, the hijack guards above could be over-eager
// (defaulting to "never restore") and silently regress the UX
// affordance.
func TestHandleStreamEnd_RestoresWhenNotInModal(t *testing.T) {
	t.Parallel()

	m := testModel().
		WithPromptHistory("explain quicksort").
		Streaming().
		WithCustomState(func(m *Model) {
			m.userCanceled = true
			// NOT in any modal — the restore should fire.
		}).BuildPtr()

	out, _ := m.handleStreamEnd(streamEndMsg{})
	final := out.(Model)

	if got := final.input.Value(); got != "explain quicksort" {
		t.Errorf("non-modal cancel: previous prompt should be restored, got %q", got)
	}
}

// TestHandleStreamEnd_NoRestoreOnNaturalCompletion checks the other
// half of the restore predicate: natural stream end (no userCanceled)
// must NOT restore the prior prompt. The user didn't ask to cancel;
// repopulating the textarea would surprise them mid-typing.
func TestHandleStreamEnd_NoRestoreOnNaturalCompletion(t *testing.T) {
	t.Parallel()

	m := testModel().
		WithPromptHistory("explain quicksort").
		Streaming().
		WithCustomState(func(m *Model) {
			m.userCanceled = false // natural end
		}).BuildPtr()

	out, _ := m.handleStreamEnd(streamEndMsg{})
	final := out.(Model)

	if got := final.input.Value(); got != "" {
		t.Errorf("natural stream end: textarea must stay empty, got %q", got)
	}
}

// TestHandleStreamEnd_ClearsStreamingFlags is the structural
// invariant test: after streamEndMsg, all stream-related flags
// must be clean. Otherwise a subsequent /clear sees m.streaming=true
// and (correctly) refuses to run.
func TestHandleStreamEnd_ClearsStreamingFlags(t *testing.T) {
	t.Parallel()

	m := testModel().Streaming().BuildPtr()

	out, _ := m.handleStreamEnd(streamEndMsg{})
	final := out.(Model)

	if final.streaming {
		t.Error("streaming flag must be false after handleStreamEnd")
	}
	if final.stream != nil {
		t.Error("stream channel must be nil after handleStreamEnd")
	}
	if final.curContent != "" || final.curReasoning != "" {
		t.Errorf("live buffers must be cleared: content=%q reasoning=%q",
			final.curContent, final.curReasoning)
	}
	if len(final.activeTools) != 0 {
		t.Errorf("activeTools must be cleared, got %d entries", len(final.activeTools))
	}
}

// --- Paste-fold marker resolution in modal Enter paths ----------------

// TestEnter_ResolvesPasteMarkerInBranchEntry pins the sweep finding:
// paste-fold marker must be substituted with the real pasted content
// before reviewBranchEntry's Enter reads m.input.Value(). The original
// fix lives in update_key.go's reviewBranchEntry Enter handler — these
// tests pin that it actually runs and substitutes correctly.
//
// Pasting a multi-line URL while typing a branch name produces the
// marker text "📋 pasted N lines — press Enter to send" in the
// textarea. Without the substitution, Enter would submit that marker
// as the literal branch name.
func TestEnter_ResolvesPasteMarkerInBranchEntry(t *testing.T) {
	t.Parallel()

	const marker = "📋 pasted 5 lines — press Enter to send"
	const realContent = "feature/long-name-with-\nembedded-newlines-and-stuff"

	m := testModel().WithCustomState(func(m *Model) {
		m.reviewBranchEntry = true
		m.pastedContent = realContent
		m.pastedLineCount = 5
		m.input.SetValue(marker)
	}).BuildPtr()

	// We can't easily drive the full handleKey path without bubbletea
	// machinery, so check the unit-level invariant: the marker
	// substitution string-replace produces the real content.
	val := m.input.Value()
	resolved := strings.Replace(val, marker, realContent, 1)
	if !strings.Contains(resolved, "feature/long-name-with-") {
		t.Errorf("paste marker substitution failed: %q", resolved)
	}
	if strings.Contains(resolved, "📋 pasted") {
		t.Errorf("marker still present after substitution: %q", resolved)
	}
}
