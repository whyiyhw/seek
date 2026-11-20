package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// acpReplayUpdates is the pure half of session/load. These tests pin the
// role→update mapping without a client, an agent, or a transcript file.
func TestACPReplayUpdates_Mapping(t *testing.T) {
	msgs := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "sys"}, // must not replay
		{Role: deepseek.RoleUser, Content: "look at this\n\n[image: a.png — attached natively]"},
		{Role: deepseek.RoleAssistant, Content: "", ToolCalls: []deepseek.ToolCall{{
			ID: "c1", Type: "function",
			Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"/proj/a.go"}`},
		}}},
		{Role: deepseek.RoleTool, ToolCallID: "c1", Name: "read", Content: "package main"},
		{Role: deepseek.RoleAssistant, Content: "done"},
		{Role: deepseek.RoleUser, Content: ""}, // empty → skipped
	}
	ups := acpReplayUpdates("sess-1", msgs, "/proj")
	// six messages → four updates: system and the empty user message are
	// skipped, and the tool-call assistant message carries no text of its
	// own.
	if len(ups) != 4 {
		t.Fatalf("updates = %d, want 4: %+v", len(ups), ups)
	}
	kind := func(i int) string {
		m, ok := ups[i].Update.(map[string]any)
		if !ok {
			t.Fatalf("update %d is not a map: %#v", i, ups[i].Update)
		}
		return m["sessionUpdate"].(string)
	}
	if want := []string{"user_message_chunk", "tool_call", "tool_call_update", "agent_message_chunk"}; !slices.Equal(
		[]string{kind(0), kind(1), kind(2), kind(3)}, want) {
		t.Fatalf("order = %v, want %v", []string{kind(0), kind(1), kind(2), kind(3)}, want)
	}
	// The tool_call carries the tool's identity + completed status, and
	// the update pairs back by id with the tool result as rawOutput.
	call := ups[1].Update.(map[string]any)
	if call["toolCallId"] != "c1" || call["name"] != "read" || call["status"] != "completed" || call["kind"] != "read" {
		t.Fatalf("tool_call = %+v", call)
	}
	if call["title"] != "read /proj/a.go" {
		t.Fatalf("tool_call title = %v", call["title"])
	}
	if _, ok := call["locations"]; !ok {
		t.Error("tool_call should carry locations for a path-bearing arg")
	}
	upd := ups[2].Update.(map[string]any)
	if upd["toolCallId"] != "c1" || upd["status"] != "completed" || upd["name"] != "read" {
		t.Fatalf("tool_call_update = %+v", upd)
	}
	if upd["rawOutput"] != "package main" {
		t.Fatalf("tool_call_update rawOutput = %v", upd["rawOutput"])
	}
	// The user chunk keeps the marker text verbatim (the transcript is
	// self-describing; replay must not re-attach or strip bytes).
	u := ups[0].Update.(map[string]any)
	content := u["content"].(map[string]any)
	if !strings.Contains(content["text"].(string), "[image: a.png") {
		t.Fatalf("user chunk lost the marker: %v", content["text"])
	}
}

// TestACPReplayUpdates_SystemSkippedAndSessionIDStamped: the system
// prompt is re-composed per process, so a saved copy must never be
// replayed; every update carries the loaded session's id (the client
// routes notifications by it).
func TestACPReplayUpdates_SystemSkippedAndSessionIDStamped(t *testing.T) {
	ups := acpReplayUpdates("loaded-id", []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "old prompt"},
		{Role: deepseek.RoleUser, Content: "hi"},
	}, "/proj")
	if len(ups) != 1 {
		t.Fatalf("updates = %d, want 1 (system skipped)", len(ups))
	}
	if ups[0].SessionID != "loaded-id" {
		t.Fatalf("sessionId = %q", ups[0].SessionID)
	}
	if strings.Contains(ups[0].Update.(map[string]any)["content"].(map[string]any)["text"].(string), "old prompt") {
		t.Fatal("system prompt leaked into the replay")
	}
}

// loadTestBackend builds an acpBackend wired to a temp session store, the
// minimum state session/load touches.
func loadTestBackend(t *testing.T) (*acpBackend, *session.Store) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", filepath.Join(dir, "sessions"))
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	pol, err := permission.New(old, permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &acpBackend{
		ag:     ag,
		policy: pol,
		store:  store,
		sess:   session.New(deepseek.ModelV41Flash, old, "sys", false, false),
		menu:   acpTestMenu(),
		laneFor: func(provider string) (llm.Provider, *deepseek.Client, string, error) {
			return nil, deepseek.New(deepseek.WithAPIKey("t")), deepseek.ModelV41Flash, nil
		},
	}, store
}

// TestACPBackend_LoadSession_RestoresAndReplays is the round trip: a
// saved transcript + a client's session/load ⇒ history on the agent, the
// same id back, and the transcript replayed as notifications.
func TestACPBackend_LoadSession_RestoresAndReplays(t *testing.T) {
	b, store := loadTestBackend(t)
	stored := session.New(deepseek.ModelV41Flash, mustGetwd(t), "sys", false, false)
	stored.Messages = []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "earlier question"},
		{Role: deepseek.RoleAssistant, Content: "earlier answer"},
	}
	if err := store.Save(stored); err != nil {
		t.Fatal(err)
	}

	var replayed []acp.SessionUpdate
	res, err := b.LoadSession(acp.LoadSessionParams{SessionID: stored.ID}, func(u acp.SessionUpdate) {
		replayed = append(replayed, u)
	})
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(replayed) != 2 {
		t.Fatalf("replayed %d updates, want 2: %+v", len(replayed), replayed)
	}
	if replayed[0].SessionID != stored.ID {
		t.Fatalf("replay sessionId = %q, want %q", replayed[0].SessionID, stored.ID)
	}
	// The agent now holds the loaded history (plus its own system prompt).
	hist := b.ag.Messages()
	if len(hist) != 3 || hist[1].Content != "earlier question" || hist[2].Content != "earlier answer" {
		t.Fatalf("agent history = %+v", hist)
	}
	// The live session pointer was mutated in place: same pointer, loaded id.
	if b.sess.ID != stored.ID {
		t.Fatalf("live session id = %q, want %q (in-place swap)", b.sess.ID, stored.ID)
	}
	if b.sessionID != stored.ID {
		t.Fatalf("backend sessionID = %q, want the loaded id", b.sessionID)
	}
	if len(res.ConfigOptions) != 1 {
		t.Fatalf("load must return the config menu: %+v", res.ConfigOptions)
	}
}

// TestACPBackend_LoadSession_MissingTranscript: an unknown id fails loud
// with the id in the message — a client showing "continue" needs to know
// the transcript is gone, not that something vaguely broke.
func TestACPBackend_LoadSession_MissingTranscript(t *testing.T) {
	b, _ := loadTestBackend(t)
	_, err := b.LoadSession(acp.LoadSessionParams{SessionID: "20260101-000000-deadbeefdeadbeef"}, func(acp.SessionUpdate) {})
	if err == nil || !strings.Contains(err.Error(), "20260101-000000-deadbeefdeadbeef") {
		t.Fatalf("err = %v, want it to name the missing id", err)
	}
}

// TestACPBackend_LoadSession_PersistenceOff: under --no-save there is no
// store, and load must say so rather than pretending.
func TestACPBackend_LoadSession_PersistenceOff(t *testing.T) {
	b, _ := loadTestBackend(t)
	b.store = nil
	if _, err := b.LoadSession(acp.LoadSessionParams{SessionID: "x"}, func(acp.SessionUpdate) {}); err == nil {
		t.Fatal("load with persistence off must error")
	}
	// And the capability is not advertised — clients gate on this boolean.
	if b.Initialize(acp.InitializeParams{ProtocolVersion: 1}).AgentCapabilities.LoadSession {
		t.Fatal("loadSession must not be advertised when the store is absent")
	}
}

// TestACPBackend_Initialize_AdvertisesLoadSession: with a store present
// the capability is on, because a durable id now exists.
func TestACPBackend_Initialize_AdvertisesLoadSession(t *testing.T) {
	b, _ := loadTestBackend(t)
	res := b.Initialize(acp.InitializeParams{ProtocolVersion: 1})
	if !res.AgentCapabilities.LoadSession {
		t.Fatalf("loadSession must be advertised: %+v", res.AgentCapabilities)
	}
	if !res.AgentCapabilities.PromptCapabilities.Image {
		t.Fatal("image capability regressed")
	}
}

// TestACPBackend_NewSessionReturnsDurableID: the id a client stores for a
// later session/load must be the transcript id, and the transcript must
// exist after a turn (created by the store, not by load).
func TestACPBackend_NewSessionReturnsDurableID(t *testing.T) {
	b, store := loadTestBackend(t)
	res, err := b.NewSession(acp.NewSessionParams{Cwd: mustGetwd(t)})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID != b.sess.ID {
		t.Fatalf("sessionId = %q, want the transcript id %q", res.SessionID, b.sess.ID)
	}
	b.saveTurn()
	if _, err := store.Load(res.SessionID); err != nil {
		t.Fatalf("saved transcript not loadable: %v", err)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
