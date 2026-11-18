package tui

// The `s` convert-to-skill fast lane of the ⚡ review
// (feature-memory-skill-bridge §3, M-B2). These tests drive the modal
// state machine directly — key → state, async msg → state — with a
// fake reasoner client, and assert the on-disk contract of `y`:
// <project>/.seek/skills/<name>/SKILL.md, no .install.json, no M write.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skilldistill"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// convertFakeClient stands in for the reasoner: canned body, call
// counter (the same-name tests assert the call is never paid), and
// ctx-awareness so a cancelled parent ctx surfaces as an error exactly
// like the real client would.
type convertFakeClient struct {
	calls int
	resp  string
	err   error
}

func (f *convertFakeClient) Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &deepseek.ChatResponse{Choices: []deepseek.Choice{{
		Message: deepseek.Message{Content: f.resp},
	}}}, nil
}

// setupConvertModel wires a review-ready Model: tempdir project (both
// for M and for opts.CWD — WriteProjectSkill lands under CWD/.seek/
// skills), a fake-client distiller, an optional skill Set.
func setupConvertModel(t *testing.T, fake *convertFakeClient, skills *skill.Set) (Model, *memory.Project, string) {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
	cwd := t.TempDir()
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	m := New(Options{
		MemoryProject:  p,
		Skills:         skills,
		SkillDistiller: &skilldistill.Distiller{Client: fake},
		CWD:            cwd,
	})
	return m, p, cwd
}

func convertCands() []memory.Candidate {
	return []memory.Candidate{
		{Name: "jsonl-sessions", Tagline: "sessions are JSONL", Content: "the full recorded body", Tags: []string{"format"}},
		{Name: "second-cand", Tagline: "unrelated", Content: "second body"},
	}
}

// cmdLines flattens the appendHistory tea.Cmd(s) a handler returned
// into their printable bodies, so tests assert on the exact scrollback
// lines (the "takes effect next session" contract lives there).
func cmdLines(t *testing.T, cmd tea.Cmd) []string {
	t.Helper()
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case tea.BatchMsg:
		var out []string
		for _, c := range v {
			out = append(out, cmdLines(t, c)...)
		}
		return out
	default:
		// printLineMessage is unexported in bubbletea; %v on the
		// single-field struct yields "{body}".
		s := strings.TrimSuffix(strings.TrimPrefix(fmt.Sprintf("%v", v), "{"), "}")
		return []string{s}
	}
}

