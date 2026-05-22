package memory

import (
	"context"
	"testing"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// happyHistory returns a session that clears the satisfaction
// threshold (long-enough, no rejection, no tool errors).
func happyHistory() []deepseek.Message {
	return []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
		{Role: deepseek.RoleUser, Content: "refactor X"},
		{Role: deepseek.RoleAssistant, Content: "plan"},
		{Role: deepseek.RoleUser, Content: "go ahead"},
		{Role: deepseek.RoleAssistant, Content: "step 1 done"},
		{Role: deepseek.RoleUser, Content: "now step 2"},
		{Role: deepseek.RoleAssistant, Content: "step 2 done"},
		{Role: deepseek.RoleUser, Content: "great, wrap up"},
		{Role: deepseek.RoleAssistant, Content: "wrapped"},
	}
}

func TestAutoDistill_GatedOffByDefault(t *testing.T) {
	t.Setenv("SEEK_AUTO_DISTILL", "") // ensure off
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `[{"name":"would-have-saved","tagline":"x","content":"y"}]`,
		}}}},
	}
	h := &Hook{
		Project:         p,
		Distiller:       &Distiller{Client: fake},
		HistoryProvider: happyHistory,
	}
	h.OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	if _, ok := p.Get("would-have-saved"); ok {
		t.Errorf("auto-distill should be off by default; entry was written anyway")
	}
	if fake.lastReq != nil {
		t.Errorf("reasoner should NOT be called when auto-distill is off")
	}
}

func TestAutoDistill_OnHappySessionWritesAutoSourced(t *testing.T) {
	t.Setenv("SEEK_AUTO_DISTILL", "1")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `[
				{"name":"step-1-pattern","tagline":"observed","content":"body","tags":["arch"]},
				{"name":"step-2-pattern","tagline":"also observed","content":"body2"}
			]`,
		}}}},
	}
	h := &Hook{
		Project:         p,
		Distiller:       &Distiller{Client: fake},
		HistoryProvider: happyHistory,
	}
	h.OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	if fake.lastReq == nil {
		t.Fatalf("reasoner should have been called on a happy session with auto-distill on")
	}
	for _, name := range []string{"step-1-pattern", "step-2-pattern"} {
		got, ok := p.Get(name)
		if !ok {
			t.Errorf("expected auto-distilled entry %q in M", name)
			continue
		}
		if !got.AutoSourced {
			t.Errorf("entry %q should have AutoSourced=true", name)
		}
	}
}

func TestAutoDistill_SkipsRejectedSession(t *testing.T) {
	t.Setenv("SEEK_AUTO_DISTILL", "1")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `[{"name":"x","tagline":"y","content":"z"}]`,
		}}}},
	}
	rejected := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
		{Role: deepseek.RoleUser, Content: "do X"},
		{Role: deepseek.RoleAssistant, Content: "trying"},
		{Role: deepseek.RoleUser, Content: "no, that's wrong"},
		{Role: deepseek.RoleAssistant, Content: "OK fixing"},
		{Role: deepseek.RoleUser, Content: "still broken"},
		{Role: deepseek.RoleAssistant, Content: "trying again"},
		{Role: deepseek.RoleUser, Content: "undo that"},
		{Role: deepseek.RoleAssistant, Content: "reverted"},
	}

	h := &Hook{
		Project:         p,
		Distiller:       &Distiller{Client: fake},
		HistoryProvider: func() []deepseek.Message { return rejected },
	}
	h.OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	if fake.lastReq != nil {
		t.Errorf("reasoner should NOT be called on a rejection-heavy session")
	}
	if _, ok := p.Get("x"); ok {
		t.Errorf("no entries should be written from a rejected session")
	}
}

func TestAutoDistill_SkipsTooShortSession(t *testing.T) {
	t.Setenv("SEEK_AUTO_DISTILL", "1")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `[{"name":"x","tagline":"y","content":"z"}]`,
		}}}},
	}
	short := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"},
		{Role: deepseek.RoleUser, Content: "quick question"},
		{Role: deepseek.RoleAssistant, Content: "answer"},
	}

	h := &Hook{
		Project:         p,
		Distiller:       &Distiller{Client: fake},
		HistoryProvider: func() []deepseek.Message { return short },
	}
	h.OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	if fake.lastReq != nil {
		t.Errorf("reasoner should NOT be called on a too-short session")
	}
}

func TestAutoDistill_NilDistillerOrHistoryIsSafe(t *testing.T) {
	t.Setenv("SEEK_AUTO_DISTILL", "1")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	// Missing Distiller: hook should no-op.
	(&Hook{
		Project:         p,
		HistoryProvider: happyHistory,
	}).OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	// Missing HistoryProvider: hook should no-op.
	(&Hook{
		Project:   p,
		Distiller: &Distiller{Client: &fakeChatClient{}},
	}).OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	// Both missing project AND distiller: still safe.
	(&Hook{}).OnSessionEnd(context.Background(), hooks.SessionEndEvent{})
}

func TestAutoDistill_ReasonerErrorIsSwallowed(t *testing.T) {
	// Auto-distill is best-effort enhancement. A reasoner failure
	// (network, rate limit, malformed response) must NOT propagate —
	// session shutdown should complete cleanly.
	t.Setenv("SEEK_AUTO_DISTILL", "1")
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: "this is not JSON at all", // will fail ParseCandidates
		}}}},
	}
	h := &Hook{
		Project:         p,
		Distiller:       &Distiller{Client: fake},
		HistoryProvider: happyHistory,
	}
	// Must not panic / not propagate (observers can't surface errors
	// anyway, but our internal swallowing must hold).
	h.OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	if len(p.Entries()) != 0 {
		t.Errorf("no entries should be written when parsing fails")
	}
}

func TestAutoDistill_EnvVarTruthyValues(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on", "TRUE", "Yes"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("SEEK_AUTO_DISTILL", v)
			if !autoDistillEnabled() {
				t.Errorf("%q should enable auto-distill", v)
			}
		})
	}
	for _, v := range []string{"", "0", "false", "no", "off", "maybe"} {
		t.Run("off-"+v, func(t *testing.T) {
			t.Setenv("SEEK_AUTO_DISTILL", v)
			if autoDistillEnabled() {
				t.Errorf("%q should NOT enable auto-distill", v)
			}
		})
	}
}
