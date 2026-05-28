package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
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

// ----- M5.9: snapshot + delta tests -----

// TestHook_OnPrePrompt_SecondCallEmptyWhenNoChanges verifies that turn 2+
// OnPrePrompt returns no Prepend when no entries were added mid-session.
func TestHook_OnPrePrompt_SecondCallEmptyWhenNoChanges(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "existing", Tagline: "stays"})

	h := &Hook{Project: p}

	// Turn 1: inject snapshot → gets index.
	out1, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(out1.Prepend) == 0 {
		t.Fatal("first call should inject snapshot (index)")
	}
	if !h.snapshotInjected {
		t.Fatal("snapshotInjected should be true after first call")
	}

	// Turn 2: no changes → empty Prepend.
	out2, err := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(out2.Prepend) != 0 {
		t.Errorf("second call with no changes should produce 0 Prepend, got %d messages: %v",
			len(out2.Prepend), out2.Prepend)
	}
}

// TestHook_OnPrePrompt_DeltaAfterNewEntry verifies that a new entry added
// after the snapshot produces a <context type="memory.delta"> on the next
// OnPrePrompt call.
func TestHook_OnPrePrompt_DeltaAfterNewEntry(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "alpha", Tagline: "original"})

	h := &Hook{Project: p}

	// Turn 1: inject snapshot.
	out1, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out1.Prepend) != 1 {
		t.Fatalf("expected 1 prepend (index), got %d", len(out1.Prepend))
	}
	if !strings.Contains(out1.Prepend[0].Content, `<context source="memory.index">`) {
		t.Fatalf("turn 1 should inject memory.index, got %q", out1.Prepend[0].Content)
	}

	// Add a new entry mid-session.
	_ = p.Add(Entry{Name: "beta", Tagline: "added later"})

	// Turn 2: should get a delta.
	out2, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out2.Prepend) != 1 {
		t.Fatalf("expected 1 delta message, got %d", len(out2.Prepend))
	}
	delta := out2.Prepend[0]
	if !strings.Contains(delta.Content, `<context source="memory.delta">`) {
		t.Errorf("should wrap memory.delta, got %q", delta.Content)
	}
	if !strings.Contains(delta.Content, "beta: added later") {
		t.Errorf("delta should mention new entry, got %q", delta.Content)
	}
	if strings.Contains(delta.Content, "alpha") {
		t.Errorf("delta should NOT mention original snapshot entry, got %q", delta.Content)
	}
}

// TestHook_OnPrePrompt_DeltaOnlyNewEntries verifies that delta only
// lists additions since snapshot, not pre-existing entries.
func TestHook_OnPrePrompt_DeltaOnlyNewEntries(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "keep", Tagline: "in snapshot"})

	h := &Hook{Project: p}

	// Turn 1.
	_, _ = h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})

	// Add two new entries.
	_ = p.Add(Entry{Name: "new1", Tagline: "first addition"})
	_ = p.Add(Entry{Name: "new2", Tagline: "second addition"})

	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out.Prepend) != 1 {
		t.Fatalf("expected 1 delta, got %d", len(out.Prepend))
	}
	content := out.Prepend[0].Content
	if !strings.Contains(content, "new1") || !strings.Contains(content, "new2") {
		t.Errorf("delta should include both new entries: %q", content)
	}
	if strings.Contains(content, "keep") {
		t.Errorf("delta should NOT include 'keep' (was in snapshot): %q", content)
	}
}

// TestHook_OnPrePrompt_DeltaByteStable verifies that two consecutive
// delta calls with the same set of additions produce identical bytes.
func TestHook_OnPrePrompt_DeltaByteStable(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "old", Tagline: "from snapshot"})

	hashDelta := func(h *Hook) string {
		out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
		if len(out.Prepend) == 0 {
			return ""
		}
		m := sha256.Sum256([]byte(out.Prepend[0].Content))
		return hex.EncodeToString(m[:])
	}

	h := &Hook{Project: p}
	_, _ = h.OnPrePrompt(context.Background(), hooks.PrePromptIn{}) // snapshot

	// Add entries in non-alphabetical order to exercise sort.
	_ = p.Add(Entry{Name: "zeta", Tagline: "last"})
	_ = p.Add(Entry{Name: "alpha", Tagline: "first"})

	first := hashDelta(h)
	second := hashDelta(h)

	if first == "" {
		t.Fatal("expected non-empty delta hash")
	}
	if first != second {
		t.Errorf("delta not byte-stable across consecutive calls:\n first=%s\n second=%s", first, second)
	}
}