func firstLine(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	lines := cmdLines(t, cmd)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// ---------- entry state ----------

func TestDistillConvert_SFlipsToConvertingWithSpinner(t *testing.T) {
	m, _, _ := setupConvertModel(t, &convertFakeClient{resp: `{"description": "When reading sessions, follow the JSONL layout."}`}, nil)
	m = openReview(m, convertCands())

	out, cmd := m.handleDistillKey(keyRune('s'))
	mm := out.(Model)
	if !mm.distillConverting || mm.distillConvertIdx != 0 {
		t.Fatalf("s must enter converting state for idx 0: converting=%v idx=%d", mm.distillConverting, mm.distillConvertIdx)
	}
	if cmd == nil {
		t.Fatal("s must return the async convert tea.Cmd")
	}
	if got := mm.renderDistillReview(); !strings.Contains(got, "converting to skill") {
		t.Errorf("render must show the converting spinner line:\n%s", got)
	}
	// While converting, decision keys are inert — no double-act.
	for _, r := range []rune{'y', 'n', 'e', 's'} {
		out2, _ := mm.handleDistillKey(keyRune(r))
		m2 := out2.(Model)
		if m2.distillIdx != 0 || m2.distillSaved != 0 || m2.distillConverted != nil {
			t.Errorf("key %q acted during conversion", r)
		}
	}
}

// ---------- cross-layer same-name refusal (§2.3 / §10 #3) ----------

// The loader's Set is the merged project>user>builtin cascade; a name
// owned by ANY layer must refuse `s` before the reasoner is paid.
func TestDistillConvert_SameNameRefusedEveryLayer(t *testing.T) {
	cases := []struct {
		layer  string
		source string
	}{
		{"project", "/work/repo/.seek/skills/jsonl-sessions/SKILL.md"},
		{"user", "/Users/x/.seek/skills/jsonl-sessions/SKILL.md"},
		{"builtin", "builtin:jsonl-sessions"},
	}
	for _, c := range cases {
		t.Run(c.layer, func(t *testing.T) {
			set := skill.NewSet()
			set.Add(&skill.Skill{Name: "jsonl-sessions", Description: "existing", Source: c.source})
			fake := &convertFakeClient{resp: `{"description": "When X, do Y."}`}
			m, p, cwd := setupConvertModel(t, fake, set)
			m = openReview(m, convertCands())

			out, cmd := m.handleDistillKey(keyRune('s'))
			mm := out.(Model)
			if mm.distillConverting {
				t.Fatalf("%s-layer collision must NOT start a conversion", c.layer)
			}
			if fake.calls != 0 {
				t.Errorf("same-name refusal must happen BEFORE the reasoner call (calls=%d)", fake.calls)
			}
			if line := firstLine(t, cmd); !strings.Contains(line, "already installed") {
				t.Errorf("refusal line = %q", line)
			}

			// Nothing written anywhere, review alive, y still works.
			if _, err := os.Stat(filepath.Join(cwd, ".seek", "skills")); !os.IsNotExist(err) {
				t.Errorf("no skill dir should exist")
			}
			if _, ok := p.Get("jsonl-sessions"); ok {
				t.Errorf("refused s must not write M")
			}
			out2, _ := mm.handleDistillKey(keyRune('y'))
			m2 := out2.(Model)
			if m2.distillSaved != 1 || !m2.distillReviewOpen {
				t.Errorf("y must still work after a refused s: saved=%d open=%v", m2.distillSaved, m2.distillReviewOpen)
			}
		})
	}
}

// ---------- no distiller ----------

func TestDistillConvert_NoDistillerDegradesToLine(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	cwd := t.TempDir()
	p, _ := memory.LoadOrCreate(t.TempDir())
	m := New(Options{MemoryProject: p, CWD: cwd}) // SkillDistiller nil
	m = openReview(m, convertCands())

	out, cmd := m.handleDistillKey(keyRune('s'))
	mm := out.(Model)
	if mm.distillConverting {
		t.Fatal("nil distiller must not start converting")
	}
	if line := firstLine(t, cmd); !strings.Contains(line, "unavailable") {
		t.Errorf("line = %q", line)
	}
}

// ---------- result routing ----------

func TestDistillConvert_ErrorDegradesNotTerminates(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  distillConvertDoneMsg
		want string
	}{
		{"transport error", distillConvertDoneMsg{idx: 0, err: context.Canceled}, "convert to skill failed"},
		{"bad json", distillConvertDoneMsg{idx: 0, err: fmt.Errorf("skilldistill convert: parse: unexpected token")}, "parse"},
		{"description equals tagline", distillConvertDoneMsg{idx: 0, err: fmt.Errorf("skilldistill convert: description equals the tagline verbatim")}, "tagline"},
		{"L2 reject", distillConvertDoneMsg{idx: 0, l2: skilldistill.L2Result{Verdict: skilldistill.L2Reject, Reason: "overlaps installed skill"}}, "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, p, cwd := setupConvertModel(t, &convertFakeClient{}, nil)
			m = openReview(m, convertCands())
			m.distillConverting = true
			m.distillConvertIdx = 0

			var gotLine string
			for _, c := range m.handleDistillConvertDone(tc.msg) {
				if l := firstLine(t, c); l != "" {
					gotLine = l
				}
			}
			if !strings.Contains(gotLine, tc.want) {
				t.Errorf("line = %q, want %q", gotLine, tc.want)
			}
			if m.distillConverting || m.distillConverted != nil {
				t.Errorf("failure must return to the prompt state: converting=%v converted=%v", m.distillConverting, m.distillConverted != nil)
			}
			if !m.distillReviewOpen || m.distillIdx != 0 {
				t.Errorf("review must survive: open=%v idx=%d", m.distillReviewOpen, m.distillIdx)
			}
			// And the review still works: y saves to M, nothing to disk skills.
			out, _ := m.handleDistillKey(keyRune('y'))
			m2 := out.(Model)
			if m2.distillSaved != 1 {
				t.Errorf("y after failed convert: saved=%d", m2.distillSaved)
			}
			if _, ok := p.Get("jsonl-sessions"); !ok {
				t.Errorf("y after failed convert should write M")
			}
			if _, err := os.Stat(filepath.Join(cwd, ".seek", "skills", "jsonl-sessions")); !os.IsNotExist(err) {
				t.Errorf("failed convert must not leave a skill dir")
			}
		})
	}
}

