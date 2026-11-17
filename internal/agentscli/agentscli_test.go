package agentscli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withSeekHome points $SEEK_HOME at a temp dir for the duration of
// the test so list/check/create/remove never see the real user's
// ~/.seek (same isolation as skillcli's tests). Returns the temp home.
func withSeekHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	prev := os.Getenv("SEEK_HOME")
	t.Cleanup(func() { os.Setenv("SEEK_HOME", prev) })
	if err := os.Setenv("SEEK_HOME", home); err != nil {
		t.Fatal(err)
	}
	return home
}

// inProject chdir's into a fresh temp project for the duration of the
// test so the project scope resolves to it, not to the repo checkout
// running the tests.
func inProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const verifierMD = `---
name: verifier
description: Runs the test suite and reports failures.
tools: [read, grep, git]
---
You are a test verifier subagent. Run the suite, report failures.
`

// ---------- check ----------

// TestCheck_IllegalFrontmatterReported: the M-O2.3 acceptance case —
// an agents/*.md with illegal frontmatter must surface as a check
// error (message + non-zero exit), while the valid sibling file still
// counts in the per-source summary.
func TestCheck_IllegalFrontmatterReported(t *testing.T) {
	withSeekHome(t)
	project := inProject(t)
	projAgents := filepath.Join(project, ".seek", "agents")
	write(t, filepath.Join(projAgents, "verifier.md"), verifierMD)
	write(t, filepath.Join(projAgents, "illegal.md"),
		"---\nname: illegal\ndescription: bad\nworkflow: plan-execute\n---\nBody.")

	var out, errBuf bytes.Buffer
	err := Run([]string{"check"}, &out, &errBuf)
	if err == nil {
		t.Fatal("check exited clean, want error for illegal frontmatter")
	}
	stdout := out.String()
	if !strings.Contains(stdout, "plan-execute") || !strings.Contains(stdout, "illegal.md") {
		t.Errorf("check output missing the illegal-file report:\n%s", stdout)
	}
	if !strings.Contains(stdout, "project .seek: 1") {
		t.Errorf("check output missing the per-source count for the valid file:\n%s", stdout)
	}
}

