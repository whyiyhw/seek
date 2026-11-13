// auto.go — the automatic path of skill distillation
// (feature-skill-distill M-SD2): a SessionStart hook that batch-
// distills the current project's recent sessions in a background
// goroutine, through the same funnel the manual /distill path uses,
// and lands DRAFTS — never installs.
//
// Shape is deliberately line-by-line parallel to memory's
// maybeAutoDream / runAutoDream (internal/memory/hook.go): SessionStart
// cadence check → reset state BEFORE spawning (a crash mid-batch
// doesn't loop) → fire-and-forget goroutine → silently-swallowed
// errors. The differences from dream are the point of this file:
//
//   - default OFF ($SEEK_AUTO_SKILL_DISTILL; dream is default-on) —
//     the safety net here is only "drafts never install themselves",
//     weaker than dream's live notifications, so dogfooding is opt-in
//     (PRD §3.1 D);
//   - per-project state (dream's is global because dreaming is
//     cross-project; distillation batches are per-project);
//   - IRON RULE, pinned by tests: this path has no install capability.
//     Its only writes under ~/.seek/skills/ go through
//     skillmgr.DraftStore (drafts area) and skillstats (telemetry
//     rows) — the AGENTS.md exclusive-write rule holds. The one direct
//     write is the per-project cadence state file under
//     ~/.seek/projects/<pid>/, same class as memory's dream-state.json.
package skilldistill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/internal/skill"
	"github.com/whyiyhw/seek/internal/skillmgr"
	"github.com/whyiyhw/seek/internal/skillstats"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ---- cadence state (mirrors memory.DreamState, per-project) ----

// AutoState is the persisted cadence tracker for the auto path, one
// file per project at ~/.seek/projects/<pid>/skill-distill-state.json.
// SessionsSinceDistill increments on every (enabled) SessionStart;
// LastDistillAt is the watermark of the last fired batch — sessions
// updated AFTER it are the next batch's input.
type AutoState struct {
	SchemaVersion        int       `json:"schema_version"`
	LastDistillAt        time.Time `json:"last_distill_at,omitzero"`
	SessionsSinceDistill int       `json:"sessions_since_distill"`
}

const (
	autoStateFile          = "skill-distill-state.json"
	autoStateSchemaVersion = 1

	// Cadence (PRD §3.1 D): fire when either trips.
	autoEverySessions = 8
	autoEveryDays     = 7

	// MaxSessionsPerBatch bounds a first run on an old install (zero
	// watermark = every session the project ever had). Newest-first so
	// the cap drops the stalest material, not the freshest.
	MaxSessionsPerBatch = 50

	// batchTimeout bounds the whole background pass; per-call timeouts
	// (extract 90s / filter 60s) match the manual path's round-trips.
	batchTimeout       = 10 * time.Minute
	extractCallTimeout = 90 * time.Second
	filterCallTimeout  = 60 * time.Second
)

// EnvAutoSkillDistill is the master switch. Default OFF (unset or any
// non-truthy value): unlike auto-dream there is no live notification
// safety net, only "drafts can't install themselves" — opt-in until
// dogfooding says otherwise (PRD §3.1 D).
const EnvAutoSkillDistill = "SEEK_AUTO_SKILL_DISTILL"

// EnvSkillDistillModel switches the auto path's reasoner off the
// Flash default (e.g. to a Pro model) — unattended batches, low
// cadence, human-attention-bounded output: quality-first parameters
// (PRD §7 model selection).
const EnvSkillDistillModel = "SEEK_SKILL_DISTILL_MODEL"

// AutoEnabled reports whether the automatic path is switched on.
func AutoEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvAutoSkillDistill)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// autoDue evaluates the cadence rule. The days trigger only arms once
// LastDistillAt is set — a fresh install must not fire on day 1 (same
// first-run semantics as DreamState.IsDreamDue).
func autoDue(s *AutoState, now time.Time) bool {
	if s.SessionsSinceDistill >= autoEverySessions {
		return true
	}
	return !s.LastDistillAt.IsZero() &&
		now.Sub(s.LastDistillAt) >= autoEveryDays*24*time.Hour
}

func loadAutoState(projectDir string) (*AutoState, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, autoStateFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &AutoState{SchemaVersion: autoStateSchemaVersion}, nil
		}
		return nil, err
	}
	var s AutoState
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt state shouldn't break startup — reset, like DreamState.
		return &AutoState{SchemaVersion: autoStateSchemaVersion}, nil
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = autoStateSchemaVersion
	}
	return &s, nil
}

