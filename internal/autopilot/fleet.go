package autopilot

import (
	"context"
	"os/exec"
	"strings"

	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/worktree"
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
}

// NewFleet builds the production Fleet. A nil wtMgr (non-git project)
// makes every task fail fast with a clear message rather than running
// un-isolated.
func NewFleet(mgr *subagent.Manager, wtMgr *worktree.Manager) Fleet {
	return &managerFleet{mgr: mgr, wtMgr: wtMgr, typ: subagent.TypeGeneralPurpose}
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
	// Deterministically commit whatever the subagent produced, LOCALLY,
	// inside the worktree — so the user wakes to a reviewable commit, not
	// a dirty tree (the model may or may not commit on its own). The
	// no-remote guard above keeps this from ever leaving the machine.
	// Only on success; a failed task's partial edits are left uncommitted
	// for inspection.
	if status == "done" {
		out.Commit = commitWorktree(ctx, wt.Path, t.Title)
	}
	return out
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
