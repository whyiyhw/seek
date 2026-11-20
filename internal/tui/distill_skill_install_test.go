package tui

// The 🛠 review's `s` direct-install lane (feature-memory-skill-bridge
// §4 + §10 #6/#7, M-B3). These tests pin the on-disk contract
// (<project>/.seek/skills/<name>/SKILL.md, installed version, no
// sidecar, no draft), the ZERO-conversion-call guarantee (a fake
// reasoner client with a call counter must stay at 0), and the `y`
// draft lane's byte-for-byte regression (the slow lane is untouched by
// the fast lane's arrival).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skilldistill"
)

// setupInstallModel wires a skill-review Model whose `s` writes land in
// a temp CWD (WriteProjectSkill defaults to the real process cwd when
// opts.CWD is empty — the one thing these tests must never let happen).
// The fake client exists to be NOT called: `s` is render+write, zero
// reasoner round-trips.
func setupInstallModel(t *testing.T, fake *convertFakeClient) (Model, string, string) {
	t.Helper()
	m, draftsDir := setupSkillModel(t)
	cwd := t.TempDir()
	m.opts.CWD = cwd
	m.opts.SkillDistiller = &skilldistill.Distiller{Client: fake}
	return m, draftsDir, cwd
}

// The core §10 #6 contract: `s` installs straight to project level
// with zero conversion calls; the file parses, carries the installed
// (non-draft) version, no keywords (🛠 candidates have no tags), no
// .install.json, and nothing touches the drafts area.
func TestSkillReview_SInstallsDirectToProject(t *testing.T) {
	fake := &convertFakeClient{resp: `{"description": "never asked for"}`}
	m, draftsDir, cwd := setupInstallModel(t, fake)
	cand := sampleSkillCandidate("repro-flaky-first")
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{cand, sampleSkillCandidate("second-one")},
	})

	// Opening line advertises both lanes (§4 prompt-line update).
	opened := m.renderSkillReview()
	if !strings.Contains(opened, "[y] save draft  [s] install to project  [n] drop  [q] quit") {
		t.Errorf("modal footer must advertise the s lane:\n%s", opened)
	}

	out, cmd := m.handleSkillDistillKey(keyRune('s'))
	mm := out.(Model)
	if mm.skillInstalled != 1 || mm.skillIdx != 1 || !mm.skillReviewOpen {
		t.Fatalf("s: installed=%d idx=%d open=%v", mm.skillInstalled, mm.skillIdx, mm.skillReviewOpen)
	}
	if fake.calls != 0 {
		t.Errorf("s must make ZERO conversion calls, got %d", fake.calls)
	}

	dir := filepath.Join(cwd, ".seek", "skills", "repro-flaky-first")
	md, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md must land in <project>/.seek/skills: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".install.json")); !os.IsNotExist(err) {
		t.Errorf("project skills carry NO .install.json (§2.1)")
	}
	sk, err := skill.Parse(md, "test")
	if err != nil {
		t.Fatalf("installed file must parse: %v", err)
	}
	if sk.Version != skilldistill.InstalledVersion {
		t.Errorf("version=%q, want installed %q (not the draft prerelease)", sk.Version, skilldistill.InstalledVersion)
	}
	if len(sk.Keywords) != 0 {
		t.Errorf("skill candidates carry no tags — keywords must be empty, got %v", sk.Keywords)
	}
	if sk.Description != cand.Description || sk.Body != cand.Body+"\n" {
		t.Errorf("description/body must be the candidate's verbatim (description already IS the when-to-use):\n%q\n%q", sk.Description, sk.Body)
	}

	// Success line carries the effective-time hint (§10 #7) and the dir.
	lines := cmdLines(t, cmd)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "skill installed: "+dir) {
		t.Errorf("success line must carry the dir:\n%s", joined)
	}
	if !strings.Contains(joined, "takes effect next session") {
		t.Errorf("success line must state the effective time:\n%s", joined)
	}

	// The drafts area stays untouched by `s` — the slow lane is `y`'s.
	if _, err := os.Stat(filepath.Join(draftsDir, ".drafts", "repro-flaky-first")); !os.IsNotExist(err) {
		t.Error("s must not leave a draft behind")
	}
}

// Installing the LAST candidate closes the review; the summary counts
// the install (mirrors the ⚡ review's “N → skill” tail).
func TestSkillReview_SLastCandidateClosesWithInstalledCount(t *testing.T) {
	m, _, cwd := setupInstallModel(t, &convertFakeClient{})
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{sampleSkillCandidate("only-one")},
	})

	out, cmd := m.handleSkillDistillKey(keyRune('s'))
	mm := out.(Model)
	if mm.skillReviewOpen {
		t.Fatal("installing the last candidate must close the review")
	}
	joined := strings.Join(cmdLines(t, cmd), "\n")
	if !strings.Contains(joined, "skill installed: "+filepath.Join(cwd, ".seek", "skills", "only-one")) {
		t.Errorf("success line must carry the dir:\n%s", joined)
	}
	if !strings.Contains(joined, "takes effect next session") {
		t.Errorf("success line must state the effective time:\n%s", joined)
	}
	if !strings.Contains(joined, "0 draft(s) saved, 0 dropped, 1 installed") {
		t.Errorf("done summary must count the install:\n%s", joined)
	}
}

