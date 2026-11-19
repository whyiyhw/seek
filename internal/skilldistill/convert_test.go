package skilldistill

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/skill"
)

// ---------- prompt shape (§10 #5: 语义翻转正反例) ----------

func TestConvertPromptCarriesFlipExamples(t *testing.T) {
	for _, want := range []string{
		"GOOD (tagline → description)",
		"BAD (rejected mechanically — this just copied the tagline)",
		`{"description": ""}`,
	} {
		if !strings.Contains(ConvertSystemPrompt, want) {
			t.Errorf("ConvertSystemPrompt missing %q", want)
		}
	}
	if !strings.Contains(BuildConvertUserMessage(MCandidate{Name: "n", Tagline: "tl", Content: "ct"}, "", nil), "tagline: tl") {
		t.Errorf("user message must carry the candidate tagline")
	}
}

// ---------- happy path ----------

func TestConvertToSkill_HappyPath(t *testing.T) {
	fake := chatWith(`{"description": "When a future session touches X, apply the recorded decision before acting."}`)
	d := &Distiller{Client: fake}

	cand := MCandidate{Name: "jsonl-sessions", Tagline: "sessions are JSONL", Content: "full body text", Tags: []string{"format"}}
	got, l2, err := d.ConvertToSkill(context.Background(), cand, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "jsonl-sessions" || got.Body != "full body text" {
		t.Errorf("mechanical fields must pass through: name=%q body=%q", got.Name, got.Body)
	}
	if !strings.Contains(got.Description, "When a future session") {
		t.Errorf("description = %q", got.Description)
	}
	if l2.Verdict != L2Pass {
		t.Errorf("L2 verdict = %s, want PASS", l2.Verdict)
	}
	// The request really carried the convert prompt + candidate.
	sys := fake.lastReq.Messages[0].Content
	if !strings.Contains(sys, "memory-to-skill converter") {
		t.Errorf("system prompt not the convert prompt")
	}
	if !strings.Contains(fake.lastReq.Messages[1].Content, "tagline: sessions are JSONL") {
		t.Errorf("user message missing candidate fields")
	}
}

// ---------- transport / parse failures ----------

func TestConvertToSkill_ChatError(t *testing.T) {
	d := &Distiller{Client: &fakeChatClient{err: context.Canceled}}
	_, _, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "ok-name", Tagline: "t", Content: "c"}, nil, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("want wrapped ctx error, got %v", err)
	}
}

func TestConvertToSkill_BadJSON(t *testing.T) {
	d := &Distiller{Client: chatWith("The candidate should become a skill about X.")}
	_, _, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "ok-name", Tagline: "t", Content: "c"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("want parse error, got %v", err)
	}
}

// Fenced JSON is tolerated — thinking-mode models fence despite the
// "no fences" instruction (same tolerance as every other parser).
func TestConvertToSkill_FencedJSON(t *testing.T) {
	d := &Distiller{Client: chatWith("```json\n{\"description\": \"When Y happens, follow this.\"}\n```")}
	got, _, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "ok-name", Tagline: "t", Content: "c"}, nil, nil)
	if err != nil {
		t.Fatalf("fenced JSON must parse: %v", err)
	}
	if got.Description != "When Y happens, follow this." {
		t.Errorf("description = %q", got.Description)
	}
}

// ---------- mechanical guardrails (§8.1) ----------

func TestConvertToSkill_DescriptionEqualsTaglineRejected(t *testing.T) {
	tagline := "sessions are JSONL"
	d := &Distiller{Client: chatWith(`{"description": "sessions are JSONL"}`)}
	_, _, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "ok-name", Tagline: tagline, Content: "c"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "tagline") {
		t.Fatalf("want tagline-copy rejection, got %v", err)
	}
}

// Surrounding whitespace is not a rewrite — trim before comparing.
func TestConvertToSkill_DescriptionEqualsTaglineAfterTrim(t *testing.T) {
	d := &Distiller{Client: chatWith("{\"description\": \"  sessions are JSONL \n\"}")}
	_, _, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "ok-name", Tagline: "sessions are JSONL", Content: "c"}, nil, nil)
	if err == nil {
		t.Fatal("whitespace-padded tagline copy must still be rejected")
	}
}

func TestConvertToSkill_EmptyDescriptionMeansKeepInM(t *testing.T) {
	d := &Distiller{Client: chatWith(`{"description": ""}`)}
	_, _, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "ok-name", Tagline: "t", Content: "c"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "fits M") {
		t.Fatalf("want no-trigger guidance, got %v", err)
	}
}

func TestConvertToSkill_BadNameRejectedBeforeCall(t *testing.T) {
	fake := chatWith(`{"description": "d"}`)
	d := &Distiller{Client: fake}
	if _, _, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "Not_Kebab", Tagline: "t", Content: "c"}, nil, nil); err == nil {
		t.Fatal("non-kebab name must error")
	}
	if fake.lastReq != nil {
		t.Error("name rejection must happen BEFORE the reasoner call is paid")
	}
}

// ---------- L2 novelty (§3.5: same thresholds as the funnel) ----------

