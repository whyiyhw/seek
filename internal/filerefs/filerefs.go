// filerefs — detect document-file references (PDF/Word/PPT) in user
// input text, for the DeepSeek file-input pipeline (feature-deepseek-
// app-api §3, card F2).
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

// Detect returns, in order and de-duplicated, the tokens in text that
// reference an existing regular file with a document extension. A
// leading "@" is stripped so "@report.pdf" works.
func Detect(text string) []string {
	seen := map[string]bool{}
	var out []string
	for tok := range strings.FieldsSeq(text) {
		p := cleanToken(tok)
		if p == "" || seen[p] {
			continue
		}
		if !fileExts[strings.ToLower(filepath.Ext(p))] {
			continue
		}
		if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
			continue // not a real file → not a document reference
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
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
