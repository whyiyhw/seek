package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
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

// D-1 (feature-paseo-provider.md §2.2): NewSession must anchor BOTH cwd
// conventions to p.Cwd and return the project name as sessionId — the
// deep dispatch-path behaviour is covered in internal/acp; this pins
// the production backend's wiring (policy handoff + name).
func TestACPBackend_NewSessionAnchorsCwd(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Register the chdir-back AFTER t.TempDir so it runs BEFORE the
	// TempDir RemoveAll (cleanups are LIFO): the test ends with the
	// process cwd inside dir, which Windows refuses to delete while
	// it is held as a process cwd (unlinkat EBUSY).
	t.Cleanup(func() { _ = os.Chdir(old) })

	pol, err := permission.New(old, permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	b := &acpBackend{policy: pol}

	// Default first (empty cwd → CURRENT directory, before anything
	// moves): Zed-equivalent, a behavioural no-op.
	res, err := b.NewSession(acp.NewSessionParams{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(old); res.SessionID != want {
		t.Fatalf("default sessionId = %q, want %q", res.SessionID, want)
	}
	if got := pol.Cwd(); got != old {
		t.Fatalf("default policy cwd = %q, want %q", got, old)
	}

	// Explicit cwd: both anchors move, sessionId = project name.
	res, err = b.NewSession(acp.NewSessionParams{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(dir); res.SessionID != want {
		t.Fatalf("sessionId = %q, want project name %q", res.SessionID, want)
	}
	if got := pol.Cwd(); got != dir {
		t.Fatalf("policy cwd = %q, want %q", got, dir)
	}
	if got, _ := os.Getwd(); got != dir {
		t.Fatalf("process cwd = %q, want %q", got, dir)
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
	vroute := visionRouter{assetsDir: t.TempDir()}
	png := base64.StdEncoding.EncodeToString([]byte("fake png bytes"))

	img := func(data, mime string) acp.ContentBlock {
		return acp.ContentBlock{Type: "image", Data: data, MimeType: mime}
	}
	txt := func(s string) acp.ContentBlock { return acp.ContentBlock{Type: "text", Text: s} }
	build := func(model string, blocks ...acp.ContentBlock) (string, []deepseek.ImagePart) {
		return buildACPPromptText(model, acp.PromptParams{Prompt: blocks}, vroute)
	}

	if got, parts := build(deepseek.ModelV41Flash, txt("hi")); got != "hi" || parts != nil {
		t.Fatalf("text-only = %q %v", got, parts)
	}

	// Vision model: image attached natively, part carries the stored
	// asset name, marker names the display name.
	got, parts := build(deepseek.ModelV41Flash, txt("what is this"), img(png, "image/png"))
	if !strings.Contains(got, "what is this") ||
		!strings.Contains(got, "[image: pasted-image.png — attached natively") {
		t.Fatalf("text+image = %q", got)
	}
	if len(parts) != 1 || !strings.HasSuffix(parts[0].Asset, ".png") {
		t.Fatalf("parts = %+v", parts)
	}
	if _, err := os.Stat(filepath.Join(vroute.assetsDir, parts[0].Asset)); err != nil {
		t.Fatalf("asset not stored: %v", err)
	}

	// Non-vision model (retired id = plain unknown): switch-model
	// note, no parts, nothing stored.
	got, parts = build("deepseek-v4-pro", txt("look"), img(png, "image/png"))
	if !strings.Contains(got, "[image: pasted-image.png — 当前模型不支持图片输入") || len(parts) != 0 {
		t.Fatalf("non-vision = %q %v", got, parts)
	}

	// Corrupt base64 → that block is skipped, text preserved.
	if got, parts := build(deepseek.ModelV41Flash, txt("keep"), img("!!!not-base64!!!", "image/png")); got != "keep" || parts != nil {
		t.Fatalf("corrupt base64 must be skipped: %q %v", got, parts)
	}
	// Non-image mimeType → skipped.
	if got, parts := build(deepseek.ModelV41Flash, txt("keep"), img(png, "application/pdf")); got != "keep" || parts != nil {
		t.Fatalf("non-image mime must be skipped: %q %v", got, parts)
	}
}
