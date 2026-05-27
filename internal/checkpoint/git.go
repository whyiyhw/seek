// git.go — git-backed checkpoint layer.
//
// Layered shape:
//
//   - MaybeCreateGit() is the single entry point invoked from
//     permission.Policy.Check. It owns the "one per turn" gate, the
//     git-availability check, and the actual git stash-create +
//     update-ref + jsonl-index dance.
//
//   - listGitCheckpoints + restoreGit + pruneGit back the CLI and
//     TUI surfaces. They live here because they're tightly coupled
//     to the on-disk layout MaybeCreateGit writes; splitting them
//     out into a separate file would mean two files share a private
//     index format.
//
// Wire-format invariant: jsonl line schema is the contract. The CLI
// re-emits it; future parsers (TUI list, cross-session browser in
// v4) read it. New optional fields go AT THE END; never reorder
// existing keys.

package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/permission"
)

// gitRunner is the indirection that lets tests substitute a fake
// `git` binary. Production uses runGitReal. The signature mirrors
// (cwd, args...) → (stdout, stderr, err) so tests can assert on
// the exact invocation.
type gitRunner func(ctx context.Context, cwd string, args ...string) (stdout, stderr string, err error)

func runGitReal(ctx context.Context, cwd string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// GitCheckpoint is one line of <session-dir>/checkpoints.jsonl —
// also the JSON shape returned by `seek checkpoint list --json`.
//
// Wire format. Field renames / removals would silently break the
// CLI consumer + future cross-session UI. Add new fields at the END
// with omitempty.
type GitCheckpoint struct {
	Turn       int       `json:"turn"`
	TS         time.Time `json:"ts"`
	Ref        string    `json:"ref"`
	Commit     string    `json:"commit"`
	HeadBefore string    `json:"head_before"`
	Branch     string    `json:"branch"`
	Label      string    `json:"label"`
	SessionID  string    `json:"session_id,omitempty"`
}

// MaybeCreateGit is the permission-side hook. Called inside
// permission.Policy.Check for every destructive action. Behaviour:
//
//   - If the manager is disabled (nil, no session id, no project
//     path) → no-op.
//   - If the action isn't destructive → no-op.
//   - If a checkpoint has already been written this turn → no-op
//     (the "one per turn" guarantee).
//   - If we're not in a git working tree → log once + no-op.
//   - Otherwise: write the checkpoint, bump turnIdx, flip
//     turnArmed off.
//
// Errors during the write are logged + non-fatal. Returning an
// error to permission.Check would cascade as a permission DENIAL
// to the LLM, which is the wrong UX for a silent safety net.
func (m *Manager) MaybeCreateGit(ctx context.Context, action permission.Action) {
	if m == nil || m.sessionID == "" || m.projectAbs == "" {
		return
	}
	if !isDestructive(action) {
		return
	}

	m.mu.Lock()
	if !m.turnArmed {
		m.mu.Unlock()
		return
	}
	// Check git availability under the same lock so two parallel
	// destructive actions in the same turn (in theory possible if
	// future tools run concurrently) still only produce one
	// checkpoint.
	if !m.gitChecked {
		m.gitAvailable = m.detectGitLocked(ctx)
		m.gitChecked = true
	}
	if !m.gitAvailable {
		if !m.gitHintLogged {
			m.gitHintLogged = true
			m.mu.Unlock()
			m.sinkPlain.Warn("git checkpoint disabled (not a git repo or git binary unavailable); file checkpoint still active")
			return
		}
		m.mu.Unlock()
		return
	}
	turn := m.turnIdx + 1
	m.turnArmed = false
	m.turnIdx = turn
	m.mu.Unlock()

	label := labelForAction(action)
	if err := m.writeGitCheckpoint(ctx, turn, label); err != nil {
		// Roll back turn bump so retrying next turn doesn't skip a
		// number; restore the armed flag so a subsequent destructive
		// action in the SAME turn gets another shot. Best-effort.
		m.mu.Lock()
		m.turnIdx--
		m.turnArmed = true
		m.mu.Unlock()
		m.sinkPlain.Warn(fmt.Sprintf("checkpoint git: %v", err))
		return
	}
	m.emit(CheckpointEvent{Kind: "git", Turn: turn, Label: label})
}

// detectGitLocked checks whether the current cwd is inside a git
// working tree AND the `git` binary actually runs. Called once per
// Manager lifetime under m.mu. Returns false on any error (no git,
// bare repo, permission failure) — all those collapse to the same
// "disable git checkpoint" behaviour from the user's perspective.
func (m *Manager) detectGitLocked(ctx context.Context) bool {
	run := m.runGit
	if run == nil {
		run = runGitReal
	}
	stdout, _, err := run(ctx, m.cwd, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return strings.TrimSpace(stdout) == "true"
}

// writeGitCheckpoint creates the stash commit, writes the ref, and
// appends one line to checkpoints.jsonl.
func (m *Manager) writeGitCheckpoint(ctx context.Context, turn int, label string) error {
	run := m.runGit
	if run == nil {
		run = runGitReal
	}

	// Snapshot HEAD + branch BEFORE creating the stash so the index
	// line reflects the working tree the user came from. `git stash
	// create` does not alter HEAD but we want both pieces of state
	// recorded in case the user has a detached-HEAD checkout.
	headBefore, _, herr := run(ctx, m.cwd, "rev-parse", "HEAD")
	if herr != nil {
		headBefore = ""
	}
	headBefore = strings.TrimSpace(headBefore)

	branchOut, _, berr := run(ctx, m.cwd, "symbolic-ref", "--quiet", "--short", "HEAD")
	branch := ""
	if berr == nil {
		branch = strings.TrimSpace(branchOut)
	}

	// `git stash create` writes the snapshot commit to the object
	// database WITHOUT mutating the stash list / reflog. The
	// resulting SHA goes straight under refs/seek/.
	//
	// We deliberately do NOT pass `-u` / `--include-untracked` —
	// per PRD §3.1 we respect .gitignore (so node_modules / build
	// artefacts don't bloat the checkpoint) and let the user add
	// fresh files via `git add` if they want them in the snapshot.
	// Past experience: --include-untracked on large repos can take
	// 10+ seconds and silently fail without a useful error.
	stashOut, stashErr, err := run(ctx, m.cwd, "stash", "create", fmt.Sprintf("seek checkpoint turn %d: %s", turn, label))
	if err != nil {
		return fmt.Errorf("git stash create: %w: %s", err, strings.TrimSpace(stashErr))
	}
	sha := strings.TrimSpace(stashOut)
	if sha == "" {
		// Clean working tree → `stash create` prints nothing and
		// exits 0. There's nothing to snapshot; skip silently.
		return nil
	}

	ref := fmt.Sprintf("refs/seek/checkpoints/%s/%d", m.sessionID, turn)
	if _, errOut, err := run(ctx, m.cwd, "update-ref", ref, sha); err != nil {
		return fmt.Errorf("git update-ref %s: %w: %s", ref, err, strings.TrimSpace(errOut))
	}

	entry := GitCheckpoint{
		Turn:       turn,
		TS:         time.Now().UTC(),
		Ref:        ref,
		Commit:     sha,
		HeadBefore: headBefore,
		Branch:     branch,
		Label:      label,
		SessionID:  m.sessionID,
	}
	if err := m.appendGitIndex(entry); err != nil {
		return fmt.Errorf("append index: %w", err)
	}
	return nil
}

// appendGitIndex appends one JSON line to <session-dir>/checkpoints.jsonl.
// Atomic per line: an open-append-write-close failure leaves the file
// either intact-from-before or with a complete extra line; never a
// partial line that would break later parsers.
func (m *Manager) appendGitIndex(entry GitCheckpoint) error {
	path, err := m.gitIndexPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := f.Write(b); err != nil {
		return err
	}
	return nil
}

// ListGitCheckpoints reads <session-dir>/checkpoints.jsonl and
// returns the entries in turn order. Missing file → empty slice +
// nil error (no checkpoints yet is a normal state).
//
// Malformed lines are SKIPPED with a Sink.Warn rather than failing
// the whole list — corruption in the safety net should not lock the
// user out of inspecting the rest.
func (m *Manager) ListGitCheckpoints() ([]GitCheckpoint, error) {
	if m == nil {
		return nil, nil
	}
	path, err := m.gitIndexPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var out []GitCheckpoint
	for {
		var entry GitCheckpoint
		if err := dec.Decode(&entry); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			m.sinkPlain.Warn(fmt.Sprintf("checkpoint index: skip malformed line: %v", err))
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

// RestoreOptions modulates RestoreGit behaviour.
type RestoreOptions struct {
	// Turn selects which checkpoint to restore. Zero means "the
	// latest" (the highest-numbered turn in the index).
	Turn int
	// DryRun lists the files that would be touched without
	// modifying the working tree.
	DryRun bool
	// Force skips the "working tree dirty" guard. Without it, a
	// modified-since-last-checkpoint working tree blocks restore.
	Force bool
}

// RestoreResult is returned by RestoreGit. AffectedFiles is the list
// of files the restore would (or did) change.
type RestoreResult struct {
	Checkpoint    GitCheckpoint
	AffectedFiles []string
	DryRun        bool
}

// RestoreGit resets the working tree + index to the named git
// checkpoint. HEAD is NOT moved — the user's commit history stays
// intact. Returns an error if no checkpoint exists, the working
// tree is dirty (without --force), or git fails.
func (m *Manager) RestoreGit(ctx context.Context, opts RestoreOptions) (RestoreResult, error) {
	if m == nil {
		return RestoreResult{}, fmt.Errorf("checkpoint: manager nil")
	}
	list, err := m.ListGitCheckpoints()
	if err != nil {
		return RestoreResult{}, err
	}
	if len(list) == 0 {
		return RestoreResult{}, fmt.Errorf("no git checkpoints for this session")
	}
	turn := opts.Turn
	if turn == 0 {
		turn = list[len(list)-1].Turn
	}
	var target *GitCheckpoint
	for i := range list {
		if list[i].Turn == turn {
			target = &list[i]
			break
		}
	}
	if target == nil {
		return RestoreResult{}, fmt.Errorf("git checkpoint turn %d not found", turn)
	}

	run := m.runGit
	if run == nil {
		run = runGitReal
	}

	// Affected file list: what changes between the working tree
	// HEAD (effectively the current state) and the snapshot commit.
	affected := diffFiles(ctx, run, m.cwd, target.Commit)

	if !opts.Force {
		dirtyOut, _, _ := run(ctx, m.cwd, "status", "--porcelain")
		if strings.TrimSpace(dirtyOut) != "" {
			return RestoreResult{}, fmt.Errorf("working tree has unsaved changes — re-run with --force to overwrite\n%s",
				strings.TrimRight(dirtyOut, "\n"))
		}
	}

	if opts.DryRun {
		return RestoreResult{Checkpoint: *target, AffectedFiles: affected, DryRun: true}, nil
	}

	// read-tree --reset -u <commit> rewrites the working tree +
	// index to match the snapshot. HEAD is untouched. This is the
	// "soft restore" PRD §3.1 describes.
	if _, errOut, err := run(ctx, m.cwd, "read-tree", "--reset", "-u", target.Commit); err != nil {
		return RestoreResult{}, fmt.Errorf("git read-tree: %w: %s", err, strings.TrimSpace(errOut))
	}
	return RestoreResult{Checkpoint: *target, AffectedFiles: affected, DryRun: false}, nil
}

// diffFiles returns the (best-effort) set of files that differ
// between HEAD and the target commit. Used to render "what would
// change" in --dry-run output. Failure → nil; restore still works.
func diffFiles(ctx context.Context, run gitRunner, cwd, commit string) []string {
	out, _, err := run(ctx, cwd, "diff", "--name-only", commit)
	if err != nil {
		return nil
	}
	var files []string
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			files = append(files, ln)
		}
	}
	return files
}

// PruneGit deletes git checkpoint refs older than `before` (and
// rewrites the index to match). Used by `seek checkpoint prune`.
// A zero `before` deletes nothing — callers must explicitly pass a
// time to avoid "oh I ran prune accidentally" disasters.
func (m *Manager) PruneGit(ctx context.Context, before time.Time) (int, error) {
	if m == nil || before.IsZero() {
		return 0, nil
	}
	list, err := m.ListGitCheckpoints()
	if err != nil {
		return 0, err
	}
	run := m.runGit
	if run == nil {
		run = runGitReal
	}

	var kept []GitCheckpoint
	deleted := 0
	for _, e := range list {
		if e.TS.Before(before) {
			if _, errOut, err := run(ctx, m.cwd, "update-ref", "-d", e.Ref); err != nil {
				m.sinkPlain.Warn(fmt.Sprintf("checkpoint prune: %s: %v: %s", e.Ref, err, strings.TrimSpace(errOut)))
				kept = append(kept, e)
				continue
			}
			deleted++
			continue
		}
		kept = append(kept, e)
	}
	if deleted == 0 {
		return 0, nil
	}
	if err := m.rewriteGitIndex(kept); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (m *Manager) rewriteGitIndex(entries []GitCheckpoint) error {
	path, err := m.gitIndexPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// isDestructive reports whether the action is one that should
// trigger a git checkpoint. Read / git / read-only bash are
// out of scope; write / edit / mutating bash are in.
func isDestructive(a permission.Action) bool {
	switch a.Kind {
	case permission.KindWrite, permission.KindEdit:
		return true
	case permission.KindBash:
		// Read-only bash (go vet, go list, etc.) is whitelisted by
		// the bash tool itself via Action.ReadOnly. We honour that
		// flag here so a turn of pure `go vet` doesn't burn a
		// checkpoint slot.
		return !a.ReadOnly
	}
	return false
}

// labelForAction produces the human-readable label stored on the
// checkpoint. ≤ 60 chars per PRD §3.1; used in UI only.
func labelForAction(a permission.Action) string {
	switch a.Kind {
	case permission.KindWrite:
		return shortLabel("write: " + a.Path)
	case permission.KindEdit:
		return shortLabel("edit: " + a.Path)
	case permission.KindBash:
		return shortLabel("bash: " + a.Command)
	}
	return string(a.Kind)
}

func shortLabel(s string) string {
	const max = 60
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}
