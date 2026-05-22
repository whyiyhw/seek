// Package diff computes unified diffs between two text strings.
// Used by the edit tool to preview changes before (and report them after)
// applying an exact-substring replacement.
//
// The implementation is a classic LCS-based Myers diff over lines,
// grouped into hunks with configurable context. No external dependencies.
package diff

import (
	"fmt"
	"strings"
)

const (
	DefaultContext = 3  // lines of context around each change
	MaxHunks       = 8  // cap: avoid flooding the TUI / tool result
)

// Unified returns a unified-diff string comparing oldContent to newContent.
// filename is used only in the --- / +++ header lines.
// Returns "" if the two strings are identical.
func Unified(oldContent, newContent, filename string) string {
	return UnifiedContext(oldContent, newContent, filename, DefaultContext)
}

// UnifiedContext is like Unified but lets the caller choose context depth.
func UnifiedContext(oldContent, newContent, filename string, ctx int) string {
	if oldContent == newContent {
		return ""
	}

	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	ops := lcsEdits(oldLines, newLines)
	hunks := makeHunks(ops, len(oldLines), len(newLines), ctx)

	if len(hunks) == 0 {
		return ""
	}
	if len(hunks) > MaxHunks {
		hunks = hunks[:MaxHunks]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n", filename)
	fmt.Fprintf(&sb, "+++ b/%s\n", filename)
	for _, h := range hunks {
		sb.WriteString(h.format())
	}
	return sb.String()
}

// ---- internal types -------------------------------------------------------

type opKind byte

const (
	opEqual  opKind = ' '
	opDelete opKind = '-'
	opInsert opKind = '+'
)

type op struct {
	kind    opKind
	oldLine int // 1-based; valid for opEqual and opDelete
	newLine int // 1-based; valid for opEqual and opInsert
	text    string
}

type hunk struct {
	oldStart, oldCount int
	newStart, newCount int
	ops                []op
}

func (h hunk) format() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n",
		h.oldStart, h.oldCount, h.newStart, h.newCount)
	for _, o := range h.ops {
		fmt.Fprintf(&sb, "%c%s\n", o.kind, o.text)
	}
	return sb.String()
}

// ---- LCS diff -------------------------------------------------------------

// lcsEdits computes the edit script between oldLines and newLines using a
// classic O(n²) LCS DP table. Fast enough for the file sizes seek works with.
func lcsEdits(oldLines, newLines []string) []op {
	m, n := len(oldLines), len(newLines)

	// dp[i][j] = length of LCS(old[:i], new[:j])
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if oldLines[i-1] == newLines[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack to build the edit sequence.
	ops := make([]op, 0, m+n)
	i, j := m, n
	for i > 0 || j > 0 {
		switch {
		case i > 0 && j > 0 && oldLines[i-1] == newLines[j-1]:
			ops = append(ops, op{opEqual, i, j, oldLines[i-1]})
			i--
			j--
		case j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]):
			ops = append(ops, op{opInsert, 0, j, newLines[j-1]})
			j--
		default:
			ops = append(ops, op{opDelete, i, 0, oldLines[i-1]})
			i--
		}
	}

	// Reverse: backtracking produced the list in reverse order.
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}

// ---- Hunk grouping --------------------------------------------------------

// makeHunks groups ops into hunks. Each hunk is a contiguous window of
// changed lines padded by ctx context lines on each side. Adjacent windows
// that would overlap are merged.
func makeHunks(ops []op, _, _ int, ctx int) []hunk {
	// Identify indices of changed ops.
	var changed []int
	for i, o := range ops {
		if o.kind != opEqual {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}

	// Build windows [lo, hi) of op indices to include in each hunk,
	// merging overlapping windows.
	type window struct{ lo, hi int }
	var wins []window
	for _, idx := range changed {
		lo := max0(0, idx-ctx)
		hi := min0(len(ops), idx+ctx+1)
		if len(wins) > 0 && lo <= wins[len(wins)-1].hi {
			wins[len(wins)-1].hi = max0(wins[len(wins)-1].hi, hi)
		} else {
			wins = append(wins, window{lo, hi})
		}
	}

	hunks := make([]hunk, 0, len(wins))
	for _, w := range wins {
		slice := ops[w.lo:w.hi]

		// Compute old/new start line numbers from the first op in the window.
		oldStart, newStart := 1, 1
		for _, o := range ops[:w.lo] {
			if o.kind == opEqual || o.kind == opDelete {
				oldStart++
			}
			if o.kind == opEqual || o.kind == opInsert {
				newStart++
			}
		}

		oldCount, newCount := 0, 0
		for _, o := range slice {
			if o.kind == opEqual || o.kind == opDelete {
				oldCount++
			}
			if o.kind == opEqual || o.kind == opInsert {
				newCount++
			}
		}

		hunks = append(hunks, hunk{
			oldStart: oldStart, oldCount: oldCount,
			newStart: newStart, newCount: newCount,
			ops:      slice,
		})
	}
	return hunks
}

// ---- helpers ---------------------------------------------------------------

// splitLines splits s into lines, preserving empty trailing lines that
// result from a trailing newline (which is normal for source files).
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// strings.Split("a\nb\n", "\n") → ["a","b",""] — the trailing ""
	// represents the newline at EOF, not an extra blank line. Remove it.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func max0(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min0(a, b int) int {
	if a < b {
		return a
	}
	return b
}
