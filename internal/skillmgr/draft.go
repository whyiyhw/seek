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
	// Description is the draft SKILL.md's description frontmatter —
	// populated by ListAllDrafts/ReadDraftDescription for L4's
	// merge-matching (a recurring candidate can arrive under a
	// different name than the draft it repeats). Empty when the
	// SKILL.md is unreadable; the listing tolerates that (the row
	// still shows, with "(no sidecar)"-style degradation).
	Description string
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

// ListDrafts returns the drafts INBOX, oldest first (oldest = closest
// to the 14-day GC horizon, so that's the natural review order).
// "weak" drafts (automatic path, recurrence=1 — seen in ONE session
// only) are hidden: they haven't earned a human's attention yet and
// only occupy GC quota (PRD §4 L4). A missing drafts dir is an empty
// inbox, not an error. Directories without a readable sidecar are
// listed with Status "(no sidecar)" — hiding a half-written draft
// would make it undeletable via the inbox.
func (s *DraftStore) ListDrafts() ([]DraftInfo, error) {
	all, err := s.ListAllDrafts()
	if err != nil {
		return nil, err
	}
	var out []DraftInfo
	for _, d := range all {
		if d.Status == DraftStatusWeak {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// ListAllDrafts is ListDrafts without the inbox filter — every draft
// including "weak" ones, with Description parsed from SKILL.md. Used
// by GC (weak drafts still age and count against the cap) and by L4
// merge-matching (a recurring weak draft is exactly the one to
// strengthen, so it must be findable).
func (s *DraftStore) ListAllDrafts() ([]DraftInfo, error) {
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
		if md, err := os.ReadFile(filepath.Join(draftsDir, e.Name(), "SKILL.md")); err == nil {
			if sk, err := skill.Parse(md, "draft:"+e.Name()); err == nil {
				info.Description = sk.Description
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

// StrengthenDraft is L4's merge write (PRD §4): an independent session
// re-extracted a procedure that already has a draft. Recurrence++,
// source appended, UpdatedAt refreshed. The SKILL.md is deliberately
// NOT touched — first write wins for the body; recurrence is
// provenance, not content.
//
// Promotions that ride along:
//   - recurrence ≥ 2 → status "reviewable" (the draft surfaces in the
//     ListDrafts inbox — two independent sessions are the evidence a
//     pattern is not one session's special case).
//   - sources spanning ≥ 2 distinct project_ids → suggested_scope
//     upgrades project → user (cross-project recurrence is the "user
//     work habit" signal, PRD §5.4).
//
// extraTrace appends to filter_trace so each strengthening round's
// funnel verdict stays visible to the human reviewer.
func (s *DraftStore) StrengthenDraft(name string, src DraftSource, extraEvidence []string, extraTrace []FilterTraceEntry) (*DraftSidecar, error) {
	if !draftNameRE.MatchString(name) {
		return nil, fmt.Errorf("skillmgr: invalid draft name %q", name)
	}
	draftsDir, err := s.DraftsDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(draftsDir, name)
	sidecarPath := filepath.Join(dir, SidecarName)

	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		return nil, fmt.Errorf("skillmgr: strengthen %q: read sidecar: %w", name, err)
	}
	var side DraftSidecar
	if err := json.Unmarshal(data, &side); err != nil {
		return nil, fmt.Errorf("skillmgr: strengthen %q: parse sidecar: %w", name, err)
	}

	side.Recurrence++
	side.Sources = append(side.Sources, src)
	if len(extraEvidence) > 0 {
		side.Evidence = append(side.Evidence, extraEvidence...)
	}
	side.FilterTrace = append(side.FilterTrace, extraTrace...)
	if side.Recurrence >= 2 {
		side.Status = DraftStatusReviewable
	}
	if countDistinctProjects(side.Sources) >= 2 {
		side.SuggestedScope = "user"
	}
	side.UpdatedAt = s.now().UTC().Format(time.RFC3339)

	out, err := json.MarshalIndent(side, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("skillmgr: marshal sidecar: %w", err)
	}
	if err := atomicWriteFile(sidecarPath, append(out, '\n')); err != nil {
		return nil, fmt.Errorf("skillmgr: write %s: %w", SidecarName, err)
	}
	return &side, nil
}

func countDistinctProjects(sources []DraftSource) int {
	seen := map[string]struct{}{}
	for _, s := range sources {
		if s.ProjectID != "" {
			seen[s.ProjectID] = struct{}{}
		}
	}
	return len(seen)
}

// GC policy (PRD §4 草稿区 GC / §8 #11). Age is measured from
// UpdatedAt — a draft that keeps being strengthened is active, not
// stale, even if it hasn't reached the inbox yet; eviction order under
// the cap is by CreatedAt ("最旧先清"). Both defaults are PRD-fixed.
const (
	DraftTTL  = 14 * 24 * time.Hour
	MaxDrafts = 10
)

// GCDrafts enforces the drafts-area garbage policy and returns the
// removed draft names (for a one-line notification):
//
//  1. drafts whose UpdatedAt is older than ttl (14 days) are removed;
//  2. if more than max (10) remain, the oldest-by-CreatedAt are
//     removed until the cap holds.
//
// "Unreviewed" is every draft in the area — weak AND reviewable —
// since installing removes the human's copy from the area anyway (the
// install flow leaves cleanup to `drafts rm`; a leftover installed
// draft being GC'd is the documented, harmless path).
func (s *DraftStore) GCDrafts(now time.Time, ttl time.Duration, max int) ([]string, error) {
	all, err := s.ListAllDrafts()
	if err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = DraftTTL
	}
	if max <= 0 {
		max = MaxDrafts
	}

	var removed []string
	// Pass 1: age (by UpdatedAt, CreatedAt as fallback for
	// sidecar-less drafts). Undatable drafts are kept — better to hold
	// garbage one more cycle than delete something unreadable.
	var survivors []DraftInfo
	for _, d := range all {
		ts := s.readUpdatedAt(d.Name)
		if ts.IsZero() {
			if c, err := time.Parse(time.RFC3339, d.CreatedAt); err == nil {
				ts = c
			}
		}
		if !ts.IsZero() && now.Sub(ts) > ttl {
			if _, err := s.DeleteDraft(d.Name); err == nil {
				removed = append(removed, d.Name)
			}
			continue
		}
		survivors = append(survivors, d)
	}
	// Pass 2: cap. Survivors are still oldest-CreatedAt-first
	// (ListAllDrafts order), so evict from the front.
	for len(survivors) > max {
		if _, err := s.DeleteDraft(survivors[0].Name); err == nil {
			removed = append(removed, survivors[0].Name)
		}
		survivors = survivors[1:]
	}
	return removed, nil
}

// readUpdatedAt pulls just the UpdatedAt field off a draft's sidecar.
// Zero time on any failure — callers treat that as "use CreatedAt".
func (s *DraftStore) readUpdatedAt(name string) time.Time {
	draftsDir, err := s.DraftsDir()
	if err != nil {
		return time.Time{}
	}
	data, err := os.ReadFile(filepath.Join(draftsDir, name, SidecarName))
	if err != nil {
		return time.Time{}
	}
	var side DraftSidecar
	if json.Unmarshal(data, &side) != nil {
		return time.Time{}
	}
	if ts, err := time.Parse(time.RFC3339, side.UpdatedAt); err == nil {
		return ts
	}
	return time.Time{}
}

// atomicWriteFile stages data to <path>.tmp then renames over path —
// same dance as memory.atomicWrite; duplicated (unexported) because
// exporting memory's helper would couple the two subsystems for one
// 10-line function.
func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *DraftStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
