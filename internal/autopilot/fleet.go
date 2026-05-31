package autopilot

import (
	"context"

	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/worktree"
)

// managerFleet is the production Fleet (柱 G reuse): each task gets a
// fresh git worktree, then runs as a subagent confined to it. The
// worktree is KEPT (not auto-cleaned, unlike the `agent` tool) — its
// local commits are the deliverable the user reviews in the morning.
//
// SAFETY — TODO(M-A.2) no-remote guard: an autopilot subagent inherits
// the parent's tool set / policy, so under unattended --yolo it could
// `git push` / open a PR. Per PRD feature-autopilot.md §4 D2 the
// subagent must be denied remote ops. **autopilot must NOT be wired into
// the CLI / cron (M-A.4) until this guard exists** — otherwise an
// unattended run could push without review. This adapter is unexercised
// until then.
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
	return Outcome{Task: t, Status: status, Summary: res.Summary, Worktree: wt.Path}
}
