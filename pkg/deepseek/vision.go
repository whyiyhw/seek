// vision.go — multimodal (vision) message plumbing: the ImagePart type,
// the model allow-list, wire-form marshalling, and the send-time
// asset→data-URL resolution. feature-vision PRD
// (docs/prd/feature-vision.md) D1–D3.
//
// Two serialisation forms coexist BY DESIGN:
//
//   - session/transcript form (the default struct marshal): `content`
//     stays a plain string, images ride the sibling `images` field as
//     Asset references. Text-only messages are byte-identical to the
//     pre-vision encoding — load-bearing for old-session compatibility
//     and DeepSeek's prefix cache.
//   - wire form (ChatRequest.MarshalJSON): image-bearing user messages
//     get array-form `content` ([{type:text},{type:image_url},…]) and
//     the `images` / `predicted_next` fields never cross the API.

package deepseek

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ImagePart is one image attached to a user message. Exactly one of
// Asset / URL is populated:
//
//   - Asset: the durable form recorded in the session JSONL — a
//     content-addressed file name under the project assets dir
//     (internal/assets). The API never sees it.
//   - URL: the sendable form — a data: URL produced by ResolveImages
//     just before the request, or an external https URL for callers
//     that already have one.
type ImagePart struct {
	Asset string `json:"asset,omitempty"`
	URL   string `json:"url,omitempty"`
}

// IsVisionModel reports whether the model id natively accepts image
// content parts. Explicit allow-list, NOT substring matching: -Exp ids
// rotate (GA may rename) and a silently-drifting pattern would send
// images to a model that answers 400. An unknown id is simply not a
// vision model — the submit-time router degrades to an in-band note.
func IsVisionModel(model string) bool {
	switch model {
	case ModelV4FlashVisionExp:
		return true
	}
	return false
}

// ImageLoader resolves a persisted asset name to a sendable data: URL.
// Implemented by callers (the agent layer owns the assets directory) —
// pkg/deepseek stays free of filesystem concerns.
type ImageLoader func(asset string) (dataURL string, err error)

// assetMissingNote is appended to a message's Content when its asset
// can't be loaded. Same wire-format family as the OCR-era injection
// blocks ("[image: " prefix) so a single replay styling rule covers
// both generations. Never fatal — the conversation continues.
func assetMissingNote(asset, why string) string {
	return fmt.Sprintf("\n\n[image: %s — 本地资产缺失: %s]", asset, why)
}

// ResolveImages returns a request-ready copy of msgs in which every
// Asset-only image part is materialised into a data: URL via load.
// Load failures NEVER abort the send (never-error philosophy): the
// part is dropped and an in-band note is appended to that message's
// Content so the model — and the transcript — see what happened.
// Messages without images pass through untouched; the input slice is
// never mutated (mirrors StripReasoningContent's copy semantics, and
// runs at the same send-time station).
func ResolveImages(msgs []Message, load ImageLoader) []Message {
	has := false
	for _, m := range msgs {
		if len(m.Images) > 0 {
			has = true
			break
		}
	}
	if !has {
		return msgs
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		if len(m.Images) == 0 {
			out[i] = m
			continue
		}
		parts := make([]ImagePart, 0, len(m.Images))
		for _, p := range m.Images {
			switch {
			case p.URL != "" || p.Asset == "":
				parts = append(parts, p) // already sendable / defensive
			case load == nil:
				m.Content += assetMissingNote(p.Asset, "no image loader wired")
			default:
				u, err := load(p.Asset)
				if err != nil {
					m.Content += assetMissingNote(p.Asset, err.Error())
					continue
				}
				parts = append(parts, ImagePart{Asset: p.Asset, URL: u})
			}
		}
		if len(parts) == 0 {
			m.Images = nil
		} else {
			m.Images = parts
		}
		out[i] = m
	}
	return out
}

// WithoutImages returns a copy of msgs with every Images field
// cleared. The in-band marker text inside Content is untouched, so
// the conversation stays self-describing. Used by side-channel
// one-shot calls (Summarise / compact) that shouldn't pay for — or
// depend on resolving — image bytes.
func WithoutImages(msgs []Message) []Message {
	has := false
	for _, m := range msgs {
		if len(m.Images) > 0 {
			has = true
			break
		}
	}
	if !has {
		return msgs
	}
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		m.Images = nil
		out[i] = m
	}
	return out
}