// ----- M5.11: auto_sourced flexibility tests -----

// TestHook_OnPrePrompt_AutoSourcedPrefixInIndex verifies that auto_sourced
// entries get an [auto] prefix in the M-index, while confirmed entries do not.
func TestHook_OnPrePrompt_AutoSourcedPrefixInIndex(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "confirmed", Tagline: "user reviewed", AutoSourced: false})
	// Plant an auto_sourced entry directly.
	plantEntry(t, p, Entry{
		Name:        "auto-entry",
		Tagline:     "model observed",
		AutoSourced: true,
	})

	h := &Hook{Project: p}
	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out.Prepend) != 1 {
		t.Fatalf("expected 1 prepend (index), got %d", len(out.Prepend))
	}
	content := out.Prepend[0].Content

	if !strings.Contains(content, "[auto] auto-entry: model observed") {
		t.Errorf("auto_sourced entry should have [auto] prefix, got:\n%s", content)
	}
	if strings.Contains(content, "[auto] confirmed:") {
		t.Errorf("confirmed entry should NOT have [auto] prefix, got:\n%s", content)
	}
}

// TestHook_OnPrePrompt_AutoSourcedPrefixInDelta verifies that auto_sourced
// entries added mid-session get [auto] prefix in the delta.
func TestHook_OnPrePrompt_AutoSourcedPrefixInDelta(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "existing", Tagline: "from snapshot"})

	h := &Hook{Project: p}
	_, _ = h.OnPrePrompt(context.Background(), hooks.PrePromptIn{}) // snapshot

	// Add an auto_sourced entry mid-session.
	_ = p.Add(Entry{Name: "new-auto", Tagline: "fresh observe", AutoSourced: true})

	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out.Prepend) != 1 {
		t.Fatalf("expected 1 delta, got %d", len(out.Prepend))
	}
	content := out.Prepend[0].Content
	if !strings.Contains(content, "[auto] new-auto: fresh observe") {
		t.Errorf("delta should have [auto] prefix for auto_sourced entry, got:\n%s", content)
	}
}

// TestHook_OnPrePrompt_ByteStableWithAutoSourced verifies that the snapshot
// is byte-stable across reloads when entries have AutoSourced=true —
// the [auto] prefix must be deterministic, not a source of drift.
func TestHook_OnPrePrompt_ByteStableWithAutoSourced(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	// Mix of auto_sourced and confirmed entries.
	_ = p.Add(Entry{Name: "confirmed", Tagline: "t-confirmed", AutoSourced: false})
	plantEntry(t, p, Entry{
		Name:        "auto-one",
		Tagline:     "t-auto-one",
		AutoSourced: true,
	})
	plantEntry(t, p, Entry{
		Name:        "auto-two",
		Tagline:     "t-auto-two",
		AutoSourced: true,
	})

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

	first := hash(&Hook{Project: p})
	p2, _ := LoadOrCreate(cwd)
	second := hash(&Hook{Project: p2})

	if first != second {
		t.Errorf("snapshot not byte-stable with auto_sourced entries:\n first=%s\n second=%s", first, second)
	}
}

// TestHook_OnPrePrompt_AutoSourcedFlipChangesHash verifies a deliberate
// cross-session invariant: when an auto_sourced entry is promoted to
// confirmed between sessions, the snapshot hash changes (because the
// [auto] prefix is removed). This is expected and documents the design.
func TestHook_OnPrePrompt_AutoSourcedFlipChangesHash(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)

	plantEntry(t, p, Entry{
		Name:        "will-promote",
		Tagline:     "t",
		AutoSourced: true,
	})

	hash := func(p *Project) string {
		h := &Hook{Project: p}
		out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
		var sb strings.Builder
		for _, m := range out.Prepend {
			sb.WriteString(m.Content)
		}
		sum := sha256.Sum256([]byte(sb.String()))
		return hex.EncodeToString(sum[:])
	}

	autoHash := hash(p)

	// Simulate user y-confirming the entry between sessions.
	p3, _ := LoadOrCreate(cwd)
	_ = p3.Add(Entry{Name: "will-promote", Tagline: "t", AutoSourced: false})

	confirmedHash := hash(p3)

	if autoHash == confirmedHash {
		t.Error("snapshot hash should differ when auto_sourced status changes between sessions")
		return
	}
	// Verify the source of the difference is the [auto] prefix:
	// the auto_sourced snapshot should contain [auto], the confirmed
	// should not.
	hAuto := &Hook{Project: p}
	outAuto, _ := hAuto.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if !strings.Contains(outAuto.Prepend[0].Content, "[auto] will-promote") {
		t.Error("auto_sourced snapshot should contain [auto] will-promote")
	}
	hConfirmed := &Hook{Project: p3}
	outConfirmed, _ := hConfirmed.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if strings.Contains(outConfirmed.Prepend[0].Content, "[auto] will-promote") {
		t.Error("confirmed snapshot should NOT contain [auto] will-promote")
	}
}

