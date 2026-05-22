package memory

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestBuildDreamUserMessage_MentionsN2Rule(t *testing.T) {
	out := BuildDreamUserMessage(DreamInput{})
	if !strings.Contains(out, "≥2") {
		t.Errorf("dream prompt should reference the ≥2 sources rule, got %q", truncate(out, 200))
	}
}

func TestBuildDreamUserMessage_RendersProjectsAndSessions(t *testing.T) {
	in := DreamInput{
		Projects: []DreamProject{
			{ID: "proj-a", Entries: []Entry{
				{Name: "alpha", Tagline: "A pattern", Content: "rationale a"},
				{Name: "beta", Tagline: "B pattern", Content: "rationale b", Stale: true},
			}},
		},
		Sessions: []DreamSession{
			{ID: "sess-1", Messages: []deepseek.Message{
				{Role: deepseek.RoleUser, Content: "I want to use tabs"},
				{Role: deepseek.RoleAssistant, Content: "OK"},
			}},
		},
	}
	out := BuildDreamUserMessage(in)
	if !strings.Contains(out, "proj-a") || !strings.Contains(out, "A pattern") {
		t.Errorf("project content missing from dream prompt: %q", out)
	}
	if strings.Contains(out, "B pattern") {
		t.Errorf("stale entry leaked into dream prompt: %q", out)
	}
	if !strings.Contains(out, "sess-1") || !strings.Contains(out, "I want to use tabs") {
		t.Errorf("session content missing: %q", out)
	}
}

func TestParseLCandidates_HappyArray(t *testing.T) {
	raw := `[{"trait":"prefers tabs","why":"observed everywhere","sources":["a","b"]}]`
	got, err := ParseLCandidates(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].Trait != "prefers tabs" {
		t.Errorf("unexpected parse: %+v", got)
	}
	if len(got[0].Sources) != 2 {
		t.Errorf("sources lost: %+v", got[0])
	}
}

func TestParseLCandidates_FencedJSON(t *testing.T) {
	raw := "```json\n[{\"trait\":\"x\",\"why\":\"y\",\"sources\":[\"a\",\"b\"]}]\n```"
	got, err := ParseLCandidates(raw)
	if err != nil || len(got) != 1 {
		t.Fatalf("fenced parse failed: %v %+v", err, got)
	}
}

func TestParseLCandidates_SingleObject(t *testing.T) {
	raw := `{"trait":"solo","why":"y","sources":["a","b"]}`
	got, err := ParseLCandidates(raw)
	if err != nil || len(got) != 1 {
		t.Fatalf("single-object parse failed: %v %+v", err, got)
	}
}

