// Package skilltool exposes the `Skill` tool: the model calls it with a
// skill name and the tool returns that skill's Markdown body as the
// tool result. This is the on-demand half of the Skill mechanism — the
// system-prompt manifest tells the model WHICH skills exist; this tool
// hands over the instructions WHEN the model decides to use one.
//
// See PRD §4.6.3 for the rationale on not stuffing every skill body
// into the system prompt up front; PRD v2 §4.3 covers the call-stats
// recording that NewWithStats wires up.
package skilltool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillstats"
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

// Env describes the call-time context that gets attached to each
// recorded stats row. Returned by EnvFn rather than captured at
// tool construction so values that can change mid-session (most
// notably SessionID after /branch or /load) stay accurate.
//
// All fields are best-effort: empty strings are dropped from the
// JSONL output by skillstats.
type Env struct {
	SessionID string
	ProjectID string
	Model     string
	Provider  string
}

// EnvFn returns the current Env at call time. Must be safe for
// concurrent use — the agent may invoke skills from multiple
// goroutines, though in practice today it doesn't.
type EnvFn func() Env

// statsAppender is the minimal write surface skilltool needs from
// skillstats. Declared as an interface (not the concrete *Writer)
// so tests can inject a recorder without standing up a real file.
type statsAppender interface {
	Append(skillstats.Entry) error
}

// Tool implements tools.Tool against an in-memory skill.Set. The set is
// loaded once at startup and shared by reference (read-only).
//
// stats + env are optional — when both are non-nil, every successful
// Execute appends one row to the configured .stats.jsonl. Failed
// Execute calls do NOT record (PRD v2 §4.3: "only count actual body
// retrievals"). A stats failure is silenced — the user-facing tool
// call must not break because telemetry hit ENOSPC.
type Tool struct {
	set   *skill.Set
	stats statsAppender
	env   EnvFn
}

// New constructs the tool around a loaded set with no call-stats
// recording. Kept for backward compatibility with the v0 wiring;
// new code paths should prefer NewWithStats so usage is observable.
func New(set *skill.Set) Tool { return Tool{set: set} }

// NewWithStats wires the tool to a skillstats writer. stats and env
// may both be nil to opt out of recording (equivalent to New).
func NewWithStats(set *skill.Set, stats statsAppender, env EnvFn) Tool {
	return Tool{set: set, stats: stats, env: env}
}

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

	// Record the successful fetch before returning. Order matters:
	// if a stats write itself failed, we'd rather lose the row than
	// the tool result, so the error is dropped on purpose.
	t.recordCall(sk.Name)

	// Prefix the body with the skill's own header so the model can
	// distinguish "skill instructions" from "user message" in the
	// resulting tool_result. The body itself often starts with a
	// Markdown H1, but not always.
	return fmt.Sprintf("# Skill: %s\n_Source: %s_\n\n%s",
		sk.Name, sk.Source, sk.Body), nil
}

// recordCall best-effort appends one stats row. No-op when either
// stats or env is unset.
func (t Tool) recordCall(name string) {
	if t.stats == nil || t.env == nil {
		return
	}
	e := t.env()
	_ = t.stats.Append(skillstats.Entry{
		TS:        time.Now().UTC().Format(time.RFC3339),
		Name:      name,
		SessionID: e.SessionID,
		ProjectID: e.ProjectID,
		Model:     e.Model,
		Provider:  e.Provider,
	})
}
