package keymap

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNewDefault_HasAllActions(t *testing.T) {
	km := NewDefault()
	for _, info := range AllActions() {
		if got := km.KeyFor(info.Action); got != info.Default {
			t.Errorf("KeyFor(%s) = %q, want %q", info.Action, got, info.Default)
		}
		if src := km.SourceFor(info.Action); src != "default" {
			t.Errorf("SourceFor(%s) = %q, want %q", info.Action, src, "default")
		}
	}
}

func TestResolve_DefaultBindings(t *testing.T) {
	km := NewDefault()
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want Action
	}{
		{"ctrl+c → interrupt", tea.KeyMsg{Type: tea.KeyCtrlC}, ActionInterrupt},
		{"enter → submit", tea.KeyMsg{Type: tea.KeyEnter}, ActionSubmit},
		{"alt+enter → steer", tea.KeyMsg{Type: tea.KeyEnter, Alt: true}, ActionSteer},
		{"esc → cancel", tea.KeyMsg{Type: tea.KeyEsc}, ActionCancel},
		{"ctrl+l → clear-screen", tea.KeyMsg{Type: tea.KeyCtrlL}, ActionClearScreen},
		{"ctrl+r → toggle-reasoning", tea.KeyMsg{Type: tea.KeyCtrlR}, ActionToggleReasoning},
		{"shift+tab → cycle-mode", tea.KeyMsg{Type: tea.KeyShiftTab}, ActionCycleMode},
		{"up → history-prev", tea.KeyMsg{Type: tea.KeyUp}, ActionHistoryPrev},
		{"down → history-next", tea.KeyMsg{Type: tea.KeyDown}, ActionHistoryNext},
		{"? → toggle-help", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, ActionToggleHelp},
		{"a → none", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}, ActionNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := km.Resolve(tc.msg); got != tc.want {
				t.Errorf("Resolve(%s) = %q, want %q (msg.String=%q)",
					tc.name, got, tc.want, tc.msg.String())
			}
		})
	}
}

func TestResolve_NilKeyMap_ReturnsNone(t *testing.T) {
	var km *KeyMap
	if got := km.Resolve(tea.KeyMsg{Type: tea.KeyEnter}); got != ActionNone {
		t.Errorf("nil KeyMap Resolve should be ActionNone, got %q", got)
	}
}

func TestLoad_MissingFile_ReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	km, ok := Load(filepath.Join(dir, "does-not-exist.toml"), io.Discard)
	if !ok {
		t.Fatal("missing file should silently fall back to defaults (ok=true)")
	}
	if km.KeyFor(ActionSubmit) != "enter" {
		t.Errorf("default submit key wrong: %q", km.KeyFor(ActionSubmit))
	}
}

func TestLoad_SyntaxError_FallsBackToDefaults(t *testing.T) {
	var warn bytes.Buffer
	// Unclosed string literal — guaranteed toml syntax error.
	km, ok := LoadBytes([]byte(`[bindings]
submit = "ctrl+enter
`), "test.toml", &warn)
	if ok {
		t.Error("syntax error should reject the file (ok=false)")
	}
	if !strings.Contains(warn.String(), "syntax error") {
		t.Errorf("warning should mention syntax error, got %q", warn.String())
	}
	if km.KeyFor(ActionSubmit) != "enter" {
		t.Errorf("after syntax error submit should be default; got %q", km.KeyFor(ActionSubmit))
	}
}

func TestLoad_UnknownAction_DropsRowKeepsRest(t *testing.T) {
	var warn bytes.Buffer
	km, ok := LoadBytes([]byte(`[bindings]
submmit = "ctrl+enter"
clear-screen = "ctrl+x"
`), "test.toml", &warn)
	if !ok {
		t.Errorf("unknown action should not invalidate the file; got ok=false (warnings: %s)", warn.String())
	}
	if !strings.Contains(warn.String(), "unknown action") || !strings.Contains(warn.String(), "submmit") {
		t.Errorf("warning should flag unknown action submmit, got %q", warn.String())
	}
	// The typo row was dropped, default for submit preserved
	if km.KeyFor(ActionSubmit) != "enter" {
		t.Errorf("after unknown-action drop, submit should stay default; got %q", km.KeyFor(ActionSubmit))
	}
	// The valid row applied
	if km.KeyFor(ActionClearScreen) != "ctrl+x" {
		t.Errorf("valid override clear-screen=ctrl+x not applied; got %q", km.KeyFor(ActionClearScreen))
	}
	if km.SourceFor(ActionClearScreen) != "user" {
		t.Errorf("clear-screen source should be 'user', got %q", km.SourceFor(ActionClearScreen))
	}
}

func TestLoad_ReservedKey_Rejected(t *testing.T) {
	var warn bytes.Buffer
	_, ok := LoadBytes([]byte(`[bindings]
clear-screen = "tab"
`), "test.toml", &warn)
	if !ok {
		t.Error("reserved key should drop just that row, not invalidate the whole file")
	}
	if !strings.Contains(warn.String(), "reserved key") || !strings.Contains(warn.String(), "tab") {
		t.Errorf("warning should flag reserved key tab, got %q", warn.String())
	}
}

