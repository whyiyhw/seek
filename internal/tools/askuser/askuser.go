// Package askuser implements the ask_user tool: the model emits a
// Question with 2-4 options, the TUI shows a picker, the user
// arrow-keys + Enters their answer, and the tool returns the
// structured result. PRD reference: TUI choice picker (M9.x).
//
// Wire model: the tool holds an *askuser.Policy (in
// internal/askuser, NOT this package — same name, different role:
// that one is the cross-package channel + state, this one is the
// LLM-facing schema and JSON shape). cmd/seek constructs the policy
// once at startup and injects it into the tool's constructor.
package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	auser "github.com/whyiyhw/seek/internal/askuser"
	"github.com/whyiyhw/seek/internal/tools"
)

const toolName = "ask_user"

// schemaBytes is the JSON Schema the LLM sees. The "options" array
// is constrained to 2-4 items by minItems / maxItems; the tool's
// Execute re-validates so the LLM gets a clear error if it tried
// to bypass the schema.
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "question": {
      "type": "string",
      "description": "Short header shown to the user (one line, no markdown). State the choice they need to make."
    },
    "options": {
      "type": "array",
      "minItems": 2,
      "maxItems": 4,
      "description": "The 2-4 distinct, mutually-exclusive choices to present. seek auto-appends an 'Other / type your own answer' row so do NOT include one yourself.",
      "items": {
        "type": "object",
        "properties": {
          "id": {
            "type": "string",
            "description": "Stable identifier returned to you on selection. Use kebab-case. The id 'other' is reserved."
          },
          "label": {
            "type": "string",
            "description": "Short row text the user sees in the picker. Keep under ~60 chars."
          },
          "description": {
            "type": "string",
            "description": "Optional secondary text shown muted next to the label. Use when the label alone is ambiguous."
          }
        },
        "required": ["id", "label"],
        "additionalProperties": false
      }
    },
    "multi_select": {
      "type": "boolean",
      "description": "true = user can toggle multiple rows with Space then Enter to confirm. false (default) = single selection: highlighted row + Enter accepts immediately. Use multi_select only when the choices are non-exclusive (e.g. 'which features?'); leave false for either/or decisions."
    }
  },
  "required": ["question", "options"],
  "additionalProperties": false
}`)

const description = "Ask the user to pick from 2-4 discrete choices via an inline TUI picker. Use this INSTEAD of asking the question as plain text when: (a) the choices are mutually distinct, (b) getting the wrong one would cost a real round-trip, and (c) you have 4 or fewer concrete options. Examples: 'which scope: user or project?', 'overwrite, skip, or rename?', 'commit now or open PR?'. Do NOT use for free-form questions ('what should I name this?'), open-ended preferences ('what's your style?'), or anything where the user genuinely needs to type prose — those work better as plain conversation."

// Tool is the ask_user implementation. Holds an *auser.Policy that
// bridges to the TUI; the policy's Ask call blocks until the user
// answers.
type Tool struct {
	policy *auser.Policy
}

// New constructs the tool. policy must be non-nil — without it the
// tool can't reach the user.
func New(policy *auser.Policy) Tool { return Tool{policy: policy} }

func (Tool) Name() string            { return toolName }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

type option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type args struct {
	Question    string   `json:"question"`
	Options     []option `json:"options"`
	MultiSelect bool     `json:"multi_select"`
}

// result is the JSON the LLM sees back. Exactly one of ChosenIDs /
// FreeText is non-empty; Cancelled=true means neither. Multi-select
// answers populate ChosenIDs with the toggled subset; single-select
// answers populate it with one entry.
type result struct {
	ChosenIDs []string `json:"chosen_ids,omitempty"`
	FreeText  string   `json:"free_text,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
}

func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a args
	if err := tools.UnmarshalStrict(toolName, raw, &a, "question", "options", "multi_select"); err != nil {
		return "", err
	}
	if t.policy == nil {
		return "", errors.New(toolName + ": no policy configured (programming error)")
	}

	// Translate the on-the-wire shape into the internal Question
	// type. Validation runs after the translation so error
	// messages reference the user-visible field positions.
	q := auser.Question{
		Question:    a.Question,
		MultiSelect: a.MultiSelect,
		Options:     make([]auser.Option, 0, len(a.Options)),
	}
	for _, o := range a.Options {
		q.Options = append(q.Options, auser.Option{ID: o.ID, Label: o.Label, Description: o.Description})
	}
	if err := auser.Validate(q); err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}

	// Ask blocks until the TUI's callback returns an Answer. The
	// agent's goroutine sleeps here; the TUI keeps painting.
	ans, err := t.policy.Ask(q)
	if err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}

	res := result{
		ChosenIDs: ans.ChosenIDs,
		FreeText:  ans.FreeText,
		Cancelled: ans.Cancelled,
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", toolName, err)
	}
	return string(out), nil
}
