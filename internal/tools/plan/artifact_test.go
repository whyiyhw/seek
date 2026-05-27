package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleMeta(t *testing.T, approvedAt time.Time) ArtifactMetadata {
	t.Helper()
	root := t.TempDir()
	t.Setenv("SEEK_HOME", root)
	return ArtifactMetadata{
		Problem:        "Refactor auth middleware to use a per-request token store.",
		Steps:          []string{"Inventory call sites", "Define interface", "Migrate store"},
		WhyNow:         "This unblocks the mobile launch.",
		SessionID:      "sess-abc12345",
		ProjectAbsPath: "/abs/path/to/project",
		Batch:          false,
		ApprovedAt:     approvedAt,
	}
}

func TestWriteArtifact_WritesFileWithExpectedShape(t *testing.T) {
	approvedAt := time.Date(2026, 5, 26, 14, 30, 42, 0, time.UTC)
	meta := sampleMeta(t, approvedAt)

	path, err := WriteArtifact(meta)
	if err != nil {
		t.Fatalf("WriteArtifact: %v", err)
	}

	// Filename shape: YYYYMMDD-HHMM-<slug>.md, inside ~/.seek/projects/<id>/plans/.
	if !strings.HasSuffix(path, ".md") {
		t.Errorf("path lacks .md extension: %s", path)
	}
	if !strings.Contains(path, "/plans/") {
		t.Errorf("path missing /plans/ segment: %s", path)
	}
	if !strings.Contains(filepath.Base(path), "20260526-1430-") {
		t.Errorf("filename missing YYYYMMDD-HHMM- prefix: %s", filepath.Base(path))
	}

	// Content shape.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		"# Refactor Auth Middleware Use Per",
		"- **Approved**: 2026-05-26 14:30:42",
		"- **Session**: sess-abc12345",
		"- **Approval mode**: per-call",
		"- **Project**: /abs/path/to/project",
		"## Problem",
		"Refactor auth middleware to use a per-request token store.",
		"## Steps",
		"1. Inventory call sites",
		"2. Define interface",
		"3. Migrate store",
		"## Why now",
		"This unblocks the mobile launch.",
		"---",
		"Write-once snapshot",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("artifact body missing %q\nFULL:\n%s", want, s)
		}
	}
}

func TestWriteArtifact_BatchMode(t *testing.T) {
	meta := sampleMeta(t, time.Now())
	meta.Batch = true
	path, err := WriteArtifact(meta)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), "- **Approval mode**: auto-approve-per-step") {
		t.Errorf("batch mode not reflected:\n%s", body)
	}
}

func TestWriteArtifact_OmitsWhyNowSectionWhenEmpty(t *testing.T) {
	meta := sampleMeta(t, time.Now())
	meta.WhyNow = ""
	path, err := WriteArtifact(meta)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "## Why now") {
		t.Errorf("empty WhyNow should omit section:\n%s", body)
	}
}

func TestWriteArtifact_OmitsSessionLineWhenEmpty(t *testing.T) {
	meta := sampleMeta(t, time.Now())
	meta.SessionID = ""
	path, err := WriteArtifact(meta)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "- **Session**:") {
		t.Errorf("empty SessionID should omit line:\n%s", body)
	}
}

func TestWriteArtifact_ConflictAppendsCounter(t *testing.T) {
	meta := sampleMeta(t, time.Date(2026, 5, 26, 14, 30, 0, 0, time.UTC))

	path1, err := WriteArtifact(meta)
	if err != nil {
		t.Fatal(err)
	}
	path2, err := WriteArtifact(meta)
	if err != nil {
		t.Fatal(err)
	}
	if path1 == path2 {
		t.Fatalf("second write should pick a different name, got both = %s", path1)
	}
	if !strings.Contains(path2, "-2.md") {
		t.Errorf("second write should end in -2.md, got %s", path2)
	}
	path3, err := WriteArtifact(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path3, "-3.md") {
		t.Errorf("third write should end in -3.md, got %s", path3)
	}
}

