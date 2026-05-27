package skillinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

// stageLocalSkill writes a minimal skill package to a fresh local
// directory and returns its absolute path. Used as the source for
// skill_fetch in tests that don't want to exercise network code.
func stageLocalSkill(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	pkg := filepath.Join(dir, name)
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	skillMD := "---\nname: " + name + "\ndescription: a tiny test skill\n---\n\n# " + name + "\n\nbody content.\n"
	if err := os.WriteFile(filepath.Join(pkg, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return pkg
}

// pinUserSkillsDir redirects paths.UserSkills() to a fresh tempdir
// for the duration of the test. Tests that commit skills go through
// the user-dir path, so the redirect is what keeps them from polluting
// $HOME during the test run.
func pinUserSkillsDir(t *testing.T) {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir())
}

func TestFetch_ProducesValidMetadata(t *testing.T) {
	src := stageLocalSkill(t, "tiny-skill")

	tool := NewFetch()
	args, _ := json.Marshal(map[string]string{"source": src})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	var res fetchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal result: %v\nout=%s", err, out)
	}
	if res.Name != "tiny-skill" {
		t.Errorf("Name = %q, want tiny-skill", res.Name)
	}
	if res.Description == "" {
		t.Errorf("Description should be populated, got empty")
	}
	if !strings.Contains(res.StagingPath, "seek-skill-staging-") {
		t.Errorf("StagingPath should be under seek-skill-staging-*, got %q", res.StagingPath)
	}
	// Files list should contain at least SKILL.md.
	found := false
	for _, f := range res.Files {
		if f == "SKILL.md" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Files should contain SKILL.md, got %v", res.Files)
	}
	if res.NextStepHint == "" {
		t.Errorf("NextStepHint must instruct the model to inspect before commit")
	}
}

func TestFetch_MissingSource(t *testing.T) {
	tool := NewFetch()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Errorf("expected error on missing source, got out=%q", out)
	}
	if !strings.Contains(err.Error(), "source") {
		t.Errorf("error should mention 'source', got %v", err)
	}
}

func TestFetch_InvalidSkillSource(t *testing.T) {
	// A directory with no SKILL.md should fail validation cleanly —
	// the LLM sees a clear "not a valid skill package" message
	// rather than a stack trace.
	dir := t.TempDir()
	bogus := filepath.Join(dir, "not-a-skill")
	if err := os.MkdirAll(bogus, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bogus, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewFetch()
	args, _ := json.Marshal(map[string]string{"source": bogus})
	_, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatalf("expected error on bogus source, got none")
	}
	if !strings.Contains(err.Error(), "valid skill package") {
		t.Errorf("error should explain the missing SKILL.md, got: %v", err)
	}
}

// stubAskFn returns a fixed approval decision and records the call.
// Used to drive the permission policy through Check() without a TUI.
type stubAskFn struct {
	allow  bool
	called bool
	last   permission.Action
}

func (s *stubAskFn) fn(a permission.Action) bool {
	s.called = true
	s.last = a
	return s.allow
}

func newAskPolicy(t *testing.T, ask *stubAskFn) *permission.Policy {
	t.Helper()
	p, err := permission.New("/", permission.PrefAsk)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	p.SetAskFn(ask.fn)
	return p
}

func TestCommit_UserScope(t *testing.T) {
	pinUserSkillsDir(t)
	src := stageLocalSkill(t, "happy-skill")

	// Fetch first so we have a real staging path.
	fetchOut, err := NewFetch().Execute(context.Background(),
		mustJSON(map[string]string{"source": src}))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var fr fetchResult
	if err := json.Unmarshal([]byte(fetchOut), &fr); err != nil {
		t.Fatal(err)
	}

	ask := &stubAskFn{allow: true}
	policy := newAskPolicy(t, ask)
	commit := NewCommit(policy)

	out, err := commit.Execute(context.Background(), mustJSON(map[string]any{
		"staging_path": fr.StagingPath,
		"name":         fr.Name,
		"source":       fr.Source,
		"scope":        "user",
	}))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Approval prompt was rendered with the right context.
	if !ask.called {
		t.Errorf("permission ask was not invoked")
	}
	if ask.last.SkillName != "happy-skill" {
		t.Errorf("approval action SkillName = %q, want happy-skill", ask.last.SkillName)
	}
	if ask.last.SkillSource != src {
		t.Errorf("approval action SkillSource = %q, want %q", ask.last.SkillSource, src)
	}
	// User scope: approval target shown with the ~/ tilde form.
	if !strings.HasPrefix(ask.last.SkillTarget, "~/.seek/skills/") {
		t.Errorf("user-scope approval target should start with ~/.seek/skills/, got %q", ask.last.SkillTarget)
	}
	// Result text MUST include the /new hint — that's the user's
	// only signal that they need to restart for the skill to load.
	if !strings.Contains(out, "/new") {
		t.Errorf("result must tell the user to /new, got:\n%s", out)
	}
	if !strings.Contains(out, "happy-skill") {
		t.Errorf("result must name the installed skill, got:\n%s", out)
	}
	// Result text should make the scope explicit so the model can
	// tell the user "this is just for you" vs "this is in the repo".
	if !strings.Contains(out, "user scope") {
		t.Errorf("result should declare 'user scope' explicitly, got:\n%s", out)
	}
}

