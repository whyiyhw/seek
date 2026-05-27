package suggester

import (
	"strings"
	"unicode"
)

// matchPrefixRunes is the rune-prefix length used by normalizedMatch.
// Tuned empirically:
//
//   - 3 catches typical "用 A 方案" ≈ "好的，用 A" cases via the shared
//     "用 A" prefix once normalize() strips the comma & framing words
//     don't form a common N-rune sequence
//   - much shorter (1-2) starts false-positiving on common framing
//     words ("用 ", "the ")
//   - much longer (5+) starts false-negativing on terse replies
//     ("好 A" vs "用 A 方案")
//
// This is a heuristic, not a guarantee. Edge cases (very terse
// "好 A" vs verbose "用 A 方案 谢谢") may false-negative; PRD §8
// accepts this as L1 noise.
const matchPrefixRunes = 3

// normalizedMatch reports whether `actual` (the user's actual next
// message) is plausibly the response the predictor was trying to
// match (`predicted`). The check is intentionally generous — false
// positives (claim match when user diverged slightly) are cheaper
// than false negatives (inject confusing calibration when the user
// did exactly what was predicted).
//
// Algorithm:
//  1. Normalise both: lowercase, strip ASCII punctuation, collapse whitespace
//  2. Bail false if either is empty
//  3. Identical-after-normalise → match (fast path)
//  4. Bidirectional rune-prefix contains:
//     - normalised actual contains first matchPrefixRunes runes of normalised predicted → match
//     - normalised predicted contains first matchPrefixRunes runes of normalised actual → match
//
// The bidirectional check handles both "user is more verbose than
// prediction" (predicted="用 A"，actual="好的，用 A 方案") AND the
// inverse ("predicted="用 A 方案"，actual="好的，用 A").
//
// PRD docs/prd/feature-suggested-reply.md §4.6.
func normalizedMatch(actual, predicted string) bool {
	a := normalize(actual)
	p := normalize(predicted)
	if a == "" || p == "" {
		return false
	}
	if a == p {
		return true
	}
	if pPrefix := runePrefix(p, matchPrefixRunes); pPrefix != "" && strings.Contains(a, pPrefix) {
		return true
	}
	if aPrefix := runePrefix(a, matchPrefixRunes); aPrefix != "" && strings.Contains(p, aPrefix) {
		return true
	}
	return false
}

// runePrefix returns the first n runes of s as a string. UTF-8-safe;
// byte-based s[:n] would slice mid-codepoint on CJK input.
func runePrefix(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// normalize lowercases, strips ASCII punctuation, and collapses
// whitespace runs to a single space. Leaves non-ASCII characters
// (Chinese, etc.) intact since they often carry the actual content
// signal in seek's user base.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // suppress leading space
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsSpace(r):
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
		case r < 128 && unicode.IsPunct(r):
			// Drop ASCII punctuation entirely (cheaper than mapping
			// to space + collapsing). Non-ASCII punctuation is kept
			// because it carries meaning in CJK contexts.
			continue
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimRight(b.String(), " ")
}

