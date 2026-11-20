package verifier

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the R2 过关标准 (v8.md §5): the verifier must judge
// FAIL on the exact shape of the historical bug — "Worktree-isolated
// subagent edited the MAIN tree (isolation silently bypassed)",
// docs/pitfalls.md (Worktree / Windows section), fixed by commit
// c21167d (2026-05-31, v7 柱 O).
//
// The incident's end-state, reproduced verbatim by the fixture below:
//
//   - a subagent spawned with isolation:"worktree" was asked to append
//     a line to README.md;
//   - its report said "→ <worktree>" (success);
//   - the line landed in the MAIN repo's README.md instead;
//   - the worktree stayed untouched;
//   - every test was green (nothing about WHERE the edit landed was
//     under test — that's why the bug shipped).
//
// The verifier must not be foolable by that configuration: green
// tests + a confident worker posture + real isolation breach. The
// judge here is DELIBERATELY gullible (marks every assertion pass,
// citing only the green tests) to prove the catch is mechanical.

// initModuleRepo creates a committed, clean git repo containing a
// tiny Go module whose test suite passes and a README.md — the world
// before the task runs.
func initModuleRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitCfg := [][]string{
		{"init", "-q"},
		{"config", "user.email", "a@b.c"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	}
	for _, args := range gitCfg {
		if out, err := gitRep(t, dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture.example/regression\n\ngo 1.25\n")
	write("pkg/pkg.go", "package pkg\n\nfunc Answer() int { return 42 }\n")
	write("pkg/pkg_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestAnswer(t *testing.T) {\n\tif Answer() != 42 {\n\t\tt.Fatal(\"wrong\")\n\t}\n}\n")
	write("README.md", "# fixture\n\nseed line\n")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "seed"}} {
		if out, err := gitRep(t, dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func gitRep(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// addWorktree branches a real linked worktree off the repo — the
// directory the task was SUPPOSED to confine itself to.
func addWorktree(t *testing.T, repo string) string {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "wt")
	if out, err := gitRep(t, repo, "worktree", "add", "-q", "-b", "fixture/wt", wt, "HEAD"); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	return wt
}

// TestRegression_MainTreePollutionWithGreenTestsIsFail is the R2
// acceptance ①/⑤ gate: fixture-ized, runs in the go-test lane, and
// the verifier's verdict is FAIL with mechanical evidence — despite
// every test exiting 0 and a judge that tried to pass everything.
func TestRegression_MainTreePollutionWithGreenTestsIsFail(t *testing.T) {
	if testing.Short() {
		t.Skip("fixture runs git + a real nested go test")
	}
	repo := initModuleRepo(t)
	wt := addWorktree(t, repo)

	// Baseline as the fleet would have captured it BEFORE the task:
	// the main tree is clean.
	baseline := MainTreeSnapshot(context.Background(), repo)

	// THE INCIDENT: the worker's edit lands in the MAIN README while
	// the worktree stays untouched.
	mainREADME := filepath.Join(repo, "README.md")
	orig, err := os.ReadFile(mainREADME)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainREADME, append(orig, []byte("appended by the worker\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Worktree deliberately untouched: no commit, no edits — the
	// deliverable the fleet would hand the verifier is empty.

	// The gullible judge: every assertion pass, citing only the
	// (genuinely green) tests.
	judge := allPassJudge().passAll(3, "T1")
	ver := NewWithJudge(judge, "fixture-model")

	vd := ver.Verify(context.Background(), Request{
		TaskTitle: "append a line to README.md inside the worktree",
		Assertions: []string{
			"README.md gains the line 'appended by the worker' in the worktree deliverable",
			"the main tree stays clean (worktree isolation preserved)",
			"go test ./... passes",
		},
		WorkDir:       wt,
		Commit:        "", // nothing was delivered in the worktree
		TestCmds:      []string{"go test ./..."},
		ScopeRoot:     repo,
		ScopeBaseline: baseline,
	})

	// The tests really were green — the failure cannot be blamed on
	// a red suite.
	for _, tr := range vd.Tests {
		if tr.ExitCode != 0 || tr.TimedOut || tr.RunErr != "" {
			t.Fatalf("fixture premise broken: test run %+v was not green", tr)
		}
	}

	// The gate: FAIL overall.
	if vd.Overall != Fail {
		t.Fatalf("verdict = %q, want fail — the verifier was fooled by green tests (invariants: %+v)", vd.Overall, vd.Invariants)
	}

	// …and the failure is MECHANICAL: the scope invariant names the
	// drifted path, not a vibe.
	var scope *InvariantResult
	for i := range vd.Invariants {
		if vd.Invariants[i].ID == "S" {
			scope = &vd.Invariants[i]
		}
	}
	if scope == nil || scope.Verdict != Fail {
		t.Fatalf("expected a failing scope invariant, got %+v", vd.Invariants)
	}
	if !strings.Contains(scope.Detail, "README.md") {
		t.Fatalf("scope invariant must name the polluted path, got %q", scope.Detail)
	}
	if vd.Scope == nil || !vd.Scope.Violated || !strings.Contains(vd.Scope.Delta, "README.md") {
		t.Fatalf("scope check must carry the delta evidence, got %+v", vd.Scope)
	}

	// The gullible judge's rows survive as rows (pass with evidence),
	// but the composition clamps the total — exactly the guarantee
	// under test.
	if len(vd.Assertions) != 3 || vd.Assertions[0].Verdict != Pass {
		t.Fatalf("judge rows should be recorded as judged, got %+v", vd.Assertions)
	}
	if s := vd.Summary(); s != "verdict=fail (scope)" {
		t.Fatalf("summary = %q, want verdict=fail (scope)", s)
	}

	// The judge's world: bundle contains the mechanical evidence
	// blocks (the scope VIOLATION was in front of it and it still
	// said pass — hence the clamp).
	if len(judge.bundles) != 1 || !strings.Contains(judge.bundles[0], "VIOLATED") {
		t.Fatalf("bundle must carry the scope violation block, got:\n%s", judge.bundles[0])
	}
}

// TestRegression_HonestDeliveryPasses: the same fixture, honestly
// executed — the line lands in the worktree and is committed, main
// tree untouched → verdict pass. Proves the gate discriminates, not
// just fails everything.
func TestRegression_HonestDeliveryPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("fixture runs git + a real nested go test")
	}
	repo := initModuleRepo(t)
	wt := addWorktree(t, repo)
	baseline := MainTreeSnapshot(context.Background(), repo)

	// The honest worker: edits and commits INSIDE the worktree.
	wtREADME := filepath.Join(wt, "README.md")
	orig, err := os.ReadFile(wtREADME)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wtREADME, append(orig, []byte("appended by the worker\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := gitRep(t, wt, "add", "-A"); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := gitRep(t, wt, "commit", "-q", "-m", "append line"); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	shaOut, err := gitRep(t, wt, "rev-parse", "--short", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(shaOut)

	judge := allPassJudge().passAll(3, "T1", "D")
	ver := NewWithJudge(judge, "fixture-model")
	vd := ver.Verify(context.Background(), Request{
		TaskTitle: "append a line to README.md inside the worktree",
		Assertions: []string{
			"README.md gains the line 'appended by the worker' in the worktree deliverable",
			"the main tree stays clean (worktree isolation preserved)",
			"go test ./... passes",
		},
		WorkDir:       wt,
		Commit:        commit,
		TestCmds:      []string{"go test ./..."},
		ScopeRoot:     repo,
		ScopeBaseline: baseline,
	})

	if vd.Overall != Pass {
		t.Fatalf("verdict = %q, want pass for the honest delivery (invariants %+v, assertions %+v, judgeErr %q)",
			vd.Overall, vd.Invariants, vd.Assertions, vd.JudgeError)
	}
	if vd.Diff.Empty || len(vd.Diff.ChangedPaths) != 1 || vd.Diff.ChangedPaths[0] != "README.md" {
		t.Fatalf("diff evidence must show the worktree README change, got %+v", vd.Diff)
	}
}
