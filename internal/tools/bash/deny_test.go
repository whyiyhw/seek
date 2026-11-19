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

// Two things are contractual in a guard refusal (c_ajza7r): the
// "[bash: blocked — " prefix (wire format — the model and the tests
// match on it, same class as "[plan: approved]") and the Hint: line
// that the permission deny path also carries, so both refusal families
// tell the model what to do next instead of leaving "find a rewrite"
// as the only visible move.
func TestBash_WithDeny_KeepsPrefixAndAddsHint(t *testing.T) {
	p, _ := permission.New(t.TempDir(), permission.PrefYolo)
	tool := New(p).WithDeny(func(string) (bool, string) {
		return true, "the acp lane refused this"
	})
	out, err := run(t, tool, Args{Command: "git push origin main"})
	if err != nil {
		t.Fatalf("deny should be a result, not a fatal error: %v", err)
	}
	if !strings.HasPrefix(out, "[bash: blocked — ") {
		t.Errorf("blocked prefix is wire format and must not change: %q", out)
	}
	if !strings.Contains(out, "\nHint: ") {
		t.Errorf("guard refusal should carry a Hint: line like plan-analyze: %q", out)
	}
	if !strings.Contains(out, "the acp lane refused this") {
		t.Errorf("reason must reach the model verbatim: %q", out)
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
