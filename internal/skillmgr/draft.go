// draft.go — the drafts area of the skills tree (feature-skill-distill
// §5.1): ~/.seek/skills/.drafts/<name>/{SKILL.md,.distill.json}.
//
// Everything in this file honours the AGENTS.md constraint that only
// skillmgr and skillstats write under ~/.seek/skills/: the drafts area
// is skillmgr's to create and delete, exclusively. The loader never
// sees it — loadFromDir skips dot-prefixed entries on purpose, which
// is the load-bearing wall this whole human-review-gate design leans
// on (a regression test pins it in draft_test.go).
//
// What deliberately does NOT live here (first delivery, PRD §11):
//   - StrengthenDraft / recurrence merging — automatic path (M-SD2).
//   - GCDrafts — M-SD3.
//   - InstallDraft — install is a human action via the EXISTING
//     `seek skill install <draft-dir>` command; the distillation
//     pipeline itself never installs. After installing, the draft
//     copy stays in .drafts/ until removed with `seek skill drafts rm`
//     — a leftover draft is harmless (invisible, listable), an
//     auto-installed skill is not.

package skillmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/skill"
)

// DraftsDirName is the dot-directory inside the user skills tree that
// holds skill drafts. The leading dot is what keeps the loader blind:
// loadFromDir skips dot entries by design, so drafts can never leak
// into a manifest. If that skip ever changes, the regression tests in
// draft_test.go fail first.
const DraftsDirName = ".drafts"

// SidecarName is the seek-private provenance file inside a draft
// directory — same design philosophy as .install.json: the file the
// human might edit (SKILL.md) stays separate from the state seek
// writes (.distill.json).
const SidecarName = ".distill.json"

// Draft status values. The manual /distill path writes "reviewable"
// drafts directly (the user triggered extraction and reviews inline —
// the user IS the recurrence evidence, PRD §4 L4). "weak" is reserved
// for the automatic path's single-occurrence drafts (M-SD2); it exists
// in the schema now so ListDrafts can filter it without a format bump
// later.
const (
	DraftStatusReviewable = "reviewable"
	DraftStatusWeak       = "weak"
)

// DraftSource records one session that contributed to a draft. The
// drafts area is global (not per-project); project attribution lives
// here, in the sidecar, not in the directory layout (PRD §5.1).
type DraftSource struct {
	SessionID string `json:"session_id"`
	ProjectID string `json:"project_id"`
	At        string `json:"at"` // RFC3339
}

// FilterTraceEntry records one funnel stage's verdict for a candidate,
// so a human reviewing a draft can see why it almost died in L2/L3
// (and threshold tuning has data to work with).
type FilterTraceEntry struct {
	Stage   string `json:"stage"`   // "L2" | "L3" | "human"
	Verdict string `json:"verdict"` // PASS | GREY | REJECT | ACCEPT
	Reason  string `json:"reason,omitempty"`
}

// DraftGenerator records what produced the draft — model plus prompt
// revision — so a draft's provenance is answerable months later.
type DraftGenerator struct {
	Model     string `json:"model,omitempty"`
	PromptRev string `json:"prompt_rev,omitempty"`
}

// DraftSidecar is the .distill.json schema (PRD §5.2, schema_version 1).
type DraftSidecar struct {
	SchemaVersion  int                `json:"schema_version"`
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      string             `json:"updated_at"`
	Status         string             `json:"status"`
	Recurrence     int                `json:"recurrence"`
	SuggestedScope string             `json:"suggested_scope"` // "project" | "user" (§5.4: suggestion only)
	Sources        []DraftSource      `json:"sources"`
	Evidence       []string           `json:"evidence,omitempty"`
	FilterTrace    []FilterTraceEntry `json:"filter_trace,omitempty"`
	Generator      *DraftGenerator    `json:"generator,omitempty"`
}

// DraftInfo is one row of the drafts inbox listing.
type DraftInfo struct {
	Name           string
	Status         string
	Recurrence     int
	SuggestedScope string
	CreatedAt      string
	Dir            string
}

// DraftStore owns the drafts directory under a user skills tree.
// Zero-value-ish: UserDir empty = paths.UserSkills(); Now nil =
// time.Now. Tests inject both.
type DraftStore struct {
	UserDir string
	Now     func() time.Time
}

