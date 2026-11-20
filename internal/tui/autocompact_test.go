package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Tests in this file pin the automatic /compact trigger
// (Options.AutoCompactAt → maybeAutoCompact → startCompactCmd). The
// feature rewrites the conversation history on its own, so the gates
// matter as much as the firing: every "must NOT fire" case below is a
// real way an unattended rewrite would land at the wrong moment.

// autoCompactModel builds a model at rest with a real history and a
// tracker whose LAST turn already sits at used tokens — the shape a
// stream-end leaves behind. Session+Store are wired through a tempdir
// store: auto-compact refuses to run without persistence (there would be
// no snapshot to fall back to).
func autoCompactModel(t *testing.T, at, used int) (*Model, *fakeAgent) {
	t.Helper()
	store := persistSessionTestFixture(t)
	fa := newFakeAgent()
	fa.SetMessages([]deepseek.Message{
		{Role: deepseek.RoleUser, Content: "u1"},
		{Role: deepseek.RoleAssistant, Content: "a1"},
		{Role: deepseek.RoleUser, Content: "u2"},
		{Role: deepseek.RoleAssistant, Content: "a2"},
	})
	m := testModel().
		WithAgent(fa).
		WithStore(store).
		WithAutoCompact(at).
		BuildPtr()
	m.opts.Session = session.New("deepseek-flash", t.TempDir(), "", false, false)
	m.opts.Tracker = cache.New()
	m.opts.Tracker.Record(deepseek.Usage{PromptTokens: used, CompletionTokens: 0}, m.opts.Model, 0)
	return m, fa
}

// TestAutoCompact_FiresAtThreshold is the positive control: at the
// trigger, a stream-end starts the summariser and says so in
// scrollback.
func TestAutoCompact_FiresAtThreshold(t *testing.T) {
	m, _ := autoCompactModel(t, 350_000, 350_000)
	cmd, notice := m.maybeAutoCompact(false)
	if cmd == nil {
		t.Fatalf("expected an auto-compact cmd at the threshold, got nil (notice=%q)", notice)
	}
	if !m.compacting {
		t.Error("compacting latch must be set while the summariser is in flight")
	}
	if !strings.Contains(notice, "auto-compact") {
		t.Errorf("notice should name auto-compact, got %q", notice)
	}
	// The cmd must be the real summariser call, not a placeholder.
	if msg := cmd(); msg != nil {
		if _, ok := msg.(compactDoneMsg); !ok {
			t.Errorf("auto-compact cmd produced %T, want compactDoneMsg", msg)
		}
	}
}

// TestAutoCompact_BelowThresholdDoesNotFire is the negative control —
// without it the positive test could pass on "always fires".
func TestAutoCompact_BelowThresholdDoesNotFire(t *testing.T) {
	m, _ := autoCompactModel(t, 350_000, 349_999)
	if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
		t.Error("must not compact below the trigger")
	}
	if m.compacting {
		t.Error("latch must stay clear when nothing fired")
	}
}

// TestAutoCompact_DisabledWhenUnset: nil resolver / 0 trigger = never.
func TestAutoCompact_DisabledWhenUnset(t *testing.T) {
	m, _ := autoCompactModel(t, 0, 900_000)
	if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
		t.Error("nil AutoCompactAt must mean off")
	}
}

// TestAutoCompact_NeverDuringTurnOrQueue covers the four "someone else
// is driving" states. Each would be a history swap under a turn that is
// already in flight or already dispatched.
func TestAutoCompact_NeverDuringTurnOrQueue(t *testing.T) {
	t.Run("streaming", func(t *testing.T) {
		m, _ := autoCompactModel(t, 350_000, 400_000)
		m.streaming = true
		if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
			t.Error("must not compact mid-stream")
		}
	})
	t.Run("queued message", func(t *testing.T) {
		m, _ := autoCompactModel(t, 350_000, 400_000)
		m.queuedText = "next"
		if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
			t.Error("must not compact when another turn is already queued")
		}
	})
	t.Run("pending steer", func(t *testing.T) {
		m, _ := autoCompactModel(t, 350_000, 400_000)
		m.pendingSteerText = "steer"
		if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
			t.Error("must not compact when a steer is pending")
		}
	})
	t.Run("active goal loop", func(t *testing.T) {
		m, _ := autoCompactModel(t, 350_000, 400_000)
		m.goalActive = true
		if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
			t.Error("must not compact underneath a /goal loop")
		}
	})
	t.Run("esc-cancelled turn", func(t *testing.T) {
		m, _ := autoCompactModel(t, 350_000, 400_000)
		if cmd, _ := m.maybeAutoCompact(true); cmd != nil {
			t.Error("Esc stops everything — no auto-compact after a cancel")
		}
	})
}

