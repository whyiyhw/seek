package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/pkg/agent"
)

// emptyModel returns a Model with just enough wiring for command tests.
// No viewport (inline mode); no real agent.
func emptyModel() *Model {
	m := Model{
		opts: Options{
			Tracker: cache.New(),
			Model:   "deepseek-chat",
		},
		input: textarea.New(),
	}
	return &m
}

// runHandler invokes a command handler and returns its cmdResult. In
// inline mode the result is the testable surface — text + flags — and
// avoids any bubbletea machinery.
func runHandler(t *testing.T, m *Model, input string) cmdResult {
	t.Helper()
	parts := strings.SplitN(input, " ", 2)
	name := strings.TrimSpace(parts[0])
	var args string
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	for _, c := range allCommands() {
		for _, n := range c.names {
			if n == name {
				return c.handler(m, args)
			}
		}
	}
	t.Fatalf("no handler for %q", input)
	return cmdResult{}
}

func TestDispatch_RejectsNonCommands(t *testing.T) {
	m := emptyModel()
	handled, _ := dispatchCommand(m, "hello world")
	if handled {
		t.Errorf("non-slash input treated as command")
	}
}

func TestDispatch_Unknown(t *testing.T) {
	m := emptyModel()
	handled, cmd := dispatchCommand(m, "/foobar")
	if !handled {
		t.Errorf("unknown should be handled (with a feedback line)")
	}
	if cmd == nil {
		t.Errorf("unknown should still produce a feedback cmd")
	}
}

func TestHelp_TextHasKeys(t *testing.T) {
	res := runHandler(t, emptyModel(), "/help")
	for _, frag := range []string{"/help", "/clear", "/reset", "/exit", "Ctrl+R", "scrollback"} {
		if !strings.Contains(res.text, frag) {
			t.Errorf("help missing %q: %s", frag, res.text)
		}
	}
}

func TestExit_SetsQuit(t *testing.T) {
	res := runHandler(t, emptyModel(), "/exit")
	if !res.quit {
		t.Errorf("expected quit=true")
	}
}

func TestClear_SetsClear(t *testing.T) {
	res := runHandler(t, emptyModel(), "/clear")
	if !res.clear {
		t.Errorf("expected clear=true")
	}
}

func TestModel_NoArg_ShowsCurrent(t *testing.T) {
	res := runHandler(t, emptyModel(), "/model")
	if !strings.Contains(res.text, "current model") {
		t.Errorf("text = %q", res.text)
	}
}

func TestModel_WithArg_SwitchesAndFiresHook(t *testing.T) {
	m := emptyModel()
	var captured string
	m.opts.SetModel = func(s string) { captured = s }
	res := runHandler(t, m, "/model deepseek-reasoner")
	if captured != "deepseek-reasoner" {
		t.Errorf("SetModel got %q", captured)
	}
	if m.opts.Model != "deepseek-reasoner" {
		t.Errorf("Options.Model = %q", m.opts.Model)
	}
	if !strings.Contains(res.text, "deepseek-reasoner") {
		t.Errorf("feedback text = %q", res.text)
	}
}

func TestYolo_TogglesAndFiresHook(t *testing.T) {
	m := emptyModel()
	var seen []bool
	m.opts.SetYolo = func(b bool) { seen = append(seen, b) }
	runHandler(t, m, "/yolo")
	runHandler(t, m, "/yolo")
	if len(seen) != 2 || seen[0] != true || seen[1] != false {
		t.Errorf("toggle: %v", seen)
	}
}

func TestReset_UsesHook(t *testing.T) {
	m := emptyModel()
	called := false
	m.opts.RebuildAgent = func() (*agent.Agent, error) {
		called = true
		return nil, nil
	}
	res := runHandler(t, m, "/reset")
	if !called {
		t.Error("RebuildAgent hook not invoked")
	}
	if !res.clear {
		t.Errorf("reset should also clear the screen")
	}
}

func TestReset_ReportsHookErrors(t *testing.T) {
	m := emptyModel()
	m.opts.RebuildAgent = func() (*agent.Agent, error) {
		return nil, errors.New("network down")
	}
	res := runHandler(t, m, "/reset")
	if !strings.Contains(res.text, "reset failed") || !strings.Contains(res.text, "network down") {
		t.Errorf("text = %q", res.text)
	}
}

func TestReset_NoHook(t *testing.T) {
	m := emptyModel()
	res := runHandler(t, m, "/reset")
	if !strings.Contains(res.text, "unsupported") {
		t.Errorf("text = %q", res.text)
	}
}
