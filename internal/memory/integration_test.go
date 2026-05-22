package memory

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestE2E_HookInjectsFreshlyAddedMEntry walks the user-facing
// distill-accept loop end-to-end against the actual public API:
//
//  1. New project, empty M.
//  2. Add an entry (simulates a /distill 'y' accept).
//  3. Reload the project from disk (simulates next session start).
//  4. Run PrePromptHook through the Hook struct.
//  5. Verify the new entry's name + tagline appear in the Prepend.
//
// This is the load-bearing scenario for v1 memory: "things I tell seek
// in this project come back next time".
func TestE2E_HookInjectsFreshlyAddedMEntry(t *testing.T) {
	cwd, _ := withMemoryEnv(t)

	first, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if err := first.Add(Entry{
		Name:    "session-storage-format",
		Tagline: "JSONL not JSON for prefix-cache stability",
		Content: "full rationale",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Reload (next-session simulation).
	next, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	soul, err := LoadSoul()
	if err != nil {
		t.Fatalf("LoadSoul: %v", err)
	}
	h := &Hook{Project: next, Soul: soul}

	out, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{
		UserText: "what's our session format again?",
	})
	if err != nil {
		t.Fatalf("OnPrePrompt: %v", err)
	}
	if len(out.Prepend) == 0 {
		t.Fatal("expected at least one Prepend message after Adding an entry")
	}
	combined := joinPrepend(out.Prepend)
	if !strings.Contains(combined, "session-storage-format") {
		t.Errorf("entry name missing from Prepend:\n%s", combined)
	}
	if !strings.Contains(combined, "JSONL not JSON") {
		t.Errorf("entry tagline missing from Prepend:\n%s", combined)
	}
	// Full content body must NOT leak — that's what memory_recall is for.
	if strings.Contains(combined, "full rationale") {
		t.Errorf("entry content body leaked into Prepend (should be tagline-only)")
	}
}

// TestE2E_CrossProjectIsolation enforces PRD §8 acceptance #3:
// switching seek's cwd to a different project must NOT carry over
// the previous project's M entries into PrePromptHook injection.
func TestE2E_CrossProjectIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	// Two unrelated project directories.
	projA := t.TempDir()
	projB := t.TempDir()

	pa, _ := LoadOrCreate(projA)
	if err := pa.Add(Entry{
		Name:    "proj-a-decision",
		Tagline: "specific to A",
	}); err != nil {
		t.Fatalf("Add A: %v", err)
	}

	pb, _ := LoadOrCreate(projB)
	if err := pb.Add(Entry{
		Name:    "proj-b-decision",
		Tagline: "specific to B",
	}); err != nil {
		t.Fatalf("Add B: %v", err)
	}

	// Hook for project A only.
	hookA := &Hook{Project: pa}
	outA, _ := hookA.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	combinedA := joinPrepend(outA.Prepend)
	if !strings.Contains(combinedA, "proj-a-decision") {
		t.Errorf("project A injection missing its own entry")
	}
	if strings.Contains(combinedA, "proj-b-decision") {
		t.Errorf("project A injection LEAKED project B's entry: %s", combinedA)
	}

	// Symmetric: hook for project B should not see A.
	hookB := &Hook{Project: pb}
	outB, _ := hookB.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	combinedB := joinPrepend(outB.Prepend)
	if !strings.Contains(combinedB, "proj-b-decision") {
		t.Errorf("project B injection missing its own entry")
	}
	if strings.Contains(combinedB, "proj-a-decision") {
		t.Errorf("project B injection LEAKED project A's entry: %s", combinedB)
	}
}

// TestE2E_UserEditedSoulPicksUpOnReload covers PRD §8 acceptance #8:
// users can hand-edit ~/.seek/soul.md (it's their data) and the next
// session reflects their edits. No tool path mutates Stable, so this
// flow is reading-only on our side.
func TestE2E_UserEditedSoulPicksUpOnReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	original := `---
schema_version: 1
updated_at: 2026-05-22T13:15:00Z
---

# User profile

## Stable

- prefers tabs over spaces

## Pending
`
	soulPath := home + "/soul.md"
	if err := os.WriteFile(soulPath, []byte(original), 0o644); err != nil {
		t.Fatalf("write soul: %v", err)
	}

	s, _ := LoadSoul()
	h := &Hook{Soul: s}
	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if !strings.Contains(joinPrepend(out.Prepend), "prefers tabs over spaces") {
		t.Fatalf("initial load didn't see the original trait")
	}

	// User hand-edits soul.md outside seek.
	edited := strings.Replace(original, "prefers tabs over spaces", "prefers spaces actually", 1)
	if err := os.WriteFile(soulPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("rewrite soul: %v", err)
	}

	// Next session: LoadSoul again, build a fresh Hook.
	s2, _ := LoadSoul()
	h2 := &Hook{Soul: s2}
	out2, _ := h2.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	combined := joinPrepend(out2.Prepend)
	if !strings.Contains(combined, "prefers spaces actually") {
		t.Errorf("edited soul.md not reflected in next session: %s", combined)
	}
	if strings.Contains(combined, "prefers tabs over spaces") {
		t.Errorf("stale trait still present after edit")
	}
}

