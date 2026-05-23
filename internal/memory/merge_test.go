package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestLevenshtein_Identical(t *testing.T) {
	if d := levenshtein("hello", "hello"); d != 0 {
		t.Errorf("identical strings should have distance 0, got %d", d)
	}
}

func TestLevenshtein_Empty(t *testing.T) {
	if d := levenshtein("", "abc"); d != 3 {
		t.Errorf("empty vs abc should be 3, got %d", d)
	}
	if d := levenshtein("abc", ""); d != 3 {
		t.Errorf("abc vs empty should be 3, got %d", d)
	}
}

func TestLevenshtein_OneSubstitution(t *testing.T) {
	if d := levenshtein("kitten", "sitten"); d != 1 {
		t.Errorf("kitten→sitten should be 1, got %d", d)
	}
}

func TestLevenshtein_CJK(t *testing.T) {
	// Same character swapped with different character
	if d := levenshtein("你好", "你好"); d != 0 {
		t.Errorf("identical CJK should be 0, got %d", d)
	}
	if d := levenshtein("你好", "你们"); d != 1 {
		t.Errorf("你好→你们 should be 1, got %d", d)
	}
}

func TestTraitSimilarity_Identical(t *testing.T) {
	s := traitSimilarity("prefers explicit error handling", "prefers explicit error handling")
	if s < 0.99 {
		t.Errorf("identical traits should have similarity ~1.0, got %f", s)
	}
}

func TestTraitSimilarity_Empty(t *testing.T) {
	if s := traitSimilarity("", ""); s != 1.0 {
		t.Errorf("both empty should be 1.0, got %f", s)
	}
	if s := traitSimilarity("hello", ""); s != 0.0 {
		t.Errorf("one empty should be 0.0, got %f", s)
	}
}

func TestTraitSimilarity_Normalization(t *testing.T) {
	// Case difference should produce high similarity.
	s := traitSimilarity("Prefers Error Handling", "prefers error handling")
	if s < 0.95 {
		t.Errorf("case difference only should produce >0.95, got %f", s)
	}
}

func TestTraitSimilarity_Threshold(t *testing.T) {
	cases := []struct {
		a, b string
		want bool // true = should be considered "same" (≥0.5)
	}{
		{"prefers explicit error handling over panic", "prefers explicit error handling", true},
		{"uses tabs for indentation", "uses spaces for indentation", true},
		{"prefers struct-based config over function options", "likes functional options", false},
		{"中文偏好：显式错误处理", "中文习惯：显式错误处理", true},
		{"completely unrelated trait", "something entirely different", false},
		{"low evidence trait", "high evidence trait", false},
	}
	for _, c := range cases {
		got := traitSimilarity(c.a, c.b) >= mergeSimilarityThreshold
		if got != c.want {
			t.Errorf("traitSimilarity(%q, %q) ≥ 0.55 = %v, want %v (score=%f)",
				c.a, c.b, got, c.want, traitSimilarity(c.a, c.b))
		}
	}
}

