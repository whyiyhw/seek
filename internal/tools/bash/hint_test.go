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

// --- bashAdvisory (success-path nudge) --------------------------------
//
// The comment block in hint.go carries the measurement that deleted four
// of the six classes. These tests pin the SHIPPED set, so re-adding a
// deleted class (grep / git / list_dir / find) is a visible decision that
// has to update a test with a name saying it is gone.

// unlimited is the nil limiter: every class is allowed, no per-session
// state. Class-shape tests use it; the cap has its own tests below.
func unlimited() *advisoryLimiter { return nil }

func TestBashAdvisory_CatSuggestsRead(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"cat README.md",
		"head -20 main.go",
		"tail -100 logs/app.log",
		"cd /repo && cat foo.go",
	} {
		t.Run(cmd, func(t *testing.T) {
			got := bashAdvisory(cmd, unlimited())
			if !strings.Contains(got, "`read` tool") {
				t.Errorf("expected read suggestion for %q, got: %s", cmd, got)
			}
		})
	}
}

func TestBashAdvisory_CDPrefixFlagged(t *testing.T) {
	t.Parallel()
	got := bashAdvisory("cd /repo && ls foo/", unlimited())
	if !strings.Contains(got, "drop the `cd` prefix") {
		t.Errorf("missing cd suggestion for cd && ls: %s", got)
	}
}

// TestBashAdvisory_DeletedClassesStayDeleted pins the measurement's
// outcome. Each of these classes fired thousands of times across the
// 40-session baseline and moved the hinted behaviour by ~0pp; the
// advisory for them is gone. If one comes back, this test fails and the
// re-adder has to bring a new measurement with it.
func TestBashAdvisory_DeletedClassesStayDeleted(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"grep -rn foo .",      // was: use the grep tool        (1,447 firings)
		"rg foo",              //   ditto                       (same class)
		"ag foo",              //   ditto                       (same class)
		"git log --oneline",   // was: use the git tool        (841 firings)
		"git status",          //   ditto
		"ls docs/prd/",        // was: use the list_dir tool   (291 firings)
		"find . -name '*.go'", // was: use grep/list_dir     (14 firings)
	} {
		t.Run(cmd, func(t *testing.T) {
			if got := bashAdvisory(cmd, unlimited()); got != "" {
				t.Errorf("%q must produce NO advisory (class deleted 2026-11-22); got: %s", cmd, got)
			}
		})
	}
}

// TestBashAdvisory_CappedOncePerClassPerSession is the cost cap. One
// session in the baseline fired the cd advisory 574 times; a nudge that
// fires every call is transcript bytes on every later request.
func TestBashAdvisory_CappedOncePerClassPerSession(t *testing.T) {
	t.Parallel()
	l := &advisoryLimiter{}

	if got := bashAdvisory("cat a.go", l); !strings.Contains(got, "`read` tool") {
		t.Fatalf("first read-class firing should carry the advice, got: %q", got)
	}
	if got := bashAdvisory("cat b.go", l); got != "" {
		t.Errorf("second read-class firing in the same session must be silent, got: %q", got)
	}

	// A different class has its own budget.
	if got := bashAdvisory("cd /elsewhere && ls", l); !strings.Contains(got, "drop the `cd` prefix") {
		t.Errorf("cd class must fire independently of the read class, got: %q", got)
	}
	if got := bashAdvisory("cd /another && pwd", l); got != "" {
		t.Errorf("second cd-class firing must be silent, got: %q", got)
	}

	if n := len(l.firedClasses()); n != 2 {
		t.Errorf("firedClasses = %d, want 2 (one per class)", n)
	}
}

// TestBashAdvisory_NilLimiterIsUncapped documents the zero-value Tool
// path: a Tool{} built in a test has no limiter, and must not nil-panic.
func TestBashAdvisory_NilLimiterIsUncapped(t *testing.T) {
	t.Parallel()
	for range 3 {
		if got := bashAdvisory("cat a.go", nil); !strings.Contains(got, "`read` tool") {
			t.Fatalf("nil limiter must allow every firing, got: %q", got)
		}
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
		"bazel build //...",
	} {
		t.Run(cmd, func(t *testing.T) {
			if got := bashAdvisory(cmd, unlimited()); got != "" {
				t.Errorf("opaque command %q should produce empty advisory, got: %s", cmd, got)
			}
		})
	}
}

func TestBashAdvisory_EmptyCommand(t *testing.T) {
	t.Parallel()
	if got := bashAdvisory("", unlimited()); got != "" {
		t.Errorf("empty command → empty advisory, got: %q", got)
	}
	if got := bashAdvisory("   ", unlimited()); got != "" {
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
