// Package skillinstall exposes two tools that, together, let the LLM
// install a skill at the user's request:
//
//   - skill_fetch  — downloads + validates a skill source into /tmp.
//     No permission gate; only writes under os.TempDir().
//   - skill_commit — moves a staged skill into ~/.seek/skills/<name>/.
//     Gated by permission.Policy (ModeAsk → user y/N prompt).
//
// The two-stage flow is deliberate: between fetch and commit the
// model can (and should) use read/grep to inspect the staged files —
// SKILL.md body, any scripts/ contents, etc. — and judge whether the
// source is legitimately what the user asked for. Squashing it into
// a single skill_install would force the user-approval step to act
// on metadata alone, which is not enough signal for the kinds of
// "did the model grab the right repo?" mistakes we want to catch.
//
// The installed skill is NOT hot-loaded into the running session. The
// loader is intentionally a startup-only operation (PRD v2 §3) so the
// prefix cache for the live conversation stays stable. skill_commit's
// result text explicitly tells the model to instruct the user to /new
// (TUI) or restart (CLI) before the new skill becomes available.
package skillinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/skillmgr"
	"github.com/whyiyhw/seek/internal/tools"
)

// --- skill_fetch ---------------------------------------------------------

const fetchName = "skill_fetch"

var fetchSchema = []byte(`{
  "type": "object",
  "properties": {
    "source": {
      "type": "string",
      "description": "Where to fetch the skill from. Three forms: (1) an absolute or repo-relative path to a local directory containing SKILL.md; (2) a git URL like https://github.com/foo/bar (optionally with #ref); (3) an https tarball URL."
    },
    "name": {
      "type": "string",
      "description": "Optional override for the resolved skill name. Default: SKILL.md frontmatter name, falling back to the source basename. Kebab-case."
    },
    "subpath": {
      "type": "string",
      "description": "For git sources only: a subdirectory inside the cloned repo that holds the SKILL.md (e.g. \"skills/foo\")."
    },
    "sha256": {
      "type": "string",
      "description": "For https tarball sources only: expected hex SHA-256 of the downloaded archive. When provided, the fetch fails if the digest does not match."
    }
  },
  "required": ["source"],
  "additionalProperties": false
}`)

const fetchDesc = "Fetch and validate a skill package WITHOUT installing it. Downloads to a /tmp staging directory and parses SKILL.md. Returns the resolved name, description, source type, list of files in the package, and the first ~500 chars of the body. Use read/grep on the returned staging_path BEFORE calling skill_commit so you can judge whether the source is legitimate."

// FetchTool implements the staging half of the install flow. No
// state, no permission gating: writes only to os.TempDir().
type FetchTool struct{}

// NewFetch returns the read-side install tool. No constructor args —
// the tool reaches no host state.
func NewFetch() FetchTool { return FetchTool{} }

func (FetchTool) Name() string            { return fetchName }
func (FetchTool) Description() string     { return fetchDesc }
func (FetchTool) Schema() json.RawMessage { return fetchSchema }

type fetchArgs struct {
	Source  string `json:"source"`
	Name    string `json:"name"`
	Subpath string `json:"subpath"`
	SHA256  string `json:"sha256"`
}

// fetchResult is the JSON the LLM sees back. Kept compact: the LLM
// uses these fields to decide if it should commit, and to choose
// which staged files to read for deeper inspection.
type fetchResult struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       string   `json:"source"`
	SourceType   string   `json:"source_type"`
	StagingPath  string   `json:"staging_path"`
	Files        []string `json:"files"`
	BodyPreview  string   `json:"body_preview"`
	NextStepHint string   `json:"next_step_hint"`
}

func (FetchTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a fetchArgs
	if err := tools.UnmarshalStrict(fetchName, raw, &a, "source", "name", "subpath", "sha256"); err != nil {
		return "", err
	}
	if a.Source == "" {
		return "", tools.MissingField(fetchName, "source", raw, "source", "name", "subpath", "sha256")
	}

	stage, err := skillmgr.Stage(skillmgr.StageOptions{
		Source:  a.Source,
		Name:    a.Name,
		Subpath: a.Subpath,
		SHA256:  a.SHA256,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", fetchName, err)
	}

	res := fetchResult{
		Name:         stage.Name,
		Description:  stage.Description,
		Source:       stage.Source,
		SourceType:   stage.Type.String(),
		StagingPath:  stage.StagingDir,
		Files:        stage.Files,
		BodyPreview:  stage.BodyPreview,
		NextStepHint: "Inspect files under staging_path with read/grep — at minimum SKILL.md and anything under scripts/ — then call skill_commit with staging_path and name to install. Refuse to commit if the body or scripts look unrelated to what the user asked for.",
	}
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%s: marshal result: %w", fetchName, err)
	}
	return string(out), nil
}

