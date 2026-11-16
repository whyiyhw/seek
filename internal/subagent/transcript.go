package subagent

// The sub transcript's last-assistant read (W3.3, feature-web-frontend
// §3.2 "summary 单源纪律"): the index carries no summary — a surface
// that wants the subagent's findings reads the child's own
// transcript.jsonl tail. This is that reader, sibling to ListProject
// (same read-only posture, same tolerance rules as the W3.1 reader):
// line 1 is the session header, lines 2..N are deepseek.Message JSON.
//
// The file is shared with the (possibly still running) writer process
// — the lock-free line-scan posture of session.ReadMessagesPage
// applies verbatim: whole earlier lines, a torn trailing chunk is
// skipped (next read sees it complete), malformed lines are skipped
// without error (a panel must not fail on one bad line).

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// LastAssistant returns the content of the LAST assistant message in
// subSid's transcript.jsonl under projectAbsPath/sessions/<parentSid>/
// subagents/<subSid>/ — the single-source "what did this subagent
// conclude" text (orchestration D5: summary 全文已在子 transcript).
//
// ("", nil) means "no assistant entry on disk yet" — a missing
// transcript (active child, or a terminal run whose Runner never wrote
// one), a header-only file, or one whose assistant messages are all
// empty-bodied. Presentation renders a hint; only real I/O errors
// propagate.
func LastAssistant(projectAbsPath, parentSid, subSid string) (string, error) {
	dir, err := paths.SubagentSessionDir(projectAbsPath, parentSid, subSid)
	if err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // no transcript yet — an answer, not an error
		}
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 1 MiB line ceiling, the readEvents precedent: a single
	// long-assistant message must not abort the scan.
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	last := ""
	first := true // line 1 is the header, never a message
	for scanner.Scan() {
		line := scanner.Bytes()
		if first {
			first = false
			continue
		}
		if len(line) == 0 {
			continue
		}
		var m struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(line, &m) != nil {
			continue // malformed: skip, same posture as readEvents
		}
		if m.Role == deepseek.RoleAssistant && m.Content != "" {
			last = m.Content
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return last, nil
}
