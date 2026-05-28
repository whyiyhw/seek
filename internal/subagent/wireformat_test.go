package subagent

import (
	"strings"
	"testing"
)

func TestFormatCompleted_PrefixContract(t *testing.T) {
	out := FormatCompleted("20260601-103412-7a3f4b",
		"Found 5 handlers in internal/tui.\nDetails follow...",
		3,
		Tokens{Prompt: 8000, Completion: 200, CacheHit: 7500},
	)
	// Wire-format prefix MUST be exactly this — parsers will look
	// for it verbatim.
	if !strings.HasPrefix(out, "[agent: completed] ") {
		t.Errorf("missing prefix; got:\n%s", out)
	}
	// Headline is the first non-empty line.
	wantHeadline := "Found 5 handlers in internal/tui."
	if !strings.Contains(out, wantHeadline) {
		t.Errorf("missing headline %q in:\n%s", wantHeadline, out)
	}
	// Body must include the rest.
	if !strings.Contains(out, "Details follow...") {
		t.Errorf("missing body content in:\n%s", out)
	}
	// Footer.
	if !strings.Contains(out, "turns: 3") {
		t.Errorf("missing turns count in:\n%s", out)
	}
	if !strings.Contains(out, "tokens: 8200") { // 8000 + 200
		t.Errorf("missing token total in:\n%s", out)
	}
}

func TestFormatCompleted_HeadlineCappedAt120(t *testing.T) {
	long := strings.Repeat("x", 300)
	out := FormatCompleted("sid-aaa", long, 1, Tokens{})
	first := strings.SplitN(out, "\n", 2)[0]
	// Headline portion (after "[agent: completed] ") at most 120
	// chars.
	headlinePart := strings.TrimPrefix(first, "[agent: completed] ")
	if len(headlinePart) > 120 {
		t.Errorf("headline length %d > 120 chars", len(headlinePart))
	}
}

func TestFormatCompleted_SummaryTruncated(t *testing.T) {
	huge := strings.Repeat("a", MaxSummaryBytes+1000)
	out := FormatCompleted("sid-bbb", huge, 1, Tokens{})
	if !strings.Contains(out, truncationSuffix) {
		t.Errorf("oversized summary not truncated; missing %q", truncationSuffix)
	}
}

func TestFormatCompleted_SummaryWithinBudgetNotTruncated(t *testing.T) {
	body := strings.Repeat("b", MaxSummaryBytes-100)
	out := FormatCompleted("sid-ccc", body, 1, Tokens{})
	if strings.Contains(out, truncationSuffix) {
		t.Errorf("under-budget summary should not be truncated")
	}
}

func TestFormatFailed_PrefixAndReason(t *testing.T) {
	out := FormatFailed("20260601-103412-7a3f4b", "too_many_subagents", "3 active, retry later")
	if !strings.HasPrefix(out, "[agent: failed reason=too_many_subagents]") {
		t.Errorf("missing wire-format prefix; got:\n%s", out)
	}
	if !strings.Contains(out, "3 active, retry later") {
		t.Errorf("missing hint; got:\n%s", out)
	}
	if !strings.Contains(out, "sub-sid: 7a3f4b") {
		t.Errorf("missing sub-sid footer; got:\n%s", out)
	}
}

func TestFormatFailed_NoHint(t *testing.T) {
	out := FormatFailed("a-b-7a3f", "canceled", "")
	// Must not have trailing space after the closing bracket.
	if strings.Contains(out, "] \n") {
		t.Errorf("trailing space after empty hint: %q", out)
	}
}

// TestShortSid covers the regular case + degenerate sub-sids
// (missing hyphens, empty) so the wire format never crashes on
// unusual inputs.
func TestShortSid(t *testing.T) {
	cases := map[string]string{
		"20260601-103412-7a3f4b": "7a3f4b",
		"no-hyphen":              "hyphen",
		"":                       "",
		"trailing-":              "trailing-", // last char IS hyphen, fall through
	}
	for in, want := range cases {
		if got := shortSid(in); got != want {
			t.Errorf("shortSid(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTruncateSummary_UTF8Boundary ensures we never split a UTF-8
// codepoint when truncating at MaxSummaryBytes. Build a payload
// where the byte at MaxSummaryBytes lands mid-rune.
func TestTruncateSummary_UTF8Boundary(t *testing.T) {
	// A Chinese "字" is 3 bytes (E5 AD 97). Pad with ASCII to land
	// the truncation point inside a multi-byte rune.
	padLen := MaxSummaryBytes - 1 // truncation falls 1 byte into the rune
	body := strings.Repeat("x", padLen) + "字字字字"

	out := truncateSummary(body)
	// The output prefix must be valid UTF-8 — no half rune at the
	// boundary.
	prefix := strings.TrimSuffix(out, truncationSuffix)
	for i, r := range prefix {
		if r == 0xFFFD { // replacement char from broken UTF-8
			t.Errorf("invalid UTF-8 at offset %d in truncated output", i)
		}
	}
	if !strings.HasSuffix(out, truncationSuffix) {
		t.Error("missing truncation suffix on oversized input")
	}
}