// §2.3 caller-side contract: any layer already owning the name refuses
// `s` — error line, NO advance (y/n stay live: the follow-up `y`
// proves the draft lane still works), zero writes, zero calls.
func TestSkillReview_SSameNameRefused_NoAdvance(t *testing.T) {
	fake := &convertFakeClient{}
	m, draftsDir, cwd := setupInstallModel(t, fake)
	m.opts.Skills.Add(&skill.Skill{Name: "repro-flaky-first", Description: "existing"})
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{sampleSkillCandidate("repro-flaky-first")},
	})

	out, cmd := m.handleSkillDistillKey(keyRune('s'))
	mm := out.(Model)
	if mm.skillIdx != 0 || mm.skillReviewOpen != true {
		t.Fatalf("same-name refusal must stay on the candidate: idx=%d open=%v", mm.skillIdx, mm.skillReviewOpen)
	}
	if line := firstLine(t, cmd); !strings.Contains(line, "already installed") {
		t.Errorf("refusal must say the name is taken:\n%s", line)
	}
	if fake.calls != 0 {
		t.Errorf("refusal is pre-render — zero calls expected, got %d", fake.calls)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".seek", "skills", "repro-flaky-first")); !os.IsNotExist(err) {
		t.Error("refused s must not write the project slot")
	}

	// y still works after the refusal — the draft lane is unharmed.
	out, _ = mm.handleSkillDistillKey(keyRune('y'))
	m2 := out.(Model)
	if m2.skillSaved != 1 || m2.skillReviewOpen {
		t.Fatalf("y after refused s: saved=%d open=%v", m2.skillSaved, m2.skillReviewOpen)
	}
	if _, err := os.Stat(filepath.Join(draftsDir, ".drafts", "repro-flaky-first", "SKILL.md")); err != nil {
		t.Errorf("y after refused s must still write the draft: %v", err)
	}
}

// Write failures degrade exactly like `y`'s render failures: error
// line, count as dropped, advance — the review survives.
func TestSkillReview_SWriteFailureDegrades(t *testing.T) {
	m, _, cwd := setupInstallModel(t, &convertFakeClient{})
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{sampleSkillCandidate("taken-slot"), sampleSkillCandidate("next-up")},
	})
	// Occupy the project slot so WriteProjectSkill refuses (M-B1's
	// filesystem-layer same-name defence).
	if err := os.MkdirAll(filepath.Join(cwd, ".seek", "skills", "taken-slot"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, cmd := m.handleSkillDistillKey(keyRune('s'))
	mm := out.(Model)
	if !mm.skillReviewOpen || mm.skillIdx != 1 {
		t.Fatalf("write failure must degrade-and-advance: open=%v idx=%d", mm.skillReviewOpen, mm.skillIdx)
	}
	if mm.skillInstalled != 0 || mm.skillDropped != 1 {
		t.Errorf("failed install counts dropped: installed=%d dropped=%d", mm.skillInstalled, mm.skillDropped)
	}
	if line := firstLine(t, cmd); !strings.Contains(line, "install skill") || !strings.Contains(line, "failed") {
		t.Errorf("write failure must print an error line:\n%s", line)
	}
}

// §10 #6's second half — the `y` draft lane is byte-for-byte what it
// was before `s` existed: the draft SKILL.md equals RenderSKILLMD's
// output exactly, and the sidecar keeps the manual-path contract.
func TestSkillReview_YDraftBytesUnchangedWithSPresent(t *testing.T) {
	m, draftsDir, cwd := setupInstallModel(t, &convertFakeClient{})
	cand := sampleSkillCandidate("draft-bytes-frozen")
	_ = m.handleSkillDistillDone(skillDistillDoneMsg{
		candidates: []skilldistill.SkillCandidate{cand},
	})

	out, _ := m.handleSkillDistillKey(keyRune('y'))
	mm := out.(Model)
	if mm.skillSaved != 1 || mm.skillInstalled != 0 {
		t.Fatalf("y: saved=%d installed=%d", mm.skillSaved, mm.skillInstalled)
	}

	want, err := skilldistill.RenderSKILLMD(cand)
	if err != nil {
		t.Fatalf("render expected draft bytes: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(draftsDir, ".drafts", "draft-bytes-frozen", "SKILL.md"))
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("y draft bytes must be unchanged by the s lane:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
	// And the draft lane never writes the project tree.
	if _, err := os.Stat(filepath.Join(cwd, ".seek", "skills")); !os.IsNotExist(err) {
		t.Error("y must not create the project skills tree")
	}
}
