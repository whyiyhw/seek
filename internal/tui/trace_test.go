package tui

// Trace wiring tests: every keystroke path that writes a ui-trace row.
//
// What these pin, and why each matters:
//
//  1. The row is written for each interaction kind (inject / queue /
//     steer / withdraw / cancel / compact / goal). Without per-kind
//     coverage, deleting one call site is invisible — the trace still
//     "works", it just silently stops counting that mechanism.
//  2. Chars carries the text length, not the text. The trace's stated
//     contract is "counting, not reading"; a future edit that pastes
//     content in would violate it silently.
//  3. SID comes from the live session, so a /branch (which swaps
//     opts.Session) is attributed to the NEW session — rows from a
//     forked session must not be credited to the parent.
//  4. A nil UITrace disables everything without touching the keystroke
//     path (the --no-save and test default).
//  5. The turn counter is the same one the status bar shows.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/whyiyhw/seek/internal/goal"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/uitrace"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// tracedModel builds a streaming Model wired to a fresh trace file and
// returns it plus the path to read back.
func tracedModel(t *testing.T, withAgent bool, injectAccepted bool) (Model, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ui-trace.jsonl")

	b := testModel().WithCustomState(func(m *Model) {
		m.opts.UITrace = uitrace.New(path)
		m.opts.Session = &session.Session{ID: "sid-1"}
		m.opts.Ctx = context.Background()
		m.streaming = true
		m.cancelStream = func() {}
	})
	if withAgent {
		b = b.WithAgent(newInjectableAgent(injectAccepted))
	} else {
		// A plain fakeAgent: no midTurnInjector, so every interaction
		// takes the pre-injection fallback path — which is exactly what
		// the queue/steer/withdraw/cancel rows describe.
		b = b.WithAgent(newFakeAgent())
	}
	return b.Build(), path
}

// readTrace is a small helper so each test asserts on parsed rows.
func readTrace(t *testing.T, path string) []uitrace.Event {
	t.Helper()
	rows, err := uitrace.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return rows
}

func TestTrace_InjectRow(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, true, true)
	m.input.SetValue("use the other file")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = out

	rows := readTrace(t, path)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.Event != uitrace.EventInject {
		t.Errorf("event = %q, want inject", r.Event)
	}
	if r.Chars != len("use the other file") {
		t.Errorf("chars = %d, want %d", r.Chars, len("use the other file"))
	}
	if r.SID != "sid-1" {
		t.Errorf("sid = %q, want sid-1", r.SID)
	}
	// The text itself must never reach the row — len only.
	if r.Detail != "" {
		t.Errorf("detail = %q, want empty (the trace counts, it does not read)", r.Detail)
	}
}

func TestTrace_QueueRowOnFallback(t *testing.T) {
	t.Parallel()
	// Agent rejects the injection → queuedText path → one queue row.
	m, path := tracedModel(t, true, false)
	m.input.SetValue("later please")

	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventQueue {
		t.Fatalf("rows = %+v, want one queue row", rows)
	}
	if rows[0].Chars != len("later please") {
		t.Errorf("chars = %d", rows[0].Chars)
	}
}

func TestTrace_SteerRow(t *testing.T) {
	t.Parallel()
	// No injectable agent at all → Alt+Enter takes the steer fallback.
	m, path := tracedModel(t, false, false)
	m.input.SetValue("stop, do X")

	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventSteer {
		t.Fatalf("rows = %+v, want one steer row", rows)
	}
}

func TestTrace_WithdrawRow(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)
	m.queuedText = "changed my mind"
	// Empty Enter withdraws the queue.
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventWithdraw {
		t.Fatalf("rows = %+v, want one withdraw row", rows)
	}
	if rows[0].Detail != "queue" {
		t.Errorf("detail = %q, want queue", rows[0].Detail)
	}
	// The withdraw row carries the withdrawn text's length, which is how
	// a report measures "how much text users take back".
	if rows[0].Chars != len("changed my mind") {
		t.Errorf("chars = %d, want %d", rows[0].Chars, len("changed my mind"))
	}
}

func TestTrace_WithdrawSteerRowNamesTheKind(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)
	m.pendingSteerText = "no wait"

	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Detail != "steer" {
		t.Fatalf("rows = %+v, want one withdraw row with detail=steer", rows)
	}
}

func TestTrace_CancelRow(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)

	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventCancel {
		t.Fatalf("rows = %+v, want one cancel row", rows)
	}
}

func TestTrace_DeliverRowsSeparateQueueFromSteer(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)
	// Two deliveries through the real stream-end dispatch: one steer,
	// one queue. The Detail field is what lets a report say which
	// mechanism actually reached the model.
	m.pendingSteerText = "steered text"
	m.handleStreamEnd(streamEndMsg{})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventDeliver || rows[0].Detail != "steer" {
		t.Fatalf("rows = %+v, want one deliver row with detail=steer", rows)
	}
}

func TestTrace_CompactRowCarriesTrigger(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)
	// handleCompactDone needs a non-nil agent for the Reset path.
	m.opts.Agent = newFakeAgent()

	m.handleCompactDone(compactDoneMsg{
		summary: "summary text",
		usage:   deepseek.Usage{PromptTokens: 1234},
		trigger: "auto",
	})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventCompact {
		t.Fatalf("rows = %+v, want one compact row", rows)
	}
	if rows[0].Detail != "auto" {
		t.Errorf("detail = %q, want the trigger (auto)", rows[0].Detail)
	}
	// Tokens carries the pre-compact prompt-token count — the magnitude a
	// budget pass tunes on. Chars stays 0 (no text involved).
	if rows[0].Tokens != 1234 {
		t.Errorf("tokens = %d, want the prompt-token count 1234", rows[0].Tokens)
	}
	if rows[0].Chars != 0 {
		t.Errorf("chars = %d, want 0 (a compact row measures context, not text)", rows[0].Chars)
	}
}

