// Package git is the read-only git wrapper exposed as a tool. It
// exists for one reason: in plan mode the bash tool is denied (and
// in ask mode it pops a y/N for every shell command), so the model
// can't run `git log` / `git diff` / `git blame` to inspect history.
// Those are *read-only* operations — they belong in plan mode and
// should not require interactive approval — but the bash tool can't
// distinguish them from `rm -rf`.
//
// The git tool solves this with a hardcoded subcommand whitelist.
// Only read operations are dispatched; mutating ones (push, commit,
// reset, checkout, rebase, merge, clean, fetch, pull, clone,
// worktree) are refused before exec.Command is even constructed.
// permission.KindGit treats the tool as safe in every mode — the
// safety property comes from THIS file, not from the policy.
//
// Defence in depth:
//   - No shell. exec.Command("git", subcommand, args...) directly;
//     args are passed as a slice, never interpolated.
//   - Subcommand allowlist (read ops only).
//   - Arg blocklist (-c, --exec, --upload-pack, etc.) catches
//     workarounds — `git -c core.sshCommand='rm -rf'` would be
//     blocked even though `-c` looks innocent.
//   - Output cap (500 lines hard). Default 100. Model can lower
//     but cannot raise above 500. Mirrors the read tool's policy.
//   - 30s timeout. git operations that hang (network, stuck index
//     lock) can't park the agent goroutine forever.
//   - --no-color via -c color.ui=false. The tool result the model
//     sees stays plain text — no ANSI codes leaking into prompts.
package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/tools"
)

const toolName = "git"

// hardLineCap is the absolute maximum number of lines returned from
// any git invocation. The model's max_lines arg is clamped to this
// before the command runs — there is no way to bypass it. Same
// philosophy as the read tool's 50-line cap: the safety is in the
// tool, not in the schema (which the LLM can lie about).
const hardLineCap = 500

// defaultLineCap is what runs when the model doesn't specify a
// max_lines. 100 lines is enough to see, e.g., 20 recent commits in
// `git log --oneline` or a moderate-sized diff. Larger queries need
// the model to explicitly opt in by passing max_lines.
const defaultLineCap = 100

// execTimeout caps how long a single git invocation can run. 30s
// covers `git blame` on a 5K-line file or `git diff` across a
// thousand-commit range; longer is suspicious and usually means a
// stuck index or a misuse (e.g. blaming a 100K-line file).
const execTimeout = 30 * time.Second

// allowedSubcommands is the complete list of git subcommands the
// tool will dispatch. Read-only by construction. New additions
// require evidence the subcommand cannot mutate the working tree,
// the index, the object database, or the remote.
//
// Local-only reads: the bulk of the list. No network, no disk
// writes, no ref updates.
//
// Network reads: `ls-remote` is the one exception. It hits a remote
// to enumerate refs but doesn't fetch their objects, doesn't write
// to .git/, and doesn't update local refs. We allow it for "what
// branches/tags exist on origin?" style questions; the 30s timeout
// keeps a slow or stalled remote from parking the agent.
var allowedSubcommands = map[string]bool{
	"log":       true, // commit history
	"diff":      true, // file/commit/branch diffs
	"show":      true, // commit, tag, or blob inspection
	"status":    true, // working-tree state (read-only with default args)
	"blame":     true, // line-by-line authorship
	"branch":    true, // list branches (default; -d/-D blocked by arg blocklist)
	"tag":       true, // list tags (similarly)
	"rev-parse": true, // resolve refs to hashes
	"ls-files":  true, // list tracked files
	"ls-tree":   true, // list a tree object's entries
	"cat-file":  true, // read object content
	"shortlog":  true, // grouped commit log by author
	"describe":  true, // ref → human-readable name
	"reflog":    true, // ref history (default `reflog show`, read-only)
	"ls-remote": true, // list refs on a remote — NETWORK READ, no fetch
}

