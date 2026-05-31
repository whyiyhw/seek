//go:build linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLandlock_ConfinesWrites verifies the kernel-level guarantee that
// powers autopilot's "can't escape the worktree" promise on Linux: under
// applyLandlock, a write inside the allowed dir succeeds and a write
// outside is DENIED. landlock is irreversible per-process, so the actual
// confinement runs in a re-exec'd child — the parent only asserts.
func TestLandlock_ConfinesWrites(t *testing.T) {
	if os.Getenv("SEEK_SB_CHILD") == "1" {
		runSandboxChild()
		return // unreachable — runSandboxChild always os.Exit's
	}
	if !Available() {
		t.Skip("landlock unavailable on this kernel")
	}
	// The denied dir must be OUTSIDE the system temp (applyLandlock grants
	// temp), so anchor under $HOME.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	root, err := os.MkdirTemp(home, ".seek-ll-")
	if err != nil {
		t.Skipf("cannot create test root under home: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	allowed := filepath.Join(root, "allowed")
	denied := filepath.Join(root, "denied")
	for _, d := range []string{allowed, denied} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run", "^TestLandlock_ConfinesWrites$")
	cmd.Env = append(os.Environ(),
		"SEEK_SB_CHILD=1",
		"SEEK_SB_ALLOWED="+allowed,
		"SEEK_SB_DENIED="+denied,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed child did not exit cleanly: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(allowed, "in")); err != nil {
		t.Fatalf("write inside the allowed dir should have landed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(denied, "out")); err == nil {
		t.Fatalf("write outside the allowed dir must be denied — landlock leak\n%s", out)
	}
}

// runSandboxChild applies landlock to itself, then proves the boundary by
// writing inside (must succeed) and outside (must fail). Exit code 0 only
// if both held; non-zero otherwise (with a reason on stderr).
func runSandboxChild() {
	allowed := os.Getenv("SEEK_SB_ALLOWED")
	denied := os.Getenv("SEEK_SB_DENIED")
	if err := applyLandlock(Options{WritableDirs: []string{allowed}}); err != nil {
		os.Stderr.WriteString("child: applyLandlock: " + err.Error() + "\n")
		os.Exit(10)
	}
	if err := os.WriteFile(filepath.Join(allowed, "in"), []byte("x"), 0o644); err != nil {
		os.Stderr.WriteString("child: allowed write was denied: " + err.Error() + "\n")
		os.Exit(11)
	}
	if err := os.WriteFile(filepath.Join(denied, "out"), []byte("x"), 0o644); err == nil {
		os.Stderr.WriteString("child: write outside writable dirs SUCCEEDED (sandbox leak)\n")
		os.Exit(12)
	}
	os.Exit(0)
}

// TestEncodeDecodeOptions is platform-independent round-trip coverage for
// the trampoline's argv-safe Options encoding.
func TestEncodeDecodeOptions(t *testing.T) {
	in := Options{WritableDirs: []string{"/a/b", "/c d/e"}, AllowNetwork: true}
	got, err := decodeOptions(encodeOptions(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.WritableDirs) != 2 || got.WritableDirs[1] != "/c d/e" || !got.AllowNetwork {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if _, err := decodeOptions("!!!not-base64!!!"); err == nil {
		t.Fatal("expected error decoding garbage")
	}
}
