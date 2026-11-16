// Package agentsmgr owns the write path for typed-agent definition
// files — the agent-type counterpart of internal/skillmgr
// (feature-orchestration.md §5, M-O2.3 / D10).
//
// The loader in internal/agenttypes is intentionally read-only; this
// package is the ONLY one that mutates the filesystem under
// ~/.seek/agents/ (or <project>/.seek/agents/ for --project). Same red
// line as skillmgr/skillstats for skills: hand-editing the files works
// (they're plain Markdown), but seek itself funnels every write
// through here so validation runs before anything lands on disk.
//
// Scope discipline is the package's second contract: every write AND
// delete stays inside one of the two loader-visible scope
// directories. There is deliberately no --into knob (skillmgr.Create
// has one) — an arbitrary target parent would place definitions where
// the loader never looks, which is worse than refusing. Both Create
// and Remove gate the name through agenttypes' kebab-case rule before
// any filesystem call and re-assert the resolved path sits inside the
// scope dir (filepath.Rel) as belt-and-braces.
//
// What this package does NOT own:
//   - Parsing/validation rules — agenttypes.Parse is the single
//     authority. Create serialises, round-trips through Parse, and
//     refuses to write anything Parse would reject (a scaffold that
//     loads with a startup warning would be a bug factory).
//   - CLI wiring — that's internal/agentscli, which translates flags
//     into these options.
package agentsmgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/agenttypes"
	"github.com/whyiyhw/seek/internal/paths"
)

// CreateOptions controls Create. Name + Description are the minimum
// inputs (same required fields agenttypes.Parse enforces); Body is
// optional — empty scaffolds a TODO role prompt. Everything else maps
// 1:1 onto the D11 frontmatter contract and is optional.
type CreateOptions struct {
	Name        string
	Description string
	Body        string // "" = scaffold a TODO role prompt
	Tools       []string
	Provider    string
	Model       string
	Effort      string
	Workflow    string
	MaxTurns    int

	// Project=true writes <ProjectDir>/.seek/agents/<name>.md (the
	// checked-in, team-shared scope); default writes the user scope
	// (<UserDir> override, else ~/.seek/agents/). These are the only
	// two writable locations — see the package doc.
	Project    bool
	ProjectDir string // base for --project (default ".")
	UserDir    string // base for non-project (default ~/.seek/agents/)
}

// CreateResult records what was written.
type CreateResult struct {
	Name string
	Path string // absolute path of the created .md file
}

// Create scaffolds one agent-definition file. Validation order is
// load-bearing: the full file content is built in memory and round-
// tripped through agenttypes.Parse BEFORE any filesystem work, so an
// illegal shape (bad name, missing description, forbidden tool,
// loosening workflow, ...) never creates a directory entry — the
// failure mode is a plain error, not a file that loads with a warning.
func Create(opts CreateOptions) (*CreateResult, error) {
	if opts.Name == "" {
		return nil, errors.New("agentsmgr: create: Name is required")
	}
	if opts.Description == "" {
		return nil, errors.New("agentsmgr: create: Description is required (agent types won't load without it)")
	}
	body := opts.Body
	if strings.TrimSpace(body) == "" {
		body = scaffoldBody(opts.Name)
	}

	data := []byte(serialize(opts.Name, opts.Description, body, opts))
	// Single validation authority: anything Parse rejects, Create
	// rejects. fileStem = Name so the name-fallback path agrees with
	// the filename even if the frontmatter name were absent (it never
	// is — serialize always writes it).
	if _, err := agenttypes.Parse(data, opts.Name, "create"); err != nil {
		return nil, fmt.Errorf("agentsmgr: create: %w", err)
	}

	dir, err := resolveScopeDir(opts)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(dir, opts.Name+".md")
	// Belt-and-braces on top of Parse's kebab-case name check: the
	// final path must sit inside the scope dir. Parse already rejects
	// separators and dots, so this can only fire if the two checks
	// ever drift apart — cheap insurance for the "writes never leave
	// the scope" contract.
	if rel, err := filepath.Rel(dir, target); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("agentsmgr: create: target %q escapes scope dir %q", target, dir)
	}

	// Lstat (not Stat): a dangling symlink at the target must count
	// as "exists" — Stat reports ENOENT for it and the WriteFile
	// below would happily write THROUGH the link to wherever it
	// points. Refusing is the only safe answer for a create path.
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("agentsmgr: create: %s already exists (agent types are single files; edit or delete it, there is no --force)", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("agentsmgr: stat target: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("agentsmgr: mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return nil, fmt.Errorf("agentsmgr: write %s: %w", target, err)
	}
	return &CreateResult{Name: opts.Name, Path: target}, nil
}

// RemoveOptions controls Remove.
type RemoveOptions struct {
	Name    string
	UserDir string // overrides paths.UserAgents() — tests inject

	// Builtins (optional, mirrors skillmgr.Uninstall): when provided,
	// Remove refuses names whose winning definition is a built-in —
	// built-ins live in the binary, not on disk.
	Builtins *agenttypes.Set
}

