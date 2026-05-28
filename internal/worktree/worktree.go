// Package worktree manages seek-owned git worktrees for v5 柱 G
// subagent isolation (PRD docs/prd/feature-subagent.md §3.8).
//
// Each worktree lives at ~/.seek/projects/<pid>/worktrees/<wt-id>/
// on the filesystem and is anchored by a git ref under
// refs/seek/worktrees/<wt-id>. The refs/seek/ namespace is shared
// with v3 checkpoint — it's intentionally out of the user's
// default refspec so `git push` doesn't surface these refs, and
// git's gc.reflogExpire eventually reaps them.
//
// Dirty-discard safety net: when exit_worktree is called with
// if_dirty="discard", the current contents are stashed to
// refs/seek/discarded/<ts> BEFORE hard-resetting, giving the user
// a 48-hour window to recover an accidental discard. Manager.
// PruneDiscarded handles GC (no daemon — called from seek startup
// and the `seek worktree gc` CLI per PRD §3.8 "zero常驻进程"
// constraint).
//
// The package does NOT couple to internal/subagent. The agent
// orchestration sits in cmd/seek / internal/tools/agent and calls
// Manager.Create / Cleanup directly with whatever ID it wants for
// the worktree (sub_sid for the agent path; auto-generated for
// model-driven enter_worktree calls). This separation keeps the
// worktree primitive useful even when there's no subagent in
// play.
package worktree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
)

// Worktree is the public record of one managed worktree. Returned
// from Create + Status + lookups; everything callers need to refer
// back to the worktree (Path for cwd-pinning, Branch for git
// operations, SubSid for cross-reference in subagents.jsonl).
type Worktree struct {
	// ID is the seek-internal identifier (timestamp + 6 hex chars,
	// same shape as session IDs). Used for the on-disk dir name
	// AND the git ref namespace, so the two stay aligned.
	ID string
	// Path is the absolute path to the worktree's root directory.
	// Callers pinning a child agent's cwd / tool execution
	// directory hand this to the consumer.
	Path string
	// Branch is the git branch name the worktree is checked out
	// at. Auto-generated as "seek/wt/<id>" when the Create
	// caller didn't supply one — the seek/ prefix keeps these
	// branches out of the user's default tab-completion.
	Branch string
	// Base is the commit SHA the worktree was branched off (the
	// HEAD of the project at Create time). Stored so the user can
	// reason about "what state was this branched from" when
	// browsing /worktrees later.
	Base string
}

// CleanupResult is the public outcome of Manager.Cleanup. Status
// is the wire-format-friendly result word ("cleaned" / "kept" /
// "discarded"); the rest is detail the calling tool surfaces.
type CleanupResult struct {
	Status   string // "cleaned" | "kept" | "discarded"
	Changes  int    // dirty file count at cleanup time
	Path     string // worktree path
	Branch   string // worktree branch
	StashRef string // populated when Status == "discarded" and Changes > 0
}

// Manager owns the per-project worktree state. Single instance per
// seek invocation, constructed by cmd/seek at startup.
//
// Concurrency: the active map is mu-protected; git command calls
// happen WITHOUT the lock (they're slow and don't touch the map's
// invariants). Concurrent Create from parallel tool dispatch is
// safe — each call generates a fresh ID + path so they don't
// collide on filesystem state.
type Manager struct {
	mu     sync.Mutex
	root   string            // abs project working tree path
	runGit GitRunner         // injectable for tests
	active map[string]Worktree // ID → Worktree

	// homeProjectDir caches the resolved ~/.seek/projects/<pid>/
	// directory so we don't re-resolve paths on every Create. Set
	// once at construction; immutable thereafter.
	homeProjectDir string
}

// NewManager constructs a Manager. projectRoot must be the
// absolute path to the user's project working tree (the git repo
// root); ENOENT or non-git inputs are NOT detected here — the
// failure surfaces when Create runs git from that cwd. Failing
// late lets callers construct a Manager even in non-git
// directories so the tool registration path doesn't need to
// branch.
func NewManager(projectRoot string) (*Manager, error) {
	return NewManagerWithRunner(projectRoot, runGitReal)
}

// NewManagerWithRunner is the test seam — wires a caller-supplied
// GitRunner instead of the real `git` binary. Production code uses
// NewManager which calls this internally with runGitReal.
//
// Exported only so tests in OTHER packages (e.g. internal/tools/
// enterworktree) can mock git invocations. Calling this from
// production paths defeats every concurrency / process-isolation
// guarantee runGitReal provides; resist the temptation.
func NewManagerWithRunner(projectRoot string, run GitRunner) (*Manager, error) {
	if run == nil {
		return nil, fmt.Errorf("worktree: NewManagerWithRunner: nil GitRunner")
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve project root: %w", err)
	}
	dir, err := paths.ProjectDir(abs)
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve project dir: %w", err)
	}
	return &Manager{
		root:           abs,
		runGit:         run,
		active:         make(map[string]Worktree),
		homeProjectDir: dir,
	}, nil
}

