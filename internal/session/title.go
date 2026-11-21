// Session titling — deriving a one-line label for a stored transcript
// (headers carry no title field). Extracted from internal/rpc/methods
// (2026-11-21, c_chzxgg) when ACP's session/list needed the same
// derivation: the label is a fact about the transcript, so its single
// source lives with the store that reads transcripts — two surfaces
// (serve's /rpc session/list, ACP's session/list) must not grow two
// title styles.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

const (
	// TitleMaxRunes caps a derived title (the serve-side rename
	// validation reuses the same cap so a rename can't outgrow the
	// derived form it overrides).
	TitleMaxRunes = 80
	// firstUserScanLines bounds the label scan: real sessions open with
	// the user's prompt; one that does not is not worth a whole-file
	// read for a label.
	firstUserScanLines = 32
)

// FirstUserTitle flattens a session's first user message into a
// one-line label. Best-effort by design: a missing file, a torn tail
// (concurrent append) or a corrupt line ends the scan quietly — a list
// view must never fail because one file is mid-write.
func FirstUserTitle(dir, id string) string {
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 16<<10)
	first := true
	for scanned := 0; ; {
		line, rerr := r.ReadBytes('\n')
		if first {
			first = false
			if rerr != nil {
				return ""
			}
			continue
		}
		trimmed := bytes.TrimSuffix(line, []byte{'\n'})
		if len(trimmed) == 0 {
			if rerr != nil {
				return ""
			}
			continue
		}
		if !json.Valid(trimmed) {
			return "" // torn or corrupt: no label rather than a failed list
		}
		scanned++
		var m struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(trimmed, &m); err != nil {
			return ""
		}
		if m.Role == deepseek.RoleUser {
			return oneLineTitle(m.Content)
		}
		if rerr != nil || scanned == firstUserScanLines {
			return ""
		}
	}
}

// oneLineTitle flattens content to its first line, trimmed and capped
// at TitleMaxRunes with an ellipsis.
func oneLineTitle(content string) string {
	if i := strings.IndexByte(content, '\n'); i >= 0 {
		content = content[:i]
	}
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) <= TitleMaxRunes {
		return content
	}
	runes := []rune(content)
	return string(runes[:TitleMaxRunes]) + "…"
}
