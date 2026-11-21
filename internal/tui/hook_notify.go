package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/hooks"
)

// notifyTitleMax caps the question text carried into
// hooks.NotificationEvent.Title. The shell env (SEEK_NOTIFY_TITLE) is
// one env var on a hook process command line — an unbounded picker
// question (they can be long) would be a footgun there. The cap is
// generous enough to identify the prompt and short enough that a hook
// logging it stays readable.
const notifyTitleMax = 120

// askUserNotificationCmd returns a tea.Cmd that fires the Notification
// observer for an ask_user picker popping up ("something surfaced
// outside the TUI's text plane" — feature-hooks-injection §3.3).
//
// Deliberately a Cmd and NOT a synchronous call in the Update handler:
// a shell `notification` hook execs `bash -c` with its own timeout
// (default 5s), and blocking the Bubble Tea loop on that would freeze
// the picker the user is about to interact with. The Cmd runs on a
// separate goroutine, so the picker renders immediately.
//
// nil registry → nil Cmd (no batch entry), keeping the no-hooks path
// byte-for-byte the same as before this wiring.
func askUserNotificationCmd(hreg *hooks.Registry, question string) tea.Cmd {
	if hreg == nil {
		return nil
	}
	title := truncateOneLine(question, notifyTitleMax)
	return func() tea.Msg {
		// context.Background: the notification is a standalone fact
		// ("a picker appeared"), not part of the turn that spawned it.
		hreg.NotifyNotification(context.Background(), hooks.NotificationEvent{
			Kind:  "ask_user",
			Title: title,
		})
		return nil
	}
}

// batchNotificationTitle renders the Notification title for a v2
// ask_user batch: the first question — what the user is answering
// right now, which is what a hook author wants to key on — plus a
// "(1/N)" suffix when the batch has more, so a hook can tell 1-of-1
// from 1-of-4 (update.go's wiring comment promises exactly this).
// The suffix rides through the shared notifyTitleMax truncation: a
// question long enough to hit the cap loses it, which is the same
// "truncated" signal as any other over-long title. An empty batch is
// defensive-only (the tool rejects it); fall back to a generic label
// rather than an empty Title.
func batchNotificationTitle(b askuser.Batch) string {
	if len(b.Questions) == 0 {
		return "ask_user"
	}
	title := b.Questions[0].Question
	if len(b.Questions) > 1 {
		title = fmt.Sprintf("%s (1/%d)", title, len(b.Questions))
	}
	return title
}
