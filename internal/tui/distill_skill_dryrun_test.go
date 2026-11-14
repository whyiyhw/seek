package tui

// /distill --dry-run tests (butler card c_oh2dl3). Three properties
// are load-bearing:
//   - zero disk writes: the drafts tree stays empty, no review modal
//     opens — the dry run is pure scrollback;
//   - the report carries every funnel layer's verdict: L0 gate · L1
//     extraction yield · per-stage drops · per-survivor L2 outcome;
//   - the real (flagless) run is unchanged: both passes still fire,
//     bare /distill prints no announcement line (existing gate tests
//     in distill_skill_test.go are the byte-stability net).
//
// The report is rendered by the pure renderSkillDryRunReport, so most
// assertions run against the string directly; the zero-write
// acceptance goes through handleSkillDistillDone exactly as
// production does.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/skilldistill"
)

// dryRunFixtures builds one PASS candidate, one GREY candidate (L2Note
// set the way Extract sets it), and one rejection per stage.
func dryRunFixtures() (kept []skilldistill.SkillCandidate, rejected []skilldistill.Rejection) {
	grey := sampleSkillCandidate("repro-flaky-first")
	grey.L2Note = "borderline overlap with flaky-test-routine (similarity 0.48) — review carefully"
	pass := skilldistill.SkillCandidate{
		Name:        "pin-go-version",
		Description: "When CI builds fail on an old toolchain, pin the Go version first",
		Body:        "## When to use\nCI-only build failures\n## Steps\n1. check go.mod",
		Evidence:    []string{"go.mod"},
	}
	kept = []skilldistill.SkillCandidate{pass, grey}
	rejected = []skilldistill.Rejection{
		{Name: "Bad_Name", Reason: "name \"Bad_Name\" is not kebab-case ([a-z][a-z0-9-]*)", Stage: skilldistill.StageShape},
		{Name: "ghost-proc", Reason: "evidence quote not found verbatim in transcript (1 of 1 missing) — presumed hallucinated", Stage: skilldistill.StageEvidence},
		{Name: "dup-skill", Reason: "name conflicts with installed skill \"dup-skill\"", Stage: skilldistill.StageL2},
	}
	return kept, rejected
}

