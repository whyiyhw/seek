// Package skilldistill is the cognitive half of skill auto-distillation
// (feature-skill-distill.md M-SD1): turn one session's transcript into
// candidate SKILL.md drafts, filtered by the novelty funnel's cheap
// layers before a human ever sees them.
//
// What this package owns:
//   - L0 mechanical gate — is the session even procedural-knowledge
//     shaped (≥20 messages, ≥3 tool calls)? Zero API cost.
//   - Extraction prompt + response parsing (L1 prompt-internal dedup
//     rides along: the prompt carries the full skill manifest and the
//     project's M index, and the empty-array discipline is load-bearing
//     — same trick that keeps /distill for M from inventing garbage).
//   - Evidence verification — every evidence quote must appear verbatim
//     in the transcript; a candidate whose quotes can't be found is
//     presumed hallucinated and rejected (PRD §8 #13).
//   - L2 mechanical dedup — name collision + trigram-Jaccard text
//     overlap against installed skills and the M index. Deterministic.
//   - L3 semantic filter — one reasoner call, ACCEPT/REJECT, for the
//     funnel's grey zone. Wired for the automatic path; the manual
//     /distill path may skip it (the human is the stronger filter).
//   - SKILL.md rendering — a draft must parse with skill.Parse as-is:
//     "draft" and "installed" differ only in directory location.
//
// What it explicitly does NOT own:
//   - Any filesystem write. Drafts land on disk via internal/skillmgr's
//     DraftStore — the AGENTS.md rule that only skillmgr/skillstats
//     mutate ~/.seek/skills/ is the package boundary here.
package skilldistill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// SkillCandidate is one extraction result — a repeatable procedure the
// reasoner believes is worth keeping as a skill DRAFT. Fields mirror
// what the LLM emits; RenderSKILLMD turns a candidate into the bytes
// that land in .drafts/<name>/SKILL.md.
type SkillCandidate struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Evidence    []string `json:"evidence"`

	// L2Note carries the grey-zone warning from CheckNovelty (and the
	// flagged names) to whoever renders the review modal and the draft
	// sidecar's filter_trace. Not part of the reasoner's output shape —
	// the JSON tag keeps round-tripping a candidate through
	// marshal/unmarshal lossless for the fields that matter.
	L2Note     string   `json:"-"`
	L2GreyName []string `json:"-"`
}

// MEntry mirrors memory.IndexEntry (name + tagline) without importing
// internal/memory — skilldistill reads the M index as dedup context,
// nothing more, and the looser coupling keeps the dependency arrow
// pointing at the small types, not the whole memory subsystem.
type MEntry struct {
	Name    string
	Tagline string
}

// Rejection records why a candidate never reached the review modal.
// The TUI echoes these as one muted line each so "the reasoner found
// something but the funnel ate it" stays observable — silent funnel
// drops are how a novelty check quietly becomes a novelty black hole.
type Rejection struct {
	Name   string
	Reason string
}

func (r Rejection) String() string {
	return fmt.Sprintf("%s: %s", r.Name, r.Reason)
}

// DefaultMaxCandidates caps the per-extraction yield. Same "≤3 because
// more than that overwhelms the review pass" rationale as memory's
// /distill — with the added weight that every accepted candidate here
// costs a permanent manifest slot once installed.
const DefaultMaxCandidates = 3

// DefaultModel is what Distiller falls back to when Model is empty —
// latency-first Flash with thinking, same as /distill and observe
// (PRD §7 model selection). Exported so the TUI can stamp the same
// default into draft sidecars without duplicating the choice.
const DefaultModel = deepseek.ModelV41Flash

// L0 gate thresholds (PRD §4 L0). Procedural knowledge needs a
// multi-step session to exist in; pure Q&A dies here at zero API cost.
const (
	GateMinMessages  = 20
	GateMinToolCalls = 3
)

