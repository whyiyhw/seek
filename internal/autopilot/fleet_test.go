package autopilot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/worktree"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// initRepo makes a throwaway git repo with one commit so a worktree-style
// dir has a HEAD to commit on top of.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "a@b.c"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := git(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "seed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(ctx, dir, "add", "-A"); err != nil {
		t.Fatalf("seed add: %v: %s", err, out)
	}
	if out, err := git(ctx, dir, "commit", "-q", "-m", "seed"); err != nil {
		t.Fatalf("seed commit: %v: %s", err, out)
	}
	return dir
}

func TestCommitWorktree_CommitsChanges(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "out.txt"), []byte("autopilot wrote this"), 0o644); err != nil {
		t.Fatal(err)
	}

	sha := commitWorktree(context.Background(), dir, "Append a line")
	if sha == "" {
		t.Fatal("expected a non-empty short SHA after committing a change")
	}
	// The message is prefixed and the change is actually recorded.
	log, err := git(context.Background(), dir, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(log); got != "autopilot: Append a line" {
		t.Fatalf("commit subject = %q", got)
	}
	// Tree is clean afterwards (everything was committed).
	st, _ := git(context.Background(), dir, "status", "--porcelain")
	if strings.TrimSpace(st) != "" {
		t.Fatalf("worktree should be clean after commit, got: %q", st)
	}
}

func TestCommitWorktree_NothingToCommit(t *testing.T) {
	dir := initRepo(t)
	// No changes since the seed commit → no new commit, empty SHA, no error.
	if sha := commitWorktree(context.Background(), dir, "noop"); sha != "" {
		t.Fatalf("expected empty SHA when nothing changed, got %q", sha)
	}
}

func TestCommitWorktree_NotAGitDir(t *testing.T) {
	// best-effort: a non-git dir yields empty SHA, never a panic/error.
	if sha := commitWorktree(context.Background(), t.TempDir(), "x"); sha != "" {
		t.Fatalf("non-git dir should yield empty SHA, got %q", sha)
	}
	if sha := commitWorktree(context.Background(), "", "x"); sha != "" {
		t.Fatalf("empty dir should yield empty SHA, got %q", sha)
	}
}

// numstatCase drives TestParseNumstat. Input lines use \t literally —
// they mirror real `git show --numstat` output (verified against git
// 2.x: binary files render "-\t-\t<path>", renames
// "<ins>\t<del>\t<old => new>" as ONE line).
func numstatCase(in string, files, ins, del int) numstatCaseT {
	return numstatCaseT{in: in, files: files, ins: ins, del: del}
}

type numstatCaseT struct {
	in    string
	files int
	ins   int
	del   int
}

func TestParseNumstat(t *testing.T) {
	cases := []numstatCaseT{
		// Plain text deltas.
		numstatCase("3\t0\tc.txt\n", 1, 3, 0),
		numstatCase("3\t0\tc.txt\n0\t1\ta.txt => renamed.txt\n", 2, 3, 1),
		// Binary file: "-\t-" counts as a touched file, zero line deltas.
		numstatCase("-\t-\tb.bin\n", 1, 0, 0),
		// Rename lines (both spellings) never panic and count once.
		numstatCase("0\t0\ta.txt => b.txt\n", 1, 0, 0),
		numstatCase("0\t0\tdir/{old => new}/f.go\n", 1, 0, 0),
		// The exact mix probed from real git (text edit + binary edit +
		// rename with edit).
		numstatCase("-\t-\tb.bin\n3\t0\tc.txt\n0\t1\ta.txt => renamed.txt\n", 3, 3, 1),
		// Mixed shapes + CRLF tolerance + trailing newline absence.
		numstatCase("1\t2\ta.go\r\n-\t-\tb.bin\n0\t0\tc => d", 3, 1, 2),
		// Garbage: too-few-fields lines and non-numeric / negative
		// counts are skipped, never a panic.
		numstatCase("", 0, 0, 0),
		numstatCase("\n\n", 0, 0, 0),
		numstatCase("not-a-numstat-line\n", 0, 0, 0),
		numstatCase("only\ttwo\n", 0, 0, 0),
		numstatCase("x\ty\tz\n", 1, 0, 0),
		numstatCase("-3\t4\tneg.go\n", 1, 0, 4),
		// Paths may contain tabs — extra fields are ignored, the file
		// still counts once.
		numstatCase("1\t1\tweird\tname\n", 1, 1, 1),
	}
	for _, c := range cases {
		files, ins, del := parseNumstat(c.in)
		if files != c.files || ins != c.ins || del != c.del {
			t.Errorf("parseNumstat(%q) = %d files, +%d/-%d; want %d, +%d/-%d",
				c.in, files, ins, del, c.files, c.ins, c.del)
		}
	}
}

