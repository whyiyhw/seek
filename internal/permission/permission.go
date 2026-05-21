// Package permission gates dangerous tool actions. M2 is intentionally
// minimal: a global Policy that either allows everything (--yolo) or
// applies safe defaults (no bash, no writes outside CWD). Interactive
// per-call prompts arrive with the TUI in M3.
//
// Denials are returned as plain errors so the agent can feed them back to
// the LLM as a tool result. The model then knows to ask the user instead
// of retrying blindly.
package permission

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Kind is the category of a guarded action.
type Kind string

const (
	KindBash  Kind = "bash"
	KindWrite Kind = "write"
	KindEdit  Kind = "edit"
)

// Action describes one attempt to perform a guarded operation.
type Action struct {
	Kind    Kind
	Path    string // for write/edit
	Command string // for bash; only first ~80 chars are shown in errors
}

// Policy is the per-process permission policy. Construct via New.
type Policy struct {
	yolo bool
	cwd  string // absolute path; used to decide "inside vs outside"
}

// New returns a Policy. cwd should be the project root (typically os.Getwd()
// at start-up). yolo=true allows everything.
func New(cwd string, yolo bool) (*Policy, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("permission: resolve cwd: %w", err)
	}
	return &Policy{yolo: yolo, cwd: abs}, nil
}

// ErrDenied is returned when an action is blocked by policy. Callers should
// surface its message verbatim to the LLM — it includes the specific reason
// and the override instructions ("run with --yolo …").
var ErrDenied = errors.New("permission denied")

// Check evaluates an Action against the Policy. Nil = allowed.
func (p *Policy) Check(a Action) error {
	if p == nil {
		return fmt.Errorf("%w: no policy configured", ErrDenied)
	}
	if p.yolo {
		return nil
	}

	switch a.Kind {
	case KindBash:
		return fmt.Errorf("%w: bash is gated; ask the user to re-run seek with --yolo, or run the command yourself: %s",
			ErrDenied, shorten(a.Command, 80))

	case KindWrite, KindEdit:
		if a.Path == "" {
			return fmt.Errorf("%w: %s requires a path", ErrDenied, a.Kind)
		}
		inside, err := isWithin(p.cwd, a.Path)
		if err != nil {
			return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
		}
		if !inside {
			return fmt.Errorf("%w: %s on %q is outside the working directory %q — re-run with --yolo to allow",
				ErrDenied, a.Kind, a.Path, p.cwd)
		}
		return nil

	default:
		return fmt.Errorf("%w: unknown action kind %q", ErrDenied, a.Kind)
	}
}

// CWD returns the resolved working directory the policy is anchored to.
func (p *Policy) CWD() string { return p.cwd }

// Yolo reports whether the policy is in unrestricted mode.
func (p *Policy) Yolo() bool { return p.yolo }

// isWithin reports whether target resolves to a path inside root (inclusive
// of root itself). Both paths are made absolute before comparison.
func isWithin(root, target string) (bool, error) {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(root, absTarget)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return true, nil
	}
	if strings.HasPrefix(rel, "..") {
		return false, nil
	}
	// On non-Unix filesystems Rel can return paths like "..\foo". The
	// prefix check above covers that. Anything else is "inside".
	return true, nil
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
