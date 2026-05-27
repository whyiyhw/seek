package bash

import (
	"strings"
	"testing"
)

func TestPlanAnalyzeBashHint_CDPrefix(t *testing.T) {
	t.Parallel()
	got := planAnalyzeBashHint("cd /tmp && ls")
	if !strings.Contains(got, "drop the `cd` prefix") {
		t.Errorf("expected cd-hint, got: %s", got)
	}
}

func TestPlanAnalyzeBashHint_GitViaBash(t *testing.T) {
	t.Parallel()
	cases := []string{
		"git log --oneline",
		"git diff",
		"cd /repo && git status",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := planAnalyzeBashHint(cmd)
			if !strings.Contains(got, "use the `git` tool") {
				t.Errorf("expected git-tool hint for %q, got: %s", cmd, got)
			}
		})
	}
}

func TestPlanAnalyzeBashHint_GoTest(t *testing.T) {
	t.Parallel()
	cases := []string{
		"go test",
		"go test ./...",
		"go test -race ./internal/...",
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := planAnalyzeBashHint(cmd)
			if !strings.Contains(got, "go vet") {
				t.Errorf("expected go-vet suggestion for %q, got: %s", cmd, got)
			}
			if !strings.Contains(got, "side effects") {
				t.Errorf("expected side-effects explanation for %q, got: %s", cmd, got)
			}
		})
	}
}

func TestPlanAnalyzeBashHint_Metachar(t *testing.T) {
	t.Parallel()
	cases := []string{
		"ls && rm -rf /",  // chaining
		"echo 'x' | sh",   // pipe
		"echo $(date)",    // command substitution
		"echo > /tmp/out", // redirect
		"echo `pwd`",      // backtick substitution
	}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			got := planAnalyzeBashHint(cmd)
			if !strings.Contains(got, "metacharacter") {
				t.Errorf("expected metachar hint for %q, got: %s", cmd, got)
			}
		})
	}
}

func TestPlanAnalyzeBashHint_CombinesMultipleHints(t *testing.T) {
	t.Parallel()
	// "cd /x && git log" hits THREE patterns: cd, git, metachar.
	got := planAnalyzeBashHint("cd /x && git log")
	if !strings.Contains(got, "drop the `cd` prefix") {
		t.Errorf("missing cd hint: %s", got)
	}
	if !strings.Contains(got, "use the `git` tool") {
		t.Errorf("missing git hint: %s", got)
	}
	if !strings.Contains(got, "metacharacter") {
		t.Errorf("missing metachar hint: %s", got)
	}
}

func TestPlanAnalyzeBashHint_FallbackForUnknownCommand(t *testing.T) {
	t.Parallel()
	got := planAnalyzeBashHint("docker run alpine")
	if !strings.Contains(got, "whitelisted inspector") {
		t.Errorf("expected fallback to mention inspector option, got: %s", got)
	}
	// Should NOT include the specific-hint phrases (the fallback's
	// "go vet, go list, npm ls" mention is the allowlist enumeration,
	// not the go-test-specific hint, so we check for distinctive
	// phrases rather than just keywords).
	for _, banned := range []string{
		"drop the `cd` prefix",
		"use the `git` tool",
		"runs code",     // unique to the go-test hint
		"metacharacter", // unique to the metachar hint
	} {
		if strings.Contains(got, banned) {
			t.Errorf("fallback should not include %q, got: %s", banned, got)
		}
	}
}

// TestPlanAnalyzeBashHint_FallbackOffersEscapePaths is the regression
// test for the smoke-test issue where the model, faced with
// bash("curl ...") being denied, suggested `--yolo` restart instead
// of in-session options. The fallback hint must steer the model
// toward propose(), Shift+Tab, or /yolo — and explicitly NOT toward
// restarting.
func TestPlanAnalyzeBashHint_FallbackOffersEscapePaths(t *testing.T) {
	t.Parallel()
	got := planAnalyzeBashHint("curl https://example.com")
	for _, want := range []string{
		"propose",     // path 1: plan-execute via propose
		"Shift+Tab",   // path 2: in-session mode cycle
		"/yolo",       // path 2 alt: slash command toggle
		"NEVER",       // path 3 anti-pattern: no --yolo restart suggestion
		"--yolo flag", // explicitly disclaimed
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fallback should mention %q, got: %s", want, got)
		}
	}
}

func TestPlanAnalyzeBashHint_EmptyCommand(t *testing.T) {
	t.Parallel()
	if got := planAnalyzeBashHint(""); got != "" {
		t.Errorf("empty command → empty hint, got: %q", got)
	}
	if got := planAnalyzeBashHint("   "); got != "" {
		t.Errorf("whitespace-only → empty hint, got: %q", got)
	}
}

// Smoke: helpers stay structurally accurate.
func TestHintCDPrefix_TabSeparator(t *testing.T) {
	t.Parallel()
	// Tab after cd should also trigger.
	if !hintCDPrefix("cd\t/tmp") {
		t.Errorf("hintCDPrefix should accept tab separator")
	}
	if hintCDPrefix("cdr foo") {
		t.Errorf("hintCDPrefix must not match 'cdr' (no separator)")
	}
}

func TestHintGitViaBash_StandaloneVsSubword(t *testing.T) {
	t.Parallel()
	if !hintGitViaBash("git status") {
		t.Errorf("expected match for 'git status'")
	}
	if hintGitViaBash("github clone foo") {
		t.Errorf("must not match 'github' (substring)")
	}
	if !hintGitViaBash("cd /x && git log") {
		t.Errorf("expected match after && separator")
	}
	if !hintGitViaBash("ls | git apply") {
		t.Errorf("expected match after pipe separator")
	}
}

func TestHintGoTest_RequiresAdjacentTokens(t *testing.T) {
	t.Parallel()
	if !hintGoTest("go test ./...") {
		t.Errorf("expected match for 'go test'")
	}
	if hintGoTest("gotest ./...") {
		t.Errorf("must not match 'gotest' (single token)")
	}
	if hintGoTest("test go") {
		t.Errorf("must not match wrong order 'test go'")
	}
}
