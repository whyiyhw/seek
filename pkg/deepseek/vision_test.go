package deepseek

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestIsVisionModel(t *testing.T) {
	cases := map[string]bool{
		ModelV4FlashVisionExp: true,
		ModelV4Flash:          false,
		ModelV4Pro:            false,
		"deepseek-v4-flash-vision":   false, // hypothetical GA rename — NOT matched until allow-listed
		"vision":                     false, // substring must never match
		"":                           false,
	}
	for model, want := range cases {
		if got := IsVisionModel(model); got != want {
			t.Errorf("IsVisionModel(%q) = %v, want %v", model, got, want)
		}
	}
}

// TestMessageWire_TextOnly_Bytes pins the wire bytes of text-only
// messages: they must stay byte-identical to the pre-vision encoding
// (load-bearing for DeepSeek's prefix cache — the rendered prompt
// tokens derive from these bytes — and for wire-compat with the old
// client). Any change here needs a deliberate migration story.
func TestMessageWire_TextOnly_Bytes(t *testing.T) {
	cases := []struct {
		msg  Message
		want string
	}{
		{Message{Role: RoleUser, Content: "hi"}, `{"role":"user","content":"hi"}`},
		{Message{Role: RoleUser, Content: ""}, `{"role":"user"}`},
		{
			Message{Role: RoleTool, Content: "ok", ToolCallID: "t1"},
			`{"role":"tool","content":"ok","tool_call_id":"t1"}`,
		},
		{
			Message{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{Function: ToolCallFunc{Name: "grep"}}}},
			`{"role":"assistant","tool_calls":[{"function":{"name":"grep","arguments":""}}]}`,
		},
		{
			// ReasoningContent replay for tool-call assistant turns survives.
			Message{Role: RoleAssistant, ReasoningContent: "think", ToolCalls: []ToolCall{{Function: ToolCallFunc{Name: "grep"}}}},
			`{"role":"assistant","tool_calls":[{"function":{"name":"grep","arguments":""}}],"reasoning_content":"think"}`,
		},
	}
	for _, c := range cases {
		w, err := toWire(c.msg)
		if err != nil {
			t.Fatalf("toWire(%+v): %v", c.msg, err)
		}
		got, err := json.Marshal(w)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(got) != c.want {
			t.Errorf("wire bytes drifted:\n got %s\nwant %s", got, c.want)
		}
	}
}

