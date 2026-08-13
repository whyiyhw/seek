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
var runnerFailureMarkers = []string{
	"seek sandbox:",
	"sandbox-exec:",
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

	// Runner failure first: it is the more serious reading, and its
	// marker is unambiguous where the denial signatures are not.
	for _, m := range runnerFailureMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return sandboxDiagRunnerFailed, "seek's OS sandbox could not be applied, so this command did NOT run " +
				"(it fails closed rather than running unconfined). This is an environment problem on seek's side, " +
				"not something the command can be rewritten to avoid — report it rather than retrying variations."
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