// GateResult is the L0 mechanical gate's verdict.
type GateResult struct {
	Pass bool
	// Boost marks sessions containing a `Pitfall:` commit trailer —
	// pitfall-fixing sessions are exactly where "hit坑 → work around"
	// procedures concentrate. It raises priority for the automatic
	// path's batch ordering (M-SD2); the manual path only reports it.
	Boost  bool
	Detail string
}

// MechanicalGate is funnel layer L0: deterministic, zero API. Counts
// non-system messages (tool results included — they're half of any
// real procedure) and assistant tool calls.
func MechanicalGate(history []deepseek.Message) GateResult {
	msgs, calls := 0, 0
	boost := false
	for _, m := range history {
		if m.Role == deepseek.RoleSystem {
			continue
		}
		msgs++
		calls += len(m.ToolCalls)
		if !boost && strings.Contains(m.Content, "Pitfall:") {
			boost = true
		}
	}
	if msgs < GateMinMessages || calls < GateMinToolCalls {
		return GateResult{
			Pass:   false,
			Boost:  boost,
			Detail: fmt.Sprintf("%d messages / %d tool calls (need ≥%d / ≥%d)", msgs, calls, GateMinMessages, GateMinToolCalls),
		}
	}
	detail := fmt.Sprintf("%d messages / %d tool calls", msgs, calls)
	if boost {
		detail += " (pitfall signal)"
	}
	return GateResult{Pass: true, Boost: boost, Detail: detail}
}

// SkillDistillSystemPrompt frames the extraction call. The structure
// mirrors memory.DistillSystemPrompt deliberately — the empty-array
// discipline sentence is the single most important line ("DO NOT
// invent weak candidates"): without it the model fills the quota with
// generic best practices, and a bad skill costs every future session's
// prefix forever (PRD §1.3).
const SkillDistillSystemPrompt = `You are a skill-distillation engine.

You will read the conversation history of one seek session and extract
REPEATABLE PROCEDURES worth keeping as skill drafts — things the agent
learned to DO in this session that future sessions should follow again.
Skills are procedural knowledge ("how to do X"), not facts or decisions.

The session's existing skills (name + description) and project memory
entries are provided. A candidate that duplicates any of them is NOT
novel — do not output it.

REQUIREMENTS for every candidate:
- name: kebab-case slug, ≤40 chars (e.g. fix-tui-state-bug)
- description: MUST be a concrete when-to-use trigger ("When X
  happens, follow this"). It goes into every future session's prompt
  as the activation trigger. "Improves productivity" style filler is
  an automatic rejection.
- body: markdown with these sections:
  ## When to use (specific triggers + counter-examples)
  ## Steps (numbered; each step names the tool to call, the signal to
  watch for, and what to do when it fails — the raw material is the
  REAL tool sequence from this session, not imagined best practice)
  ## Verification (how to confirm it worked — commands/assertions
  that actually appeared in the session)
  ## References (optional; docs/outputs worth keeping)
- evidence: 1-3 VERBATIM excerpts from the transcript proving the
  procedure really happened. Copy exact spans — they are mechanically
  checked against the transcript; a candidate whose evidence cannot
  be found is discarded.

REJECT (these belong elsewhere or nowhere):
- One-off project decisions ("we chose JSONL") — that's the M memory
  layer via /distill, not a skill
- User preferences and traits — the L (soul) layer, not a skill
- Generic programming knowledge every model already has
- Anything that only makes sense inside this one session
- Anything already covered by an existing skill or memory entry above

If nothing qualifies, return []. DO NOT invent weak candidates to fill
the quota.

Respond with ONLY a JSON array of objects with keys name, description,
body, evidence. No prose, no markdown fences.`

