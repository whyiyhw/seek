package skilldistill

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// fakeChatClient mirrors memory's: canned response + captured request,
// so the extraction and filter paths test without a real backend.
type fakeChatClient struct {
	lastReq *deepseek.ChatRequest
	resp    *deepseek.ChatResponse
	err     error
}

func (f *fakeChatClient) Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	f.lastReq = req
	return f.resp, f.err
}

func chatWith(content string) *fakeChatClient {
	return &fakeChatClient{resp: &deepseek.ChatResponse{Choices: []deepseek.Choice{{
		Message: deepseek.Message{Content: content},
	}}}}
}

// ---------- L0 mechanical gate ----------

func gateHistory(messages int, toolCalls int) []deepseek.Message {
	var h []deepseek.Message
	calls := 0
	for i := 0; i < messages; i++ {
		m := deepseek.Message{Role: deepseek.RoleUser, Content: "message body"}
		if calls < toolCalls && i%3 == 0 {
			m.Role = deepseek.RoleAssistant
			m.ToolCalls = []deepseek.ToolCall{{ID: "c1", Function: deepseek.ToolCallFunc{Name: "bash"}}}
			calls++
		}
		h = append(h, m)
	}
	return h
}

func TestMechanicalGate(t *testing.T) {
	cases := []struct {
		name     string
		messages int
		calls    int
		wantPass bool
	}{
		{"pure Q&A under threshold", 19, 0, false},
		{"enough messages no tools", 30, 2, false},
		{"tools but too short", 10, 5, false},
		{"exactly at threshold", GateMinMessages, GateMinToolCalls, true},
		{"comfortably procedural", 40, 8, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MechanicalGate(gateHistory(c.messages, c.calls))
			if got.Pass != c.wantPass {
				t.Errorf("Pass = %v (detail %q), want %v", got.Pass, got.Detail, c.wantPass)
			}
		})
	}
}

func TestMechanicalGate_SystemMessagesDontCount(t *testing.T) {
	var h []deepseek.Message
	for i := 0; i < 25; i++ {
		h = append(h, deepseek.Message{Role: deepseek.RoleSystem, Content: "system"})
	}
	h = append(h, gateHistory(3, 0)...)
	if got := MechanicalGate(h); got.Pass {
		t.Errorf("system messages must not count toward the gate; got Pass with detail %q", got.Detail)
	}
}

func TestMechanicalGate_PitfallBoost(t *testing.T) {
	h := gateHistory(25, 4)
	h = append(h, deepseek.Message{Role: deepseek.RoleAssistant, Content: "committed with Pitfall: loader skips dot dirs"})
	got := MechanicalGate(h)
	if !got.Pass || !got.Boost {
		t.Errorf("Pass=%v Boost=%v, want true/true", got.Pass, got.Boost)
	}
}

// ---------- parsing ----------

