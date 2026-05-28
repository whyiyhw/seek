// Package enterworktree implements the `enter_worktree` tool — the
// LLM-facing surface over worktree.Manager.Create. Mirrors the
// thin-wrapper pattern of internal/tools/agent: schema + Execute
// here, all logic in internal/worktree.
//
// PRD docs/prd/feature-subagent.md §3.8 — the worktree integration
// half of v5 柱 G. The model can call this directly (e.g.
// "spawn a worktree so I can try this refactor without touching
// my working copy"), and internal/tools/agent's isolation flow
// (M11.1 phase 2) calls Manager.Create under the same code path.
package enterworktree

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/worktree"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "branch": {
      "type": "string",
      "description": "Branch name to create or use. Auto-generated as seek/wt/<id> when omitted — the seek/ prefix keeps these out of the user's default tab-completion."
    },
    "base": {
      "type": "string",
      "description": "Base commit, branch, or tag to branch from. Defaults to HEAD."
    }
  },
  "additionalProperties": false
}`)

const description = `Create a seek-managed git worktree under ~/.seek/projects/<pid>/worktrees/<id>/. Useful when you want to try a change without touching the user's working copy — write/edit/bash run there in isolation, then call exit_worktree to discard, keep, or auto-clean when done. The worktree's branch is recorded under refs/seek/worktrees/<id> (out of the user's default refspec so push won't carry it). Returns the absolute path + branch — pass the path to exit_worktree when you're done.`

// Args is the strict-decode target. JSON tags must match
// schemaBytes field names exactly (UnmarshalStrict rejects
// mismatches).
type Args struct {
	Branch string `json:"branch,omitempty"`
	Base   string `json:"base,omitempty"`
}

// Tool is the LLM-facing wrapper. Constructed with New(mgr); the
// manager carries all the heavy state (git command runner +
// active worktree map). nil manager means "host program didn't
// wire worktree" (e.g. running outside a git repo) — in that
// case the tool isn't registered at all, so New(nil) is a
// programmer error.
type Tool struct {
	mgr *worktree.Manager
}

// New constructs the tool. mgr must be non-nil; nil is a
// programmer error (host should branch on git availability and
// omit tool registration entirely rather than reach this path).
func New(mgr *worktree.Manager) *Tool {
	if mgr == nil {
		panic("enterworktree: New called with nil Manager — host program did not wire internal/worktree")
	}
	return &Tool{mgr: mgr}
}

func (*Tool) Name() string            { return "enter_worktree" }
func (*Tool) Description() string     { return description }
func (*Tool) Schema() json.RawMessage { return schemaBytes }

// Execute creates a new worktree and returns the [worktree:
// created path=... branch=...] wire-format string. The prefix is
// byte-stable contract; the part after the closing `]` is detail
// the model parses verbatim into the next exit_worktree call.
//
// Failure paths return (errorMessage, nil) — the model reads the
// failure string and decides whether to retry or escalate. The
// error return value is reserved for argument-parsing bugs.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("enter_worktree", raw, &a,
		"branch", "base"); err != nil {
		return "", err
	}

	wt, err := t.mgr.Create(ctx, a.Branch, a.Base)
	if err != nil {
		// Wire-format failure: model reads "worktree: failed"
		// prefix and decides. The git stderr is included verbatim
		// in the hint so debugging information isn't lost.
		return fmt.Sprintf("[worktree: failed reason=create_error] %s", err.Error()), nil
	}
	return fmt.Sprintf("[worktree: created path=%s branch=%s base=%s]", wt.Path, wt.Branch, wt.Base), nil
}

// Compile-time assertions: Tool satisfies tools.Tool. We do NOT
// mark this ReadOnly — Create mutates the filesystem (new dir)
// and git state (new branch + ref). Parallel-dispatch of two
// enter_worktree calls would create two separate worktrees with
// no shared state, so it COULD be safe — but PRD §8 risk table
// notes orphan-worktree on crash mid-creation, and serial
// dispatch keeps the orphan-recovery scan simple.
var _ tools.Tool = (*Tool)(nil)
