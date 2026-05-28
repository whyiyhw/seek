package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestWriteSubagentTranscript_LandsAtDocumentedPath pins the
// G1 fix: the production Runner persists the subagent's
// transcript JSONL to SessionDir/transcript.jsonl per PRD
// feature-subagent.md §3.3. Without this, `seek -resume <sub-
// sid>`, the future /agents detail view, and feature-inspect-
// rpc all have nothing to read.
//
// The test exercises the extracted writeSubagentTranscript
// helper rather than the full Runner closure (which requires
// DeepSeek client + per-platform subprocess machinery to
// stand up). Helper extraction was deliberate FOR THIS TEST.
func TestWriteSubagentTranscript_LandsAtDocumentedPath(t *testing.T) {
	dir := t.TempDir()
	policy, err := permission.New(dir, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	job := subagent.RunnerJob{
		SubSid:       "20260601-100000-abcdef",
		SystemPrompt: "you are a subagent",
		UserPrompt:   "summarise PRs",
		SessionDir:   dir,
		Policy:       policy,
	}
	msgs := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "you are a subagent"},
		{Role: deepseek.RoleUser, Content: "summarise PRs"},
		{Role: deepseek.RoleAssistant, Content: "Found 3 stale PRs..."},
	}
	usage := deepseek.Usage{
		PromptTokens:          8000,
		CompletionTokens:      100,
		TotalTokens:           8100,
		PromptCacheHitTokens:  7500,
		PromptCacheMissTokens: 500,
	}

	writeSubagentTranscript(job, msgs, "deepseek-v4-flash", usage, 2)

	transcript := filepath.Join(dir, "transcript.jsonl")
	data, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatalf("transcript not written at %s: %v", transcript, err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// 1 header + 3 messages.
	if len(lines) != 4 {
		t.Fatalf("expected 4 JSONL lines (header + 3 messages), got %d: %q", len(lines), data)
	}

	// Header: sub-sid is the session ID, usage round-trips.
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatalf("header JSON parse: %v", err)
	}
	if header["id"] != "20260601-100000-abcdef" {
		t.Errorf("header.id = %v, want sub-sid", header["id"])
	}
	if header["model"] != "deepseek-v4-flash" {
		t.Errorf("header.model = %v", header["model"])
	}
	if header["system_prompt"] != "you are a subagent" {
		t.Errorf("header.system_prompt round-trip lost content")
	}
	// header.cwd must be the policy's cwd (Policy.Cwd() abs'd).
	if header["cwd"] == nil || header["cwd"] == "" {
		t.Error("header.cwd missing")
	}
	// Turns + Usage fields land at the top level (not nested).
	if turns, ok := header["turns"].(float64); !ok || int(turns) != 2 {
		t.Errorf("header.turns = %v, want 2", header["turns"])
	}

	// Messages: parse line 3 (assistant) and verify content
	// round-trip.
	var lastMsg map[string]any
	if err := json.Unmarshal([]byte(lines[3]), &lastMsg); err != nil {
		t.Fatalf("message JSON parse: %v", err)
	}
	if lastMsg["role"] != string(deepseek.RoleAssistant) {
		t.Errorf("last message role = %v", lastMsg["role"])
	}
	if !strings.Contains(lastMsg["content"].(string), "Found 3 stale PRs") {
		t.Errorf("last message content lost: %v", lastMsg["content"])
	}
}

// TestWriteSubagentTranscript_FailureNonFatal covers the
// best-effort contract: a write error (SessionDir not
// writable) must NOT propagate; the caller's RunnerResult is
// still constructed normally. Helper has no return value, so
// "didn't panic + didn't propagate" IS the test.
func TestWriteSubagentTranscript_FailureNonFatal(t *testing.T) {
	policy, err := permission.New(t.TempDir(), permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	job := subagent.RunnerJob{
		SubSid:     "id",
		SessionDir: "/dev/null/cannot/mkdir/here", // SaveTo MkdirAll will fail
		Policy:     policy,
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("writeSubagentTranscript panicked: %v", r)
		}
	}()
	writeSubagentTranscript(job, nil, "m", deepseek.Usage{}, 0)
}
