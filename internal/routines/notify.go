package routines

// Notifier is the cross-platform OS notification primitive.
// Production callers use the package-level DefaultNotifier
// (set per build tag in notify_{darwin,linux,windows,other}.go);
// tests inject via TickOptions.Notifier to avoid actually
// popping notifications during test runs.
//
// Implementations MUST be best-effort: any failure to dispatch
// (missing binary, permission, headless host) returns a
// descriptive error but the cron run that triggered the
// notification stays intact. Per PRD §3.8 / §8 risk row "OS
// notification in headless server" — the user loses the popup,
// not the data.
type Notifier func(title, body string) error

// shouldNotify decides whether to dispatch a notification for
// the (job, terminal-status) pair. Pure function — kept here
// so all three call sites (tick, RunOne, future trigger
// runner) share one source of truth.
//
//	always     → notify on every terminal status
//	on_failure → notify on failed / killed; skip completed
//	never      → never notify
//	(other)    → never notify (defensive; ValidateNotify gates input)
//
// Empty Notify field defaults to "always" — Store.Create fills
// that on persistence, but in-memory Jobs constructed elsewhere
// might leave it empty; defaulting here protects the path.
func shouldNotify(job Job, status string) bool {
	policy := job.Notify
	if policy == "" {
		policy = NotifyAlways
	}
	switch policy {
	case NotifyAlways:
		return true
	case NotifyOnFailure:
		return status != StatusCompleted
	default:
		return false
	}
}

// noopNotifier is the fallback Notifier for platforms with no
// native popup mechanism (BSD variants currently). Returns
// nil — pretending the notification went through is correct
// here: there's no per-host way to surface it, and treating
// "no platform support" as a failure would clutter stderr on
// every cron run.
func noopNotifier(title, body string) error { return nil }
