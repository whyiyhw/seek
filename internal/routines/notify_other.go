//go:build !darwin && !linux && !windows

package routines

// DefaultNotifier on platforms without a known native popup
// mechanism (FreeBSD / OpenBSD / others) is a silent no-op.
// Returning nil "the notification went through" is honest:
// there's no per-host facility to surface it on these
// platforms, and writing WARN to stderr every minute on a
// cron-driven server would be noise without recourse.
var DefaultNotifier Notifier = noopNotifier
