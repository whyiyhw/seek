// session.go owns the ACP session's working-directory contract
// (feature-paseo-provider.md §2.2 D-1, feature-acp.md backlog ⑥).
//
// Headless ACP clients (paseo) spawn `seek acp` WITHOUT a cwd — the
// child inherits the daemon's (often "/") — so the project root
// reaches seek ONLY as session/new's `cwd` parameter. AnchorSession is
// the single place that turns the parameter into the session's actual
// working directory. It must cover BOTH path-resolution conventions
// alive in the tool layer:
//
//   - policy-anchored: read/edit/write (Policy.Resolve) and bash
//     (cmd.Dir = Policy.CWD()) → covered by Policy.Reanchor;
//   - process-anchored: grep/list_dir walk and git exec inherit the
//     PROCESS cwd → covered by os.Chdir.
//
// Skipping either half leaves some of the toolbox running in the
// daemon's cwd — the "not degraded, all wrong" failure mode D-1 warns
// about. Zed is unaffected: it spawns seek IN the project dir and sends
// the same dir as cwd, so the anchor is a behavioural no-op there.
package acp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/whyiyhw/seek/internal/paths"
)

// Reanchorer is the subset of *permission.Policy the session cwd
// contract needs. permission.Policy satisfies it structurally; the
// indirection keeps package acp composable without importing the
// permission layer (transport composes via the Backend, not the other
// way round).
type Reanchorer interface {
	Reanchor(cwd string) error
}

// Anchor is the resolved per-session working directory.
type Anchor struct {
	// Abs is the absolute project root the session runs in.
	Abs string
	// ProjectID is paths.ProjectID(Abs) — the 16-hex namespace under
	// ~/.seek/projects/ that archives project-scoped state.
	ProjectID string
	// Name is filepath.Base(Abs) — the human-facing project name, used
	// as the ACP sessionId (one seek process = one session, MVP).
	Name string
}

// AnchorSession resolves session/new's cwd parameter into an Anchor
// and makes it effective: process cwd + policy re-anchor move together,
// so every tool — whichever convention it resolves paths by — lands in
// the session directory. An empty cwd anchors to the CURRENT directory
// (the pre-D-1 behaviour, and what Zed-equivalent clients get for
// free). A cwd that cannot be chdir'd into fails loud: session/new
// returns the error and no session starts, rather than silently
// running in the daemon's directory.
func AnchorSession(cwd string, policy Reanchorer) (Anchor, error) {
	// filepath.Abs("") resolves to the process cwd — the "default stays
	// the current directory" branch falls out of the same code path.
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return Anchor{}, fmt.Errorf("acp: resolve session cwd %q: %w", cwd, err)
	}
	// Process anchor first: covers git exec + the walking tools. A
	// deleted/unreadable dir errors here instead of half-anchoring.
	if err := os.Chdir(abs); err != nil {
		return Anchor{}, fmt.Errorf("acp: session cwd %q: %w", abs, err)
	}
	// Policy anchor second: read/edit/write/bash + the cwd-inside write
	// whitelist follow. Tools hold the *Policy by pointer, so the
	// re-anchor reaches all of them without rebuilding the registry.
	if policy != nil {
		if err := policy.Reanchor(abs); err != nil {
			return Anchor{}, fmt.Errorf("acp: reanchor policy to %q: %w", abs, err)
		}
	}
	return Anchor{Abs: abs, ProjectID: paths.ProjectID(abs), Name: filepath.Base(abs)}, nil
}