func TestParseLCandidates_NamelessSingleObjectReturnsNil(t *testing.T) {
	got, err := ParseLCandidates(`{"why":"x","sources":["a","b"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != nil {
		t.Errorf("trait-less single object should produce nil, got %+v", got)
	}
}

func TestFilterByEvidence_DropsSingleSource(t *testing.T) {
	in := []LCandidate{
		{Trait: "should keep", Sources: []string{"a", "b"}},
		{Trait: "single source", Sources: []string{"only"}},
		{Trait: "dup source", Sources: []string{"a", "a", "a"}}, // dedupes to 1
		{Trait: "3 distinct", Sources: []string{"a", "b", "c"}},
		{Trait: "no sources", Sources: nil},
	}
	out := FilterByEvidence(in, 2)
	if len(out) != 2 {
		t.Fatalf("expected 2 candidates after filter, got %d (%+v)", len(out), out)
	}
	if out[0].Trait != "should keep" || out[1].Trait != "3 distinct" {
		t.Errorf("filter kept the wrong candidates: %+v", out)
	}
}

func TestFilterByEvidence_NormalisesCasing(t *testing.T) {
	// "proj-A" and "proj-a" should count as the same source — different
	// casing across the projects map vs the reasoner's free-form output
	// would otherwise inflate the distinct count.
	in := []LCandidate{
		{Trait: "shaky", Sources: []string{"proj-A", "PROJ-a", "proj-a"}},
	}
	out := FilterByEvidence(in, 2)
	if len(out) != 0 {
		t.Errorf("case-insensitive dedup should yield 1 distinct source → dropped; got %+v", out)
	}
}

func TestDreamer_EndToEnd(t *testing.T) {
	// 1st candidate passes (2 sources), 2nd fails (1 source).
	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{
			Choices: []deepseek.Choice{{Message: deepseek.Message{
				Content: `[
					{"trait":"prefers terse code","why":"observed in both","sources":["proj-a","proj-b"]},
					{"trait":"only in proj-a","why":"x","sources":["proj-a"]}
				]`,
			}}},
		},
	}
	d := &Dreamer{Client: fake}

	got, err := d.Dream(context.Background(), DreamInput{})
	if err != nil {
		t.Fatalf("Dream: %v", err)
	}
	if len(got) != 1 || got[0].Trait != "prefers terse code" {
		t.Errorf("expected filter to keep only the ≥2-source candidate, got %+v", got)
	}

	// System prompt should be the dream framing, not /distill's.
	if !strings.Contains(fake.lastReq.Messages[0].Content, "dream mode") {
		t.Errorf("system prompt should be DreamSystemPrompt, got %q", fake.lastReq.Messages[0].Content)
	}
	if fake.lastReq.Model != deepseek.ModelReasoner {
		t.Errorf("Dreamer should default to ModelReasoner, got %q", fake.lastReq.Model)
	}
}

func TestDreamer_RespectsCustomMinSources(t *testing.T) {
	fake := &fakeChatClient{
		resp: &deepseek.ChatResponse{
			Choices: []deepseek.Choice{{Message: deepseek.Message{
				Content: `[
					{"trait":"three needed","sources":["a","b"]},
					{"trait":"yes three","sources":["a","b","c"]}
				]`,
			}}},
		},
	}
	d := &Dreamer{Client: fake, MinSources: 3}
	got, _ := d.Dream(context.Background(), DreamInput{})
	if len(got) != 1 || got[0].Trait != "yes three" {
		t.Errorf("expected only ≥3-source candidate, got %+v", got)
	}
}

func TestFormatLCandidatesMarkdown_StableAcrossRuns(t *testing.T) {
	c := []LCandidate{
		{
			Trait:   "tabs over spaces",
			Why:     "consistent across projects",
			Sources: []string{"proj-z", "proj-a", "proj-a", "proj-m"}, // dupe + unsorted
		},
	}
	first := FormatLCandidatesMarkdown(c)
	second := FormatLCandidatesMarkdown(c)
	if first != second {
		t.Errorf("formatter not deterministic:\n  first=%q\n  second=%q", first, second)
	}
	if !strings.Contains(first, "tabs over spaces") {
		t.Errorf("trait missing from output: %q", first)
	}
	// Sources sorted + deduped → "proj-a, proj-m, proj-z".
	if !strings.Contains(first, "proj-a, proj-m, proj-z") {
		t.Errorf("sources not sorted/deduped: %q", first)
	}
}

func TestFormatLCandidatesMarkdown_EmptyReturnsEmpty(t *testing.T) {
	if got := FormatLCandidatesMarkdown(nil); got != "" {
		t.Errorf("nil input should produce empty string, got %q", got)
	}
}

func TestListProjects_SkipsManifestlessDirs(t *testing.T) {
	cwd, home := withMemoryEnv(t)
	// Create a legit project.
	p, err := LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Plant a junk directory under ~/.seek/projects/. ListProjects must
	// skip it without erroring out — half-written or foreign-tool
	// directories are realistic in the wild.
	junkDir := home + "/projects/notavalidhashid42"
	if err := os.MkdirAll(junkDir, 0o755); err != nil {
		t.Fatalf("mkdir junk: %v", err)
	}

	got, err := ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(got) != 1 || got[0].ID != p.ID {
		t.Errorf("expected the one valid project, got %+v", got)
	}
}

func TestListProjects_NoProjectsRootIsZero(t *testing.T) {
	// No ~/.seek/projects/ directory at all (not even empty) — common
	// before the user has ever started a session. Must NOT error.
	t.Setenv("SEEK_HOME", t.TempDir())
	got, err := ListProjects()
	if err != nil {
		t.Fatalf("ListProjects on empty home: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil slice when projects/ doesn't exist, got %+v", got)
	}
}

func TestSoulSetSections_RoundTripsThroughParse(t *testing.T) {
	s := &Soul{}
	s.SetSections("- trait one\n- trait two", "- candidate one")

	// Round-trip via parseSoul to ensure the rendered file is something
	// LoadSoul would understand the same way.
	reparsed := parseSoul("", s.Raw)
	if !strings.Contains(reparsed.Stable, "trait one") {
		t.Errorf("Stable section lost on render: %q", reparsed.Stable)
	}
	if !strings.Contains(reparsed.Pending, "candidate one") {
		t.Errorf("Pending section lost on render: %q", reparsed.Pending)
	}
	if reparsed.SchemaVersion != 1 {
		t.Errorf("schema_version not preserved, got %d", reparsed.SchemaVersion)
	}
	if reparsed.UpdatedAt.IsZero() {
		t.Errorf("updated_at not preserved")
	}
}

func TestSoulSetSections_PreservesStableWhenOnlyPendingChanges(t *testing.T) {
	// Simulate the dream write path: load existing soul, replace
	// pending, leave stable alone.
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	original := &Soul{}
	original.SetSections("- old stable trait", "- old pending")
	if err := original.Save(); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	s, err := LoadSoul()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s.SetSections(s.Stable, "- new pending only")
	if err := s.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	final, _ := LoadSoul()
	if !strings.Contains(final.Stable, "old stable trait") {
		t.Errorf("Stable lost after Pending-only update: %q", final.Stable)
	}
	if !strings.Contains(final.Pending, "new pending only") {
		t.Errorf("new Pending not written: %q", final.Pending)
	}
	if strings.Contains(final.Pending, "old pending") {
		t.Errorf("old Pending leaked in: %q", final.Pending)
	}
}
