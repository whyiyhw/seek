// filerefs — detect document-file references (PDF/Word/PPT) in user
// input text, for the DeepSeek file-input pipeline (feature-deepseek-
// app-api §3, card F2; purpose-bearing DetectRefs joined in F3).
//
// Structured after internal/imgrefs (same Detect shape, same stat
// gate, cleanToken copied verbatim). cleanToken is deliberately
// DUPLICATED, not shared: the PRD (F2, D-F2) ruled that the two
// detection semantics have already diverged and the packages stay
// independent — do not refactor this back into a shared helper.
//
// The stat-gate is the load-bearing correctness guard, same as
// imgrefs: a bare ".pdf" substring inside a code snippet, URL, or
// error message is NOT a file and must not trigger an upload.
package filerefs

import (
	"os"
	"path/filepath"
	"strings"
)

// fileExts — the v1 extension table (D-F2): PDF/Word/PPT only.
// txt/md/csv/json/code go through the read tool instead (free, no
// upload round-trip); XLSX/EPUB are out as low-frequency. Adding a
// format later = adding one line here.
var fileExts = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true,
	".ppt": true, ".pptx": true,
}

// richPrefix is the per-file purpose upgrade syntax (D-F1):
// "@rich:report.pdf" uploads with rich_layout_recognition instead of
// the text_recognition default. Per-file, not config — it is a
// property of one reference, and one message may mix both purposes.
const richPrefix = "rich:"

// Ref is one detected document reference, carrying the purpose the
// user asked for (F3's routing input).
type Ref struct {
	// Path is the stat-verified file path.
	Path string
	// Rich reports the "@rich:" prefix (rich_layout_recognition —
	// figures at extra input tokens, same parse price).
	Rich bool
}

// DetectRefs returns, in order and de-duplicated by path, the document
// references in text. First occurrence wins on the Rich flag: a file
// referenced both plain and rich in one message routes at the first
// form the user wrote.
func DetectRefs(text string) []Ref {
	seen := map[string]bool{}
	var out []Ref
	for tok := range strings.FieldsSeq(text) {
		p := cleanToken(tok)
		if p == "" {
			continue
		}
		rich := false
		// "rich:" binds only when what follows is a real document —
		// a file literally NAMED "rich:something.pdf" keeps working via the
		// stat fallback below.
		if rest, ok := strings.CutPrefix(p, richPrefix); ok {
			if isDoc(rest) {
				p, rich = rest, true
			}
		}
		if seen[p] || !isDoc(p) {
			continue
		}
		seen[p] = true
		out = append(out, Ref{Path: p, Rich: rich})
	}
	return out
}

// Detect returns, in order and de-duplicated, the tokens in text that
// reference an existing regular file with a document extension. A
// leading "@" is stripped so "@report.pdf" works. The F2 surface,
// kept for callers that don't care about purpose; purpose-aware
// callers use DetectRefs.
func Detect(text string) []string {
	refs := DetectRefs(text)
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Path)
	}
	return out
}

// isDoc reports whether p has a table extension AND stats as an
// existing regular file.
func isDoc(p string) bool {
	if !fileExts[strings.ToLower(filepath.Ext(p))] {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// cleanToken peels @ prefixes, surrounding quotes/backticks/brackets,
// and trailing sentence punctuation — looping until stable so nested
// wrappers like "(@report.pdf)," resolve to "report.pdf" regardless
// of order. Copied from internal/imgrefs; kept in sync by hand.
func cleanToken(tok string) string {
	for {
		prev := tok
		tok = strings.Trim(tok, "\"'`()[]<>")
		tok = strings.TrimRight(tok, ".,;:!?")
		tok = strings.TrimPrefix(tok, "@")
		if tok == prev {
			return tok
		}
	}
}
