package sessionexport

import (
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// fixedSession builds a fake multi-turn transcript driving every
// render path: header metadata, system prompt, user turns (one
// with images), assistant turns (reasoning + tool calls), tool
// results (multi-line / oversized / empty-content), and a
// non-JSON-arguments call.
func fixedSession() *session.Session {
	created := time.Date(2026, 1, 21, 10, 30, 45, 0, time.UTC)
	return &session.Session{
		SchemaVersion: 3,
		ID:            "20260121-103045-a1b2c3",
		CreatedAt:     created,
		UpdatedAt:     created.Add(32 * time.Minute),
		Model:         "deepseek-flash",
		CWD:           "/tmp/proj",
		SystemPrompt:  "You are seek, a coding agent.",
		Turns:         3,
		ToolCalls:     3,
		Usage: deepseek.Usage{
			PromptTokens:     1234,
			CompletionTokens: 567,
			TotalTokens:      1801,
		},
		Messages: []deepseek.Message{
			{Role: deepseek.RoleUser, Content: "帮我看看 session 包有哪些测试", Images: []deepseek.ImagePart{
				{Asset: "shot-20260121.png"},
				{URL: "https://example.com/diagram.png"},
			}},
			{Role: deepseek.RoleAssistant, ReasoningContent: "用户想要测试清单。\n先 grep 一下。", ToolCalls: []deepseek.ToolCall{
				{ID: "call_grep1", Type: "function", Function: deepseek.ToolCallFunc{
					Name:      "grep",
					Arguments: `{"pattern":"func Test","path":"internal/session"}`,
				}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "call_grep1", Content: "session_test.go:17 func TestSave\n" +
				"session_test.go:44 func TestLoad\n" +
				"session_test.go:90 func TestRepair\n" +
				strings.Repeat("listproject_test.go:5 func TestListProject\n", 40)},
			{Role: deepseek.RoleAssistant, Content: "共 3 个测试文件、42 个测试函数。", ReasoningContent: "数完了。"},
			{Role: deepseek.RoleUser, Content: "再跑一遍带 -race 的"},
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{ID: "call_bash1", Type: "function", Function: deepseek.ToolCallFunc{
					Name:      "bash",
					Arguments: `{"command":"go test -race ./internal/session/"}`,
				}},
				{ID: "call_bad", Type: "function", Function: deepseek.ToolCallFunc{
					Name:      "mumble",
					Arguments: `not-json-at-all`,
				}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "call_bash1", Content: "ok  \tgithub.com/whyiyhw/seek/internal/session\t0.212s"},
			{Role: deepseek.RoleTool, ToolCallID: "call_bad", Content: ""},
			{Role: deepseek.RoleAssistant, Content: "全部通过。"},
		},
	}
}

func TestRender_MultiTurnRoleSections(t *testing.T) {
	md := Render(fixedSession())

	for _, want := range []string{
		"# seek session 20260121-103045-a1b2c3",
		"- **Model**: deepseek-flash",
		"- **Created**: 2026-01-21T10:30:45Z",
		"- **Updated**: 2026-01-21T11:02:45Z",
		"- **CWD**: /tmp/proj",
		"- **Turns**: 3 · **Tool calls**: 3",
		"- **Tokens**: prompt 1,234 · completion 567 · total 1,801",
		"<summary>System prompt · 29 chars</summary>",
		"You are seek, a coding agent.",
		"## User",
		"## Assistant",
		"帮我看看 session 包有哪些测试",
		"共 3 个测试文件、42 个测试函数。",
		"再跑一遍带 -race 的",
		"全部通过。",
		"```thinking\n用户想要测试清单。\n先 grep 一下。\n```",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("render missing %q\n--- got ---\n%s", want, md)
		}
	}
	if got := strings.Count(md, "## User"); got != 2 {
		t.Errorf("expected 2 User sections, got %d", got)
	}
	if got := strings.Count(md, "## Assistant"); got != 4 {
		t.Errorf("expected 4 Assistant sections, got %d", got)
	}
}

func TestRender_ToolCallsFoldedToSummaryLines(t *testing.T) {
	md := Render(fixedSession())

	// Assistant tool calls fold to one summary line each: name +
	// bounded key=value args.
	for _, want := range []string{
		"- **grep** · `path=internal/session pattern=func Test`",
		"- **bash** · `command=go test -race ./internal/session/`",
		// Non-JSON arguments fall back to the raw string.
		"- **mumble** · `not-json-at-all`",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("render missing folded call %q\n--- got ---\n%s", want, md)
		}
	}

	// Tool results fold to their first line under a named section.
	for _, want := range []string{
		"## Tool · grep (call_grep1)",
		"> session_test.go:17 func TestSave",
		"## Tool · bash (call_bash1)",
		"> ok  \tgithub.com/whyiyhw/seek/internal/session\t0.212s",
		"> *(empty output)*",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("render missing folded result %q\n--- got ---\n%s", want, md)
		}
	}

	// The folding actually folds: later lines of the oversized
	// grep result must NOT be dumped into the export verbatim.
	if strings.Contains(md, "listproject_test.go:5") {
		t.Error("oversized tool result was not folded — line 2+ leaked into export")
	}
	if !strings.Contains(md, "chars total)*") {
		t.Error("oversized tool result missing the total-chars note")
	}
}