func TestDistillConvert_PassOpensPreviewGreyCarriesNote(t *testing.T) {
	m, _, _ := setupConvertModel(t, &convertFakeClient{}, nil)
	m = openReview(m, convertCands())
	m.distillConverting = true
	m.distillConvertIdx = 0

	sc := skilldistill.SkillCandidate{
		Name:        "jsonl-sessions",
		Description: "When reading or writing sessions, follow the JSONL layout.",
		Body:        "the full recorded body",
	}
	m.handleDistillConvertDone(distillConvertDoneMsg{idx: 0, cand: sc, l2: skilldistill.L2Result{Verdict: skilldistill.L2Pass}})

	if m.distillConverted == nil {
		t.Fatal("PASS must open the preview")
	}
	got := m.renderDistillReview()
	for _, want := range []string{
		"When reading or writing sessions, follow the JSONL layout.",
		"the full recorded body",
		"[y] install to project", "[n] back", "[q] quit review",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("preview render missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "⚠") {
		t.Errorf("PASS preview must not carry a grey warning")
	}

	// GREY rides along into the preview.
	m.distillConverting = true
	m.handleDistillConvertDone(distillConvertDoneMsg{idx: 0, cand: sc, l2: skilldistill.L2Result{
		Verdict: skilldistill.L2Grey, Reason: "borderline overlap with x"}})
	if got := m.renderDistillReview(); !strings.Contains(got, "⚠") || !strings.Contains(got, "borderline overlap") {
		t.Errorf("GREY preview must show the warning:\n%s", got)
	}
}

// ---------- preview: n back / q quit ----------

func TestDistillConvert_NBacksOutYStillSavesM(t *testing.T) {
	m, p, _ := setupConvertModel(t, &convertFakeClient{}, nil)
	m = openReview(m, convertCands())
	m.distillConverted = &skilldistill.SkillCandidate{Name: "jsonl-sessions", Description: "d", Body: "b"}

	out, _ := m.handleDistillKey(keyRune('n'))
	mm := out.(Model)
	if mm.distillConverted != nil || !mm.distillReviewOpen || mm.distillIdx != 0 {
		t.Fatalf("n must back out to the prompt: converted=%v open=%v idx=%d", mm.distillConverted != nil, mm.distillReviewOpen, mm.distillIdx)
	}
	// y now saves to M — the candidate was never consumed by s.
	out2, _ := mm.handleDistillKey(keyRune('y'))
	m2 := out2.(Model)
	if m2.distillSaved != 1 || m2.distillSkilled != 0 {
		t.Errorf("y after n-back: saved=%d skilled=%d", m2.distillSaved, m2.distillSkilled)
	}
	if _, ok := p.Get("jsonl-sessions"); !ok {
		t.Errorf("y after n-back should write M")
	}
}

func TestDistillConvert_QAtPreviewQuits(t *testing.T) {
	m, _, _ := setupConvertModel(t, &convertFakeClient{}, nil)
	m = openReview(m, convertCands())
	m.distillConverted = &skilldistill.SkillCandidate{Name: "x", Description: "d", Body: "b"}

	out, _ := m.handleDistillKey(keyRune('q'))
	if mm := out.(Model); mm.distillReviewOpen {
		t.Fatal("q at preview must quit the review")
	}
}

// ---------- y: the on-disk contract ----------

func TestDistillConvert_YInstallsProjectSkill(t *testing.T) {
	fake := &convertFakeClient{resp: `{"description": "When reading or writing sessions, follow the JSONL layout before acting."}`}
	m, p, cwd := setupConvertModel(t, fake, nil)
	m = openReview(m, convertCands())

	// s → convert done (via the real cmd so the round-trip is covered)
	out, cmd := m.handleDistillKey(keyRune('s'))
	m = out.(Model)
	msg := cmd().(distillConvertDoneMsg)
	m.handleDistillConvertDone(msg)
	if m.distillConverted == nil {
		t.Fatalf("convert cmd must produce a preview: %+v err=%v", msg.cand, msg.err)
	}

	out, _ = m.handleDistillKey(keyRune('y'))
	m = out.(Model)
	if m.distillSkilled != 1 || m.distillIdx != 1 || !m.distillReviewOpen {
		t.Fatalf("install: skilled=%d idx=%d open=%v", m.distillSkilled, m.distillIdx, m.distillReviewOpen)
	}

	dir := filepath.Join(cwd, ".seek", "skills", "jsonl-sessions")
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
	if sk.Version != "0.1.0" || len(sk.Keywords) != 1 || sk.Keywords[0] != "format" {
		t.Errorf("version=%q keywords=%v", sk.Version, sk.Keywords)
	}
	if !strings.Contains(sk.Description, "When reading or writing sessions") {
		t.Errorf("description must be the rewritten one: %q", sk.Description)
	}
	if sk.Body != "the full recorded body\n" {
		t.Errorf("body must be the content verbatim: %q", sk.Body)
	}
	// Double-store stays closed: nothing in M.
	if _, ok := p.Get("jsonl-sessions"); ok {
		t.Errorf("s is save-AS — M must NOT be written")
	}
}

// Installing the LAST candidate closes the review; the returned cmd
// batch carries both the success line (with the next-session hint) and
// the done-summary with the → skill count.
func TestDistillConvert_LastCandidateInstallClosesWithSkillCount(t *testing.T) {
	m, _, cwd := setupConvertModel(t, &convertFakeClient{}, nil)
	m = openReview(m, []memory.Candidate{{Name: "only-one", Tagline: "t", Content: "c", Tags: []string{"x"}}})
	m.distillConverted = &skilldistill.SkillCandidate{Name: "only-one", Description: "d", Body: "b"}

	out, cmd := m.handleDistillKey(keyRune('y'))
	mm := out.(Model)
	if mm.distillReviewOpen {
		t.Fatal("installing the last candidate must close the review")
	}
	lines := cmdLines(t, cmd)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "skill installed: "+filepath.Join(cwd, ".seek", "skills", "only-one")) {
		t.Errorf("success line must carry the dir:\n%s", joined)
	}
	if !strings.Contains(joined, "takes effect next session") {
		t.Errorf("success line must state the effective time:\n%s", joined)
	}
	if !strings.Contains(joined, "0 saved, 0 dropped, 1 → skill") {
		t.Errorf("done summary must count the skill install:\n%s", joined)
	}
}

