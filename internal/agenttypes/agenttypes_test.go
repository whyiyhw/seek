package agenttypes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_Good(t *testing.T) {
	src := "---\n" +
		"name: verifier\n" +
		"description: Runs the test suite and reports failures. Read-only except tests.\n" +
		"tools: [read, grep, list_dir, git, bash]\n" +
		"provider: glm\n" +
		"model: glm-5.3\n" +
		"effort: low\n" +
		"workflow: plan-analyze\n" +
		"max_turns: 60\n" +
		"---\n" +
		"You are a test verifier subagent."
	def, err := Parse([]byte(src), "verifier", "/x/verifier.md")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "verifier" || def.Description == "" || def.Provider != "glm" ||
		def.Model != "glm-5.3" || def.Effort != "low" || def.Workflow != "plan-analyze" || def.MaxTurns != 60 {
		t.Errorf("parsed fields drifted: %+v", def)
	}
	if strings.Join(def.Tools, ",") != "read,grep,list_dir,git,bash" {
		t.Errorf("tools = %v", def.Tools)
	}
	if def.Body != "You are a test verifier subagent." {
		t.Errorf("body = %q", def.Body)
	}
}

// TestParse_NameFallbackToFilename: CC-style files omit `name` — the
// filename stem is the identity (D10 layout is <name>.md).
func TestParse_NameFallbackToFilename(t *testing.T) {
	src := "---\ndescription: x\n---\nRole."
	def, err := Parse([]byte(src), "verifier", "/x/verifier.md")
	if err != nil {
		t.Fatal(err)
	}
	if def.Name != "verifier" {
		t.Errorf("name = %q, want filename stem", def.Name)
	}
}

// TestParse_Minimal: only the two required fields (description +
// body); everything else inherits.
func TestParse_Minimal(t *testing.T) {
	def, err := Parse([]byte("---\ndescription: x\n---\nRole.\n"), "min", "src")
	if err != nil {
		t.Fatal(err)
	}
	if def.Tools != nil || def.Provider != "" || def.Workflow != "" || def.MaxTurns != 0 {
		t.Errorf("optional fields should stay zero, got %+v", def)
	}
}

// TestParse_BadFrontmatter is the acceptance table: every illegal
// shape gets a distinct, findable error and NOTHING is returned.
func TestParse_BadFrontmatter(t *testing.T) {
	cases := []struct {
		name string
		stem string // filename stem; "t" unless the case exercises the fallback
		in   string
		want string // substring of the error
	}{
		{"no frontmatter", "t", "# heading\nBody.", "missing frontmatter"},
		{"unterminated", "t", "---\nname: t\n", "unterminated"},
		{"missing description", "t", "---\nname: t\n---\nBody.", "missing `description`"},
		{"empty body", "t", "---\nname: t\ndescription: d\n---\n", "empty body"},
		{"bad name", "t", "---\nname: Bad_Name\ndescription: d\n---\nB.", "kebab-case"},
		{"no name at all", "", "---\ndescription: d\n---\nB.", "no name"},
		{"forbidden tool agent", "t", "---\ndescription: d\ntools: [read, agent]\n---\nB.", "never allowed"},
		{"forbidden tool ask_user", "t", "---\ndescription: d\ntools: [ask_user]\n---\nB.", "never allowed"},
		{"workflow loosening", "t", "---\ndescription: d\nworkflow: plan-execute\n---\nB.", "plan-analyze"},
		{"unknown workflow", "t", "---\ndescription: d\nworkflow: yolo\n---\nB.", "plan-analyze"},
		{"bad effort", "t", "---\ndescription: d\neffort: turbo\n---\nB.", "low, medium, high"},
		{"zero max_turns", "t", "---\ndescription: d\nmax_turns: 0\n---\nB.", "positive integer"},
		{"negative max_turns", "t", "---\ndescription: d\nmax_turns: -3\n---\nB.", "positive integer"},
		{"non-numeric max_turns", "t", "---\ndescription: d\nmax_turns: many\n---\nB.", "positive integer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			def, err := Parse([]byte(c.in), c.stem, "src")
			if err == nil {
				t.Fatalf("err = nil, def = %+v — bad shape accepted", def)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %q, want substring %q", err, c.want)
			}
		})
	}
}