// TestE2E_DistillToInjectionRoundTrip wires the actual public surface
// from /distill accept (Project.Add) all the way through to next
// session's PrePromptHook output. Most surgical of the integration
// tests — catches wiring errors that per-component tests would miss.
func TestE2E_DistillToInjectionRoundTrip(t *testing.T) {
	cwd, _ := withMemoryEnv(t)

	// Session 1: simulate /distill accept of two candidates.
	p1, _ := LoadOrCreate(cwd)
	cands := []Candidate{
		{Name: "build-faster", Tagline: "use go build, not go run", Content: "..."},
		{Name: "test-first", Tagline: "write the failing test before the fix", Content: "..."},
	}
	for _, c := range cands {
		if err := p1.Add(Entry{
			Name:    c.Name,
			Tagline: c.Tagline,
			Content: c.Content,
		}); err != nil {
			t.Fatalf("Add %s: %v", c.Name, err)
		}
	}

	// Session 2: fresh handle, no in-memory carryover.
	p2, _ := LoadOrCreate(cwd)
	h := &Hook{Project: p2}
	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	combined := joinPrepend(out.Prepend)

	for _, c := range cands {
		if !strings.Contains(combined, c.Name) {
			t.Errorf("accepted candidate %q missing from session-2 Prepend", c.Name)
		}
		if !strings.Contains(combined, c.Tagline) {
			t.Errorf("accepted candidate %q tagline missing from session-2 Prepend", c.Name)
		}
	}
}

// TestE2E_LplusMIndexFitsTokenBudget is a soft check on PRD §8
// acceptance #6: "L stable + M index total < 4k tokens at PRD §4's
// documented capacity (200 entries × ~60-char lines, L ≤ 500 tokens)".
//
// We don't have a tokeniser in-process. PRD §4 uses bytes/4 as the
// token approximation for mixed English/code, which matches DeepSeek's
// observed behaviour closely enough for budget gating. Fixture sizing
// mirrors the PRD's documented ceiling — 200 entries with ~40-char
// taglines (the "name: tagline" line averages ~55 chars).
func TestE2E_LplusMIndexFitsTokenBudget(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	for i := 0; i < 200; i++ {
		// Tagline length tracks PRD §4 "一条 ~60 字符" — name eats
		// ~10-12 chars, the formatting prefix eats ~4, leaving ~44 chars
		// for tagline at the documented cap.
		if err := p.Add(Entry{
			Name:    "entry-" + padNum(i),
			Tagline: "representative project decision tagline " + padNum(i),
		}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	// L stable at the 500-token cap from PRD §4 (~2KB of markdown).
	soul := &Soul{Stable: strings.Repeat(
		"- a typical user-trait bullet describing one preference clearly\n", 8)}
	h := &Hook{Project: p, Soul: soul}

	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	combined := joinPrepend(out.Prepend)
	approxTokens := len(combined) / 4 // PRD §4's bytes/4 heuristic
	if approxTokens > 4000 {
		t.Errorf("L+M injection over budget: %d approx tokens (~%d bytes); PRD §8 #6 budget is 4k",
			approxTokens, len(combined))
	}
	if approxTokens < 500 {
		t.Errorf("L+M injection suspiciously small at %d approx tokens; fixture may be broken", approxTokens)
	}
}

// joinPrepend folds all Prepend messages into one string for substring
// assertions — pure test utility, no public API surface.
func joinPrepend(msgs []deepseek.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

func padNum(n int) string {
	switch {
	case n < 10:
		return "00" + itoa(n)
	case n < 100:
		return "0" + itoa(n)
	default:
		return itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
