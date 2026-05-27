// Package edit implements the `edit` tool: exact-substring replacement in
// a file. The Claude Code / pi convention — old_string must match
// uniquely (or expected_replacements times exactly), new_string=""
// deletes. FIM lives in the separate fim_complete tool, not here.
//
// Matching has two tiers, both transparent to the caller:
//  1. Exact byte match (preserves file bytes 1:1).
//  2. Unicode NFC fallback — when (1) misses, retry after running both
//     sides through NFC normalisation. Smaller LLMs frequently produce
//     visually-identical but byte-different sequences for combining
//     characters / pre-composed forms; this lets them succeed without
//     a re-read loop. When the fallback fires the file is rewritten in
//     NFC form and the result message says so.
//
// When both tiers miss, the error embeds the closest line-window in
// the file as a hint, so the model can re-align without another grep+read.
package edit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/whyiyhw/seek/internal/diff"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/tools"
)

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "path":                  {"type": "string", "description": "Path to the file to edit."},
    "old_string":            {"type": "string", "description": "Exact substring to replace. Must include enough surrounding context to be unique unless expected_replacements is set."},
    "new_string":            {"type": "string", "description": "Replacement text. Empty string deletes the match."},
    "expected_replacements": {"type": "integer", "description": "Number of matches expected. Defaults to 1. If the actual count differs the edit fails atomically.", "minimum": 1}
  },
  "required": ["path", "old_string", "new_string"],
  "additionalProperties": false
}`)

const description = "Replace exact substring(s) in a file. old_string must appear in the file the exact number of times given by expected_replacements (default 1). new_string=\"\" deletes the match. Edits outside the working directory are refused unless seek was started with --yolo."

type Args struct {
	Path                 string `json:"path"`
	OldString            string `json:"old_string"`
	NewString            string `json:"new_string"`
	ExpectedReplacements int    `json:"expected_replacements,omitempty"`
}

// Snapshotter is the optional dependency for file checkpoint (v3
// feature-checkpoint). Mirrors write.Snapshotter; we duplicate the
// interface name rather than importing internal/tools/write so the
// two tools stay independent.
type Snapshotter interface {
	SnapshotFile(path, toolName, callID string) error
	FinaliseSnapshot(path string, after []byte) error
}

type Tool struct {
	policy *permission.Policy
	snap   Snapshotter
}

func New(p *permission.Policy) Tool { return Tool{policy: p} }

// WithSnapshotter returns a copy of t bound to s. Optional — leaving
// the snapshotter unset (nil) disables file checkpoint integration.
func (t Tool) WithSnapshotter(s Snapshotter) Tool {
	t.snap = s
	return t
}

func (Tool) Name() string            { return "edit" }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

func (t Tool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a Args
	if err := tools.UnmarshalStrict("edit", raw, &a, "path", "old_string", "new_string", "expected_replacements"); err != nil {
		return "", err
	}
	if a.Path == "" {
		return "", tools.MissingField("edit", "path", raw, "path", "old_string", "new_string", "expected_replacements")
	}
	if a.OldString == "" {
		return "", tools.MissingField("edit", "old_string (must be non-empty)", raw, "path", "old_string", "new_string", "expected_replacements")
	}
	if a.OldString == a.NewString {
		return "", fmt.Errorf("edit: old_string equals new_string — no-op")
	}
	if a.ExpectedReplacements <= 0 {
		a.ExpectedReplacements = 1
	}

	clean := filepath.Clean(a.Path)
	orig, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	content := string(orig)

	// Tier 1: exact byte match.
	exactCount := strings.Count(content, a.OldString)
	var (
		updated  string
		gotCount int
		fallback string // empty unless a fallback tier fired
	)
	switch {
	case exactCount == a.ExpectedReplacements:
		updated = strings.ReplaceAll(content, a.OldString, a.NewString)
		gotCount = exactCount
	default:
		// Tier 2: NFC normalisation. NFC-normalise the file, the needle,
		// and the replacement, then match in NFC space. If the count is
		// right we proceed with NFC content as the new file body.
		nfcContent := norm.NFC.String(content)
		nfcOld := norm.NFC.String(a.OldString)
		nfcCount := strings.Count(nfcContent, nfcOld)
		if nfcCount == a.ExpectedReplacements {
			nfcNew := norm.NFC.String(a.NewString)
			updated = strings.ReplaceAll(nfcContent, nfcOld, nfcNew)
			gotCount = nfcCount
			fallback = "nfc"
		} else {
			return "", noMatchError(content, a.OldString, a.ExpectedReplacements, exactCount, nfcCount)
		}
	}

	// Compute unified diff BEFORE writing so the approval prompt can show
	// exactly what will change. The diff is also included in the tool result
	// so the model gets a structured summary of the change it just made.
	udiff := diff.Unified(content, updated, filepath.Base(clean))

	if err := t.policy.Check(permission.Action{
		Kind:    permission.KindEdit,
		Path:    a.Path,
		Display: permission.Display{Diff: udiff},
	}); err != nil {
		return "", err
	}

	// Preserve the original file's permission bits so +x on scripts,
	// special group perms, etc. survive the rewrite. New files get 0o644.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(clean); err == nil {
		mode = fi.Mode().Perm()
	}
	// File checkpoint: snapshot prior content (the byte sequence we
	// already have in `orig`) before mutating. Snapshotter handles
	// re-reading internally to keep the API uniform with write; the
	// duplicate read is one-shot and bounded by the tool's existing
	// file-size limits.
	if t.snap != nil {
		_ = t.snap.SnapshotFile(clean, "edit", "")
	}
	if err := os.WriteFile(clean, []byte(updated), mode); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	if t.snap != nil {
		_ = t.snap.FinaliseSnapshot(clean, []byte(updated))
	}

	abs, err := filepath.Abs(clean)
	if err != nil {
		abs = clean
	}
	result := fmt.Sprintf("edited %s: %d replacement(s), %d → %d bytes",
		abs, gotCount, len(orig), len(updated))
	if fallback == "nfc" {
		result += " (matched after Unicode NFC normalisation — file rewritten in NFC form)"
	}
	if udiff != "" {
		// Wrap in a ```diff fence so the TUI's commit-line renderer can
		// detect and colourise the diff (red `-` / green `+`) under the
		// summary line. The fence is also a clearer signal to the model
		// that the trailing block is a diff and not narrative prose.
		// internal/diff.Unified caps at 8 hunks so this stays bounded.
		result += "\n```diff\n" + udiff
		if !strings.HasSuffix(udiff, "\n") {
			result += "\n"
		}
		result += "```"
	}
	return result, nil
}

