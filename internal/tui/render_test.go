package tui

import (
	"bytes"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/whyiyhw/seek/internal/cache"
)

// renderTestModel builds a Model wired with the minimum for end-to-end
// teatest rendering: a Tracker so the status bar can format costs and
// a model id so the picker has something to match against. Network
// upgrade probe is disabled.
//
// These tests drive a full tea.Program through Update + View, catching
// regressions that pure handler-level tests miss — e.g. a label rename
// in view.go (▌ you → ▌ yu) would slip past update_test.go but trip
// the assertions here.
func renderTestModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("SEEK_NO_UPGRADE_CHECK", "1")
	SetTheme("dark")
	return New(Options{
		Tracker: cache.New(),
		Model:   "deepseek-chat",
	})
}

// waitFor is a thin wrapper over teatest.WaitFor with shared timing
// defaults — most TUI renders settle in a frame or two, but bubbletea
// batches commands, so we give it 2s before failing.
func waitFor(t *testing.T, tm *teatest.TestModel, needle string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains(b, []byte(needle))
	},
		teatest.WithCheckInterval(20*time.Millisecond),
		teatest.WithDuration(2*time.Second),
	)
}

// shutdown sends Ctrl+C and waits for the program to exit cleanly so
// the test doesn't leak goroutines (versionCheckCmd / tickStatusEvery
// live until the program returns).
func shutdown(t *testing.T, tm *teatest.TestModel) {
	t.Helper()
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestRender_ShiftTabCommitsModeLabel: Shift+Tab cycles the permission
// mode and uses tea.Println to commit "  mode: plan" above the live
// region (update_key.go: KeyShiftTab branch). teatest captures all
// program output, so we assert on the printed text.
func TestRender_ShiftTabCommitsModeLabel(t *testing.T) {
	m := renderTestModel(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	tm.Send(tea.KeyMsg{Type: tea.KeyShiftTab})
	waitFor(t, tm, "mode: plan")

	shutdown(t, tm)
}

// TestRender_SlashOpensCommandMenu: typing "/" opens the slash-command
// menu in the live region (renderCommandMenu in view.go). We assert on
// a stable menu entry (/help is always present) so the test doesn't
// break when commands get reordered.
func TestRender_SlashOpensCommandMenu(t *testing.T) {
	m := renderTestModel(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	waitFor(t, tm, "/help")

	shutdown(t, tm)
}

// TestRender_HelpOverlayDispatch: dispatching /help via the slash
// pipeline flips m.helpOverlayOpen and View() switches to
// renderHelpOverlay. Catches regressions in BOTH the command-dispatch
// path AND the overlay renderer (handler-level tests only cover one
// or the other in isolation).
func TestRender_HelpOverlayDispatch(t *testing.T) {
	m := renderTestModel(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	for _, r := range "/help" {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	// First Enter: slash-menu accepts "/help" as the highlighted
	// candidate and sets the textarea to "/help " (with space) —
	// matches what a real user sees. Second Enter dispatches the
	// command for real.
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// Can't assert on the panel's "Help — seek" title — initialSizeCmd
	// forces 80x24 (os.Stdout fallback in test env) and the help panel
	// is ~30 rows tall, so the title scrolls past stdout before
	// bubbletea writes it. Assert on a bottom-of-panel string that IS
	// in the visible viewport AND is a distinctive overlay signature
	// (key-binding text doesn't appear anywhere outside the overlay).
	waitFor(t, tm, "Shift+Tab")

	shutdown(t, tm)
}