// TestAutoCompact_NoReentryWhileInFlight: a second stream-end must not
// start a second summariser against the same history.
func TestAutoCompact_NoReentryWhileInFlight(t *testing.T) {
	m, _ := autoCompactModel(t, 350_000, 400_000)
	if cmd, _ := m.maybeAutoCompact(false); cmd == nil {
		t.Fatal("first call should fire")
	}
	if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
		t.Error("second call must be suppressed by the compacting latch")
	}
}

// TestAutoCompact_DoesNotReTriggerOnTheFreshHistory: right after a
// successful compaction the tracker is sealed (ctx% re-bases to ~0), but
// the very next turn can push it straight back over a low trigger. The
// post-compact floor keeps one exchange from immediately re-summarising
// the three-message history the previous compaction just produced.
func TestAutoCompact_DoesNotReTriggerOnTheFreshHistory(t *testing.T) {
	m, fa := autoCompactModel(t, 350_000, 400_000)
	if cmd, _ := m.maybeAutoCompact(false); cmd == nil {
		t.Fatal("first call should fire")
	}
	// The summariser lands: history becomes the summary pair, the
	// tracker is sealed, and the floor records that size.
	m.handleCompactDone(compactDoneMsg{summary: "summary"})
	if m.compacting {
		t.Fatal("latch must clear on success")
	}
	if got := len(fa.Messages()); got != 2 {
		t.Fatalf("post-compact history = %d messages, want 2", got)
	}
	if got := m.contextUsed(); got != 0 {
		t.Fatalf("ctx%% must re-base to 0 after the seal, got %d", got)
	}

	// One exchange, and it already reads over the trigger again (a low
	// trigger makes this the normal case, not a corner).
	fa.SetMessages(append(fa.Messages(),
		deepseek.Message{Role: deepseek.RoleUser, Content: "u3"},
		deepseek.Message{Role: deepseek.RoleAssistant, Content: "a3"},
	))
	m.opts.Tracker.Record(deepseek.Usage{PromptTokens: 400_000}, m.opts.Model, 0)
	if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
		t.Error("must not re-compact one exchange after the last compaction")
	}

	// A second exchange justifies it.
	fa.SetMessages(append(fa.Messages(),
		deepseek.Message{Role: deepseek.RoleUser, Content: "u4"},
		deepseek.Message{Role: deepseek.RoleAssistant, Content: "a4"},
	))
	if cmd, _ := m.maybeAutoCompact(false); cmd == nil {
		t.Error("expected a fresh compaction once the history grew past the floor")
	}
}

// TestAutoCompact_SkipsShortHistory: ≤2 messages is [system, maybe one
// user msg] — nothing to summarise, so the trigger stays quiet even when
// the tracker says the context is large (a resumed session with a
// fresh-but-large measurement).
func TestAutoCompact_SkipsShortHistory(t *testing.T) {
	m, fa := autoCompactModel(t, 1000, 500_000)
	fa.SetMessages([]deepseek.Message{{Role: deepseek.RoleUser, Content: "only"}})

	if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
		t.Error("must not compact a two-message history")
	}
}

// TestAutoCompact_RefusesWithoutPersistence pins the --no-save guard:
// without a Session+Store there is no fork-snapshot, so an automatic
// compaction would destroy the only copy of the history. Manual
// /compact stays available there — the user asked for it.
func TestAutoCompact_RefusesWithoutPersistence(t *testing.T) {
	m, _ := autoCompactModel(t, 350_000, 400_000)
	m.opts.Session = nil
	if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
		t.Error("must not auto-compact when the history cannot be snapshotted")
	}

	m.opts.Session = session.New("deepseek-flash", t.TempDir(), "", false, false)
	m.opts.Store = nil
	if cmd, _ := m.maybeAutoCompact(false); cmd != nil {
		t.Error("must not auto-compact with no store to write the snapshot to")
	}
}