func TestParseSkillCandidates_Shapes(t *testing.T) {
	valid := `{"name":"fix-tui-bug","description":"When a modal swallows keys...","body":"## When to use\n...","evidence":["tool call: bash"]}`

	cases := []struct {
		name      string
		raw       string
		wantKept  int
		wantRej   int
		wantError bool
	}{
		{"plain array", `[` + valid + `]`, 1, 0, false},
		{"code fence", "```json\n[" + valid + `]` + "\n```", 1, 0, false},
		{"leading prose", "Here you go:\n[" + valid + `]`, 1, 0, false},
		{"single object", valid, 1, 0, false},
		{"empty array", `[]`, 0, 0, false},
		{"missing name", `[{"description":"d","body":"b"}]`, 0, 1, false},
		{"bad kebab name", `[{"name":"Fix_TUI","description":"d","body":"b"}]`, 0, 1, false},
		{"missing description", `[{"name":"ok-name","body":"b"}]`, 0, 1, false},
		{"missing body", `[{"name":"ok-name","description":"d"}]`, 0, 1, false},
		{"not json at all", `I found nothing worth keeping.`, 0, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kept, rejected, err := ParseSkillCandidates(c.raw)
			if c.wantError {
				if err == nil {
					t.Fatalf("expected error, got kept=%d rejected=%d", len(kept), len(rejected))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(kept) != c.wantKept {
				t.Errorf("kept = %d, want %d", len(kept), c.wantKept)
			}
			if len(rejected) != c.wantRej {
				t.Errorf("rejected = %d, want %d", len(rejected), c.wantRej)
			}
		})
	}
}

// ---------- evidence verification ----------

func TestVerifyEvidence(t *testing.T) {
	transcript := "user: help me fix the TUI\nassistant: [tool call: bash(go test ./...)]\n[tool result r1]: ok, all green\n"
	cases := []struct {
		name     string
		evidence []string
		wantMiss int
	}{
		{"verbatim hit", []string{"[tool result r1]: ok, all green"}, 0},
		{"multiple all present", []string{"user: help me fix the TUI", "[tool call: bash(go test ./...)]"}, 0},
		{"one hallucinated", []string{"user: help me fix the TUI", "assistant: I reformatted the entire disk"}, 1},
		{"empty quotes ignored", []string{"", "  "}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			missing := VerifyEvidence(c.evidence, transcript)
			if len(missing) != c.wantMiss {
				t.Errorf("missing = %d (%v), want %d", len(missing), missing, c.wantMiss)
			}
		})
	}
}

// ---------- L2 mechanical dedup ----------

func skillSetWith(t *testing.T, names ...string) *skill.Set {
	t.Helper()
	set := skill.NewSet()
	for _, n := range names {
		set.Add(&skill.Skill{Name: n, Description: "desc " + n})
	}
	return set
}

// nearly-identical texts should cross the reject threshold; disjoint
// texts should not. tuned by hand against trigramJaccard.
const l2ExistingDesc = "When fixing TUI state machine bugs where a modal key handler swallows keys, reproduce first with a test"
const l2SameDesc = "When fixing TUI state machine bugs where a modal's key handler swallows keys, follow a reproduce-first sequence"
const l2OtherDesc = "When a git rebase drops commits mid-stack, verify the reflog before force-pushing anything"

func TestCheckNovelty_NameCollision(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "fix-tui-bug", Description: "unrelated words"})
	cand := SkillCandidate{Name: "fix-tui-bug", Description: l2OtherDesc, Body: "steps"}

	got := CheckNovelty(cand, set, nil)
	if got.Verdict != L2Reject {
		t.Fatalf("Verdict = %v, want REJECT", got.Verdict)
	}
	if !strings.Contains(got.Reason, "fix-tui-bug") {
		t.Errorf("reason should name the colliding skill: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "update") {
		t.Errorf("reason should point at the update flow: %q", got.Reason)
	}
}

func TestCheckNovelty_SimilarityThresholds(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "fix-tui-state-bug", Description: l2ExistingDesc})

	cases := []struct {
		name       string
		desc       string
		wantVerd   L2Verdict
		wantReason string
	}{
		{"same thing", l2SameDesc, L2Reject, "fix-tui-state-bug"},
		{"disjoint topic", l2OtherDesc, L2Pass, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckNovelty(SkillCandidate{Name: "other-name", Description: c.desc, Body: "## Steps\n1. do it"}, set, nil)
			if got.Verdict != c.wantVerd {
				t.Fatalf("Verdict = %v (sim reason %q), want %v", got.Verdict, got.Reason, c.wantVerd)
			}
			if c.wantReason != "" && !strings.Contains(got.Reason, c.wantReason) {
				t.Errorf("reason %q should mention %q", got.Reason, c.wantReason)
			}
		})
	}
}

