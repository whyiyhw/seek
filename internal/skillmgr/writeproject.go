// writeproject.go — the project-level direct-write entry for the
// memory→skill bridge (feature-memory-skill-bridge.md §2.3, M-B1).
//
// WriteProjectSkill is the `s` fast lane's landing: a candidate a
// human just reviewed becomes a live project skill in one keystroke.
// Contrast with the drafts area (draft.go), the `y` slow lane: user
// level, sidecar'd, approve-gated. The gates' looseness follows "has
// a human seen the content" (§2.2), not a one-size rule — the
// automatic M-SD2 path keeps NO direct-write ability whatsoever.
//
// What deliberately does NOT happen here (§2.3):
//   - No .install.json — project skills are team-shared via git and
//     carry no local install state (PRD v2 §4.2 precedent, same as
//     Install's project lane).
//   - No cross-layer same-name check — that's the CALLER's contract:
//     the TUI queries skills.Get on the project>user>builtin cascade
//     before offering `s` (lands with M-B2). This entry's own defence
//     is narrower: never overwrite an occupied project slot.
//   - No M write — double-store stays closed.

package skillmgr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/whyiyhw/seek/internal/skill"
)

// WriteProjectOptions controls WriteProjectSkill. ProjectDir defaults
// to cwd (same rule as skill.Load / resolveTargetParent). MD must be a
// complete, already-rendered SKILL.md whose frontmatter name equals
// Name. Now is carried by the approved §2.3 signature but currently
// unread: this path writes no timestamped artifact (no sidecar), so
// there is nothing to stamp.
type WriteProjectOptions struct {
	ProjectDir string
	Name       string // kebab-case; MUST equal MD's frontmatter name
	MD         []byte // complete SKILL.md bytes, rendered by the caller
	Now        func() time.Time
}

// WriteProjectSkill writes <ProjectDir>/.seek/skills/<Name>/SKILL.md
// and returns the skill directory. That file is the sole filesystem
// effect: no sidecar, no stats, no memory. Behaviour chain (§2.3):
// skill.Parse → name consistency → same-name refusal → atomic write —
// sharing resolveTargetParent with Install's commit segment so the
// "where do project skills live" rule has exactly one implementation.
func WriteProjectSkill(opts WriteProjectOptions) (string, error) {
	sk, err := skill.Parse(opts.MD, "project-skill")
	if err != nil {
		return "", fmt.Errorf("skillmgr: write-project: SKILL.md invalid: %w", err)
	}
	if opts.Name == "" {
		return "", errors.New("skillmgr: write-project: Name is required")
	}
	// Name must equal the frontmatter name. Since Parse already
	// enforced the kebab regex on that name, equality also guarantees
	// opts.Name is a single safe path component — the path-escape
	// defence lives here, not in a second regex.
	if opts.Name != sk.Name {
		return "", fmt.Errorf("skillmgr: write-project: Name %q does not match SKILL.md frontmatter name %q", opts.Name, sk.Name)
	}

	targetParent, err := resolveTargetParent(InstallOptions{Project: true, ProjectDir: opts.ProjectDir})
	if err != nil {
		return "", err
	}
	dir := filepath.Join(targetParent, opts.Name)

	// Self-defence: an occupied name slot is refused, never
	// overwritten — same contract as WriteDraft. "Occupied" means the
	// directory exists at all, even as garbage; the fix path is git,
	// not a silent replace.
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("skillmgr: write-project: %q already exists at %s (edit the existing skill or remove it via git; never overwrite)", opts.Name, dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("skillmgr: write-project: stat target: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("skillmgr: write-project: mkdir skill dir: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(dir, "SKILL.md"), opts.MD); err != nil {
		// Best-effort rollback so a failed write doesn't leave a
		// half-occupied name slot: drop the orphan tmp (if the rename
		// died after staging) and the now-empty dir. Cleanup failures
		// don't mask the real error.
		_ = os.Remove(filepath.Join(dir, "SKILL.md.tmp"))
		_ = os.Remove(dir)
		return "", fmt.Errorf("skillmgr: write-project: write SKILL.md: %w", err)
	}
	return dir, nil
}
