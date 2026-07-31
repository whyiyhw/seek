package tui

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

func TestNormalizePasteText_CRLF(t *testing.T) {
	got := normalizePasteText("a\r\nb\rc")
	want := "a\nb\nc"
	if got != want {
		t.Fatalf("normalizePasteText() = %q, want %q", got, want)
	}
}

func TestPasteLineCount_CRLF(t *testing.T) {
	if n := pasteLineCount("a\r\nb\r\n"); n != 2 {
		t.Fatalf("pasteLineCount(CRLF) = %d, want 2", n)
	}
}

func TestEnterInsertsNewlineDuringPaste(t *testing.T) {
	m := Model{lastInputRunesAt: time.Now(), legacyConhostInput: true}
	if !m.enterInsertsNewlineDuringPaste() {
		t.Fatal("expected Enter within pasteEnterGap to insert newline")
	}
	m.lastInputRunesAt = time.Now().Add(-pasteEnterGap - time.Millisecond)
	if m.enterInsertsNewlineDuringPaste() {
		t.Fatal("expected stale Enter to submit, not newline")
	}
}

// TestEnterInsertsNewlineDuringPaste_GatedOff pins the Windows Terminal /
// IME regression: outside legacy conhost the guard must NEVER fire, even
// when Enter arrives right after runes. Windows Terminal forwards the
// IME-commit Enter (text + Enter in one dispatch), so a firing guard turns
// every Chinese/Japanese/Korean message into a stray newline instead of a
// send. Conhost is safe because it suppresses keys during active IME
// composition instead of forwarding them.
func TestEnterInsertsNewlineDuringPaste_GatedOff(t *testing.T) {
	m := Model{lastInputRunesAt: time.Now()} // zero-value legacyConhostInput
	if m.enterInsertsNewlineDuringPaste() {
		t.Fatal("guard must be off outside legacy conhost (zero-value model)")
	}
	m.legacyConhostInput = false                       // explicit WT-like environment
	m.lastInputRunesAt = time.Now().Add(pasteEnterGap) // fresh runes
	if m.enterInsertsNewlineDuringPaste() {
		t.Fatal("guard must not fire on terminal emulators even with fresh runes")
	}
}

// TestEnterSubmitsOutsideLegacyConhost is the end-to-end pin of the reported
// bug: on Windows Terminal (guard off), an Enter arriving within pasteEnterGap
// of typed runes must SUBMIT the message, not insert a newline.
func TestEnterSubmitsOutsideLegacyConhost(t *testing.T) {
	m := testModel().
		WithAgent(newFakeAgent()).
		WithCustomState(func(m *Model) {
			m.opts.Ctx = context.Background()
			m.legacyConhostInput = false // Windows Terminal / any emulator
		}).
		Build()
	m.input.SetValue("你好世界")
	// Fresh runes — the old time-based guard would have fired here.
	m.lastInputRunesAt = time.Now().Add(pasteEnterGap)

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := out.(Model)
	if mm.input.Value() != "" {
		t.Fatalf("Enter should submit and clear the input, got %q", mm.input.Value())
	}
	if !mm.streaming {
		t.Fatal("Enter should have submitted the message (streaming=true)")
	}
}

func TestLegacyConhostInputFor(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{"windows bare conhost", "windows", nil, true},
		{"windows terminal", "windows", map[string]string{"WT_SESSION": "guid"}, false},
		{"vscode terminal", "windows", map[string]string{"TERM_PROGRAM": "vscode"}, false},
		{"wezterm", "windows", map[string]string{"TERM_PROGRAM": "WezTerm"}, false},
		{"mintty", "windows", map[string]string{"TERM_PROGRAM": "mintty"}, false},
		{"conemu", "windows", map[string]string{"ConEmuPID": "1234"}, false},
		{"wt wins over TERM_PROGRAM", "windows", map[string]string{"WT_SESSION": "g", "TERM_PROGRAM": "vscode"}, false},
		{"empty env value counts as unset", "windows", map[string]string{"WT_SESSION": ""}, true},
		{"linux", "linux", nil, false},
		{"darwin", "darwin", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Host-independent baseline: explicitly clear every detection
			// var (empty string == unset for os.Getenv). Without this the
			// dev machine's own WT_SESSION / TERM_PROGRAM leaks in.
			for _, k := range []string{"WT_SESSION", "TERM_PROGRAM", "ConEmuPID", legacyConhostInputEnvOverride} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := legacyConhostInputFor(tc.goos); got != tc.want {
				t.Fatalf("legacyConhostInputFor(%q, env=%v) = %v, want %v", tc.goos, tc.env, got, tc.want)
			}
		})
	}
}

func TestDetectLegacyConhostInput_Override(t *testing.T) {
	// WT_SESSION=guid makes the detection baseline deterministic ("not
	// legacy") on every host, so the fallback case is host-independent.
	t.Setenv("WT_SESSION", "guid")

	for _, v := range []string{"1", "true", "on", "TRUE"} {
		t.Setenv(legacyConhostInputEnvOverride, v)
		if !detectLegacyConhostInput() {
			t.Fatalf("override %q must force the guard on", v)
		}
	}
	for _, v := range []string{"0", "false", "off", "FALSE"} {
		t.Setenv(legacyConhostInputEnvOverride, v)
		if detectLegacyConhostInput() {
			t.Fatalf("override %q must force the guard off", v)
		}
	}
	// Unknown values fall back to auto-detection.
	t.Setenv(legacyConhostInputEnvOverride, "banana")
	if got, want := detectLegacyConhostInput(), legacyConhostInputFor(runtime.GOOS); got != want {
		t.Fatalf("garbage override should fall back to detection: got %v, want %v", got, want)
	}
}

func TestPasteBurstEnterInsertsNewline(t *testing.T) {
	m := Model{input: textarea.New(), legacyConhostInput: true}
	// Set in the future so the time check is always within pasteEnterGap,
	// regardless of goroutine scheduling delays under -race or heavy load.
	m.lastInputRunesAt = time.Now().Add(pasteEnterGap)
	m.input.SetValue("line1")

	out, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	mm := out.(Model)
	if mm.input.Value() != "line1\n" {
		t.Fatalf("burst Enter should insert newline, got %q", mm.input.Value())
	}
}

func TestInsertPasteText_FoldsLongPaste(t *testing.T) {
	m := Model{input: textarea.New()}
	m.input.SetHeight(3)
	lines := strings.Join([]string{"l1", "l2", "l3", "l4", "l5", "l6"}, "\n")

	out := m.insertPasteText(lines)
	if out.pastedContent != lines {
		t.Fatalf("pastedContent not stored: %q", out.pastedContent)
	}
	if !strings.Contains(out.input.Value(), "pasted 6 lines") {
		t.Fatalf("expected fold marker, got %q", out.input.Value())
	}
}

func TestResolvePasteInInput(t *testing.T) {
	m := Model{input: textarea.New()}
	m.pastedContent = "real\nbody"
	m.pastedLineCount = 2
	m.input.SetValue(pasteFoldMarker(2))

	(&m).resolvePasteInInput()
	if m.pastedContent != "" {
		t.Fatal("pastedContent should be cleared")
	}
	if got := m.input.Value(); got != "real\nbody" {
		t.Fatalf("resolvePasteInInput() = %q", got)
	}
}
