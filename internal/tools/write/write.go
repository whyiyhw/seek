// Package write implements the `write` tool: create or overwrite a file
// on disk. Always overwrites; relies on the permission Policy to keep the
// blast radius scoped to the project (PRD §4.7 — writes outside CWD
// require --yolo).
//
// v3 (feature-checkpoint): if a Snapshotter is configured we snapshot
// the prior content before writing and finalise after, feeding the
// file-checkpoint subsystem. Snapshot failures are non-fatal — the
// write still proceeds; the safety net just degrades for that one
// file.
package write

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/whyiyhw/seek/internal/fsobserve"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools"
)

// Snapshotter is the optional dependency for file checkpoint. Wire
// to *checkpoint.Manager in cmd/seek; tests usually leave it nil.
//
// The interface lives here rather than in internal/checkpoint to
// avoid the import cycle and to make the tool's coupling explicit
// at the call site.
type Snapshotter interface {
	SnapshotFile(path, toolName, callID string) error
	FinaliseSnapshot(path string, after []byte) error
}

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":    {"type": "string", "description": "Absolute or repo-relative path. Parent directories are created if missing."},
    "content": {"type": "string", "description": "Full file contents to write."}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`)

const description = "Create or overwrite a file with the given contents. Parent directories are created automatically. Overwriting an EXISTING file requires that you read it first in this session — write replaces the whole file, so an unread or externally-changed target is refused with instructions to read it. Prefer edit for partial changes; use write for new files and full rewrites. Writes outside the working directory are refused unless seek was started with --yolo."

type Args struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Tool struct {
	policy   *permission.Policy
	snap     Snapshotter
	observer *fsobserve.Store
}

func New(p *permission.Policy) Tool { return Tool{policy: p} }

// WithObserver enables the blind-overwrite guard: a write to an existing
// file is refused unless `read` has seen its current contents in this
// session.
//
// Why write needs this and edit does not: edit matches an exact
// old_string with an expected occurrence count, so an edit built on a
// stale or imagined view simply fails to apply. write is
// os.WriteFile — full replacement, no matching — so nothing stops it
// from discarding content the model never saw. The checkpoint
// snapshotter makes that recoverable, but recovery needs a human to
// notice, which is exactly what does not happen unattended.
//
// Optional: a nil observer leaves write's behaviour unchanged.
func (t Tool) WithObserver(s *fsobserve.Store) Tool {
	t.observer = s
	return t
}

// WithSnapshotter returns a copy of t bound to s. Optional — leaving
// the snapshotter unset (nil) disables file checkpoint integration.
func (t Tool) WithSnapshotter(s Snapshotter) Tool {
	t.snap = s
	return t
}

// writeGuarded performs the write that the plan authorised, re-asserting
// the plan's precondition at the syscall level.
//
// The naive shape — check, then os.WriteFile — has a window between the
// two in which the world can change, and the change that matters is
// exactly the one the guard exists to catch: a file appearing at a path
// the model was told was empty, or being replaced between the check and
// the write. The realistic racer is not another goroutine, it is the
// user's editor, a running build, or a `git checkout` in another
// terminal.
//
// So the precondition is re-established by the operation itself, the way
// dsh resolves its write intent inside the provider's locked region
// (packages/fs/fs-local/src/index.ts:178-187):
//
//   - target was absent  → O_CREATE|O_EXCL. The kernel guarantees we are
//     the creator; EEXIST means someone won the race, which is precisely
//     an unread-file overwrite, so it is refused with the same message.
//   - target was present → open WITHOUT O_CREATE and verify the token
//     through the file descriptor. Stat'ing the fd rather than the path
//     is what makes the check meaningful: the file being verified is
//     provably the file being written, so a rename-over between open and
//     stat cannot slip a different file past the guard.
func writeGuarded(path string, content []byte, plan fsobserve.Decision) error {
	if !plan.Guarded {
		// No observer configured, or a non-regular target. Unchanged
		// pre-guard behaviour.
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return nil
	}

	if !plan.Exists {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				// Appeared between Plan and here. Same hazard as an
				// unread existing file, so the same refusal.
				return errors.New(fsobserve.Explain(fsobserve.StatusUnseen, path))
			}
			return fmt.Errorf("write: %w", err)
		}
		defer f.Close()
		if _, err := f.Write(content); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return nil
	}

	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Observed present at plan time, gone now — someone deleted
			// it. Treat as stale rather than silently recreating it.
			return errors.New(fsobserve.Explain(fsobserve.StatusStale, path))
		}
		return fmt.Errorf("write: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("write: stat: %w", err)
	}
	if !plan.Matches(fi) {
		return errors.New(fsobserve.Explain(fsobserve.StatusStale, path))
	}

	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("write: truncate: %w", err)
	}
	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

func (Tool) Name() string            { return "write" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("write", raw, &a, "path", "content"); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", tools.MissingField("write", "path", raw, "path", "content")
	}

	if err := t.policy.Check(permission.Action{Kind: permission.KindWrite, Path: a.Path}); err != nil {
		return "", err
	}

	clean := t.policy.Resolve(a.Path)

	// Blind-overwrite guard. Refusing rather than warning is deliberate:
	// a warning is appended to the result AFTER the file has already been
	// replaced, which tells the model it may have destroyed something it
	// cannot get back without a checkpoint restore. The refusal costs one
	// `read` call; the warning costs the content.
	//
	// This is a tool error, which seek surfaces to the model as a tool
	// result (not a fatal), so the model can act on it and retry — the
	// same contract permission denials use.
	plan := t.observer.Plan(clean)
	if plan.Status != fsobserve.StatusOK {
		return "", errors.New(fsobserve.Explain(plan.Status, clean))
	}

	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return "", fmt.Errorf("write: mkdir: %w", err)
	}
	// File checkpoint: snapshot the prior content (if any) BEFORE
	// writing. Per PRD §3.2 a snapshot failure does NOT block the
	// write — Snapshotter.SnapshotFile is responsible for logging
	// via its Sink.
	if t.snap != nil {
		_ = t.snap.SnapshotFile(clean, "write", "")
	}
	if err := writeGuarded(clean, []byte(a.Content), plan); err != nil {
		return "", err
	}
	if t.snap != nil {
		_ = t.snap.FinaliseSnapshot(clean, []byte(a.Content))
	}
	// The model authored this content, so it has by definition seen the
	// file's new state. Without this, a legitimate write → write sequence
	// would trip the stale check on the model's own previous write.
	t.observer.Observe(clean)

	abs, err := filepath.Abs(clean)
	if err != nil {
		abs = clean
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), abs), nil
}
