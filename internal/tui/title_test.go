package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// countTitleGen is a test TitleSuggester that records every invocation and
// returns a stable title. The call count is the "at most once per session"
// invariant the card pins.
type countTitleGen struct {
	calls int
	title string
}

func (g *countTitleGen) gen(context.Context, []deepseek.Message) string {
	g.calls++
	return g.title
}

// runTitleCmd runs a Cmd and returns the single titleReadyMsg it emits.
// scheduleTitle returns a bare Cmd (not a Batch), so one call is enough.
func runTitleCmd(t *testing.T, cmd tea.Cmd) titleReadyMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a title Cmd, got nil")
	}
	raw := cmd()
	msg, ok := raw.(titleReadyMsg)
	if !ok {
		t.Fatalf("Cmd emitted %T, want titleReadyMsg", raw)
	}
	return msg
}

// wiredTitleModel builds a model with a real temp session store + a
// counting generator so the whole schedule → result → persist path runs
// hermetically (no DeepSeek client needed).
func wiredTitleModel(t *testing.T, gen *countTitleGen, sess *session.Session) (*Model, *session.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := session.NewStoreAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	fa := newFakeAgent()
	fa.SetMessages([]deepseek.Message{
		{Role: deepseek.RoleUser, Content: "帮我看看 WebSocket 重连"},
		{Role: deepseek.RoleAssistant, Content: "先看重连逻辑"},
	})
	b := testModel().WithAgent(fa).WithSession(sess).WithStore(st)
	b.opts.TitleSuggester = gen.gen
	b.opts.Ctx = context.Background()
	return b.BuildPtr(), st
}

func titleSession() *session.Session {
	s := session.New("deepseek-v4-flash", "/tmp", "", false, false)
	s.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "帮我看看 WebSocket 重连"}}
	return s
}

// ④ cost: one session fires AT MOST ONE generation. Two stream ends
// before the result lands must not double-fire (gate set at schedule time).
func TestScheduleTitle_OncePerSession(t *testing.T) {
	gen := &countTitleGen{title: "修复 WebSocket 重连"}
	sess := titleSession()
	m, _ := wiredTitleModel(t, gen, sess)

	first := m.scheduleTitle(false)
	second := m.scheduleTitle(false) // second stream-end before result lands
	if first == nil {
		t.Fatal("first stream-end must schedule a title")
	}
	if second != nil {
		t.Fatal("second stream-end must NOT schedule another title (one-shot)")
	}
	// Executing the one Cmd calls the generator exactly once.
	msg := runTitleCmd(t, first)
	if msg.Title != gen.title || msg.SID != sess.ID {
		t.Fatalf("titleReadyMsg = %+v, want title=%q sid=%s", msg, gen.title, sess.ID)
	}
	if gen.calls != 1 {
		t.Fatalf("generator calls = %d, want 1", gen.calls)
	}
	// A third stream-end while in flight still can't schedule another
	// (titleRequested stays set until the result handler writes the header).
	if m.scheduleTitle(false) != nil {
		t.Fatal("post-result stream-end must not re-schedule (one-shot gate)")
	}
}

// Esc-cancelled / already-titled / no-history all suppress scheduling.
func TestScheduleTitle_Suppressed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		canceled  bool
		withTitle string
		noHistory bool
	}{
		{"canceled", true, "", false},
		{"already titled", false, "已有标题", false},
		{"no history", false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gen := &countTitleGen{title: "x"}
			sess := titleSession()
			sess.Title = tc.withTitle
			m, _ := wiredTitleModel(t, gen, sess)
			if tc.noHistory {
				m.opts.Agent.(*fakeAgent).SetMessages(nil)
			}
			if cmd := m.scheduleTitle(tc.canceled); cmd != nil {
				t.Fatalf("%s must not schedule a title", tc.name)
			}
			if gen.calls != 0 {
				t.Fatalf("generator called %d times", gen.calls)
			}
		})
	}
}

// ① result lands → header written + persisted. Empty / stale results are
// dropped so the derived first-user-line label stays.
func TestTitleReady_WritesHeaderAndPersists(t *testing.T) {
	gen := &countTitleGen{title: "修复 WebSocket 重连"}
	sess := titleSession()
	m, st := wiredTitleModel(t, gen, sess)

	msg := runTitleCmd(t, m.scheduleTitle(false))
	out, _ := m.Update(msg)
	final := out.(Model)
	if got := final.opts.Session.Title; got != gen.title {
		t.Fatalf("session title = %q, want %q", got, gen.title)
	}
	loaded, err := st.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != gen.title {
		t.Fatalf("persisted title = %q, want %q", loaded.Title, gen.title)
	}
}

func TestTitleReady_DropsEmptyAndStale(t *testing.T) {
	sess := titleSession()
	gen := &countTitleGen{title: "unused"}
	m, st := wiredTitleModel(t, gen, sess)

	// Empty result: no header write.
	out, _ := m.Update(titleReadyMsg{SID: sess.ID, Title: ""})
	if out.(Model).opts.Session.Title != "" {
		t.Fatal("empty title must not be written")
	}
	// Stale SID (session swapped by /clear /branch): no header write.
	out, _ = m.Update(titleReadyMsg{SID: "stale-sid", Title: "旧结果"})
	if out.(Model).opts.Session.Title != "" {
		t.Fatal("stale title must not be written")
	}
	loaded, err := st.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "" {
		t.Fatalf("header must stay empty, got %q", loaded.Title)
	}
}

// ④ resume: a session with history but no title is named once on Init;
// a session already carrying a title is not.
func TestScheduleResumeTitle(t *testing.T) {
	gen := &countTitleGen{title: "恢复会话标题"}
	sess := titleSession()
	sess.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "原问题"},
		{Role: deepseek.RoleAssistant, Content: "原回答"},
	}
	m, _ := wiredTitleModel(t, gen, sess)
	m.turns = 1 // resumed history

	first := m.scheduleResumeTitle()
	if first == nil {
		t.Fatal("resumed session with history must schedule a title")
	}
	if m.scheduleResumeTitle() != nil {
		t.Fatal("resume title must be one-shot")
	}
	msg := runTitleCmd(t, first)
	if msg.Title != gen.title || msg.SID != sess.ID {
		t.Fatalf("titleReadyMsg = %+v", msg)
	}

	// Fresh session (turns==0) does not name on startup.
	freshGen := &countTitleGen{title: "x"}
	fresh, _ := wiredTitleModel(t, freshGen, titleSession())
	if fresh.scheduleResumeTitle() != nil {
		t.Fatal("fresh session must not name on startup")
	}

	// Already-titled resume does not re-generate.
	titled := titleSession()
	titled.Title = "已有标题"
	titled.Messages = sess.Messages
	titledGen := &countTitleGen{title: "x"}
	tm, _ := wiredTitleModel(t, titledGen, titled)
	tm.turns = 1
	if tm.scheduleResumeTitle() != nil {
		t.Fatal("already-titled resume must not regenerate")
	}
}

// /clear, /branch, /compact reset the gate so the new session header can
// be named afresh (verify the reset hook clears titleRequested).
func TestResetSessionCounters_ClearsTitleGate(t *testing.T) {
	gen := &countTitleGen{title: "x"}
	m, _ := wiredTitleModel(t, gen, titleSession())
	m.titleRequested = true
	m.resetSessionCounters()
	if m.titleRequested {
		t.Fatal("resetSessionCounters must clear the one-shot title gate")
	}
}

// Verify the generator func shape compiles against a real titlesug-style
// signature (context + history → title). countTitleGen.gen already does.
var _ func(context.Context, []deepseek.Message) string = (&countTitleGen{}).gen