// The grey zone must produce GREY (kept, flagged), not a rejection —
// the handoff to L3 (auto) or the human (manual) is the whole point
// of the band's existence.
func TestCheckNovelty_GreyZone(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "tui-key-fixes", Description: l2ExistingDesc})
	mIndex := []MEntry{{Name: "tui-key-handler", Tagline: "modal key handlers must restore focus on exit"}}

	cand := SkillCandidate{
		Name:        "repro-tui-flake-serially",
		Description: "When TUI state machine bugs make a modal swallow keys, write the reproduction test first before any fix",
		Body:        "## Steps\n1. do it",
	}
	got := CheckNovelty(cand, set, mIndex)
	if got.Verdict != L2Grey {
		t.Fatalf("expected GREY for partial overlap, got %v (reason %q, best only?)", got.Verdict, got.Reason)
	}
	if len(got.GreyNames) == 0 {
		t.Errorf("GREY must carry the flagged names for L3 context")
	}
}

func TestCheckNovelty_MemoryOverlapRejected(t *testing.T) {
	mIndex := []MEntry{{Name: "jsonl-session-format", Tagline: "sessions are JSONL because prefix-cache bytes must stay identical"}}
	// A "skill" whose body mostly restates the recorded decision.
	cand := SkillCandidate{
		Name:        "keep-sessions-jsonl",
		Description: "sessions are JSONL because prefix-cache bytes must stay identical",
		Body:        "## Steps\n1. write sessions are JSONL because prefix-cache bytes must stay identical lines",
	}
	got := CheckNovelty(cand, nil, mIndex)
	if got.Verdict != L2Reject {
		t.Fatalf("expected REJECT for M-entry overlap, got %v (reason %q)", got.Verdict, got.Reason)
	}
	if !strings.Contains(got.Reason, "jsonl-session-format") {
		t.Errorf("rejection reason must point at the M entry, got %q", got.Reason)
	}
}

// ---------- trigram Jaccard sanity ----------

