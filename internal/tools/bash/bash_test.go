package bash

import (
	"context"
	"encoding/json"
	"errors"
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

func TestBash_TruncatesHugeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only smoke")
	}
	// 64 KiB of output; should be truncated to 32 KiB.
	out, err := run(t, yolo(t), Args{Command: "yes a | head -c 65536"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "output truncated") {
		t.Errorf("expected truncation: %s", out[:200])
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