// RemoveResult records what was deleted.
type RemoveResult struct {
	Name string
	Path string
}

// Remove deletes a user-scope agent definition. Project-scope files
// are deliberately not reachable from here — they are checked in and
// shared by the team, so removal goes through git like any other
// source edit (same stance as skillmgr.Uninstall).
//
// Name validation is the same authority Create inherits from Parse
// (agenttypes.ValidName): kebab-case admits no separators, dots, or
// backslashes, so a "../escape" name is refused here, before any
// filesystem call — Remove takes a raw CLI argument, and without this
// gate filepath.Join would happily resolve it outside the scope dir
// and os.Remove would delete an arbitrary .md.
func Remove(opts RemoveOptions) (*RemoveResult, error) {
	if opts.Name == "" {
		return nil, errors.New("agentsmgr: remove: Name is required")
	}
	if !agenttypes.ValidName(opts.Name) {
		return nil, fmt.Errorf("agentsmgr: remove: name %q must be kebab-case ([a-z][a-z0-9-]*)", opts.Name)
	}
	if opts.Builtins != nil {
		if def := opts.Builtins.Get(opts.Name); def != nil && def.Origin == agenttypes.OriginBuiltin {
			return nil, fmt.Errorf("agentsmgr: remove: %q is a built-in agent type and cannot be removed", opts.Name)
		}
	}
	dir := opts.UserDir
	if dir == "" {
		p, err := paths.UserAgents()
		if err != nil {
			return nil, fmt.Errorf("agentsmgr: resolve user agents dir: %w", err)
		}
		dir = p
	}
	target := filepath.Join(dir, opts.Name+".md")
	// Belt-and-braces twin of Create's check: whatever the name rule
	// admits, the resolved path must stay inside the scope dir. Only
	// fires if the two checks ever drift apart — cheap insurance for
	// the "deletes never leave the scope" contract.
	if rel, err := filepath.Rel(dir, target); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("agentsmgr: remove: target %q escapes scope dir %q", target, dir)
	}
	if err := os.Remove(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("agentsmgr: %q not found in %s (project-level agent types are checked in — remove those via git)", opts.Name, dir)
		}
		return nil, fmt.Errorf("agentsmgr: remove %s: %w", target, err)
	}
	return &RemoveResult{Name: opts.Name, Path: target}, nil
}

// resolveScopeDir picks the one directory Create may write into.
// Compare skillmgr.resolveCreateParent: no --into branch here, by
// design (package doc).
func resolveScopeDir(opts CreateOptions) (string, error) {
	if opts.Project {
		base := opts.ProjectDir
		if base == "" {
			base = "."
		}
		abs, err := filepath.Abs(base)
		if err != nil {
			return "", fmt.Errorf("agentsmgr: resolve project dir: %w", err)
		}
		return filepath.Join(abs, ".seek", "agents"), nil
	}
	if opts.UserDir != "" {
		return opts.UserDir, nil
	}
	p, err := paths.UserAgents()
	if err != nil {
		return "", fmt.Errorf("agentsmgr: resolve user agents dir: %w", err)
	}
	return p, nil
}

// serialize renders the D11 frontmatter contract plus body. Only
// non-empty optional keys are written so the file a user reads lists
// exactly what was configured. The description is flattened to one
// line and bracket-quoted when needed so frontmatter.Split reads it
// back as a scalar (a value starting with `[` parses as an inline
// list — Parse's round-trip in Create is the net that catches any
// shape we fail to sanitise here).
func serialize(name, description, body string, opts CreateOptions) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", name)
	fmt.Fprintf(&b, "description: %s\n", scalarValue(sanitizeLine(description)))
	if len(opts.Tools) > 0 {
		fmt.Fprintf(&b, "tools: [%s]\n", strings.Join(opts.Tools, ", "))
	}
	if opts.Provider != "" {
		fmt.Fprintf(&b, "provider: %s\n", opts.Provider)
	}
	if opts.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", opts.Model)
	}
	if opts.Effort != "" {
		fmt.Fprintf(&b, "effort: %s\n", opts.Effort)
	}
	if opts.Workflow != "" {
		fmt.Fprintf(&b, "workflow: %s\n", opts.Workflow)
	}
	if opts.MaxTurns > 0 {
		fmt.Fprintf(&b, "max_turns: %d\n", opts.MaxTurns)
	}
	b.WriteString("---\n\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// scaffoldBody is the TODO role prompt written when Create got no
// Body. It parses (non-empty) so the scaffold is immediately loadable
// — the author fills in the role, not the required fields.
func scaffoldBody(name string) string {
	return fmt.Sprintf(`You are a %s subagent.

TODO: describe the role prompt — what this type should do, how it
should work, and how it should report back. This body replaces the
built-in template's role clause, so be specific about the job.
`, name)
}

// sanitizeLine collapses newlines/tabs to spaces so a multi-line
// description can't smuggle extra frontmatter lines into the file.
func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

// scalarValue double-quotes a value that would otherwise parse as an
// inline list (leading `[`), letting unquote restore it verbatim.
func scalarValue(s string) string {
	if strings.HasPrefix(s, "[") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}
