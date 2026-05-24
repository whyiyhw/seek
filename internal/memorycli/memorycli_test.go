package memorycli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/memory"
)

// withTempProject creates a temporary project for testing and returns
// its absolute path. The caller must ensure the project is loaded via
// memory.LoadOrCreate(cwd) inside each test (or use setupEnv).
func withTempProject(t *testing.T) (cwd string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	cwd = t.TempDir()
	return cwd
}

// runCLI is a helper that invokes Run with the given args and returns
// the captured stdout and stderr. It loads the project at cwd, runs,
// and returns the combined output for assertion.
func runCLI(t *testing.T, cwd string, args ...string) (stdout, stderr string) {
	t.Helper()
	origWd, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir %s: %v", cwd, err)
	}
	defer func() {
		_ = os.Chdir(origWd)
	}()

	var outBuf, errBuf bytes.Buffer
	err := Run(args, &outBuf, &errBuf)
	if err != nil {
		// Some commands return errors for "unknown verb" etc.
		// Callers may want to check error separately.
	}
	return outBuf.String(), errBuf.String()
}

func TestMemoryCLI_Help(t *testing.T) {
	stdout, _ := runCLI(t, t.TempDir(), "help")
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("help should show usage, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "list") {
		t.Errorf("help should list verbs, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "add") {
		t.Errorf("help should include 'add' verb, got:\n%s", stdout)
	}
}

func TestMemoryCLI_EmptyArgsShowsHelp(t *testing.T) {
	stdout, _ := runCLI(t, t.TempDir())
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("empty args should show help, got:\n%s", stdout)
	}
}

func TestMemoryCLI_UnknownVerb(t *testing.T) {
	_, stderr := runCLI(t, t.TempDir(), "xyz")
	if !strings.Contains(stderr, "unknown verb") {
		t.Errorf("unknown verb should show error, got:\n%s", stderr)
	}
}

