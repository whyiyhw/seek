package autopilot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/verifier"
	"github.com/whyiyhw/seek/internal/worktree"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// managerFleet is the production Fleet (柱 G reuse): each task gets a
// fresh git worktree, then runs as a subagent confined to it. The
// worktree is KEPT (not auto-cleaned, unlike the `agent` tool) — its
// local commit is the deliverable the user reviews in the morning.
//
// SAFETY (no-remote guard): the autopilot subagent's bash is wrapped with
// a deny check (IsRemoteMutating) at wiring time in cmd/seek, so an
// unattended --yolo run cannot `git push` / open a PR / hit `gh api`. The
// Fleet only ever commits LOCALLY (below) — the morning review + merge is
// the human gate, per PRD feature-autopilot.md §4 D2.
type managerFleet struct {
	mgr   *subagent.Manager
	wtMgr *worktree.Manager
	typ   subagent.Type
	// model is the lane the subagents run on (SpawnArgs leaves
	// Provider/Model empty, so each child inherits the parent
	// session's model). Used to price the rolled-up token buckets;
	// empty → CostUSD reports 0 (cost we can't attribute is reported
	// as zero, never guessed).
	model string
	// ver judges each completed task against its DoneWhen (v8 柱 R
	// R2). Nil = verification not wired (tests, or the caller opted
	// out) — Outcome.Verdict stays nil and the report says so by
	// omission.
	ver *verifier.Verifier
}

// NewFleet builds the production Fleet. A nil wtMgr (non-git project)
// makes every task fail fast with a clear message rather than running
// un-isolated. model is the parent session's model id (what the
// subagents actually run on); pass "" only to explicitly opt out of
// per-outcome cost accounting. ver (nil = skip verification) is the
// independent verifier run after each completed task — on-demand per
// task completion, not a resident loop.
func NewFleet(mgr *subagent.Manager, wtMgr *worktree.Manager, model string, ver *verifier.Verifier) Fleet {
	return &managerFleet{mgr: mgr, wtMgr: wtMgr, typ: subagent.TypeGeneralPurpose, model: model, ver: ver}
}

func (f *managerFleet) Run(ctx context.Context, t Task) Outcome {
	if f.mgr == nil {
		return Outcome{Task: t, Status: "failed", Summary: "autopilot: subagent manager unavailable"}
	}
	if f.wtMgr == nil {
		return Outcome{Task: t, Status: "failed", Summary: "autopilot requires a git working tree (run from inside a git repo)"}
	}
	wt, err := f.wtMgr.Create(ctx, "", "")
	if err != nil {
		return Outcome{Task: t, Status: "failed", Summary: "worktree create: " + err.Error()}
	}
	// Pre-task main-tree snapshot for the scope check (v8 柱 R R2):
	// the verifier compares the main tree against this baseline to
	// catch worktree-isolation bypasses — the shape of the historical
	// "subagent edited the MAIN tree" bug, which green tests never
	// surfaced (docs/pitfalls.md, fix c21167d).
	baseline := verifier.MainTreeSnapshot(ctx, f.wtMgr.Root())
	res := f.mgr.Spawn(ctx, subagent.SpawnArgs{
		Description:  t.Title,
		Prompt:       t.Prompt,
		Type:         f.typ,
		WorktreePath: wt.Path,
	})
	status := "failed"
	if res.Status == subagent.StatusCompleted {
		status = "done"
	}
	out := Outcome{Task: t, Status: status, Summary: res.Summary, Worktree: wt.Path}
	// Evidence fields (v8 柱 R R1): the token burn and its price come
	// straight off the Spawn Result — Manager zeroes Tokens on every
	// failure path, so a failed task honestly reports 0/0.
	out.Tokens = res.Tokens.Prompt + res.Tokens.Completion
	out.CostUSD = tokenCost(f.model, res.Tokens)
	// Deterministically commit whatever the subagent produced, LOCALLY,
	// inside the worktree — so the user wakes to a reviewable commit, not
	// a dirty tree (the model may or may not commit on its own). The
	// no-remote guard above keeps this from ever leaving the machine.
	// Only on success; a failed task's partial edits are left uncommitted
	// for inspection.
	if status == "done" {
		out.Commit = commitWorktree(ctx, wt.Path, t.Title)
		out.Files, out.Insertions, out.Deletions = commitDiffstat(ctx, wt.Path, out.Commit)
		// Independent verification (v8 柱 R R2): fresh-context judge
		// over the done_when assertions + diff + tests. Observational
		// for the run — a fail verdict does NOT flip Status; the
		// morning review reads both. Judge/infra failures land as
		// doubt inside the verdict, never as a missing verdict.
		if f.ver != nil {
			out.Verdict = f.ver.Verify(ctx, verifier.Request{
				TaskTitle:     t.Title,
				Assertions:    t.DoneWhen,
				WorkDir:       wt.Path,
				Commit:        out.Commit,
				TestCmds:      detectTestCmds(wt.Path),
				ScopeRoot:     f.wtMgr.Root(),
				ScopeBaseline: baseline,
			})
		}
	}
	return out
}

