package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/permission"
)

func yolo(t *testing.T) Tool {
	t.Helper()
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	return New(p)
}

func run(t *testing.T, tool Tool, a Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return tool.Execute(context.Background(), b)
}

func TestBash_DeniedWithoutYolo(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.PrefDeny)
	_, err := run(t, New(p), Args{Command: "echo hi"})
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestBash_EchoUnderYolo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "echo hello-seek"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello-seek") {
		t.Errorf("missing output: %s", out)
	}
	if !strings.Contains(out, "exit=0") {
		t.Errorf("missing exit code: %s", out)
	}
}

// TestBash_PinsWorkingDirectoryToPolicy verifies the contractual
// pinning: bash.Execute must set cmd.Dir to policy.CWD(), NOT
// inherit os.Getwd() at exec time. The system prompt promises the
// model that "bash already runs from the working directory" — this
// test is the load-bearing check that the promise holds even if
// some other code inside the process calls os.Chdir.
func TestBash_PinsWorkingDirectoryToPolicy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	// Build a Policy whose CWD is a fresh temp dir, then run `pwd`.
	// Output must show the temp dir's REAL path (after symlink
	// resolution on macOS where /var → /private/var) — not whatever
	// the test runner's process CWD happens to be.
	dir := t.TempDir()
	p, err := permission.New(dir, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, New(p), Args{Command: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	// macOS resolves /var/folders/... → /private/var/folders/... in pwd
	// output, so trimRight on the expected and check suffix to be
	// platform-agnostic.
	if !strings.Contains(out, strings.TrimPrefix(dir, "/private")) {
		t.Errorf("bash should execute from policy.CWD() = %q; got output:\n%s", dir, out)
	}
}

// TestBash_AppendsAdvisoryOnDedicatedToolPattern is the end-to-end
// regression test for the success-path advisory mechanism. When the
// model uses bash for something a dedicated tool does better
// (`ls`/`cat`/`git`/etc.), the result must carry a `[hint: …]`
// trailer so the model learns the preferred shape on the next turn.
func TestBash_AppendsAdvisoryOnDedicatedToolPattern(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "ls /tmp"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[hint:") {
		t.Errorf("advisory missing — ls(1) should suggest list_dir: %s", out)
	}
	if !strings.Contains(out, "list_dir") {
		t.Errorf("advisory should mention list_dir, got: %s", out)
	}
}

// TestBash_NoAdvisoryForOpaqueCommands verifies the inverse — commands
// that don't match a dedicated-tool pattern must not pollute the
// result with a [hint] line. echo / go vet / arbitrary scripts run
// silently.
func TestBash_NoAdvisoryForOpaqueCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "echo only-output"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[hint:") {
		t.Errorf("echo should not trigger advisory, got: %s", out)
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "false"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "exit=1") {
		t.Errorf("expected exit=1: %s", out)
	}
}

func TestBash_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	out, err := run(t, yolo(t), Args{Command: "sleep 5", TimeoutMS: 200})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "TIMED OUT") {
		t.Errorf("expected timeout marker: %s", out)
	}
}

func TestBash_MissingCommand(t *testing.T) {
	_, err := run(t, yolo(t), Args{Command: ""})
	if err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Errorf("err = %v", err)
	}
}

func TestBash_ElidesMiddleOfHugeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	// 64 KiB of output; the middle should be elided, not the tail.
	out, err := run(t, yolo(t), Args{Command: "yes a | head -c 65536"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "elided") {
		t.Errorf("expected mid-output elision, got: %s", out[:200])
	}
	if len(out) > maxOutputBytes+1024 {
		t.Errorf("clamped output = %d bytes, want ≤ ~%d", len(out), maxOutputBytes)
	}
}

