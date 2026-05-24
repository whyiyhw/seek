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
	"sync"
)

// Kind is the category of a guarded action.
type Kind string

const (
	KindBash           Kind = "bash"
	KindWrite          Kind = "write"
	KindEdit           Kind = "edit"
	KindRead           Kind = "read"
	KindMemoryRemember Kind = "memory_remember"
)

// Action describes one attempt to perform a guarded operation.
type Action struct {
	Kind    Kind
	Path    string // for write/edit
	Command string // for bash; only first ~80 chars are shown in errors

	// Diff is an optional unified diff string populated by the edit tool
	// before it calls Check. When non-empty the TUI renders it alongside
	// the y/N approval prompt so the user can see exactly what will change.
	Diff string

	// MemoryName / MemoryTagline are populated by memory_remember so the
	// TUI can render "save memory: NAME — TAGLINE" alongside the y/N
	// prompt. The full content body is intentionally NOT here — name +
	// tagline is enough decision context, and content can be paragraphs.
	MemoryName    string
	MemoryTagline string
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
	// ModePlan is read-only exploration. All mutations (bash, write,
	// edit, memory_remember) are denied unconditionally — even writes
	// inside CWD that ModeDeny would allow. Reads inside CWD are
	// permitted. Set by --plan or /plan toggle.
	ModePlan
)

// Policy is the per-process permission policy. Construct via New.
//
// Concurrency: `mode` and `askFn` can be updated at runtime (via
// SetMode / SetAskFn) — most commonly when /yolo flips Ask→Yolo
// mid-session from the TUI goroutine while a tool dispatch is calling
// Check on the agent goroutine. The mutex serialises those transitions
// so concurrent Check + SetMode is race-free. `cwd` is set at
// construction and never changes; the mutex covers it anyway because
// it's cheap and avoids a footgun if that assumption ever changes.
type Policy struct {
	mu    sync.RWMutex
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
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.askFn = fn
}

