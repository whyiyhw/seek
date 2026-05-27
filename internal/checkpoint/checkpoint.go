// Package checkpoint implements seek's two-layer "safety net" for
// model-driven changes (PRD v3 柱 A · docs/prd/feature-checkpoint.md).
//
// Two layers, intentionally complementary:
//
//   - Git checkpoint (this package's git.go) — once per turn, before
//     the first destructive action (write / edit / mutating bash),
//     we snapshot the working tree with `git stash create` and pin
//     the resulting commit object under refs/seek/checkpoints/<sid>/<turn>.
//     Coarse-grained, cross-tool (catches bash-driven changes too),
//     restorable via `/restore` → `git read-tree --reset -u <ref>`.
//
//   - File checkpoint (this package's file.go) — every write/edit
//     snapshots the prior file body to a content-addressed blob in
//     <session-dir>/checkpoints/blobs/sha256/<aa>/<bb>/<rest>, with
//     an append-only event log at <session-dir>/checkpoints/index.jsonl.
//     Fine-grained, fast, works without git. `/undo` and `/redo`
//     walk the event log in reverse / forward direction.
//
// Both layers are best-effort: failures are logged via Sink.Warn and
// never block the caller. The whole point is to be a safety net the
// user forgets exists until they need it; bubbling errors up would
// surface a feature that should be invisible.
//
// Design constraints (load-bearing — see PRD v3 §2):
//
//   - Zero impact on the prompt byte stream. Writing refs / blobs /
//     index lines is filesystem-only; nothing in this package gets
//     stitched into the LLM history.
//
//   - No permission picker. Checkpoint writes are "trust already
//     established" actions: a user who turned the agent loose with
//     write/edit has already opted into the work; asking permission
//     for the safety net would be theatre. Cleanup on SessionEnd is
//     similarly automatic.
//
//   - Graceful degradation. Non-git working tree, missing `git`
//     binary, malformed prior index — all log + continue. The only
//     hard error states are user-facing CLI / TUI commands ("you
//     asked to restore turn 5 but turn 5 doesn't exist").
//
// The two halves share Sink (one warn channel) and the session-id /
// project-abs configuration but otherwise live in parallel files
// so a reader chasing one layer is not forced to skim the other.
package checkpoint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/whyiyhw/seek/internal/paths"
)

// Sink is the optional callback target for non-fatal warnings from
// checkpoint writes. Callers that care about visibility (TUI) plug
// in something that surfaces to the status bar; callers that don't
// (tests, batch CLI) leave it nil.
//
// Warn is called from arbitrary goroutines and must be cheap +
// non-blocking. The recommended implementation is a buffered chan
// send with default-drop on overflow.
type Sink interface {
	Warn(msg string)
}

// nopSink is the fallback when caller leaves Sink nil. Centralised
// so every Manager method can use the same shape regardless of
// whether a real Sink was configured.
type nopSink struct{}

func (nopSink) Warn(string) {}

// CheckpointEvent is exported for the TUI status-bar notifier. A
// Manager fires one after every successful checkpoint write so a
// listener can flash "✓ checkpoint #N" without polling the index.
//
// Kind is "git" or "file"; Turn is the git turn index (only
// meaningful when Kind=="git"); Path is the file path (only
// meaningful when Kind=="file"); Seq is the file index seq.
type CheckpointEvent struct {
	Kind  string
	Turn  int
	Path  string
	Seq   int64
	Label string
}

// EventSink is an optional sibling-interface — if the Sink also
// implements OnCheckpoint, the manager fires events into it. Per
// CLAUDE.md "Sink interfaces: don't break the main contract — add
// OPTIONAL sibling interfaces" we keep the core Sink signature
// minimal and upcast at call sites.
type EventSink interface {
	OnCheckpoint(ev CheckpointEvent)
}

// Manager is the per-session façade over both checkpoint layers.
// One Manager per active session; constructed in cmd/seek/main.go
// after the session is loaded / created.
//
// Concurrency: write/edit tool calls can fire SnapshotFile from the
// tool goroutine; MaybeCreateGit fires from the permission Check
// path (same goroutine in practice but defensively serialised).
// SessionEnd observer cleanup runs from the agent-shutdown goroutine.
// The mutex serialises turn-index bookkeeping and the per-path file
// checkpoint state.
type Manager struct {
	mu sync.Mutex

	sessionID  string
	projectAbs string
	cwd        string

	// gitAvailable / gitChecked memoises the "is this a git working
	// tree and is the git binary on PATH?" check. Decided lazily on
	// first MaybeCreateGit so a non-interactive `seek -p` against
	// a non-git scratch directory pays zero startup cost.
	gitChecked   bool
	gitAvailable bool
	// gitHintLogged ensures the "git checkpoint disabled" hint goes
	// to Sink.Warn exactly once per session (PRD §3.1).
	gitHintLogged bool

	// turnIdx is the monotonically-increasing turn counter exposed
	// in the git ref name. Bumped only when a git checkpoint is
	// actually written; an empty turn (no destructive actions) does
	// NOT consume a turn index.
	turnIdx int
	// turnArmed flips false after the first MaybeCreateGit in a turn
	// succeeds; reset to true on every PreTurn observation. Encodes
	// "only one git checkpoint per turn".
	turnArmed bool

	// fileState holds the per-path file-checkpoint undo/redo stacks
	// and the redo-truncation flag. Lazily loaded from index.jsonl
	// on first access.
	fileLoaded bool
	fileState  fileIndex

	// keepOnExit suppresses the SessionEnd cleanup pass. Set when
	// the user launched with --keep-checkpoints.
	keepOnExit bool

	// sinkPlain handles plain Warn; sinkEvent is optionally set when
	// Sink also implements EventSink so the manager can fire status-
	// bar events. Both default to no-op.
	sinkPlain Sink
	sinkEvent EventSink

	// runGit is the override hook tests use to short-circuit the
	// real `git` binary. Production code leaves it nil → runGitReal.
	runGit gitRunner
}

