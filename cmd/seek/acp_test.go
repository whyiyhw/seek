package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
		u, ok := acpUpdate("s1", "/proj", ev)
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

	// reasoning rides the thought channel (user decision 2026-11-19),
	// never as message content
	m = upd(agent.MessageDelta{Delta: "hmm…", Reasoning: true})
	if m["sessionUpdate"] != "agent_thought_chunk" {
		t.Fatalf("reasoning delta → %v, want agent_thought_chunk", m)
	}
	if c, _ := m["content"].(map[string]any); c["text"] != "hmm…" {
		t.Fatalf("thought text lost: %v", m)
	}
	// the think tool's live stream (CoT and answer alike) is thought
	// material — the process is the point
	m = upd(agent.ToolDelta{CallID: "c9", Name: "think", Delta: "step 1…", Reasoning: true})
	if m["sessionUpdate"] != "agent_thought_chunk" {
		t.Fatalf("think CoT delta → %v, want agent_thought_chunk", m)
	}
	m = upd(agent.ToolDelta{CallID: "c9", Name: "think", Delta: "the answer is…"})
	if m["sessionUpdate"] != "agent_thought_chunk" {
		t.Fatalf("think answer delta → %v, want agent_thought_chunk", m)
	}

	// tool call start: full protocol surface — name, kind, locations,
	// rawInput (parsed to an object — paseo's detail panel renders
	// objects, its own mock sends them) alongside the id/title/status
	// triple, and the title carries the arg summary.
	ts := upd(agent.ToolExecStart{
		CallID: "c1", Name: "bash",
		Args: `{"command":"go test ./..."}`,
	})
	if ts["sessionUpdate"] != "tool_call" || ts["toolCallId"] != "c1" || ts["status"] != "in_progress" {
		t.Fatalf("tool start → %v", ts)
	}
	if ts["name"] != "bash" || ts["kind"] != "execute" {
		t.Fatalf("tool start name/kind → %v", ts)
	}
	if ts["title"] != "bash go test ./..." {
		t.Fatalf("tool start title → %v", ts["title"])
	}
	if ri, ok := ts["rawInput"].(map[string]any); !ok || ri["command"] != "go test ./..." {
		t.Fatalf("tool start rawInput should be the parsed object → %T %v", ts["rawInput"], ts["rawInput"])
	}
	if _, has := ts["locations"]; has {
		t.Fatalf("bash args carry no path — no locations: %v", ts)
	}

	// tool call end (success + failure)
	teOK := upd(agent.ToolExecEnd{CallID: "c1", Name: "bash", Result: "ok", Args: `{"command":"ls"}`})
	if teOK["sessionUpdate"] != "tool_call_update" || teOK["status"] != "completed" {
		t.Fatalf("tool end ok → %v", teOK)
	}
	if teOK["rawOutput"] != "ok" {
		t.Fatalf("tool end rawOutput (plain text stays string) → %v", teOK["rawOutput"])
	}
	if _, has := teOK["content"]; has {
		t.Fatalf("text content blocks must NOT be sent (they poison the whole update on paseo): %v", teOK)
	}
	teErr := upd(agent.ToolExecEnd{CallID: "c2", Name: "read", Err: errors.New("boom")})
	if teErr["status"] != "failed" {
		t.Fatalf("tool end err → %v", teErr)
	}
	if teErr["rawOutput"] != "boom" {
		t.Fatalf("tool end err rawOutput → %v", teErr["rawOutput"])
	}
}

func TestACPUpdate_Dropped(t *testing.T) {
	// Turn bookkeeping must NOT surface to the client. (Reasoning now
	// rides the thought channel — see TestACPUpdate_Mapping.)
	for _, ev := range []agent.Event{
		agent.TurnStart{},
		agent.TurnEnd{},
		agent.AgentStart{},
	} {
		if _, ok := acpUpdate("s", "/proj", ev); ok {
			t.Errorf("%T should not surface as a session/update", ev)
		}
	}
}

