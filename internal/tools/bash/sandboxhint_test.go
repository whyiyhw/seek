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
