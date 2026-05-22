package agent

import (
	"encoding/json"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// msgsToLLM converts the agent's internal deepseek.Message history into
// the provider-agnostic llm.Message format for second-tier providers.
func msgsToLLM(msgs []deepseek.Message) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		lm := llm.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolName:   m.Name,
		}
		for _, tc := range m.ToolCalls {
			lm.ToolCalls = append(lm.ToolCalls, llm.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		out = append(out, lm)
	}
	return out
}

// toolsToLLM converts the registry's tool definitions into llm.ToolDef
// slices for second-tier providers. The schemas are passed through
// verbatim — they're JSON Schema objects that every provider understands.
func toolsToLLM(reg *tools.Registry) []llm.ToolDef {
	if reg == nil {
		return nil
	}
	names := reg.Names()
	out := make([]llm.ToolDef, 0, len(names))
	for _, name := range names {
		t := reg.Lookup(name)
		if t == nil {
			continue
		}
		out = append(out, llm.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      json.RawMessage(t.Schema()),
		})
	}
	return out
}