// newID returns a fresh worktree ID. Same shape as subagent
// IDs (timestamp + 6 hex chars) so they sort naturally and visual
// inspection of ~/.seek/projects/<pid>/worktrees/ shows chronology.
// Falling back to nanosecond suffix on entropy exhaustion mirrors
// internal/session.generateID.
func newID(now time.Time) string {
	var rnd [3]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("%s-%06x",
			now.Format("20060102-150405"),
			now.Nanosecond()/1000)
	}
	return fmt.Sprintf("%s-%s",
		now.Format("20060102-150405"),
		hex.EncodeToString(rnd[:]))
}

// Create makes a new worktree branched off `base` (default HEAD).
// `branch` is the branch name; empty string → auto-generate
// "seek/wt/<id>" under the seek/ prefix so it stays out of the
// user's default branch listings.
//
// Steps:
//
//  1. Generate a fresh ID + resolve the on-disk path.
//  2. Resolve base SHA (rev-parse base — default HEAD).
//  3. `git worktree add -b <branch> <path> <base>` creates the
//     branch + checks it out at path.
//  4. Update-ref refs/seek/worktrees/<id> → base SHA, so seek's
//     namespace owns a pin even if the user later git-branch -D's
//     the worktree's branch.
//  5. Register in m.active under mu.
//
// Errors are surfaced verbatim from git stderr so the calling tool
// can return a useful message to the model. Failure paths NEVER
// leave a half-created worktree in m.active.
func (m *Manager) Create(ctx context.Context, branch, base string) (Worktree, error) {
	if m == nil {
		return Worktree{}, errors.New("worktree: nil Manager")
	}
	now := time.Now().UTC()
	id := newID(now)
	worktreePath := filepath.Join(m.homeProjectDir, "worktrees", id)

	if base == "" {
		base = "HEAD"
	}
	baseSha, errOut, err := m.runGit(ctx, m.root, "rev-parse", base)
	if err != nil {
		return Worktree{}, fmt.Errorf("worktree: resolve base %q: %v: %s", base, err, strings.TrimSpace(errOut))
	}
	baseSha = strings.TrimSpace(baseSha)

	if branch == "" {
		branch = "seek/wt/" + id
	}

	// Ensure parent dir exists. `git worktree add` creates the
	// leaf, but it expects the parents to be there.
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return Worktree{}, fmt.Errorf("worktree: mkdir parent: %w", err)
	}

	_, addErr, err := m.runGit(ctx, m.root,
		"worktree", "add", "-b", branch, worktreePath, baseSha)
	if err != nil {
		return Worktree{}, fmt.Errorf("worktree: git worktree add: %v: %s", err, strings.TrimSpace(addErr))
	}

	// Pin via refs/seek/worktrees/<id> — same namespace
	// convention as v3 checkpoint, kept out of the default refspec
	// so push doesn't surface it. Failure here is non-fatal: the
	// worktree is usable without the pin; we just log + degrade.
	ref := "refs/seek/worktrees/" + id
	if _, refErr, rerr := m.runGit(ctx, m.root, "update-ref", ref, baseSha); rerr != nil {
		// Best-effort; surface as part of the path's stderr but
		// don't undo the worktree creation. The caller can still
		// use the worktree; user can clean up the ref manually.
		_ = refErr
	}

	wt := Worktree{
		ID:     id,
		Path:   worktreePath,
		Branch: branch,
		Base:   baseSha,
	}
	m.mu.Lock()
	m.active[id] = wt
	m.mu.Unlock()
	return wt, nil
}

// Status reports the dirty file count at the worktree path. 0
// means clean (no `git status --porcelain` output). Used by
// Cleanup to decide between cleaned/kept/discarded paths and by
// /worktrees panel rendering.
func (m *Manager) Status(ctx context.Context, path string) (int, error) {
	if m == nil {
		return 0, errors.New("worktree: nil Manager")
	}
	out, errOut, err := m.runGit(ctx, path, "status", "--porcelain")
	if err != nil {
		return 0, fmt.Errorf("worktree: git status: %v: %s", err, strings.TrimSpace(errOut))
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return 0, nil
	}
	return strings.Count(out, "\n") + 1, nil
}