// --- skill_commit --------------------------------------------------------

const commitName = "skill_commit"

var commitSchema = []byte(`{
  "type": "object",
  "properties": {
    "staging_path": {
      "type": "string",
      "description": "The staging_path returned by skill_fetch. Must be a path under os.TempDir() with the seek-skill-staging- prefix; arbitrary paths are refused."
    },
    "name": {
      "type": "string",
      "description": "Skill name to install. Must match the name returned by skill_fetch — passed back explicitly so a mismatched call surfaces a clear error instead of silently picking up the staged name."
    },
    "source": {
      "type": "string",
      "description": "The original source string the user provided. Shown in the approval prompt so the user can confirm the URL/path that's about to be installed."
    },
    "scope": {
      "type": "string",
      "enum": ["user", "project"],
      "description": "Where to install. 'user' = ~/.seek/skills/<name>/ — available in every seek session on this machine, private to this user. 'project' = <cwd>/.seek/skills/<name>/ — shared via git with anyone who clones the repo, no .install.json sidecar (PRD v2 §4.2). REQUIRED: ASK THE USER FIRST. You cannot infer this; do not pick a default. The two scopes have very different consequences (private vs shared with the team)."
    },
    "force": {
      "type": "boolean",
      "description": "When true, overwrite an existing skill of the same name. Default false: a conflict returns an error so the user can decide whether to replace."
    }
  },
  "required": ["staging_path", "name", "source", "scope"],
  "additionalProperties": false
}`)

const commitDesc = "Install a previously-staged skill. REQUIRES (1) you have asked the user whether to install at user scope (~/.seek/skills/) or project scope (<cwd>/.seek/skills/, shared via git) and they answered, and (2) the user's interactive approval at the y/N prompt. On success the result text tells you to instruct the user to run /new (or restart) before the new skill is available — the running session's skill manifest is fixed at startup by design."

// CommitTool wraps skillmgr.Commit with a permission check. The
// policy is injected — same pattern as bash/edit/write — so the
// host (cmd/seek) controls the gate's behaviour (Ask / Yolo / Deny).
type CommitTool struct {
	policy *permission.Policy
}

// NewCommit constructs the install-half tool. policy must be non-nil;
// the tool can't operate without a gate.
func NewCommit(policy *permission.Policy) CommitTool {
	return CommitTool{policy: policy}
}

func (CommitTool) Name() string            { return commitName }
func (CommitTool) Description() string     { return commitDesc }
func (CommitTool) Schema() json.RawMessage { return commitSchema }

