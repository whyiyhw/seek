package memory

import (
	"strings"
	"testing"
)

func TestEstimateTokens_Empty(t *testing.T) {
	if n := estimateTokens(""); n != 0 {
		t.Errorf("empty string should be 0, got %d", n)
	}
}

func TestEstimateTokens_ASCII(t *testing.T) {
	// ~4 chars per token for ASCII
	n := estimateTokens("hello world") // 11 chars
	if n < 2 || n > 3 {
		t.Errorf("11 ASCII chars ≈ 2.75 tokens, got %d", n)
	}
}

func TestEstimateTokens_CJK(t *testing.T) {
	// ~3 chars per token for CJK
	n := estimateTokens("你好世界") // 4 CJK chars
	if n < 1 || n > 2 {
		t.Errorf("4 CJK chars ≈ 1.33 tokens, got %d", n)
	}
}

func TestEstimateTokens_Mixed(t *testing.T) {
	n := estimateTokens("倾向显式错误处理胜过 panic") // mixed CJK + ASCII
	if n < 3 || n > 8 {
		t.Errorf("mixed string gave unexpected token count %d", n)
	}
}

func TestTruncateSoulStable_UnderLimit(t *testing.T) {
	input := "- 倾向显式错误处理胜过 panic\n- 代码风格偏简洁"
	// Small content should pass through unchanged.
	out := truncateSoulStable(input)
	if out != input {
		t.Errorf("under-limit content should be unchanged:\ngot:  %q\nwant: %q", out, input)
	}
}

func TestTruncateSoulStable_Empty(t *testing.T) {
	if out := truncateSoulStable(""); out != "" {
		t.Errorf("empty input should produce empty, got %q", out)
	}
}

func TestTruncateSoulStable_Whitespace(t *testing.T) {
	input := "  \n  "
	out := truncateSoulStable(input)
	if out != input {
		t.Errorf("whitespace-only should pass through, got %q", out)
	}
}

func TestTruncateSoulStable_DropsTrailingBullets(t *testing.T) {
	// Build content that exceeds the token budget.
	// Each bullet is ~60 CJK chars, estimateTokens ≈ 20 tokens.
	// 60 bullets × 20 = ~1200 tokens — well over the 450 limit.
	var bullets []string
	for i := 0; i < 60; i++ {
		bullets = append(bullets, "- **这是一个非常长的用户偏好条目用来测试截断逻辑是否正常工作**")
	}
	input := strings.Join(bullets, "\n")

	out := truncateSoulStable(input)

	// Output should be shorter than input.
	if len(out) >= len(input) {
		t.Errorf("truncated output should be shorter than input (%d >= %d)", len(out), len(input))
	}

	// Output must be under the token budget.
	if estimateTokens(out) > maxSoulTokens+5 { // +5 for the truncation notice
		t.Errorf("output exceeds token budget: %d > %d", estimateTokens(out), maxSoulTokens+5)
	}

	// Output should end with a truncation notice.
	if !strings.Contains(out, "truncated") {
		t.Errorf("truncated output should contain notice, got:\n%s", out)
	}
}

func TestTruncateSoulStable_PreservesPreamble(t *testing.T) {
	input := `一些前置介绍文字
可能会包含说明

- 第一个条目
- 第二个条目`

	out := truncateSoulStable(input)

	// Preamble must be preserved.
	if !strings.Contains(out, "一些前置介绍文字") {
		t.Errorf("preamble should be preserved, got:\n%s", out)
	}
}

func TestTruncateSoulStable_PreservesFirstBullet(t *testing.T) {
	input := `- 最重要的条目
  来源：项目A (5 sessions)
  首次观察：2026-01-01

- 次要条目`

	out := truncateSoulStable(input)

	if !strings.Contains(out, "最重要的条目") {
		t.Errorf("first bullet should be preserved, got:\n%s", out)
	}
}

func TestTruncateSoulStable_SubBulletsStayWithParent(t *testing.T) {
	// Each entry with its sub-bullets should be treated as one unit.
	input := `- **主条目**
  - 来源：项目A
  - 确认次数：5`

	out := truncateSoulStable(input)
	if !strings.Contains(out, "主条目") || !strings.Contains(out, "来源：项目A") {
		t.Errorf("sub-bullets should stay with parent, got:\n%s", out)
	}
}

func TestTruncateSoulStable_NoBullets(t *testing.T) {
	input := "这是一段没有 bullet 的文字\n它只是一段说明"
	out := truncateSoulStable(input)
	if out != input {
		t.Errorf("no-bullet content should pass through unchanged, got %q", out)
	}
}

func TestTruncateSoulStable_Deterministic(t *testing.T) {
	// Same input must produce same output every time.
	input := "- first\n- second\n- third\n- fourth\n- fifth\n- sixth\n- seventh\n- eighth\n- ninth\n- tenth"
	out1 := truncateSoulStable(input)
	out2 := truncateSoulStable(input)
	if out1 != out2 {
		t.Errorf("truncateSoulStable is not deterministic:\nrun1: %q\nrun2: %q", out1, out2)
	}
}

func TestIsBulletLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"- hello", true},
		{"* hello", true},
		{"+ hello", true},
		{"  - nested", false}, // indented bullets not treated as entry starts
		{"-hello", false},     // no space after dash
		{"plain text", false},
		{"", false},
		{"# heading", false},
	}
	for _, c := range cases {
		got := isBulletLine(c.line)
		if got != c.want {
			t.Errorf("isBulletLine(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}
