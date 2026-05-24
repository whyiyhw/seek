package memory

import (
	"strings"
	"unicode"
)

// maxSoulTokens is the approximate token budget for L-layer injection.
// PRD v1 §4 specifies ~500 tokens; we target 450 to leave a 50-token
// safety margin for the <context> wrapper and estimation error.
const maxSoulTokens = 450

// maxMIndexTokens is the token budget for the M-index injected by
// PrePromptHook. 1500 tokens is generous enough to hold ~75–100 taglines
// (the expected ceiling for a single project) while bounding worst-case
// injection. When the index exceeds this budget, low-score entries are
// dropped first.
const maxMIndexTokens = 1500

// estimateTokens returns a rough token count for a string.
// DeepSeek has no public tokenizer; this is a CJK-aware character
// approximation: ASCII/Latin ~4 chars/token, CJK ~3 chars/token
// (roughly 1.5 tokens/char for CJK in DeepSeek's sentencepiece
// vocabulary, per empirical testing).
func estimateTokens(s string) int {
	var ascii, cjk int
	for _, r := range s {
		if r > unicode.MaxLatin1 {
			// Non-Latin-1 — includes CJK, emoji, etc.
			cjk++
		} else {
			ascii++
		}
	}
	return ascii/4 + cjk/3
}

// truncateSoulStable limits a markdown section body to ~maxSoulTokens
// by dropping trailing bullet entries. It preserves well-formed output:
// each entry (bullet + its indented continuation lines) is kept or
// dropped as a unit. Non-bullet preamble text before the first bullet
// is always kept.
//
// Returns the original string unchanged if already within budget.
func truncateSoulStable(s string) string {
	if estimateTokens(s) <= maxSoulTokens {
		return s
	}

	type block struct {
		lines []string
	}

	// Split into blocks: each bullet line (starts with "- ", "* ", "+ ")
	// begins a new block; everything before the first bullet is preamble.
	//
	// Block lines include the bullet line itself and any subsequent
	// non-bullet continuation lines (indented sub-bullets, blank lines
	// between entries, etc.).
	lines := strings.Split(s, "\n")
	var preamble []string
	var blocks []block

	// Collect preamble up to the first bullet marker.
	i := 0
	for ; i < len(lines); i++ {
		if isBulletLine(lines[i]) {
			break
		}
		preamble = append(preamble, lines[i])
	}

	// Group remaining lines into bullet-led blocks.
	var current block
	for ; i < len(lines); i++ {
		if isBulletLine(lines[i]) {
			if len(current.lines) > 0 {
				blocks = append(blocks, current)
			}
			current = block{lines: []string{lines[i]}}
		} else if current.lines != nil {
			current.lines = append(current.lines, lines[i])
		}
		// Lines after the last bullet with no current block are dropped.
	}
	if current.lines != nil {
		blocks = append(blocks, current)
	}

	// Rebuild preamble.
	var result strings.Builder
	result.WriteString(strings.Join(preamble, "\n"))
	if len(preamble) > 0 && preamble[len(preamble)-1] != "" {
		result.WriteString("\n")
	}
	// Append blocks in order while staying within budget.
	// Reserve room for the truncation notice so adding it doesn't push
	// the final output over the limit.
	const truncNoticeBudget = 15 // "… *(truncated — …)*\n" ≈ 60 ASCII chars / 4
	for _, b := range blocks {
		prefix := ""
		if result.Len() > 0 && result.String()[result.Len()-1] != '\n' {
			prefix = "\n"
		}
		candidate := result.String() + prefix + strings.Join(b.lines, "\n")

		if estimateTokens(candidate) > maxSoulTokens-truncNoticeBudget {
			if result.Len() > 0 && result.String()[result.Len()-1] != '\n' {
				result.WriteString("\n")
			}
			result.WriteString("… *(truncated — soul.md exceeds token budget)*\n")
			break
		}

		result.Reset()
		result.WriteString(candidate)
	}

	return strings.TrimRight(result.String(), "\n")
}

// isBulletLine returns true if the line starts a markdown bullet entry
// (dash, asterisk, or plus followed by a space).
func isBulletLine(line string) bool {
	if len(line) < 2 {
		return false
	}
	return (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' '
}
