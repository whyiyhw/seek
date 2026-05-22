// Package grep implements the `grep` tool: search for a pattern across a
// file, directory, or glob and return matching lines with surrounding
// context. Pairs with read(offset, limit) — grep finds the line number,
// read fetches the precise range — so the model never has to slurp an
// entire file just to locate a symbol.
package grep

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/tools"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "pattern":       {"type": "string",  "description": "Regular expression to search for. Use fixed=true for a literal string match."},
    "path":          {"type": "string",  "description": "File path, directory (searched recursively), or glob (e.g. 'internal/**/*.go'). Relative paths are resolved from the working directory."},
    "context_lines": {"type": "integer", "description": "Lines of context before and after each match. Default 3, max 10.", "minimum": 0, "maximum": 10},
    "fixed":         {"type": "boolean", "description": "Treat pattern as a literal string, not a regex. Default false."},
    "ignore_case":   {"type": "boolean", "description": "Case-insensitive matching. Default false."},
    "max_matches":   {"type": "integer", "description": "Stop after this many matches. Default 50, max 200.", "minimum": 1, "maximum": 200}
  },
  "required": ["pattern", "path"],
  "additionalProperties": false
}`)

const description = "Search for a regex (or literal string) across a file, directory, or glob. Returns matching lines with surrounding context and line numbers. Use this to locate a symbol or pattern, then follow up with read(offset, limit) to extract the precise range — avoids reading entire files into context."

// Args is the decoded argument struct.
// ContextLines is a pointer so we can distinguish "not provided" (nil → use
// default 3) from "explicitly 0" (pointer to 0 → no context lines shown).
type Args struct {
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	ContextLines *int   `json:"context_lines,omitempty"`
	Fixed        bool   `json:"fixed,omitempty"`
	IgnoreCase   bool   `json:"ignore_case,omitempty"`
	MaxMatches   int    `json:"max_matches,omitempty"`
}

const (
	defaultContextLines = 3
	defaultMaxMatches   = 50
	maxAllowedMatches   = 200
	maxFileSizeBytes    = 2 * 1024 * 1024 // 2 MiB — skip binary/generated blobs
)

// Tool is the grep tool implementation.
type Tool struct{}

func New() Tool { return Tool{} }

func (Tool) Name() string            { return "grep" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

func (Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("grep", raw, &a,
		"pattern", "path", "context_lines", "fixed", "ignore_case", "max_matches"); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		return "", tools.MissingField("grep", "pattern", raw,
			"pattern", "path", "context_lines", "fixed", "ignore_case", "max_matches")
	}
	if a.Path == "" {
		return "", tools.MissingField("grep", "path", raw,
			"pattern", "path", "context_lines", "fixed", "ignore_case", "max_matches")
	}

	contextLines := defaultContextLines
	if a.ContextLines != nil {
		contextLines = *a.ContextLines
		if contextLines < 0 {
			contextLines = 0
		}
	}
	if a.MaxMatches <= 0 {
		a.MaxMatches = defaultMaxMatches
	}
	if a.MaxMatches > maxAllowedMatches {
		a.MaxMatches = maxAllowedMatches
	}

	pat := a.Pattern
	if a.Fixed {
		pat = regexp.QuoteMeta(pat)
	}
	if a.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", fmt.Errorf("grep: invalid pattern %q: %w", a.Pattern, err)
	}

	files, err := expandPath(a.Path)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}

	var (
		results   []fileResult
		totalHits int
		capped    bool
	)

	for _, f := range files {
		if totalHits >= a.MaxMatches {
			capped = true
			break
		}
		blocks, err := searchFile(f, re, contextLines, a.MaxMatches-totalHits)
		if err != nil || len(blocks) == 0 {
			continue
		}
		totalHits += countHits(blocks)
		results = append(results, fileResult{path: f, matches: blocks})
	}

	if totalHits == 0 {
		return fmt.Sprintf("grep: %q — no matches in %s\n", a.Pattern, a.Path), nil
	}

	return formatResults(a.Pattern, results, totalHits, capped), nil
}

// fileResult holds all matched blocks for a single file.
type fileResult struct {
	path    string
	matches []matchBlock
}

// matchBlock is a run of lines that should be printed together:
// [start..end] inclusive, with matchLines marking which are actual hits.
type matchBlock struct {
	start      int // 1-based
	lines      []string
	matchLines map[int]bool // 1-based absolute line numbers that matched
}

func searchFile(path string, re *regexp.Regexp, ctx, maxHits int) ([]matchBlock, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileSizeBytes {
		return nil, nil // silently skip large / binary files
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if isBinary(data) {
		return nil, nil
	}

	fileLines := strings.Split(string(data), "\n")
	// Remove trailing empty element caused by a final newline.
	if len(fileLines) > 0 && fileLines[len(fileLines)-1] == "" {
		fileLines = fileLines[:len(fileLines)-1]
	}

	// Collect 1-based line numbers of all matches.
	var hitLines []int
	for i, line := range fileLines {
		if re.MatchString(line) {
			hitLines = append(hitLines, i+1)
			if len(hitLines) >= maxHits {
				break
			}
		}
	}
	if len(hitLines) == 0 {
		return nil, nil
	}

	// Merge nearby hits into contiguous blocks to avoid redundant context.
	type interval struct{ lo, hi int }
	var intervals []interval
	for _, ln := range hitLines {
		lo := max1(1, ln-ctx)
		hi := min1(len(fileLines), ln+ctx)
		if len(intervals) > 0 && lo <= intervals[len(intervals)-1].hi+1 {
			intervals[len(intervals)-1].hi = max1(intervals[len(intervals)-1].hi, hi)
		} else {
			intervals = append(intervals, interval{lo, hi})
		}
	}

	hitSet := make(map[int]bool, len(hitLines))
	for _, ln := range hitLines {
		hitSet[ln] = true
	}

	var blocks []matchBlock
	for _, iv := range intervals {
		b := matchBlock{
			start:      iv.lo,
			lines:      fileLines[iv.lo-1 : iv.hi],
			matchLines: hitSet,
		}
		blocks = append(blocks, b)
	}
	return blocks, nil
}

func countHits(blocks []matchBlock) int {
	seen := make(map[int]bool)
	for _, b := range blocks {
		for ln := range b.matchLines {
			seen[ln] = true
		}
	}
	return len(seen)
}

func formatResults(pattern string, results []fileResult, total int, capped bool) string {
	var sb strings.Builder

	fileWord := "file"
	if len(results) != 1 {
		fileWord = "files"
	}
	capNote := ""
	if capped {
		capNote = " (capped — refine the pattern or path to see more)"
	}
	fmt.Fprintf(&sb, "grep: %q — %d match(es) across %d %s%s\n",
		pattern, total, len(results), fileWord, capNote)

	for _, r := range results {
		fmt.Fprintf(&sb, "\n%s\n", r.path)
		prevEnd := -1
		for _, b := range r.matches {
			if prevEnd >= 0 && b.start > prevEnd+1 {
				sb.WriteString("  ---\n")
			}
			for i, line := range b.lines {
				absLine := b.start + i
				if b.matchLines[absLine] {
					fmt.Fprintf(&sb, "> %5d  %s\n", absLine, line)
				} else {
					fmt.Fprintf(&sb, "  %5d  %s\n", absLine, line)
				}
			}
			prevEnd = b.start + len(b.lines) - 1
		}
	}
	return sb.String()
}

// expandPath resolves a path string into a sorted list of regular files.
// Supports: exact file, directory (recursive walk), and globs including **.
func expandPath(raw string) ([]string, error) {
	clean := filepath.Clean(raw)

	info, err := os.Stat(clean)
	if err == nil && info.IsDir() {
		return walkDir(clean)
	}
	if err == nil {
		return []string{clean}, nil
	}

	// Not found as-is — treat as glob.
	if !containsGlob(raw) {
		return nil, fmt.Errorf("no such file or directory: %s", raw)
	}

	if strings.Contains(raw, "**") {
		return expandDoubleStarGlob(raw)
	}

	matches, err := filepath.Glob(clean)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
			files = append(files, m)
		}
	}
	sort.Strings(files)
	return files, nil
}

func walkDir(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

// expandDoubleStarGlob handles patterns like "internal/**/*.go".
// It splits on the first ** and walks from the literal prefix, matching
// the suffix glob against each candidate path.
func expandDoubleStarGlob(pattern string) ([]string, error) {
	idx := strings.Index(pattern, "**")
	prefix := filepath.Clean(pattern[:idx])
	suffix := pattern[idx+2:]
	if suffix != "" && suffix[0] == '/' || suffix[0] == filepath.Separator {
		suffix = suffix[1:]
	}

	var files []string
	err := filepath.WalkDir(prefix, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if suffix == "" {
			files = append(files, path)
			return nil
		}
		// Match the trailing suffix glob against the file's name or relative path.
		rel, _ := filepath.Rel(prefix, path)
		if matched, _ := filepath.Match(suffix, filepath.Base(rel)); matched {
			files = append(files, path)
		} else if matched, _ := filepath.Match(suffix, rel); matched {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func containsGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

func isBinary(data []byte) bool {
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	return bytes.IndexByte(check, 0) >= 0
}

func max1(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min1(a, b int) int {
	if a < b {
		return a
	}
	return b
}