// Fixture arithmetic (see distill_test.go's boundary notes): with an
// empty body the compared text normalizes to the description verbatim,
// so "abcdef" vs "zabcde" → 3/5 = 0.60 (REJECT) and "abcdefgh" vs
// "zabcdefz" → 4/8 = 0.50 (GREY) hold by hand-count.
func TestConvertToSkill_L2SameNameReject(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "dup-name", Description: "unrelated"})
	d := &Distiller{Client: chatWith(`{"description": "When X, do Y."}`)}

	got, l2, err := d.ConvertToSkill(context.Background(), MCandidate{Name: "dup-name", Tagline: "t", Content: "c"}, set, nil)
	if err != nil {
		t.Fatalf("L2Reject is not an error: %v", err)
	}
	if l2.Verdict != L2Reject {
		t.Fatalf("verdict = %s, want REJECT", l2.Verdict)
	}
	if got.Name != "" {
		t.Errorf("REJECT must return a zero candidate, got %+v", got)
	}
	if !strings.Contains(l2.Reason, "dup-name") {
		t.Errorf("reason must name the conflicting skill: %q", l2.Reason)
	}
}

func TestConvertToSkill_L2OverlapThresholds(t *testing.T) {
	cand := MCandidate{Name: "ok-name", Tagline: "unrelated tagline", Content: ""}

	// Fixture sanity FIRST (see pitfalls.md: hand-counted trigram sets
	// are wrong more often than right) — verify the sims really are
	// 0.60 / 0.50 before asserting any behaviour on them.
	if sim := trigramJaccard("abcdef", "zabcde"); sim != 0.60 {
		t.Fatalf("reject fixture sim = %v, want 0.60", sim)
	}
	if sim := trigramJaccard("abcdefgh", "zabcdefz"); sim != 0.50 {
		t.Fatalf("grey fixture sim = %v, want 0.50", sim)
	}

	// 0.60 exactly: "abcdef" (4 trigrams) vs "zabcde" (5) → 4/5.
	reject := skill.NewSet()
	reject.Add(&skill.Skill{Name: "near-dup", Description: "zabcde"})
	d := &Distiller{Client: chatWith(`{"description": "abcdef"}`)}
	if _, l2, err := d.ConvertToSkill(context.Background(), cand, reject, nil); err != nil || l2.Verdict != L2Reject {
		t.Errorf("sim 0.60 → REJECT: verdict=%s err=%v", l2.Verdict, err)
	}

	// 0.50: "abcdefgh" (6) vs "zabcdefz" (6) → 4/8.
	grey := skill.NewSet()
	grey.Add(&skill.Skill{Name: "grey-zone", Description: "zabcdefz"})
	d = &Distiller{Client: chatWith(`{"description": "abcdefgh"}`)}
	got, l2, err := d.ConvertToSkill(context.Background(), cand, grey, nil)
	if err != nil || l2.Verdict != L2Grey {
		t.Fatalf("sim 0.50 → GREY: verdict=%s err=%v", l2.Verdict, err)
	}
	if got.L2Note == "" || len(got.L2GreyName) == 0 {
		t.Errorf("GREY must ride along on the candidate: note=%q names=%v", got.L2Note, got.L2GreyName)
	}
}

func TestConvertToSkill_L2OverlapAgainstMIndex(t *testing.T) {
	// MEntry name "" + tagline "zabcde" normalizes to "zabcde" → 0.60
	// vs description "abcdef": the "this is really a recorded
	// decision" signal must fire on the convert path too.
	d := &Distiller{Client: chatWith(`{"description": "abcdef"}`)}
	_, l2, err := d.ConvertToSkill(context.Background(),
		MCandidate{Name: "ok-name", Tagline: "unrelated", Content: ""}, nil,
		[]MEntry{{Name: "", Tagline: "zabcde"}})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if l2.Verdict != L2Reject || !strings.Contains(l2.Reason, "memory entry") {
		t.Errorf("M-index overlap must REJECT with the M-layer reason: %+v", l2)
	}
}

// ---------- render ----------

func TestRenderProjectSkillMD_RoundTrip(t *testing.T) {
	md, err := RenderProjectSkillMD(
		SkillCandidate{Name: "jsonl-sessions", Description: "When reading sessions,\nfollow JSONL.", Body: "## body\ncontent"},
		[]string{"format", "session-format: v2", "cache"},
	)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	sk, err := skill.Parse(md, "test")
	if err != nil {
		t.Fatalf("rendered output must parse: %v", err)
	}
	if sk.Version != InstalledVersion {
		t.Errorf("version = %q, want %q (a live skill, not a draft)", sk.Version, InstalledVersion)
	}
	if sk.Author != "distilled" {
		t.Errorf("author = %q", sk.Author)
	}
	if strings.Contains(string(md), DraftVersion) {
		t.Errorf("project skill must not carry the draft prerelease")
	}
	if len(sk.Keywords) != 3 || sk.Keywords[1] != "session-format: v2" {
		t.Errorf("keywords = %v (block list must survive commas/colons)", sk.Keywords)
	}
	// RenderSKILLMD's contract: body carries exactly one trailing \n.
	if sk.Description != "When reading sessions,\nfollow JSONL." || sk.Body != "## body\ncontent\n" {
		t.Errorf("round-trip drift: desc=%q body=%q", sk.Description, sk.Body)
	}
}

func TestRenderProjectSkillMD_NoKeywordsOmitsKey(t *testing.T) {
	md, err := RenderProjectSkillMD(SkillCandidate{Name: "x-y", Description: "d", Body: "b"}, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(string(md), "keywords") {
		t.Errorf("no tags → no keywords key\n%s", md)
	}
}
