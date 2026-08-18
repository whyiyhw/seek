package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/tui/teatest"
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
		Model:   "deepseek-v4-flash",
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
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))
}

// TestRender_ShiftTabCommitsModeLabel: Shift+Tab cycles the permission
// mode and uses tea.Println to commit "  mode: plan" above the live
// region (update_key.go: KeyShiftTab branch). teatest captures all
// program output, so we assert on the printed text.
func TestRender_ShiftTabCommitsModeLabel(t *testing.T) {
	m := renderTestModel(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	waitFor(t, tm, "mode: plan")

	shutdown(t, tm)
}

// TestRender_SlashOpensCommandMenu: typing "/" opens the slash-command
// menu in the live region (renderCommandMenu in view.go). We assert on
// the footer hint string ("Tab to complete") which is always the last
// line of the menu regardless of command ordering, and sits at a stable
// position in the View() output.
func TestRender_SlashOpensCommandMenu(t *testing.T) {
	m := renderTestModel(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	tm.Send(tea.KeyPressMsg{Text: "/"})
	waitFor(t, tm, "Tab to complete")

	shutdown(t, tm)
}

// TestRender_HelpDispatch: dispatching /help all via the slash pipeline
// opens the help overlay in the live region (instead of committing
// to scrollback). We verify the overlay content appears in the
// program's accumulated output after shutdown.
func TestRender_HelpDispatch(t *testing.T) {
	m := renderTestModel(t)
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(80, 24))

	// Dispatch "/help all" through the slash menu: type "/help all"
	// and press Enter. Bare /help now opens a topic picker (mirroring
	// /model's picker pattern), so we pass an explicit topic to
	// directly show the overlay.
	for _, r := range "/help all" {
		tm.Send(tea.KeyPressMsg{Text: string(r)})
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Give the program a moment to render the help overlay, dismiss it,
	// then quit and verify the overlay content in accumulated output.
	time.Sleep(50 * time.Millisecond)
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEsc})
	time.Sleep(50 * time.Millisecond)
	tm.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	out := tm.FinalOutput(t)
	var buf bytes.Buffer
	buf.ReadFrom(out)
	output := buf.String()

	// The dismiss hint is only rendered by the help overlay.
	if !strings.Contains(output, "Esc or q to close help") {
		t.Error("help overlay dismiss hint not found in program output")
	}
	// Keybinding text from the help content should also be present.
	// M9.4: help renders bubbletea canonical key strings from keymap.Snapshot();
	// `shift+tab` is the default binding for cycle-mode.
	if !strings.Contains(output, "shift+tab") {
		t.Error("help keybinding 'shift+tab' not found in program output")
	}
}
