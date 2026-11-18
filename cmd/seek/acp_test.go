package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/fileroute"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
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
	froute := fileroute.Router{} // no client → document refs degrade to lane notes
	png := base64.StdEncoding.EncodeToString([]byte("fake png bytes"))

	img := func(data, mime string) acp.ContentBlock {
		return acp.ContentBlock{Type: "image", Data: data, MimeType: mime}
	}
	txt := func(s string) acp.ContentBlock { return acp.ContentBlock{Type: "text", Text: s} }
	build := func(model string, blocks ...acp.ContentBlock) (string, []deepseek.ImagePart) {
		got, parts, _ := buildACPPromptText(context.Background(), model, acp.PromptParams{Prompt: blocks}, vroute, froute)
		return got, parts
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

// TestBuildACPPromptText_FileRefs: a document reference typed into the
// ACP prompt text routes through the file router (feature-deepseek-
// app-api F3) — here with no client, so the lane note lands in-band
// and no parts ride.
func TestBuildACPPromptText_FileRefs(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(doc, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	froute := fileroute.Router{} // no client → lane note; assetsDir never reached

	got, images, files := buildACPPromptText(context.Background(), deepseek.ModelV41Flash,
		acp.PromptParams{Prompt: []acp.ContentBlock{{Type: "text", Text: "read @" + doc}}},
		visionRouter{assetsDir: t.TempDir()}, froute)
	if !strings.HasPrefix(got, "read @"+doc) {
		t.Fatalf("original text must be preserved: %q", got)
	}
	if !strings.Contains(got, "[file: report.pdf — 文件输入需 DeepSeek 车道") {
		t.Fatalf("lane note missing: %q", got)
	}
	if len(images) != 0 || len(files) != 0 {
		t.Fatalf("no client → no parts: %+v %+v", images, files)
	}
}

// --- Model selection over ACP (feature-paseo-provider.md §3.4, "C") ---

// stubLaneProvider satisfies llm.Provider for lane-swap assertions; its
// methods must never be called in these tests (no turns are run).
type stubLaneProvider struct{ name string }

func (s stubLaneProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	return nil, errors.New("stubLaneProvider: no turns expected in this test")
}
func (s stubLaneProvider) Name() string { return s.name }

func acpTestMenu() []acp.ConfigOption {
	return []acp.ConfigOption{{
		ID:           "model",
		Name:         "Model",
		Category:     "model",
		Type:         "select",
		CurrentValue: "deepseek:deepseek-flash",
		Options: []acp.ConfigSelectOption{
			{Value: "deepseek:deepseek-flash", Name: "deepseek-flash (V4.1-Flash)"},
			{Value: "glm:glm-5.3", Name: "glm-5.3 (GLM)"},
		},
	}}
}

// newSwitchTestBackend builds an acpBackend wired for lane-switch
// assertions: a real agent on the DeepSeek lane (dummy key, no network
// at construction), the standard test menu, and a laneFor stub that
// hands out the two lanes the menu advertises.
func newSwitchTestBackend(t *testing.T) *acpBackend {
	t.Helper()
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &acpBackend{
		ag:        ag,
		menu:      acpTestMenu(),
		sessionID: "sess-1",
		laneFor: func(provider string) (llm.Provider, *deepseek.Client, string, error) {
			switch provider {
			case "deepseek":
				return nil, deepseek.New(deepseek.WithAPIKey("t")), deepseek.ModelV41Flash, nil
			case "glm":
				return stubLaneProvider{"GLM"}, nil, "glm-5.3", nil
			}
			return nil, nil, "", errors.New("unknown provider " + provider)
		},
	}
}

func TestACPBackend_NewSessionServesMenu(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	pol, err := permission.New(old, permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	b := newSwitchTestBackend(t)
	b.policy = pol

	res, err := b.NewSession(acp.NewSessionParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ConfigOptions) != 1 || res.ConfigOptions[0].ID != "model" {
		t.Fatalf("session/new must carry the model menu (C): %+v", res.ConfigOptions)
	}
	if res.ConfigOptions[0].CurrentValue != "deepseek:deepseek-flash" {
		t.Fatalf("currentValue = %q, want the startup lane", res.ConfigOptions[0].CurrentValue)
	}
	if b.sessionID != res.SessionID {
		t.Fatalf("backend must remember the anchored session: %q vs %q", b.sessionID, res.SessionID)
	}
}

func TestACPBackend_SetConfigOption_SwitchesLane(t *testing.T) {
	b := newSwitchTestBackend(t)

	// deepseek → glm: the agent's model follows the pick and the menu's
	// currentValue moves with it.
	res, err := b.SetConfigOption(acp.SetConfigOptionParams{
		SessionID: "sess-1", ConfigID: "model", Value: "glm:glm-5.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := b.ag.Model(); got != "glm-5.3" {
		t.Fatalf("agent model = %q, want glm-5.3 (the lane must actually swap)", got)
	}
	if len(res.ConfigOptions) != 1 || res.ConfigOptions[0].CurrentValue != "glm:glm-5.3" {
		t.Fatalf("response must echo the full menu with the new current: %+v", res.ConfigOptions)
	}

	// And back: switching is repeatable, both directions.
	if _, err := b.SetConfigOption(acp.SetConfigOptionParams{
		SessionID: "sess-1", ConfigID: "model", Value: "deepseek:deepseek-flash",
	}); err != nil {
		t.Fatal(err)
	}
	if got := b.ag.Model(); got != deepseek.ModelV41Flash {
		t.Fatalf("agent model = %q, want back on %q", got, deepseek.ModelV41Flash)
	}
}

func TestACPBackend_SetConfigOption_Rejections(t *testing.T) {
	cases := []struct {
		name string
		b    func(t *testing.T) *acpBackend
		p    acp.SetConfigOptionParams
	}{
		{"no session yet", func(t *testing.T) *acpBackend {
			b := newSwitchTestBackend(t)
			b.sessionID = ""
			return b
		}, acp.SetConfigOptionParams{SessionID: "sess-1", ConfigID: "model", Value: "glm:glm-5.3"}},
		{"unknown session", func(t *testing.T) *acpBackend {
			return newSwitchTestBackend(t)
		}, acp.SetConfigOptionParams{SessionID: "other", ConfigID: "model", Value: "glm:glm-5.3"}},
		{"unknown config id", func(t *testing.T) *acpBackend {
			return newSwitchTestBackend(t)
		}, acp.SetConfigOptionParams{SessionID: "sess-1", ConfigID: "thought", Value: "low"}},
		{"value not in menu (stale client echo)", func(t *testing.T) *acpBackend {
			return newSwitchTestBackend(t)
		}, acp.SetConfigOptionParams{SessionID: "sess-1", ConfigID: "model", Value: "glm:glm-4"}},
		{"no menu served (freeform startup model)", func(t *testing.T) *acpBackend {
			b := newSwitchTestBackend(t)
			b.menu = nil
			return b
		}, acp.SetConfigOptionParams{SessionID: "sess-1", ConfigID: "model", Value: "glm:glm-5.3"}},
		{"laneFor failure wraps the provider name", func(t *testing.T) *acpBackend {
			b := newSwitchTestBackend(t)
			b.laneFor = func(string) (llm.Provider, *deepseek.Client, string, error) {
				return nil, nil, "", errors.New("no key")
			}
			return b
		}, acp.SetConfigOptionParams{SessionID: "sess-1", ConfigID: "model", Value: "glm:glm-5.3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.b(t).SetConfigOption(c.p); err == nil {
				t.Fatal("expected a rejection, got nil")
			}
		})
	}
}

// TestACPModelMenu pins the menu builder against a SEEK_HOME-isolated
// config: providers appear only when a key resolves, currentValue
// follows the startup model, and a freeform startup model yields NO
// menu (a dropdown whose current isn't in its own list would lie).
func TestACPModelMenu(t *testing.T) {
	// Neutralise every provider env var (KeyFor checks env BEFORE the
	// config file — a developer shell exporting DEEPSEEK_API_KEY would
	// otherwise widen the menu nondeterministically).
	for _, env := range []string{
		"DEEPSEEK_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GLM_API_KEY", "OPENCODE_API_KEY",
	} {
		t.Setenv(env, "")
	}
	t.Setenv("SEEK_HOME", t.TempDir())
	cfg := config.Config{}
	config.SetKey(&cfg, "deepseek", "k")
	config.SetKey(&cfg, "glm", "k")
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	menu := acpModelMenu(deepseek.ModelV41Flash)
	if len(menu) != 1 {
		t.Fatalf("menu = %+v, want exactly the model select", menu)
	}
	opt := menu[0]
	if opt.ID != "model" || opt.Type != "select" || opt.Category != "model" {
		t.Fatalf("option header = %+v", opt)
	}
	if len(opt.Options) != 2 { // deepseek + glm keys only
		t.Fatalf("options = %+v, want exactly the two keyed lanes", opt.Options)
	}
	if opt.CurrentValue != "deepseek:deepseek-flash" {
		t.Fatalf("currentValue = %q, want the startup lane", opt.CurrentValue)
	}

	// Startup on glm: current follows.
	menu = acpModelMenu("glm-5.3")
	if len(menu) != 1 || menu[0].CurrentValue != "glm:glm-5.3" {
		t.Fatalf("glm startup menu = %+v", menu)
	}

	// Freeform startup model: no menu at all (client hides dropdown).
	if menu := acpModelMenu("deepseek-v4-pro"); menu != nil {
		t.Fatalf("freeform startup model must yield nil menu, got %+v", menu)
	}
}
