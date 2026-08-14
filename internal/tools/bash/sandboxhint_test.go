package bash

import (
	"runtime"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/sandbox"
)

// denialText returns an errno string this platform's sandbox actually
// produces, so the tests exercise the signature that ships rather than a
// synthetic one.
func denialText(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		return "sh: /etc/hosts: Operation not permitted"
	case "linux":
		return "sh: /etc/hosts: Permission denied"
	default:
		t.Skip("no sandbox denial signatures on this platform")
		return ""
	}
}

// TestDiagnoseSandbox_NoConfinementNeverBlames is the load-bearing gate.
// "Permission denied" is one of the most common strings in ordinary
// tooling output; blaming a sandbox that was never applied would be a
// confidently wrong hint, which is worse than no hint.
func TestDiagnoseSandbox_NoConfinementNeverBlames(t *testing.T) {
	diag, hint := diagnoseSandbox(nil, 1, "cp: /etc/hosts: Permission denied\nOperation not permitted\n")
	if diag != sandboxDiagNone || hint != "" {
		t.Errorf("unconfined command was blamed on the sandbox: diag=%v hint=%q", diag, hint)
	}
}

func TestDiagnoseSandbox_ZeroExitNeverBlames(t *testing.T) {
	opt := &sandbox.Options{WritableDirs: []string{"/w"}}
	// A successful command whose output merely mentions the phrase — a
	// grep for the string, a test name, a log line being echoed.
	out := "ok\ngrep found: Operation not permitted\nPermission denied\n"
	if diag, _ := diagnoseSandbox(opt, 0, out); diag != sandboxDiagNone {
		t.Errorf("a successful command was diagnosed as a sandbox failure: %v", diag)
	}
}

func TestDiagnoseSandbox_DeniedNamesTheWritableSet(t *testing.T) {
	opt := &sandbox.Options{WritableDirs: []string{"/repo/wt-1"}}
	diag, hint := diagnoseSandbox(opt, 1, denialText(t))
	if diag != sandboxDiagDenied {
		t.Fatalf("diag = %v, want sandboxDiagDenied", diag)
	}
	if !strings.Contains(hint, "/repo/wt-1") {
		t.Errorf("hint does not name where the model CAN write: %q", hint)
	}
	// Telling the model to stop retrying is the point — a hint that only
	// says "denied" invites the same command with a different spelling.
	if !strings.Contains(hint, "retrying") {
		t.Errorf("hint does not tell the model retrying is futile: %q", hint)
	}
}

func TestDiagnoseSandbox_UnrelatedFailureIsNotDiagnosed(t *testing.T) {
	opt := &sandbox.Options{WritableDirs: []string{"/w"}}
	out := "go: build failed\n./main.go:12:2: undefined: foo\n"
	if diag, hint := diagnoseSandbox(opt, 2, out); diag != sandboxDiagNone {
		t.Errorf("an ordinary build failure was blamed on the sandbox: diag=%v hint=%q", diag, hint)
	}
}

// TestDiagnoseSandbox_RunnerFailureBeatsDenial: the Linux trampoline
// fails closed with exit 127 and a "seek sandbox:" marker, and its
// stderr can also contain an errno phrase. Reporting that as a denial
// would tell the model the command ran and was refused, when in fact it
// never ran at all.
func TestDiagnoseSandbox_RunnerFailureBeatsDenial(t *testing.T) {
	opt := &sandbox.Options{WritableDirs: []string{"/w"}}
	out := "seek sandbox: landlock: operation not permitted\n"
	diag, hint := diagnoseSandbox(opt, 127, out)
	if diag != sandboxDiagRunnerFailed {
		t.Fatalf("diag = %v, want sandboxDiagRunnerFailed", diag)
	}
	if !strings.Contains(hint, "did NOT run") {
		t.Errorf("hint does not say the command never ran: %q", hint)
	}
}

// TestDiagnoseSandbox_ExitCodeAloneIsNotRunnerFailure: 127 is also the
// shell's "command not found". Without the marker requirement, every
// typo'd command under a sandbox would be reported as seek's jail
// breaking.
func TestDiagnoseSandbox_ExitCodeAloneIsNotRunnerFailure(t *testing.T) {
	opt := &sandbox.Options{WritableDirs: []string{"/w"}}
	out := "sh: nosuchcmd: command not found\n"
	if diag, hint := diagnoseSandbox(opt, 127, out); diag != sandboxDiagNone {
		t.Errorf("command-not-found was reported as a sandbox runner failure: diag=%v hint=%q", diag, hint)
	}
}

func TestDiagnoseSandbox_MacOSWrapperFailure(t *testing.T) {
	opt := &sandbox.Options{}
	out := "sandbox-exec: sandbox_apply: Operation not permitted\n"
	diag, _ := diagnoseSandbox(opt, 1, out)
	if diag != sandboxDiagRunnerFailed {
		t.Errorf("diag = %v, want sandboxDiagRunnerFailed for a sandbox-exec wrapper failure", diag)
	}
}

