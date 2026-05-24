package memorytool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/tools"
)

// ----- memory_archive -----

var archiveSchema = []byte(`{
  "type": "object",
  "properties": {
    "name":   {"type": "string", "description": "Entry name (kebab-case, matches what memory_remember or memory_observe used)."},
    "reason": {"type": "string", "description": "Why this entry is being archived. Required — helps audit trail and prevents accidental archivals."}
  },
  "required": ["name", "reason"],
  "additionalProperties": false
}`)

const archiveDescription = `Move a project-memory entry to the archive. The entry is removed from the active M-index (so it no longer appears in the injected project-memory index), but its full content is preserved in archived.jsonl for future reference.

Use this when:
- An entry contains information that is no longer accurate or relevant
- A decision has been superseded by a newer entry
- The tagline/content is misleading and a clean removal is better than editing

This is the "active forgetting" counterpart to memory_observe. Together they let you curate the M-index without waiting for the decay-score GC to act.

Archived entries are NOT shown in the M-index and are NOT returned by memory_recall. They can still be inspected via 'seek memory list --archived' or by reading ~/.seek/projects/<id>/archived.jsonl directly.`

// Archive is the memory_archive tool.
type Archive struct {
	project *memory.Project
}

// NewArchive constructs the archive tool. project may be nil — in that
// case Execute returns a fixed message so the model knows memory isn't
// available this session.
func NewArchive(project *memory.Project) Archive {
	return Archive{project: project}
}

func (Archive) Name() string            { return "memory_archive" }
func (Archive) Description() string     { return archiveDescription }
func (Archive) Schema() json.RawMessage { return archiveSchema }

type archiveArgs struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func (t Archive) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a archiveArgs
	if err := tools.UnmarshalStrict("memory_archive", raw, &a, "name", "reason"); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", tools.MissingField("memory_archive", "name", raw, "name", "reason")
	}
	if a.Reason == "" {
		return "", tools.MissingField("memory_archive", "reason", raw, "name", "reason")
	}
	if t.project == nil {
		return "", errors.New("memory_archive: memory is not available in this session")
	}

	if err := t.project.Archive(a.Name, a.Reason); err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			return fmt.Sprintf("memory_archive: entry %q not found", a.Name), nil
		}
		return "", fmt.Errorf("memory_archive: %w", err)
	}

	return fmt.Sprintf("archived %q (reason: %s)", a.Name, a.Reason), nil
}
