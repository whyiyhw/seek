package memory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestBuildDistillUserMessage_IncludesCap(t *testing.T) {
	out := BuildDistillUserMessage(nil, 5)
	if !strings.Contains(out, "up to 5") {
		t.Errorf("user message should mention candidate cap; got %q", truncate(out, 200))
	}
}

func TestBuildDistillUserMessage_DefaultsCap(t *testing.T) {
	out := BuildDistillUserMessage(nil, 0)
	if !strings.Contains(out, "up to 3") {
		t.Errorf("zero cap should fall back to default 3; got %q", truncate(out, 200))
	}
}

func TestBuildDistillUserMessage_SkipsSystemMessages(t *testing.T) {
	hist := []deepseek.Message{
		{Role: deepseek.RoleSystem, Content: "internal system prompt"},
		{Role: deepseek.RoleUser, Content: "I want to refactor X"},
		{Role: deepseek.RoleAssistant, Content: "OK here's how"},
	}
	out := BuildDistillUserMessage(hist, 3)
	if strings.Contains(out, "internal system prompt") {
		t.Errorf("system messages leaked into distill prompt: %q", out)
	}
	if !strings.Contains(out, "refactor X") {
		t.Errorf("user content missing from transcript: %q", out)
	}
}

func TestBuildDistillUserMessage_RendersToolCalls(t *testing.T) {
	hist := []deepseek.Message{
		{
			Role:    deepseek.RoleAssistant,
			Content: "looking at the file",
			ToolCalls: []deepseek.ToolCall{
				{Function: deepseek.ToolCallFunc{Name: "read", Arguments: `{"path":"x.go"}`}},
			},
		},
		{Role: deepseek.RoleTool, ToolCallID: "call_1", Content: "file body here"},
	}
	out := BuildDistillUserMessage(hist, 3)
	if !strings.Contains(out, "[tool call: read") {
		t.Errorf("tool call not rendered: %q", out)
	}
	if !strings.Contains(out, "[tool result") || !strings.Contains(out, "file body here") {
		t.Errorf("tool result not rendered: %q", out)
	}
}

func TestParseCandidates_HappyArray(t *testing.T) {
	raw := `[{"name":"foo","tagline":"a","content":"b","tags":["x"]}]`
	got, err := ParseCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "foo" || got[0].Tagline != "a" {
		t.Errorf("unexpected parse result: %+v", got)
	}
	if len(got[0].Tags) != 1 || got[0].Tags[0] != "x" {
		t.Errorf("tags missing: %+v", got[0].Tags)
	}
}

