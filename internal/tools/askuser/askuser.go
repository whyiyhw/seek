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
)

const toolName = "ask_user"

// schemaBytes is the JSON Schema the LLM sees. Supports two input
// forms (v1 single + v2 batch); the Execute function branches on
// which fields the model populated. The schema is intentionally
// permissive on additionalProperties (omitted globally) so the
// model picks whichever form is closer to its training and we
// don't get false "unknown field" rejections from validators.
//
// The "options" array is constrained to 2-4 items by minItems /
// maxItems in both forms; the tool's Execute re-validates so the
// LLM gets a clear error if it tried to bypass the schema.
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "question": {
      "type": "string",
      "description": "[v1 single-question form] Short header shown to the user (one line, no markdown). State the choice they need to make. Use this for single questions; for 2-4 independent questions in one call, use the 'questions' array form instead."
    },
    "header": {
      "type": "string",
      "description": "[v1 single-question form] Optional short chip-style label (1-2 words: 'Framework', 'Storage', 'Auth'). Typically omitted for single-question pickers; useful in multi-question batches for visual separation."
    },
    "options": {
      "type": "array",
      "minItems": 2,
      "maxItems": 4,
      "description": "[v1 single-question form] The 2-4 distinct, mutually-exclusive choices to present. seek auto-appends an 'Other / type your own answer' row so do NOT include one yourself.",
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
          },
          "preview": {
            "type": "string",
            "description": "Optional plain-text preview (mockup / code snippet / ASCII diagram) shown in a side-panel when the user hovers this option in a wide terminal. Plain monospace only — no markdown. Truncated at ~12 lines × 80 cols. Use when comparing visual / structural alternatives where labels are insufficient."
          }
        },
        "required": ["id", "label"]
      }
    },
    "multi_select": {
      "type": "boolean",
      "description": "true = user can toggle multiple rows with Space then Enter to confirm. false (default) = single selection: highlighted row + Enter accepts immediately. Use multi_select only when the choices are non-exclusive (e.g. 'which features?'); leave false for either/or decisions."
    },
    "questions": {
      "type": "array",
      "minItems": 1,
      "maxItems": 4,
      "description": "[v2 batch form] 1-4 independent questions asked in one call; the TUI renders them as a stack. Use ONLY for truly independent decisions (e.g. 'framework + storage + auth'); do NOT batch related follow-ups, and do NOT batch when Q2 depends on Q1's answer. When this field is set, the top-level question/options/multi_select/header fields are ignored.",
      "items": {
        "type": "object",
        "properties": {
          "question": { "type": "string" },
          "header":   { "type": "string" },
          "options": {
            "type": "array",
            "minItems": 2,
            "maxItems": 4,
            "items": {
              "type": "object",
              "properties": {
                "id":          { "type": "string" },
                "label":       { "type": "string" },
                "description": { "type": "string" },
                "preview":     { "type": "string" }
              },
              "required": ["id", "label"]
            }
          },
          "multi_select": { "type": "boolean" }
        },
        "required": ["question", "options"]
      }
    }
  }
}`)

const description = `Ask the user to pick from 2-4 discrete choices via an inline TUI picker. Use this INSTEAD of asking the question as plain text when: (a) the choices are mutually distinct, (b) getting the wrong one would cost a real round-trip, and (c) you have 4 or fewer concrete options.

Two forms:
- Single question (v1): {question, options, multi_select?, header?}
- Batch (v2, 1-4 questions): {questions: [{question, options, multi_select?, header?}, ...]}

Each option may include an optional "preview" string — a plain-text mockup, code snippet, or ASCII diagram (~12 lines × 80 cols) rendered in a side panel when the user hovers the option in a wide terminal. Use preview when comparing visual / structural alternatives where labels alone are insufficient (e.g. design-style mockups).

Examples (single): 'which scope: user or project?', 'overwrite, skip, or rename?', 'commit now or open PR?'. Examples (batch): asking framework + state-management + styling library together at project setup.