// TestParse_BlockListTools: tools may use the block-list shape — same
// frontmatter dialect as SKILL.md.
func TestParse_BlockListTools(t *testing.T) {
	src := "---\ndescription: d\ntools:\n  - read\n  - git\n---\nB."
	def, err := Parse([]byte(src), "t", "src")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(def.Tools, ",") != "read,git" {
		t.Errorf("tools = %v", def.Tools)
	}
}

// TestParse_EmptyToolsListInherits: `tools: []` normalises to nil
// (inherit) — an empty whitelist is not a subagent with zero tools.
func TestParse_EmptyToolsListInherits(t *testing.T) {
	def, err := Parse([]byte("---\ndescription: d\ntools: []\n---\nB."), "t", "src")
	if err != nil {
		t.Fatal(err)
	}
	if def.Tools != nil {
		t.Errorf("tools = %v, want nil (inherit)", def.Tools)
	}
}

// TestParse_UnknownKeysTolerated: forward-compat — a newer spec key
// must not break this build.
func TestParse_UnknownKeysTolerated(t *testing.T) {
	def, err := Parse([]byte("---\ndescription: d\nfuture-field: whatever\n---\nB."), "t", "src")
	if err != nil {
		t.Fatalf("unknown key rejected: %v", err)
	}
	if def.Name != "t" || def.Body != "B." {
		t.Errorf("def = %+v", def)
	}
}

// --- Load: scope cascade, duplicates, no-dir ---

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const verifierMD = "---\nname: verifier\ndescription: runs tests\ntools: [read, bash]\n---\nVerify everything."

func TestLoad_NoDirs_OnlyBuiltins(t *testing.T) {
	// The "无 agents 目录时行为不变" acceptance: missing directories
	// are the common case — no errors, exactly the three built-ins.
	set, stats, err := Load(LoadOptions{
		ProjectDir:    t.TempDir(),
		UserAgentsDir: filepath.Join(t.TempDir(), "nope"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Errors) != 0 {
		t.Errorf("stats.Errors = %v, want none", stats.Errors)
	}
	if set.Len() != 3 {
		t.Fatalf("set.Len() = %d, want 3 builtins", set.Len())
	}
	for _, name := range []string{"general-purpose", "explore", "plan"} {
		if set.Get(name) == nil {
			t.Errorf("builtin %s missing", name)
		}
	}
	if set.CustomCount() != 0 {
		t.Errorf("CustomCount() = %d, want 0", set.CustomCount())
	}
}

func TestLoad_ProjectAndUser(t *testing.T) {
	project := t.TempDir()
	user := t.TempDir()
	write(t, filepath.Join(project, ".seek", "agents", "verifier.md"), verifierMD)
	write(t, filepath.Join(user, "librarian.md"),
		"---\nname: librarian\ndescription: finds docs\n---\nIndex things.")

	set, stats, err := Load(LoadOptions{ProjectDir: project, UserAgentsDir: user})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Errors) != 0 {
		t.Fatalf("stats.Errors = %v", stats.Errors)
	}
	if set.Len() != 5 {
		t.Errorf("set.Len() = %d, want 5 (2 custom + 3 builtin)", set.Len())
	}
	v := set.Get("verifier")
	if v == nil || v.Origin != OriginProject {
		t.Errorf("verifier origin = %+v, want project", v)
	}
	l := set.Get("librarian")
	if l == nil || l.Origin != OriginUser {
		t.Errorf("librarian origin = %+v, want user", l)
	}
	if set.CustomCount() != 2 {
		t.Errorf("CustomCount() = %d, want 2", set.CustomCount())
	}
}

// TestLoad_ScopeOverride: project beats user beats builtin for the
// same name (D11's scope stack). The loser's Source is recorded in
// Shadowed.
func TestLoad_ScopeOverride(t *testing.T) {
	project := t.TempDir()
	user := t.TempDir()
	write(t, filepath.Join(project, ".seek", "agents", "explore.md"),
		"---\ndescription: project explore\n---\nFrom project.")
	write(t, filepath.Join(user, "explore.md"),
		"---\ndescription: user explore\n---\nFrom user.")

	set, stats, err := Load(LoadOptions{ProjectDir: project, UserAgentsDir: user})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Errors) != 0 {
		t.Fatalf("scope override is by design, not an error; got %v", stats.Errors)
	}
	got := set.Get("explore")
	if got.Origin != OriginProject || got.Description != "project explore" {
		t.Errorf("explore winner = %+v, want project", got)
	}
	if sh := set.Shadowed("explore"); len(sh) != 2 { // user file + builtin
		t.Errorf("Shadowed(explore) = %v, want 2 entries", sh)
	}
}