// TestCommitDiffstat_RealGit exercises the full commit→numstat path
// against a real git repo: a text edit, a binary edit and a rename in
// one commit. Expected numbers mirror git's own output (probed):
// "-\t-\tb.bin", "3\t0\tc.txt", "0\t1\ta.txt => renamed.txt".
func TestCommitDiffstat_RealGit(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(ctx, dir, "add", "-A"); err != nil {
		t.Fatalf("stage seed: %v: %s", err, out)
	}
	if out, err := git(ctx, dir, "commit", "-q", "-m", "seed content"); err != nil {
		t.Fatalf("commit seed: %v: %s", err, out)
	}

	// One change commit: edit + rewrite binary + rename (with edit).
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.bin"), []byte{0x00, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("brand\nnew\nfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(ctx, dir, "mv", "seed.txt", "renamed.txt"); err != nil {
		t.Fatalf("git mv: %v: %s", err, out)
	}
	sha := commitWorktree(ctx, dir, "change")
	if sha == "" {
		t.Fatal("expected commit SHA")
	}
	files, ins, del := commitDiffstat(ctx, dir, sha)
	if files != 3 || ins != 3 || del != 1 {
		// Surface git's actual output so a config-driven drift (e.g.
		// diff.renames off) is diagnosable at a glance.
		out, _ := git(ctx, dir, "show", "--numstat", "--format=", sha)
		t.Fatalf("commitDiffstat = %d files, +%d/-%d; want 3, +3/-1\nnumstat:\n%s", files, ins, del, out)
	}
	// No commit (empty SHA) and empty dir short-circuit to zeros.
	if f, i, d := commitDiffstat(ctx, dir, ""); f != 0 || i != 0 || d != 0 {
		t.Fatalf("empty SHA should yield zeros, got %d/%d/%d", f, i, d)
	}
	if f, i, d := commitDiffstat(ctx, "", sha); f != 0 || i != 0 || d != 0 {
		t.Fatalf("empty dir should yield zeros, got %d/%d/%d", f, i, d)
	}
}

// TestTokenCost pins the bucket→Usage mapping. GLM-5.3's card is
// flat-rate (NoOffPeakDiscount), so the expected value is
// tier-independent and the test never flakes across peak boundaries.
func TestTokenCost(t *testing.T) {
	tok := subagent.Tokens{Prompt: 5000, Completion: 100, CacheHit: 4500}
	want := pricing.Cost(pricing.ModelGLM53, pricing.TierStandard, deepseek.Usage{
		PromptCacheHitTokens:  4500,
		PromptCacheMissTokens: 500,
		CompletionTokens:      100,
	})
	if got := tokenCost(pricing.ModelGLM53, tok); got != want {
		t.Fatalf("tokenCost = %v, want %v (miss bucket must price at miss rate)", got, want)
	}
	if got := tokenCost("", tok); got != 0 {
		t.Fatalf("empty model must report 0, got %v", got)
	}
	// Defensive clamp: a Result claiming more cache hits than prompt
	// tokens must not price a negative miss.
	if got := tokenCost(pricing.ModelGLM53, subagent.Tokens{Prompt: 100, CacheHit: 300, Completion: 0}); got < 0 {
		t.Fatalf("clamped cost went negative: %v", got)
	}
}

