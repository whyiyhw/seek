// Package memorytool implements the two M-layer tool entry points the
// LLM can call: memory_recall (read one entry; bumps last_recalled_at)
// and memory_remember (write one entry; goes through permission.Policy
// for inline y/N approval, mirroring the write/edit tools).
//
// Both tools share a *memory.Project handle injected by the caller —
// the agent loop is single-threaded per Prompt, so a per-session
// shared Project is safe and avoids re-loading manifest+JSONL on every
// invocation.
package memorytool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools"
)

// ----- memory_recall -----

var recallSchema = []byte(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Entry name (kebab-case, matches what memory_remember used)."}
  },
  "required": ["name"],
  "additionalProperties": false
}`)

const recallDescription = "Fetch the full content of a project-memory (M) entry by name. The M-index injected at prompt-start gives you the name + tagline of every active entry; use this when you need the full rationale. Calling it bumps the entry's last_recalled_at and recall_count, which keeps the decay-score forgetter from staling it."

// Recall is the memory_recall tool. ReadOnly so the agent can dispatch
// it in parallel with other read-only calls (e.g. read, grep) — recall
// only writes the recall-count metadata, not the entry body.
type Recall struct {
	project *memory.Project
}

// NewRecall constructs the recall tool. project may be nil — in that
// case Execute returns a fixed message so the model knows memory isn't
// available this session (e.g. seek launched outside any project).
func NewRecall(project *memory.Project) Recall { return Recall{project: project} }

func (Recall) Name() string            { return "memory_recall" }
func (Recall) Description() string     { return recallDescription }
func (Recall) Schema() json.RawMessage { return recallSchema }
func (Recall) ReadOnly() bool          { return true }

type recallArgs struct {
	Name string `json:"name"`
}

func (t Recall) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a recallArgs
	if err := tools.UnmarshalStrict("memory_recall", raw, &a, "name"); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", tools.MissingField("memory_recall", "name", raw, "name")
	}
	if t.project == nil {
		return "memory is not available in this session (no project memory loaded)", nil
	}

	entry, ok := t.project.Get(a.Name)
	if !ok {
		return fmt.Sprintf("memory: no entry named %q", a.Name), nil
	}
	if err := t.project.TouchRecall(a.Name, time.Now().UTC()); err != nil {
		// TouchRecall failure (disk full, EACCES) shouldn't prevent
		// returning the body — surface a warning suffix instead.
		body, _ := json.MarshalIndent(entry, "", "  ")
		return fmt.Sprintf("%s\n\n(warning: failed to update recall metadata: %v)", body, err), nil
	}

	body, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("memory_recall: marshal: %w", err)
	}
	return string(body), nil
}

// ----- memory_remember -----

var rememberSchema = []byte(`{
  "type": "object",
  "properties": {
    "name":    {"type": "string", "description": "kebab-case unique key for this entry (e.g. session-storage-format)."},
    "tagline": {"type": "string", "description": "One-line summary shown in the M-index. Make it specific."},
    "content": {"type": "string", "description": "Full rationale: what was decided, why, what alternatives were rejected. ≤500 words."},
    "tags":    {"type": "array",  "items": {"type": "string"}, "description": "Optional categorisation tags."}
  },
  "required": ["name", "tagline", "content"],
  "additionalProperties": false
}`)

const rememberDescription = "Save a project-specific decision + rationale to project memory (M layer). Requires user approval per call (inline y/N prompt). Use this when you've learned something the next session in this project would benefit from knowing — not user preferences (those live in L), not session-scoped facts (those stay in conversation history). Re-calling with the same name updates the existing entry."

type Remember struct {
	project *memory.Project
	policy  *permission.Policy
}

// NewRemember constructs the remember tool. project may be nil (same
// behaviour as Recall — memory unavailable). policy MUST be non-nil;
// memory_remember is unconditionally gated.
func NewRemember(project *memory.Project, p *permission.Policy) Remember {
	return Remember{project: project, policy: p}
}

func (Remember) Name() string            { return "memory_remember" }
func (Remember) Description() string     { return rememberDescription }
func (Remember) Schema() json.RawMessage { return rememberSchema }

type rememberArgs struct {
	Name    string   `json:"name"`
	Tagline string   `json:"tagline"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
}

func (t Remember) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a rememberArgs
	if err := tools.UnmarshalStrict("memory_remember", raw, &a, "name", "tagline", "content", "tags"); err != nil {
		return "", err
	}
	for _, missing := range []struct {
		val   string
		field string
	}{
		{a.Name, "name"},
		{a.Tagline, "tagline"},
		{a.Content, "content"},
	} {
		if missing.val == "" {
			return "", tools.MissingField("memory_remember", missing.field, raw, "name", "tagline", "content", "tags")
		}
	}
	if t.project == nil {
		return "", errors.New("memory_remember: memory is not available in this session")
	}

	if err := t.policy.Check(permission.Action{
		Kind:          permission.KindMemoryRemember,
		MemoryName:    a.Name,
		MemoryTagline: a.Tagline,
	}); err != nil {
		return "", err
	}

	if err := t.project.Add(memory.Entry{
		Name:    a.Name,
		Tagline: a.Tagline,
		Content: a.Content,
		Tags:    a.Tags,
	}); err != nil {
		return "", fmt.Errorf("memory_remember: %w", err)
	}
	return fmt.Sprintf("remembered %q in project memory", a.Name), nil
}
