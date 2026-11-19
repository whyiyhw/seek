package verifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestParseChangedPaths(t *testing.T) {
	got := parseChangedPaths("1\t2\ta/b.go\n-\t-\tbin/logo.png\n0\t10\told/{x.go => y.go}\nnot-a-line\n")
	want := []string{"a/b.go", "bin/logo.png", "y.go"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCapOutput(t *testing.T) {
	small := "hello"
	if got := capOutput(small); got != small {
		t.Fatalf("small output must pass through, got %q", got)
	}
	big := strings.Repeat("x", maxOutputBytes+1000)
	got := capOutput(big)
	if len(got) >= len(big) {
		t.Fatal("oversized output must be elided")
	}
	if !strings.Contains(got, "bytes elided") {
		t.Fatal("elision must be explicit, not silent truncation")
	}
	if !strings.HasPrefix(got, "xxx") || !strings.HasSuffix(strings.TrimRight(got, "\n"), "xxx") {
		t.Fatal("head AND tail must survive")
	}
}

// TestMainTreeSnapshot_AndDelta: real git — the snapshot moves when a
// clean tree gets polluted (the regression's core signal), when a
// dirty file's content grows (numstat drift with unchanged status),
// and stays put when nothing changed.
func TestMainTreeSnapshot_AndDelta(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "t"}, {"config", "commit.gpgsign", "false"},
	} {
		if out, err := gitOut(context.Background(), dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeFile(t, filepath.Join(dir, "seed.txt"), "one\n")
	writeFile(t, filepath.Join(dir, "dirty.txt"), "a\n")
	if out, err := gitOut(context.Background(), dir, "add", "-A"); err != nil {
		t.Fatal(out)
	}
	if out, err := gitOut(context.Background(), dir, "commit", "-q", "-m", "seed"); err != nil {
		t.Fatal(out)
	}

	clean := MainTreeSnapshot(context.Background(), dir)
	if clean != MainTreeSnapshot(context.Background(), dir) {
		t.Fatal("snapshot must be stable for an unchanged tree")
	}

	// Pollution 1: a previously-clean file is modified.
	writeFile(t, filepath.Join(dir, "seed.txt"), "one\ntwo\n")
	p1 := MainTreeSnapshot(context.Background(), dir)
	if p1 == clean {
		t.Fatal("snapshot must move when a clean file is edited")
	}
	delta := snapshotDelta(clean, p1)
	if !strings.Contains(strings.Join(delta, ";"), "seed.txt") {
		t.Fatalf("delta must name seed.txt, got %v", delta)
	}

	// Pollution 2: an already-dirty file's content grows — porcelain
	// status unchanged, only the numstat counts move.
	before2 := p1
	writeFile(t, filepath.Join(dir, "dirty.txt"), "a\nb\nc\n")
	p2 := MainTreeSnapshot(context.Background(), dir)
	if p2 == before2 {
		t.Fatal("snapshot must move on content drift in a dirty file")
	}
	delta2 := snapshotDelta(before2, p2)
	joined := strings.Join(delta2, ";")
	if !strings.Contains(joined, "numstat") || !strings.Contains(joined, "dirty.txt") {
		t.Fatalf("delta must report the numstat drift on dirty.txt, got %v", delta2)
	}

	// Untracked file counts as pollution too.
	writeFile(t, filepath.Join(dir, "untracked.txt"), "new\n")
	p3 := MainTreeSnapshot(context.Background(), dir)
	if p3 == p2 || !strings.Contains(strings.Join(snapshotDelta(p2, p3), ";"), "untracked.txt") {
		t.Fatal("untracked additions must surface in the delta")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunTests_ExecSurface: commands run with cwd pinned to WorkDir,
// non-zero exits and spawn failures are recorded (never panic), and
// the timeout kills the process.
func TestRunTests_ExecSurface(t *testing.T) {
	dir := t.TempDir()
	req := Request{
		WorkDir: dir,
		TestCmds: []string{
			"pwd",
			"exit 3",
			"definitely-not-a-real-binary-xyz --version",
			"sleep 5",
		},
		TestTimeout: 300 * time.Millisecond,
	}
	runs := runTests(context.Background(), req)
	if len(runs) != 4 {
		t.Fatalf("expected 4 runs, got %d", len(runs))
	}
	if !strings.HasPrefix(strings.TrimSpace(runs[0].Output), dir) {
		t.Fatalf("cmd must run inside WorkDir, output starts %q", runs[0].Output[:min(40, len(runs[0].Output))])
	}
	if runs[0].ExitCode != 0 || runs[0].RunErr != "" {
		t.Fatalf("green run misrecorded: %+v", runs[0])
	}
	if runs[1].ExitCode != 3 {
		t.Fatalf("exit 3 must be recorded, got %+v", runs[1])
	}
	// A missing binary under /bin/sh is exit 127 — a red run, not a
	// spawn error (the shell handled it).
	if runs[2].ExitCode != 127 || runs[2].RunErr != "" {
		t.Fatalf("command-not-found must record exit 127, got %+v", runs[2])
	}
	if !runs[3].TimedOut {
		t.Fatalf("sleep 5 under a 300ms timeout must be recorded as timed out, got %+v", runs[3])
	}
	for i, want := range []string{"T1", "T2", "T3", "T4"} {
		if runs[i].ID != want {
			t.Fatalf("run %d id = %q, want %q", i, runs[i].ID, want)
		}
		if runs[i].Evidence == "" {
			t.Fatalf("run %d must render an evidence block", i)
		}
	}

	// A dead ctx (the parent run was cancelled) is a spawn-level
	// failure: RunErr set, no exit code — the doubt path, never pass.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dead := runTests(ctx, Request{WorkDir: dir, TestCmds: []string{"pwd"}})
	if len(dead) != 1 || dead[0].RunErr == "" {
		t.Fatalf("cancelled ctx must land in RunErr, got %+v", dead)
	}
}

// TestEvaluateInvariants_TestOutcomes: red tests → fail; unrunnable
// tests → doubt; green → pass; scope violation → fail; no assertions
// → doubt.
func TestEvaluateInvariants_TestOutcomes(t *testing.T) {
	base := func(tests []TestRun, cmds int) (Request, *Verdict) {
		req := Request{TestCmds: make([]string, cmds), Assertions: []string{"a"}}
		vd := &Verdict{Tests: tests}
		return req, vd
	}

	req, vd := base([]TestRun{{ID: "T1", Cmd: "go test ./...", ExitCode: 1}}, 1)
	if got := evaluateInvariants(req, vd)[0]; got.Verdict != Fail {
		t.Fatalf("red test must be a failing invariant, got %+v", got)
	}

	req, vd = base([]TestRun{{ID: "T1", Cmd: "x", RunErr: "exec: not found"}}, 1)
	if got := evaluateInvariants(req, vd)[0]; got.Verdict != Doubt {
		t.Fatalf("unrunnable test must be doubt, got %+v", got)
	}

	req, vd = base([]TestRun{{ID: "T1", Cmd: "go test ./...", ExitCode: 0}}, 1)
	invs := evaluateInvariants(req, vd)
	if invs[0].Verdict != Pass || len(invs) != 1 {
		t.Fatalf("green test + assertions present = single passing invariant, got %+v", invs)
	}

	req, vd = base(nil, 0)
	req.Assertions = nil
	for _, inv := range evaluateInvariants(req, vd) {
		if inv.ID == "A" && inv.Verdict != Doubt {
			t.Fatalf("no-assertions invariant must be doubt, got %+v", inv)
		}
	}
}

// TestVerify_UnattendedFakeKeyIsDoubt: the §9.0/§9.1 eval-lane
// posture — with the fake key the judge backend 401s; the verdict
// must degrade to doubt with the reason recorded, never a fake pass.
// (The verifier writes nothing to disk anywhere, so the
// eval-home-isolation contract — all user-state writes resolve
// through paths.Home(), run.sh pins SEEK_HOME — is inherited as-is.)
func TestVerify_UnattendedFakeKeyIsDoubt(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Authentication Fails, Your api key: ***"}}`))
	}))
	t.Cleanup(srv.Close)

	client := deepseek.New(deepseek.WithAPIKey("sk-fake-unattended"), deepseek.WithBaseURL(srv.URL))
	ver := New(client, "fixture-model")
	vd := ver.Verify(context.Background(), Request{
		TaskTitle:  "t",
		Assertions: []string{"a"},
		WorkDir:    t.TempDir(),
	})
	if vd.Overall != Doubt {
		t.Fatalf("overall = %q, want doubt under a 401 judge backend", vd.Overall)
	}
	if !strings.Contains(vd.JudgeError, "401") && !strings.Contains(vd.JudgeError, "Authentication") {
		t.Fatalf("judge error must carry the backend failure, got %q", vd.JudgeError)
	}
}
