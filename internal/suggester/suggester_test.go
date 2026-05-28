package suggester

import (
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestCleanPrediction(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \n\t  ", ""},
		{"trimmed", "  好的就 A  ", "好的就 A"},
		{"first line only", "用 A 方案\nbecause it's safer", "用 A 方案"},
		{"strips ascii quotes", `"用 A"`, "用 A"},
		{"strips smart quotes", "“用 A”", "用 A"},
		{"strips Chinese brackets", "「用 A」", "用 A"},
		{"sentinel echo rejected", predictNextSentinel, ""},
		{"keeps non-wrapping quotes", `let's "go" with A`, `let's "go" with A`},
		{"rejects over-long", strings.Repeat("a", 201), ""},
		{"keeps just-under-limit", strings.Repeat("a", 200), strings.Repeat("a", 200)},
		// Below-minimum runes (< minPredictionRunes) rejected as content-free
		{"rejects single rune", "好", ""},
		{"rejects single ascii", "a", ""},
		// Multi-choice short predictions ("用 A" = 3 runes) survive both the
		// minRunes floor (2) and the denylist (since "用 a" is not generic)
		{"keeps 用 A multi-choice", "用 A", "用 A"},
		{"keeps 选 B multi-choice", "选 B", "选 B"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanPrediction(tc.in); got != tc.want {
				t.Errorf("cleanPrediction(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCleanPrediction_GenericDenylist pins the content-based noise
// filter. The Flash model occasionally pads out a low-confidence
// guess with generic replies ("好的" / "ok" / "继续") that carry no
// real user-intent signal — those get dropped to "" so the UI
// doesn't show a meaningless placeholder.
func TestCleanPrediction_GenericDenylist(t *testing.T) {
	cases := []string{
		// Bare denylist entries
		"好", "好的", "ok", "okay", "yes", "继续", "知道了", "thanks", "明白",
		// With trailing punctuation — normalize strips ASCII punct so they match
		"OK!", "好的。", "Sure!", "ok.",
		// Wrapping quotes get stripped first, then denylist matches
		`"好的"`, "“ok”",
		// Mixed case
		"Ok", "YES", "OK",
		// Trailing/leading whitespace (collapsed via normalize)
		"  好的  ",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if got := cleanPrediction(in); got != "" {
				t.Errorf("cleanPrediction(%q) = %q, want \"\" (generic noise)", in, got)
			}
		})
	}
}

func TestShouldPredict(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		// Positive: clear invitations to respond
		{"ascii question mark", "Should I run the tests?", true},
		{"chinese question mark", "需要我把它合到 main 吗？", true},
		{"multi-choice bracket A", "Options: [A] do X [B] do Y", true},
		{"multi-choice paren", "What now: A) keep B) discard", true},
		{"chinese 选项", "请选 选项 A 还是 B", true},
		{"shall I phrase", "Shall I proceed with the migration", true},
		{"would you phrase", "Would you like me to commit this?", true},
		{"chinese 需要我", "需要我跑下 race 测试吗", true},
		{"chinese 要不要", "要不要顺便把 lint 跑了", true},

		// Negative: clear endings, no invitation
		{"empty", "", false},
		{"plain done", "Done. All tests pass.", false},
		{"chinese 完成", "改完了，提交完毕。", false},
		{"long body no question", "Here is the analysis. The function signature changed in three places. The test suite was updated to match. I ran go test -race and everything passed cleanly.", false},
		// "?" further back than tailScanRunes should still be caught
		// because the assistant final paragraph is usually well under 300 runes;
		// validate that a ? near the END is detected even with prose before.
		{"question buried in long body", strings.Repeat("filler ", 80) + "Should I do this?", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldPredict(tc.content); got != tc.want {
				t.Errorf("ShouldPredict(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestTailRunes(t *testing.T) {
	if got := tailRunes("", 5); got != "" {
		t.Errorf("empty input → empty, got %q", got)
	}
	if got := tailRunes("hello", 0); got != "" {
		t.Errorf("n=0 → empty, got %q", got)
	}
	if got := tailRunes("hello", 100); got != "hello" {
		t.Errorf("n > len → full string, got %q", got)
	}
	if got := tailRunes("hello", 3); got != "llo" {
		t.Errorf("ascii tail, got %q", got)
	}
	// Rune-aware on CJK
	if got := tailRunes("一二三四五", 2); got != "四五" {
		t.Errorf("cjk tail, got %q", got)
	}
}

func TestSuggest_NilPredictor(t *testing.T) {
	var p *Predictor
	got := p.Suggest(t.Context(), []deepseek.Message{{Role: "user", Content: "hi"}})
	if got != "" {
		t.Errorf("nil predictor should return empty string, got %q", got)
	}
}

func TestNormalizedMatch(t *testing.T) {
	cases := []struct {
		name      string
		actual    string
		predicted string
		want      bool
	}{
		{"identical", "用 A 方案", "用 A 方案", true},
		{"actual longer", "好的就 用 A 方案 谢谢", "用 A 方案", true},
		{"predicted longer", "用 A", "用 A 方案吧", true},
		{"different choice", "用 B", "用 A 方案", false},
		{"case insensitive", "USE A", "use a", true},
		{"strips ascii punct", "yes, A.", "yes A", true},
		{"keeps Chinese punctuation", "用 A，谢谢", "用 A", true},
		{"empty actual", "", "用 A", false},
		{"empty predicted", "用 A", "", false},
		{"both empty", "", "", false},
		{"trailing whitespace", "  用 A 方案  ", "用 A 方案", true},
		{"completely off", "改用 redis 行不", "用 mysql 行不", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizedMatch(tc.actual, tc.predicted); got != tc.want {
				t.Errorf("normalizedMatch(%q, %q) = %v, want %v",
					tc.actual, tc.predicted, got, tc.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	if got := normalize("  Hello, World!  "); got != "hello world" {
		t.Errorf("normalize trims+lowercases+strips ascii punct, got %q", got)
	}
	// CJK punctuation is now stripped (commit "noise placeholder bug" —
	// see normalize comment). "，" is dropped; surrounding non-punct
	// chars glue together without a synthetic space.
	if got := normalize("用 A，方案"); got != "用 a方案" {
		t.Errorf("normalize should strip CJK punct (，) and keep CJK chars, got %q", got)
	}
	if got := normalize("  multi   space  text  "); got != "multi space text" {
		t.Errorf("normalize collapses whitespace, got %q", got)
	}
}

func TestInjectCalibration_MismatchInjects(t *testing.T) {
	msgs := []deepseek.Message{
		{Role: "system", Content: "main sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "I recommend A.", PredictedNext: "用 A 方案"},
		{Role: "user", Content: "我用 B"},
	}
	out := InjectCalibration(msgs)
	if len(out) != len(msgs)+1 {
		t.Fatalf("expected len+1 after inject, got %d (in=%d)", len(out), len(msgs))
	}
	// Inserted note is at index len-2 (immediately before last user msg).
	note := out[len(out)-2]
	if note.Role != deepseek.RoleSystem {
		t.Errorf("inserted note role = %q, want system", note.Role)
	}
	if !strings.Contains(note.Content, "[calibration]") {
		t.Errorf("note content should carry [calibration] tag, got %q", note.Content)
	}
	if !strings.Contains(note.Content, "用 A 方案") {
		t.Errorf("note should mention the predicted text, got %q", note.Content)
	}
	if !strings.Contains(note.Content, "我用 B") {
		t.Errorf("note should mention the actual text, got %q", note.Content)
	}
	// Last message preserved.
	if last := out[len(out)-1]; last.Role != deepseek.RoleUser || last.Content != "我用 B" {
		t.Errorf("trailing user message corrupted: %+v", last)
	}
}

func TestInjectCalibration_MatchSkipsInject(t *testing.T) {
	msgs := []deepseek.Message{
		{Role: "system", Content: "main sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "I recommend A.", PredictedNext: "用 A 方案"},
		{Role: "user", Content: "好的，用 A"},
	}
	out := InjectCalibration(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("expected len unchanged on match, got %d", len(out))
	}
}

func TestInjectCalibration_NoPredictionSkipsInject(t *testing.T) {
	msgs := []deepseek.Message{
		{Role: "system", Content: "main sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "I recommend A."},
		{Role: "user", Content: "我用 B"},
	}
	out := InjectCalibration(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("expected len unchanged without prediction, got %d", len(out))
	}
}

func TestInjectCalibration_LastIsAssistant_SkipsInject(t *testing.T) {
	msgs := []deepseek.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", PredictedNext: "thanks"},
	}
	out := InjectCalibration(msgs)
	if len(out) != len(msgs) {
		t.Fatalf("expected len unchanged when last is assistant, got %d", len(out))
	}
}

func TestInjectCalibration_SkipsThroughToolMessages(t *testing.T) {
	// Pattern: assistant-with-tool-calls → tool result → user.
	// The "most recent non-tool assistant" is the assistant with
	// PredictedNext set. Should still detect mispredict.
	msgs := []deepseek.Message{
		{Role: "user", Content: "fix it"},
		{Role: "assistant", Content: "Done. I recommend running tests next.", PredictedNext: "跑测试"},
		{Role: "tool", Content: "(some tool result)", ToolCallID: "x"},
		{Role: "user", Content: "改名好了吗"},
	}
	out := InjectCalibration(msgs)
	if len(out) != len(msgs)+1 {
		t.Fatalf("expected len+1 when prior assistant (through tool) had mispredict, got %d", len(out))
	}
}

func TestInjectCalibration_DoesNotMutateInput(t *testing.T) {
	msgs := []deepseek.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "A", PredictedNext: "用 A"},
		{Role: "user", Content: "我用 B"},
	}
	originalLen := len(msgs)
	_ = InjectCalibration(msgs)
	if len(msgs) != originalLen {
		t.Errorf("InjectCalibration mutated input slice length: %d → %d", originalLen, len(msgs))
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("short string unchanged, got %q", got)
	}
	if got := truncateRunes("用ABCDEFG", 4); got != "用ABC…" {
		t.Errorf("rune-aware truncation, got %q", got)
	}
	// Verify byte-vs-rune safety (CJK = 3 bytes each in UTF-8)
	if got := truncateRunes("一二三四五六七", 3); got != "一二三…" {
		t.Errorf("rune truncation on CJK, got %q", got)
	}
}