func TestWriteArtifact_RequiresProjectPath(t *testing.T) {
	meta := sampleMeta(t, time.Now())
	meta.ProjectAbsPath = ""
	_, err := WriteArtifact(meta)
	if err == nil || !strings.Contains(err.Error(), "ProjectAbsPath") {
		t.Fatalf("want ProjectAbsPath error, got %v", err)
	}
}

func TestWriteArtifact_RequiresProblem(t *testing.T) {
	meta := sampleMeta(t, time.Now())
	meta.Problem = "   "
	_, err := WriteArtifact(meta)
	if err == nil || !strings.Contains(err.Error(), "Problem") {
		t.Fatalf("want Problem error, got %v", err)
	}
}

func TestWriteArtifact_RequiresSteps(t *testing.T) {
	meta := sampleMeta(t, time.Now())
	meta.Steps = nil
	_, err := WriteArtifact(meta)
	if err == nil || !strings.Contains(err.Error(), "Steps") {
		t.Fatalf("want Steps error, got %v", err)
	}
}

func TestWriteArtifact_RequiresApprovedAt(t *testing.T) {
	meta := sampleMeta(t, time.Time{})
	_, err := WriteArtifact(meta)
	if err == nil || !strings.Contains(err.Error(), "ApprovedAt") {
		t.Fatalf("want ApprovedAt error, got %v", err)
	}
}

func TestWriteArtifact_NoTmpLeftBehindOnSuccess(t *testing.T) {
	meta := sampleMeta(t, time.Now())
	path, err := WriteArtifact(meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp sibling should be cleaned up after success, got %v", err)
	}
}

func TestExtractSlug_DropsStopwordsAndLimitsLength(t *testing.T) {
	cases := []struct {
		problem string
		want    string
	}{
		{"Refactor the auth middleware to use a per-request token store",
			"refactor-auth-middleware-use-per"},
		{"Add a new handler for the X feature",
			"add-new-handler-feature"},
		{"the the the the the", "plan"}, // all-stopword fallback
		{"", "plan"},                    // empty fallback
		{"!!!@@@###", "plan"},           // all-junk fallback
		{"go-zero microservice setup process", "go-zero-microservice-setup-process"},
	}
	for _, c := range cases {
		t.Run(c.problem, func(t *testing.T) {
			got := extractSlug(c.problem)
			if got != c.want {
				t.Errorf("extractSlug(%q) = %q, want %q", c.problem, got, c.want)
			}
		})
	}
}

func TestExtractSlug_DropsShortAndUnicodeTokens(t *testing.T) {
	// Single-letter tokens drop; CJK collapses to nothing → fallback.
	if got := extractSlug("重构 认证 中间件"); got != fallbackSlug {
		t.Errorf("all-unicode → expected fallback, got %q", got)
	}
	// Single letters dropped, real word kept.
	if got := extractSlug("a b c migration d e f"); got != "migration" {
		t.Errorf("expected 'migration', got %q", got)
	}
}

func TestExtractSlug_FilenameSafe(t *testing.T) {
	// Slug must never contain anything other than [a-z0-9-]; the
	// resolveNonConflictingPath / os.WriteFile pipeline depends on
	// it.
	tricky := "Inject `<script>` into ${HOME}/admin and; rm -rf /"
	slug := extractSlug(tricky)
	for _, r := range slug {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			t.Errorf("slug contains unsafe rune %q in %q", r, slug)
		}
	}
}

func TestHumanizeSlug_TitleCase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"auth-middleware-refactor", "Auth Middleware Refactor"},
		{"plan", "Plan"},
		{"go-zero-setup", "Go Zero Setup"},
		{"", ""},
	}
	for _, c := range cases {
		if got := humanizeSlug(c.in); got != c.want {
			t.Errorf("humanizeSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