// detectTestCmds derives the deterministic test commands for a
// deliverable dir: "go test ./..." when it carries a go.mod. No model
// freedom here — the verifier must run the SAME suite the repo
// defines, not one the worker suggested.
func detectTestCmds(dir string) []string {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return []string{"go test ./..."}
	}
	return nil
}

// tokenCost prices one subagent's rolled-up token buckets at model.
// Empty model (lane not wired) → 0: Outcome.CostUSD documents that an
// unattributable cost is reported as zero, never guessed. The tier is
// sampled at outcome time — per-turn tier accounting lives in the
// parent Tracker; this is the report-side approximation on the
// aggregate buckets.
func tokenCost(model string, t subagent.Tokens) float64 {
	if model == "" {
		return 0
	}
	// Prompt is the API's prompt_tokens total, which DeepSeek defines
	// as hit + miss; recover the miss bucket so hits price at the
	// ~10× cheaper rate. Clamp defensively: a Result carrying more
	// hits than prompt must not price a negative miss.
	miss := t.Prompt - t.CacheHit
	if miss < 0 {
		miss = 0
	}
	return pricing.Cost(model, pricing.CurrentTier(time.Now()), deepseek.Usage{
		PromptCacheHitTokens:  t.CacheHit,
		PromptCacheMissTokens: miss,
		CompletionTokens:      t.Completion,
	})
}

// commitDiffstat returns the file/insertion/deletion footprint of one
// local commit, read inside the worktree. Zero values when there is no
// commit or git errored — same best-effort posture as commitWorktree.
func commitDiffstat(ctx context.Context, dir, sha string) (files, insertions, deletions int) {
	if dir == "" || sha == "" {
		return 0, 0, 0
	}
	out, err := git(ctx, dir, "show", "--numstat", "--format=", sha)
	if err != nil {
		return 0, 0, 0
	}
	return parseNumstat(out)
}

// parseNumstat tallies `git show --numstat` output. Each line is
// "<ins>\t<del>\t<path>"; binary files render as "-\t-\t<path>" (a
// touched file, no line deltas) and renames as
// "<ins>\t<del>\t<old => new>" (one file, one line, counted once —
// verified against git 2.x output). Lines that don't split into three
// tab-separated fields (or carry non-numeric counts) are skipped, never
// a panic: numstat is evidence, not a parser contract.
func parseNumstat(out string) (files, insertions, deletions int) {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		files++
		insertions += numOrZero(parts[0])
		deletions += numOrZero(parts[1])
	}
	return files, insertions, deletions
}

// numOrZero parses a numstat count column; "-" (binary) and anything
// non-numeric count as 0.
func numOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// commitWorktree stages and commits everything in dir with msg, returning
// the short SHA. Empty string if there was nothing to commit or git
// errored — autopilot treats commit as best-effort polish on top of the
// already-isolated worktree, never a hard failure.
func commitWorktree(ctx context.Context, dir, msg string) string {
	if dir == "" {
		return ""
	}
	if _, err := git(ctx, dir, "add", "-A"); err != nil {
		return ""
	}
	// `diff --cached --quiet` exits 0 when nothing is staged → nothing to
	// commit; exit 1 means there are staged changes worth a commit.
	if _, err := git(ctx, dir, "diff", "--cached", "--quiet"); err == nil {
		return ""
	}
	if msg == "" {
		msg = "autopilot: task changes"
	}
	if _, err := git(ctx, dir, "commit", "-q", "-m", "autopilot: "+msg); err != nil {
		return ""
	}
	sha, err := git(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sha)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return string(b), err
}
