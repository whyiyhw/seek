//go:build darwin

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileFor_Shape(t *testing.T) {
	p := profileFor(Options{WritableDirs: []string{"/some/dir"}})
	for _, want := range []string{"(version 1)", "(allow default)", "(deny file-write*)", "(allow file-write*", "(deny network*)"} {
		if !strings.Contains(p, want) {
			t.Fatalf("profile missing %q:\n%s", want, p)
		}
	}
	// AllowNetwork=true must NOT emit the network deny.
	if strings.Contains(profileFor(Options{AllowNetwork: true}), "(deny network*)") {
		t.Fatal("AllowNetwork should not deny network")
	}
}

// The load-bearing test: a write OUTSIDE the writable set is denied by the
// kernel. Runs real sandbox-exec on this Mac.
func TestSeatbelt_ConfinesWrites(t *testing.T) {
	if !Available() {
		t.Skip("sandbox-exec unavailable")
	}
	// Use a root under HOME (NOT the system temp dir, which the profile
	// always allows) so the "denied" path is genuinely outside the
	// writable set.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	root, err := os.MkdirTemp(home, ".seek-sbtest-")
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

	opt := Options{WritableDirs: []string{allowed}}

	// Allowed write succeeds.
	cmd := Wrap(context.Background(), opt, "sh", "-c", "echo ok > "+filepath.Join(allowed, "f"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write to allowed dir should succeed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(allowed, "f")); err != nil {
		t.Fatalf("allowed file not created: %v", err)
	}

	// Write outside the writable set is denied by the sandbox.
	cmd2 := Wrap(context.Background(), opt, "sh", "-c", "echo no > "+filepath.Join(denied, "f"))
	if err := cmd2.Run(); err == nil {
		t.Fatal("write outside writable dirs should be denied by the sandbox")
	}
	if _, err := os.Stat(filepath.Join(denied, "f")); err == nil {
		t.Fatal("denied file must not exist (sandbox should have blocked the write)")
	}
}