// TestBash_PreservesVerdictAtTail is the regression guard for the defect
// that motivated head+tail clamping: `go test`, `make`, `npm run build`
// all print their verdict LAST. Head-only truncation dropped it and the
// model reported success for a failing run.
func TestBash_PreservesVerdictAtTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	const verdict = "FAIL___build_broke___FAIL"
	out, err := run(t, yolo(t), Args{
		Command: "yes 'compiling some package' | head -c 65536; echo '" + verdict + "'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, verdict) {
		t.Fatalf("verdict line was dropped from over-budget output; tail must survive.\ntail seen: %q", out[max(0, len(out)-200):])
	}
	// And the head must still identify what ran.
	if !strings.Contains(out, "compiling some package") {
		t.Error("head of the output was dropped; head must survive too")
	}
}

// TestBash_ScrubsCredentialsFromChildEnv is the end-to-end guard: the
// model runs `env`, and seek's own API key must not be in the output.
// Before childenv, cmd.Env was nil — Go handed the child the complete
// parent environment, so every credential was one `env` (or one npm
// postinstall) away from the model and from third-party build scripts.
func TestBash_ScrubsCredentialsFromChildEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only: uses `env`")
	}
	t.Setenv("DEEPSEEK_API_KEY", "sk-must-not-leak-to-child")
	t.Setenv("GH_TOKEN", "ghp-must-not-leak-to-child")
	t.Setenv("BASH_TEST_BENIGN", "visible-to-child")

	out, err := run(t, yolo(t), Args{Command: "env"})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-must-not-leak-to-child", "ghp-must-not-leak-to-child"} {
		if strings.Contains(out, secret) {
			t.Errorf("credential leaked into the child environment: %s", secret)
		}
	}
	// Scrubbing must not gut the environment: ordinary variables and PATH
	// have to survive or the shell becomes useless.
	if !strings.Contains(out, "BASH_TEST_BENIGN=visible-to-child") {
		t.Error("benign variable was dropped from the child environment")
	}
	if !strings.Contains(out, "PATH=") {
		t.Error("PATH was dropped from the child environment")
	}
}

func TestBash_EnvPassthroughRestoresNamedVar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only: uses `env`")
	}
	t.Setenv("GH_TOKEN", "ghp-explicitly-allowed")
	t.Setenv("DEEPSEEK_API_KEY", "sk-still-withheld")

	tool := yolo(t).WithEnvPassthrough([]string{"GH_TOKEN"})
	out, err := run(t, tool, Args{Command: "env"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GH_TOKEN=ghp-explicitly-allowed") {
		t.Error("WithEnvPassthrough did not restore the named variable")
	}
	if strings.Contains(out, "sk-still-withheld") {
		t.Error("passthrough of one variable leaked an unrelated credential")
	}
}

func TestClampOutput_UnderBudgetIsUntouched(t *testing.T) {
	in := []byte("short output\nsecond line\n")
	got, elided := clampOutput(in)
	if elided != 0 {
		t.Errorf("elided = %d, want 0", elided)
	}
	if string(got) != string(in) {
		t.Errorf("clampOutput mutated an under-budget output")
	}
}

func TestClampOutput_KeepsBothEnds(t *testing.T) {
	var b strings.Builder
	b.WriteString("HEAD_SENTINEL\n")
	for b.Len() < maxOutputBytes*3 {
		b.WriteString("filler line that is not interesting\n")
	}
	b.WriteString("TAIL_SENTINEL\n")
	in := []byte(b.String())

	got, elided := clampOutput(in)
	if elided <= 0 {
		t.Fatalf("elided = %d, want > 0", elided)
	}
	s := string(got)
	if !strings.HasPrefix(s, "HEAD_SENTINEL\n") {
		t.Error("head sentinel missing — head must be kept verbatim from byte 0")
	}
	if !strings.HasSuffix(s, "TAIL_SENTINEL\n") {
		t.Error("tail sentinel missing — tail must be kept verbatim through the last byte")
	}
	if !strings.Contains(s, "elided") {
		t.Error("elision marker missing")
	}
	if len(got) > maxOutputBytes {
		t.Errorf("clamped to %d bytes, want ≤ %d", len(got), maxOutputBytes)
	}
}

