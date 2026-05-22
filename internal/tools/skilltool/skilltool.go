// Package skilltool exposes the `Skill` tool: the model calls it with a
// skill name and the tool returns that skill's Markdown body as the
// tool result. This is the on-demand half of the Skill mechanism — the
// system-prompt manifest tells the model WHICH skills exist; this tool
// hands over the instructions WHEN the model decides to use one.
//
// See PRD §4.6.3 for the rationale on not stuffing every skill body
// into the system prompt up front.
package skilltool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/tools"
)

// Tool name kept TitleCased ("Skill") to match the convention in
// PRD §4.6.3 and to make it visually distinct from the file-manipulation
// tools (read/write/edit/etc.) in tool-call traces.
const toolName = "Skill"

// schemaBytes is the JSON Schema sent to DeepSeek. Declared as a
// package-level []byte constant per the CLAUDE.md convention — same
// bytes every turn so the cache prefix stays stable.
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "The skill's kebab-case name as listed in the Available skills section of the system prompt."}
  },
  "required": ["name"],
  "additionalProperties": false
}`)

const description = "Fetch the instructions for a skill listed in the Available skills section. Returns the skill's Markdown body; follow its steps for the current task. Call this when the user's request matches a skill's description."

// Tool implements tools.Tool against an in-memory skill.Set. The set is
// loaded once at startup and shared by reference (read-only).
type Tool struct {
	set *skill.Set
}

// New constructs the tool around a loaded set. nil set means "no
// skills available" — the tool still registers (the model may try to
// call it) but every invocation reports the missing-name path.
func New(set *skill.Set) Tool { return Tool{set: set} }

func (Tool) Name() string            { return toolName }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

type args struct {
	Name string `json:"name"`
}

func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a args
	if err := tools.UnmarshalStrict("Skill", raw, &a, "name"); err != nil {
		return "", err
	}
	if a.Name == "" {
		return "", tools.MissingField("Skill", "name", raw, "name")
	}
	if t.set == nil {
		return "", fmt.Errorf("Skill: no skills are loaded in this session")
	}
	sk := t.set.Get(a.Name)
	if sk == nil {
		// List what IS available so the model can recover without
		// another round-trip — much cheaper than letting it guess.
		var names []string
		for _, x := range t.set.List() {
			names = append(names, x.Name)
		}
		return "", fmt.Errorf("Skill: %q not found. Available: %v", a.Name, names)
	}
	// Prefix the body with the skill's own header so the model can
	// distinguish "skill instructions" from "user message" in the
	// resulting tool_result. The body itself often starts with a
	// Markdown H1, but not always.
	return fmt.Sprintf("# Skill: %s\n_Source: %s_\n\n%s",
		sk.Name, sk.Source, sk.Body), nil
}