func saveAutoState(projectDir string, s *AutoState) error {
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return err
	}
	s.SchemaVersion = autoStateSchemaVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(projectDir, autoStateFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(projectDir, autoStateFile))
}

// ---- session access (injectable for tests) ----

// SessionRef is one project session the batch may distill.
type SessionRef struct {
	ID        string
	UpdatedAt time.Time
}

// SessionReader is the read-only session surface the batch needs.
// An interface so end-to-end tests drive fake histories without
// touching disk, while production uses SessionStore over the real
// *session.Store.
type SessionReader interface {
	// ProjectSessions returns the project's root sessions (forks
	// excluded), oldest first.
	ProjectSessions(projectID string) ([]SessionRef, error)
	// Messages loads one session's transcript.
	Messages(sessionID string) ([]deepseek.Message, error)
}

// SessionStore adapts the real session store. ListProject skips
// forked sessions on purpose: a /branch or /compact child shares
// history with its parent, and L4's "N≥2 INDEPENDENT sessions"
// recurrence invariant must not count one conversation twice.
type SessionStore struct{ Store *session.Store }

func (s SessionStore) ProjectSessions(projectID string) ([]SessionRef, error) {
	infos, err := s.Store.ListProject(projectID)
	if err != nil {
		return nil, err
	}
	refs := make([]SessionRef, 0, len(infos))
	for _, i := range infos {
		refs = append(refs, SessionRef{ID: i.ID, UpdatedAt: i.UpdatedAt})
	}
	return refs, nil
}

func (s SessionStore) Messages(sessionID string) ([]deepseek.Message, error) {
	sess, err := s.Store.Load(sessionID)
	if err != nil {
		return nil, err
	}
	return sess.Messages, nil
}

// ---- the hook ----

// AutoHook implements hooks.SessionStartObserver. Zero-value fields
// disable it: OnSessionStart no-ops unless Distiller, Drafts,
// Sessions and ProjectID are all set and the env switch is on.
//
// Concurrency: one batch per process at a time (running flag under
// mu). A SessionStart arriving while a batch runs only increments the
// counter — correct, because the running batch already advanced the
// watermark past every pre-batch session, so this start is the first
// session of the NEXT cycle. Two concurrent PROCESSES both pass the
// cadence check only in the tiny window between state load and save;
// worst case is a duplicated extraction whose L4 merge then
// strengthens the same draft (noise, never an install) — same
// accepted posture as auto-dream.
type AutoHook struct {
	Distiller  *Distiller
	Skills     *skill.Set // may be nil (no installed skills → empty manifest, L2 passes all)
	MIndex     []MEntry   // current project's M index snapshot (L1/L2 dedup context)
	Drafts     *skillmgr.DraftStore
	Stats      *skillstats.Writer // may be nil (telemetry off)
	Sessions   SessionReader
	ProjectID  string
	ProjectDir string // state file lives here (must be the project's ~/.seek/projects/<pid>/)
	Now        func() time.Time

	// autoDone is closed when the background batch finishes. Tests
	// use it to wait; production leaves it nil (fire-and-forget).
	autoDone chan struct{}

	mu      sync.Mutex
	running bool
}

// OnSessionStart runs the cadence check (maybeAutoDistill's shape from
// memory/hook.go): increment, check due, reset-and-spawn. Never blocks
// on the batch itself — the user's first Prompt is moments away.
func (h *AutoHook) OnSessionStart(ctx context.Context, _ hooks.SessionStartEvent) {
	if !AutoEnabled() {
		return
	}
	if h.Distiller == nil || h.Drafts == nil || h.Sessions == nil || h.ProjectID == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	state, err := loadAutoState(h.ProjectDir)
	if err != nil {
		return // unreadable state: skip this start, next one retries
	}
	state.SessionsSinceDistill++
	if !autoDue(state, h.now()) || h.running {
		_ = saveAutoState(h.ProjectDir, state)
		return
	}

	// Reset BEFORE spawning: a crash mid-batch must not re-trigger on
	// every subsequent start (dream's lesson, memory/hook.go).
	prev := state.LastDistillAt
	state.SessionsSinceDistill = 0
	state.LastDistillAt = h.now()
	if err := saveAutoState(h.ProjectDir, state); err != nil {
		return
	}

	h.running = true
	done := h.autoDone
	go func() {
		// Defer order matters: running must clear BEFORE done closes,
		// so a caller that waits on done and immediately re-fires sees
		// a free hook (close(done) runs last — defers are LIFO).
		if done != nil {
			defer close(done)
		}
		defer func() {
			h.mu.Lock()
			h.running = false
			h.mu.Unlock()
		}()
		h.runBatch(ctx, prev, state.LastDistillAt)
	}()
}

