package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// LCandidate is one user-trait candidate produced by `seek -dream`.
// Distinct from Candidate (M-layer) — L candidates are CROSS-project
// patterns describing the user, not project decisions.
//
// Sources holds the project IDs (or session IDs prefixed with "session:")
// where the trait was observed. The N≥2 filter in PRD §6 ("evidence
// from ≥2 different projects") is enforced after parse: anything with
// fewer than 2 distinct project sources is dropped before write.
//
// FirstSeen and LastSeen are set by parseLMarkdown when reading existing
// Pending entries from soul.md. Dream-reasoner output sets neither — they
// are maintenance-only fields (M5.10 evaluatePending).
//
// JSON tags match thinking mode's output schema; FirstSeen/LastSeen are
// omitempty so they don't leak into the dream API call.
type LCandidate struct {
	Trait     string    `json:"trait"`
	Why       string    `json:"why"`
	Sources   []string  `json:"sources"`
	FirstSeen time.Time `json:"first_seen,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

// DreamSystemPrompt frames the cross-project distillation task. The
// project-fact vs user-trait distinction is PRD §6's load-bearing
// requirement — without it thinking mode happily produces "this project
// uses JSONL" as a "user trait", which is the wrong abstraction layer.
//
// The N≥2 explicit instruction lives here too so a single-source
// reasoner output looks like thinking mode ignoring the rule rather
// than something we have to filter silently downstream.
const DreamSystemPrompt = `You are a user-trait distillation engine ("dream mode").

You will read cross-project memory entries (M layer) and recent session
tails from a user's seek history. Your job is to identify patterns
that describe the USER — their preferences, thinking habits, recurring
choices — as opposed to PROJECT FACTS.

CRITICAL distinction:

- PROJECT FACT (REJECT): "project seek uses JSONL for sessions because
  prefix-cache needs byte-identical history". This is a decision about
  one codebase; it belongs in that project's M layer, not in L.

- USER TRAIT (ACCEPT): "user tends to choose explicit error handling
  over panic across all Go projects". This describes the user, not a
  project.

REQUIREMENTS for every candidate:
- trait: one specific sentence about the user. NOT "user is good at
  Go" (too generic) — "user prefers behaviour-comparison tests over
  pure-output assertions" (specific, falsifiable).
- why: brief rationale referring to the evidence.
- sources: array of project IDs (or "session:<id>") where this trait
  was observed. **MUST contain ≥2 distinct projects** — a pattern
  visible only in one project is a project fact, not a user trait.

REJECT:
- Single-project patterns (those are project facts; output them only
  if you can cite ≥2 projects).
- Generic developer-personality claims you cannot substantiate from
  the evidence ("user values clean code").
- Restatements of M-layer entries verbatim (the M layer already has
  them).
- Anything you could equally say about every seek user.

If no candidates qualify, return an empty array. DO NOT invent weak
candidates to fill a quota.

Respond with ONLY a JSON array of objects matching the schema. No
prose, no markdown fences, no commentary.`

// DreamInput is what gets stuffed into thinking mode's user message.
// Projects holds {id, entries[]} so thinking mode can attribute each
// trait to specific projects. Sessions is optional context — recent
// session message tails for cross-checking patterns the M layer
// hasn't captured yet.
type DreamInput struct {
	Projects []DreamProject
	Sessions []DreamSession
}

// DreamProject is a compact snapshot of one project's M entries —
// just name + tagline + content per active (non-stale) entry. We don't
// pass timestamps because thinking mode doesn't need to reason about
// recency for trait extraction.
type DreamProject struct {
	ID      string
	Entries []Entry
}

// DreamSession carries the trailing K messages of a session. Used as
// supplementary signal — patterns that haven't been distilled into M
// yet are still detectable here.
type DreamSession struct {
	ID       string
	Messages []deepseek.Message
}

// BuildDreamUserMessage renders the per-call user message. Format
// mirrors BuildDistillUserMessage's "human-readable transcript"
// philosophy: thinking mode reads prose, not wire schemas.
func BuildDreamUserMessage(in DreamInput) string {
	var sb strings.Builder
	sb.WriteString("Identify user-trait candidates from the following cross-project material. Remember: ≥2 distinct project sources per candidate.\n\n")

	sb.WriteString("=== projects ===\n")
	if len(in.Projects) == 0 {
		sb.WriteString("(no project memory yet)\n")
	}
	for _, p := range in.Projects {
		fmt.Fprintf(&sb, "\n-- project %s (%d entries) --\n", p.ID, len(p.Entries))
		for _, e := range p.Entries {
			if e.Stale {
				continue
			}
			fmt.Fprintf(&sb, "* %s: %s\n", e.Name, e.Tagline)
			if e.Content != "" {
				sb.WriteString("    ")
				sb.WriteString(truncate(strings.ReplaceAll(e.Content, "\n", " "), 400))
				sb.WriteString("\n")
			}
		}
	}

	if len(in.Sessions) > 0 {
		sb.WriteString("\n=== recent session tails ===\n")
		for _, s := range in.Sessions {
			fmt.Fprintf(&sb, "\n-- session %s --\n", s.ID)
			for _, m := range s.Messages {
				if m.Role == deepseek.RoleSystem {
					continue
				}
				fmt.Fprintf(&sb, "%s: %s\n", m.Role, truncate(m.Content, 300))
			}
		}
	}

	sb.WriteString("\n=== end material ===\n")
	sb.WriteString("Respond with a JSON array of LCandidate objects. Empty array if nothing qualifies.")
	return sb.String()
}

// ParseLCandidates extracts dream output with the same tolerance
// ParseCandidates uses for /distill: code fences, single object,
// leading prose.
//
// Filtering to ≥2 sources happens AFTER parse — see FilterByEvidence.
// Keeping the steps separate lets callers preview the raw reasoner
// output (useful when debugging "why no candidates").
func ParseLCandidates(raw string) ([]LCandidate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("dream: empty response")
	}
	raw = stripCodeFence(raw)
	raw = trimLeadingProse(raw)

	if strings.HasPrefix(raw, "{") {
		var c LCandidate
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, fmt.Errorf("dream: single-object parse: %w", err)
		}
		if c.Trait == "" {
			return nil, nil
		}
		return []LCandidate{c}, nil
	}
	if !strings.HasPrefix(raw, "[") {
		return nil, fmt.Errorf("dream: expected JSON array or object, got %q", truncate(raw, 60))
	}

	var out []LCandidate
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("dream: array parse: %w", err)
	}
	return out, nil
}

// FilterByEvidence drops candidates whose Sources list contains fewer
// than minDistinctSources unique entries. Per PRD §6, minDistinctSources
// = 2 for the L promotion path. Sources are normalised by trimming
// whitespace and lowering case before dedup — different projects
// referenced with mismatched casing don't count twice.
func FilterByEvidence(in []LCandidate, minDistinctSources int) []LCandidate {
	out := in[:0]
	for _, c := range in {
		seen := map[string]struct{}{}
		for _, s := range c.Sources {
			s = strings.ToLower(strings.TrimSpace(s))
			if s == "" {
				continue
			}
			seen[s] = struct{}{}
		}
		if len(seen) >= minDistinctSources {
			out = append(out, c)
		}
	}
	return out
}

// Dreamer orchestrates one `seek -dream` round-trip. Mirrors Distiller's
// shape — chatClient interface, V4-Flash-thinking default — so the
// testing path is the same.
type Dreamer struct {
	Client     chatClient
	Model      string // default deepseek.ModelV4Flash (+ Thinking)
	MinSources int    // default 2 per PRD §6
}

// Dream runs the prompt construction → thinking-mode call → parse → filter
// pipeline. The returned slice has been filtered to MinSources; callers
// can use it directly without additional gating.
func (d *Dreamer) Dream(ctx context.Context, in DreamInput) ([]LCandidate, error) {
	if d.Client == nil {
		return nil, errors.New("dream: Client is required")
	}
	model := d.Model
	if model == "" {
		model = deepseek.ModelV4Flash
	}
	minSrc := d.MinSources
	if minSrc <= 0 {
		minSrc = 2
	}

	req := &deepseek.ChatRequest{
		Model: model,
		Messages: deepseek.StripReasoningContent([]deepseek.Message{
			{Role: deepseek.RoleSystem, Content: DreamSystemPrompt},
			{Role: deepseek.RoleUser, Content: BuildDreamUserMessage(in)},
		}),
	}
	// Dream is a one-shot reasoning extraction — opt into thinking
	// explicitly. V4 models only think when asked via the Thinking
	// parameter; the retired deepseek-reasoner alias used to provide
	// this implicitly.
	if deepseek.ShouldEnableThinking(model) || model == deepseek.ModelV4Flash {
		req.Thinking = &deepseek.ThinkingMode{Type: "enabled"}
	}

	resp, err := d.Client.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("dream: chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("dream: reasoner returned no choices")
	}

	raw, err := ParseLCandidates(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, err
	}
	return FilterByEvidence(raw, minSrc), nil
}

// FormatLCandidatesMarkdown renders candidates as a markdown bullet
// list compatible with Soul's "## Pending" section. Used both for
// `seek -dream` stdout preview and for the file-write path so the
// on-disk shape matches what the user previewed.
func FormatLCandidatesMarkdown(candidates []LCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, c := range candidates {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "- **%s**\n", c.Trait)
		if c.Why != "" {
			fmt.Fprintf(&sb, "  - 来源 / why: %s\n", c.Why)
		}
		if len(c.Sources) > 0 {
			// Deduplicate + sort sources for byte-stable output across
			// runs — important if a future change ever loads soul.md
			// through prefix cache.
			seen := map[string]struct{}{}
			var uniq []string
			for _, s := range c.Sources {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				if _, ok := seen[s]; ok {
					continue
				}
				seen[s] = struct{}{}
				uniq = append(uniq, s)
			}
			sort.Strings(uniq)
			fmt.Fprintf(&sb, "  - sources: %s\n", strings.Join(uniq, ", "))
		}
		if !c.FirstSeen.IsZero() {
			fmt.Fprintf(&sb, "  - 首次观察：%s\n", c.FirstSeen.Format("2006-01-02"))
		}
		if !c.LastSeen.IsZero() {
			fmt.Fprintf(&sb, "  - 最近确认：%s\n", c.LastSeen.Format("2006-01-02"))
		}
	}
	return sb.String()
}