// TestTrace_CompactRowCarriesDuration: dur_ms is how long the user waited
// for the summarsiser. It used to be hard-coded 0, which left "is compact
// slow?" unanswerable from the ledger — the exact question a budget pass
// asks. Asserted from an explicit elapsed value rather than a sleep: a
// real-time span is exactly 0 across sub-tick gaps on Windows.
func TestTrace_CompactRowCarriesDuration(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)
	m.opts.Agent = newFakeAgent()

	m.handleCompactDone(compactDoneMsg{
		summary: "summary text",
		usage:   deepseek.Usage{PromptTokens: 1234},
		trigger: "manual",
		elapsed: 2500 * time.Millisecond,
	})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventCompact {
		t.Fatalf("rows = %+v, want one compact row", rows)
	}
	if rows[0].DurMS != 2500 {
		t.Errorf("dur_ms = %d, want 2500 (the measured span)", rows[0].DurMS)
	}
}

// TestCompactDone_FailureWritesNoCompactRow: a failed summariser is not a
// compaction. The row's tokens carry the pre-compact context size and its
// duration carries the wait — recording either on a 400 would inflate the
// "how often does history get rewritten" count with attempts.
func TestCompactDone_FailureWritesNoCompactRow(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)
	m.opts.Agent = newFakeAgent()

	m.handleCompactDone(compactDoneMsg{
		usage:   deepseek.Usage{PromptTokens: 999},
		trigger: "auto",
		elapsed: 30 * time.Second,
		err:     errors.New("summariser 400"),
	})

	if rows := readTrace(t, path); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none (a failure is not a compaction)", rows)
	}
}

// TestStartCompactCmd_MeasuresElapsedFromTheCmdRun: the span must start
// inside the tea.Cmd (that is when the hook + summariser actually run),
// not when the caller built it — otherwise the number includes UI idle
// time. clockFn pins both ends, so no sleep is involved and the assertion
// is exact under -race and on every platform.
func TestStartCompactCmd_MeasuresElapsedFromTheCmdRun(t *testing.T) {
	t.Parallel()
	fa := newFakeAgent()
	m := emptyModel()
	m.opts.Agent = fa

	// First call = the Cmd's start, second = its end. Anything the
	// production path would do between them is irrelevant here.
	calls := 0
	base := time.Date(2026, time.November, 24, 12, 0, 0, 0, time.UTC)
	m.clockFn = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(3 * time.Second)
	}

	msg, ok := startCompactCmd(m, "manual")().(compactDoneMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want compactDoneMsg", msg)
	}
	if msg.elapsed != 3*time.Second {
		t.Errorf("elapsed = %v, want 3s", msg.elapsed)
	}
	if msg.trigger != "manual" {
		t.Errorf("trigger = %q, want manual", msg.trigger)
	}
}

func TestTrace_FailureDoesNotBreakKeystroke(t *testing.T) {
	t.Parallel()
	// Trace path points at a directory, so OpenFile fails. The keystroke
	// must still work — a broken trace can never break typing.
	dir := t.TempDir()
	b := testModel().WithCustomState(func(m *Model) {
		m.opts.UITrace = uitrace.New(dir) // a directory, not a file
		m.opts.Session = &session.Session{ID: "sid-1"}
		m.streaming = true
		m.cancelStream = func() {}
	}).WithAgent(newInjectableAgent(true))
	m := b.Build()
	m.input.SetValue("hello")

	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := out.(Model)

	if got.injectedPreview != "hello" {
		t.Errorf("keystroke path must survive a failing trace write, preview = %q", got.injectedPreview)
	}
}

func TestTrace_NilWriterNoOps(t *testing.T) {
	t.Parallel()
	// The default in tests: UITrace nil. Nothing panics, nothing written.
	m := testModel().Streaming().Build()
	if m.opts.UITrace != nil {
		t.Fatal("testModel default should have no trace writer")
	}
	m.traceEvent(uitrace.EventInject, "", 3) // must not panic
	out, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	_ = out
}

func TestTrace_GoalVerdictRow(t *testing.T) {
	t.Parallel()
	m, path := tracedModel(t, false, false)
	// handleGoalVerdict drops verdicts while a stream is in flight (a
	// stale verdict from a round the user already took over). Clear the
	// flag so the handler proceeds to the met → stop path.
	m.streaming = false
	m.cancelStream = nil
	m.goalActive = true
	m.goalCond = "make tests pass"
	m.goalTurns = 3
	m.goalStart = time.Now()

	// Met → stop → one goal row with detail=met.
	m.handleGoalVerdict(goalVerdictMsg{v: goal.Verdict{Met: true, Reason: "done"}})

	rows := readTrace(t, path)
	if len(rows) != 1 || rows[0].Event != uitrace.EventGoal {
		t.Fatalf("rows = %+v, want one goal row", rows)
	}
	if rows[0].Detail != "met" {
		t.Errorf("detail = %q, want met", rows[0].Detail)
	}
	if rows[0].Chars != 3 {
		t.Errorf("chars = %d, want the turn count 3", rows[0].Chars)
	}
}
