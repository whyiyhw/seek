// convert.go — the M→skill converter (feature-memory-skill-bridge.md
// §3.2/§3.3, M-B2). One on-demand reasoner call turns a memory-review
// candidate into a SkillCandidate whose description is a REWRITTEN
// when-to-use trigger — the review's `s` fast lane pays for it only
// when pressed (option B of §3.2), never at extraction time.
//
// Field mapping is deliberately asymmetric in effort (§8.1/§8.3):
// description is the reasoner's whole job (semantic flip from
// "what was decided" to "when to apply"), while name/body/keywords are
// mechanical — name reused, body copied verbatim, tags passed through.
// The anti-copy guardrail below enforces the rewrite mechanically:
// a description equal to the tagline is a contract violation, not a
// degraded-but-acceptable answer (no copy-tagline fallback exists).

package skilldistill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/skill"
)

// MCandidate mirrors memory.Candidate the same way MEntry mirrors
// memory.IndexEntry: skilldistill does not import internal/memory, so
// the dependency arrow points at the small type, not the memory
// subsystem (§3.3).
type MCandidate struct {
	Name    string
	Tagline string
	Content string
	Tags    []string
}

// ConvertPromptRev versions the conversion prompt contract (§8.1) —
// the mechanical guardrails below are calibrated to THIS prompt's
// promises, so a prompt rewrite must bump it.
const ConvertPromptRev = "m-to-skill-convert-v1"

// ConvertSystemPrompt frames the conversion call. The GOOD/BAD pair is
// the P1 constraint's prompt-side anchor (§8.1): the mechanical
// description==tagline check below is the parser-side anchor, and the
// two are calibrated together.
const ConvertSystemPrompt = `You are a memory-to-skill converter.

You get ONE project-memory candidate — a decision this project
recorded (name, tagline = what was decided, content = the full body) —
plus the existing skills and memory entries for context.

Your ONLY output is the skill's DESCRIPTION: the activation trigger
that goes into every future session's prompt. A tagline answers "what
was decided"; a description answers "WHEN should a future session
apply this knowledge again".

GOOD (tagline → description):
  tagline: "session files are JSONL with schema_version=2"
  description: "When reading or writing seek session files, follow the
  JSONL schema_version=2 layout — line 1 is the scalar header, lines
  2..N are one message per line; migrate legacy .json on next Save."

BAD (rejected mechanically — this just copied the tagline):
  tagline: "session files are JSONL with schema_version=2"
  description: "session files are JSONL with schema_version=2"

RULES:
- 2-4 lines, opening with the concrete trigger ("When ..."), then the
  constraints a future session must know before acting.
- Do NOT copy or lightly rephrase the tagline. Descriptions equal to
  the tagline are rejected by a mechanical check.
- Do NOT invent facts beyond the content body.
- Keep the language of the candidate (Chinese stays Chinese).
- If the decision is a one-off fact with NO reusable when-to-use
  trigger, return {"description": ""} — it belongs in memory, not in
  skills.

Respond with ONLY a JSON object: {"description": "..."} — no prose, no
markdown fences.`