Do NOT use for free-form questions ('what should I name this?'), open-ended preferences, or anything where the user genuinely needs to type prose — those work better as plain conversation. Do NOT batch related follow-ups, and do NOT batch a question where you genuinely don't know what to ask second until you see Q1's answer.`

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
	Preview     string `json:"preview"` // v2
}

// args carries both v1 and v2 fields. Execute branches on which
// is populated. Both forms are permitted in the schema so the
// model picks whichever matches its training; we don't reject
// "unknown field" combinations because some validators send both
// (e.g. an LLM that hedged).
type args struct {
	// v1 fields:
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []option `json:"options"`
	MultiSelect bool     `json:"multi_select"`
	// v2 fields:
	Questions []v2Question `json:"questions"`
}

type v2Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []option `json:"options"`
	MultiSelect bool     `json:"multi_select"`
}

// result is the JSON the LLM sees back for the v1 single-question
// form. Exactly one of ChosenIDs / FreeText is non-empty;
// Cancelled=true means neither. Multi-select answers populate
// ChosenIDs with the toggled subset; single-select answers
// populate it with one entry.
type result struct {
	ChosenIDs []string `json:"chosen_ids,omitempty"`
	FreeText  string   `json:"free_text,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
}

// batchResult is the v2 result shape. Length == len(input.questions),
// answers aligned by index.
type batchResult struct {
	Answers []result `json:"answers"`
}

// toOptions converts the wire-format option slice to the internal
// auser.Option slice. Shared by v1 + v2 paths.
func toOptions(in []option) []auser.Option {
	out := make([]auser.Option, 0, len(in))
	for _, o := range in {
		out = append(out, auser.Option{
			ID:          o.ID,
			Label:       o.Label,
			Description: o.Description,
			Preview:     o.Preview,
		})
	}
	return out
}

func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	// We don't use UnmarshalStrict here — both v1 and v2 share the
	// same args struct, so any "unknown field" would be a real
	// schema violation rather than the v1/v2 hedging that DOES need
	// to flow through. Strict parsing happens via the per-form
	// validation below.
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("%s: bad arguments: %w", toolName, err)
	}
	if t.policy == nil {
		return "", errors.New(toolName + ": no policy configured (programming error)")
	}

	// Dispatch on form: v2 batch takes precedence when populated
	// (the model used the explicit array form), v1 otherwise.
	if len(a.Questions) > 0 {
		return t.executeBatch(a.Questions)
	}
	return t.executeSingle(a)
}

// executeSingle handles the v1 single-question form. Internally it
// still wraps the question into a 1-element Batch so the TUI sees
// a uniform shape — but the result JSON keeps the v1 unwrapped
// {chosen_ids, free_text, cancelled} layout for backward compat
// with skills / prompts trained on v1.
func (t Tool) executeSingle(a args) (string, error) {
	q := auser.Question{
		Question:    a.Question,
		Header:      a.Header,
		MultiSelect: a.MultiSelect,
		Options:     toOptions(a.Options),
	}
	if err := auser.Validate(q); err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}

	// AskBatch with a 1-element batch — TUI never has to special-
	// case "is this v1 or v2?". The TUI stack renderer naturally
	// degenerates to the single-question UX when len==1.
	answers, err := t.policy.AskBatch(auser.Batch{Questions: []auser.Question{q}})
	if err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}
	ans := answers[0]

	out, err := json.Marshal(result{
		ChosenIDs: ans.ChosenIDs,
		FreeText:  ans.FreeText,
		Cancelled: ans.Cancelled,
	})
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", toolName, err)
	}
	return string(out), nil
}

// executeBatch handles the v2 multi-question form. Result is
// wrapped in {answers: [...]} so the model can iterate.
func (t Tool) executeBatch(qs []v2Question) (string, error) {
	batch := auser.Batch{Questions: make([]auser.Question, 0, len(qs))}
	for _, q := range qs {
		batch.Questions = append(batch.Questions, auser.Question{
			Question:    q.Question,
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
			Options:     toOptions(q.Options),
		})
	}
	if err := auser.ValidateBatch(batch); err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}

	answers, err := t.policy.AskBatch(batch)
	if err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}

	res := batchResult{Answers: make([]result, 0, len(answers))}
	for _, a := range answers {
		res.Answers = append(res.Answers, result{
			ChosenIDs: a.ChosenIDs,
			FreeText:  a.FreeText,
			Cancelled: a.Cancelled,
		})
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("%s: marshal batch result: %w", toolName, err)
	}
	return string(out), nil
}
