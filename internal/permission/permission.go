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

// ApprovalRequest is what the TUI consumes when ModeAsk needs a user
// answer. The host (cmd/seek) glues the policy's askFn to a channel of
// these and the TUI reads from that channel.
//
// Reply MUST receive exactly one value — true to allow, false to deny.
// askFn blocks on Reply, so a missing reply hangs the agent.
type ApprovalRequest struct {
	Action Action
	Reply  chan<- bool
}

// Mode controls how Check resolves a dangerous Action.
type Mode int

const (
	// ModeDeny refuses dangerous actions outright. The default for
	// non-interactive launches (print mode), since there's no user to
	// ask. Returns a denial message instructing the model to surface
	// the request to a human.
	ModeDeny Mode = iota
	// ModeAsk consults the askFn callback for each dangerous action.
	// The default for the interactive TUI.
	ModeAsk
	// ModeYolo permits every action. Set by --yolo or by an "always
	// approve" answer at an inline prompt.
	ModeYolo
)

// Policy is the per-process permission policy. Construct via New.
type Policy struct {
	mode  Mode
	cwd   string // absolute path; used to decide "inside vs outside"
	askFn func(Action) bool
}

// New returns a Policy. cwd should be the project root (typically
// os.Getwd() at start-up).
func New(cwd string, mode Mode) (*Policy, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("permission: resolve cwd: %w", err)
	}
	return &Policy{mode: mode, cwd: abs}, nil
}

// SetAskFn registers a callback consulted for each dangerous action
// when the policy is in ModeAsk. The callback is expected to BLOCK
// until the user answers (or the surrounding context cancels). It is
// called from the tool's goroutine, NOT the TUI's — so the callback
// can safely use blocking channel ops to coordinate with the UI.
func (p *Policy) SetAskFn(fn func(Action) bool) {
	if p != nil {
		p.askFn = fn
	}
}

// Mode returns the current mode.
func (p *Policy) Mode() Mode {
	if p == nil {
		return ModeDeny
	}
	return p.mode
}

// SetMode updates the active mode. Used by /yolo to upgrade Ask→Yolo
// mid-session.
func (p *Policy) SetMode(m Mode) {
	if p != nil {
		p.mode = m
	}
}

// ErrDenied is returned when an action is blocked by policy. Callers should
// surface its message verbatim to the LLM — it includes the specific reason
// and the override instructions ("run with --yolo …").
var ErrDenied = errors.New("permission denied")

// Check evaluates an Action against the Policy. Nil = allowed.
//
// Resolution order:
//  1. ModeYolo  → always allow.
//  2. Action is "safe" (write/edit inside CWD; nothing else reaches
//     Check today) → allow.
//  3. ModeAsk + askFn set → consult the callback; allow if true.
//  4. Otherwise → return ErrDenied with a clear message the model
//     can pass back to the user.
func (p *Policy) Check(a Action) error {
	if p == nil {
		return fmt.Errorf("%w: no policy configured", ErrDenied)
	}
	if p.mode == ModeYolo {
		return nil
	}

	// First: is this action even dangerous? Safe actions return nil
	// without ever consulting askFn.
	dangerous := false
	switch a.Kind {
	case KindBash:
		dangerous = true
	case KindWrite, KindEdit:
		if a.Path == "" {
			return fmt.Errorf("%w: %s requires a path", ErrDenied, a.Kind)
		}
		inside, err := isWithin(p.cwd, a.Path)
		if err != nil {
			return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
		}
		if !inside {
			dangerous = true
		}
	default:
		return fmt.Errorf("%w: unknown action kind %q", ErrDenied, a.Kind)
	}

	if !dangerous {
		return nil
	}

	// Dangerous: ask if we can, otherwise deny.
	if p.mode == ModeAsk && p.askFn != nil {
		if p.askFn(a) {
			return nil
		}
		return fmt.Errorf("%w: user declined %s", ErrDenied, a.Kind)
	}

	switch a.Kind {
	case KindBash:
		return fmt.Errorf("%w: bash is gated; re-run seek with --yolo, or run the command yourself: %s",
			ErrDenied, shorten(a.Command, 80))
	default:
		return fmt.Errorf("%w: %s on %q is outside the working directory %q — re-run with --yolo to allow",
			ErrDenied, a.Kind, a.Path, p.cwd)
	}
}

// CWD returns the resolved working directory the policy is anchored to.
func (p *Policy) CWD() string { return p.cwd }

// Yolo reports whether the policy is in unrestricted mode.
func (p *Policy) Yolo() bool { return p != nil && p.mode == ModeYolo }

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