func TestMemoryCLI_AddAndList(t *testing.T) {
	cwd := withTempProject(t)
	_, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Add an entry.
	stdout, stderr := runCLI(t, cwd, "add", "test-entry", "-tagline", "a test entry", "-content", "test content body", "-tags", "test,example")
	if stderr != "" {
		t.Fatalf("add stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "added") {
		t.Errorf("add should confirm, got: %s", stdout)
	}

	// List should show it.
	stdout, stderr = runCLI(t, cwd, "list")
	if stderr != "" {
		t.Fatalf("list stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "test-entry") {
		t.Errorf("list should show test-entry, got:\n%s", stdout)
	}
}

func TestMemoryCLI_AddMissingRequiredFields(t *testing.T) {
	cwd := withTempProject(t)
	_, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Missing content should show usage.
	stdout, stderr := runCLI(t, cwd, "add", "test-entry", "-tagline", "x")
	if !strings.Contains(stdout, "usage:") && !strings.Contains(stderr, "usage:") {
		t.Errorf("missing fields should show usage, got stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestMemoryCLI_Show(t *testing.T) {
	cwd := withTempProject(t)
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if err := p.Add(memory.Entry{
		Name:    "show-me",
		Tagline: "show this tagline",
		Content: "show this content body",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stdout, stderr := runCLI(t, cwd, "show", "show-me")
	if stderr != "" {
		t.Fatalf("show stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "show this tagline") {
		t.Errorf("show should include tagline, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "show this content body") {
		t.Errorf("show should include content, got:\n%s", stdout)
	}
}

func TestMemoryCLI_ShowNonexistent(t *testing.T) {
	cwd := withTempProject(t)
	_, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	_, stderr := runCLI(t, cwd, "show", "nope")
	if !strings.Contains(stderr, "not found") {
		t.Errorf("nonexistent entry should say not found, got: %s", stderr)
	}
}

func TestMemoryCLI_Search(t *testing.T) {
	cwd := withTempProject(t)
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if err := p.Add(memory.Entry{
		Name:    "arch-decision",
		Tagline: "use JSONL for session storage",
		Content: "x",
		Tags:    []string{"architecture"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := p.Add(memory.Entry{
		Name:    "test-pref",
		Tagline: "prefer table-driven tests",
		Content: "x",
		Tags:    []string{"testing"},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Search by tagline substring.
	stdout, stderr := runCLI(t, cwd, "search", "JSONL")
	if stderr != "" {
		t.Fatalf("search stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "arch-decision") {
		t.Errorf("search 'JSONL' should find arch-decision, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "test-pref") {
		t.Errorf("search 'JSONL' should NOT find test-pref, got:\n%s", stdout)
	}

	// Search by tag.
	stdout, stderr = runCLI(t, cwd, "search", "testing")
	if stderr != "" {
		t.Fatalf("search stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "test-pref") {
		t.Errorf("search 'testing' should find test-pref, got:\n%s", stdout)
	}

	// No match.
	stdout, stderr = runCLI(t, cwd, "search", "zzzzz")
	if stderr != "" {
		t.Fatalf("search stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "no entries") {
		t.Errorf("no match should say so, got:\n%s", stdout)
	}
}

func TestMemoryCLI_Archive(t *testing.T) {
	cwd := withTempProject(t)
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if err := p.Add(memory.Entry{
		Name:    "old-news",
		Tagline: "outdated",
		Content: "body",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stdout, stderr := runCLI(t, cwd, "archive", "old-news", "-reason", "test cleanup")
	if stderr != "" {
		t.Fatalf("archive stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "archived") {
		t.Errorf("archive should confirm, got:\n%s", stdout)
	}

	// List should no longer show it.
	stdout, stderr = runCLI(t, cwd, "list")
	if stderr != "" {
		t.Fatalf("list stderr: %s", stderr)
	}
	if strings.Contains(stdout, "old-news") {
		t.Errorf("archived entry should not appear in list, got:\n%s", stdout)
	}

	// But LoadArchived should have it.
	archived, err := p.LoadArchived()
	if err != nil {
		t.Fatalf("LoadArchived: %v", err)
	}
	if len(archived) != 1 || archived[0].Name != "old-news" {
		t.Errorf("archived.jsonl should contain old-news, got %+v", archived)
	}
}

func TestMemoryCLI_ArchiveNonexistent(t *testing.T) {
	cwd := withTempProject(t)
	_, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	_, stderr := runCLI(t, cwd, "archive", "nope", "-reason", "test")
	if !strings.Contains(stderr, "not found") {
		t.Errorf("archiving nonexistent should say not found, got: %s", stderr)
	}
}

func TestMemoryCLI_ListWithStale(t *testing.T) {
	cwd := withTempProject(t)
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	if err := p.Add(memory.Entry{
		Name:    "fresh",
		Tagline: "still good",
		Content: "x",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Default list shows active entries.
	stdout, _ := runCLI(t, cwd, "list")
	if !strings.Contains(stdout, "fresh") {
		t.Errorf("list should show fresh entry, got:\n%s", stdout)
	}
}

func TestMemoryCLI_ListAllFlag(t *testing.T) {
	cwd := withTempProject(t)
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if err := p.Add(memory.Entry{
		Name:    "active",
		Tagline: "active entry",
		Content: "x",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	stdout, _ := runCLI(t, cwd, "list", "--all")
	if !strings.Contains(stdout, "active") {
		t.Errorf("list --all should show active entry, got:\n%s", stdout)
	}
}

// TestMemoryCLI_RoundTrip_AddThroughCLI checks that entries added via
// the CLI are visible to the Project API (and vice versa).
func TestMemoryCLI_RoundTrip_AddThroughCLI(t *testing.T) {
	cwd := withTempProject(t)

	// Load project (creates the directory structure).
	if _, err := memory.LoadOrCreate(cwd); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	// Add via CLI.
	stdout, stderr := runCLI(t, cwd, "add", "cli-entry", "-tagline", "added from CLI", "-content", "this was added via seek memory add")
	if stderr != "" {
		t.Fatalf("add stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "added") {
		t.Fatalf("add didn't confirm, got: %s", stdout)
	}

	// Verify via Project API.
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	e, ok := p.Get("cli-entry")
	if !ok {
		t.Fatal("cli-entry not found via Project.Get")
	}
	if e.Tagline != "added from CLI" {
		t.Errorf("tagline = %q, want %q", e.Tagline, "added from CLI")
	}
	if e.Content != "this was added via seek memory add" {
		t.Errorf("content = %q, want %q", e.Content, "this was added via seek memory add")
	}
}

// TestMemoryCLI_AddWithTags checks that tags are correctly parsed from
// the comma-separated CLI flag.
func TestMemoryCLI_AddWithTags(t *testing.T) {
	cwd := withTempProject(t)
	if _, err := memory.LoadOrCreate(cwd); err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}

	stdout, stderr := runCLI(t, cwd, "add", "tagged-entry", "-tagline", "has tags", "-content", "body", "-tags", "alpha, beta, gamma")
	if stderr != "" {
		t.Fatalf("add stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "added") {
		t.Fatalf("add didn't confirm, got: %s", stdout)
	}

	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	e, ok := p.Get("tagged-entry")
	if !ok {
		t.Fatal("tagged-entry not found")
	}
	if len(e.Tags) != 3 || e.Tags[0] != "alpha" || e.Tags[1] != "beta" || e.Tags[2] != "gamma" {
		t.Errorf("tags = %v, want [alpha beta gamma]", e.Tags)
	}
}

func TestMemoryCLI_ListArchived(t *testing.T) {
	cwd := withTempProject(t)
	p, err := memory.LoadOrCreate(cwd)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if err := p.Add(memory.Entry{
		Name:    "old-news",
		Tagline: "outdated",
		Content: "x",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := p.Archive("old-news", "test"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	stdout, stderr := runCLI(t, cwd, "list", "--archived")
	if stderr != "" {
		t.Fatalf("list --archived stderr: %s", stderr)
	}
	if !strings.Contains(stdout, "old-news") {
		t.Errorf("list --archived should show old-news, got:\n%s", stdout)
	}
}