func TestCommit_ProjectScope(t *testing.T) {
	// Project scope lands the skill under <cwd>/.seek/skills/<name>/
	// instead of ~/.seek/skills/. Run the commit from a tempdir so
	// we don't actually write into the seek repo we're testing in.
	projectDir := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	src := stageLocalSkill(t, "project-skill")
	fetchOut, _ := NewFetch().Execute(context.Background(), mustJSON(map[string]string{"source": src}))
	var fr fetchResult
	_ = json.Unmarshal([]byte(fetchOut), &fr)

	ask := &stubAskFn{allow: true}
	commit := NewCommit(newAskPolicy(t, ask))

	out, err := commit.Execute(context.Background(), mustJSON(map[string]any{
		"staging_path": fr.StagingPath,
		"name":         fr.Name,
		"source":       fr.Source,
		"scope":        "project",
	}))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Approval shows the project dir, not the home dir.
	if !strings.Contains(ask.last.SkillTarget, projectDir) {
		t.Errorf("project-scope approval target should point at <cwd>, got %q (cwd=%q)", ask.last.SkillTarget, projectDir)
	}
	// File landed in the project dir.
	installed := filepath.Join(projectDir, ".seek", "skills", "project-skill", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Errorf("expected project-scope install at %s: %v", installed, err)
	}
	// Project installs MUST NOT write a .install.json sidecar
	// (PRD v2 §4.2: they're git-shared and shouldn't carry local state).
	sidecar := filepath.Join(projectDir, ".seek", "skills", "project-skill", ".install.json")
	if _, err := os.Stat(sidecar); err == nil {
		t.Errorf("project-scope must NOT write .install.json, but found %s", sidecar)
	}
	// Scope hint in the result text.
	if !strings.Contains(out, "project scope") {
		t.Errorf("result should declare 'project scope' explicitly, got:\n%s", out)
	}
}

func TestCommit_RejectsMissingScope(t *testing.T) {
	pinUserSkillsDir(t)
	src := stageLocalSkill(t, "needs-scope")

	fetchOut, _ := NewFetch().Execute(context.Background(),
		mustJSON(map[string]string{"source": src}))
	var fr fetchResult
	_ = json.Unmarshal([]byte(fetchOut), &fr)

	ask := &stubAskFn{allow: true}
	commit := NewCommit(newAskPolicy(t, ask))

	// No scope passed — the schema requires it; UnmarshalStrict
	// should refuse before we ever reach the approval prompt.
	_, err := commit.Execute(context.Background(), mustJSON(map[string]any{
		"staging_path": fr.StagingPath,
		"name":         fr.Name,
		"source":       fr.Source,
	}))
	if err == nil {
		t.Fatal("commit must refuse missing scope — the model can't choose for the user")
	}
	if ask.called {
		t.Errorf("approval prompt fired despite missing scope — should be caught earlier")
	}
}

func TestCommit_RejectsInvalidScope(t *testing.T) {
	pinUserSkillsDir(t)
	src := stageLocalSkill(t, "bad-scope-skill")

	fetchOut, _ := NewFetch().Execute(context.Background(),
		mustJSON(map[string]string{"source": src}))
	var fr fetchResult
	_ = json.Unmarshal([]byte(fetchOut), &fr)

	ask := &stubAskFn{allow: true}
	commit := NewCommit(newAskPolicy(t, ask))

	_, err := commit.Execute(context.Background(), mustJSON(map[string]any{
		"staging_path": fr.StagingPath,
		"name":         fr.Name,
		"source":       fr.Source,
		"scope":        "global", // invalid — only user/project accepted
	}))
	if err == nil {
		t.Fatal("commit must reject invalid scope values")
	}
	if !strings.Contains(err.Error(), "user") || !strings.Contains(err.Error(), "project") {
		t.Errorf("error should list valid scope values, got: %v", err)
	}
	if ask.called {
		t.Errorf("approval prompt fired despite invalid scope")
	}
}

