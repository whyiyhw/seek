//go:build darwin

package bash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/sandbox"
)

// A bash tool with WithSandbox must confine writes to the allowed dirs —
// a write outside is kernel-denied (the write that powers autopilot's
// "can't escape the repo" guarantee on macOS). Real sandbox-exec.
func TestBash_WithSandbox_ConfinesWrites(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("sandbox-exec unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	root, err := os.MkdirTemp(home, ".seek-bashsb-") // outside system temp
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	allowed := filepath.Join(root, "allowed")
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(denied, 0o755); err != nil {
		t.Fatal(err)
	}

	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	tool := New(p).WithSandbox(sandbox.Options{WritableDirs: []string{allowed}})

	// Write inside the allowed dir succeeds.
	out, err := run(t, tool, Args{Command: "echo ok > " + filepath.Join(allowed, "f")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit=0") {
		t.Fatalf("allowed write should succeed: %s", out)
	}
	if _, err := os.Stat(filepath.Join(allowed, "f")); err != nil {
		t.Fatalf("allowed file not created: %v", err)
	}

	// Write outside is denied by the sandbox (non-zero exit, no file).
	out2, _ := run(t, tool, Args{Command: "echo no > " + filepath.Join(denied, "f")})
	if strings.Contains(out2, "exit=0") {
		t.Fatalf("write outside writable dirs should be denied: %s", out2)
	}
	if _, err := os.Stat(filepath.Join(denied, "f")); err == nil {
		t.Fatal("denied file must not exist (sandbox should block the write)")
	}
}

func TestBash_NoSandbox_Unchanged(t *testing.T) {
	// Without WithSandbox, behavior is the plain shell (regression guard).
	out, err := run(t, yolo(t), Args{Command: "echo plain"})
	if err != nil || !strings.Contains(out, "plain") {
		t.Fatalf("no-sandbox bash must be unchanged: %q err=%v", out, err)
	}
}

// TestBash_SandboxDenial_IsAttributed closes the loop that the unit
// tests in sandboxhint_test.go cannot: it drives a REAL seatbelt denial
// and asserts the resulting output carries the attribution hint.
//
// The signatures in denialSignatures() are a claim about what the kernel
// and shell actually print. Only a real denial can validate that claim —
// if macOS ever changes the errno text, this test fails and the hint
// stops silently working instead of silently lying.
func TestBash_SandboxDenial_IsAttributed(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("sandbox-exec unavailable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	root, err := os.MkdirTemp(home, ".seek-bashsb-hint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	allowed := filepath.Join(root, "allowed")
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(denied, 0o755); err != nil {
		t.Fatal(err)
	}

	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	tool := New(p).WithSandbox(sandbox.Options{WritableDirs: []string{allowed}})

	out, _ := run(t, tool, Args{Command: "echo no > " + filepath.Join(denied, "f")})
	if !strings.Contains(out, "[sandbox:") {
		t.Fatalf("a real seatbelt denial produced no attribution hint — the errno signature no longer matches "+
			"what the shell prints. Raw output:\n%s", out)
	}
	if !strings.Contains(out, allowed) {
		t.Errorf("hint does not name the writable directory:\n%s", out)
	}

	// And the converse: an ordinary failure inside the sandbox must NOT
	// be attributed to it.
	out2, _ := run(t, tool, Args{Command: "exit 3"})
	if strings.Contains(out2, "[sandbox:") {
		t.Errorf("an ordinary non-zero exit was blamed on the sandbox:\n%s", out2)
	}
}
