package bash

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

func TestBash_WithDeny_BlocksMatching(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	deny := func(cmd string) (bool, string) {
		if strings.Contains(cmd, "git push") {
			return true, "no remote ops"
		}
		return false, ""
	}
	tool := New(p).WithDeny(deny)

	out, err := run(t, tool, Args{Command: "git push origin main"})
	if err != nil {
		t.Fatalf("deny should be a result, not a fatal error: %v", err)
	}
	if !strings.Contains(out, "blocked") || !strings.Contains(out, "no remote ops") {
		t.Fatalf("blocked result missing reason: %q", out)
	}
}

func TestBash_WithDeny_AllowsNonMatching(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	tool := New(p).WithDeny(func(cmd string) (bool, string) {
		return strings.Contains(cmd, "git push"), "no remote"
	})
	out, err := run(t, tool, Args{Command: "echo hello-allowed"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "blocked") || !strings.Contains(out, "hello-allowed") {
		t.Fatalf("non-matching command should run normally: %q", out)
	}
}