// Cleanup removes (or keeps) a worktree per ifDirty. Three result
// shapes per PRD §3.8:
//
//   - "cleaned": no dirty files → git worktree remove + ref delete.
//   - "kept" (ifDirty=keep, default): worktree left in place;
//     caller surfaces path+branch so user can manually finish.
//   - "discarded" (ifDirty=discard): dirty contents stashed to
//     refs/seek/discarded/<ts> (NOT in default refspec; GC'd by
//     PruneDiscarded), then hard-reset + git worktree remove.
//
// Cleanup ALWAYS removes the worktree from m.active before
// returning successfully, even on the "kept" path — keeping it
// in active would mean a re-cleanup attempt could hit a stale
// path. The user holds the worktree via filesystem state, not
// Manager state.
func (m *Manager) Cleanup(ctx context.Context, path string, ifDirty string) (CleanupResult, error) {
	if m == nil {
		return CleanupResult{}, errors.New("worktree: nil Manager")
	}
	if ifDirty == "" {
		ifDirty = "keep"
	}
	if ifDirty != "keep" && ifDirty != "discard" {
		return CleanupResult{}, fmt.Errorf("worktree: ifDirty must be keep|discard, got %q", ifDirty)
	}

	// Look up the worktree by path. We track active worktrees by
	// ID, but tools call us with Path — scan the map.
	m.mu.Lock()
	var found Worktree
	var foundID string
	for id, w := range m.active {
		if w.Path == path {
			found, foundID = w, id
			break
		}
	}
	m.mu.Unlock()
	// Not finding the worktree in active is NOT fatal — it might
	// be one the user created manually or restored from a prior
	// seek run that crashed mid-cleanup. Proceed with best-effort
	// cleanup using just the path.
	if foundID == "" {
		found = Worktree{Path: path}
	}

	changes, err := m.Status(ctx, path)
	if err != nil {
		return CleanupResult{}, err
	}

	if changes == 0 {
		// Clean removal path.
		if err := m.removeWorktree(ctx, found); err != nil {
			return CleanupResult{}, err
		}
		m.unregister(foundID)
		return CleanupResult{Status: "cleaned", Path: path, Branch: found.Branch}, nil
	}

	if ifDirty == "keep" {
		// Leave the worktree in place; just deregister so the
		// caller knows seek's bookkeeping has moved on. The user
		// can now manually `git worktree remove` or finish their
		// work in the directory.
		m.unregister(foundID)
		return CleanupResult{
			Status:  "kept",
			Changes: changes,
			Path:    path,
			Branch:  found.Branch,
		}, nil
	}

	// discard path: stash to refs/seek/discarded/<ts> first
	// (rescue net per PRD §3.8 48h GC), then hard-reset + remove.
	stashRef, err := m.stashForDiscard(ctx, path)
	if err != nil {
		// Refuse to discard if we can't make a rescue copy first
		// — surfacing the error gives the user a chance to
		// recover before nuking their work.
		return CleanupResult{}, fmt.Errorf("worktree: rescue stash failed: %w", err)
	}
	if _, resetErr, err := m.runGit(ctx, path, "reset", "--hard"); err != nil {
		return CleanupResult{}, fmt.Errorf("worktree: git reset --hard: %v: %s", err, strings.TrimSpace(resetErr))
	}
	// Also clean untracked files so the subsequent worktree
	// remove sees a pristine tree.
	if _, cleanErr, err := m.runGit(ctx, path, "clean", "-fd"); err != nil {
		return CleanupResult{}, fmt.Errorf("worktree: git clean: %v: %s", err, strings.TrimSpace(cleanErr))
	}
	if err := m.removeWorktree(ctx, found); err != nil {
		return CleanupResult{}, err
	}
	m.unregister(foundID)
	return CleanupResult{
		Status:   "discarded",
		Changes:  changes,
		Path:     path,
		Branch:   found.Branch,
		StashRef: stashRef,
	}, nil
}

// stashForDiscard creates a tree-snapshot commit of the dirty
// worktree and pins it under refs/seek/discarded/<ts>. Returns
// the ref so CleanupResult can echo it to the user. Uses `git
// stash create` (no stash-list mutation, no working-tree change)
// — same primitive v3 checkpoint uses.
func (m *Manager) stashForDiscard(ctx context.Context, path string) (string, error) {
	out, errOut, err := m.runGit(ctx, path, "stash", "create",
		"seek discarded worktree "+time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("git stash create: %v: %s", err, strings.TrimSpace(errOut))
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		// Clean tree — but Status said dirty? Race or stale
		// state; nothing to stash, no rescue ref needed.
		return "", nil
	}
	ref := "refs/seek/discarded/" + time.Now().UTC().Format("20060102-150405")
	if _, refErr, rerr := m.runGit(ctx, m.root, "update-ref", ref, sha); rerr != nil {
		return "", fmt.Errorf("update-ref %s: %v: %s", ref, rerr, strings.TrimSpace(refErr))
	}
	return ref, nil
}

