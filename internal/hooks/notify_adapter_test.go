package hooks

import (
	"context"
	"errors"
	"testing"
)

// notifyAdapterFake records Notification events for the adapter test
// (distinct type from hooks_test.go's notificationFake, which counts
// calls rather than collecting payloads).
type notifyAdapterFake struct {
	events []NotificationEvent
}

func (f *notifyAdapterFake) OnNotification(_ context.Context, ev NotificationEvent) {
	f.events = append(f.events, ev)
}

// TestNotifierObserver_ReportsThenDelegates: the wrapper is pure
// observation plus delegation — the event fires BEFORE the wrapped
// sender runs (so the intent is recorded even when the popup fails),
// and the sender's error is returned unchanged.
func TestNotifierObserver_ReportsThenDelegates(t *testing.T) {
	rec := &notifyAdapterFake{}
	reg := NewRegistry()
	reg.Register(rec)

	var sends int
	sendErr := errors.New("no notification daemon")
	send := func(title, body string) error {
		sends++
		if len(rec.events) != 1 {
			t.Errorf("sender ran before the observer fired (events=%d)", len(rec.events))
		}
		return sendErr
	}

	wrapped := NotifierObserver(reg, send)
	if err := wrapped("seek cron: nightly (completed)", "done"); err != sendErr {
		t.Errorf("error = %v, want the delegate's error unchanged", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("notifications = %d, want 1", len(rec.events))
	}
	if rec.events[0].Kind != "os" {
		t.Errorf("Kind = %q, want os", rec.events[0].Kind)
	}
	if rec.events[0].Title != "seek cron: nightly (completed)" {
		t.Errorf("Title = %q, want the popup title", rec.events[0].Title)
	}
	if sends != 1 {
		t.Errorf("delegate calls = %d, want exactly 1", sends)
	}
}

// TestNotifierObserver_NilSenderIsNil: nil sender → nil wrapper keeps
// routines.TickOptions.Notifier's "nil means platform default"
// contract intact (a non-nil no-op would silently disable popups).
func TestNotifierObserver_NilSenderIsNil(t *testing.T) {
	if fn := NotifierObserver(NewRegistry(), nil); fn != nil {
		t.Error("nil sender must yield a nil wrapper")
	}
}

// TestNotifierObserver_NilRegistryIsSafe: a nil registry is valid
// throughout this package; dispatch is nil-receiver safe, so the
// wrapper still delegates.
func TestNotifierObserver_NilRegistryIsSafe(t *testing.T) {
	called := false
	wrapped := NotifierObserver(nil, func(string, string) error {
		called = true
		return nil
	})
	if err := wrapped("t", "b"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !called {
		t.Error("delegate not called with a nil registry")
	}
}