// wireImageURL / wireContentPart are the array-form `content` blocks
// the vision API accepts on user messages.
type wireImageURL struct {
	URL string `json:"url"`
}

type wireContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *wireImageURL `json:"image_url,omitempty"`
}

// wireMessage is the on-wire form of Message: identical fields except
// `content` (array of parts when the message carries images) and the
// two fields that NEVER cross the API boundary — PredictedNext
// (session-only UX hint) and Images (seek's persistence shape; its
// parts are inlined into content instead).
type wireMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	Name             string          `json:"name,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
}

// toWire converts one Message. An Asset-only part at this point means
// the caller skipped ResolveImages — fail the marshal loudly rather
// than silently dropping an image the user attached.
func toWire(m Message) (wireMessage, error) {
	w := wireMessage{
		Role:             m.Role,
		Name:             m.Name,
		ToolCallID:       m.ToolCallID,
		ToolCalls:        m.ToolCalls,
		ReasoningContent: m.ReasoningContent,
	}
	if len(m.Images) == 0 {
		if m.Content != "" {
			b, err := json.Marshal(m.Content)
			if err != nil {
				return w, err
			}
			w.Content = b
		}
		return w, nil
	}
	parts := make([]wireContentPart, 0, len(m.Images)+1)
	if m.Content != "" {
		parts = append(parts, wireContentPart{Type: "text", Text: m.Content})
	}
	for _, p := range m.Images {
		if p.URL == "" {
			return w, fmt.Errorf("deepseek: image part %q has no URL — run ResolveImages before sending", p.Asset)
		}
		parts = append(parts, wireContentPart{
			Type:     "image_url",
			ImageURL: &wireImageURL{URL: p.URL},
		})
	}
	b, err := json.Marshal(parts)
	if err != nil {
		return w, err
	}
	w.Content = b
	return w, nil
}

// MarshalJSON renders the API request form: Messages are converted to
// wireMessage (array-form content for image-bearing user messages;
// session-only fields stripped). Text-only MESSAGES render
// byte-identically to the pre-vision encoding — pinned by
// TestMessageWire_TextOnly_Bytes — because that is what becomes prompt
// tokens (and what DeepSeek's prefix cache keys on). Request-object key
// order may differ; the API and cache are insensitive to it.
func (r ChatRequest) MarshalJSON() ([]byte, error) {
	wireMsgs := make([]wireMessage, 0, len(r.Messages))
	for _, m := range r.Messages {
		w, err := toWire(m)
		if err != nil {
			return nil, err
		}
		wireMsgs = append(wireMsgs, w)
	}
	type alias ChatRequest
	out := struct {
		alias
		Messages []wireMessage `json:"messages"`
	}{alias(r), wireMsgs}
	return json.Marshal(out)
}

// UnmarshalJSON accepts BOTH content forms so a session line remains
// loadable regardless of what wrote it: a plain string (seek's native
// form — images ride the sibling `images` field) or an array of parts
// (the wire form — text parts concatenate into Content in order,
// image_url parts become URL-bearing ImageParts). The string fast path
// is the common one; the array path is forward-compat for external
// tools and hand-edited transcripts.
func (m *Message) UnmarshalJSON(data []byte) error {
	type alias Message
	var raw struct {
		alias
		Content json.RawMessage `json:"content"` // depth-0 shadows alias.Content
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = Message(raw.alias) // alias.Content stays zero; filled below
	if len(raw.Content) == 0 {
		return nil // content omitted (omitempty round-trip)
	}
	var s string
	if err := json.Unmarshal(raw.Content, &s); err == nil {
		m.Content = s
		return nil
	}
	var parts []wireContentPart
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return fmt.Errorf("deepseek: message content: neither string nor parts array: %w", err)
	}
	var sb strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			sb.WriteString(p.Text)
		case "image_url":
			if p.ImageURL != nil && p.ImageURL.URL != "" {
				m.Images = append(m.Images, ImagePart{URL: p.ImageURL.URL})
			}
		}
	}
	m.Content = sb.String()
	return nil
}
