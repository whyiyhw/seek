package memorytool

import (
	"context"
	"encoding/json"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/tools"
)

// ----- memory_observe -----

var observeSchema = []byte(`{
  "type": "object",
  "properties": {
    "name":    {"type": "string", "description": "kebab-case unique key for this entry (e.g. session-storage-format)."},
    "tagline": {"type": "string", "description": "One-line summary shown in the M-index. Make it specific."},
    "content": {"type": "string", "description": "Full rationale: what was decided, why, what alternatives were rejected. ≤500 words."},
    "tags":    {"type": "array",  "items": {"type": "string"}, "description": "Optional categorisation tags. The FIRST tag determines the entry's group in the M-index; put the most representative one first."}
  },
  "required": ["name", "tagline", "content"],
  "additionalProperties": false
}`)

const observeDescription = `Save a project-specific decision to M by observing strong signals during conversation.

Use this when you detect strong signals in the conversation:
- User confirmed a specific decision ("use JSONL", "let's go with option A")
- User corrected you and settled on an alternative ("no, use this API instead")
- User provided a critical constraint ("note this API has a rate limit")
- User expressed clear satisfaction after a resolved discussion

Do NOT use for:
- Casual conversation ("thanks", "good morning")
- Undecided states ("maybe", "let's see")
- Topics already captured under the same name (memory_observe overwrites by name; use memory_amend to append instead)

This tool returns immediately without blocking. The entry goes through an async
V4-Flash dedup + value check before being written. Only passes are persisted;
rejects are silently discarded.`

// Observe is the memory_observe tool. It returns empty string immediately and
// enqueues the entry for async filtering via the Hook.
type Observe struct {
	project *memory.Project
	// enqueue is injected by the Hook at construction time. It starts the async
	// filter goroutine. nil = memory unavailable (tool returns error).
	enqueue func(context.Context, memory.Entry)
}

// NewObserve constructs the observe tool. project may be nil (memory
// unavailable). enqueue is called by Execute to start the async filter;
// it must be non-blocking (launches goroutine internally).
func NewObserve(project *memory.Project, enqueue func(context.Context, memory.Entry)) Observe {
	return Observe{project: project, enqueue: enqueue}
}

func (Observe) Name() string            { return "memory_observe" }
func (Observe) Description() string     { return observeDescription }
func (Observe) Schema() json.RawMessage { return observeSchema }

type observeArgs struct {
	Name    string   `json:"name"`
	Tagline string   `json:"tagline"`
	Content string   `json:"content"`
	Tags    []string `json:"tags,omitempty"`
}

func (t Observe) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a observeArgs
	if err := tools.UnmarshalStrict("memory_observe", raw, &a, "name", "tagline", "content", "tags"); err != nil {
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
			return "", tools.MissingField("memory_observe", missing.field, raw, missing.field)
		}
	}

	if t.project == nil {
		return "memory is not available in this session (no project memory loaded)", nil
	}
	if t.enqueue == nil {
		return "memory_observe: async filter not available (hook not configured)", nil
	}

	entry := memory.Entry{
		Name:    a.Name,
		Tagline: a.Tagline,
		Content: a.Content,
		Tags:    a.Tags,
	}

	// Enqueue launches the async filter goroutine. Non-blocking.
	t.enqueue(ctx, entry)

	// Return empty string — the tool call succeeds silently. The TUI will
	// show a notification if the filter passes (via ResultChan).
	return "", nil
}
