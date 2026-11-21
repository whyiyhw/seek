package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// notifyRecorder captures every observer the TUI fires.
type notifyRecorder struct {
	mu            sync.Mutex
	notifications []hooks.NotificationEvent
	compacts      []hooks.PreCompactEvent
}

func (r *notifyRecorder) OnNotification(_ context.Context, ev hooks.NotificationEvent) {
	r.mu.Lock()
	r.notifications = append(r.notifications, ev)
	r.mu.Unlock()
}

func (r *notifyRecorder) OnPreCompact(_ context.Context, ev hooks.PreCompactEvent) {
	r.mu.Lock()
	r.compacts = append(r.compacts, ev)
	r.mu.Unlock()
}

func (r *notifyRecorder) Notifications() []hooks.NotificationEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hooks.NotificationEvent(nil), r.notifications...)
}

func (r *notifyRecorder) Compacts() []hooks.PreCompactEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hooks.PreCompactEvent(nil), r.compacts...)
}

func newNotifyRegistry(rec *notifyRecorder) *hooks.Registry {
	reg := hooks.NewRegistry()
	reg.Register(rec)
	return reg
}

// TestCmdCompact_FiresPreCompactBeforeSummarise: /compact must report
// the PreCompact observer with trigger=manual, and it must fire BEFORE
// the summariser request goes out (the point of a "pre" event). The
// server records whether the hook had already fired when the request
// arrived — that ordering is the assertion.
func TestCmdCompact_FiresPreCompactBeforeSummarise(t *testing.T) {
	rec := &notifyRecorder{}
	hreg := newNotifyRegistry(rec)

	var sawHookAtRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(rec.Compacts()) == 1 {
			sawHookAtRequest = true
		}
		if !strings.Contains(readBody(r), "Summarise the conversation") {
			t.Errorf("summariser prompt missing from request")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"BRIEFING"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`)
	}))
	defer srv.Close()

	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
		InitialMessages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "u1"},
			{Role: deepseek.RoleAssistant, Content: "a1"},
			{Role: deepseek.RoleUser, Content: "u2"},
			{Role: deepseek.RoleAssistant, Content: "a2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	m := emptyModel()
	m.opts.Agent = ag
	m.opts.Hooks = hreg

	res := runHandler(t, m, "/compact")
	if res.extra == nil {
		t.Fatal("expected an async cmd from /compact")
	}
	// The hook must NOT have fired yet — cmdCompact only builds the
	// Cmd; the fire happens when bubbletea runs it (off the Update
	// goroutine). This is the "don't block the UI loop" contract.
	if got := rec.Compacts(); len(got) != 0 {
		t.Fatalf("PreCompact fired %d times before the Cmd ran, want 0", len(got))
	}

	msg := res.extra()
	done, ok := msg.(compactDoneMsg)
	if !ok {
		t.Fatalf("unexpected msg type %T", msg)
	}
	if done.err != nil {
		t.Fatalf("compact err: %v", done.err)
	}
	got := rec.Compacts()
	if len(got) != 1 {
		t.Fatalf("PreCompact fired %d times, want 1", len(got))
	}
	if got[0].Trigger != "manual" {
		t.Errorf("Trigger = %q, want manual", got[0].Trigger)
	}
	if !sawHookAtRequest {
		t.Error("PreCompact must fire BEFORE the summariser request")
	}
}

// TestCmdCompact_NilHooksNoFire: with no registry the command still
// runs to completion — the nil-safe dispatch path, and the pre-wiring
// byte-identical behaviour.
func TestCmdCompact_NilHooksNoFire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"x","model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"B"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer srv.Close()

	ag, _ := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		SystemPrompt: "sys",
		InitialMessages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "u"},
			{Role: deepseek.RoleAssistant, Content: "a"},
		},
	})
	m := emptyModel()
	m.opts.Agent = ag
	res := runHandler(t, m, "/compact")
	if done := res.extra().(compactDoneMsg); done.err != nil {
		t.Fatalf("compact err: %v", done.err)
	}
}

// TestAskUserRequest_FiresNotification: the v1 picker popping up fires
// Notification{Kind:"ask_user"} with the question as the title.
func TestAskUserRequest_FiresNotification(t *testing.T) {
	rec := &notifyRecorder{}
	m := emptyModel()
	m.opts.Hooks = newNotifyRegistry(rec)

	q := askuser.Question{Question: "which framework?", Options: []askuser.Option{{ID: "react", Label: "React"}}}
	_, cmd := m.Update(askUserRequestMsg{req: askuser.Request{Question: q, Reply: make(chan askuser.Answer, 1)}})
	if cmd == nil {
		t.Fatal("Update returned no cmd for an ask_user request")
	}
	// The Cmd is what fires the observer; run it.
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	got := rec.Notifications()
	if len(got) != 1 {
		t.Fatalf("Notification fired %d times, want 1", len(got))
	}
	if got[0].Kind != "ask_user" {
		t.Errorf("Kind = %q, want ask_user", got[0].Kind)
	}
	if got[0].Title != "which framework?" {
		t.Errorf("Title = %q, want the question text", got[0].Title)
	}
}

// TestAskUserBatchRequest_FiresNotification: the v2 batch picker fires
// the same observer, titled with the question the user is answering
// first plus a "(1/N)" suffix when more questions follow.
func TestAskUserBatchRequest_FiresNotification(t *testing.T) {
	rec := &notifyRecorder{}
	m := emptyModel()
	m.opts.Hooks = newNotifyRegistry(rec)

	batch := askuser.Batch{Questions: []askuser.Question{
		{Question: "which storage?"},
		{Question: "which auth?"},
	}}
	_, cmd := m.Update(askUserBatchRequestMsg{req: askuser.BatchRequest{Batch: batch, Reply: make(chan []askuser.Answer, 1)}})
	if cmd == nil {
		t.Fatal("Update returned no cmd for a batch ask_user request")
	}
	if bm, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range bm {
			if c != nil {
				c()
			}
		}
	}
	got := rec.Notifications()
	if len(got) != 1 {
		t.Fatalf("Notification fired %d times, want 1", len(got))
	}
	if got[0].Kind != "ask_user" || got[0].Title != "which storage? (1/2)" {
		t.Errorf("Notification = %+v, want kind=ask_user title=which storage? (1/2)", got[0])
	}
}

// TestBatchNotificationTitle_CountSuffix: the count suffix is the
// contract the update.go wiring comment promises — a hook reading
// SEEK_NOTIFY_TITLE can tell a 1-of-1 ask from a batch with more
// questions queued. 1-of-1 stays bare (a suffix there is noise).
func TestBatchNotificationTitle_CountSuffix(t *testing.T) {
	if got := batchNotificationTitle(askuser.Batch{Questions: []askuser.Question{{Question: "only?"}}}); got != "only?" {
		t.Errorf("1-of-1 title = %q, want no suffix", got)
	}
	if got := batchNotificationTitle(askuser.Batch{Questions: []askuser.Question{
		{Question: "first?"},
		{Question: "second?"},
		{Question: "third?"},
	}}); got != "first? (1/3)" {
		t.Errorf("1-of-3 title = %q, want (1/3) suffix", got)
	}
	if got := batchNotificationTitle(askuser.Batch{}); got != "ask_user" {
		t.Errorf("empty-batch title = %q, want the generic label", got)
	}
}

// TestAskUserNotification_TitleIsBounded: the title feeds a shell env
// var, so a huge question must be truncated rather than pasted
// wholesale (1000-char prompt → capped at notifyTitleMax runes + the
// ellipsis).
func TestAskUserNotification_TitleIsBounded(t *testing.T) {
	rec := &notifyRecorder{}
	hreg := newNotifyRegistry(rec)
	long := strings.Repeat("x", 1000)
	cmd := askUserNotificationCmd(hreg, long)
	if cmd == nil {
		t.Fatal("nil cmd for a non-nil registry")
	}
	cmd()
	got := rec.Notifications()
	if len(got) != 1 {
		t.Fatalf("notifications = %d, want 1", len(got))
	}
	if n := len([]rune(got[0].Title)); n > notifyTitleMax+1 {
		t.Errorf("title rune length = %d, want <= %d (+ellipsis)", n, notifyTitleMax)
	}
}

// TestAskUserNotification_NilRegistryReturnsNilCmd: no registry → no
// Cmd, keeping the pre-wiring command batch identical.
func TestAskUserNotification_NilRegistryReturnsNilCmd(t *testing.T) {
	if cmd := askUserNotificationCmd(nil, "q"); cmd != nil {
		t.Error("nil registry must yield a nil Cmd")
	}
}

// TestNotificationObserver_CronNotifierWrapped: the OS notification
// adapter reports Kind:"os" and still delegates to the wrapped sender
// verbatim (including its error).
func TestNotificationObserver_CronNotifierWrapped(t *testing.T) {
	rec := &notifyRecorder{}
	hreg := newNotifyRegistry(rec)

	var sent [][2]string
	sendErr := errors.New("osascript: no display")
	send := func(title, body string) error {
		sent = append(sent, [2]string{title, body})
		return sendErr
	}
	wrapped := hooks.NotifierObserver(hreg, send)
	if err := wrapped("seek cron: nightly (completed)", "all good"); err != sendErr {
		t.Errorf("wrapped notifier error = %v, want the delegate's error", err)
	}
	if len(sent) != 1 || sent[0][0] != "seek cron: nightly (completed)" {
		t.Errorf("delegate got %v, want one call with the original title", sent)
	}
	got := rec.Notifications()
	if len(got) != 1 {
		t.Fatalf("Notification fired %d times, want 1", len(got))
	}
	if got[0].Kind != "os" || got[0].Title != "seek cron: nightly (completed)" {
		t.Errorf("Notification = %+v, want kind=os with the popup title", got[0])
	}
}

// TestNotificationObserver_NilSenderReturnsNil keeps the "nil means
// platform default" contract of TickOptions.Notifier intact.
func TestNotificationObserver_NilSenderReturnsNil(t *testing.T) {
	if fn := hooks.NotifierObserver(hooks.NewRegistry(), nil); fn != nil {
		t.Error("nil sender must yield a nil wrapper")
	}
}

// TestAutoCompact_FiresPreCompactTriggerAuto: the automatic path rides
// the same startCompactCmd as /compact, so the PreCompact payload must
// say trigger=auto — a hook keyed on "manual" would otherwise log every
// unattended compaction as user-initiated.
func TestAutoCompact_FiresPreCompactTriggerAuto(t *testing.T) {
	rec := &notifyRecorder{}
	m, _ := autoCompactModel(t, 350_000, 400_000)
	m.opts.Hooks = newNotifyRegistry(rec)
	cmd, _ := m.maybeAutoCompact(false)
	if cmd == nil {
		t.Fatal("expected an auto-compact cmd")
	}
	cmd() // fakeAgent's Summarise completes immediately
	got := rec.Compacts()
	if len(got) != 1 {
		t.Fatalf("PreCompact fired %d times, want 1", len(got))
	}
	if got[0].Trigger != "auto" {
		t.Errorf("Trigger = %q, want auto", got[0].Trigger)
	}
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
