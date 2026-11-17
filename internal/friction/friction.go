package friction

// Friction accounting (v8 §4 pillar S): fold per-tool failure/denial/
// repeat statistics out of a session's message sequence. Pure function
// over []deepseek.Message — the transcript IS the state (AGENTS.md
// "Reconstruct state from the transcript"), so this package adds no
// state file and no runtime field; the RPC surface exposes the same
// fold read-only as stats/friction.
//
// The three wire-format contracts it reads are each referenced from
// their producer, never re-typed here — one implementation per format,
// and deleting this package can never break the runtime:
//
//   - agent.ToolErrorPrefix — pkg/agent's buildToolResultMsg stamps every
//     failed tool result as that prefix + the error's text.
//   - permission.ErrDenied — a denial reaches the transcript wrapped in
//     the tool-error prefix ("tool error: permission denied: …"), so
//     Denied is scored when ErrDenied.Error() prefixes the remainder.
//     Denied ⊆ Errors by construction: a denied call IS a failed call;
//     the two counters answer different questions, not disjoint sets.
//   - tools.CanonicalArgs — the exact equality key RepeatGuard uses
//     (tool name + NUL + canonical arguments), so "repeat" here means
//     precisely what the guard would have nudged about at write time.

import (
	"encoding/json"
	"strings"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ToolStat is one tool's row: how many times it was called, how many of
// those results were errors, how many of the errors were permission
// denials, and how many calls re-issued an identical name+arguments
// combination already seen in this session.
type ToolStat struct {
	Calls   int `json:"calls"`
	Errors  int `json:"errors"`
	Denied  int `json:"denied"`
	Repeats int `json:"repeats"`
}

// Summary is the folded picture of one session's message sequence.
type Summary struct {
	// Turns counts assistant messages — one model response each, the
	// same unit the session header's Turns field tracks.
	Turns int `json:"turns"`
	// ToolCalls is the total tool_calls entries across all assistant
	// messages, including orphans that never received a result.
	ToolCalls int                 `json:"tool_calls"`
	Tools     map[string]ToolStat `json:"tools"` // keyed by tool name; never nil
}

// Fold reduces a session's message sequence to a Summary. It never
// mutates msgs and derives everything in one pass:
//
//   - Assistant messages add a turn; each of their tool_calls adds a
//     call to the named tool's row and bumps the session-wide
//     name+CanonicalArgs counter — occurrence #2 and beyond of the same
//     key score a Repeat for that tool.
//   - Tool messages attribute to the assistant tool_call bearing the
//     same ToolCallID (the ID map is built while walking, so a tool
//     message after its call — the only order the runtime emits — is
//     always attributable). Content carrying agent.ToolErrorPrefix
//     scores an Error; if the remainder additionally carries
//     permission.ErrDenied's text, it scores a Denied too.
//   - Orphan tool_calls (issued but never answered — an interrupted
//     run) still count as Calls and can score Repeats; they simply have
//     no result to score Errors/Denied from. A tool message whose
//     ToolCallID matches no earlier call in this sequence is skipped:
//     with no issuing call there is no tool name to attribute to.
//
// Malformed arguments JSON is not an error: CanonicalArgs falls back to
// the raw bytes, which is a sound equality key even where it is not a
// canonical one — the same tolerance RepeatGuard applies live.
func Fold(msgs []deepseek.Message) Summary {
	s := Summary{Tools: map[string]ToolStat{}}
	names := map[string]string{} // tool_call ID → tool name
	seen := map[string]int{}     // name+"\x00"+canonical args → occurrences
	for _, m := range msgs {
		switch m.Role {
		case deepseek.RoleAssistant:
			s.Turns++
			for _, tc := range m.ToolCalls {
				s.ToolCalls++
				key := tc.Function.Name + "\x00" + tools.CanonicalArgs(json.RawMessage(tc.Function.Arguments))
				seen[key]++
				row := s.Tools[tc.Function.Name]
				row.Calls++
				if seen[key] > 1 {
					row.Repeats++
				}
				s.Tools[tc.Function.Name] = row
				if tc.ID != "" {
					names[tc.ID] = tc.Function.Name
				}
			}
		case deepseek.RoleTool:
			name, ok := names[m.ToolCallID]
			if !ok {
				continue
			}
			row := s.Tools[name]
			if strings.HasPrefix(m.Content, agent.ToolErrorPrefix) {
				row.Errors++
				if strings.HasPrefix(strings.TrimPrefix(m.Content, agent.ToolErrorPrefix), permission.ErrDenied.Error()) {
					row.Denied++
				}
			}
			s.Tools[name] = row
		}
	}
	return s
}
