package agent

import (
	"encoding/json"
	"fmt"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// msgsToLLM converts the agent's internal deepseek.Message history into
// the provider-agnostic llm.Message format for second-tier providers,
// dropping attachments with an in-band note so the model knows content
// existed — same never-error posture as the vision router. This is the
// TRANSCRIPT-shaped conversion (summarise / compact), where paying for
// image bytes is waste: the markers inside Content remain.
//
// Carrying images to a provider is the sibling below
// (msgsToLLMWithImages) — an opt-in, not the default, so the side
// channels can't start uploading bytes by accident.
func msgsToLLM(msgs []deepseek.Message) []llm.Message {
	return msgsToLLMWithImages(msgs, false)
}

// msgsToLLMWithImages is msgsToLLM with the capability decision made by
// the caller: keep is the result of asking whoever knows whether the
// active model accepts images (internal/modelcap), evaluated at send
// time exactly as the DeepSeek path's gate is. When keep is true, every
// URL-form ImagePart rides along as an llm.Image; Asset-only parts are
// skipped because the caller resolves assets BEFORE conversion
// (deepseek.ResolveImages at the same send station).
//
// Document parts have no second-tier transport (the Files API is
// DeepSeek-only), so they always degrade to the sibling note.
func msgsToLLMWithImages(msgs []deepseek.Message, keep bool) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		lm := llm.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolName:   m.Name,
		}
		if len(m.Images) > 0 {
			if keep {
				for _, p := range m.Images {
					if p.URL != "" {
						lm.Images = append(lm.Images, llm.Image{URL: p.URL})
					}
				}
			}
			if len(lm.Images) == 0 {
				lm.Content += fmt.Sprintf("\n\n[image: %d 张图片未透传 — 当前 provider 不支持图片输入]", len(m.Images))
			}
		}
		if len(m.Files) > 0 {
			lm.Content += fmt.Sprintf("\n\n[file: %d 个文档未透传 — 当前 provider 不支持文件输入]", len(m.Files))
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
