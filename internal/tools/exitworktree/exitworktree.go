// Package exitworktree implements the `exit_worktree` tool —
// counterpart to enter_worktree. PRD docs/prd/feature-subagent.md
// §3.8 wire format:
//
//   clean       → [worktree: cleaned]
//   dirty+keep  → [worktree: kept path=<...> branch=<...> changes=<n>]
//   dirty+discard → [worktree: discarded changes=<n>] (rescue stash at <ref>)
//
// The "rescue stash at <ref>" suffix on the discard path is
// display-only — the canonical wire-format prefix is just the
// bracketed token. Parsers that match the prefix verbatim never
// have to worry about the optional suffix.
package exitworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/worktree"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Absolute path of the worktree to clean up. Comes from enter_worktree's return value."
    },
    "if_dirty": {
      "type": "string",
      "enum": ["keep", "discard"],
      "description": "keep (default): leave the worktree on disk if it has uncommitted changes, return path+branch+change-count so the user can finish manually. discard: rescue-stash the dirty content to refs/seek/discarded/<ts> (recoverable for ~48h via seek worktree gc), then hard-reset and remove."
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`)

const description = `Clean up a seek-managed worktree created by enter_worktree. If the worktree is clean, removes it. If dirty: with if_dirty=keep (default), leaves it for the user to finish; with if_dirty=discard, hard-resets after first stashing a rescue copy to refs/seek/discarded/<ts> (48-hour recovery window). Returns a [worktree: cleaned|kept|discarded ...] result the parent agent can record.`

type Args struct {
	Path    string `json:"path"`
	IfDirty string `json:"if_dirty,omitempty"`
}

// Tool is the exit_worktree tool implementation. Construct via New.
type Tool struct {
	mgr *worktree.Manager
}

// New returns an exit_worktree tool bound to the given worktree manager.
func New(mgr *worktree.Manager) *Tool {
	if mgr == nil {
		panic("exitworktree: New called with nil Manager — host did not wire internal/worktree")
	}
	return &Tool{mgr: mgr}
}

func (*Tool) Name() string            { return "exit_worktree" }
func (*Tool) Description() string     { return description }
func (*Tool) Schema() json.RawMessage { return schemaBytes }

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("exit_worktree", raw, &a, "path", "if_dirty"); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", tools.MissingField("exit_worktree", "path", raw, "path", "if_dirty")
	}
	// Tolerate both "keep" / "discard" / "" — Manager.Cleanup
	// owns the canonicalisation and rejects anything else.
	res, err := t.mgr.Cleanup(ctx, a.Path, a.IfDirty)
	if err != nil {
		return fmt.Sprintf("[worktree: failed reason=cleanup_error] %s", err.Error()), nil
	}
	return formatResult(res), nil
}

// formatResult builds the wire-format string from the
// CleanupResult. Lives as a function (not a method) so tests can
// drive specific result shapes without spinning up a real
// Manager.
func formatResult(res worktree.CleanupResult) string {
	switch res.Status {
	case "cleaned":
		return "[worktree: cleaned]"
	case "kept":
		return fmt.Sprintf("[worktree: kept path=%s branch=%s changes=%d]",
			res.Path, res.Branch, res.Changes)
	case "discarded":
		out := fmt.Sprintf("[worktree: discarded changes=%d]", res.Changes)
		// Trailing rescue-stash hint is display-only — outside
		// the canonical bracket so prefix parsers don't trip on
		// it (PRD §3.2 byte-stability contract).
		if res.StashRef != "" {
			out += " rescue stash at " + res.StashRef
		}
		return out
	default:
		// Shouldn't happen — Cleanup returns one of the three
		// status values or an error. Defensive: surface the raw
		// status so debugging isn't blind.
		return fmt.Sprintf("[worktree: failed reason=unknown_status] cleanup returned status=%q", strings.TrimSpace(res.Status))
	}
}

var _ tools.Tool = (*Tool)(nil)