// Mode returns the current mode.
func (p *Policy) Mode() Mode {
	if p == nil {
		return ModeDeny
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// SetMode updates the active mode. Used by /yolo to upgrade Ask→Yolo
// mid-session — called from the TUI goroutine while tool dispatch may
// concurrently be in Check on the agent goroutine, hence the mutex.
func (p *Policy) SetMode(m Mode) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = m
}

// ErrDenied is returned when an action is blocked by policy. Callers should
// surface its message verbatim to the LLM — it includes the specific reason
// and the override instructions ("run with --yolo …").
var ErrDenied = errors.New("permission denied")

// Check evaluates an Action against the Policy. Nil = allowed.
//
// Resolution order:
//  1. ModeYolo  → always allow.
//  2. ModePlan  → read-only: deny bash/write/edit/memory_remember
//     unconditionally; allow reads within CWD only.
//  3. Action is "safe" (write/edit inside CWD; nothing else reaches
//     Check today) → allow.
//  4. ModeAsk + askFn set → consult the callback; allow if true.
//  5. Otherwise → return ErrDenied with a clear message the model
//     can pass back to the user.
func (p *Policy) Check(a Action) error {
	if p == nil {
		return fmt.Errorf("%w: no policy configured", ErrDenied)
	}
	// Snapshot the mutable fields under the lock and release before
	// any potentially slow work (isWithin does I/O via filepath.Abs;
	// askFn blocks on the user). Holding the lock across either would
	// turn a brief read-side lock into a session-long write barrier.
	p.mu.RLock()
	mode := p.mode
	cwd := p.cwd
	askFn := p.askFn
	p.mu.RUnlock()

	if mode == ModeYolo {
		return nil
	}

	// ModePlan: strict read-only. All writes, edits, bash, and
	// memory writes are denied unconditionally — even inside CWD.
	// Reads are allowed only within CWD.
	if mode == ModePlan {
		switch a.Kind {
		case KindRead:
			if a.Path == "" {
				return fmt.Errorf("%w: %s requires a path", ErrDenied, a.Kind)
			}
			inside, err := isWithin(cwd, a.Path)
			if err != nil {
				return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
			}
			if !inside {
				return fmt.Errorf("%w: plan mode: %s outside working directory %q",
					ErrDenied, a.Kind, cwd)
			}
			return nil
		case KindBash:
			return fmt.Errorf("%w: plan mode: bash is not allowed — explore with read/grep/list_dir instead",
				ErrDenied)
		case KindWrite, KindEdit:
			return fmt.Errorf("%w: plan mode: %s is not allowed — produce a plan in your response instead",
				ErrDenied, a.Kind)
		case KindMemoryRemember:
			return fmt.Errorf("%w: plan mode: memory_remember is not allowed",
				ErrDenied)
		default:
			return fmt.Errorf("%w: plan mode: unknown action kind %q", ErrDenied, a.Kind)
		}
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
		inside, err := isWithin(cwd, a.Path)
		if err != nil {
			return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
		}
		if !inside {
			dangerous = true
		}
	case KindRead:
		if a.Path == "" {
			return fmt.Errorf("%w: %s requires a path", ErrDenied, a.Kind)
		}
		inside, err := isWithin(cwd, a.Path)
		if err != nil {
			return fmt.Errorf("%w: resolve path %q: %v", ErrDenied, a.Path, err)
		}
		if !inside {
			dangerous = true
		}
	case KindMemoryRemember:
		// Memory writes are always dangerous — there is no "safe"
		// path equivalent. The TUI shows name+tagline so the user
		// can decide; yolo skips the ask, ModeDeny refuses.
		if a.MemoryName == "" {
			return fmt.Errorf("%w: memory_remember requires a name", ErrDenied)
		}
		dangerous = true
	default:
		return fmt.Errorf("%w: unknown action kind %q", ErrDenied, a.Kind)
	}

	if !dangerous {
		return nil
	}

	// Dangerous: ask if we can, otherwise deny.
	if mode == ModeAsk && askFn != nil {
		if askFn(a) {
			return nil
		}
		return fmt.Errorf("%w: user declined %s", ErrDenied, a.Kind)
	}

	switch a.Kind {
	case KindBash:
		return fmt.Errorf("%w: bash is gated; re-run seek with --yolo, or run the command yourself: %s",
			ErrDenied, shorten(a.Command, 80))
	case KindMemoryRemember:
		return fmt.Errorf("%w: memory_remember %q is gated; re-run seek with --yolo, or save the entry yourself",
			ErrDenied, a.MemoryName)
	default:
		return fmt.Errorf("%w: %s on %q is outside the working directory %q — re-run with --yolo to allow",
			ErrDenied, a.Kind, a.Path, cwd)
	}
}

// CWD returns the resolved working directory the policy is anchored to.
func (p *Policy) CWD() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cwd
}

// Yolo reports whether the policy is in unrestricted mode.
func (p *Policy) Yolo() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode == ModeYolo
}

// Plan reports whether the policy is in read-only plan mode.
func (p *Policy) Plan() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode == ModePlan
}

// isWithin reports whether target resolves to a path inside root (inclusive
// of root itself). Both paths are made absolute and their symlinks are
// resolved before comparison so a symlink inside root that points outside
// is caught.
//
// For non-existent paths (e.g. a new file about to be created) we walk up
// the directory tree until finding an existing ancestor, resolve its
// symlinks, then append the non-existent suffix — preserving the guard.
func isWithin(root, target string) (bool, error) {
	// Resolve the root first so symlinks in root-level paths (e.g.
	// /var → /private/var on macOS) don't cause false denials.
	absRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, fmt.Errorf("resolve root %q: %w", root, err)
	}

	// Resolve symlinks in the target path, walking up if needed.
	resolved, err := resolveClosest(target)
	if err != nil {
		return false, err
	}
	absTarget, err := filepath.Abs(resolved)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(absRoot, absTarget)
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

// resolveClosest resolves symlinks on the deepest existing ancestor of path,
// then appends the non-existent suffix. This handles both existing paths
// (full EvalSymlinks) and new paths (partial resolution up to the nearest
// existing directory).
func resolveClosest(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	// Walk up until we find a parent that exists.
	parent := filepath.Dir(path)
	if parent == path {
		// Reached the root without finding anything — return path as-is.
		return path, nil
	}
	resolvedParent, err := resolveClosest(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
