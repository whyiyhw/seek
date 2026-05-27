package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ----------------------------------------------------------------------
// fakeAgent — a test-only AgentClient.
//
// Production code talks to *agent.Agent which spins up an HTTP client,
// SSE reader, and the full event loop. For TUI tests we don't want any
// of that — we want a knob to push events into the channel that
// waitForAgentEvent reads from, and assertions on what the TUI asked
// the agent to do (SetModel/SetEffort/Reset/etc).
//
// Usage:
//
//	fa := newFakeAgent()
//	m := testModel().WithAgent(fa).Streaming().Build()
//	// model is now mid-stream; push an event to drive Update
//	fa.PushEvent(agent.MessageDelta{Delta: "hello"})
//	fa.Close() // simulates stream end (channel close → streamEndMsg)
//
// fakeAgent is goroutine-safe so tests can push events from a separate
// goroutine if they want to simulate async streaming. Most tests can
// stay single-threaded — PushEvent on a buffered channel doesn't block
// until the buffer fills (capacity 64, more than any realistic test
// turn).
// ----------------------------------------------------------------------

type fakeAgent struct {
	mu sync.Mutex

	// events is the channel that Prompt returns. Tests push via
	// PushEvent; close via Close.
	events chan agent.Event

	// messages is what Messages() returns. Tests can preset history
	// or assert on what Reset writes.
	messages []deepseek.Message

	// summariseResult is what Summarise returns. Default: empty.
	summariseSummary string
	summariseUsage   deepseek.Usage
	summariseErr     error

	// Recorded calls for assertion.
	PromptCalls    []string
	ResetCalls     [][]deepseek.Message
	SetModelCalls  []string
	SetEffortCalls []string
	SetLangCalls   []string
}

func newFakeAgent() *fakeAgent {
	// 64 buffered: enough headroom for any realistic test turn
	// (typically 3-10 events). Tests that overflow this should
	// PushEvent in a goroutine + use a synchronisation primitive,
	// not raise the cap.
	return &fakeAgent{events: make(chan agent.Event, 64)}
}

// Compile-time check: fakeAgent satisfies AgentClient.
var _ AgentClient = (*fakeAgent)(nil)

func (a *fakeAgent) Prompt(ctx context.Context, text string) <-chan agent.Event {
	a.mu.Lock()
	a.PromptCalls = append(a.PromptCalls, text)
	a.mu.Unlock()
	return a.events
}

func (a *fakeAgent) Messages() []deepseek.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Return a copy so callers can't mutate our state.
	out := make([]deepseek.Message, len(a.messages))
	copy(out, a.messages)
	return out
}

func (a *fakeAgent) Reset(history []deepseek.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ResetCalls = append(a.ResetCalls, history)
	a.messages = history
}

func (a *fakeAgent) Summarise(ctx context.Context) (string, deepseek.Usage, error) {
	return a.summariseSummary, a.summariseUsage, a.summariseErr
}

func (a *fakeAgent) SetModel(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SetModelCalls = append(a.SetModelCalls, s)
}

func (a *fakeAgent) SetEffort(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SetEffortCalls = append(a.SetEffortCalls, s)
}

func (a *fakeAgent) SetLang(s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.SetLangCalls = append(a.SetLangCalls, s)
}

// PushEvent enqueues an agent event for the next waitForAgentEvent to
// receive. Non-blocking up to the channel's capacity (64).
func (a *fakeAgent) PushEvent(e agent.Event) {
	a.events <- e
}

// PushEvents is a tiny convenience for queuing a turn's worth of events
// in one call.
func (a *fakeAgent) PushEvents(events ...agent.Event) {
	for _, e := range events {
		a.events <- e
	}
}

// Close shuts the event channel — bubbletea sees this as streamEndMsg.
// Idempotent guard so tests can defer Close() without panicking when
// they also call it explicitly.
func (a *fakeAgent) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.events != nil {
		close(a.events)
		a.events = nil
	}
}

// SetMessages presets the history Messages() returns. Used to simulate
// a resumed session.
func (a *fakeAgent) SetMessages(msgs []deepseek.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = msgs
}

// ----------------------------------------------------------------------
// testModel — a fluent builder for tui.Model in tests.
//
// Every test that touches more than counters used to set 5-10 fields
// by hand: width, height, ready, opts.Tracker, opts.Model,
// opts.Agent, sometimes m.streaming, m.promptHistory,
// etc. The builder gives sensible defaults (80×40, ready=true,
// tracker wired, no agent) and a knob for each common axis.
//
// Build() returns a value (Model), not a pointer, because the TUI
// Model is a value type — Update/handleKey take it by value. Tests
// that need a pointer (cmdNew etc) can take &m after Build().
// ----------------------------------------------------------------------

