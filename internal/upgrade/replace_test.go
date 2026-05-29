//go:build !windows

package upgrade

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReplaceBinary_PermissionDeniedHintsSudo is the load-bearing pin for
// the user-experience fix: when seek -upgrade can't write to its install
// directory (the common /usr/local/bin case for any user who didn't run
// the upgrade with sudo), the error must say "try: sudo seek -upgrade"
// rather than the raw "permission denied" that Go's os.Rename returns by
// default. The previous error required users to recognize EACCES and
// independently figure out the elevation path.
//
// Strategy: make a target directory read-only via chmod 0o555, then try
// to rename a file INTO it. POSIX requires write permission on the
// parent dir to create / replace / delete entries, so the rename fails
// with EACCES — which Go's stdlib maps to fs.ErrPermission, which
// replaceBinary now detects and converts to the helpful message.
func TestReplaceBinary_PermissionDeniedHintsSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		// root bypasses the directory's write-bit check entirely; the
		// rename would succeed and the test would prove nothing. The
		// real bug only ever surfaces for non-root users, which is also
		// the only audience that needs the sudo hint.
		t.Skip("test must run as non-root user; root bypasses directory write bits")
	}

	dir := t.TempDir()

	// Build the source file (the "new" binary that wants to land at
	// targetPath). Plain temp file, content irrelevant.
	src := filepath.Join(dir, "seek.new")
	if err := os.WriteFile(src, []byte("new-binary-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Build the locked target directory + the pre-existing target file
	// inside it (simulating /usr/local/bin/seek as it would exist
	// post-install).
	targetDir := filepath.Join(dir, "locked-install-dir")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "seek")
	if err := os.WriteFile(target, []byte("old-binary-bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Lock the dir: r-x for owner, no write. Rename within it now fails
	// with EACCES.
	if err := os.Chmod(targetDir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore writeability at teardown so t.TempDir's auto-cleanup can
	// remove the directory contents. Without this, the test harness
	// itself hits EACCES trying to remove the locked dir.
	t.Cleanup(func() { _ = os.Chmod(targetDir, 0o755) })

	err := replaceBinary(src, target)
	if err == nil {
		t.Fatal("expected permission error from rename into read-only dir; got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "sudo seek -upgrade") {
		t.Errorf("error message missing the actionable sudo hint.\n  got:  %q\n  want substring: %q", msg, "sudo seek -upgrade")
	}
	if !strings.Contains(msg, "elevated privileges") {
		t.Errorf("error message missing the 'elevated privileges' phrasing that explains WHY sudo is needed.\n  got: %q", msg)
	}
	if !strings.Contains(msg, target) {
		t.Errorf("error message should name the offending target path so the user knows which dir is locked.\n  got:  %q\n  want substring: %q", msg, target)
	}
}

// TestReplaceBinary_NonPermissionErrorPreservesWrap covers the inverse
// branch: when the rename fails for a NON-permission reason (e.g. the
// source file doesn't exist), the original error wrap is preserved so
// the underlying cause stays visible via errors.Is / errors.As. We
// don't want the sudo-hint code path to swallow other errors.
func TestReplaceBinary_NonPermissionErrorPreservesWrap(t *testing.T) {
	dir := t.TempDir()
	// src doesn't exist — rename will fail with fs.ErrNotExist.
	src := filepath.Join(dir, "does-not-exist")
	target := filepath.Join(dir, "target")

	err := replaceBinary(src, target)
	if err == nil {
		t.Fatal("expected error from rename of nonexistent file")
	}
	if strings.Contains(err.Error(), "sudo seek -upgrade") {
		t.Errorf("non-permission error incorrectly rewrote to sudo hint: %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("non-permission error must preserve the underlying error chain for errors.Is checks; got: %v", err)
	}
}

// TestReplaceBinary_HappyPath verifies the success path still works —
// adding the EACCES detection branch must not accidentally break the
// normal rename.
func TestReplaceBinary_HappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "seek.new")
	if err := os.WriteFile(src, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "seek")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(src, target); err != nil {
		t.Fatalf("happy-path rename failed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target content after rename = %q, want %q", string(got), "new")
	}
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source should be gone after successful rename; got: %v", err)
	}
}
