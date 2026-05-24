package memory

import (
	"sort"
	"strings"
	"time"
	"unicode"
)

// mergeSimilarityThreshold is the Jaccard word-overlap ratio above which
// two trait texts are considered "the same" for merging purposes. 0.55 means
// 55% of distinct words overlap after normalization — catches rephrasings
// like "prefers explicit error handling over panic" vs "prefers explicit
// error handling" (4/6 = 0.67) while keeping genuinely distinct traits
// separate like "low evidence trait" vs "high evidence trait" (2/4 = 0.5).
const mergeSimilarityThreshold = 0.55

// maxPendingTokens is a generous cap on the Pending section after merge.
// Pending is never injected into the prompt (only Stable is), but this
// prevents unbounded disk growth after many dream passes.
const maxPendingTokens = 2000

// MergeIntoL merges incoming dream candidates into the existing Pending
// markdown section. Similar traits (Levenshtein ratio ≥ mergeSimilarityThreshold)
// are consolidated: sources are combined, evidence is extended, and the
// existing trait text is preserved (safe for manual edits).
//
// Returns the merged Pending markdown, truncated to maxPendingTokens at
// bullet boundaries if necessary.
func MergeIntoL(existingMarkdown string, incoming []LCandidate) string {
	if len(incoming) == 0 {
		return existingMarkdown
	}

	existing := parseLMarkdown(existingMarkdown)

	// Merge incoming into existing, one by one.
	for _, inc := range incoming {
		matched := false
		for i := range existing {
			if traitSimilarity(existing[i].Trait, inc.Trait) >= mergeSimilarityThreshold {
				// Merge sources — union, dedup, sort.
				existing[i].Sources = mergeSources(existing[i].Sources, inc.Sources)

				// Extend why if the incoming brings new information.
				existing[i].Why = mergeWhy(existing[i].Why, inc.Why)

				matched = true
				break
			}
		}
		if !matched {
			existing = append(existing, inc)
		}
	}

	// Sort deterministically: most-sourced first, then longest trait,
	// then alphabetical.
	sort.SliceStable(existing, func(i, j int) bool {
		// More sources = higher priority
		if len(existing[i].Sources) != len(existing[j].Sources) {
			return len(existing[i].Sources) > len(existing[j].Sources)
		}
		// Longer trait = more specific = higher priority
		if len(existing[i].Trait) != len(existing[j].Trait) {
			return len(existing[i].Trait) > len(existing[j].Trait)
		}
		return existing[i].Trait < existing[j].Trait
	})

	rendered := FormatLCandidatesMarkdown(existing)

	// Cap at maxPendingTokens using the same bullet-boundary truncation
	// that hook.go uses for Stable injection.
	if estimateTokens(rendered) > maxPendingTokens {
		rendered = truncateSoulStable(rendered)
	}

	return rendered
}

// parseLMarkdown reverses FormatLCandidatesMarkdown. It handles the strict
// format produced by that function — it is NOT a general markdown parser.
//
// Expected format per candidate:
//
//   - **trait text**
//   - 来源 / why: evidence
//   - sources: proj-a, proj-b
//
// The 来源 / why and sources lines are optional.
func parseLMarkdown(s string) []LCandidate {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var out []LCandidate
	var current *LCandidate
	lines := strings.Split(s, "\n")

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		// Main bullet: "- **Trait**"
		if strings.HasPrefix(line, "- **") && strings.Contains(line, "**") {
			if current != nil {
				out = append(out, *current)
			}
			trait := extractBoldText(line)
			current = &LCandidate{Trait: trait}
			continue
		}

		if current == nil {
			continue
		}

		// Indented sub-bullet: "- 来源 / why: ..."
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "来源") {
			if _, after, ok := strings.Cut(line, ":"); ok {
				current.Why = strings.TrimSpace(after)
			}
			continue
		}

		// Indented sub-bullet: "- sources: ..."
		if strings.HasPrefix(line, "- ") && strings.HasPrefix(strings.TrimLeft(line, "- "), "sources:") {
			if _, after, ok := strings.Cut(line, ":"); ok {
				for _, s := range strings.Split(after, ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						current.Sources = append(current.Sources, s)
					}
				}
			}
			continue
		}

		// Indented sub-bullet: "- 首次观察：<date>" (M5.10)
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "首次观察") {
			if _, after, ok := strings.Cut(line, "："); ok {
				if t, err := time.Parse("2006-01-02", strings.TrimSpace(after)); err == nil {
					current.FirstSeen = t
				}
			}
			continue
		}

		// Indented sub-bullet: "- 最近确认：<date>" (M5.10)
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "最近确认") {
			if _, after, ok := strings.Cut(line, "："); ok {
				if t, err := time.Parse("2006-01-02", strings.TrimSpace(after)); err == nil {
					current.LastSeen = t
				}
			}
			continue
		}
	}

	if current != nil {
		out = append(out, *current)
	}

	return out
}

