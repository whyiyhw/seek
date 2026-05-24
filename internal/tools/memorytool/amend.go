package memorytool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/tools"
)

// ----- memory_amend -----

var amendSchema = []byte(`{
  "type": "object",
  "properties": {
    "name":           {"type": "string", "description": "Entry name (kebab-case, matches what memory_remember or memory_observe used)."},
    "append_content": {"type": "string", "description": "New content to append to the existing entry's rationale. Timestamped and separated automatically."}
  },
  "required": ["name", "append_content"],
  "additionalProperties": false
}`)

const amendDescription = `Append new information to an existing project-memory entry. The appended content is timestamped and separated from the original content automatically.

Use this when:
- You need to supplement an existing entry with additional context
- A decision has evolved and you want to record the update
- You want to add new evidence supporting an existing observation

This is the "update" counterpart to memory_observe. Unlike memory_observe, which overwrites the entire entry, memory_amend preserves the original rationale and adds to it.`

// Amend is the memory_amend tool.
type Amend struct {
	project *memory.Project
}

// NewAmend constructs the amend tool. project may be nil — in that
// case Execute returns a fixed message so the model knows memory isn't
// available this session.
func NewAmend(project *memory.Project) Amend {
	return Amend{project: project}
}

func (Amend) Name() string            { return "memory_amend" }
func (Amend) Description() string     { return amendDescription }
func (Amend) Schema() json.RawMessage { return amendSchema }

type amendArgs struct {
	Name          string `json:"name"`
	AppendContent string `json:"append_content"`
}

func (t Amend) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a amendArgs
	if err := tools.UnmarshalStrict("memory_amend", raw, &a, "name", "append_content"); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", tools.MissingField("memory_amend", "name", raw, "name", "append_content")
	}
	if strings.TrimSpace(a.AppendContent) == "" {
		return "", tools.MissingField("memory_amend", "append_content", raw, "name", "append_content")
	}
	if t.project == nil {
		return "", errors.New("memory_amend: memory is not available in this session")
	}

	if err := t.project.Amend(a.Name, strings.TrimSpace(a.AppendContent), time.Now().UTC()); err != nil {
		if errors.Is(err, memory.ErrNotFound) {
			return fmt.Sprintf("memory_amend: entry %q not found", a.Name), nil
		}
		return "", fmt.Errorf("memory_amend: %w", err)
	}

	return fmt.Sprintf("appended to %q", a.Name), nil
}