// BuildSkillDistillUserMessage renders the per-call user message:
// dedup context (manifest + M index) first, then the transcript. The
// transcript rendering matches memory's BuildDistillUserMessage so the
// two reasoners read the same shape of history.
func BuildSkillDistillUserMessage(history []deepseek.Message, manifest string, mIndex []MEntry, maxCandidates int) string {
	if maxCandidates <= 0 {
		maxCandidates = DefaultMaxCandidates
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Extract up to %d skill candidates from this session.\n\n", maxCandidates)

	sb.WriteString("=== existing skills (do not duplicate) ===\n")
	if strings.TrimSpace(manifest) == "" {
		sb.WriteString("(none)\n")
	} else {
		sb.WriteString(manifest)
		if !strings.HasSuffix(manifest, "\n") {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("=== project memory entries M (decisions already recorded — not skills) ===\n")
	if len(mIndex) == 0 {
		sb.WriteString("(none)\n")
	} else {
		for _, e := range mIndex {
			fmt.Fprintf(&sb, "- %s: %s\n", e.Name, e.Tagline)
		}
	}

	sb.WriteString("\n=== conversation ===\n")
	for _, m := range history {
		if m.Role == deepseek.RoleSystem {
			continue
		}
		switch m.Role {
		case deepseek.RoleTool:
			fmt.Fprintf(&sb, "[tool result %s]: %s\n", m.ToolCallID, truncate(m.Content, 800))
		default:
			fmt.Fprintf(&sb, "%s: %s\n", m.Role, m.Content)
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "  [tool call: %s(%s)]\n", tc.Function.Name, truncate(tc.Function.Arguments, 200))
			}
		}
	}
	sb.WriteString("=== end conversation ===\n\n")
	sb.WriteString("Respond with a JSON array of candidates. Empty array if nothing qualifies.")
	return sb.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ParseSkillCandidates extracts candidates from the reasoner's raw
// response, with the same fence/prose/single-object tolerance as
// memory.ParseCandidates (the model produces the same variations
// despite "JSON only").
//
// Per-candidate validation happens here rather than at render time:
// empty name/description and non-kebab names are EXPECTED failures
// (PRD §5.3), and they come back as Rejections instead of a hard error
// so one malformed candidate doesn't take down the well-formed rest.
// Returns an error only when the whole payload isn't JSON at all.
func ParseSkillCandidates(raw string) ([]SkillCandidate, []Rejection, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, errors.New("skilldistill: empty response")
	}
	raw = stripCodeFence(raw)
	raw = trimLeadingProse(raw)

	if strings.HasPrefix(raw, "{") {
		raw = "[" + raw + "]"
	}
	if !strings.HasPrefix(raw, "[") {
		return nil, nil, fmt.Errorf("skilldistill: expected JSON array or object, got %q", truncate(raw, 60))
	}

	var parsed []SkillCandidate
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, nil, fmt.Errorf("skilldistill: array parse: %w", err)
	}

	var (
		kept     []SkillCandidate
		rejected []Rejection
	)
	for _, c := range parsed {
		switch {
		case c.Name == "":
			rejected = append(rejected, Rejection{Name: "(unnamed)", Reason: "missing name"})
		case !validName(c.Name):
			rejected = append(rejected, Rejection{Name: c.Name, Reason: fmt.Sprintf("name %q is not kebab-case ([a-z][a-z0-9-]*)", c.Name)})
		case strings.TrimSpace(c.Description) == "":
			rejected = append(rejected, Rejection{Name: c.Name, Reason: "missing description"})
		case strings.TrimSpace(c.Body) == "":
			rejected = append(rejected, Rejection{Name: c.Name, Reason: "missing body"})
		default:
			kept = append(kept, c)
		}
	}
	return kept, rejected, nil
}

// validName mirrors skill's nameRE without exporting it — the real
// gate is skill.Parse, which RenderSKILLMD and DraftStore both run.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		return strings.TrimPrefix(s, "```")
	}
	if end := strings.LastIndex(s, "```"); end >= 0 {
		s = s[:end]
	}
	return strings.TrimSpace(s)
}

func trimLeadingProse(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '[' || s[i] == '{' {
			return s[i:]
		}
	}
	return s
}

// VerifyEvidence returns the evidence quotes that do NOT appear
// verbatim in the rendered transcript. Empty result = every quote is
// backed by the session. This is the anti-hallucination tripwire
// (PRD §8 #13): a procedure the session never actually performed
// should fail here, whatever the reasoner claims.
func VerifyEvidence(evidence []string, transcript string) []string {
	var missing []string
	for _, q := range evidence {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if !strings.Contains(transcript, q) {
			missing = append(missing, q)
		}
	}
	return missing
}

