// sandboxhint.go — tells the model WHY a confined command failed.
//
// # The problem
//
// When the OS sandbox is active, a denied filesystem write surfaces to
// the model as nothing more than the shell's own errno text — "Operation
// not permitted" on macOS seatbelt, "Permission denied" under Linux
// landlock — plus a non-zero exit code. Nothing in that output says
// seek's own jail caused it.
//
// The model therefore cannot distinguish three very different
// situations that all look identical from the transcript:
//
//  1. the sandbox refused the action (retrying is pointless; the path is
//     outside the writable set on purpose)
//  2. the command failed on its own merits (retrying differently is the
//     right move)
//  3. the sandbox failed to APPLY, so the command never ran at all
//
// Conflating (1) with (2) is expensive precisely where it is least
// visible: `WithSandbox` is wired for autopilot subagents, which run
// unattended. A model that reads a jail denial as a broken build will
// try another path, then another, burning turns with nobody watching.
//
// # The approach, and why the gating is the whole design
//
// Borrowed from dsh's `confine()`, which returns `denialSignatures` plus
// `runnerFailureRules` described as EXIT-GATED stderr signatures
// (packages/sandbox/sandbox/src/index.ts:95-116). The gating is not an
// optimisation, it is what makes the classification safe: "Permission
// denied" is one of the most common strings in ordinary tooling output,
// and claiming seek's sandbox caused an unrelated chmod failure would be
// worse than saying nothing. So a diagnosis requires ALL of:
//
//   - confinement was actually configured for this command, and
//   - the command exited non-zero, and
//   - the output carries a platform-specific signature.
//
// Runner failure is identified by seek's own marker rather than by exit
// code alone: 127 is also "command not found", so the exit code gates
// and the marker identifies.

package bash

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/whyiyhw/seek/internal/sandbox"
)

// sandboxDiagnosis classifies a non-zero exit from a confined command.
type sandboxDiagnosis int

const (
	// sandboxDiagNone means nothing in the output points at the sandbox.
	sandboxDiagNone sandboxDiagnosis = iota
	// sandboxDiagDenied means the jail refused an action the command
	// attempted. The command ran; the model should stop retrying paths
	// outside the writable set.
	sandboxDiagDenied
	// sandboxDiagRunnerFailed means the jail could not be applied, so
	// the command did not run at all. This is seek's problem, not the
	// model's, and it must not be mistaken for a denial.
	sandboxDiagRunnerFailed
)

// runnerFailureMarkers identify seek's own confinement machinery
// reporting that it could not establish the jail.
//
// The Linux trampoline prints "seek sandbox: …" and exits 127 rather
// than running the command unconfined (internal/sandbox/sandbox_linux.go
// RunTrampolineIfRequested — fail-closed by design). macOS surfaces the
// wrapper's own name when `sandbox-exec` cannot apply the profile.
//
// ⚠️ These are matched PER LINE, after subtracting benignSandboxNotices —
// never as a substring of the whole output. See that variable for the
// incident this shape exists to prevent.
var runnerFailureMarkers = []string{
	"seek sandbox:",
	"sandbox-exec:",
}

// benignSandboxNotices are lines seek's confinement machinery may print
// on a SUCCESSFUL path even though they share a prefix with the fatal
// ones. They must be excluded before any fatal-marker match.
//
// # Why this exists while the list is empty
//
// dsh shipped exactly this bug and wrote it up
// (docs/postmortem/0004-landlock-partial-notice-misclassified-child-failures.md):
// their launcher prints `landlock-run: partial enforcement (older
// Landlock ABI)` on a kernel with an older ABI and then executes the
// child normally. Their classifier reduced the contract to the substring
// `landlock-run: ` plus "any nonzero exit", so ripgrep's exit 1 — which
// means "no matches", a SUCCESS — was reported as sandbox infrastructure
// failure. Their fix: "runner classification now requires status-gated
// fatal evidence after exact informational exclusions."
//
// seek is currently safe by accident, not by design: every `seek
// sandbox:` line in internal/sandbox/sandbox_linux.go is immediately
// followed by os.Exit(127), so the prefix is fatal-only today. That is
// one commit away from being false.
//
// **If you add any informational output prefixed `seek sandbox:` (a
// partial-enforcement notice, a degraded-rung warning, a debug line),
// you MUST add its exact text here in the same change.** Otherwise every
// ordinary non-zero exit under a sandbox starts reporting as a jail
// failure, and `TestDiagnoseSandbox_BenignNoticeIsNotRunnerFailure`
// is the test that will tell you.
var benignSandboxNotices = []string{}