// DraftsDir returns the drafts root. Created lazily by WriteDraft;
// ListDrafts tolerates it missing (empty inbox is the common case).
func (s *DraftStore) DraftsDir() (string, error) {
	base, err := resolveUserDir(s.UserDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, DraftsDirName), nil
}

// draftNameRE guards DeleteDraft against path escape — same shape as
// skill's nameRE, duplicated because skill doesn't export it and one
// import for a regexp used once at a trust boundary isn't worth
// widening skill's API for.
var draftNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// WriteDraft lays down .drafts/<name>/SKILL.md + .distill.json.
//
// The SKILL.md bytes MUST already parse (skill.Parse runs here as the
// gate — "a draft is a legal package" is the contract, PRD §5.3).
// A draft of the same name must not already exist: silently
// overwriting a draft someone is mid-review on is exactly the class
// of accident the drafts area exists to prevent.
func (s *DraftStore) WriteDraft(md []byte, side DraftSidecar) (string, error) {
	sk, err := skill.Parse(md, "draft")
	if err != nil {
		return "", fmt.Errorf("skillmgr: draft: SKILL.md invalid: %w", err)
	}

	draftsDir, err := s.DraftsDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(draftsDir, sk.Name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("skillmgr: draft %q already exists at %s (remove it first: seek skill drafts rm %s)", sk.Name, dir, sk.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("skillmgr: stat draft dir: %w", err)
	}

	if side.SchemaVersion == 0 {
		side.SchemaVersion = 1
	}
	if side.Status == "" {
		side.Status = DraftStatusReviewable
	}
	if side.Recurrence == 0 {
		side.Recurrence = 1
	}
	if side.SuggestedScope == "" {
		side.SuggestedScope = "project"
	}
	now := s.now().UTC().Format(time.RFC3339)
	if side.CreatedAt == "" {
		side.CreatedAt = now
	}
	side.UpdatedAt = now

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("skillmgr: mkdir draft dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), md, 0o644); err != nil {
		return "", fmt.Errorf("skillmgr: write draft SKILL.md: %w", err)
	}
	sidecarBytes, err := json.MarshalIndent(side, "", "  ")
	if err != nil {
		return "", fmt.Errorf("skillmgr: marshal sidecar: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SidecarName), append(sidecarBytes, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("skillmgr: write %s: %w", SidecarName, err)
	}
	return dir, nil
}

// ListDrafts returns the drafts inbox, oldest first (oldest = closest
// to the 14-day GC horizon, so that's the natural review order).
// A missing drafts dir is an empty inbox, not an error. Directories
// without a readable sidecar are listed with Status "(no sidecar)" —
// hiding a half-written draft would make it undeletable via the inbox.
func (s *DraftStore) ListDrafts() ([]DraftInfo, error) {
	draftsDir, err := s.DraftsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(draftsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("skillmgr: read drafts dir: %w", err)
	}

	var out []DraftInfo
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info := DraftInfo{
			Name:           e.Name(),
			Status:         "(no sidecar)",
			SuggestedScope: "project",
			Dir:            filepath.Join(draftsDir, e.Name()),
		}
		if data, err := os.ReadFile(filepath.Join(draftsDir, e.Name(), SidecarName)); err == nil {
			var side DraftSidecar
			if err := json.Unmarshal(data, &side); err == nil {
				info.Status = side.Status
				info.Recurrence = side.Recurrence
				info.SuggestedScope = side.SuggestedScope
				info.CreatedAt = side.CreatedAt
			}
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		// Oldest created first; ties and sidecar-less entries fall
		// back to name so the order is deterministic.
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// DeleteDraft removes one draft directory. The name is validated
// against the kebab regex BEFORE touching the filesystem — RemoveAll
// on a caller-supplied string is a path-escape waiting to happen.
func (s *DraftStore) DeleteDraft(name string) (string, error) {
	if !draftNameRE.MatchString(name) {
		return "", fmt.Errorf("skillmgr: invalid draft name %q", name)
	}
	draftsDir, err := s.DraftsDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(draftsDir, name)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("skillmgr: draft %q not found in %s", name, draftsDir)
		}
		return "", fmt.Errorf("skillmgr: stat draft: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("skillmgr: remove draft: %w", err)
	}
	return dir, nil
}

func (s *DraftStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