// The layer verdicts are the acceptance surface: L0 (gate), L1
// (extraction yield), shape/evidence/L2 drops, and per-survivor L2
// outcomes must all be readable off the report.
func TestRenderSkillDryRunReport_LayerVerdicts(t *testing.T) {
	kept, rejected := dryRunFixtures()
	report := renderSkillDryRunReport("42 messages / 17 tool calls", kept, rejected)

	for _, want := range []string{
		"L0 PASS — 42 messages / 17 tool calls", // L0 gate verdict
		"L1 extraction returned 5 candidate(s)", // extraction yield = kept + rejected
		"drops shape 1 / evidence 1 / L2 1",     // per-stage drops
		"would-draft 2 (grey 1)",                // survivor tally
		"skill candidate rejected — Bad_Name:",  // rejection lines, real-run format
		"skill candidate rejected — ghost-proc:",
		"skill candidate rejected — dup-skill:",
		"skill candidate 1/2: pin-go-version — dry-run, nothing written",
		"skill candidate 2/2: repro-flaky-first — dry-run, nothing written",
		"L2: PASS",          // survivor 1 verdict
		"L2: GREY",          // survivor 2 verdict
		"similarity 0.48",   // grey note rides along
		"nothing written",   // the dry-run guarantee
		"rerun /distill to review and save",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

// Body preview is capped at skillDryRunBodyPreviewLines with a
// visible truncation marker — the dry run must not flood scrollback
// with full bodies.
func TestRenderSkillDryRunReport_BodyTruncation(t *testing.T) {
	// Zero-padded line numbers so no marker string is a substring of
	// another ("line 01" vs "line 10").
	var lines []string
	for i := 1; i <= skillDryRunBodyPreviewLines+10; i++ {
		lines = append(lines, fmt.Sprintf("body line %02d", i))
	}
	cand := skilldistill.SkillCandidate{
		Name:        "long-body",
		Description: "d",
		Body:        strings.Join(lines, "\n"),
	}
	report := renderSkillDryRunReport("g", []skilldistill.SkillCandidate{cand}, nil)

	if !strings.Contains(report, "body line 01") || !strings.Contains(report, fmt.Sprintf("body line %02d", skillDryRunBodyPreviewLines)) {
		t.Error("first N body lines must be in the preview")
	}
	if strings.Contains(report, fmt.Sprintf("body line %02d", skillDryRunBodyPreviewLines+1)) {
		t.Error("body lines beyond the cap must not appear")
	}
	if !strings.Contains(report, "(+10 more body lines") {
		t.Errorf("truncation marker missing:\n%s", report)
	}
}

// THE acceptance: a dry-run completion with live candidates must
// write nothing anywhere — no modal, no .drafts/, no sidecar — while
// the same candidates through the real path do write (checked here so
// the zero-write assertion can't pass vacuously).
func TestSkillDistillDryRun_ZeroDiskWrites(t *testing.T) {
	m, draftsDir := setupSkillModel(t)
	kept, rejected := dryRunFixtures()

	cmds := m.handleSkillDistillDone(skillDistillDoneMsg{candidates: kept, rejected: rejected, dryRun: true})

	if m.skillReviewOpen {
		t.Fatal("dry-run must not open the review modal")
	}
	if len(m.pendingSkill) != 0 {
		t.Fatalf("dry-run must not queue candidates, pending=%d", len(m.pendingSkill))
	}
	if len(cmds) == 0 {
		t.Fatal("dry-run must return the report cmd")
	}
	// Zero writes: the drafts tree must not exist at all.
	if entries, err := os.ReadDir(draftsDir); err != nil || len(entries) != 0 {
		t.Fatalf("dry-run wrote to the drafts tree (entries=%d err=%v)", len(entries), err)
	}

	// Control: the SAME candidates through the real path + 'y' DO
	// write — proving the assertions above can detect a write.
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{candidates: kept})
	if !m.skillReviewOpen {
		t.Fatal("control: real path must open the modal")
	}
	m2, _ := m.handleSkillDistillKey(keyRune('y'))
	m = m2.(Model)
	if _, err := os.Stat(filepath.Join(draftsDir, ".drafts", "pin-go-version", "SKILL.md")); err != nil {
		t.Fatalf("control: real path must write the draft: %v", err)
	}
}

// Empty dry-run result keeps the real run's "nothing found" line
// (with the dry-run marker) instead of a bare tally.
func TestSkillDistillDryRun_EmptyResult(t *testing.T) {
	m, draftsDir := setupSkillModel(t)
	cmds := m.handleSkillDistillDone(skillDistillDoneMsg{dryRun: true})
	if m.skillReviewOpen {
		t.Error("empty dry-run must not open the modal")
	}
	if len(cmds) == 0 {
		t.Fatal("empty dry-run must still print the report line")
	}
	if entries, _ := os.ReadDir(draftsDir); len(entries) != 0 {
		t.Errorf("empty dry-run must write nothing, found %d entries", len(entries))
	}
	// The report text itself: via the renderer (the cmd is opaque).
	report := renderSkillDryRunReport("", nil, nil)
	for _, want := range []string{"no reusable procedure found in this session", "nothing written"} {
		if !strings.Contains(report, want) {
			t.Errorf("empty report missing %q:\n%s", want, report)
		}
	}
}

// ---------- cmdDistill arg parsing / dry-run wiring ----------

// --dry-run fires ONLY the skill pass (no M round-trip, no modal) and
// announces the dry run; unknown arguments error instead of being
// silently ignored.
func TestCmdDistillDryRun_FlagParsingAndWiring(t *testing.T) {
	m := gateModel(t, 24, 4)
	res := runHandler(t, m, "/distill --dry-run")
	if !strings.Contains(res.text, "dry-run") || !strings.Contains(res.text, "nothing will be written") {
		t.Errorf("dry-run must announce itself: %q", res.text)
	}
	if res.extra == nil {
		t.Fatal("skill pass must fire")
	}
	if !m.skillDistilling {
		t.Error("skill spinner must be running")
	}
	if m.distilling {
		t.Error("dry-run must NOT run the memory pass")
	}
	if !strings.Contains(m.skillDistillGate, "24 messages") {
		t.Errorf("gate detail = %q", m.skillDistillGate)
	}

	// Unknown args error — no silent /distill foo.
	m2 := gateModel(t, 24, 4)
	bad := runHandler(t, m2, "/distill now")
	if !strings.Contains(bad.text, "unknown argument") {
		t.Errorf("unknown arg must error: %q", bad.text)
	}
	if m2.skillDistilling || m2.distilling {
		t.Error("rejected invocation must start nothing")
	}
}

func TestCmdDistillDryRun_SkillSegmentUnavailable(t *testing.T) {
	m := gateModel(t, 24, 4)
	m.opts.SkillDistiller = nil
	res := runHandler(t, m, "/distill --dry-run")
	if !strings.Contains(res.text, "unavailable") {
		t.Errorf("expected unavailable note: %q", res.text)
	}
	if m.skillDistilling || m.distilling || res.extra != nil {
		t.Error("nothing must start when the skill segment is missing")
	}
}

// A failing L0 gate prints the SAME line in both modes, so the dry
// run and the real run compare cleanly at the gate boundary.
func TestCmdDistillDryRun_GateFailSameLineAsRealRun(t *testing.T) {
	dry := gateModel(t, 10, 0)
	dryRes := runHandler(t, dry, "/distill --dry-run")
	if !strings.Contains(dryRes.text, "skill drafts skipped") {
		t.Fatalf("gate failure must say what was skipped: %q", dryRes.text)
	}
	if dry.skillDistilling || dry.distilling || dryRes.extra != nil {
		t.Error("gate failure must start nothing in dry-run")
	}

	real := gateModel(t, 10, 0)
	realRes := runHandler(t, real, "/distill")
	if dryRes.text != realRes.text {
		t.Errorf("gate-fail line must be byte-identical across modes:\ndry:  %q\nreal: %q", dryRes.text, realRes.text)
	}
	if realRes.extra == nil {
		t.Error("control: real run must still fire the M pass on gate failure")
	}
}

// Real-run regression for the rejection-line fix: appendHistory's
// returned cmd must be wired, or the lines never print (the exact bug
// the dry-run comparison surface exposed).
func TestHandleSkillDistillDone_RejectionCmdsAreReturned(t *testing.T) {
	m, _ := setupSkillModel(t)
	_, rejected := dryRunFixtures()
	cmds := m.handleSkillDistillDone(skillDistillDoneMsg{rejected: rejected})
	// 3 rejection lines and no candidates → no modal, 3 printable cmds.
	if m.skillReviewOpen {
		t.Fatal("rejections alone must not open the modal")
	}
	if len(cmds) != len(rejected) {
		t.Fatalf("one cmd per rejection line expected, got %d for %d rejections", len(cmds), len(rejected))
	}
	for i, c := range cmds {
		if c == nil {
			t.Errorf("cmd %d is nil — the rejection line would never print", i)
		}
	}
}
