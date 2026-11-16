package agentsmgr

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/agenttypes"
)

// write is the shared test helper: create parents + write file.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------- Create ----------

// TestCreate_RoundTripsThroughParse: the core Create contract — every
// file it writes must parse cleanly through the single validation
// authority (agenttypes.Parse). A scaffold that loads with a startup
// warning would be a bug factory.
func TestCreate_RoundTripsThroughParse(t *testing.T) {
	user := t.TempDir()
	cases := []CreateOptions{
		{Name: "verifier", Description: "Runs the test suite and reports failures."},
		{Name: "librarian", Description: "finds docs", Body: "You index things."},
		{
			Name:        "auditor",
			Description: "one-line",
			Body:        "You audit.",
			Tools:       []string{"read", "grep", "git"},
			Provider:    "glm",
			Model:       "glm-5.3",
			Effort:      "low",
			Workflow:    "plan-analyze",
			MaxTurns:    60,
		},
	}
	for _, opts := range cases {
		opts.UserDir = user
		res, err := Create(opts)
		if err != nil {
			t.Fatalf("Create(%s): %v", opts.Name, err)
		}
		if res.Path != filepath.Join(user, opts.Name+".md") {
			t.Errorf("Create(%s).Path = %s, want inside user dir", opts.Name, res.Path)
		}
		data, err := os.ReadFile(res.Path)
		if err != nil {
			t.Fatal(err)
		}
		stem := strings.TrimSuffix(filepath.Base(res.Path), ".md")
		def, err := agenttypes.Parse(data, stem, res.Path)
		if err != nil {
			t.Errorf("scaffolded %s does not parse: %v\n---file---\n%s", opts.Name, err, data)
			continue
		}
		if def.Name != opts.Name || def.Description != opts.Description {
			t.Errorf("%s round-trip got name=%q description=%q", opts.Name, def.Name, def.Description)
		}
		if opts.Body != "" && def.Body != opts.Body+"\n" {
			t.Errorf("%s body round-trip mismatch:\n got %q\nwant %q", opts.Name, def.Body, opts.Body+"\n")
		}
		if len(opts.Tools) > 0 && !reflect.DeepEqual(def.Tools, opts.Tools) {
			t.Errorf("%s tools round-trip: got %v want %v", opts.Name, def.Tools, opts.Tools)
		}
	}
}

// TestCreate_IllegalFrontmatterRefusedBeforeWrite: every illegal D11
// shape must fail Create with a parse-flavoured error AND leave the
// filesystem untouched — no directory, no half-written file.
func TestCreate_IllegalFrontmatterRefusedBeforeWrite(t *testing.T) {
	user := t.TempDir()
	cases := []struct {
		name string
		opts CreateOptions
		want string // substring of the expected error
	}{
		{
			name: "uppercase name",
			opts: CreateOptions{Name: "Verifier", Description: "d", Body: "b"},
			want: "kebab-case",
		},
		{
			name: "path traversal name",
			opts: CreateOptions{Name: "../evil", Description: "d", Body: "b"},
			want: "kebab-case",
		},
		{
			name: "nested path name",
			opts: CreateOptions{Name: "a/b", Description: "d", Body: "b"},
			want: "kebab-case",
		},
		{
			name: "missing description",
			opts: CreateOptions{Name: "no-desc", Description: "", Body: "b"},
			want: "Description is required",
		},
		{
			name: "forbidden tool",
			opts: CreateOptions{Name: "bad-tool", Description: "d", Body: "b", Tools: []string{"read", "agent"}},
			want: "never allowed",
		},
		{
			name: "loosening workflow",
			opts: CreateOptions{Name: "bad-flow", Description: "d", Body: "b", Workflow: "plan-execute"},
			want: `only "plan-analyze" is allowed`,
		},
		{
			name: "bad effort",
			opts: CreateOptions{Name: "bad-effort", Description: "d", Body: "b", Effort: "turbo"},
			want: "effort",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.UserDir = user
			_, err := Create(tc.opts)
			if err == nil {
				t.Fatal("Create succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
			// The write must be gated on validation: nothing may exist.
			if _, statErr := os.Lstat(filepath.Join(user, tc.opts.Name+".md")); statErr == nil {
				t.Errorf("file exists after refused Create: %s", filepath.Join(user, tc.opts.Name+".md"))
			} else if !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("unexpected lstat error: %v", statErr)
			}
		})
	}
	// And the scope directory itself was never created — validation
	// runs before any filesystem work, including MkdirAll.
	if _, err := os.Stat(user); err == nil {
		if entries, _ := os.ReadDir(user); len(entries) > 0 {
			t.Errorf("user dir has entries after all-refused Creates: %v", entries)
		}
	}
}

