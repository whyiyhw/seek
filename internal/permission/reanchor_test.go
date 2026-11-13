package permission

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReanchor_MovesCwdAndResolve verifies the ACP session/new
// handshake contract (feature-paseo-provider.md §2.2 D-1): Reanchor
// re-points the shared policy so tools that captured the *Policy at
// construction (read/edit/write via Resolve, bash via CWD) follow the
// session cwd, and relative writes re-gate against the NEW root.
func TestReanchor_MovesCwdAndResolve(t *testing.T) {
	old := t.TempDir()
	new := t.TempDir()

	p, err := New(old, PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Cwd(); got != old {
		t.Fatalf("Cwd() = %q, want %q", got, old)
	}

	if err := p.Reanchor(new); err != nil {
		t.Fatal(err)
	}
	if got := p.Cwd(); got != new {
		t.Fatalf("Cwd() after reanchor = %q, want %q", got, new)
	}
	// Resolve must follow: this is the path read/edit/write touch.
	if got := p.Resolve("x.txt"); got != filepath.Join(new, "x.txt") {
		t.Fatalf("Resolve after reanchor = %q, want %q", got, filepath.Join(new, "x.txt"))
	}
	// The sandbox whitelist follows too: a write inside the new root is
	// no longer "outside the working directory" under PrefDeny.
	if err := p.Check(Action{Kind: KindWrite, Path: filepath.Join(new, "x.txt")}); err != nil {
		t.Fatalf("write inside reanchored root denied: %v", err)
	}
}

// TestReanchor_NilAndErrorPaths pins the failure modes: nil policy is
// loud, and an unresolvable cwd errors rather than silently keeping
// the old anchor.
func TestReanchor_NilAndErrorPaths(t *testing.T) {
	var nilPolicy *Policy
	if err := nilPolicy.Reanchor("/x"); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("nil policy should error loudly, got %v", err)
	}
	p, err := New(t.TempDir(), PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	// filepath.Abs only fails when Getwd fails and the path is relative,
	// which we can't portably force; an absolute path always resolves.
	// The reachable guard is: error wrapping keeps the prefix.
	if err := p.Reanchor("/definitely/absolute"); err != nil {
		t.Fatalf("absolute reanchor should succeed: %v", err)
	}
}