// BuildConvertUserMessage renders the per-call user message: dedup
// context first (same order as the extraction prompt), then the
// candidate. Exported so tests snapshot the prompt shape (§10 #5).
func BuildConvertUserMessage(cand MCandidate, manifest string, mIndex []MEntry) string {
	var sb strings.Builder
	sb.WriteString("Convert this memory candidate into a skill description.\n\n")

	sb.WriteString("=== existing skills (do not duplicate) ===\n")
	if strings.TrimSpace(manifest) == "" {
		sb.WriteString("(none)\n")
	} else {
		sb.WriteString(manifest)
		if !strings.HasSuffix(manifest, "\n") {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("=== project memory entries M (decisions already recorded) ===\n")
	if len(mIndex) == 0 {
		sb.WriteString("(none)\n")
	} else {
		for _, e := range mIndex {
			fmt.Fprintf(&sb, "- %s: %s\n", e.Name, e.Tagline)
		}
	}

	sb.WriteString("\n=== candidate ===\n")
	fmt.Fprintf(&sb, "name: %s\ntagline: %s\ncontent:\n%s\n=== end candidate ===\n", cand.Name, cand.Tagline, cand.Content)
	return sb.String()
}

// convertResponse is the reasoner's entire output shape: the
// description. Name/body/keywords never round-trip through the model
// (§3.3) — there is nothing else to parse, nothing else to hallucinate.
type convertResponse struct {
	Description string `json:"description"`
}

// ConvertToSkill runs one reasoner call and the mechanical funnel
// around it:
//
//  1. parse (fence/prose tolerant, same as every other parser here);
//  2. guardrails — empty description = "no when-to-use trigger, keep
//     it in M" failure; description == tagline = contract violation
//     (§8.1: reject, never fall back to a tagline copy);
//  3. assemble the candidate (name reused, body = content verbatim);
//  4. CheckNovelty against installed skills + the M index — REJECT
//     returns the L2Result with a zero candidate and no error; GREY
//     rides along as the candidate's L2Note for the preview state.
//
// The returned error covers transport/parse/guardrail failures; an
// L2Reject is NOT an error (the caller shows l2.Reason verbatim).
func (d *Distiller) ConvertToSkill(ctx context.Context, cand MCandidate, skills *skill.Set, mIndex []MEntry) (SkillCandidate, L2Result, error) {
	if d.Client == nil {
		return SkillCandidate{}, L2Result{}, errors.New("skilldistill convert: Client is required")
	}
	if !validName(cand.Name) {
		return SkillCandidate{}, L2Result{}, fmt.Errorf("skilldistill convert: candidate name %q is not kebab-case ([a-z][a-z0-9-]*)", cand.Name)
	}

	manifest := ""
	if skills != nil {
		manifest = skills.Manifest()
	}
	resp, err := d.Client.Chat(ctx, d.chatRequest(ConvertSystemPrompt, BuildConvertUserMessage(cand, manifest, mIndex)))
	if err != nil {
		return SkillCandidate{}, L2Result{}, fmt.Errorf("skilldistill convert: chat: %w", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		return SkillCandidate{}, L2Result{}, errors.New("skilldistill convert: reasoner returned no choices")
	}

	description, err := parseConvertResponse(resp.Choices[0].Message.Content)
	if err != nil {
		return SkillCandidate{}, L2Result{}, err
	}
	// §3.3 rule 5: empty description is the converter's honest "this
	// is a one-off fact" verdict — a failure with guidance, so the
	// user lands on `y` instead of a hollow skill.
	if strings.TrimSpace(description) == "" {
		return SkillCandidate{}, L2Result{}, errors.New("skilldistill convert: no when-to-use trigger found — this candidate fits M (press y), not a skill")
	}
	// §8.1 mechanical guardrail: a verbatim tagline copy means the
	// rewrite didn't happen. Reject — no copy-tagline fallback.
	if strings.TrimSpace(description) == strings.TrimSpace(cand.Tagline) {
		return SkillCandidate{}, L2Result{}, fmt.Errorf("skilldistill convert: description equals the tagline verbatim — the when-to-use rewrite is mandatory, retry with s or save as M with y")
	}

	out := SkillCandidate{
		Name:        cand.Name,
		Description: description,
		Body:        cand.Content, // verbatim by design (§3.3 rule 2)
	}
	l2 := CheckNovelty(out, skills, mIndex)
	if l2.Verdict == L2Reject {
		return SkillCandidate{}, l2, nil
	}
	if l2.Verdict == L2Grey {
		out.L2Note = l2.Reason
		out.L2GreyName = l2.GreyNames
	}
	return out, l2, nil
}

// parseConvertResponse extracts {"description": "..."} with the same
// fence/prose tolerance as the other parsers — thinking-mode models
// emit the same variations despite "JSON only".
func parseConvertResponse(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("skilldistill convert: empty response")
	}
	raw = stripCodeFence(raw)
	raw = trimLeadingProse(raw)
	var cr convertResponse
	if err := json.Unmarshal([]byte(raw), &cr); err != nil {
		return "", fmt.Errorf("skilldistill convert: parse: %w (raw: %q)", err, truncate(raw, 60))
	}
	return cr.Description, nil
}

// InstalledVersion is the frontmatter version stamped on `s`-landed
// project skills (§3.4): a live skill, deliberately NOT DraftVersion —
// `seek skill status` must not advertise the drafts-approve flow for
// something that is already installed.
const InstalledVersion = "0.1.0"

// RenderProjectSkillMD renders the installed-skill variant of a
// converted candidate: RenderSKILLMD's shape with the keywords list
// (the M candidate's tags — §8.3's first production keywords writer)
// and InstalledVersion instead of the draft prerelease. Keywords use
// the block-list shape: tags are freeform single-line strings, and the
// block list survives commas/colons that an inline flow list would
// mis-split. Same self-check contract as RenderSKILLMD: the output
// must survive skill.Parse unchanged.
func RenderProjectSkillMD(cand SkillCandidate, keywords []string) ([]byte, error) {
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
	if len(keywords) > 0 {
		sb.WriteString("keywords:\n")
		for _, k := range keywords {
			// A tag with a newline would forge extra list items /
			// frontmatter lines — collapse to spaces; empty tags are
			// dropped (they'd render as bare "-" items).
			k = strings.TrimSpace(strings.ReplaceAll(k, "\n", " "))
			if k == "" {
				continue
			}
			fmt.Fprintf(&sb, "  - %s\n", k)
		}
	}
	fmt.Fprintf(&sb, "version: %s\n", InstalledVersion)
	sb.WriteString("license: MIT\n")
	sb.WriteString("author: distilled\n")
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimRight(cand.Body, "\n"))
	sb.WriteString("\n")

	out := []byte(sb.String())
	if _, err := skill.Parse(out, "project:"+cand.Name); err != nil {
		return nil, fmt.Errorf("skilldistill: rendered project skill does not parse: %w", err)
	}
	return out, nil
}
