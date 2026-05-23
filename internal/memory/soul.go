package memory

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// Soul is the parsed view of ~/.seek/soul.md (the L layer).
//
// For v1 only the section bodies are exposed — Stable goes into the
// PrePromptHook injection, Pending is read by `seek -dream` to find
// candidates due for promotion. The body of each section is opaque
// markdown by design: the dream process writes structured bullets but
// the model reads them as prose, so there's no value in re-parsing.
//
// Raw holds the full file contents so Save() round-trips bytes that
// sit outside the recognised sections (intro text, comments,
// hand-edited annotations).
//
// TODO(M8+): PRD v1 §4 specifies two L-layer constraints, both now
// enforced:
//
//  1. ✅ Injection limit — handled by hook.go:OnPrePrompt via
//     truncateSoulStable (tokens.go); keeps injected L ≤~500 tokens
//     by dropping trailing bullet entries at deterministic boundaries.
//
//  2. ✅ Dream-time merging — MergeIntoL (merge.go)
//     consolidates near-duplicate L candidates by word-level Jaccard
//     similarity (≥0.55) and falls back to Levenshtein for CJK;
//     sources are merged, why evidence extended, and the output is
//     capped at maxPendingTokens (2000) via truncateSoulStable.
type Soul struct {
	Path          string
	SchemaVersion int
	UpdatedAt     time.Time
	Stable        string // body of "## Stable..." up to next "## " or EOF
	Pending       string // body of "## Pending..." up to next "## " or EOF
	Raw           string
}

// LoadSoul reads ~/.seek/soul.md. Missing file returns a zero-value
// Soul (no error) — the steady state until `seek -dream` produces L
// candidates is "no L content yet, inject nothing".
func LoadSoul() (*Soul, error) {
	path, err := paths.Soul()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Soul{Path: path}, nil
		}
		return nil, err
	}
	return parseSoul(path, string(data)), nil
}

func parseSoul(path, raw string) *Soul {
	s := &Soul{Path: path, Raw: raw}

	body := raw
	if strings.HasPrefix(body, "---\n") {
		// Frontmatter ends at the next "\n---\n" sequence. Anything
		// outside frontmatter is the markdown body.
		if end := strings.Index(body[4:], "\n---\n"); end >= 0 {
			parseSoulFrontmatter(body[4:4+end], s)
			body = body[4+end+len("\n---\n"):]
		}
	}

	s.Stable = extractSoulSection(body, "## Stable")
	s.Pending = extractSoulSection(body, "## Pending")
	return s
}

func parseSoulFrontmatter(front string, s *Soul) {
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "schema_version":
			n, err := strconv.Atoi(val)
			if err == nil {
				s.SchemaVersion = n
			}
		case "updated_at":
			t, err := time.Parse(time.RFC3339, val)
			if err == nil {
				s.UpdatedAt = t
			}
		}
	}
}

// extractSoulSection returns the trimmed body between a line that
// HasPrefix(prefix) and the next line that HasPrefix("## "), or EOF.
// HasPrefix matching lets the PRD's "## Stable（已确认 ≥3 次会话无反例）"
// also match "## Stable" — the parenthetical caption is descriptive and
// not part of the header identity.
func extractSoulSection(body, prefix string) string {
	lines := strings.Split(body, "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		if start < 0 && strings.HasPrefix(line, prefix) {
			start = i + 1
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "## ") {
			end = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

// Save writes Raw back to disk atomically. Path defaults to ~/.seek/soul.md
// when zero. The caller is responsible for keeping Raw in sync with any
// edits to the structured fields — for v1 only `seek -dream` writes,
// and dream constructs a fresh Raw each time.
func (s *Soul) Save() error {
	path := s.Path
	if path == "" {
		p, err := paths.Soul()
		if err != nil {
			return err
		}
		path = p
	}
	return atomicWrite(path, []byte(s.Raw))
}

// SetSections regenerates Raw from the given Stable + Pending section
// bodies, bumps SchemaVersion + UpdatedAt, and updates the parsed
// fields to match. Used by `seek -dream` to land new L-pending
// candidates without trashing existing Stable.
//
// NOTE: this is a full re-render — any markdown content the user
// inserted outside the two recognised sections (intro paragraphs,
// hand-typed notes between Stable and Pending, extra `##` headers)
// is LOST. v1 makes this trade because the alternative — surgical
// in-place section edits that preserve arbitrary surrounding markdown —
// is significantly harder to get right, and the file format is
// documented as auto-managed.
func (s *Soul) SetSections(stable, pending string) {
	s.Stable = stable
	s.Pending = pending
	s.SchemaVersion = 1
	s.UpdatedAt = time.Now().UTC()
	s.Raw = renderSoul(s.SchemaVersion, s.UpdatedAt, stable, pending)
}

// renderSoul builds the on-disk markdown layout. Section headers match
// what parseSoul / extractSoulSection expect (HasPrefix on "## Stable"
// / "## Pending") so round-tripping (parse → SetSections → save → parse)
// is invariant.
func renderSoul(version int, updatedAt time.Time, stable, pending string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "---\nschema_version: %d\nupdated_at: %s\n---\n\n", version, updatedAt.Format(time.RFC3339))
	sb.WriteString("# User profile\n\n")
	sb.WriteString("## Stable\n\n")
	if s := strings.TrimSpace(stable); s != "" {
		sb.WriteString(s)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## Pending\n\n")
	if p := strings.TrimSpace(pending); p != "" {
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	return sb.String()
}
