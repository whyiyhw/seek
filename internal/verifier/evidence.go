// evidence.go — the deterministic evidence layer. Everything here is
// pure Go + git + the test runner: no model, no judgment. The judge
// session reasons OVER this bundle; the verdict clamps are computed
// FROM it. Ground truth first, opinion second.
package verifier

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Output caps. A test log or diff fed to the judge is evidence, not a
// firehose: the head carries the setup, the tail carries the verdict,
// the middle is elided (same posture as the bash tool's 32 KiB cap).
const (
	maxOutputBytes  = 32 << 10
	maxOutputHead   = 16 << 10
	defaultTestWait = 10 * time.Minute
)

// runTests executes each command in req.TestCmds inside req.WorkDir,
// bounded by req.TestTimeout. Each run records the exit code and a
// capped head+tail of combined output. Commands run through the shell
// ("/bin/sh -c") exactly like the bash tool — the caller composes
// real commands ("go test ./..."), not argv vectors.
//
// Execution surface (documented for the v8 R2 acceptance): the
// subprocess cwd is pinned to WorkDir, the environment is inherited
// from the seek process (the same surface the worker's own bash tool
// had — no new exposure; under the eval lane's fake key this env is
// the fake-key env), and each command is killed at the timeout. A
// command that cannot run at all (missing binary, spawn error)
// records RunErr — that path degrades the verdict to doubt, never
// to pass.
func runTests(ctx context.Context, req Request) []TestRun {
	if len(req.TestCmds) == 0 || req.WorkDir == "" {
		return nil
	}
	timeout := req.TestTimeout
	if timeout <= 0 {
		timeout = defaultTestWait
	}
	runs := make([]TestRun, len(req.TestCmds))
	for i, cmd := range req.TestCmds {
		runs[i] = runOneTest(ctx, req.WorkDir, cmd, timeout, fmt.Sprintf("T%d", i+1))
	}
	return runs
}

func runOneTest(ctx context.Context, dir, cmd string, timeout time.Duration, id string) TestRun {
	tr := TestRun{ID: id, Cmd: cmd}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	c := exec.CommandContext(tctx, shell(), shellArg(), cmd)
	c.Dir = dir
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	tr.Duration = time.Since(start)
	tr.Output = capOutput(buf.String())
	if tctx.Err() == context.DeadlineExceeded {
		tr.TimedOut = true
	} else if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			tr.ExitCode = ee.ExitCode()
		} else {
			tr.RunErr = err.Error()
		}
	}
	tr.Evidence = renderTestEvidence(tr)
	return tr
}

// collectDiff reads the deliverable's footprint from WorkDir: the
// numstat and a capped patch of req.Commit. No commit → empty
// footprint (itself evidence — "nothing was delivered").
func collectDiff(ctx context.Context, req Request) DiffFootprint {
	df := DiffFootprint{Commit: req.Commit}
	if req.WorkDir == "" || req.Commit == "" {
		df.Empty = true
		df.Evidence = "[D] no deliverable commit — the diff under judgment is empty"
		return df
	}
	numstat, _ := gitOut(ctx, req.WorkDir, "show", "--numstat", "--format=", req.Commit)
	df.Numstat = strings.TrimSpace(numstat)
	df.ChangedPaths = parseChangedPaths(df.Numstat)
	df.Empty = len(df.ChangedPaths) == 0
	patch, _ := gitOut(ctx, req.WorkDir, "show", "--format=", "--unified=2", req.Commit)
	df.Patch = capOutput(patch)
	df.Evidence = renderDiffEvidence(df)
	return df
}