// blockedArgPrefixes refuses arguments that could subvert the
// read-only contract even when the subcommand itself is on the
// allowlist:
//
//   - `-c` lets the caller override any config key for one
//     invocation. `git -c core.sshCommand='rm -rf /' log` is a
//     well-known footgun.
//   - `--exec`, `--upload-pack`, `--receive-pack` invoke remote
//     programs.
//   - `-C` changes the working directory; combined with relative
//     paths elsewhere this opens shenanigans.
//   - `--git-dir`, `--work-tree` redirect git away from the project
//     root, defeating the cwd guarantee.
//   - `-o`, `--output` write to disk; some subcommands (`diff`,
//     `format-patch`) accept these.
//   - Any subcommand-style flag for mutating operations: `--delete`,
//     `--force`, `--prune` belong to branch/tag/reflog forms that
//     mutate refs. Even though we only allow read subcommands,
//     these are belt-and-suspenders for unforeseen attacks.
//   - `--exec-path=` overrides where git looks for plumbing.
var blockedArgs = []string{
	"-c",
	"-C",
	"--git-dir",
	"--git-dir=",
	"--work-tree",
	"--work-tree=",
	"--exec",
	"--exec=",
	"--exec-path",
	"--exec-path=",
	"--upload-pack",
	"--upload-pack=",
	"--receive-pack",
	"--receive-pack=",
	"-o",
	"--output",
	"--output=",
	"--delete",
	"-d",
	"-D",
	"--force",
	"-f",
	"--prune",
}