// TestCreate_WritesOnlyInsideScope: the scope-discipline contract
// ("写路径不越 scope"). With well-formed input, the ONLY path touched
// under the scope root is <scope>/<name>.md — and nothing outside the
// scope dir changes at all.
func TestCreate_WritesOnlyInsideScope(t *testing.T) {
	project := t.TempDir()
	neighbour := filepath.Join(project, "keeper.txt")
	write(t, neighbour, "do not touch")

	projAgents := filepath.Join(project, ".seek", "agents")
	res, err := Create(CreateOptions{
		Name:        "verifier",
		Description: "d",
		Body:        "b",
		Project:     true,
		ProjectDir:  project,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(projAgents, "verifier.md")
	if res.Path != wantPath {
		t.Errorf("Path = %s, want %s", res.Path, wantPath)
	}
	if data, err := os.ReadFile(neighbour); err != nil || string(data) != "do not touch" {
		t.Errorf("neighbour file disturbed: %q, %v", data, err)
	}
	entries, err := os.ReadDir(projAgents)
	if err != nil || len(entries) != 1 || entries[0].Name() != "verifier.md" {
		t.Errorf("scope dir should hold exactly verifier.md, got %v (%v)", entries, err)
	}
}

// TestCreate_RefusesExistingTarget: no --force on the create path
// (mirrors skillmgr.Create). Includes the dangling-symlink variant:
// Lstat must see it as "exists" instead of writing through the link.
func TestCreate_RefusesExistingTarget(t *testing.T) {
	user := t.TempDir()
	opts := CreateOptions{Name: "verifier", Description: "d", Body: "b", UserDir: user}
	if _, err := Create(opts); err != nil {
		t.Fatal(err)
	}
	_, err := Create(opts)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second Create err = %v, want already-exists", err)
	}

	// Dangling symlink: points outside the scope; a naive Stat+Write
	// would follow it and land at the symlink's target.
	outside := t.TempDir()
	link := filepath.Join(user, "ghost.md")
	if err := os.Symlink(filepath.Join(outside, "pwned.md"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err = Create(CreateOptions{Name: "ghost", Description: "d", Body: "b", UserDir: user})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Create over dangling symlink err = %v, want already-exists", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "pwned.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("write escaped scope through symlink")
	}
}

// TestCreate_DescriptionSanitised: multi-line and bracket-leading
// descriptions must survive the round-trip as single scalars.
func TestCreate_DescriptionSanitised(t *testing.T) {
	user := t.TempDir()
	res, err := Create(CreateOptions{
		Name:        "multi",
		Description: "line one\nline two",
		Body:        "b",
		UserDir:     user,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(res.Path)
	def, err := agenttypes.Parse(data, "multi", res.Path)
	if err != nil {
		t.Fatalf("sanitised description does not parse: %v\n%s", err, data)
	}
	if def.Description != "line one line two" {
		t.Errorf("description = %q, want newline collapsed", def.Description)
	}

	res, err = Create(CreateOptions{
		Name:        "bracket",
		Description: "[looks like a list]",
		Body:        "b",
		UserDir:     user,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(res.Path)
	def, err = agenttypes.Parse(data, "bracket", res.Path)
	if err != nil {
		t.Fatalf("bracket description does not parse: %v\n%s", err, data)
	}
	if def.Description != "[looks like a list]" {
		t.Errorf("description = %q, want the bracketed text verbatim", def.Description)
	}
}

// ---------- Remove ----------

// TestRemove_DeletesUserScopeFile: the plain path.
func TestRemove_DeletesUserScopeFile(t *testing.T) {
	user := t.TempDir()
	if _, err := Create(CreateOptions{Name: "verifier", Description: "d", Body: "b", UserDir: user}); err != nil {
		t.Fatal(err)
	}
	res, err := Remove(RemoveOptions{Name: "verifier", UserDir: user})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(user, "verifier.md") {
		t.Errorf("Path = %s", res.Path)
	}
	if _, err := os.Lstat(res.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still present after Remove: %v", err)
	}
}

// TestRemove_RefusesBuiltins: with a loaded registry wired in, the
// built-in names are not removable (they live in the binary).
func TestRemove_RefusesBuiltins(t *testing.T) {
	set, _, err := agenttypes.Load(agenttypes.LoadOptions{
		ProjectDir:    t.TempDir(),
		UserAgentsDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"general-purpose", "explore", "plan"} {
		_, err := Remove(RemoveOptions{Name: name, UserDir: t.TempDir(), Builtins: set})
		if err == nil || !strings.Contains(err.Error(), "built-in") {
			t.Errorf("Remove(%q) err = %v, want built-in refusal", name, err)
		}
	}
}

// TestRemove_MissingNamesHintAtProjectScope: a name that only exists
// project-side must produce the "via git" hint, not a bare ENOENT.
func TestRemove_MissingNamesHintAtProjectScope(t *testing.T) {
	_, err := Remove(RemoveOptions{Name: "nope", UserDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "via git") {
		t.Fatalf("err = %v, want project-scope hint", err)
	}
	if _, err := Remove(RemoveOptions{Name: "", UserDir: t.TempDir()}); err == nil {
		t.Fatal("empty Name accepted")
	}
}

// TestRemove_RejectsScopeEscapingNames: Remove takes a raw CLI
// argument as its name; without the kebab-case gate a "../x" name
// would resolve outside the scope dir and os.Remove an arbitrary .md.
// Every escaping shape must error with the kebab-case message AND
// leave the outside-scope sentinel untouched.
func TestRemove_RejectsScopeEscapingNames(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	if err := os.MkdirAll(user, 0o755); err != nil {
		t.Fatal(err)
	}
	// Sentinel outside the scope dir: root/x.md is the exact file a
	// "../x" name resolves to. It must survive every refused Remove.
	sentinel := filepath.Join(root, "x.md")
	write(t, sentinel, "outside the scope, must survive")

	for _, name := range []string{"../x", "a/b", `..\x`, "/abs", "..", ".", "con/../../x"} {
		_, err := Remove(RemoveOptions{Name: name, UserDir: user})
		if err == nil {
			t.Errorf("Remove(%q) succeeded, want refusal", name)
			continue
		}
		if !strings.Contains(err.Error(), "kebab-case") {
			t.Errorf("Remove(%q) err = %v, want kebab-case message", name, err)
		}
		if data, rerr := os.ReadFile(sentinel); rerr != nil || string(data) != "outside the scope, must survive" {
			t.Errorf("Remove(%q): out-of-scope sentinel disturbed (%v)", name, rerr)
		}
	}
	// The scope dir itself stayed untouched by the refused names.
	entries, err := os.ReadDir(user)
	if err != nil || len(entries) != 0 {
		t.Errorf("user dir disturbed by refused Removes: %v (%v)", entries, err)
	}
}
