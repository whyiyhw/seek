package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Background-completion → main-session transcript integration
// (feature-orchestration PRD §4.1 D2, card c_bickfj acceptance):
// a background subagent's terminal notice must reach the PARENT
// agent's message history via the Notifier PrePrompt hook — the
// same history persistSession snapshots into the session JSONL, so
// this is the "完成事件写入主会话 transcript（事件溯源，resume 可重建）"
// link proven end-to-end against a fake DeepSeek backend.
func TestPrompt_BackgroundNoticePrependedToHistory(t *testing.T) {
	// Backend: capture each request's messages, answer with one
	// final assistant text so the turn completes in a single round.
	var captured [][]deepseek.Message
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []deepseek.Message `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("backend: decode request: %v", err)
		}
		captured = append(captured, body.Messages)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, strings.Join([]string{
			`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"acknowledged"}}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n"))
	}))
	defer srv.Close()

	// Manager with SessionCtx + an instantly-completing runner, so
	// the notice is pending before the parent's next Prompt runs.
	t.Setenv("SEEK_HOME", t.TempDir())
	projCwd := t.TempDir()
	policy, err := permission.New(projCwd, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := subagent.NewManager(subagent.ManagerOpts{
		ProjectAbsPath:    projCwd,
		ParentSidFn:       func() string { return "20260601-100000-parent" },
		ParentTracker:     cache.New(),
		ParentPolicy:      policy,
		ProjectSectionFn:  func() string { return "" },
		SkillManifestFn:   func() string { return "" },
		ParentToolNamesFn: func() []string { return []string{"read"} },
		MaxConcurrent:     3,
		Runner: func(ctx context.Context, j subagent.RunnerJob) (subagent.RunnerResult, error) {
			return subagent.RunnerResult{
				Summary: "research concluded: three viable designs",
				Tokens:  subagent.Tokens{Prompt: 100, Completion: 20},
				Turns:   4,
			}, nil
		},
		SessionCtx: context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}

	res := mgr.SpawnBackground(subagent.SpawnArgs{
		Description: "design survey",
		Prompt:      "survey designs",
		Type:        subagent.TypeExplore,
	})
	if !strings.HasPrefix(res.Summary, "[agent: background started sub-sid=") {
		t.Fatalf("background handle summary:\n%s", res.Summary)
	}
	// Wait for the terminal notice to land in the inbox (runner is
	// instant but the goroutine still needs a scheduling beat).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(mgr.PendingNotices()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	if len(mgr.PendingNotices()) != 1 {
		t.Fatal("notice never reached the inbox")
	}

	// Parent agent with the Notifier on its PrePrompt chain —
	// exactly how cmd/seek wires the parent (children get a
	// CloneWithout snapshot, not this registry).
	reg := hooks.NewRegistry()
	reg.Register(subagent.NewNotifier(mgr))

	ag, err := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey("test"), deepseek.WithBaseURL(srv.URL)),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
		Tools:        tools.New(),
		Hooks:        reg,
	})
	if err != nil {
		t.Fatal(err)
	}

	for ev := range ag.Prompt(context.Background(), "what did the survey find?") {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("Prompt error: %v", e.Err)
		}
	}

	// The API request carried the notice as a user <context> message
	// BEFORE the user's own text.
	if len(captured) == 0 {
		t.Fatal("backend captured no requests")
	}
	msgs := captured[0]
	var notice, userText bool
	for _, m := range msgs {
		if m.Role == deepseek.RoleUser && strings.HasPrefix(m.Content, "<context>\n[agent: background done sub-sid="+res.SubSid+"]") {
			notice = true
		}
		if strings.Contains(m.Content, "what did the survey find?") {
			userText = true
		}
	}
	if !notice {
		t.Errorf("request messages carry no background-done <context> notice; got roles=%d", len(msgs))
	}
	if !userText {
		t.Error("request messages lost the user text")
	}

	// And the notice is COMMITTED to the parent's history — the
	// array persistSession snapshots into the main session JSONL
	// (完成事件落盘 / resume 可重建).
	hist := ag.Messages()
	found := false
	for _, m := range hist {
		if m.Role == deepseek.RoleUser && strings.HasPrefix(m.Content, "<context>\n[agent: background done sub-sid="+res.SubSid+"]") {
			found = true
			if !strings.Contains(m.Content, "research concluded: three viable designs") {
				t.Errorf("notice in history missing summary body:\n%s", m.Content)
			}
		}
	}
	if !found {
		t.Error("background-done notice never committed to agent history (transcript)")
	}

	// Drain discipline: the inbox is empty after the Prompt ran the
	// hook — a second turn must not re-deliver.
	if pending := mgr.PendingNotices(); len(pending) != 0 {
		t.Errorf("inbox after Prompt = %d notices, want 0 (double delivery risk)", len(pending))
	}
}
