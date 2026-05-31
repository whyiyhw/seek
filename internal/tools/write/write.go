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
	"fmt"
	"os"
	"path/filepath"

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

const description = "Create or overwrite a file with the given contents. Parent directories are created automatically. Writes outside the working directory are refused unless seek was started with --yolo."

type Args struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type Tool struct {
	policy *permission.Policy
	snap   Snapshotter
}

func New(p *permission.Policy) Tool { return Tool{policy: p} }

// WithSnapshotter returns a copy of t bound to s. Optional — leaving
// the snapshotter unset (nil) disables file checkpoint integration.
func (t Tool) WithSnapshotter(s Snapshotter) Tool {
	t.snap = s
	return t
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
	if err := os.WriteFile(clean, []byte(a.Content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if t.snap != nil {
		_ = t.snap.FinaliseSnapshot(clean, []byte(a.Content))
	}

	abs, err := filepath.Abs(clean)
	if err != nil {
		abs = clean
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), abs), nil
}
