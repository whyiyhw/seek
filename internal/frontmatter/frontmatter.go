// Package frontmatter is the shared parser for seek's Markdown-with-
// YAML-frontmatter config surfaces (SKILL.md, agents/<name>.md).
//
// It exists so every .md config file seek reads speaks ONE frontmatter
// dialect — extracted verbatim from internal/skill (the original owner,
// PRD v2 §4.1) when typed agent definitions (feature-orchestration.md
// D10) needed the exact same shapes. Before this package, the
// alternative was either exporting a generic parser from the skill
// domain package or letting each config surface grow its own dialect —
// both drift hazards; a shape that works in SKILL.md but silently fails
// in agents/<name>.md is a user-facing bug no test outside this package
// can catch.
//
// Deliberately NOT a YAML library. The supported shapes are the four
// skill files actually use (see Parse): full YAML grammar is dead
// weight here and a pull-in of gopkg.in/yaml for ~200 lines of stdlib
// string walking violates the repo's stdlib-first convention.
package frontmatter

import (
	"errors"
	"strings"
)

// ErrNoFrontmatter / ErrUnterminated are the structural failures of
// Split. Sentinel values (not fmt.Errorf) so callers can branch on
// them if they ever need to; the usual path is just wrapping with a
// domain prefix ("skill %s: %w").
var (
	ErrNoFrontmatter = errors.New("missing frontmatter (file must start with `---`)")
	ErrUnterminated  = errors.New("unterminated frontmatter (no closing `---`)")
)

// Split parses raw file bytes into frontmatter fields and the markdown
// body. The expected layout:
//
//	---
//	name: kebab-case-name
//	description: single-line summary
//	key: [a, b, "c"]
//	---
//
//	<markdown body>
//
// Returned maps: scalars (key → string) and lists (key → []string) for
// the four supported value shapes:
//
//  1. scalar:        key: value
//  2. quoted scalar: key: "value"  or  key: 'value'
//  3. inline list:   key: [a, b, "c"]
//  4. block list:    key:
//     - a
//     - b
//     block scalar:  key: |
//     line 1
//     line 2
//
// body is the everything-after-the-closing-`---` with leading blank
// lines trimmed. A UTF-8 BOM and leading blank lines before the opener
// are tolerated (files saved from Windows editors).
func Split(data []byte) (scalars map[string]string, lists map[string][]string, body string, err error) {
	// Strip a UTF-8 BOM if present so the `---` check below doesn't
	// silently miss the opener on files saved from Windows editors.
	text := strings.TrimPrefix(string(data), "\ufeff")
	// Be tolerant of leading blank lines — `---` MUST be the first
	// non-blank content though, otherwise it's not frontmatter.
	trimmed := strings.TrimLeft(text, "\n\r ")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, nil, "", ErrNoFrontmatter
	}

	// Walk lines so we can locate the closing `---` precisely.
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return nil, nil, "", errors.New("empty after frontmatter opener")
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return nil, nil, "", ErrUnterminated
	}

	scalars, lists = parseFrontmatter(lines[1:closeIdx])
	body = strings.TrimLeft(strings.Join(lines[closeIdx+1:], "\n"), "\n")
	return scalars, lists, body, nil
}