// removeWorktree calls `git worktree remove --force` then deletes
// the seek-owned ref. --force is used because the worktree might
// still contain build artefacts the user hasn't tracked; for
// seek-owned worktrees we always want to nuke. The user manually
// keeps things by passing if_dirty=keep BEFORE we reach this
// point.
func (m *Manager) removeWorktree(ctx context.Context, wt Worktree) error {
	_, rmErr, err := m.runGit(ctx, m.root, "worktree", "remove", "--force", wt.Path)
	if err != nil {
		return fmt.Errorf("worktree: git worktree remove: %v: %s", err, strings.TrimSpace(rmErr))
	}
	if wt.ID != "" {
		ref := "refs/seek/worktrees/" + wt.ID
		// Best-effort: log + continue. Leftover ref doesn't break
		// anything; git gc eventually reaps it.
		_, _, _ = m.runGit(ctx, m.root, "update-ref", "-d", ref)
	}
	return nil
}

// unregister removes the worktree from the active map. Safe to
// call with an empty id (no-op).
func (m *Manager) unregister(id string) {
	if id == "" {
		return
	}
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

// List returns a snapshot of currently-active worktrees. Order
// is insertion order is NOT guaranteed — callers that care sort
// by Path / ID themselves.
func (m *Manager) List() []Worktree {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Worktree, 0, len(m.active))
	for _, w := range m.active {
		out = append(out, w)
	}
	return out
}

// MapToProject rewrites an absolute path under a registered
// worktree to the project-root-equivalent path. Used by v5 §5.3
// hooks path-matching: a hook rule "deny writes to docs/prd/**"
// should fire when a subagent in a worktree writes to
// <worktree>/docs/prd/foo, not just <project>/docs/prd/foo.
//
// Returns the input unchanged when path doesn't fall under any
// active worktree. Lookup is by path-prefix match — if you have
// two nested worktrees (shouldn't happen, but defensive) the
// longest prefix wins.
func (m *Manager) MapToProject(path string) string {
	if m == nil || path == "" {
		return path
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var bestPrefix string
	for _, w := range m.active {
		if isPrefix(path, w.Path) && len(w.Path) > len(bestPrefix) {
			bestPrefix = w.Path
		}
	}
	if bestPrefix == "" {
		return path
	}
	rel, err := filepath.Rel(bestPrefix, path)
	if err != nil {
		return path
	}
	return filepath.Join(m.root, rel)
}

// isPrefix reports whether path falls under root (including the
// case path == root). Filesystem-aware via filepath.Clean so
// trailing slashes / "." segments don't trip up the comparison.
func isPrefix(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// PruneDiscarded removes refs/seek/discarded/<ts> refs older than
// olderThan. Called from seek startup (best-effort, errors
// logged) and the `seek worktree gc` CLI. Returns the count
// removed.
//
// The 48h default (PRD §3.8) is the caller's choice — we accept
// any duration so the CLI can force-prune everything via
// olderThan=0.
func (m *Manager) PruneDiscarded(ctx context.Context, olderThan time.Duration) (int, error) {
	if m == nil {
		return 0, errors.New("worktree: nil Manager")
	}
	// Enumerate via for-each-ref. Format gives us "<ref> <ts>"
	// per line where <ts> is the ref's tagged timestamp; but
	// `update-ref` doesn't set a reflog-friendly time. Instead
	// we parse the ref NAME (which encodes UTC YYYYMMDD-HHMMSS).
	out, errOut, err := m.runGit(ctx, m.root, "for-each-ref", "--format=%(refname)", "refs/seek/discarded/")
	if err != nil {
		return 0, fmt.Errorf("worktree: for-each-ref discarded: %v: %s", err, strings.TrimSpace(errOut))
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	pruned := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		ts, ok := parseDiscardedRefTS(line)
		if !ok {
			// Unknown shape — skip rather than nuke something we
			// don't understand.
			continue
		}
		if ts.After(cutoff) {
			continue
		}
		if _, refErr, err := m.runGit(ctx, m.root, "update-ref", "-d", line); err != nil {
			// Best-effort: skip the one that failed, log via
			// stderr-bound caller, continue.
			_ = refErr
			continue
		}
		pruned++
	}
	return pruned, nil
}

// parseDiscardedRefTS extracts the YYYYMMDD-HHMMSS suffix from a
// refs/seek/discarded/<ts> ref name and parses it as UTC time.
// Returns ok=false for any ref that doesn't match the expected
// shape (defensive: future schema changes or manual ref
// pollution shouldn't crash GC).
func parseDiscardedRefTS(ref string) (time.Time, bool) {
	const prefix = "refs/seek/discarded/"
	if !strings.HasPrefix(ref, prefix) {
		return time.Time{}, false
	}
	suffix := strings.TrimPrefix(ref, prefix)
	ts, err := time.Parse("20060102-150405", suffix)
	if err != nil {
		return time.Time{}, false
	}
	return ts.UTC(), true
}
