package tui

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeWebhook records the last (event, title, body) the dispatcher was
// called with, so tests can assert what sessionNotifyCmd fired.
type fakeWebhook struct {
	called             bool
	event, title, body string
}

func (f *fakeWebhook) dispatch(_ context.Context, event, title, body string) {
	f.called, f.event, f.title, f.body = true, event, title, body
}

func TestSessionNotifyCmd_Gating(t *testing.T) {
	t.Parallel()
	longAgo := time.Now().Add(-2 * time.Minute) // a 2-min "long" turn

	cases := []struct {
		name        string
		setup       func(m *Model, f *fakeWebhook)
		wasCanceled bool
		wantCmd     bool
	}{
		{"no webhook configured", func(m *Model, f *fakeWebhook) {
			m.opts.Webhook = nil
			m.opts.SessionNotifySeconds = 60
			m.streamStartTime = longAgo
		}, false, false},
		{"notify disabled (seconds 0)", func(m *Model, f *fakeWebhook) {
			m.opts.Webhook = f.dispatch
			m.opts.SessionNotifySeconds = 0
			m.streamStartTime = longAgo
		}, false, false},
		{"cancelled turn", func(m *Model, f *fakeWebhook) {
			m.opts.Webhook = f.dispatch
			m.opts.SessionNotifySeconds = 60
			m.streamStartTime = longAgo
		}, true, false},
		{"start time unset", func(m *Model, f *fakeWebhook) {
			m.opts.Webhook = f.dispatch
			m.opts.SessionNotifySeconds = 60
			m.streamStartTime = time.Time{}
		}, false, false},
		{"turn too short", func(m *Model, f *fakeWebhook) {
			m.opts.Webhook = f.dispatch
			m.opts.SessionNotifySeconds = 60
			m.streamStartTime = time.Now().Add(-5 * time.Second)
		}, false, false},
		{"long turn fires", func(m *Model, f *fakeWebhook) {
			m.opts.Webhook = f.dispatch
			m.opts.SessionNotifySeconds = 60
			m.streamStartTime = longAgo
			m.promptHistory = []string{"refactor the auth middleware"}
		}, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := emptyModel()
			f := &fakeWebhook{}
			tc.setup(m, f)
			cmd := m.sessionNotifyCmd(tc.wasCanceled)
			if (cmd != nil) != tc.wantCmd {
				t.Fatalf("sessionNotifyCmd returned cmd=%v, want %v", cmd != nil, tc.wantCmd)
			}
			if cmd == nil {
				return
			}
			cmd() // execute the best-effort POST closure
			if !f.called {
				t.Fatal("expected the webhook dispatcher to be called")
			}
			if f.event != "session.completed" {
				t.Errorf("event = %q, want session.completed", f.event)
			}
			if !strings.Contains(f.body, "refactor the auth middleware") {
				t.Errorf("body should name the task, got %q", f.body)
			}
			if !strings.Contains(f.title, "seek: task finished") {
				t.Errorf("title = %q", f.title)
			}
		})
	}
}

func TestSessionNotifyBody(t *testing.T) {
	t.Parallel()
	if got := sessionNotifyBody(nil); got != "Your seek task finished." {
		t.Errorf("empty history = %q", got)
	}
	if got := sessionNotifyBody([]string{"fix the bug"}); got != "Task: fix the bug" {
		t.Errorf("single = %q", got)
	}
	if got := sessionNotifyBody([]string{"first line\nsecond line"}); got != "Task: first line" {
		t.Errorf("multiline should keep only the first line, got %q", got)
	}
	long := strings.Repeat("x", 300)
	got := sessionNotifyBody([]string{long})
	if n := len([]rune(got)); n > len("Task: ")+160 || !strings.HasSuffix(got, "…") {
		t.Errorf("long prompt should be truncated with an ellipsis, got %d runes", n)
	}

	// Rune-safe truncation: a long Chinese prompt must not be sliced
	// mid-rune into invalid UTF-8 / mojibake.
	zh := sessionNotifyBody([]string{strings.Repeat("重构认证中间件", 50)}) // 350 runes
	if !utf8.ValidString(zh) {
		t.Errorf("truncated Chinese prompt is not valid UTF-8: %q", zh)
	}
	if !strings.HasSuffix(zh, "…") {
		t.Errorf("long Chinese prompt should end with an ellipsis, got %q", zh)
	}
}