// TestCompactDone_ClearsLatch: the failure path must clear the latch
// too, or one failed compaction would disable the feature for the rest
// of the session.
func TestCompactDone_ClearsLatch(t *testing.T) {
	m, _ := autoCompactModel(t, 350_000, 400_000)
	m.compacting = true
	m.handleCompactDone(compactDoneMsg{err: errors.New("summariser 400")})
	if m.compacting {
		t.Error("failure path must clear the compacting latch")
	}

	m.compacting = true
	m.handleCompactDone(compactDoneMsg{summary: "S", usage: deepseek.Usage{}})
	if m.compacting {
		t.Error("success path must clear the compacting latch")
	}
}

// TestCmdCompact_RefusesWhileCompacting: a manual /compact during an
// automatic one must not start a second summariser.
func TestCmdCompact_RefusesWhileCompacting(t *testing.T) {
	m, _ := autoCompactModel(t, 350_000, 400_000)
	m.compacting = true
	res := cmdCompact(m, "")
	if res.extra != nil {
		t.Error("manual /compact must not fire while a compaction is in flight")
	}
	if !strings.Contains(res.text, "already compacting") {
		t.Errorf("expected an 'already compacting' notice, got %q", res.text)
	}
}

// TestStatusBar_AutoCompactMovesEscalation: the ctx% segment must go
// red at the configured trigger (and say "auto-compact", not "type
// /compact"), not at the compiled 75%.
func TestStatusBar_AutoCompactMovesEscalation(t *testing.T) {
	t.Parallel()

	m := "deepseek-flash" // 1M window
	// 400k on a 1M window is 40% — below the compiled 60% warn line, but
	// past a 350k trigger.
	snap := StatusSnapshot{
		Model:         m,
		LastUsage:     deepseek.Usage{PromptTokens: 400_000},
		AutoCompactAt: 350_000,
	}
	out := stripANSI(formatBudget(snap))
	if !strings.Contains(out, "auto-compact") {
		t.Errorf("critical ctx at the trigger should say auto-compact, got %q", out)
	}
	if strings.Contains(out, "/compact soon") {
		t.Errorf("with auto-compact on, the manual nudge is redundant: %q", out)
	}

	// Below 0.8 × trigger → still quiet, so the tint doesn't sit red for
	// most of the session.
	snap.LastUsage = deepseek.Usage{PromptTokens: 200_000}
	if got := stripANSI(formatBudget(snap)); strings.Contains(got, "!") {
		t.Errorf("well below the trigger should stay muted, got %q", got)
	}

	// Unset trigger falls back to the compiled fractions unchanged.
	snap.AutoCompactAt = 0
	snap.LastUsage = deepseek.Usage{PromptTokens: 800_000} // 80% > 75%
	if got := stripANSI(formatBudget(snap)); !strings.Contains(got, "/compact soon") {
		t.Errorf("without auto-compact the manual nudge must return, got %q", got)
	}
}

// TestStatusBar_AutoCompactDoesNotChangeFractionPct pins that the
// displayed percentage is still measured against the MODEL window, not
// the trigger: "ctx 40%" with a 350k trigger on a 1M model is the
// honest reading, and the trigger only decides the colour.
func TestStatusBar_AutoCompactDoesNotChangeFractionPct(t *testing.T) {
	t.Parallel()

	snap := StatusSnapshot{
		Model:         "deepseek-flash",
		LastUsage:     deepseek.Usage{PromptTokens: 400_000},
		AutoCompactAt: 350_000,
	}
	out := stripANSI(formatBudget(snap))
	if !strings.Contains(out, "ctx 40%") {
		t.Errorf("percentage must stay window-relative, got %q", out)
	}
	if got := budget.Fraction("deepseek-flash", 400_000); got < 0.39 || got > 0.41 {
		t.Errorf("fraction drifted: %v", got)
	}
}