func TestParseCandidates_EmptyArrayMeansNoCandidates(t *testing.T) {
	got, err := ParseCandidates(`[]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(got))
	}
}

func TestParseCandidates_SingleObjectIsWrapped(t *testing.T) {
	raw := `{"name":"solo","tagline":"alone","content":"x"}`
	got, err := ParseCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "solo" {
		t.Errorf("single-object parse failed: %+v", got)
	}
}

func TestParseCandidates_MarkdownFencedJSON(t *testing.T) {
	raw := "```json\n[{\"name\":\"fenced\",\"tagline\":\"in fence\",\"content\":\"c\"}]\n```"
	got, err := ParseCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "fenced" {
		t.Errorf("fenced parse failed: %+v", got)
	}
}

func TestParseCandidates_LanguageAgnosticFence(t *testing.T) {
	raw := "```\n[{\"name\":\"plain\",\"tagline\":\"a\",\"content\":\"b\"}]\n```"
	got, err := ParseCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "plain" {
		t.Errorf("plain-fence parse failed: %+v", got)
	}
}

func TestParseCandidates_LeadingProse(t *testing.T) {
	raw := "Here are the candidates I extracted:\n\n[{\"name\":\"prosey\",\"tagline\":\"a\",\"content\":\"b\"}]"
	got, err := ParseCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Name != "prosey" {
		t.Errorf("leading-prose parse failed: %+v", got)
	}
}

func TestParseCandidates_EmptyResponseErrors(t *testing.T) {
	_, err := ParseCandidates("")
	if err == nil {
		t.Errorf("empty response should error, not return nil")
	}
	_, err = ParseCandidates("   \n\t  ")
	if err == nil {
		t.Errorf("whitespace-only response should error")
	}
}

func TestParseCandidates_GarbageErrors(t *testing.T) {
	_, err := ParseCandidates("the reasoner forgot to output JSON")
	if err == nil {
		t.Errorf("non-JSON response should error")
	}
}

func TestParseCandidates_SingleObjectMissingNameReturnsNil(t *testing.T) {
	// An object without a name field is the model failing to follow the
	// schema; ParseCandidates drops it rather than passing it through
	// for Add() to reject downstream.
	got, err := ParseCandidates(`{"tagline":"no name","content":"x"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != nil {
		t.Errorf("nameless single object should produce nil, got %+v", got)
	}
}

// fakeChatClient wraps a canned ChatResponse so Distiller can be tested
// without a real DeepSeek backend. Captures the request so tests can
// assert on what was sent (model id, system prompt, etc.).
type fakeChatClient struct {
	lastReq *deepseek.ChatRequest
	resp    *deepseek.ChatResponse
	err     error
}

func (f *fakeChatClient) Chat(_ context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	f.lastReq = req
	return f.resp, f.err
}

func TestDistiller_EndToEnd_HappyPath(t *testing.T) {
	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{
			Choices: []deepseek.Choice{
				{
					Message: deepseek.Message{
						Content: `[{"name":"e2e","tagline":"works","content":"x"}]`,
					},
				},
			},
		},
	}
	d := &Distiller{Client: fake}

	got, err := d.Distill(context.Background(), []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "test session"},
	})
	if err != nil {
		t.Fatalf("Distill: %v", err)
	}
	if len(got) != 1 || got[0].Name != "e2e" {
		t.Errorf("unexpected candidates: %+v", got)
	}

	// Verify the request shape.
	if fake.lastReq.Model != deepseek.ModelV4Flash {
		t.Errorf("Distiller should default to ModelV4Flash, got %q", fake.lastReq.Model)
	}
	// V4-Flash needs Thinking opted in explicitly — Distiller must set
	// it because the prior implicit-via-reasoner-alias path sunsets
	// 2026-07-24.
	if fake.lastReq.Thinking == nil || fake.lastReq.Thinking.Type != "enabled" {
		t.Errorf("Distiller should set Thinking.Type=enabled, got %+v", fake.lastReq.Thinking)
	}
	if len(fake.lastReq.Messages) != 2 {
		t.Fatalf("expected system+user messages, got %d", len(fake.lastReq.Messages))
	}
	if fake.lastReq.Messages[0].Role != deepseek.RoleSystem {
		t.Errorf("first message should be system, got %q", fake.lastReq.Messages[0].Role)
	}
	if !strings.Contains(fake.lastReq.Messages[0].Content, "session-distillation engine") {
		t.Errorf("system prompt content mismatch: %q", fake.lastReq.Messages[0].Content)
	}
}

func TestDistiller_PropagatesChatError(t *testing.T) {
	sentinel := errors.New("network down")
	fake := &fakeChatClient{err: sentinel}
	d := &Distiller{Client: fake}

	_, err := d.Distill(context.Background(), nil)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel, got %v", err)
	}
}

func TestDistiller_NilClientErrors(t *testing.T) {
	d := &Distiller{}
	_, err := d.Distill(context.Background(), nil)
	if err == nil {
		t.Errorf("expected error when Client is nil")
	}
}

func TestDistiller_ZeroChoices(t *testing.T) {
	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: nil},
	}
	d := &Distiller{Client: fake}
	_, err := d.Distill(context.Background(), nil)
	if err == nil {
		t.Errorf("expected error when reasoner returns no choices")
	}
}

func TestDistiller_CustomMax(t *testing.T) {
	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{Content: "[]"}}}},
	}
	d := &Distiller{Client: fake, Max: 7}
	_, _ = d.Distill(context.Background(), nil)
	if !strings.Contains(fake.lastReq.Messages[1].Content, "up to 7") {
		t.Errorf("custom Max not propagated to user message: %q", fake.lastReq.Messages[1].Content)
	}
}