// TestCheck_UserScopeFileAndCleanRun: a valid user-scope file loads
// clean (exit 0), and user-scope errors are reported too — check
// covers BOTH scopes, not just the project one.
func TestCheck_UserScopeFileAndCleanRun(t *testing.T) {
	home := withSeekHome(t)
	inProject(t)
	write(t, filepath.Join(home, "agents", "librarian.md"),
		"---\nname: librarian\ndescription: finds docs\n---\nIndex things.")

	var out, errBuf bytes.Buffer
	if err := Run([]string{"check"}, &out, &errBuf); err != nil {
		t.Fatalf("clean check errored: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ok — all agent definitions valid") {
		t.Errorf("missing ok line:\n%s", out.String())
	}

	write(t, filepath.Join(home, "agents", "broken.md"),
		"---\nname: broken\ndescription: d\ntools: [ask_user]\n---\nBody.")
	out.Reset()
	if err := Run([]string{"check"}, &out, &errBuf); err == nil {
		t.Fatal("check exited clean despite user-scope error")
	}
	if !strings.Contains(out.String(), "broken.md") {
		t.Errorf("user-scope error not reported:\n%s", out.String())
	}
}

// TestCheck_EmptyDirs: the common case — no definition files at all —
// is not an error, just an informational line.
func TestCheck_EmptyDirs(t *testing.T) {
	withSeekHome(t)
	inProject(t)
	var out, errBuf bytes.Buffer
	if err := Run([]string{"check"}, &out, &errBuf); err != nil {
		t.Fatalf("empty check errored: %v", err)
	}
	if !strings.Contains(out.String(), "no agent definition files found") {
		t.Errorf("missing empty-state line:\n%s", out.String())
	}
}

// ---------- list ----------

// TestList_ShowsScopesAndShadowing: list renders the same registry
// view startup builds — custom types first, built-ins last, cross-
// scope shadowing visible in the footer.
func TestList_ShowsScopesAndShadowing(t *testing.T) {
	home := withSeekHome(t)
	project := inProject(t)
	write(t, filepath.Join(project, ".seek", "agents", "verifier.md"), verifierMD)
	write(t, filepath.Join(project, ".seek", "agents", "explore.md"),
		"---\nname: explore\ndescription: project-flavoured explorer\n---\nYou explore harder.")
	write(t, filepath.Join(home, "agents", "explore.md"),
		"---\nname: explore\ndescription: user-flavoured explorer\n---\nYou explore.")
	write(t, filepath.Join(home, "agents", "librarian.md"),
		"---\nname: librarian\ndescription: finds docs\n---\nIndex things.")

	var out, errBuf bytes.Buffer
	if err := Run([]string{"list"}, &out, &errBuf); err != nil {
		t.Fatalf("list errored: %v\n%s", err, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{
		// verifier + explore (project wins over user — one entry) +
		// librarian + 3 builtins.
		"5 agent type(s)",
		"verifier",
		"librarian",
		"project-flavoured explorer",
		"general-purpose",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q:\n%s", want, got)
		}
	}
	// The shadowed user explore must not be the active row: its
	// description is gone from the table, its path surfaces only in
	// the shadow footer.
	if strings.Contains(got, "user-flavoured explorer") {
		t.Errorf("shadowed description still rendered as a row:\n%s", got)
	}
	if !strings.Contains(got, "explore overrides:") {
		t.Errorf("shadow footer missing:\n%s", got)
	}
	shadowLine := got[strings.Index(got, "explore overrides:"):]
	if !strings.Contains(shadowLine, filepath.Join("agents", "explore.md")) {
		t.Errorf("shadow footer missing the user-scope path:\n%s", shadowLine)
	}
}

// ---------- create / remove (the CLI write path) ----------

// TestCreateAndRemove_UserScope: end-to-end through the verbs — create
// writes a file the loader accepts, remove deletes it again. Both
// scoped to SEEK_HOME so the real user dir is never touched.
func TestCreateAndRemove_UserScope(t *testing.T) {
	home := withSeekHome(t)
	project := inProject(t)

	var out, errBuf bytes.Buffer
	err := Run([]string{"create", "verifier", "--description", "Runs the test suite.",
		"--tools", "read,grep,git", "--workflow", "plan-analyze"}, &out, &errBuf)
	if err != nil {
		t.Fatalf("create errored: %v\n%s", err, errBuf.String())
	}
	wantPath := filepath.Join(home, "agents", "verifier.md")
	if !strings.Contains(out.String(), wantPath) {
		t.Errorf("create output missing %s:\n%s", wantPath, out.String())
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("created file not on disk: %v", err)
	}

	// check must now be clean — the scaffold is immediately loadable.
	out.Reset()
	if err := Run([]string{"check"}, &out, &errBuf); err != nil {
		t.Fatalf("check rejected the scaffold: %v\n%s", err, out.String())
	}

	out.Reset()
	if err := Run([]string{"remove", "verifier"}, &out, &errBuf); err != nil {
		t.Fatalf("remove errored: %v\n%s", err, errBuf.String())
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Errorf("file still present after remove: %v", err)
	}
	// The project tree must be untouched by the user-scope verbs.
	if entries, _ := os.ReadDir(project); len(entries) != 0 {
		t.Errorf("project dir polluted by user-scope create/remove: %v", entries)
	}
}

// TestCreate_ProjectScopeAndIllegalInput: --project lands in
// <cwd>/.seek/agents/; illegal frontmatter knobs are refused by the
// underlying Parse round-trip and nothing is written.
func TestCreate_ProjectScopeAndIllegalInput(t *testing.T) {
	withSeekHome(t)
	project := inProject(t)

	var out, errBuf bytes.Buffer
	if err := Run([]string{"create", "auditor", "--description", "d", "--body", "You audit.",
		"--project"}, &out, &errBuf); err != nil {
		t.Fatalf("create --project errored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".seek", "agents", "auditor.md")); err != nil {
		t.Fatalf("project-scope file missing: %v", err)
	}

	errBuf.Reset()
	if err := Run([]string{"create", "loose", "--description", "d", "--body", "b",
		"--workflow", "plan-execute", "--project"}, &out, &errBuf); err == nil {
		t.Fatal("create accepted a loosening workflow")
	} else if !strings.Contains(err.Error(), "plan-analyze") {
		t.Errorf("error lacks workflow context: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".seek", "agents", "loose.md")); !os.IsNotExist(err) {
		t.Error("refused create still wrote a file")
	}
}

// TestRemove_BuiltinRefused: built-in names get the clear refusal,
// not a misleading "not found".
func TestRemove_BuiltinRefused(t *testing.T) {
	withSeekHome(t)
	inProject(t)
	var out, errBuf bytes.Buffer
	err := Run([]string{"remove", "explore"}, &out, &errBuf)
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("remove explore err = %v, want built-in refusal", err)
	}
}

// ---------- dispatcher ----------

// TestRun_HelpAndUnknownVerb: no args → help on stdout, exit 0;
// unknown verb → error naming the verb.
func TestRun_HelpAndUnknownVerb(t *testing.T) {
	withSeekHome(t)
	inProject(t)
	var out, errBuf bytes.Buffer
	if err := Run(nil, &out, &errBuf); err != nil {
		t.Fatalf("bare Run errored: %v", err)
	}
	if !strings.Contains(out.String(), "Usage: seek agents") {
		t.Errorf("help missing:\n%s", out.String())
	}
	out.Reset()
	errBuf.Reset()
	if err := Run([]string{"frobnicate"}, &out, &errBuf); err == nil {
		t.Fatal("unknown verb accepted")
	}
	if !strings.Contains(errBuf.String(), "frobnicate") {
		t.Errorf("unknown-verb message missing name:\n%s", errBuf.String())
	}
}