// Config is the constructor surface for NewManager.
type Config struct {
	SessionID  string
	ProjectAbs string
	// CWD is the working directory git operations run in. Usually
	// equal to ProjectAbs but kept separate so future "subproject
	// checkpoint" support can scope to a different tree.
	CWD string
	// Sink receives non-fatal warning messages. May be nil.
	Sink Sink
	// KeepOnExit suppresses the SessionEnd cleanup pass when true.
	// Wire to the --keep-checkpoints CLI flag.
	KeepOnExit bool
}

// New constructs a Manager. Never returns an error — every config
// problem (empty session id, missing cwd) is downgraded to a
// disabled manager so the rest of seek keeps working. The
// disabled-manager state is identical to "non-git directory":
// every method is a no-op that logs once.
//
// A nil *Manager is also valid: every method handles nil receivers
// for the "checkpoint disabled" case (--no-save sessions).
func New(cfg Config) *Manager {
	m := &Manager{
		sessionID:  cfg.SessionID,
		projectAbs: cfg.ProjectAbs,
		cwd:        cfg.CWD,
		keepOnExit: cfg.KeepOnExit,
		turnArmed:  true,
	}
	if cfg.Sink == nil {
		m.sinkPlain = nopSink{}
	} else {
		m.sinkPlain = cfg.Sink
		if es, ok := cfg.Sink.(EventSink); ok {
			m.sinkEvent = es
		}
	}
	return m
}

// SessionID returns the session id this manager is scoped to. Useful
// for tests and for the CLI when assembling user-facing paths.
func (m *Manager) SessionID() string {
	if m == nil {
		return ""
	}
	return m.sessionID
}

// ProjectAbs returns the project absolute path.
func (m *Manager) ProjectAbs() string {
	if m == nil {
		return ""
	}
	return m.projectAbs
}

// CheckpointDir resolves the on-disk root for this session's
// checkpoints — <seek_home>/projects/<id>/sessions/<sid>/. Lazy:
// callers can MkdirAll on first write.
func (m *Manager) CheckpointDir() (string, error) {
	if m == nil || m.sessionID == "" || m.projectAbs == "" {
		return "", fmt.Errorf("checkpoint: manager not configured")
	}
	return paths.SessionCheckpointDir(m.projectAbs, m.sessionID)
}

// CheckpointSubDir returns <session-dir>/checkpoints/ (the file
// CAS root). Created on demand by callers.
func (m *Manager) CheckpointSubDir() (string, error) {
	root, err := m.CheckpointDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "checkpoints"), nil
}

// gitIndexPath is the per-session git checkpoint index file path:
// <session-dir>/checkpoints.jsonl. Append-only.
func (m *Manager) gitIndexPath() (string, error) {
	root, err := m.CheckpointDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "checkpoints.jsonl"), nil
}

// fileIndexPath returns <session-dir>/checkpoints/index.jsonl —
// the file checkpoint append-only event log.
func (m *Manager) fileIndexPath() (string, error) {
	sub, err := m.CheckpointSubDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sub, "index.jsonl"), nil
}

// blobsDir returns <session-dir>/checkpoints/blobs/sha256/ — the
// content-addressed blob root used by file checkpoints.
func (m *Manager) blobsDir() (string, error) {
	sub, err := m.CheckpointSubDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(sub, "blobs", "sha256"), nil
}

// OnPreTurn re-arms the per-turn git checkpoint flag. Wire this to
// hooks.PreTurnObserver so a new turn re-enables "snapshot before
// the first destructive op". A manager that has never seen a
// PreTurn still works (turnArmed defaults true).
func (m *Manager) OnPreTurn(_ context.Context, _ any) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.turnArmed = true
	m.mu.Unlock()
}

// OnSessionEnd cleans up the file checkpoint blob directory unless
// the user passed --keep-checkpoints. Errors are logged to Sink.Warn
// (never returned). The git checkpoint refs are NOT touched — they
// live in git's gc cycle (90-day reflogExpire by default). See PRD
// §3.1 for the rationale.
func (m *Manager) OnSessionEnd(_ context.Context, _ any) {
	if m == nil || m.keepOnExit {
		return
	}
	sub, err := m.CheckpointSubDir()
	if err != nil {
		return
	}
	if err := os.RemoveAll(sub); err != nil && !os.IsNotExist(err) {
		m.sinkPlain.Warn(fmt.Sprintf("checkpoint cleanup: %v", err))
	}
}

// Warn proxies through to the configured Sink. Exported so the
// CLI / TUI helpers can route their own messages through the same
// channel for uniform formatting.
func (m *Manager) Warn(msg string) {
	if m == nil {
		return
	}
	m.sinkPlain.Warn(msg)
}

// emit fires a CheckpointEvent into the optional EventSink. Always
// safe to call (nil-tolerant).
func (m *Manager) emit(ev CheckpointEvent) {
	if m == nil || m.sinkEvent == nil {
		return
	}
	m.sinkEvent.OnCheckpoint(ev)
}