// noMatchError builds the error returned when neither exact nor NFC matching
// produced the expected count. It includes the closest line-window in the file
// so the model can re-align without another round-trip.
func noMatchError(content, needle string, expected, exact, nfc int) error {
	var b strings.Builder
	if exact != expected && nfc != expected && nfc != exact {
		fmt.Fprintf(&b, "edit: expected %d replacements but old_string occurs %d times (exact) / %d times (after Unicode NFC normalisation)",
			expected, exact, nfc)
	} else {
		fmt.Fprintf(&b, "edit: expected %d replacements but old_string occurs %d times", expected, exact)
	}

	// Closest-candidate hint: only useful when we found 0 anywhere.
	if exact == 0 && nfc == 0 {
		if hint := closestCandidate(content, needle); hint != "" {
			b.WriteString("\n\n")
			b.WriteString(hint)
		} else {
			b.WriteString(" — broaden context or set expected_replacements")
		}
	} else {
		b.WriteString(" — broaden context or set expected_replacements")
	}
	return fmt.Errorf("%s", b.String())
}

// closestCandidate returns a human-readable hint pointing at the line-window
// in content that most closely matches needle. Scoring is simple: split both
// into lines, slide a window of len(needleLines) across content, count lines
// that match after TrimSpace. Returns "" if no window scores above zero
// (e.g. needle is a single short string with no near-match anywhere).
//
// The hint format is deliberately compact — a 1-line header with file
// coordinates, then the candidate's actual bytes verbatim, fenced. The model
// can copy from inside the fence and retry.
func closestCandidate(content, needle string) string {
	needleLines := splitLines(needle)
	contentLines := splitLines(content)
	n := len(needleLines)
	if n == 0 || len(contentLines) < n {
		return ""
	}

	// Pre-normalise needle lines once: collapse internal whitespace runs to a
	// single space. We compare against the same normalisation of each window
	// line. This is what catches whitespace-only differences (extra indent,
	// double space, tabs vs spaces) — the most common miss class after NFC.
	needleNorm := make([]string, n)
	for j, ln := range needleLines {
		needleNorm[j] = canonForCompare(ln)
	}

	bestScore := 0
	bestIdx := -1
	for i := 0; i+n <= len(contentLines); i++ {
		score := 0
		for j := 0; j < n; j++ {
			if canonForCompare(contentLines[i+j]) == needleNorm[j] {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	if bestIdx < 0 || bestScore == 0 {
		return ""
	}

	// Cap the candidate at 20 lines so we don't flood the tool result on
	// a long needle. The model can always grep + read for more.
	end := bestIdx + n
	const maxLines = 20
	if end-bestIdx > maxLines {
		end = bestIdx + maxLines
	}
	candidate := strings.Join(contentLines[bestIdx:end], "\n")

	// Diff the needle against the candidate so the model can SEE which bytes
	// differ. For whitespace-only or NFC/NFD differences the raw bytes look
	// identical; the unified diff makes them visible by putting the two
	// versions on adjacent `-` / `+` lines. This is the load-bearing signal —
	// without it the model just sees two "identical" blobs and has to guess.
	needleForDiff := needle
	candidateForDiff := candidate
	if !strings.HasSuffix(needleForDiff, "\n") {
		needleForDiff += "\n"
	}
	if !strings.HasSuffix(candidateForDiff, "\n") {
		candidateForDiff += "\n"
	}
	diffText := diff.Unified(needleForDiff, candidateForDiff,
		fmt.Sprintf("lines %d-%d", bestIdx+1, bestIdx+n))

	var b strings.Builder
	fmt.Fprintf(&b, "closest candidate at lines %d-%d (matched %d/%d lines after NFC + whitespace collapse):\n",
		bestIdx+1, bestIdx+n, bestScore, n)
	if diffText != "" {
		b.WriteString("```diff\n")
		b.WriteString(diffText)
		if !strings.HasSuffix(diffText, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n")
		b.WriteString("Tip: `-` lines are your old_string, `+` lines are the file. Copy the `+` lines verbatim. Likely cause: whitespace, Unicode normalisation, or invisible characters.")
	} else {
		// Defensive: diff.Unified returns "" only when the two inputs are
		// byte-identical, which shouldn't happen here (we found 0 matches).
		// Fall back to showing the candidate raw.
		b.WriteString("```\n")
		b.WriteString(candidate)
		if !strings.HasSuffix(candidate, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n")
		b.WriteString("Tip: re-read this range and copy the bytes verbatim.")
	}
	return b.String()
}

// splitLines splits s on '\n' without dropping the trailing empty entry,
// so window scoring is symmetric for needles that don't end in newline.
func splitLines(s string) []string {
	return strings.Split(s, "\n")
}

// canonForCompare normalises a string so that the closest-candidate scorer
// treats visually-equivalent inputs as equal. Three steps, in order:
//  1. NFC: collapse decomposed sequences (é vs e+◌́).
//  2. Strip Unicode "format" category (Cf) — ZWS / ZWJ / ZWNJ / BOM / etc.
//     These are zero-width by definition and almost never load-bearing in
//     code; if the model omitted one, we still want to find the candidate.
//  3. strings.Fields → re-join with single space: folds runs of whitespace
//     (including tabs and Unicode spaces) into one.
//
// Used ONLY for "is this window similar enough to surface?" scoring. The
// actual file bytes and the unified-diff output below use the raw strings,
// so the model still sees the real differences in the hint.
func canonForCompare(s string) string {
	s = norm.NFC.String(s)
	if hasFormatRune(s) {
		var b strings.Builder
		b.Grow(len(s))
		for _, r := range s {
			if unicode.In(r, unicode.Cf) {
				continue
			}
			b.WriteRune(r)
		}
		s = b.String()
	}
	return strings.Join(strings.Fields(s), " ")
}

func hasFormatRune(s string) bool {
	for _, r := range s {
		if r >= 0x80 && unicode.In(r, unicode.Cf) {
			return true
		}
	}
	return false
}