// newManagerFleet wires the production Fleet against a stub-Runner
// subagent.Manager and a real (temp) git repo — no LLM, no network.
// SEEK_HOME is redirected so the subagent index + worktrees land in
// the test sandbox.
func newManagerFleet(t *testing.T, runner subagent.Runner) Fleet {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	repo := initRepo(t)
	policy, err := permission.New(repo, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	parentTools := []string{"read", "grep", "bash", "write", "edit"}
	mgr, err := subagent.NewManager(subagent.ManagerOpts{
		ProjectAbsPath:    repo,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return parentTools },
		Runner:            runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	wtMgr, err := worktree.NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	// GLM-5.3: flat-rate card, so the cost assertions below are exact
	// regardless of the peak/off-peak clock.
	return NewFleet(mgr, wtMgr, pricing.ModelGLM53)
}

var evidenceTokens = subagent.Tokens{Prompt: 5000, Completion: 100, CacheHit: 4500}

// evidenceCost is the exact expected CostUSD for evidenceTokens on the
// GLM-5.3 card (tier-independent, see newManagerFleet).
func evidenceCost() float64 {
	return pricing.Cost(pricing.ModelGLM53, pricing.TierStandard, deepseek.Usage{
		PromptCacheHitTokens:  4500,
		PromptCacheMissTokens: 500,
		CompletionTokens:      100,
	})
}

// TestManagerFleet_SuccessWithChanges: done + a real edit in the
// worktree → commit, diffstat, tokens and cost all populated.
func TestManagerFleet_SuccessWithChanges(t *testing.T) {
	fleet := newManagerFleet(t, func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
		if err := os.WriteFile(filepath.Join(job.Policy.Cwd(), "out.txt"), []byte("autopilot wrote this\n"), 0o644); err != nil {
			return subagent.RunnerResult{}, err
		}
		return subagent.RunnerResult{Summary: "wrote out.txt", Tokens: evidenceTokens, Turns: 1}, nil
	})
	out := fleet.Run(context.Background(), Task{ID: "t1", Title: "write file", Prompt: "do it"})
	if out.Status != "done" {
		t.Fatalf("status = %q, want done (summary: %s)", out.Status, out.Summary)
	}
	if out.Commit == "" {
		t.Fatal("expected a local commit SHA")
	}
	if out.Files != 1 || out.Insertions != 1 || out.Deletions != 0 {
		t.Fatalf("diffstat = %df +%d/-%d, want 1f +1/-0", out.Files, out.Insertions, out.Deletions)
	}
	if out.Tokens != 5100 {
		t.Fatalf("tokens = %d, want prompt+completion = 5100", out.Tokens)
	}
	if out.CostUSD != evidenceCost() {
		t.Fatalf("cost = %v, want %v", out.CostUSD, evidenceCost())
	}
}

// TestManagerFleet_SuccessNoChanges: done but a clean worktree → no
// commit, zero diffstat, tokens/cost still reported.
func TestManagerFleet_SuccessNoChanges(t *testing.T) {
	fleet := newManagerFleet(t, func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
		return subagent.RunnerResult{Summary: "nothing needed doing", Tokens: evidenceTokens, Turns: 1}, nil
	})
	out := fleet.Run(context.Background(), Task{ID: "t2", Title: "noop", Prompt: "do it"})
	if out.Status != "done" {
		t.Fatalf("status = %q, want done (summary: %s)", out.Status, out.Summary)
	}
	if out.Commit != "" || out.Files != 0 || out.Insertions != 0 || out.Deletions != 0 {
		t.Fatalf("clean worktree should yield no commit + zero diffstat, got %+v", out)
	}
	if out.Tokens != 5100 || out.CostUSD != evidenceCost() {
		t.Fatalf("evidence should still report tokens/cost, got %dtok %v", out.Tokens, out.CostUSD)
	}
}

// TestManagerFleet_Failure: the Runner errors → failed outcome with
// ALL evidence fields zero (Manager zeroes Tokens on failure paths;
// failed tasks are never committed).
func TestManagerFleet_Failure(t *testing.T) {
	fleet := newManagerFleet(t, func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
		// Partial edits exist but must stay uncommitted on failure.
		if err := os.WriteFile(filepath.Join(job.Policy.Cwd(), "partial.txt"), []byte("boom\n"), 0o644); err != nil {
			return subagent.RunnerResult{}, err
		}
		return subagent.RunnerResult{}, context.DeadlineExceeded
	})
	out := fleet.Run(context.Background(), Task{ID: "t3", Title: "doomed", Prompt: "do it"})
	if out.Status != "failed" {
		t.Fatalf("status = %q, want failed", out.Status)
	}
	if out.Commit != "" || out.Files != 0 || out.Insertions != 0 || out.Deletions != 0 ||
		out.Tokens != 0 || out.CostUSD != 0 {
		t.Fatalf("failed outcome must carry zero evidence, got %+v", out)
	}
	if out.Worktree == "" {
		t.Fatal("worktree should still be reported for morning inspection")
	}
}

// TestManagerFleet_CtxCancel: the run is cancelled mid-spawn → failed
// outcome, zero evidence, no commit — and Run returns promptly.
func TestManagerFleet_CtxCancel(t *testing.T) {
	fleet := newManagerFleet(t, func(ctx context.Context, job subagent.RunnerJob) (subagent.RunnerResult, error) {
		<-ctx.Done()
		return subagent.RunnerResult{}, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel while the runner is in flight — give the fleet a moment to
	// create the worktree + enter Spawn first.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	out := fleet.Run(ctx, Task{ID: "t4", Title: "slow", Prompt: "do it"})
	if out.Status != "failed" {
		t.Fatalf("status = %q, want failed after cancel (summary: %s)", out.Status, out.Summary)
	}
	if out.Commit != "" || out.Files != 0 || out.Insertions != 0 || out.Deletions != 0 ||
		out.Tokens != 0 || out.CostUSD != 0 {
		t.Fatalf("cancelled outcome must carry zero evidence, got %+v", out)
	}
}