// parseChangedPaths extracts the path column from `git show --numstat`
// output. Rename lines ("<old => new>") count once under the new
// spelling; binary lines carry "-" counts but still name a file.
func parseChangedPaths(numstat string) []string {
	var paths []string
	for line := range strings.SplitSeq(numstat, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		p := parts[len(parts)-1]
		if i := strings.Index(p, " => "); i >= 0 {
			p = p[i+4:]
			p = strings.TrimSuffix(p, "}")
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// MainTreeSnapshot captures the dirt state of a repo tree — the
// autopilot fleet calls it on the MAIN tree before spawning a task
// and hands the string back as Request.ScopeBaseline. porcelain
// (staged+unstaged+untracked, status codes included) plus the
// numstat of the working tree vs HEAD: together they move when
// either a file's status changes or an already-dirty file's content
// changes.
func MainTreeSnapshot(ctx context.Context, root string) string {
	porcelain, _ := gitOut(ctx, root, "status", "--porcelain")
	numstat, _ := gitOut(ctx, root, "diff", "HEAD", "--numstat")
	return strings.TrimSpace(porcelain) + "\n--\n" + strings.TrimSpace(numstat)
}

// checkScope compares the ScopeRoot tree's current snapshot against
// the baseline. Any drift — a new dirty path, a changed status, a
// changed numstat — is a violation: during a worktree-isolated task
// the main tree must not move at all. False positives (the human
// editing the main tree mid-run) surface as a fail with the delta
// spelled out; the morning review sees exactly which paths moved and
// can overrule. The opposite posture (ignore drift) is the historical
// bug: the worker's edit landed in the main tree and nothing looked.
func checkScope(ctx context.Context, req Request) ScopeCheck {
	sc := ScopeCheck{Root: req.ScopeRoot, Before: req.ScopeBaseline}
	after := MainTreeSnapshot(ctx, req.ScopeRoot)
	sc.After = after
	if after != sc.Before {
		sc.Violated = true
		sc.Delta = strings.Join(snapshotDelta(sc.Before, after), ", ")
	}
	sc.Evidence = renderScopeEvidence(sc)
	return sc
}

// snapshotDelta lists what moved between two snapshots: porcelain
// entries that appeared or changed status, then numstat lines that
// appeared or changed (content drift on an already-dirty file keeps
// its porcelain status; only the numstat counts move). Sorted for
// stable output across runs.
func snapshotDelta(before, after string) []string {
	bp := porcelainPaths(before)
	ap := porcelainPaths(after)
	bn := numstatLines(before)
	var delta []string
	for p, status := range ap {
		if s, ok := bp[p]; !ok || s != status {
			delta = append(delta, fmt.Sprintf("%s %s", status, p))
		}
	}
	for l := range numstatLines(after) {
		if !bn[l] {
			delta = append(delta, "numstat "+l)
		}
	}
	sort.Strings(delta)
	return delta
}

// numstatLines parses the numstat half of a MainTreeSnapshot (after
// the "--" separator) into a line set.
func numstatLines(snapshot string) map[string]bool {
	i := strings.Index(snapshot, "\n--\n")
	if i < 0 {
		return nil
	}
	m := make(map[string]bool)
	for line := range strings.SplitSeq(snapshot[i+4:], "\n") {
		if line = strings.TrimSpace(line); line != "" {
			m[line] = true
		}
	}
	return m
}

// porcelainPaths parses the porcelain half of a MainTreeSnapshot
// (everything before the "--" separator) into path → status.
func porcelainPaths(snapshot string) map[string]string {
	porcelain := snapshot
	if i := strings.Index(snapshot, "\n--\n"); i >= 0 {
		porcelain = snapshot[:i]
	}
	m := make(map[string]string)
	for line := range strings.SplitSeq(porcelain, "\n") {
		if len(line) < 4 {
			continue
		}
		m[strings.TrimSpace(line[3:])] = line[:2]
	}
	return m
}

// gitOut runs git in dir and returns combined output.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// capOutput keeps the head and tail of oversized output with an
// explicit elision marker, so a verdict buried at the end of a test
// log survives.
func capOutput(s string) string {
	if len(s) <= maxOutputBytes {
		return s
	}
	return s[:maxOutputHead] + "\n…[" + fmt.Sprint(len(s)-maxOutputBytes) + " bytes elided]…\n" + s[len(s)-maxOutputHead:]
}

func shell() string    { return "/bin/sh" }
func shellArg() string { return "-c" }

func renderTestEvidence(t TestRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] $ %s\n", t.ID, t.Cmd)
	if t.RunErr != "" {
		fmt.Fprintf(&b, "spawn error: %s\n", t.RunErr)
	} else if t.TimedOut {
		fmt.Fprintf(&b, "TIMED OUT after %s\n", t.Duration.Truncate(time.Millisecond))
	} else {
		fmt.Fprintf(&b, "exit=%d in %s\n", t.ExitCode, t.Duration.Truncate(time.Millisecond))
	}
	if t.Output != "" {
		b.WriteString(indent(t.Output))
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderDiffEvidence(df DiffFootprint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[D] deliverable %s\n", df.Commit)
	if df.Empty {
		b.WriteString("no changed paths\n")
	} else {
		fmt.Fprintf(&b, "changed paths: %s\n", strings.Join(df.ChangedPaths, ", "))
		if df.Numstat != "" {
			b.WriteString(indent(df.Numstat))
			b.WriteByte('\n')
		}
		if df.Patch != "" {
			b.WriteString(indent(df.Patch))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderScopeEvidence(sc ScopeCheck) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[S] main tree isolation check on %s\n", sc.Root)
	if sc.Violated {
		fmt.Fprintf(&b, "VIOLATED — tree drifted from the pre-task baseline: %s\n", sc.Delta)
		fmt.Fprintf(&b, "baseline:\n%s\ncurrent:\n%s\n", indent(sc.Before), indent(sc.After))
	} else {
		b.WriteString("clean — identical to the pre-task baseline\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}