func TestLoad_ConflictingKeys_RejectsWholeFile(t *testing.T) {
	var warn bytes.Buffer
	km, ok := LoadBytes([]byte(`[bindings]
clear-screen = "ctrl+x"
toggle-reasoning = "ctrl+x"
`), "test.toml", &warn)
	if ok {
		t.Error("conflicting keys should invalidate the whole file (PRD §4.4 rule 4)")
	}
	if !strings.Contains(warn.String(), "multiple actions") {
		t.Errorf("warning should flag the conflict, got %q", warn.String())
	}
	// All bindings back to defaults
	if km.KeyFor(ActionClearScreen) != "ctrl+l" {
		t.Errorf("after conflict reject, clear-screen should be default ctrl+l; got %q", km.KeyFor(ActionClearScreen))
	}
}

func TestLoad_ValidRebind_AppliesAndUpdatesReverseLookup(t *testing.T) {
	km, ok := LoadBytes([]byte(`[bindings]
clear-screen = "ctrl+x"
`), "test.toml", io.Discard)
	if !ok {
		t.Fatal("valid file should apply (ok=true)")
	}
	// Forward lookup
	if got := km.KeyFor(ActionClearScreen); got != "ctrl+x" {
		t.Errorf("KeyFor(ActionClearScreen) = %q, want ctrl+x", got)
	}
	// Reverse lookup via Resolve — new key triggers
	if act := km.Resolve(tea.KeyMsg{Type: tea.KeyCtrlX}); act != ActionClearScreen {
		t.Errorf("ctrl+x should resolve to clear-screen, got %q", act)
	}
	// Old default no longer triggers clear-screen (cleared from byKey)
	if act := km.Resolve(tea.KeyMsg{Type: tea.KeyCtrlL}); act == ActionClearScreen {
		t.Errorf("old default ctrl+l should no longer trigger clear-screen after rebind")
	}
}

func TestLoad_CaseInsensitiveKey(t *testing.T) {
	km, ok := LoadBytes([]byte(`[bindings]
clear-screen = "Ctrl+X"
`), "test.toml", io.Discard)
	if !ok {
		t.Fatal("case-mixed key should still apply")
	}
	if got := km.KeyFor(ActionClearScreen); got != "ctrl+x" {
		t.Errorf("normalized key should be ctrl+x, got %q", got)
	}
}

func TestLoad_UnparseableKey_DroppedWithWarning(t *testing.T) {
	var warn bytes.Buffer
	km, ok := LoadBytes([]byte(`[bindings]
clear-screen = "weird key with spaces"
`), "test.toml", &warn)
	if !ok {
		t.Error("unparseable key should drop the row, not invalidate the file")
	}
	if !strings.Contains(warn.String(), "unrecognised key") {
		t.Errorf("warning should flag unrecognised key, got %q", warn.String())
	}
	if km.KeyFor(ActionClearScreen) != "ctrl+l" {
		t.Errorf("dropped row should leave default in place; got %q", km.KeyFor(ActionClearScreen))
	}
}

func TestSnapshot_OrderMatchesAllActions(t *testing.T) {
	km := NewDefault()
	snap := km.Snapshot()
	infos := AllActions()
	if len(snap) != len(infos) {
		t.Fatalf("Snapshot length %d != AllActions length %d", len(snap), len(infos))
	}
	for i, b := range snap {
		if b.Action != infos[i].Action {
			t.Errorf("Snapshot[%d].Action = %q, want %q", i, b.Action, infos[i].Action)
		}
	}
}

func TestSnapshot_FlagsUserOverrides(t *testing.T) {
	km, _ := LoadBytes([]byte(`[bindings]
clear-screen = "ctrl+x"
`), "", io.Discard)
	snap := km.Snapshot()
	for _, b := range snap {
		if b.Action == ActionClearScreen && b.Source != "user" {
			t.Errorf("ActionClearScreen Source = %q, want user", b.Source)
		}
		if b.Action == ActionSubmit && b.Source != "default" {
			t.Errorf("ActionSubmit Source = %q, want default", b.Source)
		}
	}
}

func TestCheck_GoodFile_ReturnsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.toml")
	if err := os.WriteFile(path, []byte(`[bindings]
clear-screen = "ctrl+x"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if errs := Check(path); errs != nil {
		t.Errorf("good file should return nil errors, got %v", errs)
	}
}

func TestCheck_BadFile_ReturnsErrorStrings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.toml")
	if err := os.WriteFile(path, []byte(`[bindings]
fooaction = "ctrl+x"
clear-screen = "tab"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	errs := Check(path)
	if len(errs) < 2 {
		t.Fatalf("expected ≥2 error lines, got %v", errs)
	}
	joined := strings.Join(errs, "\n")
	if !strings.Contains(joined, "unknown action") {
		t.Errorf("expected unknown action error, got %q", joined)
	}
	if !strings.Contains(joined, "reserved key") {
		t.Errorf("expected reserved key error, got %q", joined)
	}
}

func TestIsReservedKey(t *testing.T) {
	for _, k := range []string{"tab", "backspace", "space"} {
		if !IsReservedKey(k) {
			t.Errorf("%q should be reserved", k)
		}
	}
	for _, k := range []string{"ctrl+x", "enter", "esc"} {
		if IsReservedKey(k) {
			t.Errorf("%q should NOT be reserved (it's rebindable as a source for its default action)", k)
		}
	}
}

func TestIsKnownAction(t *testing.T) {
	for _, info := range AllActions() {
		if !IsKnownAction(string(info.Action)) {
			t.Errorf("AllActions includes %q but IsKnownAction says no", info.Action)
		}
	}
	if IsKnownAction("submmit") {
		t.Error("typo submmit should NOT be a known action")
	}
}