// parseFrontmatter walks frontmatter lines and returns scalar values
// (key -> string) and list values (key -> []string) for the four
// shapes recognised since PRD v2 §4.1:
//
//  1. scalar:        key: value
//  2. quoted scalar: key: "value"  or  key: 'value'
//  3. inline list:   key: [a, b, "c"]
//  4. block list:    key:
//     - a
//     - b
//     block scalar:  key: |
//     line 1
//     line 2
//
// Multi-line shapes (block list, block scalar) own their continuation
// lines as long as those lines are indented MORE than the parent key
// (or are blank). The block ends as soon as we see a non-blank line
// whose indentation drops back to the parent's column.
//
// Comments (`# ...`) and blank lines are skipped at the top level. A
// line we can't classify is silently ignored — frontmatter is
// developer-facing, and erroring on every typo would block startup.
func parseFrontmatter(lines []string) (map[string]string, map[string][]string) {
	scalars := map[string]string{}
	lists := map[string][]string{}

	for i := 0; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], "\r")
		trim := strings.TrimSpace(raw)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		// Only top-level (column 0) keys are scanned here. Indented
		// lines belong to whatever block we're currently inside, and
		// the block-consuming branches below advance `i` past them.
		if leadingSpaces(raw) > 0 {
			continue
		}
		idx := strings.Index(raw, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(raw[:idx])
		val := strings.TrimSpace(raw[idx+1:])

		// Shape 4a: block scalar `key: |`
		if val == "|" || val == "|-" {
			block, next := readBlockScalar(lines, i+1)
			scalars[key] = block
			i = next - 1
			continue
		}
		// Shape 4b: block list when value is empty and next non-blank
		// line is an indented `- item`.
		if val == "" {
			items, next, ok := readBlockList(lines, i+1)
			if ok {
				lists[key] = items
				i = next - 1
				continue
			}
			// Empty value, no block — record as empty scalar so the
			// presence of the key is at least observable.
			scalars[key] = ""
			continue
		}
		// Shape 3: inline list `[a, b, "c"]`
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			lists[key] = splitInlineList(val[1 : len(val)-1])
			continue
		}
		// Shapes 1 + 2: scalar, optionally quoted.
		scalars[key] = unquote(val)
	}
	return scalars, lists
}

// leadingSpaces counts spaces (not tabs — frontmatter convention is
// 2-space indent) at the start of the line. Used to detect whether a
// line is a top-level key or a continuation of a multi-line block.
func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

// readBlockScalar consumes lines starting at `from` that are indented
// (or blank), stripping that indent and joining with `\n`. Returns the
// joined body and the index of the first un-consumed line.
//
// We use the minimum indentation of the *first* non-blank continuation
// line as the strip amount — matches YAML literal block semantics
// well enough for frontmatter, where authors don't write
// indentation-sensitive content.
func readBlockScalar(lines []string, from int) (string, int) {
	// Find indent off the first non-blank line.
	indent := -1
	for i := from; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if leadingSpaces(lines[i]) == 0 {
			break // unindented = block ended before it began
		}
		indent = leadingSpaces(lines[i])
		break
	}
	if indent < 0 {
		return "", from
	}

	var out []string
	i := from
	for ; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(raw) == "" {
			out = append(out, "")
			continue
		}
		if leadingSpaces(raw) < indent {
			break
		}
		out = append(out, raw[indent:])
	}
	// Trim trailing blank lines — they're usually accidental in
	// frontmatter and noisy in `status` output.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n"), i
}

// readBlockList consumes "  - item" lines starting at `from`, ignoring
// blank lines. Returns the items, the index of the first un-consumed
// line, and `ok=false` if the very first non-blank line isn't a list
// item (in which case the caller treats the parent key as an empty
// scalar).
func readBlockList(lines []string, from int) ([]string, int, bool) {
	var items []string
	saw := false
	i := from
	for ; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(raw) == "" {
			if saw {
				// Permit blank lines between items, but stop if we
				// see one before any item appears (avoids consuming
				// the whole rest of the frontmatter).
				continue
			}
			continue
		}
		if leadingSpaces(raw) == 0 {
			break
		}
		t := strings.TrimSpace(raw)
		if !strings.HasPrefix(t, "- ") && t != "-" {
			if !saw {
				return nil, from, false
			}
			break
		}
		saw = true
		item := strings.TrimSpace(strings.TrimPrefix(t, "-"))
		items = append(items, unquote(item))
	}
	return items, i, saw
}

// splitInlineList splits "a, b, \"c\"" into ["a", "b", "c"]. Naïve
// comma split is fine for frontmatter — list items are short
// identifiers ("Read", "Grep") and never contain commas.
func splitInlineList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		p := unquote(strings.TrimSpace(part))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// unquote strips wrapping "double" or 'single' quotes if both ends
// match. Matches what humans write when a value contains a colon.
func unquote(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') ||
			(v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