func TestExtractBoldText(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"- **hello world**", "hello world"},
		{"- **hello**", "hello"},
		{"- hello", "hello"},
		{"no bold here", "no bold here"},
		{"- ****", ""},
	}
	for _, c := range cases {
		got := extractBoldText(c.input)
		if got != c.want {
			t.Errorf("extractBoldText(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestParseLMarkdown_Empty(t *testing.T) {
	if out := parseLMarkdown(""); out != nil {
		t.Errorf("empty input should return nil, got %+v", out)
	}
}

func TestParseLMarkdown_SingleEntry(t *testing.T) {
	input := `- **prefers explicit error handling**
  - 来源 / why: seen across three Go projects
  - sources: proj-seek, proj-foo, proj-bar`

	cands := parseLMarkdown(input)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d: %+v", len(cands), cands)
	}
	if cands[0].Trait != "prefers explicit error handling" {
		t.Errorf("trait mismatch: %q", cands[0].Trait)
	}
	if !strings.Contains(cands[0].Why, "seen across three Go projects") {
		t.Errorf("why mismatch: %q", cands[0].Why)
	}
	if len(cands[0].Sources) != 3 {
		t.Errorf("expected 3 sources, got %d: %v", len(cands[0].Sources), cands[0].Sources)
	}
}

func TestParseLMarkdown_MultipleEntries(t *testing.T) {
	input := `- **trait one**
  - sources: proj-a

- **trait two**
  - 来源 / why: reason
  - sources: proj-b, proj-c`

	cands := parseLMarkdown(input)
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if cands[0].Trait != "trait one" || cands[1].Trait != "trait two" {
		t.Errorf("unexpected traits: %q, %q", cands[0].Trait, cands[1].Trait)
	}
}

func TestParseLMarkdown_NoWhyOrSources(t *testing.T) {
	input := `- **just a trait**`
	cands := parseLMarkdown(input)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Trait != "just a trait" {
		t.Errorf("trait mismatch: %q", cands[0].Trait)
	}
	if cands[0].Why != "" {
		t.Errorf("expected empty why, got %q", cands[0].Why)
	}
	if len(cands[0].Sources) != 0 {
		t.Errorf("expected no sources, got %v", cands[0].Sources)
	}
}

func TestParseLMarkdown_RoundTrip(t *testing.T) {
	// Parse → Format → Parse should produce the same candidates.
	original := []LCandidate{
		{Trait: "trait alpha", Why: "observed in many projects", Sources: []string{"proj-a", "proj-b"}},
		{Trait: "trait beta", Sources: []string{"proj-c"}},
	}
	rendered := FormatLCandidatesMarkdown(original)
	reparsed := parseLMarkdown(rendered)

	if len(reparsed) != len(original) {
		t.Fatalf("round-trip length mismatch: %d vs %d", len(reparsed), len(original))
	}
	for i := range original {
		if reparsed[i].Trait != original[i].Trait {
			t.Errorf("trait %d mismatch: %q vs %q", i, reparsed[i].Trait, original[i].Trait)
		}
	}
}

func TestMergeSources_DedupAndSort(t *testing.T) {
	out := mergeSources([]string{"proj-b", "proj-a"}, []string{"proj-c", "proj-a"})
	want := []string{"proj-a", "proj-b", "proj-c"}
	if len(out) != len(want) {
		t.Fatalf("expected %v, got %v", want, out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, out[i], want[i])
		}
	}
}

func TestMergeWhy_AppendsWhenNew(t *testing.T) {
	got := mergeWhy("reason one", "reason two")
	if got != "reason one; reason two" {
		t.Errorf("expected 'reason one; reason two', got %q", got)
	}
}

func TestMergeWhy_SkipsSubstring(t *testing.T) {
	got := mergeWhy("reason one with details", "reason one")
	if got != "reason one with details" {
		t.Errorf("existing already contains incoming, should keep as-is, got %q", got)
	}
}

func TestMergeWhy_EmptyIncoming(t *testing.T) {
	got := mergeWhy("reason one", "")
	if got != "reason one" {
		t.Errorf("empty incoming should not change existing, got %q", got)
	}
}

func TestMergeWhy_EmptyExisting(t *testing.T) {
	got := mergeWhy("", "reason one")
	if got != "reason one" {
		t.Errorf("empty existing should accept incoming, got %q", got)
	}
}

func TestMergeIntoL_EmptyExisting(t *testing.T) {
	incoming := []LCandidate{
		{Trait: "new trait", Sources: []string{"proj-a", "proj-b"}},
	}
	out := MergeIntoL("", incoming)
	if !strings.Contains(out, "new trait") {
		t.Errorf("new trait should appear in output: %q", out)
	}
	if !strings.Contains(out, "proj-a") {
		t.Errorf("sources should appear in output: %q", out)
	}
}

func TestMergeIntoL_EmptyIncoming(t *testing.T) {
	existing := "- **existing trait**\n  - sources: proj-a"
	out := MergeIntoL(existing, nil)
	if out != existing {
		t.Errorf("nil incoming should return existing unchanged, got %q", out)
	}
}

