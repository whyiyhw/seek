package autopilot

import (
	"fmt"
	"regexp"
)

// Lanes that mount the no-remote guard. The label rides the refusal
// reason because the fact "nobody is watching" belongs to the LANE, not
// to this package: under SEEK_ACP_YOLO a human is present and may have
// asked for the push themselves — the original wording claimed
// "autopilot runs unattended" there, which was false and taught the
// model to route around the guard (c_ajza7r, 2026-11-19).
const (
	LaneAutopilot = "autopilot" // seek autopilot run
	LaneGoal      = "goal"      // seek goal "<goal>"
	LaneACP       = "acp"       // seek acp with SEEK_ACP_YOLO=1
)

// IsRemoteMutating reports whether a shell command performs a REMOTE
// mutation — pushing commits or creating/merging PRs/releases. Autopilot
// subagents run unattended and must NOT reach the remote (PRD §4 D2):
// they produce LOCAL commits for the user to review in the morning.
//
// This is the policy half of the no-remote guard; bash.Tool.WithDeny is
// the mechanism. It is best-effort string matching (defense-in-depth, not
// a security boundary — 柱 O's OS sandbox is the kernel-level guarantee).
// Read-only network ops (git fetch / pull, gh pr view) are allowed.
//
// Lane-less entry point, kept for the existing callers and tests: the
// reason reports the autopilot lane, this function's historical home.
// Every live mount point passes its real lane via IsRemoteMutatingOnLane
// so the refusal cannot assert a lane it is not on.
func IsRemoteMutating(command string) (bool, string) {
	return IsRemoteMutatingOnLane(LaneAutopilot, command)
}

// IsRemoteMutatingOnLane is IsRemoteMutating with the calling lane named
// in the reason. The blocking set is identical — the lane only changes
// the reason's label. The reason is disposition guidance, not a fact
// claim: it says what the guard is (best-effort matching), where it is
// mounted (the named lane), and what a human-authorised session should
// do instead of engineering a bypass. The guard cannot know whether a
// human is present or has authorised the operation; it must not say.
func IsRemoteMutatingOnLane(lane, command string) (bool, string) {
	if lane == "" {
		lane = LaneAutopilot
	}
	for _, p := range remotePatterns {
		if p.re.MatchString(command) {
			return true, fmt.Sprintf(reasonFormat, lane, p.refusal)
		}
	}
	return false, ""
}

// reasonFormat composes a refusal from the lane and the pattern's clause.
// The sandbox clause is deliberately hedged: the OS sandbox is only the
// real guarantee where it is actually available/enabled, and the guard
// must not assert an enforcement fact it cannot see (the same mistake as
// the old "runs unattended" text, c_ajza7r).
const reasonFormat = "no-remote guard on the %s lane: this lane %s. " +
	"The guard is best-effort string matching, not a security boundary — where the OS sandbox is available it is the real guarantee. " +
	"If the user explicitly asked for this operation in this session, it is theirs to run — in their terminal, or via a shell script they run — not something to route around."

var remotePatterns = []struct {
	re      *regexp.Regexp
	refusal string
}{
	{regexp.MustCompile(`\bgit\s+push\b`), "does not push — local commits are left for review"},
	{regexp.MustCompile(`\bgit\s+remote\s+(add|set-url|rename|remove|set-head)\b`), "does not change git remotes"},
	{regexp.MustCompile(`\bgh\s+(pr|repo|release)\s+(create|merge|close|edit|delete|ready)\b`), "does not open/merge/close PRs or change repos — local commits are left for review"},
	{regexp.MustCompile(`\bgh\s+api\b`), "does not call the GitHub API"},
}