func TestRender_ArgumentFoldingBounds(t *testing.T) {
	long := strings.Repeat("x", 500)
	s := &session.Session{
		ID: "s", Model: "m",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{ID: "c1", Function: deepseek.ToolCallFunc{
					Name:      "bash",
					Arguments: `{"command":"` + long + `","cwd":"/a","extra":1,"more":2}`,
				}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "c1", Content: "done"},
		},
	}
	md := Render(s)

	line := "- **bash** · `"
	i := strings.Index(md, line)
	if i < 0 {
		t.Fatalf("missing bash summary line in:\n%s", md)
	}
	rest := md[i+len(line):]
	j := strings.IndexByte(rest, '`')
	if j < 0 {
		t.Fatalf("unterminated summary line in:\n%s", md)
	}
	summary := rest[:j]
	if !strings.Contains(summary, "cwd=/a extra=1 …(+1 more)") {
		t.Errorf("expected 3 shown keys + (+1 more) marker when a key is dropped, got %q", summary)
	}
	if strings.Contains(summary, long) {
		t.Errorf("500-char argument value not truncated: %q", summary)
	}
	// Whole summary line stays under the bound (+1 for the ellipsis).
	if rc := len([]rune(summary)); rc > argsMax+1 {
		t.Errorf("summary line %d runes exceeds argsMax=%d", rc, argsMax)
	}
}

func TestRender_ImagePlaceholders(t *testing.T) {
	md := Render(fixedSession())
	for _, want := range []string{
		"*[image: shot-20260121.png]*",
		"*[image: https://example.com/diagram.png]*",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("render missing image placeholder %q", want)
		}
	}
}

func TestRender_EmptySession(t *testing.T) {
	md := Render(&session.Session{ID: "empty-1", Model: "deepseek-flash"})
	if !strings.Contains(md, "# seek session empty-1") {
		t.Errorf("empty session still renders header, got:\n%s", md)
	}
	if strings.Contains(md, "## User") {
		t.Errorf("empty session must not render message sections:\n%s", md)
	}
}

func TestRender_NilSession(t *testing.T) {
	if got := Render(nil); got != "" {
		t.Errorf("Render(nil) = %q, want empty", got)
	}
}

func TestRender_Deterministic(t *testing.T) {
	a, b := Render(fixedSession()), Render(fixedSession())
	if a != b {
		t.Error("Render is not deterministic for the same session")
	}
}

func TestRender_TruncateRuneSafe(t *testing.T) {
	// CJK content must never be split mid-rune by the folding.
	s := &session.Session{
		ID: "s", Model: "m",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleAssistant, ToolCalls: []deepseek.ToolCall{
				{ID: "c1", Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"command":"` + strings.Repeat("测", 100) + `"`}},
			}},
			{Role: deepseek.RoleTool, ToolCallID: "c1", Content: strings.Repeat("试", 400)},
		},
	}
	md := Render(s)
	for _, r := range md {
		if r == 0xFFFD {
			t.Fatal("folded export contains U+FFFD replacement runes — truncation split a multi-byte character")
		}
	}
}

func TestRender_ToolNameUnresolvable(t *testing.T) {
	// Tool message whose id matches no assistant call: falls back
	// through Name → "tool", never renders an empty label.
	s := &session.Session{
		ID: "s", Model: "m",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleTool, ToolCallID: "orphan-1", Content: "stray"},
		},
	}
	md := Render(s)
	if !strings.Contains(md, "## Tool · tool (orphan-1)") {
		t.Errorf("unresolvable tool label fallback broken, got:\n%s", md)
	}
	if !strings.Contains(md, "> stray") {
		t.Errorf("orphan result content missing")
	}
}

func TestRender_FenceEscapesInnerFence(t *testing.T) {
	s := &session.Session{
		ID: "s", Model: "m",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleAssistant, ReasoningContent: "outer\n```\ninner fence\n```\ndone"},
		},
	}
	md := Render(s)
	// The thinking fence must have grown past ``` so the inner
	// fence doesn't terminate it early.
	if !strings.Contains(md, "````thinking\nouter\n```\ninner fence\n```\ndone\n````") {
		t.Errorf("inner fence not escaped, got:\n%s", md)
	}
}
