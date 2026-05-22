package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
)

func TestHook_OnPrePrompt_EmptyInputsProduceNoMessages(t *testing.T) {
	// A hook with both fields nil-or-empty must not inject a
	// "<context></context>" wrapper — empty content is still bytes the
	// cache has to absorb, and the prompt would be noisier without
	// information gain.
	h := &Hook{}
	out, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "hi"})
	if err != nil {
		t.Fatalf("OnPrePrompt: %v", err)
	}
	if len(out.Prepend) != 0 {
		t.Errorf("empty Hook should produce no Prepend, got %d msgs", len(out.Prepend))
	}

	// Same expectation when Soul exists but has no Stable content.
	h.Soul = &Soul{Stable: "   \n\n  "}
	out, _ = h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out.Prepend) != 0 {
		t.Errorf("whitespace-only Soul.Stable should produce no Prepend, got %v", out.Prepend)
	}
}

func TestHook_OnPrePrompt_InjectsSoulAndIndex(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	if err := p.Add(Entry{
		Name: "alpha", Tagline: "first tagline", Content: "ALPHA full content",
	}); err != nil {
		t.Fatalf("Add alpha: %v", err)
	}
	if err := p.Add(Entry{
		Name: "beta", Tagline: "second tagline", Content: "BETA full content",
	}); err != nil {
		t.Fatalf("Add beta: %v", err)
	}

	soul := &Soul{Stable: "- 倾向显式错误处理胜过 panic\n- 偏简洁代码"}
	h := &Hook{Project: p, Soul: soul}

	out, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{UserText: "anything"})
	if err != nil {
		t.Fatalf("OnPrePrompt: %v", err)
	}
	if len(out.Prepend) != 2 {
		t.Fatalf("expected 2 prepend messages (soul + index), got %d", len(out.Prepend))
	}

	// Order matters for byte-stability + readability: Soul first
	// (long-running user traits), then index (this-project specifics).
	soulMsg := out.Prepend[0]
	indexMsg := out.Prepend[1]

	if !strings.Contains(soulMsg.Content, `<context source="memory.soul">`) {
		t.Errorf("first message should wrap memory.soul, got %q", soulMsg.Content)
	}
	if !strings.Contains(soulMsg.Content, "倾向显式错误处理胜过 panic") {
		t.Errorf("soul content missing: %q", soulMsg.Content)
	}

	if !strings.Contains(indexMsg.Content, `<context source="memory.index">`) {
		t.Errorf("second message should wrap memory.index, got %q", indexMsg.Content)
	}
	// Index lists name+tagline pairs but NOT full content.
	if !strings.Contains(indexMsg.Content, "alpha: first tagline") {
		t.Errorf("index missing alpha entry: %q", indexMsg.Content)
	}
	if !strings.Contains(indexMsg.Content, "beta: second tagline") {
		t.Errorf("index missing beta entry: %q", indexMsg.Content)
	}
	if strings.Contains(indexMsg.Content, "ALPHA full content") {
		t.Errorf("index leaked full content body (should be name+tagline only): %q", indexMsg.Content)
	}
	// memory_recall hint surfaces in the wrapper text.
	if !strings.Contains(indexMsg.Content, "memory_recall") {
		t.Errorf("index should hint at memory_recall, got %q", indexMsg.Content)
	}
}

func TestHook_OnPrePrompt_StaleEntriesExcludedFromIndex(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	if err := p.Add(Entry{Name: "fresh", Tagline: "should appear"}); err != nil {
		t.Fatalf("Add fresh: %v", err)
	}
	// Plant a stale entry directly (Add() can't set Stale=true on
	// initial insert without bumping UpdatedAt; planting bypasses that).
	plantEntry(t, p, Entry{
		Name:    "buried",
		Tagline: "should NOT appear",
		Stale:   true,
	})

	h := &Hook{Project: p}
	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out.Prepend) != 1 {
		t.Fatalf("expected 1 message (index only), got %d", len(out.Prepend))
	}
	content := out.Prepend[0].Content
	if !strings.Contains(content, "fresh") {
		t.Errorf("fresh entry missing: %q", content)
	}
	if strings.Contains(content, "buried") {
		t.Errorf("stale entry should NOT appear in index: %q", content)
	}
}

// TestHook_OnPrePrompt_ByteStable verifies PRD §8 #7: identical L+M
// disk state must produce byte-identical Prepend across runs. The
// prefix cache depends on this — any non-determinism here multiplies
// cost on every subsequent turn.
func TestHook_OnPrePrompt_ByteStable(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	// Insert in an order that exercises the sort (zeta first, alpha last):
	for _, name := range []string{"zeta", "middle", "alpha"} {
		if err := p.Add(Entry{Name: name, Tagline: "t-" + name}); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}
	soul := &Soul{Stable: "- trait A\n- trait B"}

	hash := func(h *Hook) string {
		out, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
		if err != nil {
			t.Fatalf("OnPrePrompt: %v", err)
		}
		var sb strings.Builder
		for _, m := range out.Prepend {
			sb.WriteString(m.Role)
			sb.WriteString("\x1f")
			sb.WriteString(m.Content)
			sb.WriteString("\x1e")
		}
		sum := sha256.Sum256([]byte(sb.String()))
		return hex.EncodeToString(sum[:])
	}

	first := hash(&Hook{Project: p, Soul: soul})

	// Reload from disk into a NEW Project to prove stability isn't
	// just "same Go object". Same backing files → same bytes.
	p2, _ := LoadOrCreate(cwd)
	second := hash(&Hook{Project: p2, Soul: soul})

	if first != second {
		t.Errorf("PrePromptHook output not byte-stable across reloads:\n first=%s\n second=%s",
			first, second)
	}
}

func TestHook_OnSessionStart_RunsGCAndPersistsFlips(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	now := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)
	old := now.Add(-90 * day)

	plantEntry(t, p, Entry{
		Name:           "should-stale",
		Tagline:        "fades",
		CreatedAt:      old,
		UpdatedAt:      old,
		LastRecalledAt: old,
		RecallCount:    1,
	})

	h := &Hook{
		Project: p,
		Now:     func() time.Time { return now },
	}
	h.OnSessionStart(context.Background(), hooks.SessionStartEvent{})

	// Reload and confirm the flip survived.
	p2, _ := LoadOrCreate(cwd)
	got, _ := p2.Get("should-stale")
	if !got.Stale {
		t.Errorf("SessionStart should have marked entry stale, got %+v", got)
	}
}

func TestHook_OnSessionStart_NilProjectIsSafe(t *testing.T) {
	h := &Hook{}
	// Must not panic; observers can't surface errors anyway.
	h.OnSessionStart(context.Background(), hooks.SessionStartEvent{})
}