// TestACPToolTitle: the collapsed one-liner carries the arg summary for
// the tools that have one and degrades to the bare name otherwise
// (unknown args shapes included).
func TestACPToolTitle(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"read", `{"path":"notes.txt"}`, "read notes.txt"},
		{"edit", `{"path":"a.go","old_string":"x","new_string":"y"}`, "edit a.go"},
		{"bash", `{"command":"go test ./..."}`, "bash go test ./..."},
		{"think", `{"task":"plan the migration"}`, "think plan the migration"},
		{"grep", `{"pattern":"foo","path":"."}`, "grep foo"},
		{"bash", `{bad json`, "bash"},
		{"memory_observe", `{}`, "memory_observe"},
		{"read", `{"offset":5}`, "read"}, // no path key → bare name
		{"bash", `{"command":"` + strings.Repeat("x", 100) + `"}`, "bash " + strings.Repeat("x", 80) + "…"},
	}
	for _, c := range cases {
		if got := acpToolTitle(c.name, c.args); got != c.want {
			t.Errorf("acpToolTitle(%q, %.40s…) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// TestACPRawValue: JSON parses into objects/arrays (paseo's renderable
// form), everything else stays a string — including a string that was
// truncated mid-JSON.
func TestACPRawValue(t *testing.T) {
	if v, ok := acpRawValue(`{"a":1}`).(map[string]any); !ok || v["a"] != float64(1) {
		t.Errorf("object passthrough: %T %v", acpRawValue(`{"a":1}`), acpRawValue(`{"a":1}`))
	}
	if _, ok := acpRawValue(`[1,2]`).([]any); !ok {
		t.Errorf("array passthrough: %T", acpRawValue(`[1,2]`))
	}
	if s, ok := acpRawValue("plain text").(string); !ok || s != "plain text" {
		t.Errorf("text stays string: %T", acpRawValue("plain text"))
	}
	if _, ok := acpRawValue(`{"a":1`).(string); !ok {
		t.Errorf("truncated JSON degrades to string: %T", acpRawValue(`{"a":1`))
	}
}

// TestACPUpdate_ToolKind pins the name→kind table (clients pick icons
// by it; unknown tools must fall to "other", never "").
func TestACPUpdate_ToolKind(t *testing.T) {
	for name, want := range map[string]string{
		"read": "read", "list_dir": "read", "references": "read",
		"grep": "search",
		"edit": "edit", "write": "edit", "fim_complete": "edit",
		"bash": "execute", "git": "execute", "monitor": "execute",
		"think": "think", "plan": "think", "propose": "think",
		"webfetch":       "fetch",
		"memory_observe": "other", "agent": "other", "totally-new-tool": "other",
	} {
		if got := acpToolKind(name); got != want {
			t.Errorf("acpToolKind(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestACPUpdate_Locations: relative paths join against cwd (schema
// wants absolute), read's offset rides as line, tools without a path
// and unparseable args get nothing — fail-soft.
func TestACPUpdate_Locations(t *testing.T) {
	locs := acpToolLocations(`{"path":"internal/acp/server.go","offset":12}`, "/proj")
	if len(locs) != 1 || locs[0]["path"] != "/proj/internal/acp/server.go" || locs[0]["line"] != 12 {
		t.Fatalf("relative+offset → %v", locs)
	}
	locs = acpToolLocations(`{"path":"/abs/x.go"}`, "/proj")
	if len(locs) != 1 || locs[0]["path"] != "/abs/x.go" {
		t.Fatalf("absolute passthrough → %v", locs)
	}
	if locs := acpToolLocations(`{"path":"/abs/x.go","offset":0}`, "/proj"); locs[0]["line"] != nil {
		t.Fatalf("offset 0 = default → no line hint: %v", locs)
	}
	if locs := acpToolLocations(`{"command":"ls"}`, "/proj"); locs != nil {
		t.Fatalf("no path → nil, got %v", locs)
	}
	if locs := acpToolLocations(`{not json`, "/proj"); locs != nil {
		t.Fatalf("bad JSON → nil, got %v", locs)
	}
	if locs := acpToolLocations("", "/proj"); locs != nil {
		t.Fatalf("empty args → nil, got %v", locs)
	}
}

// TestACPUpdate_Content: ONLY edit-success emits a diff block — the
// single content variant that validates on every SDK generation.
// Everything else (other tools, failures, unparseable args) emits
// NOTHING: paseo's runtime zod has no skip-invalid on the content
// array, so a never-satisfiable text block poisons the whole
// tool_call_update (stuck-spinner failure mode, 2026-11-19).
func TestACPUpdate_Content(t *testing.T) {
	diff := acpToolContent("edit", `{"path":"a.go","old_string":"x","new_string":"y"}`, "ok", nil)
	if len(diff) != 1 || diff[0]["type"] != "diff" || diff[0]["path"] != "a.go" ||
		diff[0]["oldText"] != "x" || diff[0]["newText"] != "y" {
		t.Fatalf("edit success → diff block, got %v", diff)
	}
	// edit failure: a diff for an edit that never landed would lie — and
	// an error text block would poison the update. Nothing at all.
	if c := acpToolContent("edit", `{"path":"a.go","old_string":"x","new_string":"y"}`, "", errors.New("denied")); c != nil {
		t.Fatalf("edit failure → no content, got %v", c)
	}
	if c := acpToolContent("edit", `{bad`, "done", nil); c != nil {
		t.Fatalf("edit bad args → no content, got %v", c)
	}
	if c := acpToolContent("read", `{"path":"x"}`, "contents", nil); c != nil {
		t.Fatalf("non-edit → no content (rawOutput carries it), got %v", c)
	}
	if c := acpToolContent("read", `{"path":"x"}`, "", nil); c != nil {
		t.Fatalf("empty result → no content, got %v", c)
	}
}

// TestACPTruncate: the cut never splits a multi-byte rune and the
// marker lands after it (raw payloads ride JSON — broken UTF-8 would
// corrupt the wire).
func TestACPTruncate(t *testing.T) {
	long := strings.Repeat("界", acpRawCap) // 3 bytes per rune
	got := acpTruncate(long, acpRawCap)
	if !utf8.ValidString(got) {
		t.Fatal("truncated string is not valid UTF-8")
	}
	if !strings.HasSuffix(got, "…[truncated]") {
		t.Fatalf("truncation marker missing: %q", got[len(got)-40:])
	}
	if acpTruncate("short", acpRawCap) != "short" {
		t.Fatal("under-cap strings pass through untouched")
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

// TestACPBackend_InitializeNegotiatesVersion: protocolVersion is a real
// negotiation (card c_4pczu3 ①), not an echo. A client asking for a
// version seek does not speak is answered with the newest one it does,
// and the downgrade is declared — on the wire (the response IS the
// declaration) and on stderr (so a human watching `seek acp` can tell).
func TestACPBackend_InitializeNegotiatesVersion(t *testing.T) {
	b := &acpBackend{}
	cases := []struct {
		requested int
		want      int
	}{
		{1, 1},  // exact match
		{2, 1},  // newer client → downgrade to the newest we speak
		{99, 1}, // far-future client → same answer
		{0, 1},  // absent/garbage field → still a usable handshake
	}
	for _, c := range cases {
		res := b.Initialize(acp.InitializeParams{ProtocolVersion: c.requested})
		if res.ProtocolVersion != c.want {
			t.Errorf("initialize(protocolVersion=%d) answered %d, want %d", c.requested, res.ProtocolVersion, c.want)
		}
		// Capabilities are declared on every path — a downgrade must not
		// silently drop image support.
		if !res.AgentCapabilities.PromptCapabilities.Image {
			t.Errorf("initialize(protocolVersion=%d) dropped promptCapabilities.image", c.requested)
		}
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

	// Explicit cwd on a SECOND session/new (P1-2: a new session is
	// minted, not a re-anchor of the live one). The new session is
	// record-only: its anchor (both halves) takes effect at its first
	// turn — anchoring here would yank the cwd out from under the
	// installed session.
	res, err = b.NewSession(acp.NewSessionParams{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(dir); res.SessionID != want {
		t.Fatalf("sessionId = %q, want project name %q", res.SessionID, want)
	}
	if len(b.sessions) != 2 {
		t.Fatalf("sessions = %d entries, want 2", len(b.sessions))
	}
	if got := pol.Cwd(); got != old {
		t.Fatalf("policy cwd = %q, want the FIRST session's %q (second new is record-only)", got, old)
	}
	if got, _ := os.Getwd(); got != old {
		t.Fatalf("process cwd = %q, want the FIRST session's %q (second new is record-only)", got, old)
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

// TestBuildACPPromptText_Ordering pins acceptance ④ (card c_4pczu3):
// a mixed text/image prompt reaches the model with the blocks in wire
// order — the image marker sits BETWEEN the sentences that surround it,
// not appended at the end. Also pins that consecutive text blocks still
// join with a single newline (byte-identical to the pre-Segments shape
// for text-only prompts, which is what the prefix cache cares about).
func TestBuildACPPromptText_Ordering(t *testing.T) {
	vroute := visionRouter{assetsDir: t.TempDir()}
	froute := fileroute.Router{}
	png := base64.StdEncoding.EncodeToString([]byte("fake png bytes"))

	p := acp.PromptParams{Prompt: []acp.ContentBlock{
		{Type: "text", Text: "first sentence"},
		{Type: "image", Data: png, MimeType: "image/png"},
		{Type: "text", Text: "second sentence"},
	}}
	got, parts, _ := buildACPPromptText(context.Background(), deepseek.ModelV41Flash, p, vroute, froute)
	if len(parts) != 1 {
		t.Fatalf("parts = %+v, want the one image attached", parts)
	}
	first := strings.Index(got, "first sentence")
	marker := strings.Index(got, "[image: pasted-image.png — attached natively")
	second := strings.Index(got, "second sentence")
	if first < 0 || marker < 0 || second < 0 {
		t.Fatalf("prompt text lost a piece: %q", got)
	}
	if !(first < marker && marker < second) {
		t.Fatalf("wire order broken — want text/image/text, got %q (idx %d/%d/%d)", got, first, marker, second)
	}
	// The marker is its own paragraph, the text either side of it is not
	// glued together without separation.
	if !strings.Contains(got, "first sentence\n\n[image: ") {
		t.Fatalf("marker must start a new paragraph: %q", got)
	}
	if !strings.Contains(got, "]\n\nsecond sentence") {
		t.Fatalf("text after a marker must start a new paragraph: %q", got)
	}

	// Text-only: two blocks join with exactly one newline (the PromptText
	// shape every existing transcript was written with).
	two := acp.PromptParams{Prompt: []acp.ContentBlock{
		{Type: "text", Text: "a"}, {Type: "text", Text: "b"},
	}}
	if got, _, _ := buildACPPromptText(context.Background(), deepseek.ModelV41Flash, two, vroute, froute); got != "a\nb" {
		t.Fatalf("text-only join = %q, want %q (byte-identical to PromptText)", got, "a\nb")
	}
}

// TestBuildACPPromptText_FileRefs: a document reference typed into the
// ACP prompt text routes through the file router (feature-deepseek-
// app-api F3). Two contracts: an incapable model gets the switch-model
// note in-band with no parts; a file-capable model on a second-tier
// lane (no Files API client) gets the store-and-attach path — the part
// carries the asset and the send path materialises the bytes.
func TestBuildACPPromptText_FileRefs(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir()) // butler c_rzsvk5：fileroute.New→ProjectAssets 会写进真实 ~/.seek
	dir := t.TempDir()
	doc := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(doc, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	froute := fileroute.New(nil, dir, nil) // second-tier: no client

	got, images, files := buildACPPromptText(context.Background(), "glm-4.7f",
		acp.PromptParams{Prompt: []acp.ContentBlock{{Type: "text", Text: "read @" + doc}}},
		visionRouter{assetsDir: t.TempDir()}, froute)
	if !strings.HasPrefix(got, "read @"+doc) {
		t.Fatalf("original text must be preserved: %q", got)
	}
	if !strings.Contains(got, "[file: report.pdf — 当前模型不支持文件输入") {
		t.Fatalf("switch-model note missing: %q", got)
	}
	if len(images) != 0 || len(files) != 0 {
		t.Fatalf("incapable model → no parts: %+v %+v", images, files)
	}

	got, images, files = buildACPPromptText(context.Background(), "gpt-4o",
		acp.PromptParams{Prompt: []acp.ContentBlock{{Type: "text", Text: "read @" + doc}}},
		visionRouter{assetsDir: t.TempDir()}, froute)
	if !strings.Contains(got, "[file: report.pdf — attached · 8 B]") {
		t.Fatalf("attach marker missing: %q", got)
	}
	if len(images) != 0 || len(files) != 1 || files[0].Asset == "" || files[0].FileID != "" {
		t.Fatalf("second-tier attach must be Asset-only: %+v %+v", images, files)
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
// hands out the two lanes the menu advertises. Since P1-2 the state
// model carries a session table, so the harness registers one live
// session by hand (id sess-1, installed) — the state NewSession would
// have built, minus the anchoring side effects these tests don't want.
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
	cwd, _ := os.Getwd()
	st := &acpSession{
		id:        "sess-1",
		cwd:       cwd,
		menuValue: "deepseek:deepseek-flash",
	}
	return &acpBackend{
		ag:        ag,
		menu:      acpTestMenu(),
		sessionID: "sess-1",
		sessions:  map[string]*acpSession{"sess-1": st},
		installed: st,
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
	// This test drives the fresh-session flow, not the pre-seeded one:
	// clear the harness's table so session/new is the FIRST session.
	b.sessions, b.installed, b.sessionID = nil, nil, ""

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
			b.sessions = nil
			b.installed = nil
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

	menu := acpModelMenu(deepseek.ModelV41Flash, nil)
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
	menu = acpModelMenu("glm-5.3", nil)
	if len(menu) != 1 || menu[0].CurrentValue != "glm:glm-5.3" {
		t.Fatalf("glm startup menu = %+v", menu)
	}

	// Freeform startup model: no menu at all (client hides dropdown).
	if menu := acpModelMenu("deepseek-v4-pro", nil); menu != nil {
		t.Fatalf("freeform startup model must yield nil menu, got %+v", menu)
	}
}