var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "subcommand": {
      "type": "string",
      "description": "Which git subcommand to run. Read-only ops only. Local-only reads: log, diff, show, status, blame, branch, tag, rev-parse, ls-files, ls-tree, cat-file, shortlog, describe, reflog. Network read (one exception, no fetch / no ref update): ls-remote. Mutating ops (commit, push, reset, checkout, rebase, merge, clean, fetch, pull, clone) are refused — use bash for those and accept the permission prompt."
    },
    "args": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Additional flags and positional args, one per element. Examples: [\"--oneline\",\"-n\",\"20\"], [\"HEAD~5..HEAD\"], [\"--stat\",\"main...feature\"]. NEVER include -c, -C, --git-dir, --work-tree, --exec, --upload-pack, --output, --delete, -d, -D, --force, -f, --prune (refused). For destructive operations, use bash."
    },
    "max_lines": {
      "type": "integer",
      "description": "Cap on output lines. Default 100, hard maximum 500. Use this to keep 'git log' and 'git diff' bounded. To see more history use --oneline + -n N rather than raising this cap."
    }
  },
  "required": ["subcommand"],
  "additionalProperties": false
}`)

const description = "Run a read-only git subcommand (log, diff, status, blame, etc.) and return its output, capped at 500 lines. ALLOWED in plan mode — this is how the model inspects history without falling back to bash (which is blocked under plan). Use this for any git query; do NOT use bash for 'git log' or 'git diff' when this tool can do it."

// Tool is the git wrapper. Stateless: no fields. cwd resolves at
// each call so tests can chdir without re-constructing the tool.
type Tool struct{}

// New returns the git tool. No constructor args — the tool reaches
// no host state; safety is in the package-level whitelist.
func New() Tool { return Tool{} }

func (Tool) Name() string            { return toolName }
func (Tool) Description() string     { return description }
func (Tool) Schema() json.RawMessage { return schemaBytes }

// ReadOnly marks this tool as safe for concurrent dispatch.
// Mirrors the read/grep/list_dir pattern — the agent batches
// read-only tool calls in parallel.
func (Tool) ReadOnly() bool { return true }

type args struct {
	Subcommand string   `json:"subcommand"`
	Args       []string `json:"args"`
	MaxLines   int      `json:"max_lines"`
}

func (Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a args
	if err := tools.UnmarshalStrict(toolName, raw, &a, "subcommand", "args", "max_lines"); err != nil {
		return "", err
	}
	if a.Subcommand == "" {
		return "", tools.MissingField(toolName, "subcommand", raw, "subcommand", "args", "max_lines")
	}
	if !allowedSubcommands[a.Subcommand] {
		return "", fmt.Errorf(
			"git: subcommand %q is not in the read-only whitelist. Allowed: %s. For destructive operations use bash (which requires user approval).",
			a.Subcommand, allowedList(),
		)
	}
	if err := validateArgs(a.Args); err != nil {
		return "", fmt.Errorf("git: %w", err)
	}

	limit := a.MaxLines
	if limit <= 0 {
		limit = defaultLineCap
	}
	if limit > hardLineCap {
		// Clamp silently to the hard cap. We don't error out: the
		// LLM's intent is "give me more output", and saturating
		// at 500 lines is closer to that intent than refusing.
		// The truncation marker below tells the model output was
		// cut.
		limit = hardLineCap
	}

	cctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	// -c color.ui=false: belt-and-suspenders against ANSI sneaking
	// in even when our explicit --no-color isn't supported by the
	// subcommand. -c is in the blocked-args list for caller input
	// but we add it ourselves here as the FIRST arg so attempts to
	// override it with a later -c would just fail the validation.
	gitArgs := append([]string{"-c", "color.ui=false", a.Subcommand}, a.Args...)

	cmd := exec.CommandContext(cctx, "git", gitArgs...)
	// stderr captured so a `not a git repository` style error
	// surfaces clearly. Some subcommands (status) write to stderr
	// even on success; we concatenate after stdout so the model
	// sees both in order.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Distinguish "git not installed" from "not a git repo"
		// from generic non-zero exits so the model has a clear
		// signal what to do next.
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("git %s: timed out after %s — narrow the query (e.g. add -n 20)", a.Subcommand, execTimeout)
		}
		out := strings.TrimSpace(stderr.String())
		if out == "" {
			out = strings.TrimSpace(stdout.String())
		}
		// On non-zero exit, return the stderr as a tool error.
		// The model gets a clear message it can pass along or
		// retry with different args.
		return "", fmt.Errorf("git %s: %v: %s", a.Subcommand, err, out)
	}

	body := stdout.String()
	if extra := strings.TrimSpace(stderr.String()); extra != "" {
		// Some git commands (status) write hints to stderr on
		// success. Append them so they don't disappear.
		body = body + "\n--- stderr ---\n" + extra
	}

	out, truncated := capLines(body, limit)
	if truncated {
		out += fmt.Sprintf("\n... (truncated; output exceeded %d lines — use --oneline, -n N, or path filters to narrow the query)", limit)
	}
	return out, nil
}

// validateArgs scans the user-supplied args and rejects anything on
// the blocklist. Match is case-sensitive (git flags are
// case-sensitive). Both `--exec=path` and `--exec path` forms are
// caught: the former matches `--exec=`, the latter matches `--exec`.
func validateArgs(args []string) error {
	for _, a := range args {
		for _, blocked := range blockedArgs {
			// Exact match for short forms (-c, -d, -f) and bare
			// long forms (--exec). Prefix match for value-attached
			// long forms (--exec=, --output=).
			if a == blocked {
				return fmt.Errorf("arg %q is not allowed for read-only git access. For mutating operations use bash", a)
			}
			if strings.HasSuffix(blocked, "=") && strings.HasPrefix(a, blocked) {
				return fmt.Errorf("arg %q is not allowed for read-only git access. For mutating operations use bash", a)
			}
		}
	}
	return nil
}

// capLines returns the first n lines of s and a flag indicating
// whether truncation happened. Uses bufio so we don't materialise
// the whole split slice for huge outputs.
func capLines(s string, n int) (string, bool) {
	if s == "" {
		return "", false
	}
	scanner := bufio.NewScanner(strings.NewReader(s))
	// Default buf is 64 KiB per line, which is too small for the
	// occasional long minified-asset diff line. Bump to 1 MiB so
	// we don't lose a single huge line to a "too long" error.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var b strings.Builder
	count := 0
	for scanner.Scan() {
		if count >= n {
			return b.String(), true
		}
		if count > 0 {
			b.WriteByte('\n')
		}
		b.Write(scanner.Bytes())
		count++
	}
	return b.String(), false
}

// allowedList renders the whitelist for error messages. Sorted so
// the output is byte-stable across builds (prefix-cache friendliness
// when this error surfaces in conversation history).
func allowedList() string {
	keys := make([]string, 0, len(allowedSubcommands))
	for k := range allowedSubcommands {
		keys = append(keys, k)
	}
	// Manual insertion sort — keeps the import surface minimal
	// (the stdlib sort would be one import for one call site).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return strings.Join(keys, ", ")
}
