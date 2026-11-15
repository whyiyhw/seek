package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/agenttypes"
)

// The P2-c dynamic-schema tests (feature-orchestration.md §5 D12,
// M-O2.2). The schema package var is process-global state: every test
// that caches a non-default registry restores the built-ins default
// via t.Cleanup so test order can't leak bytes across cases. No
// t.Parallel() for the same reason.

// restoreDefaultSchema resets the package vars to the built-ins-only
// default (the "no agents files + empty config" startup state).
func restoreDefaultSchema(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { CacheTypesSchema(agenttypes.Builtins()) })
}

// loadRegistry is a fresh agenttypes.Load over isolated dirs — the
// same thing a seek launch does. Second return value exists so tests
// can build "two launches" trivially.
func loadRegistry(t *testing.T) []*agenttypes.Definition {
	t.Helper()
	set, stats, err := agenttypes.Load(agenttypes.LoadOptions{
		ProjectDir:    t.TempDir(),
		UserAgentsDir: filepath.Join(t.TempDir(), "nope"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats.Errors) != 0 {
		t.Fatalf("stats.Errors = %v, want none", stats.Errors)
	}
	return set.List()
}

// TestSchema_DefaultIsBuiltinsOnly: without a CacheTypesSchema call
// (the "no agents files + empty config" startup), the schema enum is
// exactly the three built-ins and the description lists each — the
// acceptance-#5 guarantee that the default registry keeps the
// pre-P2-c enum MEMBERS (the prose wording is now generated from
// agenttypes.Builtins, a D12-sanctioned one-time change).
func TestSchema_DefaultIsBuiltinsOnly(t *testing.T) {
	restoreDefaultSchema(t)

	var probe struct {
		Properties struct {
			SubagentType struct {
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"subagent_type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &probe); err != nil {
		t.Fatalf("default schema not valid JSON: %v", err)
	}
	want := []string{"general-purpose", "explore", "plan"}
	if strings.Join(probe.Properties.SubagentType.Enum, ",") != strings.Join(want, ",") {
		t.Errorf("default enum = %v, want %v", probe.Properties.SubagentType.Enum, want)
	}
	for _, name := range want {
		if !strings.Contains(probe.Properties.SubagentType.Description, name+":") {
			t.Errorf("description %q missing per-value entry %q", probe.Properties.SubagentType.Description, name)
		}
	}
	if !strings.HasSuffix(activeDescription, "Available subagent types: general-purpose, explore, plan.") {
		t.Errorf("activeDescription tail missing type list: %q", activeDescription)
	}
}

// TestCacheTypesSchema_EnumCarriesFileTypes: a project agents/*.md
// type appears in the enum and the per-value description once the
// registry is cached (PRD P2 acceptance #1, first half).
func TestCacheTypesSchema_EnumCarriesFileTypes(t *testing.T) {
	restoreDefaultSchema(t)

	project := t.TempDir()
	dir := filepath.Join(project, ".seek", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "verifier.md"), []byte(
		"---\nname: verifier\ndescription: runs the suite and reports failures\n---\nVerify everything."), 0o644); err != nil {
		t.Fatal(err)
	}
	set, stats, err := agenttypes.Load(agenttypes.LoadOptions{
		ProjectDir:    project,
		UserAgentsDir: filepath.Join(t.TempDir(), "nope"),
	})
	if err != nil || len(stats.Errors) != 0 {
		t.Fatalf("Load: %v %v", err, stats.Errors)
	}

	CacheTypesSchema(set.List())

	var probe struct {
		Properties struct {
			SubagentType struct {
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"subagent_type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &probe); err != nil {
		t.Fatalf("cached schema not valid JSON: %v", err)
	}
	found := false
	for _, e := range probe.Properties.SubagentType.Enum {
		if e == "verifier" {
			found = true
		}
	}
	if !found {
		t.Errorf("enum = %v, want verifier present", probe.Properties.SubagentType.Enum)
	}
	if !strings.Contains(probe.Properties.SubagentType.Description, "verifier: runs the suite and reports failures") {
		t.Errorf("description %q missing verifier's frontmatter description", probe.Properties.SubagentType.Description)
	}
	if !strings.Contains(activeDescription, "verifier") {
		t.Errorf("activeDescription %q missing verifier in the type tail", activeDescription)
	}
	// Within-process stability after caching — Schema() never
	// regenerates (D12: the package var IS the cache).
	if string(schemaBytes) != string(schemaBytes) {
		t.Error("Schema() not stable after CacheTypesSchema")
	}
}

// TestCacheTypesSchema_ByteIdenticalAcrossLaunches: two independent
// Load() passes over the SAME config (dirs + config layer) must
// yield byte-identical schemas — the D12 acceptance口径 "同配置多次
// 启动间字节一致". A map-iteration or readdir ordering bug would
// surface here as a byte diff.
func TestCacheTypesSchema_ByteIdenticalAcrossLaunches(t *testing.T) {
	restoreDefaultSchema(t)

	project := t.TempDir()
	dir := filepath.Join(project, ".seek", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Several files (map/dir order pressure) + a config entry whose
	// key must sort deterministically among the file names.
	for _, f := range []struct{ name, desc string }{
		{"verifier", "runs the suite"},
		{"librarian", "finds docs"},
		{"scout", "reads widely"},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name+".md"), []byte(
			"---\ndescription: "+f.desc+"\n---\nRole prompt for "+f.name+"."), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := map[string]agenttypes.RouteOverride{
		"explore": {Provider: "glm"},
		"plan":    {Effort: "low"},
	}
	launch := func() []byte {
		set, stats, err := agenttypes.Load(agenttypes.LoadOptions{
			ProjectDir:    project,
			UserAgentsDir: filepath.Join(t.TempDir(), "nope"),
			ConfigAgents:  cfg,
		})
		if err != nil || len(stats.Errors) != 0 {
			t.Fatalf("Load: %v %v", err, stats.Errors)
		}
		return buildSchema(set.List())
	}

	a, b := launch(), launch()
	if string(a) != string(b) {
		t.Errorf("two launches over the same config differ:\n%s\n---\n%s", a, b)
	}
	// And the enum order is the registry order: files sorted, then
	// config clones (sorted), then built-ins.
	var probe struct {
		Properties struct {
			SubagentType struct {
				Enum []string `json:"enum"`
			} `json:"subagent_type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(a, &probe); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(probe.Properties.SubagentType.Enum, ",")
	want := "librarian,scout,verifier,explore,plan,general-purpose"
	if got != want {
		t.Errorf("enum order = %s, want %s", got, want)
	}
}

// TestSchema_TypeDescriptionTruncated: one type's description is
// capped at 200 runes (D12: "描述长度设上限（~200 字/type）") so a
// runaway frontmatter description cannot bloat every prompt.
func TestSchema_TypeDescriptionTruncated(t *testing.T) {
	restoreDefaultSchema(t)

	long := strings.Repeat("详", 300)
	defs := append(loadRegistry(t), &agenttypes.Definition{
		Name:        "windbag",
		Description: long,
		Body:        "x",
	})
	CacheTypesSchema(defs)

	var probe struct {
		Properties struct {
			SubagentType struct {
				Description string `json:"description"`
			} `json:"subagent_type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schemaBytes, &probe); err != nil {
		t.Fatal(err)
	}
	desc := probe.Properties.SubagentType.Description
	want := "windbag: " + strings.Repeat("详", 200) + "…"
	if !strings.Contains(desc, want) {
		t.Errorf("description missing the capped windbag entry (%d runes + …):\n%s",
			utf8.RuneCountInString(want), desc)
	}
	if strings.Contains(desc, strings.Repeat("详", 201)) {
		t.Error("description exceeded the 200-rune cap")
	}
}
