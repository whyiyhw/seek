package autopilot

import "regexp"

// IsRemoteMutating reports whether a shell command performs a REMOTE
// mutation — pushing commits or creating/merging PRs/releases. Autopilot
// subagents run unattended and must NOT reach the remote (PRD §4 D2):
// they produce LOCAL commits for the user to review in the morning.
//
// This is the policy half of the no-remote guard; bash.Tool.WithDeny is
// the mechanism. It is best-effort string matching (defense-in-depth, not
// a security boundary — 柱 O's OS sandbox is the kernel-level guarantee).
// Read-only network ops (git fetch / pull, gh pr view) are allowed.
func IsRemoteMutating(command string) (bool, string) {
	for _, p := range remotePatterns {
		if p.re.MatchString(command) {
			return true, p.reason
		}
	}
	return false, ""
}

var remotePatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{regexp.MustCompile(`\bgit\s+push\b`), "autopilot runs unattended and does not push — leave local commits for review"},
	{regexp.MustCompile(`\bgit\s+remote\s+(add|set-url|rename|remove|set-head)\b`), "autopilot may not change git remotes"},
	{regexp.MustCompile(`\bgh\s+(pr|repo|release)\s+(create|merge|close|edit|delete|ready)\b`), "autopilot does not open/merge PRs or change repos — leave local commits for review"},
	{regexp.MustCompile(`\bgh\s+api\b`), "autopilot may not call the GitHub API"},
}