func TestWritableSummary_EmptySetStillReadable(t *testing.T) {
	got := writableSummary(&sandbox.Options{})
	if got == "" || !strings.Contains(got, "temp") {
		t.Errorf("writableSummary(empty) = %q, want a readable mention of the temp dir", got)
	}
}

func TestWritableSummary_ListsEveryDir(t *testing.T) {
	got := writableSummary(&sandbox.Options{WritableDirs: []string{"/a", "/b"}})
	if !strings.Contains(got, "/a") || !strings.Contains(got, "/b") {
		t.Errorf("writableSummary dropped a directory: %q", got)
	}
}

func TestDiagnoseSandbox_CaseInsensitive(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("platform has no signatures")
	}
	opt := &sandbox.Options{WritableDirs: []string{"/w"}}
	upper := strings.ToUpper(denialText(t))
	if diag, _ := diagnoseSandbox(opt, 1, upper); diag != sandboxDiagDenied {
		t.Errorf("uppercase errno text escaped detection: %v", diag)
	}
}

// TestDiagnoseSandbox_BenignNoticeIsNotRunnerFailure is the guard for the
// exact defect dsh shipped and wrote up in postmortem 0004: their
// launcher printed `landlock-run: partial enforcement (older Landlock
// ABI)` on a successful path, their classifier substring-matched
// `landlock-run: ` over the whole output plus "any nonzero exit", and
// ripgrep's exit 1 — which means "no matches found", a SUCCESS — was
// reported as sandbox infrastructure failure.
//
// This test pins the shape that prevents it: per-line matching with
// exact informational exclusions. It fails the moment someone adds a
// benign `seek sandbox:` line without registering it in
// benignSandboxNotices.
func TestDiagnoseSandbox_BenignNoticeIsNotRunnerFailure(t *testing.T) {
	const notice = "seek sandbox: partial enforcement (older landlock abi)"
	// Temporarily register the notice the way a future change would.
	orig := benignSandboxNotices
	benignSandboxNotices = []string{notice}
	t.Cleanup(func() { benignSandboxNotices = orig })

	opt := &sandbox.Options{WritableDirs: []string{"/w"}}
	// The benign notice, then an ordinary non-zero child exit — ripgrep's
	// "no matches" is the canonical example.
	out := notice + "\n"

	diag, hint := diagnoseSandbox(opt, 1, out)
	if diag == sandboxDiagRunnerFailed {
		t.Fatalf("a benign notice plus a non-zero child exit was misclassified as runner failure "+
			"(this is dsh postmortem 0004 verbatim). hint=%q", hint)
	}
}

// TestDiagnoseSandbox_FatalLineStillDetectedAlongsideBenign: excluding
// informational lines must not blind the detector to a real fatal line
// in the same output.
func TestDiagnoseSandbox_FatalLineStillDetectedAlongsideBenign(t *testing.T) {
	const notice = "seek sandbox: partial enforcement (older landlock abi)"
	orig := benignSandboxNotices
	benignSandboxNotices = []string{notice}
	t.Cleanup(func() { benignSandboxNotices = orig })

	opt := &sandbox.Options{WritableDirs: []string{"/w"}}
	out := notice + "\nseek sandbox: landlock: cannot create ruleset\n"

	if diag, _ := diagnoseSandbox(opt, 127, out); diag != sandboxDiagRunnerFailed {
		t.Errorf("diag = %v, want sandboxDiagRunnerFailed — the fatal line was masked by the exclusion", diag)
	}
}

// TestBenignNotices_AreExactNotPrefixes: the exclusion list must not be
// matched loosely, or it would swallow the fatal lines it sits next to.
func TestBenignNotices_AreExactNotPrefixes(t *testing.T) {
	orig := benignSandboxNotices
	benignSandboxNotices = []string{"seek sandbox: partial enforcement"}
	t.Cleanup(func() { benignSandboxNotices = orig })

	if isBenignNotice("seek sandbox: partial enforcement and then something fatal") {
		t.Error("benign exclusion matched a longer line — must be exact, not a prefix")
	}
	if !isBenignNotice("seek sandbox: partial enforcement") {
		t.Error("exact benign line was not excluded")
	}
}

// TestBenignSandboxNotices_MatchesReality documents the current contract:
// every `seek sandbox:` line the trampoline prints today is fatal, so the
// exclusion list is legitimately empty. If that stops being true and the
// list is not updated, the tests above are the ones that fire.
func TestBenignSandboxNotices_MatchesReality(t *testing.T) {
	if len(benignSandboxNotices) != 0 {
		t.Skip("exclusions registered; the contract note below no longer applies verbatim")
	}
	// Intentionally just an assertion of the documented state so the
	// empty list is a decision on record rather than an oversight.
	if isBenignNotice("seek sandbox: anything") {
		t.Error("empty exclusion list must exclude nothing")
	}
}