// extractBoldText returns the text between the first pair of ** markers,
// or after the leading bullet marker if no ** is found.
func extractBoldText(s string) string {
	s = strings.TrimSpace(s)
	// Strip bullet prefix if present.
	if len(s) >= 2 && (s[0] == '-' || s[0] == '*' || s[0] == '+') && s[1] == ' ' {
		s = strings.TrimSpace(s[2:])
	}

	start := strings.Index(s, "**")
	if start < 0 {
		return s
	}
	start += 2
	end := strings.Index(s[start:], "**")
	if end < 0 {
		return strings.TrimSpace(s[start:])
	}
	return strings.TrimSpace(s[start : start+end])
}

// normalizeTrait prepares a trait string for comparison: lowercase, trim,
// collapse internal whitespace.
func normalizeTrait(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)

	// Collapse whitespace runs into single spaces.
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// levenshtein returns the edit distance between two strings (as rune sequences).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	m, n := len(ra), len(rb)

	// Use the shorter string as the column dimension for memory efficiency.
	if m < n {
		ra, rb = rb, ra
		m, n = n, m
	}

	prev := make([]int, n+1)
	curr := make([]int, n+1)

	for j := 0; j <= n; j++ {
		prev[j] = j
	}

	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 0
			if ra[i-1] != rb[j-1] {
				cost = 1
			}
			curr[j] = min3(
				prev[j]+1,      // delete
				curr[j-1]+1,    // insert
				prev[j-1]+cost, // substitute
			)
		}
		prev, curr = curr, prev
	}

	return prev[n]
}

// traitSimilarity returns the similarity between two trait texts.
//
// For multi-word traits (both sides ≥2 words after splitting on spaces),
// it uses Jaccard word-overlap ratio — catches rephrasings while keeping
// genuinely distinct traits separate.
//
// For single-word or CJK text (which doesn't use spaces between words),
// it falls back to character-level Levenshtein ratio: CJK characters
// carry semantic weight individually, so character edit distance better
// captures similarity (e.g. "中文偏好" vs "中文习惯" share 2/4 chars).
func traitSimilarity(a, b string) float64 {
	na, nb := normalizeTrait(a), normalizeTrait(b)
	wordsA := words(na)
	wordsB := words(nb)

	if len(wordsA) == 0 && len(wordsB) == 0 {
		return 1.0
	}
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0.0
	}

	// Multi-word: use Jaccard word overlap.
	if len(wordsA) >= 2 && len(wordsB) >= 2 {
		setB := make(map[string]struct{}, len(wordsB))
		for _, w := range wordsB {
			setB[w] = struct{}{}
		}
		common := 0
		for _, w := range wordsA {
			if _, ok := setB[w]; ok {
				common++
			}
		}
		union := len(wordsA) + len(wordsB) - common
		if union == 0 {
			return 1.0
		}
		return float64(common) / float64(union)
	}

	// Single-word or CJK: use character-level Levenshtein ratio.
	maxLen := len([]rune(na))
	if l := len([]rune(nb)); l > maxLen {
		maxLen = l
	}
	if maxLen == 0 {
		return 1.0
	}
	dist := levenshtein(na, nb)
	return 1.0 - float64(dist)/float64(maxLen)
}

// words splits a string into space-separated tokens, filtering empties.
func words(s string) []string {
	var out []string
	for _, tok := range strings.Fields(s) {
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

// mergeSources returns the sorted union of two source slices.
func mergeSources(a, b []string) []string {
	seen := map[string]struct{}{}
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// mergeWhy appends newWhy to existingWhy if newWhy is non-empty and not
// already a substring of existingWhy.
func mergeWhy(existing, newWhy string) string {
	if newWhy == "" {
		return existing
	}
	if existing == "" {
		return newWhy
	}
	if strings.Contains(existing, newWhy) {
		return existing
	}
	return existing + "; " + newWhy
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