// batchItem is one L0-surviving session queued for extraction.
type batchItem struct {
	ref      SessionRef
	messages []deepseek.Message
	boosted  bool
}

// runBatch walks the funnel over the project's unprocessed sessions:
// window-select → L0 gate → (L1+evidence+L2 in Extract) → L3 filter →
// L4 merge into drafts → GC. Every failure is per-session: one broken
// transcript never sinks the batch. Errors are swallowed — this is a
// best-effort background pass (runAutoDream's posture).
func (h *AutoHook) runBatch(ctx context.Context, prev, watermark time.Time) {
	bctx, cancel := context.WithTimeout(ctx, batchTimeout)
	defer cancel()

	// GC runs even when the funnel yields nothing — "14 天未审草稿在
	// 下次批处理时清除" rides on the batch, not on success.
	defer func() {
		_, _ = h.Drafts.GCDrafts(h.now(), 0, 0)
	}()

	refs, err := h.Sessions.ProjectSessions(h.ProjectID)
	if err != nil || len(refs) == 0 {
		return
	}

	// Window: prev < UpdatedAt ≤ watermark, newest-first, capped.
	var window []SessionRef
	for i := len(refs) - 1; i >= 0; i-- {
		r := refs[i]
		if r.UpdatedAt.After(watermark) {
			continue
		}
		if !prev.IsZero() && !r.UpdatedAt.After(prev) {
			break // refs are oldest-first; everything earlier is out of window
		}
		window = append(window, r)
		if len(window) >= MaxSessionsPerBatch {
			break
		}
	}

	// Phase 1: load + L0 (zero API). Boosted sessions (Pitfall:
	// trailer) jump the extraction queue — pitfall-fixing sessions are
	// where "hit坑 → work around" procedures concentrate (PRD §4 L0).
	var queue []batchItem
	for _, r := range window {
		msgs, err := h.Sessions.Messages(r.ID)
		if err != nil {
			continue
		}
		gate := MechanicalGate(msgs)
		if !gate.Pass {
			continue
		}
		queue = append(queue, batchItem{ref: r, messages: msgs, boosted: gate.Boost})
	}
	stableSortQueue(queue)

	manifest := ""
	if h.Skills != nil {
		manifest = h.Skills.Manifest()
	}

	// Phase 2: extract → L3 → L4 per session, chronologically within
	// priority groups so recurrence accumulation watches the pattern
	// emerge in order.
	for _, item := range queue {
		kept, err := h.extractOne(bctx, item, manifest)
		if err != nil {
			continue // malformed reasoner output etc: silent log-and-skip (PRD §8 #12)
		}
		for _, cand := range kept {
			h.distillCandidate(bctx, cand, item.ref, manifest)
		}
	}
}

// extractOne is the per-session reasoner call (L1 prompt-internal
// dedup + evidence verification + L2 mechanical dedup all live inside
// Distiller.Extract — same funnel as the manual path).
func (h *AutoHook) extractOne(ctx context.Context, item batchItem, manifest string) ([]SkillCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, extractCallTimeout)
	defer cancel()
	kept, _, err := h.Distiller.Extract(ctx, item.messages, manifest, h.MIndex, h.Skills)
	return kept, err
}

