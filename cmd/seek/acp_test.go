package main

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/ocr"
	"github.com/whyiyhw/seek/pkg/agent"
)

// acpUpdate maps agent events → ACP session/update payloads. This verifies
// the mapping (柱 P) without a live Zed client.
func TestACPUpdate_Mapping(t *testing.T) {
	upd := func(ev agent.Event) map[string]any {
		u, ok := acpUpdate("s1", ev)
		if !ok {
			t.Fatalf("expected a surfaced update for %T", ev)
		}
		if u.SessionID != "s1" {
			t.Fatalf("sessionId = %q", u.SessionID)
		}
		return u.Update.(map[string]any)
	}

	// message chunk
	m := upd(agent.MessageDelta{Delta: "hello"})
	if m["sessionUpdate"] != "agent_message_chunk" {
		t.Fatalf("message delta → %v", m)
	}
	if c, _ := m["content"].(map[string]any); c["text"] != "hello" {
		t.Fatalf("chunk text lost: %v", m)
	}

	// tool call start
	ts := upd(agent.ToolExecStart{CallID: "c1", Name: "bash"})
	if ts["sessionUpdate"] != "tool_call" || ts["toolCallId"] != "c1" || ts["status"] != "in_progress" {
		t.Fatalf("tool start → %v", ts)
	}

	// tool call end (success + failure)
	teOK := upd(agent.ToolExecEnd{CallID: "c1"})
	if teOK["sessionUpdate"] != "tool_call_update" || teOK["status"] != "completed" {
		t.Fatalf("tool end ok → %v", teOK)
	}
	teErr := upd(agent.ToolExecEnd{CallID: "c2", Err: errors.New("boom")})
	if teErr["status"] != "failed" {
		t.Fatalf("tool end err → %v", teErr)
	}
}

func TestACPUpdate_Dropped(t *testing.T) {
	// Reasoning chunks and turn bookkeeping must NOT surface to the client.
	for _, ev := range []agent.Event{
		agent.MessageDelta{Delta: "thinking…", Reasoning: true},
		agent.TurnStart{},
		agent.TurnEnd{},
		agent.AgentStart{},
	} {
		if _, ok := acpUpdate("s", ev); ok {
			t.Errorf("%T should not surface as a session/update", ev)
		}
	}
}

// M-P.5: image input.

func TestACPBackend_AdvertisesImage(t *testing.T) {
	b := &acpBackend{}
	res := b.Initialize(acp.InitializeParams{ProtocolVersion: 1})
	if res.ProtocolVersion != 1 {
		t.Fatalf("protocolVersion echo: %d", res.ProtocolVersion)
	}
	if !res.AgentCapabilities.PromptCapabilities.Image {
		t.Fatal("initialize must advertise promptCapabilities.image=true so Zed offers image attach")
	}
}

func TestImageExtForMime(t *testing.T) {
	for mime, want := range map[string]string{
		"image/png":    ".png",
		"image/jpeg":   ".jpg",
		"IMAGE/PNG":    ".png", // case-insensitive
		" image/webp ": ".webp",
		"image/gif":    ".gif",
		"text/plain":   "", // non-image → skip
		"":             "",
	} {
		if got := imageExtForMime(mime); got != want {
			t.Errorf("imageExtForMime(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestBuildACPPromptText(t *testing.T) {
	ctx := context.Background()
	// Fake OCR engine: prints fixed text regardless of the image.
	ocrOpt := ocr.Options{Command: []string{"sh", "-c", "printf '%s' OCRTEXT"}}
	png := base64.StdEncoding.EncodeToString([]byte("fake png bytes"))

	img := func(data, mime string) acp.ContentBlock {
		return acp.ContentBlock{Type: "image", Data: data, MimeType: mime}
	}
	txt := func(s string) acp.ContentBlock { return acp.ContentBlock{Type: "text", Text: s} }
	build := func(blocks ...acp.ContentBlock) string {
		return buildACPPromptText(ctx, acp.PromptParams{Prompt: blocks}, ocrOpt)
	}

	if got := build(txt("hi")); got != "hi" {
		t.Fatalf("text-only = %q", got)
	}
	if got := build(txt("what is this"), img(png, "image/png")); !strings.Contains(got, "what is this") ||
		!strings.Contains(got, "OCRTEXT") || !strings.Contains(got, "[image: pasted-image — OCR]") {
		t.Fatalf("text+image = %q", got)
	}
	if got := build(img(png, "image/png")); !strings.Contains(got, "OCRTEXT") {
		t.Fatalf("image-only should still OCR: %q", got)
	}
	// Corrupt base64 → that block is skipped, text preserved.
	if got := build(txt("keep"), img("!!!not-base64!!!", "image/png")); got != "keep" {
		t.Fatalf("corrupt base64 must be skipped: %q", got)
	}
	// Non-image mimeType → skipped.
	if got := build(txt("keep"), img(png, "application/pdf")); got != "keep" {
		t.Fatalf("non-image mime must be skipped: %q", got)
	}
}
