// Package write implements the `write` tool: create or overwrite a file
// on disk. Always overwrites; relies on the permission Policy to keep the
// blast radius scoped to the project (PRD §4.7 — writes outside CWD
// require --yolo).
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
}

func New(p *permission.Policy) Tool { return Tool{policy: p} }

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

	clean := filepath.Clean(a.Path)
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return "", fmt.Errorf("write: mkdir: %w", err)
	}
	if err := os.WriteFile(clean, []byte(a.Content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	abs, err := filepath.Abs(clean)
	if err != nil {
		abs = clean
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(a.Content), abs), nil
}