func TestDistillConvert_ExitSummaryCountsSkilled(t *testing.T) {
	m, _, _ := setupConvertModel(t, &convertFakeClient{}, nil)
	m = openReview(m, convertCands())
	m.distillSkilled = 2

	var summary string
	out, cmd := m.handleDistillKey(keyRune('q'))
	mm := out.(Model)
	for _, l := range cmdLines(t, cmd) {
		if strings.Contains(l, "distill review done") {
			summary = l
		}
	}
	if summary == "" {
		t.Fatal("exit must print a summary")
	}
	if !strings.Contains(summary, "2 → skill") {
		t.Errorf("summary must count skilled: %q", summary)
	}
	if mm.distillConverting || mm.distillConverted != nil {
		t.Errorf("exit must tear down s-state: converting=%v converted=%v", mm.distillConverting, mm.distillConverted != nil)
	}
}

// ---------- write failure degrades, review survives ----------

func TestDistillConvert_WriteFailureDoesNotTerminate(t *testing.T) {
	m, p, cwd := setupConvertModel(t, &convertFakeClient{}, nil)
	m = openReview(m, convertCands())

	// Occupy the project slot so WriteProjectSkill refuses — the
	// same-name defence at the filesystem layer (M-B1).
	if err := os.MkdirAll(filepath.Join(cwd, ".seek", "skills", "jsonl-sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.distillConverted = &skilldistill.SkillCandidate{Name: "jsonl-sessions", Description: "d", Body: "b"}

	out, _ := m.handleDistillKey(keyRune('y'))
	mm := out.(Model)
	if mm.distillReviewOpen != true || mm.distillIdx != 1 {
		t.Fatalf("write failure must degrade-and-advance: open=%v idx=%d", mm.distillReviewOpen, mm.distillIdx)
	}
	if mm.distillSkilled != 0 || mm.distillDropped != 1 {
		t.Errorf("failed install counts dropped: skilled=%d dropped=%d", mm.distillSkilled, mm.distillDropped)
	}
	// The second candidate is still reviewable.
	out2, _ := mm.handleDistillKey(keyRune('y'))
	m2 := out2.(Model)
	if m2.distillSaved != 1 {
		t.Errorf("second candidate must still save: %d", m2.distillSaved)
	}
	if _, ok := p.Get("second-cand"); !ok {
		t.Errorf("second candidate should be in M")
	}
}

// ---------- stale-result guard ----------

// q during the in-flight conversion closes the review; the late result
// must be dropped — INCLUDING when a fresh review has since reopened at
// the same idx (idx alone is not a valid guard; converting is).
func TestDistillConvert_QDuringConvertDropsLateResult(t *testing.T) {
	fake := &convertFakeClient{resp: `{"description": "When X, do Y."}`}
	m, _, _ := setupConvertModel(t, fake, nil)
	m = openReview(m, convertCands())

	out, _ := m.handleDistillKey(keyRune('s'))
	m = out.(Model)
	if !m.distillConverting {
		t.Fatal("precondition: converting")
	}
	out, _ = m.handleDistillKey(keyRune('q'))
	m = out.(Model)
	if m.distillReviewOpen {
		t.Fatal("q during convert must abort the review")
	}

	// Late result arrives for the closed review: ignored.
	m.handleDistillConvertDone(distillConvertDoneMsg{idx: 0, cand: skilldistill.SkillCandidate{Name: "jsonl-sessions"}, l2: skilldistill.L2Result{Verdict: skilldistill.L2Pass}})
	if m.distillConverted != nil {
		t.Fatal("late result for a closed review must be dropped")
	}

	// The trap: a NEW review reopens at idx 0 — the stale msg still
	// must not splice in (guard is !converting, not idx != msg.idx).
	m = openReview(m, convertCands())
	m.handleDistillConvertDone(distillConvertDoneMsg{idx: 0, cand: skilldistill.SkillCandidate{Name: "jsonl-sessions"}, l2: skilldistill.L2Result{Verdict: skilldistill.L2Pass}})
	if m.distillConverted != nil {
		t.Fatal("stale result must not splice into a fresh review at the same idx")
	}
}

// ---------- ctx propagation (90s bound rides on opts.Ctx) ----------

func TestDistillConvert_CmdRespectsCancelledParentCtx(t *testing.T) {
	fake := &convertFakeClient{resp: `{"description": "d"}`}
	t.Setenv("SEEK_HOME", t.TempDir())
	cwd := t.TempDir()
	p, _ := memory.LoadOrCreate(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New(Options{
		MemoryProject:  p,
		SkillDistiller: &skilldistill.Distiller{Client: fake},
		CWD:            cwd,
		Ctx:            ctx,
	})
	m = openReview(m, convertCands())

	out, cmd := m.handleDistillKey(keyRune('s'))
	mm := out.(Model)
	msg := cmd().(distillConvertDoneMsg)
	if msg.err == nil {
		t.Fatalf("cancelled parent ctx must surface as the convert error: %+v", msg.cand)
	}
	mm.handleDistillConvertDone(msg)
	if mm.distillConverted != nil || !mm.distillReviewOpen {
		t.Errorf("ctx-cancelled convert must degrade to the prompt: converted=%v open=%v", mm.distillConverted != nil, mm.distillReviewOpen)
	}
}
