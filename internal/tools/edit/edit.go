// Package edit implements the `edit` tool: exact-substring replacement in
// a file. The Claude Code / pi convention — old_string must match
// uniquely (or expected_replacements times exactly), new_string=""
// deletes. FIM lives in the separate fim_complete tool, not here.
package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/diff"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":                  {"type": "string", "description": "Path to the file to edit."},
    "old_string":            {"type": "string", "description": "Exact substring to replace. Must include enough surrounding context to be unique unless expected_replacements is set."},
    "new_string":            {"type": "string", "description": "Replacement text. Empty string deletes the match."},
    "expected_replacements": {"type": "integer", "description": "Number of matches expected. Defaults to 1. If the actual count differs the edit fails atomically.", "minimum": 1}
  },
  "required": ["path", "old_string", "new_string"],
  "additionalProperties": false
}`)

const description = "Replace exact substring(s) in a file. old_string must appear in the file the exact number of times given by expected_replacements (default 1). new_string=\"\" deletes the match. Edits outside the working directory are refused unless seek was started with --yolo."

type Args struct {
	Path                 string `json:"path"`
	OldString            string `json:"old_string"`
	NewString            string `json:"new_string"`
	ExpectedReplacements int    `json:"expected_replacements,omitempty"`
}

type Tool struct {
	policy *permission.Policy
}

func New(p *permission.Policy) Tool { return Tool{policy: p} }

func (Tool) Name() string            { return "edit" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("edit", raw, &a, "path", "old_string", "new_string", "expected_replacements"); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", tools.MissingField("edit", "path", raw, "path", "old_string", "new_string", "expected_replacements")
	}
	if a.OldString == "" {
		return "", tools.MissingField("edit", "old_string (must be non-empty)", raw, "path", "old_string", "new_string", "expected_replacements")
	}
	if a.OldString == a.NewString {
		return "", fmt.Errorf("edit: old_string equals new_string — no-op")
	}
	if a.ExpectedReplacements <= 0 {
		a.ExpectedReplacements = 1
	}

	clean := filepath.Clean(a.Path)
	orig, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	content := string(orig)

	got := strings.Count(content, a.OldString)
	if got != a.ExpectedReplacements {
		return "", fmt.Errorf("edit: expected %d replacements but old_string occurs %d times — broaden context or set expected_replacements",
			a.ExpectedReplacements, got)
	}

	updated := strings.ReplaceAll(content, a.OldString, a.NewString)

	// Compute unified diff BEFORE writing so the approval prompt can show
	// exactly what will change. The diff is also included in the tool result
	// so the model gets a structured summary of the change it just made.
	udiff := diff.Unified(content, updated, filepath.Base(clean))

	if err := t.policy.Check(permission.Action{
		Kind: permission.KindEdit,
		Path: a.Path,
		Diff: udiff,
	}); err != nil {
		return "", err
	}

	if err := os.WriteFile(clean, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}

	abs, err := filepath.Abs(clean)
	if err != nil {
		abs = clean
	}
	result := fmt.Sprintf("edited %s: %d replacement(s), %d → %d bytes",
		abs, got, len(orig), len(updated))
	if udiff != "" {
		result += "\n" + udiff
	}
	return result, nil
}