type commitArgs struct {
	StagingPath string `json:"staging_path"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Scope       string `json:"scope"`
	Force       bool   `json:"force"`
}

func (t CommitTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var a commitArgs
	if err := tools.UnmarshalStrict(commitName, raw, &a, "staging_path", "name", "source", "scope", "force"); err != nil {
		return "", err
	}
	for _, m := range []struct{ name, val string }{
		{"staging_path", a.StagingPath},
		{"name", a.Name},
		{"source", a.Source},
		{"scope", a.Scope},
	} {
		if m.val == "" {
			return "", tools.MissingField(commitName, m.name, raw, "staging_path", "name", "source", "scope", "force")
		}
	}
	if a.Scope != "user" && a.Scope != "project" {
		return "", fmt.Errorf("%s: scope must be 'user' or 'project', got %q. Ask the user which they want before calling skill_commit", commitName, a.Scope)
	}
	if t.policy == nil {
		return "", errors.New(commitName + ": no permission policy configured")
	}

	// Re-derive the staging tree from the path — Commit re-validates
	// internally, but we also need to feed it the Source/Force/etc.
	// the model passed. The simplest path: load the staged SKILL.md
	// to reconstruct the StageResult fields Commit needs. We trust
	// the validation inside Commit to reject a bogus staging_path.
	stage, err := loadStagedForCommit(a)
	if err != nil {
		return "", fmt.Errorf("%s: %w", commitName, err)
	}

	target, err := computeTarget(stage)
	if err != nil {
		return "", fmt.Errorf("%s: %w", commitName, err)
	}
	if err := t.policy.Check(permission.Action{
		Kind: permission.KindSkillInstall,
		Display: permission.Display{
			SkillName:   stage.Name,
			SkillSource: stage.Source,
			SkillTarget: target,
		},
	}); err != nil {
		return "", err
	}

	res, err := skillmgr.Commit(stage)
	if err != nil {
		return "", fmt.Errorf("%s: %w", commitName, err)
	}

	// Mention /new EXPLICITLY in the result text. The LLM will pass
	// it through to the user; without this hint, install completes
	// silently and the user later calls Skill(name=...) and hits
	// "not found", confused.
	scopeHint := "at user scope (~/.seek/skills/, available in every seek session on this machine)"
	if a.Scope == "project" {
		scopeHint = "at project scope (<cwd>/.seek/skills/, shared via git with anyone who clones this repo)"
	}
	body := fmt.Sprintf(
		"Installed skill %q to %s — %s.\n\nIMPORTANT: This running session's skill manifest is fixed at startup. "+
			"The new skill is NOT available in this conversation. "+
			"Tell the user to run /new (TUI) or restart seek before they can call Skill(name=%q).",
		res.Name, res.Dir, scopeHint, res.Name,
	)
	return body, nil
}

// loadStagedForCommit reconstructs the minimal StageResult that
// skillmgr.Commit needs, by parsing what's already in the staging
// dir. We don't re-run Stage (which would re-fetch from the network)
// — we trust the directory the previous fetch left behind. The
// Source / Name / Force / Scope fields come from the caller's args.
func loadStagedForCommit(a commitArgs) (*skillmgr.StageResult, error) {
	// Quick read of SKILL.md to confirm the name matches what the
	// model passed — guards against the model passing one staging
	// path with another's name (e.g. after multiple parallel fetches).
	skName, srcType, err := peekStagedName(a.StagingPath)
	if err != nil {
		return nil, err
	}
	if skName != a.Name {
		return nil, fmt.Errorf(
			"name mismatch: staging path holds skill %q but commit was called with name %q. "+
				"Did you mix up staging_path arguments from two separate skill_fetch calls?",
			skName, a.Name,
		)
	}
	stage := &skillmgr.StageResult{
		Name:       a.Name,
		Source:     a.Source,
		Type:       srcType,
		StagingDir: a.StagingPath,
		Force:      a.Force,
	}
	// Scope routing: "project" lands under <cwd>/.seek/skills/ and
	// suppresses the .install.json sidecar (PRD v2 §4.2). "user" goes
	// to ~/.seek/skills/ (the default resolveTargetParent behaviour).
	if a.Scope == "project" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve project dir: %w", err)
		}
		stage.Project = true
		stage.ProjectDir = cwd
	}
	return stage, nil
}

// peekStagedName parses the SKILL.md frontmatter in staging just
// enough to recover the skill name and source-type-from-sidecar (or
// from re-detection). Kept narrow on purpose — anything more is
// duplicating skill.Parse for no benefit.
func peekStagedName(stagingDir string) (string, skillmgr.SourceType, error) {
	// Prefer SKILL.md uppercase, fall back to lowercase.
	for _, fname := range []string{"SKILL.md", "skill.md"} {
		p := filepath.Join(stagingDir, fname)
		data, err := readFile(p)
		if err != nil {
			continue
		}
		name, err := frontmatterName(data)
		if err != nil || name == "" {
			// Frontmatter parse failed; fall back to dir name.
			name = filepath.Base(stagingDir)
		}
		// Source type is unknowable from disk alone; default to
		// "local" — Commit doesn't actually use this for anything
		// except the InstallResult, and the caller already knows.
		return name, skillmgr.SourceLocal, nil
	}
	return "", skillmgr.SourceAuto, errors.New("staged package has no SKILL.md or skill.md")
}

// computeTarget renders the install path string for the approval
// prompt. The path the user sees in the y/N prompt MUST match where
// Commit will actually write — otherwise the approval is for a
// different action than the one that runs.
//
// Project scope: <cwd>/.seek/skills/<name>/. User scope: shown as
// "~/.seek/skills/<name>/" (the tilde is friendlier than the absolute
// path; Commit itself does the real path resolution via paths.UserSkills).
func computeTarget(stage *skillmgr.StageResult) (string, error) {
	if stage.Project {
		return filepath.Join(stage.ProjectDir, ".seek", "skills", stage.Name) + "/", nil
	}
	return "~/.seek/skills/" + stage.Name + "/", nil
}

// readFile + frontmatterName are tiny duplications of skill package
// internals. Importing internal/skill from a tool package is allowed
// (it's a sibling, not a cycle), but the parse-name-only path is
// 12 lines and the dependency direction is cleaner this way: the
// tool depends on skillmgr (which already depends on skill),
// not directly on skill.

func readFile(p string) ([]byte, error) {
	f, err := openFile(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAll(f)
}

func frontmatterName(data []byte) (string, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", errors.New("no frontmatter delimiter")
	}
	// Find closing ---
	rest := s[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", errors.New("unterminated frontmatter")
	}
	front := rest[:end]
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:")), nil
		}
	}
	return "", errors.New("frontmatter has no name field")
}
