package hooks

import "context"

// NotifierObserver wraps a best-effort notification sender so every
// send is ALSO reported to the Notification observer (feature-
// orchestration D15 / feature-hooks-injection §3.3: "OS 通知发出（柱 H
// Notifier 调用点）").
//
// The sender signature is structural — it matches internal/routines'
// `Notifier func(title, body string) error` without this package
// importing routines (the observer surface must not depend on any
// particular notification backend). The wrapped sender's error is
// returned verbatim: the hook is pure observation, it can neither
// suppress nor replace the popup.
//
// nil reg → a pass-through wrapper (nil reg is valid throughout this
// package); nil send → nil, so a caller can hand the result straight
// to TickOptions.Notifier, where nil means "use the platform default".
//
// Firing order is hook-then-send: the observation records the intent
// ("something surfaced") even when the platform popup then fails.
func NotifierObserver(reg *Registry, send func(title, body string) error) func(title, body string) error {
	if send == nil {
		return nil
	}
	return func(title, body string) error {
		reg.NotifyNotification(context.Background(), NotificationEvent{
			Kind:  "os",
			Title: title,
		})
		return send(title, body)
	}
}
