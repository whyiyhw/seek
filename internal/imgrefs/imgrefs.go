// imgrefs — detect image-file references in user input text.
//
// Migrated verbatim from internal/ocr when 柱 Q was decommissioned
// (feature-vision M-V.0): the stat-gated detector is pure text+stat
// logic with zero OCR coupling, and the vision input router still
// needs it to find @-references. The stat-gate is the load-bearing
// correctness guard (柱 Q PRD §路径假阳性): a bare ".png" substring
// inside a code snippet, URL, or error message is NOT a file and must
// not trigger image attachment.
package imgrefs

import (
	"os"
	"path/filepath"
	"strings"
)

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true,
	".tiff": true, ".bmp": true, ".heic": true, ".gif": true,
}

// Detect returns, in order and de-duplicated, the tokens in text that
// reference an existing regular file with an image extension. A
// leading "@" is stripped so "@shot.png" works.
func Detect(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range strings.Fields(text) {
		p := cleanToken(tok)
		if p == "" || seen[p] {
			continue
		}
		if !imageExts[strings.ToLower(filepath.Ext(p))] {
			continue
		}
		if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
			continue // not a real file → not an image reference
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// cleanToken peels @ prefixes, surrounding quotes/backticks/brackets,
// and trailing sentence punctuation — looping until stable so nested
// wrappers like "(@shot.png)," resolve to "shot.png" regardless of
// order.
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