type testModelBuilder struct {
	opts        Options
	mutators    []func(*Model)
	skipReady   bool
	skipDefSize bool
}

// testModel starts a builder with sensible defaults:
//   - Tracker = cache.New()
//   - Model   = "deepseek-chat"
//   - width=80, height=40, ready=true (post-WindowSizeMsg state)
//   - no Agent, no Session, no Store (caller adds when needed)
//
// All defaults are overridable via the With* methods.
func testModel() *testModelBuilder {
	return &testModelBuilder{
		opts: Options{
			Tracker: cache.New(),
			Model:   "deepseek-chat",
		},
	}
}

func (b *testModelBuilder) WithAgent(a AgentClient) *testModelBuilder {
	b.opts.Agent = a
	return b
}

func (b *testModelBuilder) WithModel(name string) *testModelBuilder {
	b.opts.Model = name
	return b
}

func (b *testModelBuilder) WithCWD(cwd string) *testModelBuilder {
	b.opts.CWD = cwd
	return b
}

func (b *testModelBuilder) WithSession(s *session.Session) *testModelBuilder {
	b.opts.Session = s
	return b
}

func (b *testModelBuilder) WithStore(s *session.Store) *testModelBuilder {
	b.opts.Store = s
	return b
}

func (b *testModelBuilder) WithYolo() *testModelBuilder {
	b.opts.Yolo = true
	return b
}

func (b *testModelBuilder) WithPlan() *testModelBuilder {
	b.opts.Plan = true
	return b
}

// WithSize overrides the default 80×40 dimensions. Pass (0, 0) to
// simulate the pre-WindowSizeMsg state (combined with SkipReady).
func (b *testModelBuilder) WithSize(w, h int) *testModelBuilder {
	b.skipDefSize = true
	b.mutators = append(b.mutators, func(m *Model) {
		m.width = w
		m.height = h
	})
	return b
}

// SkipReady leaves m.ready=false (pre-WindowSizeMsg). Use when testing
// the "starting…" placeholder path.
func (b *testModelBuilder) SkipReady() *testModelBuilder {
	b.skipReady = true
	return b
}

// Streaming flips m.streaming=true + a non-zero streamStartTime so
// View() doesn't render the welcome banner and `time.Since` is sane.
// Tests that want to drive a real stream should pair this with
// WithAgent(fakeAgent) and PushEvent.
func (b *testModelBuilder) Streaming() *testModelBuilder {
	b.mutators = append(b.mutators, func(m *Model) {
		m.streaming = true
		m.streamStartTime = time.Now()
	})
	return b
}

// WithPromptHistory presets the up-arrow recall list. Side effect:
// hides the welcome banner (gate is `turns==0 && len(promptHistory)==0`).
func (b *testModelBuilder) WithPromptHistory(prompts ...string) *testModelBuilder {
	b.mutators = append(b.mutators, func(m *Model) {
		m.promptHistory = append(m.promptHistory, prompts...)
		m.historyIdx = -1
	})
	return b
}

// WithTurns sets m.turns. Hides the welcome banner. Most tests want 1.
func (b *testModelBuilder) WithTurns(n int) *testModelBuilder {
	b.mutators = append(b.mutators, func(m *Model) { m.turns = n })
	return b
}

// WithCustomState lets tests reach into any field that doesn't have
// a dedicated knob. Prefer a dedicated method when the field is used
// by ≥2 tests; this escape hatch is for one-off arrangement.
func (b *testModelBuilder) WithCustomState(fn func(*Model)) *testModelBuilder {
	b.mutators = append(b.mutators, fn)
	return b
}

func (b *testModelBuilder) Build() Model {
	// Apply New() so default fields (input, spinner, historyIdx=-1,
	// pathPicker, etc.) get their initial values.
	m := New(b.opts)

	if !b.skipDefSize {
		m.width = 80
		m.height = 40
	}
	if !b.skipReady {
		m.ready = true
	}

	for _, fn := range b.mutators {
		fn(&m)
	}

	return m
}

// BuildPtr is sugar for tests that need a *Model (most slash-command
// handlers do). Equivalent to `m := b.Build(); return &m`.
func (b *testModelBuilder) BuildPtr() *Model {
	m := b.Build()
	return &m
}

// ----------------------------------------------------------------------
// Common assertion helpers (no logic — just labels for readability).
// ----------------------------------------------------------------------

func assertContains(t *testing.T, haystack, needle, why string) {
	t.Helper()
	if !contains(haystack, needle) {
		t.Errorf("%s — output missing %q in %q", why, needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle, why string) {
	t.Helper()
	if contains(haystack, needle) {
		t.Errorf("%s — output unexpectedly contained %q in %q", why, needle, haystack)
	}
}

// contains wraps strings.Contains — kept private + named to match the
// assertion helper convention.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