// RenderTranscript renders history the same way the extraction prompt
// does, so VerifyEvidence checks against exactly what the reasoner
// read. Exported because the caller (TUI / future auto path) holds the
// history and needs the same rendering to re-verify later.
func RenderTranscript(history []deepseek.Message) string {
	// Reuse the prompt builder's conversation section: build with
	// empty dedup context and slice out the transcript part. Cheaper
	// than duplicating the loop and risking the two drifting apart.
	full := BuildSkillDistillUserMessage(history, "", nil, 1)
	const begin = "=== conversation ===\n"
	const end = "=== end conversation ==="
	i := strings.Index(full, begin)
	j := strings.Index(full, end)
	if i < 0 || j < 0 {
		return full
	}
	return full[i+len(begin) : j]
}

// L2 similarity thresholds (PRD §4 L2). Tuned conservative-on-purpose:
// the cost asymmetry is lopsided (a false accept pollutes every future
// session's manifest; a false reject just means the user writes the
// skill by hand via `seek skill create`).
const (
	SimilarityReject = 0.60 // ≥ → same thing, reject outright
	SimilarityGrey   = 0.35 // ≥ (and < reject) → borderline, grey zone
)

// L2Verdict is the mechanical dedup outcome for one candidate.
type L2Verdict int

const (
	L2Pass   L2Verdict = iota // no collision worth flagging
	L2Grey                    // borderline overlap — keep, but surface it
	L2Reject                  // duplicate of something installed/recorded
)

func (v L2Verdict) String() string {
	switch v {
	case L2Pass:
		return "PASS"
	case L2Grey:
		return "GREY"
	default:
		return "REJECT"
	}
}

// L2Result is CheckNovelty's output. Reason is human-readable and
// lands verbatim in the review modal (and the draft sidecar's
// filter_trace), so a rejection is never a silent black hole.
type L2Result struct {
	Verdict L2Verdict
	Reason  string
	// GreyNames are the skills/M-entries the grey-zone overlap hit.
	// The automatic path feeds their bodies into the L3 filter context
	// (PRD §4 L2→L3 handoff); the manual path just shows the names.
	GreyNames []string
}

// CheckNovelty is funnel layer L2: deterministic, post-parse, pre-draft.
//
//   - Name collision with a loaded skill → REJECT. First delivery
//     deliberately has no supersedes/update flow (PRD §11) — refusing
//     keeps "never overwrite an installed skill" conservative-simple.
//   - Text overlap: trigram Jaccard of description+body vs each
//     skill's description; same measure vs each M entry's
//     name+tagline ("this is really a recorded decision" signal).
//     ≥0.6 REJECT, 0.35–0.60 GREY, else PASS.
func CheckNovelty(cand SkillCandidate, skills *skill.Set, mIndex []MEntry) L2Result {
	if skills != nil {
		if existing := skills.Get(cand.Name); existing != nil {
			return L2Result{
				Verdict: L2Reject,
				Reason: fmt.Sprintf("name conflicts with installed skill %q; to improve it use `seek skill update` / edit it, not a new draft",
					existing.Name),
			}
		}
	}

	candText := cand.Description + "\n" + cand.Body

	var (
		bestSim   float64
		bestName  string
		bestIsMem bool
		greyNames []string
	)
	if skills != nil {
		for _, sk := range skills.List() {
			sim := trigramJaccard(candText, sk.Description)
			if sim > bestSim {
				bestSim, bestName, bestIsMem = sim, sk.Name, false
			}
			if sim >= SimilarityGrey && sim < SimilarityReject {
				greyNames = append(greyNames, sk.Name)
			}
		}
	}
	for _, e := range mIndex {
		sim := trigramJaccard(candText, e.Name+" "+e.Tagline)
		if sim > bestSim {
			bestSim, bestName, bestIsMem = sim, e.Name, true
		}
		if sim >= SimilarityGrey && sim < SimilarityReject {
			greyNames = append(greyNames, e.Name)
		}
	}

	switch {
	case bestSim >= SimilarityReject:
		if bestIsMem {
			return L2Result{
				Verdict: L2Reject,
				Reason:  fmt.Sprintf("overlaps memory entry %q (similarity %.2f) — this is a recorded decision (M layer), not a new skill", bestName, bestSim),
			}
		}
		return L2Result{
			Verdict: L2Reject,
			Reason:  fmt.Sprintf("overlaps installed skill %q (similarity %.2f) — same procedure already covered", bestName, bestSim),
		}
	case bestSim >= SimilarityGrey:
		// Dedupe the grey list (a name can be appended above and also
		// be bestName) while keeping stable order.
		sort.Strings(greyNames)
		return L2Result{
			Verdict:   L2Grey,
			Reason:    fmt.Sprintf("borderline overlap with %s (similarity %.2f) — review carefully", bestName, bestSim),
			GreyNames: dedupeStrings(greyNames),
		}
	default:
		return L2Result{Verdict: L2Pass}
	}
}

