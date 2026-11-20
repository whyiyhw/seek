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
// Carrying attachments to a provider is the sibling below
// (msgsToLLMWithAttachments) — an opt-in, not the default, so the side
// channels can't start uploading bytes by accident.
func msgsToLLM(msgs []deepseek.Message) []llm.Message {
	return msgsToLLMWithAttachments(msgs, false, false)
}

// msgsToLLMWithAttachments is msgsToLLM with the capability decisions
// made by the caller: keepImages / keepFiles are the results of asking
// whoever knows whether the active model accepts each attachment class
// (internal/modelcap), evaluated at send time exactly as the DeepSeek
// path's gates are. When keep is true, every URL-form ImagePart rides
// along as an llm.Image and every file part materialised by
// deepseek.ResolveFileData rides as an llm.File (the caller resolves
// assets BEFORE conversion, at the same send station the DeepSeek path
// uses). Files whose media type the second-tier transports can't
// encode (Word/PPT — PDF is the only shared format) stay behind with
// their own note.
func msgsToLLMWithAttachments(msgs []deepseek.Message, keepImages, keepFiles bool) []llm.Message {
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		lm := llm.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolName:   m.Name,
		}
		if len(m.Images) > 0 {
			if keepImages {
				for _, p := range m.Images {
					if p.URL != "" {
						lm.Images = append(lm.Images, llm.Image{URL: p.URL})
					}
				}
			}
			if len(lm.Images) == 0 {
				lm.Content += fmt.Sprintf("\n\n[image: %d 张图片未透传 — 当前模型不支持图片输入]", len(m.Images))
			}
		}
		if len(m.Files) > 0 {
			dropped := 0
			if keepFiles {
				for _, p := range m.Files {
					f := llm.File{Filename: p.Asset, URL: p.Data}
					if f.Sendable() {
						lm.Files = append(lm.Files, f)
					} else {
						dropped++
					}
				}
			}
			// Two distinct causes, two distinct notes: the model can't take
			// documents at all (gate said no), or it can but this format is
			// not on the second-tier wire (Word/PPT — PDF is the only one
			// every adapter encodes). Collapsing them into one sentence
			// leaves the user guessing which fix applies.
			switch {
			case !keepFiles:
				lm.Content += fmt.Sprintf("\n\n[file: %d 个文档未透传 — 当前模型不支持文件输入]", len(m.Files))
			case dropped > 0:
				lm.Content += fmt.Sprintf("\n\n[file: %d 个文档未透传 — 二线车道仅支持 PDF 附件（Word/PPT 需 DeepSeek 车道）]", dropped)
			}
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
