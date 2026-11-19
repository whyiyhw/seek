package verifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// This file pins the v8.md §8 ❌ clause for R2: the verifier's own
// configuration (the judge system prompt) must NOT live anywhere the
// core session can write. "Core 写得到的地方" = the repo working tree
// (the write/edit tools' reach) and the SEEK_HOME state tree
// (~/.seek/...). The prompt is a compile-time constant; this test
// proves behaviourally that planting decoy prompt files in every
// plausible core-writable location changes nothing on the wire.

// judgeWire records what the (httptest) judge backend received.
type judgeWire struct {
	system string
	user   string
	body   string
}

// cannedJudgeReply is a well-formed judgement the fake backend returns.
const cannedJudgeReply = `{"assertions":[{"index":1,"verdict":"pass","evidence":["D"],"note":"diff shows it"}],"overall_note":"ok"}`

func recordingJudgeServer(t *testing.T, wire *judgeWire) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		wire.body = string(b)
		var req struct {
			Messages []deepseek.Message `json:"messages"`
		}
		if err := json.Unmarshal(b, &req); err == nil {
			for _, m := range req.Messages {
				if m.Role == deepseek.RoleSystem {
					wire.system = m.Content
				}
				if m.Role == deepseek.RoleUser {
					wire.user = m.Content
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "test", "object": "chat.completion", "created": 1, "model": "fixture-model",
			"choices": []map[string]any{
				{"index": 0, "message": map[string]any{"role": "assistant", "content": cannedJudgeReply}, "finish_reason": "stop"},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestPrompt_NotOverridableByCoreWritablePaths: decoy prompt files
// under SEEK_HOME and the working tree (every spot a core session
// could plausibly plant an override) must not alter the system prompt
// the judge session sends — the wire bytes must equal the compile-time
// constant, byte for byte.
func TestPrompt_NotOverridableByCoreWritablePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	repo := t.TempDir()

	const pwned = "PWNED-PROMPT-SHOULD-NEVER-REACH-THE-WIRE"
	decoys := []string{
		filepath.Join(home, "verifier", "prompt.md"),
		filepath.Join(home, "verifier", "system_prompt.txt"),
		filepath.Join(home, "verifier.json"),
		filepath.Join(home, "config.json"),
		filepath.Join(repo, ".seek", "verifier", "prompt.md"),
		filepath.Join(repo, ".seek", "verifier-prompt.md"),
		filepath.Join(repo, "verifier-prompt.md"),
		filepath.Join(repo, "AGENTS.md"),
	}
	for _, p := range decoys {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		// config.json / verifier.json get a plausible JSON shape; the
		// rest get raw text. Either way the payload is unmistakable.
		content := pwned
		if strings.HasSuffix(p, ".json") {
			content = `{"verifier": {"prompt": "` + pwned + `"}, "system_prompt": "` + pwned + `"}`
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A decoy env override too — the classic side channel.
	t.Setenv("SEEK_VERIFIER_PROMPT", pwned)

	var wire judgeWire
	srv := recordingJudgeServer(t, &wire)
	client := deepseek.New(deepseek.WithAPIKey("sk-test"), deepseek.WithBaseURL(srv.URL))
	ver := New(client, "fixture-model")

	// Run from inside the decoy-laden repo dir so any relative-path
	// config read would trip over the decoys.
	oldWd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	vd := ver.Verify(context.Background(), Request{
		TaskTitle:  "t",
		Assertions: []string{"something"},
		WorkDir:    repo,
	})

	if wire.system != SystemPrompt {
		t.Fatalf("system prompt on the wire is not the compile-time constant (len wire=%d const=%d):\n%.200q", len(wire.system), len(SystemPrompt), wire.system)
	}
	if strings.Contains(wire.body, pwned) || strings.Contains(wire.user, pwned) {
		t.Fatal("decoy prompt content leaked into the judge request")
	}
	if vd == nil {
		t.Fatal("expected a verdict")
	}
	// And the canned reply parsed through: the fake backend's pass row
	// cites D, which the empty-diff bundle grounds… the diff block
	// exists ("no deliverable commit"), so D is a known id — pass.
	if len(vd.Assertions) != 1 || vd.Assertions[0].Verdict != Pass {
		t.Fatalf("canned judgement should parse to one pass row, got %+v (judgeErr %q)", vd.Assertions, vd.JudgeError)
	}
}

// TestPrompt_NoRuntimeOverrideSurface: the constructors expose no
// prompt parameter at all — there is no argument through which a
// caller (or a core session driving one) could inject a prompt. This
// is a compile-time property; the test documents it by asserting the
// only prompt in play is the exported constant.
func TestPrompt_NoRuntimeOverrideSurface(t *testing.T) {
	if strings.TrimSpace(SystemPrompt) == "" {
		t.Fatal("SystemPrompt must be a non-empty compile-time constant")
	}
	if !strings.Contains(SystemPrompt, "independent verifier") {
		t.Fatal("SystemPrompt content drifted — update this pin deliberately")
	}
}