// TestMessage_SessionForm_Bytes pins the session/transcript form: the
// default struct marshal keeps `content` a plain string (images ride
// the sibling field), and text-only messages omit `images` entirely so
// old JSONL lines stay byte-identical.
func TestMessage_SessionForm_Bytes(t *testing.T) {
	got, err := json.Marshal(Message{Role: RoleUser, Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"role":"user","content":"hi"}` {
		t.Errorf("text-only session form drifted: %s", got)
	}

	got, err = json.Marshal(Message{
		Role:    RoleUser,
		Content: "look",
		Images:  []ImagePart{{Asset: "ab12cd34ef56.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Asset-only: `images` present with the asset ref, content stays a string.
	want := `{"role":"user","content":"look","images":[{"asset":"ab12cd34ef56.png"}]}`
	if string(got) != want {
		t.Errorf("image session form:\n got %s\nwant %s", got, want)
	}
}

func TestChatRequest_WireForm_Images(t *testing.T) {
	req := &ChatRequest{
		Model: ModelV4FlashVisionExp,
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{
				Role:    RoleUser,
				Content: "what is this",
				Images: []ImagePart{
					{Asset: "a.png", URL: "data:image/png;base64,QUJD"},
					{URL: "https://example.com/x.jpg"},
				},
			},
		},
	}
	buf, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(raw.Messages))
	}
	// System message: plain string content, untouched.
	if !strings.Contains(string(raw.Messages[0]), `"content":"sys"`) {
		t.Errorf("system message content changed: %s", raw.Messages[0])
	}
	// User message: array-form content with text + 2 image_url parts.
	var parts []map[string]any
	if err := json.Unmarshal(raw.Messages[1], &struct {
		Content *[]map[string]any `json:"content"`
	}{&parts}); err != nil {
		t.Fatalf("user content not an array: %v (%s)", err, raw.Messages[1])
	}
	if len(parts) != 3 {
		t.Fatalf("want 3 content parts, got %d: %v", len(parts), parts)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "what is this" {
		t.Errorf("part 0 = %v", parts[0])
	}
	iu0, _ := parts[1]["image_url"].(map[string]any)
	if parts[1]["type"] != "image_url" || iu0["url"] != "data:image/png;base64,QUJD" {
		t.Errorf("part 1 = %v", parts[1])
	}
	iu1, _ := parts[2]["image_url"].(map[string]any)
	if parts[2]["type"] != "image_url" || iu1["url"] != "https://example.com/x.jpg" {
		t.Errorf("part 2 = %v", parts[2])
	}
	// Persistence-only fields must never leak onto the wire.
	for _, banned := range []string{`"images"`, `"predicted_next"`} {
		if strings.Contains(string(buf), banned) {
			t.Errorf("wire form leaked %s: %s", banned, buf)
		}
	}
}

// TestChatRequest_UnresolvedImageErrors: an Asset-only part at wire
// time means the caller skipped ResolveImages — the request must fail
// loudly instead of silently dropping a user-attached image.
func TestChatRequest_UnresolvedImageErrors(t *testing.T) {
	req := &ChatRequest{
		Model:    ModelV4FlashVisionExp,
		Messages: []Message{{Role: RoleUser, Content: "x", Images: []ImagePart{{Asset: "a.png"}}}},
	}
	_, err := json.Marshal(req)
	if err == nil || !strings.Contains(err.Error(), "ResolveImages") {
		t.Fatalf("want ResolveImages error, got %v", err)
	}
}

// TestMessage_Unmarshal_BothForms: session load accepts the native
// string form (fast path) and the array form (text parts concatenate,
// image_url parts become ImageParts).
func TestMessage_Unmarshal_BothForms(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"user","content":"hi","images":[{"asset":"a.png"}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "hi" || len(m.Images) != 1 || m.Images[0].Asset != "a.png" {
		t.Errorf("string form: %+v", m)
	}

	m = Message{}
	if err := json.Unmarshal([]byte(`{"role":"user"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "" || m.Images != nil {
		t.Errorf("empty form: %+v", m)
	}

	m = Message{}
	in := `{"role":"user","content":[{"type":"text","text":"a "},{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ"}},{"type":"text","text":"b"}]}`
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	if m.Content != "a b" {
		t.Errorf("array form content = %q", m.Content)
	}
	if len(m.Images) != 1 || m.Images[0].URL != "data:image/png;base64,QQ" {
		t.Errorf("array form images = %+v", m.Images)
	}

	// Neither form → clear error, not silent garbage.
	m = Message{}
	if err := json.Unmarshal([]byte(`{"role":"user","content":42}`), &m); err == nil {
		t.Error("numeric content must error")
	}
}

func TestResolveImages(t *testing.T) {
	loadOK := func(asset string) (string, error) {
		return "data:image/png;base64," + asset, nil
	}
	base := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "look", Images: []ImagePart{{Asset: "a.png"}}},
		{Role: RoleAssistant, Content: "ok"},
	}

	// Passthrough: no images anywhere → same slice.
	if out := ResolveImages(base[:1], loadOK); len(out) != 1 {
		t.Errorf("no-image passthrough: %+v", out)
	}

	out := ResolveImages(base, loadOK)
	if got := out[1].Images[0].URL; got != "data:image/png;base64,a.png" {
		t.Errorf("resolved URL = %q", got)
	}
	if out[1].Content != "look" {
		t.Errorf("content mutated on success: %q", out[1].Content)
	}
	// Input slice untouched.
	if base[1].Images[0].URL != "" {
		t.Error("input mutated")
	}

	// Missing asset → note appended, part dropped, never an error.
	loadFail := func(string) (string, error) { return "", errors.New("gone") }
	out = ResolveImages(base, loadFail)
	if len(out[1].Images) != 0 {
		t.Errorf("failed part kept: %+v", out[1].Images)
	}
	if !strings.HasPrefix(strings.TrimSpace(out[1].Content), "look") ||
		!strings.Contains(out[1].Content, "[image: a.png") ||
		!strings.Contains(out[1].Content, "gone") {
		t.Errorf("missing-asset note wrong: %q", out[1].Content)
	}

	// nil loader → in-band note, not a panic.
	out = ResolveImages(base, nil)
	if len(out[1].Images) != 0 || !strings.Contains(out[1].Content, "no image loader wired") {
		t.Errorf("nil loader: %+v", out[1])
	}
}

// TestAssetMissingNote_Prefix pins the wire-format contract: every
// degradation note belongs to the "[image: " family so one replay
// styling rule covers all generations. Variants may only add content
// AFTER the family prefix (AGENTS.md wire-format rule).
func TestAssetMissingNote_Prefix(t *testing.T) {
	note := assetMissingNote("a.png", "boom")
	if !strings.HasPrefix(note, "\n\n[image: a.png ") {
		t.Errorf("note prefix drifted: %q", note)
	}
}

// TestSessionRoundTrip_Images: session-form marshal → unmarshal
// preserves the asset reference and the string content (what
// internal/session does per JSONL line).
func TestSessionRoundTrip_Images(t *testing.T) {
	in := Message{Role: RoleUser, Content: "look", Images: []ImagePart{{Asset: "a.png"}}}
	buf, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatal(err)
	}
	if out.Content != "look" || len(out.Images) != 1 || out.Images[0].Asset != "a.png" || out.Images[0].URL != "" {
		t.Errorf("round trip drifted: %+v", out)
	}
}