func TestTrigramJaccard(t *testing.T) {
	cases := []struct {
		a, b string
		want float64 // -1 = don't assert exact, just bounds below
	}{
		{"identical text here", "identical text here", 1.0},
		{"completely different words entirely", "unrelated symbols elsewhere", -1},
		{"", "anything", 0},
		{"ab", "ab", 0}, // shorter than a trigram
	}
	for _, c := range cases {
		got := trigramJaccard(c.a, c.b)
		if c.want >= 0 && got != c.want {
			t.Errorf("jaccard(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if sim := trigramJaccard("completely different words entirely", "unrelated symbols elsewhere"); sim >= SimilarityGrey {
		t.Errorf("disjoint texts scored %v, should be far below grey %v", sim, SimilarityGrey)
	}
	// Case and whitespace folding: same text in different casing scores 1.
	if sim := trigramJaccard("Fix The TUI Bug", "fix  the\ntui  bug"); sim != 1.0 {
		t.Errorf("case/space folding failed: %v", sim)
	}
}

// ---------- extraction end-to-end (fake reasoner) ----------

func extractionHistory() []deepseek.Message {
	var h []deepseek.Message
	h = append(h, deepseek.Message{Role: deepseek.RoleSystem, Content: "sysprompt"})
	for i := 0; i < 12; i++ {
		h = append(h, deepseek.Message{Role: deepseek.RoleUser, Content: "fix the flaky TUI test"})
		h = append(h, deepseek.Message{
			Role:    deepseek.RoleAssistant,
			Content: "running the reproduction first",
			ToolCalls: []deepseek.ToolCall{{
				ID:       "c1",
				Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"cmd":"go test ./internal/tui/ -run Flaky"}`},
			}},
		})
		h = append(h, deepseek.Message{Role: deepseek.RoleTool, ToolCallID: "c1", Content: "--- FAIL: TestFlaky"})
	}
	return h
}

const goodCandidateJSON = `[
  {
    "name": "repro-flaky-tui-first",
    "description": "When a TUI test flakes only under the race detector, reproduce it single-threaded before touching the state machine",
    "body": "## When to use\nA TUI test passes alone but fails under -race\n## Steps\n1. run go test ./internal/tui/ -run Flaky without -race\n2. only edit the state machine once it reproduces",
    "evidence": ["running the reproduction first", "--- FAIL: TestFlaky"]
  }
]`

func TestExtract_HappyPath(t *testing.T) {
	fake := chatWith(goodCandidateJSON)
	d := &Distiller{Client: fake}
	kept, rejected, err := d.Extract(context.Background(), extractionHistory(), "manifest text", nil, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(rejected) != 0 {
		t.Fatalf("unexpected rejections: %v", rejected)
	}
	if len(kept) != 1 {
		t.Fatalf("kept = %d, want 1", len(kept))
	}
	if kept[0].Name != "repro-flaky-tui-first" {
		t.Errorf("name = %q", kept[0].Name)
	}
	// The prompt must carry the dedup context (L1).
	user := fake.lastReq.Messages[1].Content
	for _, want := range []string{"manifest text", "=== conversation ==="} {
		if !strings.Contains(user, want) {
			t.Errorf("user message missing %q", want)
		}
	}
}

func TestExtract_EmptyArrayIsFine(t *testing.T) {
	d := &Distiller{Client: chatWith(`[]`)}
	kept, rejected, err := d.Extract(context.Background(), extractionHistory(), "", nil, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(kept) != 0 || len(rejected) != 0 {
		t.Errorf("kept=%d rejected=%d, want 0/0", len(kept), len(rejected))
	}
}

// A candidate whose evidence quotes don't exist in the transcript is
// presumed hallucinated and must not reach the review modal.
func TestExtract_HallucinatedEvidenceRejected(t *testing.T) {
	bad := `[{"name":"reorganize-monorepo","description":"When the repo grows past 50 packages, reorganize into a monorepo","body":"## Steps\n1. move dirs","evidence":["assistant: let us reorganize everything into a monorepo now"]}]`
	d := &Distiller{Client: chatWith(bad)}
	kept, rejected, err := d.Extract(context.Background(), extractionHistory(), "", nil, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("hallucinated candidate must not survive, kept=%v", kept)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0].Reason, "evidence") {
		t.Errorf("rejection should cite evidence, got %v", rejected)
	}
}

func TestExtract_NameCollisionRejected(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "repro-flaky-tui-first", Description: "already installed"})
	d := &Distiller{Client: chatWith(goodCandidateJSON)}
	kept, rejected, err := d.Extract(context.Background(), extractionHistory(), "", nil, set)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("colliding candidate must be rejected, kept=%v", kept)
	}
	if len(rejected) != 1 || !strings.Contains(rejected[0].Reason, "conflicts") {
		t.Errorf("rejection should cite the conflict, got %v", rejected)
	}
}

func TestExtract_GreyCandidateCarriesNote(t *testing.T) {
	set := skill.NewSet()
	set.Add(&skill.Skill{Name: "flaky-test-routine", Description: "When a TUI test flakes under the race detector, reproduce single-threaded before touching the state machine"})
	d := &Distiller{Client: chatWith(goodCandidateJSON)}
	kept, _, err := d.Extract(context.Background(), extractionHistory(), "", nil, set)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("grey candidate should be kept for review, got %d", len(kept))
	}
	if kept[0].L2Note == "" {
		t.Errorf("grey candidate must carry its L2 warning for the modal + sidecar")
	}
}

func TestExtract_ClientErrors(t *testing.T) {
	d := &Distiller{Client: &fakeChatClient{err: errors.New("boom")}}
	if _, _, err := d.Extract(context.Background(), extractionHistory(), "", nil, nil); err == nil {
		t.Fatal("expected error from failed chat")
	}
	d2 := &Distiller{Client: &fakeChatClient{}} // no choices
	if _, _, err := d2.Extract(context.Background(), extractionHistory(), "", nil, nil); err == nil {
		t.Fatal("expected error from empty response")
	}
	d3 := &Distiller{} // no client
	if _, _, err := d3.Extract(context.Background(), extractionHistory(), "", nil, nil); err == nil {
		t.Fatal("expected error from missing client")
	}
}

func TestExtract_MalformedJSONIsErrorNotPanic(t *testing.T) {
	d := &Distiller{Client: chatWith("this is not json")}
	if _, _, err := d.Extract(context.Background(), extractionHistory(), "", nil, nil); err == nil {
		t.Fatal("garbage response must be an error (manual path shows it; auto path logs it)")
	}
}

// ---------- L3 filter (fake reasoner) ----------

func TestFilter_AcceptReject(t *testing.T) {
	cand := SkillCandidate{Name: "repro-flaky-tui-first", Description: "d", Body: "b"}

	accept := &Distiller{Client: chatWith(`{"decision":"ACCEPT","reason":"concrete and novel"}`)}
	res, why, err := accept.Filter(context.Background(), cand, "manifest", nil, nil)
	if err != nil || res != FilterAccept {
		t.Fatalf("accept case: res=%v err=%v", res, err)
	}
	if why != FilterReason("concrete and novel") {
		t.Errorf("reason = %q", why)
	}

	reject := &Distiller{Client: chatWith("```json\n{\"decision\":\"REJECT\",\"reason\":\"duplicate of flaky-test-routine\"}\n```")}
	res, why, err = reject.Filter(context.Background(), cand, "manifest", nil, []string{"body of flaky-test-routine"})
	if err != nil || res != FilterReject {
		t.Fatalf("reject case: res=%v err=%v", res, err)
	}
	if !strings.Contains(string(why), "duplicate") {
		t.Errorf("reason = %q", why)
	}
	// Grey bodies ride along in the filter context.
	user := reject.Client.(*fakeChatClient).lastReq.Messages[1].Content
	if !strings.Contains(user, "body of flaky-test-routine") {
		t.Errorf("filter context must include grey-zone skill bodies")
	}
}

func TestFilter_BadDecisionIsError(t *testing.T) {
	d := &Distiller{Client: chatWith(`{"decision":"MAYBE"}`)}
	if _, _, err := d.Filter(context.Background(), SkillCandidate{Name: "x", Description: "d"}, "", nil, nil); err == nil {
		t.Fatal("unknown decision must be an error")
	}
	d2 := &Distiller{Client: chatWith(`not json`)}
	if _, _, err := d2.Filter(context.Background(), SkillCandidate{Name: "x", Description: "d"}, "", nil, nil); err == nil {
		t.Fatal("unparseable filter response must be an error")
	}
}

// ---------- rendering ----------

func TestRenderSKILLMD_ParsesAsLegalPackage(t *testing.T) {
	cand := SkillCandidate{
		Name:        "repro-flaky-tui-first",
		Description: "When a TUI test flakes only under -race,\nreproduce it single-threaded first",
		Body:        "## When to use\nflaky TUI test\n## Steps\n1. run without -race\n## Verification\ngo test green",
		Evidence:    []string{"--- FAIL: TestFlaky"},
	}
	md, err := RenderSKILLMD(cand)
	if err != nil {
		t.Fatalf("RenderSKILLMD: %v", err)
	}
	sk, err := skill.Parse(md, "draft-test")
	if err != nil {
		t.Fatalf("rendered draft must parse: %v\n---\n%s", err, md)
	}
	if sk.Name != cand.Name {
		t.Errorf("name = %q, want %q", sk.Name, cand.Name)
	}
	if !strings.Contains(sk.Description, "reproduce it single-threaded first") {
		t.Errorf("multi-line description lost a line: %q", sk.Description)
	}
	if sk.Version != DraftVersion || sk.Author != "distilled" {
		t.Errorf("version=%q author=%q", sk.Version, sk.Author)
	}
	if !strings.Contains(sk.Body, "## Verification") {
		t.Errorf("body template section missing: %q", sk.Body)
	}
}
