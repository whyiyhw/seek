package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/pkg/agent"
)

// emptyModel returns a Model with just enough wiring for command tests.
// No real agent — tests should not invoke /reset's RebuildAgent unless
// they supply the hook themselves.
func emptyModel() *Model {
	m := Model{
		opts: Options{
			Tracker: cache.New(),
			Model:   "deepseek-chat",
		},
		input:         textarea.New(),
		viewport:      viewport.New(80, 10),
		curToolActive: map[string]string{},
	}
	return &m
}

func TestDispatch_RejectsNonCommands(t *testing.T) {
	m := emptyModel()
	handled, _ := dispatchCommand(m, "hello world")
	if handled {
		t.Errorf("non-slash input treated as command")
	}
}

func TestDispatch_Help(t *testing.T) {
	m := emptyModel()
	handled, cmd := dispatchCommand(m, "/help")
	if !handled || cmd != nil {
		t.Errorf("help should be handled with no cmd; got handled=%v cmd=%v", handled, cmd)
	}
	if len(m.history) != 1 || m.history[0].role != "system" {
		t.Fatalf("expected one system history item")
	}
	for _, frag := range []string{"/help", "/clear", "/reset", "/exit", "Ctrl+R"} {
		if !strings.Contains(m.history[0].text, frag) {
			t.Errorf("help missing %q", frag)
		}
	}
}

func TestDispatch_Unknown(t *testing.T) {
	m := emptyModel()
	handled, cmd := dispatchCommand(m, "/foobar")
	if !handled || cmd != nil {
		t.Errorf("unknown should be handled; got handled=%v cmd=%v", handled, cmd)
	}
	if len(m.history) != 1 || !strings.Contains(m.history[0].text, "unknown") {
		t.Errorf("history: %+v", m.history)
	}
}

func TestDispatch_Exit(t *testing.T) {
	m := emptyModel()
	_, cmd := dispatchCommand(m, "/exit")
	if cmd == nil {
		t.Fatal("expected tea.Quit command")
	}
	// We can't directly compare tea.Cmd; just verify it returns tea.QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("got msg %T, want tea.QuitMsg", msg)
	}
}

func TestDispatch_Clear(t *testing.T) {
	m := emptyModel()
	m.history = append(m.history,
		historyItem{role: "user", text: "x"},
		historyItem{role: "assistant", text: "y"},
	)
	dispatchCommand(m, "/clear")
	if len(m.history) != 0 {
		t.Errorf("history not cleared: %+v", m.history)
	}
}

func TestDispatch_Model(t *testing.T) {
	m := emptyModel()
	var captured string
	m.opts.SetModel = func(s string) { captured = s }

	// No arg → status message, no switch.
	dispatchCommand(m, "/model")
	if !strings.Contains(m.history[len(m.history)-1].text, "current model") {
		t.Errorf("missing current-model status")
	}
	if captured != "" {
		t.Errorf("SetModel called without arg: %q", captured)
	}

	// With arg → switch happens.
	dispatchCommand(m, "/model deepseek-reasoner")
	if captured != "deepseek-reasoner" {
		t.Errorf("SetModel got %q, want deepseek-reasoner", captured)
	}
	if m.opts.Model != "deepseek-reasoner" {
		t.Errorf("Options.Model not updated: %q", m.opts.Model)
	}
}

func TestDispatch_YoloToggle(t *testing.T) {
	m := emptyModel()
	var seen []bool
	m.opts.SetYolo = func(b bool) { seen = append(seen, b) }

	dispatchCommand(m, "/yolo")
	dispatchCommand(m, "/yolo")
	if len(seen) != 2 || seen[0] != true || seen[1] != false {
		t.Errorf("toggle: %v", seen)
	}
}

func TestDispatch_ResetUsesHook(t *testing.T) {
	m := emptyModel()
	called := false
	m.opts.RebuildAgent = func() (*agent.Agent, error) {
		called = true
		return nil, nil // tests don't need a real *Agent
	}
	dispatchCommand(m, "/reset")
	if !called {
		t.Error("RebuildAgent hook not invoked")
	}
}

func TestDispatch_ResetReportsHookErrors(t *testing.T) {
	m := emptyModel()
	m.opts.RebuildAgent = func() (*agent.Agent, error) {
		return nil, errors.New("network down")
	}
	dispatchCommand(m, "/reset")
	last := m.history[len(m.history)-1]
	if !strings.Contains(last.text, "reset failed") || !strings.Contains(last.text, "network down") {
		t.Errorf("missing failure note: %q", last.text)
	}
}