// distillCandidate runs L3 (mandatory on the auto path — no human in
// the loop until review; PRD §4 L3) then L4-merges the survivor into
// the drafts area and writes a telemetry row.
func (h *AutoHook) distillCandidate(ctx context.Context, cand SkillCandidate, ref SessionRef, manifest string) {
	ctx, cancel := context.WithTimeout(ctx, filterCallTimeout)
	defer cancel()

	greyBodies := h.greyBodies(cand)
	res, _, err := h.Distiller.Filter(ctx, cand, manifest, h.MIndex, greyBodies)
	if err != nil || res != FilterAccept {
		// A filter the auto path can't run is a filter that didn't
		// pass: reject conservatively ("宁可漏过，不可放过"). The
		// watermark has already moved, so this candidate is gone —
		// acceptable for an unattended, human-gated pipeline.
		return
	}

	trace := []skillmgr.FilterTraceEntry{{
		Stage:   "L3",
		Verdict: "ACCEPT",
	}}
	if cand.L2Note != "" {
		trace = append(trace, skillmgr.FilterTraceEntry{
			Stage:   "L2",
			Verdict: "GREY",
			Reason:  cand.L2Note,
		})
	}

	src := skillmgr.DraftSource{
		SessionID: ref.ID,
		ProjectID: h.ProjectID,
		At:        h.now().UTC().Format(time.RFC3339),
	}
	if err := h.mergeDraft(cand, src, trace); err != nil {
		return
	}
	h.stat(cand.Name, ref.ID)
}

// greyBodies pulls the bodies of the skills L2 flagged as borderline,
// so L3 can compare procedures rather than descriptions (PRD §4
// L2→L3 handoff).
func (h *AutoHook) greyBodies(cand SkillCandidate) []string {
	if h.Skills == nil {
		return nil
	}
	var bodies []string
	for _, name := range cand.L2GreyName {
		if sk := h.Skills.Get(name); sk != nil {
			bodies = append(bodies, sk.Body)
		}
	}
	return bodies
}

// mergeDraft is L4: match the candidate against existing drafts by
// name or description similarity → StrengthenDraft; no match → a new
// WEAK draft (recurrence=1, invisible in the inbox until a second
// independent session confirms it). Never installs, never overwrites
// a draft's SKILL.md.
func (h *AutoHook) mergeDraft(cand SkillCandidate, src skillmgr.DraftSource, trace []skillmgr.FilterTraceEntry) error {
	all, err := h.Drafts.ListAllDrafts()
	if err != nil {
		return err
	}
	match := ""
	for _, d := range all {
		if d.Name == cand.Name {
			match = d.Name
			break
		}
		if trigramJaccard(cand.Description, d.Description) >= SimilarityReject {
			match = d.Name
			break
		}
	}
	if match != "" {
		_, err := h.Drafts.StrengthenDraft(match, src, cand.Evidence, trace)
		return err
	}

	md, err := RenderSKILLMD(cand)
	if err != nil {
		return err
	}
	side := skillmgr.DraftSidecar{
		Status:         skillmgr.DraftStatusWeak,
		SuggestedScope: "project",
		Sources:        []skillmgr.DraftSource{src},
		Evidence:       cand.Evidence,
		FilterTrace:    trace,
		Generator: &skillmgr.DraftGenerator{
			Model:     h.Distiller.model(),
			PromptRev: PromptRev,
		},
	}
	if _, err := h.Drafts.WriteDraft(md, side); err != nil {
		// Cross-run race: another process wrote the same draft between
		// our list and write. Strengthen theirs — never clobber.
		if strings.Contains(err.Error(), "already exists") {
			_, err = h.Drafts.StrengthenDraft(cand.Name, src, cand.Evidence, trace)
		}
		return err
	}
	return nil
}

// stat appends one provenance row to .stats.jsonl (best-effort):
// provider=distill-auto rows are skipped by call-count aggregation.
func (h *AutoHook) stat(name, sessionID string) {
	if h.Stats == nil {
		return
	}
	_ = h.Stats.Append(skillstats.Entry{
		TS:        h.now().UTC().Format(time.RFC3339),
		Name:      name,
		SessionID: sessionID,
		ProjectID: h.ProjectID,
		Model:     h.Distiller.model(),
		Provider:  skillstats.ProviderDistillAuto,
	})
}

func (h *AutoHook) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

// stableSortQueue orders boosted sessions first, preserving
// chronological order within each group (window was newest-first, so
// the pre-sort order inside groups is newest-first too — kept as-is:
// within a priority group the newest material is the most relevant).
func stableSortQueue(q []batchItem) {
	for i := 1; i < len(q); i++ {
		for j := i; j > 0 && q[j].boosted && !q[j-1].boosted; j-- {
			q[j], q[j-1] = q[j-1], q[j]
		}
	}
}

// ensure interface satisfaction at compile time.
var _ hooks.SessionStartObserver = (*AutoHook)(nil)
