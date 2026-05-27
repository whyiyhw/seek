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

// --- bashAdvisory (success-path teaching) -----------------------------

func TestBashAdvisory_LsSuggestsListDir(t *testing.T) {
	t.Parallel()
	got := bashAdvisory("ls docs/prd/")
	if !strings.Contains(got, "list_dir") {
		t.Errorf("expected list_dir suggestion, got: %s", got)
	}
}

func TestBashAdvisory_CatSuggestsRead(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"cat README.md",
		"head -20 main.go",
		"tail -100 logs/app.log",
	} {
		t.Run(cmd, func(t *testing.T) {
			got := bashAdvisory(cmd)
			if !strings.Contains(got, "`read` tool") {
				t.Errorf("expected read suggestion for %q, got: %s", cmd, got)
			}
		})
	}
}

func TestBashAdvisory_GrepSuggestsGrepTool(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"grep -r foo .",
		"rg foo",
		"ag foo",
	} {
		t.Run(cmd, func(t *testing.T) {
			got := bashAdvisory(cmd)
			if !strings.Contains(got, "`grep` tool") {
				t.Errorf("expected grep tool suggestion for %q, got: %s", cmd, got)
			}
		})
	}
}

func TestBashAdvisory_GitSuggestsGitTool(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"git log --oneline",
		"git status",
		"cd /repo && git diff HEAD",
	} {
		t.Run(cmd, func(t *testing.T) {
			got := bashAdvisory(cmd)
			if !strings.Contains(got, "`git` tool") {
				t.Errorf("expected git tool suggestion for %q, got: %s", cmd, got)
			}
		})
	}
}

func TestBashAdvisory_FindSuggestsGrepOrListDir(t *testing.T) {
	t.Parallel()
	got := bashAdvisory("find . -name '*.go'")
	if !strings.Contains(got, "`grep`") || !strings.Contains(got, "`list_dir`") {
		t.Errorf("expected grep/list_dir suggestion, got: %s", got)
	}
}

func TestBashAdvisory_CDPrefixAlwaysFlagged(t *testing.T) {
	t.Parallel()
	got := bashAdvisory("cd /repo && ls foo/")
	// Both the ls suggestion AND the cd suggestion should fire.
	if !strings.Contains(got, "list_dir") {
		t.Errorf("missing list_dir for cd && ls: %s", got)
	}
	if !strings.Contains(got, "drop the `cd` prefix") {
		t.Errorf("missing cd suggestion for cd && ls: %s", got)
	}
}

func TestBashAdvisory_NoSuggestionForOpaqueCommands(t *testing.T) {
	t.Parallel()
	// Commands that don't match any pattern should produce empty
	// advisory — no "[hint: ...]" trailer appended on the result.
	for _, cmd := range []string{
		"go vet ./...",
		"npm ls",
		"docker run alpine",
		"./my-script.sh",
		"echo hello",
	} {
		t.Run(cmd, func(t *testing.T) {
			if got := bashAdvisory(cmd); got != "" {
				t.Errorf("opaque command %q should produce empty advisory, got: %s", cmd, got)
			}
		})
	}
}

func TestBashAdvisory_EmptyCommand(t *testing.T) {
	t.Parallel()
	if got := bashAdvisory(""); got != "" {
		t.Errorf("empty command → empty advisory, got: %q", got)
	}
	if got := bashAdvisory("   "); got != "" {
		t.Errorf("whitespace-only → empty advisory, got: %q", got)
	}
}

func TestFirstTokenAfterCD_HandlesCdChaining(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fields []string
		want   string
	}{
		{[]string{"ls", "/tmp"}, "ls"}, // no cd
		{[]string{"cd", "/x", "&&", "ls"}, "ls"},
		{[]string{"cd", "/x", ";", "git", "log"}, "git"},
		{[]string{"cd", "/x"}, ""}, // cd with no follow-up
		{[]string{}, ""},           // empty
	}
	for _, c := range cases {
		t.Run(strings.Join(c.fields, " "), func(t *testing.T) {
			if got := firstTokenAfterCD(c.fields); got != c.want {
				t.Errorf("firstTokenAfterCD(%v) = %q, want %q", c.fields, got, c.want)
			}
		})
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
