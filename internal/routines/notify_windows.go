//go:build windows

package routines

// DefaultNotifier on Windows is currently a no-op pending a
// proper toast adapter. The two viable paths are:
//
//   - BurntToast PowerShell module — popular, requires the
//     user to `Install-Module BurntToast`. Not an OS-shipped
//     facility; we'd silently fail on the common case where
//     the module isn't installed.
//   - Direct WinRT XML Toast — needs CGO or a native package,
//     contradicting seek's zero-runtime-dependency stance.
//
// PRD §3.8 lists "Windows toast" as the long-term goal; this
// MVP returns nil so notify=always cron jobs don't spam stderr
// with "notify-send: command not found"-equivalents on every
// fire. Users who want notifications on Windows can wire up
// BurntToast in a v0.6.x dot release follow-up.
var DefaultNotifier Notifier = noopNotifier