// isBenignNotice reports whether a single output line is a known
// informational message rather than fatal evidence. Comparison is on the
// trimmed, lowercased line and must be EXACT: a prefix or substring test
// here would re-create the very ambiguity the exclusion exists to
// remove.
func isBenignNotice(line string) bool {
	for _, n := range benignSandboxNotices {
		if line == strings.ToLower(strings.TrimSpace(n)) {
			return true
		}
	}
	return false
}

// denialSignatures are the errno texts a confined command produces when
// the jail refuses it. Platform-specific because the two backends map
// denial to different errnos: seatbelt returns EPERM, landlock EACCES.
func denialSignatures() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"operation not permitted",
			// Some tools report the raw errno name instead.
			"eperm",
		}
	case "linux":
		return []string{
			"permission denied",
			"eacces",
		}
	default:
		return nil
	}
}

// diagnoseSandbox classifies a finished command and returns the model-
// facing hint, or ("", sandboxDiagNone) when the sandbox is not
// implicated.
//
// opt is the confinement that was configured; nil means the command ran
// unconfined and no diagnosis is possible — an important gate, because
// without it every ordinary "Permission denied" would be blamed on a
// sandbox that was never in the picture.
func diagnoseSandbox(opt *sandbox.Options, exitCode int, output string) (sandboxDiagnosis, string) {
	if opt == nil || exitCode == 0 {
		return sandboxDiagNone, ""
	}
	lower := strings.ToLower(output)

	// Runner failure first: it is the more serious reading, and getting
	// it wrong in the other direction ("you were denied" for a command
	// that never ran) is the worse lie.
	//
	// Matched per line with benign notices subtracted, NOT as a substring
	// of the whole output — see benignSandboxNotices for the incident
	// that shape prevents.
	for _, line := range strings.Split(lower, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isBenignNotice(line) {
			continue
		}
		for _, m := range runnerFailureMarkers {
			if strings.Contains(line, strings.ToLower(m)) {
				return sandboxDiagRunnerFailed, "seek's OS sandbox could not be applied, so this command did NOT run " +
					"(it fails closed rather than running unconfined). This is an environment problem on seek's side, " +
					"not something the command can be rewritten to avoid — report it rather than retrying variations."
			}
		}
	}

	for _, sig := range denialSignatures() {
		if strings.Contains(lower, sig) {
			return sandboxDiagDenied, "seek's OS sandbox is active for this command and denies filesystem writes " +
				"outside " + writableSummary(opt) + ". If the failure above is a write outside that set, it is " +
				"blocked deliberately — retrying the same path, or the same write via another command, will fail " +
				"identically. Write inside the allowed directories instead."
		}
	}

	return sandboxDiagNone, ""
}

// writableSummary renders the confinement's writable set for the hint.
// Naming the actual directories matters: "the sandbox denied you" tells
// the model to give up, while naming where it CAN write tells it what to
// do next.
func writableSummary(opt *sandbox.Options) string {
	if len(opt.WritableDirs) == 0 {
		return "the system temp directory"
	}
	quoted := make([]string, 0, len(opt.WritableDirs))
	for _, d := range opt.WritableDirs {
		quoted = append(quoted, fmt.Sprintf("%q", d))
	}
	return strings.Join(quoted, ", ") + " and the system temp directory"
}