func TestCommit_Denied(t *testing.T) {
	pinUserSkillsDir(t)
	src := stageLocalSkill(t, "denied-skill")

	fetchOut, err := NewFetch().Execute(context.Background(),
		mustJSON(map[string]string{"source": src}))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var fr fetchResult
	_ = json.Unmarshal([]byte(fetchOut), &fr)

	ask := &stubAskFn{allow: false}
	policy := newAskPolicy(t, ask)
	commit := NewCommit(policy)

	_, err = commit.Execute(context.Background(), mustJSON(map[string]any{
		"staging_path": fr.StagingPath,
		"name":         fr.Name,
		"source":       fr.Source,
		"scope":        "user",
	}))
	if err == nil {
		t.Fatal("commit must return an error when the user declines")
	}
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("expected ErrDenied, got %v", err)
	}

	// Nothing should have landed on disk under the user skills dir.
	userSkills := filepath.Join(os.Getenv("SEEK_HOME"), "skills")
	if entries, _ := os.ReadDir(userSkills); len(entries) > 0 {
		t.Errorf("user skills dir should be empty after denial, got: %v", entries)
	}
}

func TestCommit_RejectsBogusStagingPath(t *testing.T) {
	pinUserSkillsDir(t)
	ask := &stubAskFn{allow: true}
	policy := newAskPolicy(t, ask)
	commit := NewCommit(policy)

	// Path that does not have the seek-skill-staging- prefix —
	// validateStagingPath should refuse it before any approval
	// prompt or filesystem mutation runs.
	_, err := commit.Execute(context.Background(), mustJSON(map[string]any{
		"staging_path": "/etc/passwd",
		"name":         "evil",
		"source":       "/etc",
		"scope":        "user",
	}))
	if err == nil {
		t.Fatal("commit must refuse paths outside the staging prefix")
	}
	if ask.called {
		t.Errorf("permission ask must NOT fire when the staging path is invalid (security: don't lure the user into approving something the tool would reject anyway)")
	}
}

func TestCommit_NameMismatch(t *testing.T) {
	pinUserSkillsDir(t)
	src := stageLocalSkill(t, "real-name")

	fetchOut, _ := NewFetch().Execute(context.Background(),
		mustJSON(map[string]string{"source": src}))
	var fr fetchResult
	_ = json.Unmarshal([]byte(fetchOut), &fr)

	ask := &stubAskFn{allow: true}
	commit := NewCommit(newAskPolicy(t, ask))

	// Caller passes a name that doesn't match the staged frontmatter —
	// could be a model bug or argument shuffle across parallel fetches.
	// Must error before approval prompt fires.
	_, err := commit.Execute(context.Background(), mustJSON(map[string]any{
		"staging_path": fr.StagingPath,
		"name":         "wrong-name",
		"source":       src,
		"scope":        "user",
	}))
	if err == nil {
		t.Fatal("commit must refuse mismatched name")
	}
	if !strings.Contains(err.Error(), "name mismatch") {
		t.Errorf("error should explain the mismatch, got: %v", err)
	}
	if ask.called {
		t.Errorf("approval prompt fired despite name mismatch — should be caught before the user is asked")
	}
}

func TestCommit_MissingRequiredArgs(t *testing.T) {
	commit := NewCommit(newAskPolicy(t, &stubAskFn{allow: true}))
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing staging_path", map[string]any{"name": "x", "source": "y", "scope": "user"}},
		{"missing name", map[string]any{"staging_path": "/tmp/seek-skill-staging-x/pkg", "source": "y", "scope": "user"}},
		{"missing source", map[string]any{"staging_path": "/tmp/seek-skill-staging-x/pkg", "name": "x", "scope": "user"}},
		{"missing scope", map[string]any{"staging_path": "/tmp/seek-skill-staging-x/pkg", "name": "x", "source": "y"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := commit.Execute(context.Background(), mustJSON(tc.args))
			if err == nil {
				t.Errorf("expected missing-field error")
			}
		})
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