func TestMergeIntoL_MergesSimilarTraits(t *testing.T) {
	existing := "- **prefers explicit error handling**\n  - 来源 / why: from proj-seek\n  - sources: proj-seek"
	incoming := []LCandidate{
		{Trait: "prefers explicit error handling over panic", Why: "from proj-foo", Sources: []string{"proj-foo", "proj-bar"}},
	}

	out := MergeIntoL(existing, incoming)

	// Should have exactly one entry (merged).
	cands := parseLMarkdown(out)
	if len(cands) != 1 {
		t.Fatalf("expected 1 merged entry, got %d: %+v", len(cands), cands)
	}

	// Trait text preserved (existing kept).
	if cands[0].Trait != "prefers explicit error handling" {
		t.Errorf("expected existing trait preserved, got %q", cands[0].Trait)
	}

	// Sources combined.
	if len(cands[0].Sources) != 3 {
		t.Errorf("expected 3 combined sources, got %d: %v", len(cands[0].Sources), cands[0].Sources)
	}
}

func TestMergeIntoL_KeepsDistinctTraitsSeparate(t *testing.T) {
	existing := "- **uses tabs for indentation**\n  - sources: proj-go"
	incoming := []LCandidate{
		{Trait: "prefers struct-based config over options", Sources: []string{"proj-rust"}},
	}

	out := MergeIntoL(existing, incoming)
	cands := parseLMarkdown(out)
	if len(cands) != 2 {
		t.Fatalf("expected 2 distinct entries, got %d: %+v", len(cands), cands)
	}
}

func TestMergeIntoL_SortBySourceCount(t *testing.T) {
	existing := "- **low evidence trait**\n  - sources: proj-a"
	incoming := []LCandidate{
		{Trait: "high evidence trait", Sources: []string{"proj-a", "proj-b", "proj-c"}},
	}

	out := MergeIntoL(existing, incoming)
	cands := parseLMarkdown(out)
	if len(cands) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(cands))
	}
	// Higher source count should come first.
	if cands[0].Trait != "high evidence trait" {
		t.Errorf("more-sourced trait should be first, got %q", cands[0].Trait)
	}
}

func TestMergeIntoL_Deterministic(t *testing.T) {
	existing := "- **trait A**\n  - sources: proj-a\n\n- **trait B**\n  - sources: proj-b"
	incoming := []LCandidate{
		{Trait: "trait C", Sources: []string{"proj-c"}},
	}

	hash := func() string {
		out := MergeIntoL(existing, incoming)
		sum := sha256.Sum256([]byte(out))
		return hex.EncodeToString(sum[:])
	}

	h1 := hash()
	h2 := hash()
	if h1 != h2 {
		t.Errorf("MergeIntoL is not deterministic:\nrun1: %s\nrun2: %s", h1, h2)
	}
}

func TestMergeIntoL_WhyExtension(t *testing.T) {
	existing := "- **some trait**\n  - 来源 / why: original observation\n  - sources: proj-a"
	incoming := []LCandidate{
		{Trait: "some trait", Why: "new observation", Sources: []string{"proj-b"}},
	}

	out := MergeIntoL(existing, incoming)
	cands := parseLMarkdown(out)
	if len(cands) != 1 {
		t.Fatalf("expected 1 merged entry, got %d", len(cands))
	}
	if !strings.Contains(cands[0].Why, "original observation") || !strings.Contains(cands[0].Why, "new observation") {
		t.Errorf("why should contain both sources, got %q", cands[0].Why)
	}
}

func TestMergeIntoL_TooLargePendingRendersNotice(t *testing.T) {
	// Build a Pending section large enough to exceed maxPendingTokens.
	var bullets []string
	for i := 0; i < 200; i++ {
		bullets = append(bullets, "- **这是一个非常长的用户偏好条目用来测试 Pending 截断逻辑**\n  - sources: proj-a")
	}
	existing := strings.Join(bullets, "\n\n")
	// Give the incoming trait 3 sources so it sorts first (highest priority).
	incoming := []LCandidate{
		{Trait: "new trait", Sources: []string{"proj-b", "proj-c", "proj-d"}},
	}

	out := MergeIntoL(existing, incoming)

	if estimateTokens(out) > maxPendingTokens+10 {
		t.Errorf("output exceeds pending token cap: %d > %d", estimateTokens(out), maxPendingTokens+10)
	}

	// The incoming trait has the most sources, so it sorts first and survives truncation.
	if !strings.Contains(out, "new trait") {
		t.Errorf("incoming high-source trait should appear in output, got:\n%s", out)
	}
}