func dedupeStrings(in []string) []string {
	var out []string
	for i, s := range in {
		if i > 0 && s == in[i-1] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// trigramJaccard computes rune-trigram Jaccard similarity between two
// strings (case-folded, whitespace-run-collapsed). Rune trigrams, not
// bytes: skill descriptions are frequently Chinese, where byte
// trigrams chop mid-character and measure encoding artifacts rather
// than content. Texts shorter than 3 runes share no trigrams; that
// yields 0 similarity, which is the correct answer (nothing overlaps
// a 2-character string).
func trigramJaccard(a, b string) float64 {
	ta := trigramSet(a)
	tb := trigramSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for g := range ta {
		if _, ok := tb[g]; ok {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func trigramSet(s string) map[string]struct{} {
	s = strings.ToLower(s)
	s = strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
	runes := []rune(s)
	if len(runes) < 3 {
		return nil
	}
	set := make(map[string]struct{}, len(runes))
	for i := 0; i+3 <= len(runes); i++ {
		set[string(runes[i:i+3])] = struct{}{}
	}
	return set
}

// FilterResult / FilterReason mirror memory.Distiller.Filter's
// ACCEPT/REJECT contract — the L3 layer is the same one-shot reasoner
// verdict, just aimed at skill candidates instead of memory entries.
type FilterResult int

const (
	FilterAccept FilterResult = iota
	FilterReject
)

type FilterReason string

// SkillFilterPrompt frames the L3 semantic filter call.
const SkillFilterPrompt = `You are a skill-draft quality filter. You are given:
1. The already-installed skills (name + description)
2. The project's recorded memory entries (decisions, M layer)
3. A new skill DRAFT candidate extracted from one session

Decide: ACCEPT or REJECT.

REJECT if:
- The draft duplicates an installed skill or memory entry (same thing, different words)
- The draft is really a project decision, not a repeatable procedure
- The draft is generic knowledge any assistant already has
- The draft's when-to-use trigger is too vague to ever fire ("when writing code")

ACCEPT if:
- The draft is a concrete, repeatable procedure with a specific trigger
- Future sessions in other projects would plausibly follow it again
- Nothing installed or recorded already covers it

Respond with valid JSON only. No prose, no markdown fences:
{"decision": "ACCEPT", "reason": "..."}
{"decision": "REJECT", "reason": "duplicate of <name>"}`

// ChatClient is the minimum surface the Distiller needs from a
// DeepSeek client. An interface so tests plug in a fake without an
// httptest server (same pattern as memory's chatClient).
type ChatClient interface {
	Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error)
}

// Distiller orchestrates the reasoner half of the funnel: extraction
// (L1 prompt-internal dedup rides along) and the L3 semantic filter.
// Model defaults to ModelV41Flash with thinking — same latency-first
// choice as /distill and observe (PRD §7 model selection).
type Distiller struct {
	Client ChatClient
	Model  string // default deepseek.ModelV41Flash (+ Thinking)
	Max    int    // default DefaultMaxCandidates
}

func (d *Distiller) model() string {
	if d.Model == "" {
		return DefaultModel
	}
	return d.Model
}

// chatRequest assembles the two-message request with thinking enabled.
func (d *Distiller) chatRequest(system, user string) *deepseek.ChatRequest {
	req := &deepseek.ChatRequest{
		Model: d.model(),
		Messages: deepseek.StripReasoningContent([]deepseek.Message{
			{Role: deepseek.RoleSystem, Content: system},
			{Role: deepseek.RoleUser, Content: user},
		}),
	}
	if deepseek.ShouldEnableThinking(req.Model) {
		req.Thinking = &deepseek.ThinkingMode{Type: "enabled"}
	}
	return req
}

// Extract runs the extraction call and the zero-API funnel layers that
// sit between the raw response and the review modal: per-candidate
// shape validation (in ParseSkillCandidates), evidence verification,
// and L2 mechanical dedup. L3 is NOT run here — the manual path treats
// the human as the stronger filter; the automatic path (M-SD2) calls
// Filter separately for grey-zone candidates.
//
// kept are candidates that survived; rejected lists everything the
// funnel dropped with reasons (the TUI prints these, the automatic
// path logs them). An empty kept slice is a normal, common outcome.
func (d *Distiller) Extract(ctx context.Context, history []deepseek.Message, manifest string, mIndex []MEntry, skills *skill.Set) (kept []SkillCandidate, rejected []Rejection, err error) {
	if d.Client == nil {
		return nil, nil, errors.New("skilldistill: Client is required")
	}
	maxN := d.Max
	if maxN <= 0 {
		maxN = DefaultMaxCandidates
	}

	user := BuildSkillDistillUserMessage(history, manifest, mIndex, maxN)
	resp, err := d.Client.Chat(ctx, d.chatRequest(SkillDistillSystemPrompt, user))
	if err != nil {
		return nil, nil, fmt.Errorf("skilldistill: chat: %w", err)
	}
	// resp nil-guard: a client that returns (nil, nil) (broken fake,
	// half-initialised adapter) would otherwise panic on Choices.
	if resp == nil || len(resp.Choices) == 0 {
		return nil, nil, errors.New("skilldistill: reasoner returned no choices")
	}

	parsed, rej, err := ParseSkillCandidates(resp.Choices[0].Message.Content)
	if err != nil {
		return nil, nil, err
	}
	rejected = append(rejected, rej...)

	transcript := RenderTranscript(history)
	for _, c := range parsed {
		if missing := VerifyEvidence(c.Evidence, transcript); len(missing) > 0 {
			rejected = append(rejected, Rejection{
				Name:   c.Name,
				Reason: fmt.Sprintf("evidence quote not found verbatim in transcript (%d of %d missing) — presumed hallucinated", len(missing), len(c.Evidence)),
			})
			continue
		}
		l2 := CheckNovelty(c, skills, mIndex)
		switch l2.Verdict {
		case L2Reject:
			rejected = append(rejected, Rejection{Name: c.Name, Reason: l2.Reason})
		default:
			// PASS and GREY both reach the human; GREY candidates
			// carry their warning along in L2Note so the modal and
			// the sidecar's filter_trace can show why it nearly died.
			if l2.Verdict == L2Grey {
				c.L2Note = l2.Reason
				c.L2GreyName = l2.GreyNames
			}
			kept = append(kept, c)
		}
	}
	return kept, rejected, nil
}

// Filter is funnel layer L3: one reasoner call, ACCEPT/REJECT, for a
// grey-zone candidate. Context = full manifest + M index + the bodies
// of the specific skills the L2 grey-zone flagged (so the filter can
// actually compare procedures, not just descriptions). The manual
// /distill path skips this — the human is the stronger filter; the
// automatic path (M-SD2) must run it.
func (d *Distiller) Filter(ctx context.Context, cand SkillCandidate, manifest string, mIndex []MEntry, greyBodies []string) (FilterResult, FilterReason, error) {
	if d.Client == nil {
		return FilterReject, "", errors.New("skilldistill filter: Client is required")
	}

	var sb strings.Builder
	sb.WriteString("Installed skills:\n")
	if strings.TrimSpace(manifest) == "" {
		sb.WriteString("(none)\n")
	} else {
		sb.WriteString(manifest)
		if !strings.HasSuffix(manifest, "\n") {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nProject memory entries (M layer):\n")
	if len(mIndex) == 0 {
		sb.WriteString("(none)\n")
	} else {
		for _, e := range mIndex {
			fmt.Fprintf(&sb, "- %s: %s\n", e.Name, e.Tagline)
		}
	}
	if len(greyBodies) > 0 {
		sb.WriteString("\nBodies of flagged similar skills:\n")
		for _, b := range greyBodies {
			sb.WriteString("---\n")
			sb.WriteString(truncate(b, 1500))
			sb.WriteString("\n")
		}
	}
	fmt.Fprintf(&sb, "\nNew skill draft candidate:\nname: %s\ndescription: %s\nbody:\n%s\n", cand.Name, cand.Description, cand.Body)

	resp, err := d.Client.Chat(ctx, d.chatRequest(SkillFilterPrompt, sb.String()))
	if err != nil {
		return FilterReject, "", fmt.Errorf("skilldistill filter: chat: %w", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		return FilterReject, "", errors.New("skilldistill filter: no choices returned")
	}
	return parseFilterResult(resp.Choices[0].Message.Content)
}

type filterResponse struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func parseFilterResult(raw string) (FilterResult, FilterReason, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var fr filterResponse
	if err := json.Unmarshal([]byte(raw), &fr); err != nil {
		return FilterReject, "", fmt.Errorf("skilldistill filter: parse: %w (raw: %q)", err, truncate(raw, 60))
	}
	switch fr.Decision {
	case "ACCEPT":
		return FilterAccept, FilterReason(fr.Reason), nil
	case "REJECT":
		return FilterReject, FilterReason(fr.Reason), nil
	default:
		return FilterReject, "", fmt.Errorf("skilldistill filter: unknown decision %q", fr.Decision)
	}
}

// DraftVersion is the frontmatter version stamped on rendered drafts.
// The -draft prerelease suffix makes "not yet human-approved" visible
// in `seek skill status`; what happens to it after install is the
// installer's call (PRD §5.3).
const DraftVersion = "0.1.0-draft"

// PromptRev identifies the extraction prompt revision, recorded in
// the draft sidecar's generator block so a draft's provenance says
// which prompt produced it.
const PromptRev = "skill-distill-v1"

// RenderSKILLMD turns a candidate into Anthropic-skill-layout bytes:
// frontmatter (name / description block-scalar / version / license /
// author) + the candidate's body as-is. The description block-scalar
// shape mirrors what skill authors write and what skill.Parse reads
// back multiline.
//
// The result must survive skill.Parse unchanged — that's the "drafts
// are legal packages" contract (PRD §5.3). This function enforces it
// itself: it runs Parse on its own output and fails loudly if the
// round-trip ever breaks (e.g. a description containing a line that
// would terminate frontmatter).
func RenderSKILLMD(cand SkillCandidate) ([]byte, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", cand.Name)
	sb.WriteString("description: |\n")
	for line := range strings.SplitSeq(strings.TrimRight(cand.Description, "\n"), "\n") {
		if line == "" {
			sb.WriteString("\n")
			continue
		}
		fmt.Fprintf(&sb, "  %s\n", line)
	}
	fmt.Fprintf(&sb, "version: %s\n", DraftVersion)
	sb.WriteString("license: MIT\n")
	sb.WriteString("author: distilled\n")
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimRight(cand.Body, "\n"))
	sb.WriteString("\n")

	out := []byte(sb.String())
	if _, err := skill.Parse(out, "draft:"+cand.Name); err != nil {
		return nil, fmt.Errorf("skilldistill: rendered draft does not parse: %w", err)
	}
	return out, nil
}
