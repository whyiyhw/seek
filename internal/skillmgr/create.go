package skillmgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// nameRE mirrors internal/skill's nameRE — kept local to avoid
// exporting/importing it across package boundaries. Skill names are
// kebab-case starting with a letter (PRD v0 §4.6.1 contract).
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// CreateOptions controls Create. Name + Description are the minimum
// inputs; everything else picks a sensible default.
type CreateOptions struct {
	Name        string // required, kebab-case
	Description string // required; multi-line allowed but stored single-line for frontmatter clarity

	// Target selection (PRD v2 §5.1, in priority order):
	//   1. Into (explicit parent dir)
	//   2. Project=true → <ProjectDir>/.seek/skills/
	//   3. default      → <UserDir> (or paths.UserSkills())
	Into       string
	Project    bool
	ProjectDir string // base for --project (default ".")
	UserDir    string // base for non-project (default paths.UserSkills())
}

// CreateResult records what was written.
type CreateResult struct {
	Name string
	Dir  string
}

// Create scaffolds a new skill package on disk. PRD v2 §5.1:
//
//	<target>/<name>/
//	├── SKILL.md      # frontmatter (name, description, version=0.1.0)
//	│                 # + when-to-use / steps / references TODOs
//	├── references/   # .gitkeep so the empty dir survives version
//	│   └── .gitkeep  # control round-trips
//	└── README.md     # human-facing intro template
//
// Refuses if the target directory already exists; there is no
// --force here (PRD §5.1: "creating implies new — use install
// --force to overwrite existing"). The kebab-case name regex is
// applied before any filesystem work so invalid names never see
// a stray mkdir.
func Create(opts CreateOptions) (*CreateResult, error) {
	if opts.Name == "" {
		return nil, errors.New("skillmgr: create: Name is required")
	}
	if !nameRE.MatchString(opts.Name) {
		return nil, fmt.Errorf("skillmgr: create: name %q must be kebab-case ([a-z][a-z0-9-]*)", opts.Name)
	}
	if opts.Description == "" {
		return nil, errors.New("skillmgr: create: Description is required (skill won't load without it)")
	}

	parent, err := resolveCreateParent(opts)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(parent, opts.Name)

	// Refuse-if-exists is load-bearing: this is the "new skill"
	// path, and silently overwriting an existing dir would be very
	// surprising. The user can switch to install --force if that's
	// what they want.
	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("skillmgr: create: %s already exists; choose a different name or delete the existing directory", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("skillmgr: stat target: %w", err)
	}

	// MkdirAll on parent is safe; create the target itself with
	// Mkdir so a race that put the dir there between the Stat above
	// and now still produces an error.
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("skillmgr: mkdir parent: %w", err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return nil, fmt.Errorf("skillmgr: mkdir %s: %w", target, err)
	}

	// Files go down in order most-important-first so a partial
	// failure (out of disk after SKILL.md) leaves the user with
	// the minimum viable artefact rather than just empty dirs.
	if err := writeSkillTemplate(target, opts); err != nil {
		_ = os.RemoveAll(target) // roll back so retry isn't blocked by "already exists"
		return nil, err
	}
	if err := writeReferencesGitkeep(target); err != nil {
		_ = os.RemoveAll(target)
		return nil, err
	}
	if err := writeReadmeTemplate(target, opts); err != nil {
		_ = os.RemoveAll(target)
		return nil, err
	}
	return &CreateResult{Name: opts.Name, Dir: target}, nil
}

// resolveCreateParent mirrors install's resolveTargetParent but with
// the extra --into knob that install doesn't need.
func resolveCreateParent(opts CreateOptions) (string, error) {
	if opts.Into != "" {
		abs, err := filepath.Abs(opts.Into)
		if err != nil {
			return "", fmt.Errorf("resolve --into: %w", err)
		}
		return abs, nil
	}
	if opts.Project {
		base := opts.ProjectDir
		if base == "" {
			base = "."
		}
		abs, err := filepath.Abs(base)
		if err != nil {
			return "", fmt.Errorf("resolve project dir: %w", err)
		}
		return filepath.Join(abs, ".seek", "skills"), nil
	}
	return resolveUserDir(opts.UserDir)
}

// writeSkillTemplate emits a SKILL.md that's immediately loadable
// (frontmatter passes the parser's required-field checks) AND gives
// the author concrete TODO anchors so they don't have to start from
// a blank page.
func writeSkillTemplate(target string, opts CreateOptions) error {
	const tmpl = `---
name: %s
description: %s
version: 0.1.0
---

# %s

## When to use this skill

TODO: describe the user-request shapes that should trigger this
skill. Be concrete — the model only sees the description above in
the manifest, but it sees this section after invoking the Skill tool.

## Steps

1. TODO: first step
2. TODO: ...

## References

- [Example reference](references/example.md)
`
	body := fmt.Sprintf(tmpl, opts.Name, opts.Description, opts.Name)
	return os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(body), 0o644)
}

// writeReferencesGitkeep creates references/.gitkeep so the
// directory survives `git add .` even before the author drops real
// reference docs in.
func writeReferencesGitkeep(target string) error {
	refsDir := filepath.Join(target, "references")
	if err := os.Mkdir(refsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir references: %w", err)
	}
	return os.WriteFile(filepath.Join(refsDir, ".gitkeep"), nil, 0o644)
}

// writeReadmeTemplate is the human-facing intro. Distinct from
// SKILL.md (which is for the model) — this is what shows up in a
// GitHub repo view.
func writeReadmeTemplate(target string, opts CreateOptions) error {
	const tmpl = `# %s

%s

## Layout

- ` + "`SKILL.md`" + ` — entry point read by the agent (do not rename)
- ` + "`references/`" + ` — long-form docs the agent loads on demand
- ` + "`README.md`" + ` — this file, for humans

## Install

` + "```bash" + `
seek skill install ./%s
` + "```" + `
`
	body := fmt.Sprintf(tmpl, opts.Name, opts.Description, opts.Name)
	return os.WriteFile(filepath.Join(target, "README.md"), []byte(body), 0o644)
}