func TestClampOutput_CutsOnLineBoundaries(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < maxOutputBytes*2; i++ {
		fmt.Fprintf(&b, "line-%06d-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", i)
	}
	got, _ := clampOutput([]byte(b.String()))

	head, _, ok := strings.Cut(string(got), "\n\n... [")
	if !ok {
		t.Fatal("elision marker not found")
	}
	if !strings.HasSuffix(head, "\n") {
		t.Errorf("head does not end on a line boundary: %q", head[max(0, len(head)-60):])
	}
	_, tail, ok := strings.Cut(string(got), "] ...\n\n")
	if !ok {
		t.Fatal("elision marker tail delimiter not found")
	}
	if !strings.HasPrefix(tail, "line-") {
		t.Errorf("tail does not start on a line boundary: %q", tail[:min(60, len(tail))])
	}
}

// TestClampOutput_NoNewlinesAtAll covers a single enormous line (minified
// JS, a base64 blob): there is no boundary to cut on, and the function
// must still stay within budget rather than falling back to the whole
// input.
func TestClampOutput_NoNewlinesAtAll(t *testing.T) {
	in := bytes.Repeat([]byte("x"), maxOutputBytes*2)
	got, elided := clampOutput(in)
	if elided <= 0 {
		t.Fatalf("elided = %d, want > 0", elided)
	}
	if len(got) > maxOutputBytes {
		t.Errorf("clamped to %d bytes, want ≤ %d", len(got), maxOutputBytes)
	}
}

func TestClampOutput_ExactlyAtBudget(t *testing.T) {
	in := bytes.Repeat([]byte("y"), maxOutputBytes)
	got, elided := clampOutput(in)
	if elided != 0 {
		t.Errorf("elided = %d, want 0 at exactly the budget", elided)
	}
	if len(got) != maxOutputBytes {
		t.Errorf("len = %d, want %d", len(got), maxOutputBytes)
	}
}

func TestClampOutput_EmptyInput(t *testing.T) {
	got, elided := clampOutput(nil)
	if len(got) != 0 || elided != 0 {
		t.Errorf("clampOutput(nil) = (%q, %d), want (empty, 0)", got, elided)
	}
}

// TestBash_DevTTY_IsInaccessible verifies that child processes launched by
// the bash tool cannot open /dev/tty. Without detachStdin (Setsid), commands
// like sudo, ssh, and git credential helpers can open the controlling
// terminal and steal keystrokes from the TUI, making the session appear frozen
// and resistant to Esc cancellation.
func TestBash_DevTTY_IsInaccessible(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only test")
	}
	// Try to read from /dev/tty — must fail because Setsid detaches
	// the child from the controlling terminal.
	out, err := run(t, yolo(t), Args{Command: "test -r /dev/tty && echo ACCESSIBLE || echo INACCESSIBLE"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "INACCESSIBLE") {
		t.Errorf("expected /dev/tty to be INACCESSIBLE after Setsid, got: %s", out)
	}
}

// TestBash_ContextCancel_KillsProcess verifies that cancelling the parent
// context kills a long-running bash command. Even with Setsid, Esc must be
// able to interrupt a slow process that doesn't touch /dev/tty.
func TestBash_ContextCancel_KillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only test")
	}
	tool := yolo(t)

	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	errCh := make(chan error, 1)
	go func() {
		raw, _ := json.Marshal(Args{Command: "sleep 600", TimeoutMS: 600000})
		_, execErr := tool.Execute(ctx, raw)
		errCh <- execErr
	}()

	// Cancel within 100ms — well before the 10-minute timeout.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-errCh:
		elapsed := time.Since(start)
		if elapsed > 5*time.Second {
			t.Errorf("context cancel took %v to kill sleep — should be near-instant", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("context cancellation did NOT kill the bash process within 10s")
	}
}