// TestLoad_DuplicateNameSameDir: two files in ONE directory declaring
// the same name — deterministic winner (filename sort), loser
// reported as a load error, registration count exact.
func TestLoad_DuplicateNameSameDir(t *testing.T) {
	user := t.TempDir()
	// a.md and b.md both declare name: dupe. Sorted order makes a.md
	// the winner regardless of platform.
	write(t, filepath.Join(user, "a.md"),
		"---\nname: dupe\ndescription: from a\n---\nA body.")
	write(t, filepath.Join(user, "b.md"),
		"---\nname: dupe\ndescription: from b\n---\nB body.")

	set, stats, err := Load(LoadOptions{ProjectDir: t.TempDir(), UserAgentsDir: user})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Get("dupe"); got == nil || got.Description != "from a" {
		t.Errorf("dupe winner = %+v, want a.md (sorted first)", got)
	}
	if len(stats.Errors) != 1 || !strings.Contains(stats.Errors[0].Error(), "b.md") {
		t.Fatalf("stats.Errors = %v, want one b.md duplicate error", stats.Errors)
	}
	if sh := set.Shadowed("dupe"); len(sh) != 1 || !strings.Contains(sh[0], "b.md") {
		t.Errorf("Shadowed(dupe) = %v, want [b.md]", sh)
	}
}

// TestLoad_BadFileSkippedNotFatal: a malformed file errors into
// stats.Errors and its name never registers; the rest of the load
// (including the builtins) proceeds.
func TestLoad_BadFileSkippedNotFatal(t *testing.T) {
	user := t.TempDir()
	write(t, filepath.Join(user, "broken.md"), "---\nname: broken\ndescription: d\n") // unterminated
	write(t, filepath.Join(user, "good.md"), verifierMD)

	set, stats, err := Load(LoadOptions{ProjectDir: t.TempDir(), UserAgentsDir: user})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Errors) != 1 || !strings.Contains(stats.Errors[0].Error(), "broken.md") {
		t.Fatalf("stats.Errors = %v, want one broken.md error", stats.Errors)
	}
	if set.Get("broken") != nil {
		t.Error("broken.md registered despite parse failure")
	}
	if set.Get("verifier") == nil || set.Len() != 4 {
		t.Errorf("good files must load: len=%d", set.Len())
	}
}

// TestLoad_SubdirectoryWarns: agent types are single .md files; a
// subdirectory is reported, not silently dropped.
func TestLoad_SubdirectoryWarns(t *testing.T) {
	user := t.TempDir()
	if err := os.MkdirAll(filepath.Join(user, "oops"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, stats, err := Load(LoadOptions{ProjectDir: t.TempDir(), UserAgentsDir: user})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Errors) != 1 || !strings.Contains(stats.Errors[0].Error(), "oops") {
		t.Errorf("stats.Errors = %v, want subdirectory warning", stats.Errors)
	}
}

// TestBuiltins_ImmutableShape: the three built-ins carry the
// plan-analyze pinning and non-empty descriptions (they feed /agents
// and, from M-O2.2, the schema enum).
func TestBuiltins_ImmutableShape(t *testing.T) {
	for _, d := range Builtins() {
		if d.Origin != OriginBuiltin || !strings.HasPrefix(d.Source, "builtin:") {
			t.Errorf("%s: origin/source = %v/%s", d.Name, d.Origin, d.Source)
		}
		if d.Description == "" {
			t.Errorf("%s: empty description", d.Name)
		}
		if d.Name == "explore" || d.Name == "plan" {
			if d.Workflow != "plan-analyze" {
				t.Errorf("%s: workflow = %q, want plan-analyze", d.Name, d.Workflow)
			}
		}
		if d.Name == "general-purpose" && (d.Workflow != "" || d.Tools != nil) {
			t.Errorf("general-purpose must inherit everything, got %+v", d)
		}
	}
}