// TestHook_OnPrePrompt_DeltaByteStableWithAutoSourced verifies that delta
// output is byte-stable when the added entry is auto_sourced — the [auto]
// prefix must not introduce non-determinism.
func TestHook_OnPrePrompt_DeltaByteStableWithAutoSourced(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "snap", Tagline: "in snapshot"})

	hashDelta := func(h *Hook) string {
		out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
		if len(out.Prepend) == 0 {
			return ""
		}
		m := sha256.Sum256([]byte(out.Prepend[0].Content))
		return hex.EncodeToString(m[:])
	}

	h := &Hook{Project: p}
	_, _ = h.OnPrePrompt(context.Background(), hooks.PrePromptIn{}) // snapshot

	// Add auto_sourced entries in non-alphabetical order.
	_ = p.Add(Entry{Name: "z", Tagline: "last-alpha", AutoSourced: true})
	_ = p.Add(Entry{Name: "a", Tagline: "first-alpha", AutoSourced: true})

	first := hashDelta(h)
	second := hashDelta(h)

	if first == "" {
		t.Fatal("expected non-empty delta hash with auto_sourced entries")
	}
	if first != second {
		t.Errorf("delta with auto_sourced entries not byte-stable:\n first=%s\n second=%s", first, second)
	}
}

// ----- M5.13: observe stats feedback tests -----

// TestOnSessionEnd_WritesObserveStats verifies that OnSessionEnd persists
// observe-stats.json with the session's accept count.
func TestOnSessionEnd_WritesObserveStats(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "existing", Tagline: "t"})

	h := &Hook{Project: p}
	h.observeCount.Store(5)
	h.observeAcceptCt.Store(3)
	h.OnSessionEnd(context.Background(), hooks.SessionEndEvent{})

	// Verify the file was written.
	data, err := os.ReadFile(filepath.Join(p.Dir, observeStatsFile))
	if err != nil {
		t.Fatalf("read observe-stats.json: %v", err)
	}
	if !strings.Contains(string(data), `"launched":5`) {
		t.Errorf("expected launched=5, got %s", data)
	}
	if !strings.Contains(string(data), `"saved":3`) {
		t.Errorf("expected saved=3, got %s", data)
	}
}

// TestOnPrePrompt_InjectsObserveStats verifies that the M-index snapshot
// includes observe stats when a previous session's stats file exists.
func TestOnPrePrompt_InjectsObserveStats(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "entry", Tagline: "t"})

	// Plant observe-stats.json from a previous session.
	_ = atomicWrite(filepath.Join(p.Dir, observeStatsFile),
		[]byte(`{"launched":4,"saved":2}`))

	h := &Hook{Project: p}
	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out.Prepend) != 1 {
		t.Fatalf("expected 1 prepend (index), got %d", len(out.Prepend))
	}
	content := out.Prepend[0].Content
	if !strings.Contains(content, "memory_observe") {
		t.Errorf("M-index should contain observe stats, got:\n%s", content)
	}
	if !strings.Contains(content, "4 次调用") {
		t.Errorf("stats should mention launched count, got:\n%s", content)
	}
	if !strings.Contains(content, "2 条保存") {
		t.Errorf("stats should mention saved count, got:\n%s", content)
	}
}

// TestOnPrePrompt_NoStatsWhenNoFile verifies that the M-index omits
// observe stats when no previous stats file exists (normal first run).
func TestOnPrePrompt_NoStatsWhenNoFile(t *testing.T) {
	cwd, _ := withMemoryEnv(t)
	p, _ := LoadOrCreate(cwd)
	_ = p.Add(Entry{Name: "entry", Tagline: "t"})

	h := &Hook{Project: p}
	out, _ := h.OnPrePrompt(context.Background(), hooks.PrePromptIn{})
	if len(out.Prepend) != 1 {
		t.Fatalf("expected 1 prepend (index), got %d", len(out.Prepend))
	}
	content := out.Prepend[0].Content
	if strings.Contains(content, "memory_observe") {
		t.Errorf("M-index should NOT contain observe stats when no stats file exists, got:\n%s", content)
	}
}
