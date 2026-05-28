package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ObserveResult is the outcome of one async memory_observe filter pass,
// sent from the background goroutine to the TUI via ResultChan.
type ObserveResult struct {
	Name    string
	Tagline string
	OK      bool   // true = written to M (ACCEPT), false = rejected/failed
	Err     string // failure reason (empty on success)
}

// observeStats records per-session memory_observe outcomes for the M5.13
// feedback loop. Written to <projectDir>/observe-stats.json at SessionEnd
// and injected into the next session's M-index snapshot.
type observeStats struct {
	Launched int `json:"launched"` // goroutines started (observeCount)
	Saved    int `json:"saved"`    // successfully written to M
}

const observeStatsFile = "observe-stats.json"

// entryInfo is the intermediate representation for M-index rendering:
// one non-stale entry with its tags for group-by-tag layout.
type entryInfo struct {
	name        string
	tagline     string
	tags        []string
	pinned      bool
	recallCount int
	autoSourced bool
}

// Hook is the memory subsystem's manifestation on the agent's
// lifecycle. It implements:
//
//   - PrePromptHook — injects the L stable section + the M index as
//     user-role <context> blocks before each Prompt's user message.
//   - SessionStartObserver — runs Project.RunGC once at session start
//     so the M index that PrePrompt sees has freshly-applied stale
//     flags. GC failure is non-fatal (logged at debug level later;
//     for now silently tolerated — a session without GC is degraded
//     but functional).
//   - PostToolUseObserver — captures memory_observe calls for async
//     filter dedup and session cap tracking (the tool itself launches
//     the goroutine, but the hook tracks metadata).
//
// Both Project and Soul may be nil: callers (cmd/seek/main.go) load
// what they can and pass nils for the rest. A Hook with both nil is a
// no-op — Register it anyway so the registration order doesn't drift
// based on which fields loaded successfully.
type Hook struct {
	Project *Project
	Soul    *Soul

	// Distiller drives the async observe filter (V4-Flash thinking).
	// nil = filtering unavailable (memory_observe still returns empty but
	// no goroutine is launched — no writes happen).
	Distiller *Distiller

	// Dreamer, when non-nil and $SEEK_AUTO_DREAM=1, drives the M5.8
	// auto-dream SessionStart path: cadence in DreamState is checked
	// (every N sessions OR K days, default 20 / 14); if due, a dream
	// pass runs in a background goroutine and any L candidates that
	// pass the ≥2-source filter get appended to Soul.Pending.
	//
	// Off by default — same gating philosophy as auto-distill.
	Dreamer *Dreamer

	// HistoryProvider returns the session history at SessionEnd.
	// nil disables auto-distill regardless of Distiller — we can't
	// extract decisions from a session we don't have access to.
	// Injected (not captured at hook construction) because the
	// agent owns the messages slice and we want a fresh snapshot
	// at end-of-session, not a stale capture.
	HistoryProvider func() []deepseek.Message

	// Now overrides timestamps for testing. Production callers leave it
	// zero and the hook uses time.Now().UTC().
	Now func() time.Time

	// autoDreamRan is set by tests that need to wait for the
	// auto-dream goroutine. Production callers ignore it (the
	// channel is nil and the goroutine is fire-and-forget).
	autoDreamDone chan struct{}

	// ResultChan delivers observe filter results from background
	// goroutines to the TUI event loop. Buffered (capacity 20) so
	// fast filter passes don't block the goroutine. The TUI selects
	// on this channel in its main loop and renders scrollback
	// notifications.
	ResultChan chan ObserveResult

	// observeLocks guards concurrent memory_observe calls for the same
	// name. The tool's Execute calls TryLock before launching the
	// goroutine; if another goroutine is already in-flight for the same
	// name, the second call is silently merged (no new goroutine).
	observeLocks observeLockMap

	// observeCount tracks how many filter goroutines have been launched
	// this session. Capped at observeMax (default 10). Exceeding the
	// cap causes silent discard (no goroutine, no error).
	observeCount atomic.Int32
	observeMax   int // configured via $SEEK_OBSERVE_MAX, default 10

	// observeAcceptCt tracks how many memory_observe calls resulted in
	// a successful write to M this session. Used with observeCount to
	// compute aggregate stats for the M5.13 feedback loop.
	observeAcceptCt atomic.Int32

	// snapshotInjected is set after the first OnPrePrompt call delivers
	// the session-start L+M snapshot. Subsequent calls skip snapshot
	// injection and only emit a delta if new entries were added.
	snapshotInjected bool

	// snapshotEntryNames records the set of non-stale M entry names at
	// snapshot time. Used by buildDelta to detect new entries added
	// mid-session (memory_remember / memory_observe / /distill).
	snapshotEntryNames map[string]struct{}
}

// observeLockMap is a simple per-name mutex for concurrent dedup.
type observeLockMap struct {
	mu    sync.Mutex
	locks map[string]struct{}
}

func (m *observeLockMap) tryLock(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = make(map[string]struct{})
	}
	if _, ok := m.locks[name]; ok {
		return false // already locked
	}
	m.locks[name] = struct{}{}
	return true
}

func (m *observeLockMap) unlock(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, name)
}

// defaultObserveMax is the per-session cap on filter goroutines.
const defaultObserveMax = 10

// envObserveMax is the env-var name for configuring observe max.
const envObserveMax = "SEEK_OBSERVE_MAX"

// OnPrePrompt implements the session-start snapshot + mid-session delta
// strategy (PRD v1 §5 M5.9):
//
//   - Turn 1: inject the full L stable section + M index as user-role
//     <context> blocks (the session-start snapshot). Record the set of
//     entry names for delta detection.
//   - Turns 2+: if the M has new entries since snapshot time, inject a
//     <context type="memory.delta"> block listing only the additions.
//     Otherwise emit nothing — the snapshot is already in history.
//
// This ensures the byte prefix (system prompt + turn-1 snapshot) never
// changes within a session, maximising prefix-cache hit rate.
func (h *Hook) OnPrePrompt(_ context.Context, _ hooks.PrePromptIn) (hooks.PrePromptOut, error) {
	if !h.snapshotInjected {
		return h.injectSnapshot()
	}

	var prepend []deepseek.Message
	if h.Project != nil {
		if msg := h.buildDelta(); msg != nil {
			prepend = append(prepend, *msg)
		}
	}
	return hooks.PrePromptOut{Prepend: prepend}, nil
}

// injectSnapshot builds the session-start snapshot: L stable section +
// M index. Records snapshot entry names for later delta detection.
// Called once per session — on the first OnPrePrompt call.
func (h *Hook) injectSnapshot() (hooks.PrePromptOut, error) {
	var prepend []deepseek.Message

	if h.Soul != nil && strings.TrimSpace(h.Soul.Stable) != "" {
		prepend = append(prepend, deepseek.Message{
			Role:    deepseek.RoleUser,
			Content: wrapContext("memory.soul", truncateSoulStable(h.Soul.Stable)),
		})
	}

	if h.Project != nil {
		msg, names := h.buildMIndex()
		if msg != nil {
			// M5.13: append observe stats from previous session.
			if stats := readObserveStats(h.Project.Dir); stats != nil && stats.Launched > 0 {
				msg.Content = msg.Content + fmt.Sprintf(
					"\n\n最近 memory_observe 统计：%d 次调用，%d 条保存",
					stats.Launched, stats.Saved,
				)
			}
			prepend = append(prepend, *msg)
		}
		h.snapshotEntryNames = names
	}

	h.snapshotInjected = true
	return hooks.PrePromptOut{Prepend: prepend}, nil
}

// buildDelta returns a <context type="memory.delta"> message listing
// entries added since the session-start snapshot. Returns nil when
// there are no new non-stale entries.
//
// The delta is appended at the end of the conversation (before the
// current user message) — it does not modify the prefix bytes, so the
// prefix cache is unaffected.
//
// Entries are sorted by name for byte stability across calls with the
// same set of additions.
func (h *Hook) buildDelta() *deepseek.Message {
	if h.snapshotEntryNames == nil {
		return nil
	}

	var newEntries []Entry
	for _, e := range h.Project.Entries() {
		if e.Stale {
			continue
		}
		if _, known := h.snapshotEntryNames[e.Name]; !known {
			newEntries = append(newEntries, e)
		}
	}
	if len(newEntries) == 0 {
		return nil
	}

	sort.Slice(newEntries, func(i, j int) bool {
		return newEntries[i].Name < newEntries[j].Name
	})

	var sb strings.Builder
	sb.WriteString("New entries added since session start:\n")
	for _, e := range newEntries {
		if e.AutoSourced {
			fmt.Fprintf(&sb, "- [auto] %s: %s\n", e.Name, e.Tagline)
		} else {
			fmt.Fprintf(&sb, "- %s: %s\n", e.Name, e.Tagline)
		}
	}
	return &deepseek.Message{
		Role:    deepseek.RoleUser,
		Content: wrapContext("memory.delta", strings.TrimRight(sb.String(), "\n")),
	}
}

// buildMIndex builds the M-index <context> block with tag-grouped layout
// and token-budget truncation. When the index exceeds maxMIndexTokens,
// low-score entries are dropped first.
// Returns nil when there are no non-stale entries.
//
// Grouping: entries are grouped by their first tag value. Untagged entries
// go under a "general" section. Groups are sorted alphabetically with
// "general" last. Within each group, entries are sorted by name.
//
// Byte-stability (fast path — under budget): given identical L+M disk state
// + same env, the output is byte-identical across prompts — sort order is
// deterministic (alphabetical by name) and estimateTokens is pure.
//
// Byte-stability (slow path — over budget): the truncation sort uses a
// TIME-INDEPENDENT key (pinned desc → recall_count desc → name asc) so
// the byte output changes ONLY when entry metadata changes (add/remove/
// recall/touch/GC), NOT on every prompt. This is a deliberate trade —
// prefix-cache stability outweighs the marginal ranking improvement from
// time-dependent Score().
func (h *Hook) buildMIndex() (*deepseek.Message, map[string]struct{}) {
	// Collect non-stale entries with tags.
	all := h.Project.Entries()
	entries := make([]entryInfo, 0, len(all))
	for _, e := range all {
		if e.Stale {
			continue
		}
		entries = append(entries, entryInfo{
			name:        e.Name,
			tagline:     e.Tagline,
			tags:        e.Tags,
			pinned:      e.Pinned,
			recallCount: e.RecallCount,
			autoSourced: e.AutoSourced,
		})
	}
	if len(entries) == 0 {
		return nil, nil
	}

	body := buildGroupedIndexString(entries)
	if estimateTokens(body) <= maxMIndexTokens {
		names := nameSetFrom(entries)
		return &deepseek.Message{
			Role:    deepseek.RoleUser,
			Content: wrapContext("memory.index", body),
		}, names
	}

	// Over budget — rebuild with time-independent sort key.
	// Sort order: pinned desc → recall_count desc → name asc.
	// This keeps the byte output stable across prompts (see byte-stability
	// doc on buildMIndex). The order correlates well with Score() —
	// pinned entries always win, then frequently-recalled entries, then
	// alphabetical — without depending on time.Now().

	// Sort by static key: pinned desc → recall_count desc → name asc.
	// This is TIME-INDEPENDENT so the byte output is stable across prompts
	// (see byte-stability doc on buildMIndex).
	sort.SliceStable(entries, func(i, j int) bool {
		// Pinned entries always first.
		if entries[i].pinned != entries[j].pinned {
			return entries[i].pinned
		}
		// Then by recall count descending.
		if entries[i].recallCount != entries[j].recallCount {
			return entries[i].recallCount > entries[j].recallCount
		}
		// Alphabetical tiebreak.
		return entries[i].name < entries[j].name
	})

	// Greedily take entries until we hit budget.
	var within []entryInfo
	for _, e := range entries {
		candidate := entryInfo{
			name:        e.name,
			tagline:     e.tagline,
			tags:        e.tags,
			pinned:      e.pinned,
			recallCount: e.recallCount,
			autoSourced: e.autoSourced,
		}
		// Build a fresh slice to avoid aliasing within's backing array.
		testEntries := make([]entryInfo, len(within)+1)
		copy(testEntries, within)
		testEntries[len(within)] = candidate
		testBody := buildGroupedIndexString(testEntries)
		if estimateTokens(testBody) <= maxMIndexTokens {
			within = append(within, candidate)
		} else {
			break
		}
	}

	body = buildGroupedIndexString(within)
	if len(within) < len(entries) {
		truncated := len(entries) - len(within)
		note := fmt.Sprintf("\n... and %d more (truncated to fit token budget)", truncated)
		if estimateTokens(note) <= maxMIndexTokens-estimateTokens(body) {
			body += note
		}
	}

	names := nameSetFrom(within)
	return &deepseek.Message{
		Role:    deepseek.RoleUser,
		Content: wrapContext("memory.index", body),
	}, names
}

// buildGroupedIndexString renders entries grouped by their first tag.
// Entries are placed in exactly one group — the first element of their
// Tags slice determines the group. Multi-tag entries appear only under
// their primary (first) tag to avoid consuming budget on duplicates.
// Untagged entries go under "### general". Groups are alphabetically
// sorted; within each group entries are sorted by name.
func buildGroupedIndexString(entries []entryInfo) string {
	type namedEntry struct {
		name        string
		tagline     string
		autoSourced bool
	}

	groups := make(map[string][]namedEntry) // tag → entries
	var general []namedEntry

	for _, e := range entries {
		ne := namedEntry{name: e.name, tagline: e.tagline, autoSourced: e.autoSourced}
		if len(e.tags) > 0 && e.tags[0] != "" {
			groups[e.tags[0]] = append(groups[e.tags[0]], ne)
		} else {
			general = append(general, ne)
		}
	}

	// Collect and sort tag names.
	tagNames := make([]string, 0, len(groups))
	for tag := range groups {
		tagNames = append(tagNames, tag)
	}
	sort.Strings(tagNames)

	var sb strings.Builder
	sb.WriteString("Project memory index — call memory_recall(name) to fetch full content for any entry below.\n\n")

	for _, tag := range tagNames {
		entries := groups[tag]
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].name < entries[j].name
		})
		fmt.Fprintf(&sb, "### %s\n", tag)
		for _, e := range entries {
			if e.autoSourced {
				fmt.Fprintf(&sb, "- [auto] %s: %s\n", e.name, e.tagline)
			} else {
				fmt.Fprintf(&sb, "- %s: %s\n", e.name, e.tagline)
			}
		}
		sb.WriteByte('\n')
	}

	if len(general) > 0 {
		sort.Slice(general, func(i, j int) bool {
			return general[i].name < general[j].name
		})
		fmt.Fprintf(&sb, "### general\n")
		for _, e := range general {
			if e.autoSourced {
				fmt.Fprintf(&sb, "- [auto] %s: %s\n", e.name, e.tagline)
			} else {
				fmt.Fprintf(&sb, "- %s: %s\n", e.name, e.tagline)
			}
		}
		sb.WriteByte('\n')
	}

	return strings.TrimRight(sb.String(), "\n")
}

// nameSetFrom extracts the name set from a slice of entryInfo. Used by
// buildMIndex to return the set of entries actually included in the
// snapshot (which may be fewer than Project.Entries() when the token
// budget forces truncation).
func nameSetFrom(entries []entryInfo) map[string]struct{} {
	out := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		out[e.name] = struct{}{}
	}
	return out
}

// OnSessionStart runs a GC pass once per session. Stale flips happen
// before the first Prompt sees the M index, so the very first user
// turn benefits from up-to-date filtering. Failure here is intentionally
// swallowed — a failed GC degrades the index (might show stale entries
// or omit recently-recalled ones for one session) but should not block
// the user from talking to the model.
//
// Also fires M5.8 auto-dream cadence check: if $SEEK_AUTO_DREAM is on
// and the cadence (every N sessions / K days) has tripped, launches a
// background dream pass. SessionStart MUST NOT block — the user's
// first Prompt is moments away — so the dream runs in a goroutine,
// writes its L-pending update if successful, and is otherwise
// fire-and-forget.
func (h *Hook) OnSessionStart(ctx context.Context, _ hooks.SessionStartEvent) {
	// Reset snapshot state so the first OnPrePrompt call rebuilds the
	// snapshot from fresh GC'd data. This handles both fresh sessions
	// and --resume (where the Hook is reused across agent lifetimes).
	h.snapshotInjected = false
	h.snapshotEntryNames = nil

	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now()
	}
	if h.Project != nil {
		_, _ = h.Project.RunGC(now)
	}
	h.maybeAutoDream(ctx, now)
}

// maybeAutoDream checks cadence and, if due, spawns the dream
// goroutine. Cadence state (SessionsSinceDream++) is incremented
// every SessionStart regardless of whether dream actually fires —
// that's how the next session's check knows where we stand.
func (h *Hook) maybeAutoDream(ctx context.Context, now time.Time) {
	if !autoDreamEnabled() {
		return
	}
	if h.Dreamer == nil {
		return
	}

	state, err := LoadDreamState()
	if err != nil {
		return
	}
	state.SessionsSinceDream++
	due := state.IsDreamDue(now)
	if !due.Due {
		_ = state.Save()
		return
	}

	// Reset the counter NOW (before spawning the goroutine) so a
	// crash mid-dream doesn't re-trigger on every subsequent start.
	// The dream's actual completion bumps LastDreamAt — partial work
	// loses its candidate write but doesn't loop.
	state.SessionsSinceDream = 0
	state.LastDreamAt = now
	_ = state.Save()

	done := h.autoDreamDone
	go func() {
		if done != nil {
			defer close(done)
		}
		h.runAutoDream(ctx)
	}()
}

// runAutoDream gathers cross-project + recent-session input, runs the
// reasoner, and appends any candidates to Soul.Pending. Errors silently
// swallowed — auto-dream is best-effort enhancement.
// Skips the reasoner call entirely when there are <2 projects with
// entries, since the N≥2 filter would reject everything anyway.
func (h *Hook) runAutoDream(ctx context.Context) {
	projects, err := ListProjects()
	if err != nil || len(projects) < 2 {
		return
	}
	in := DreamInput{}
	for _, p := range projects {
		entries := p.Entries()
		if len(entries) == 0 {
			continue
		}
		in.Projects = append(in.Projects, DreamProject{ID: p.ID, Entries: entries})
	}
	if len(in.Projects) == 0 {
		return
	}

	rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cands, err := h.Dreamer.Dream(rctx, in)
	if err != nil || len(cands) == 0 {
		return
	}

	soul, err := LoadSoul()
	if err != nil {
		return
	}
	pending := MergeIntoL(soul.Pending, cands)
	// M5.10: evaluate and maintain.
	promoted, kept := EvaluatePending(pending, time.Now())
	_ = soul.ApplyMaintenance(promoted, kept)
}

// OnSessionEnd is a no-op in v2. The v1 auto-distill path (heuristic
// satisfaction detection + reasoner call) has been removed. Memory writes
// now happen in real-time via memory_observe during the conversation.
func (h *Hook) OnSessionEnd(_ context.Context, _ hooks.SessionEndEvent) {
	// M5.13: persist observe stats for the next session's feedback loop.
	h.writeObserveStats()
}

// writeObserveStats writes observe-stats.json to the project directory
// if there were any observe calls this session. Best-effort: errors are
// silently swallowed (stats are a nice-to-have, not correctness-critical).
func (h *Hook) writeObserveStats() {
	if h.Project == nil || h.observeCount.Load() == 0 {
		return
	}
	data, err := json.Marshal(observeStats{
		Launched: int(h.observeCount.Load()),
		Saved:    int(h.observeAcceptCt.Load()),
	})
	if err != nil {
		return
	}
	_ = atomicWrite(filepath.Join(h.Project.Dir, observeStatsFile), data)
}

// readObserveStats reads the previous session's observe-stats.json.
// Missing / corrupt file returns nil.
func readObserveStats(dir string) *observeStats {
	data, err := os.ReadFile(filepath.Join(dir, observeStatsFile))
	if err != nil {
		return nil
	}
	var s observeStats
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	return &s
}

// ObserveEnqueue returns the function that the memory_observe tool calls to
// start the async filter. It handles per-name dedup, session cap, and
// launches the background goroutine. The returned function is non-blocking
// (the goroutine does all the work).
//
// Called by memory_observe.Execute after argument validation.
func (h *Hook) ObserveEnqueue() func(context.Context, Entry) {
	if h.Project == nil || h.Distiller == nil {
		return nil
	}
	return func(ctx context.Context, entry Entry) {
		// Per-name dedup: if a goroutine for this name is already
		// in-flight, silently merge (no duplicate filter calls).
		if !h.observeLocks.tryLock(entry.Name) {
			return
		}

		// Session cap: check observeMax, resolve from env on first call.
		if h.observeMax == 0 {
			h.observeMax = defaultObserveMax
			if v := os.Getenv(envObserveMax); v != "" {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					h.observeMax = n
				}
			}
		}
		if int(h.observeCount.Add(1)) > h.observeMax {
			h.observeLocks.unlock(entry.Name)
			return
		}

		// Launch filter goroutine.
		go func() {
			defer h.observeLocks.unlock(entry.Name)

			// Non-blocking send: if the TUI has exited and nobody is
			// reading ResultChan, we drop the notification rather than
			// blocking the goroutine indefinitely. The channel send is
			// fire-and-forget — the goroutine must not stall on it.
			send := func(r ObserveResult) {
				if h.ResultChan == nil {
					return
				}
				select {
				case h.ResultChan <- r:
				default:
					// TUI exited or channel full; drop silently.
				}
			}

			// 10-second timeout for the filter call.
			fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			// Collect existing entries for context.
			existing := h.Project.Entries()

			// Check coverage rule first: if an entry with this name
			// exists and is already confirmed, reject immediately.
			if existingEntry, ok := h.Project.Get(entry.Name); ok && !existingEntry.AutoSourced {
				send(ObserveResult{
					Name:    entry.Name,
					Tagline: entry.Tagline,
					OK:      false,
					Err:     "entry '" + entry.Name + "' already confirmed — use memory_remember to update",
				})
				return
			}

			// Run V4-Flash filter.
			result, _, err := h.Distiller.Filter(fctx, existing, entry)
			if err != nil {
				// Timeout or network error → silent discard.
				return
			}

			if result == FilterReject {
				// Silent discard — no TUI notification.
				return
			}

			// ACCEPT: write to M.
			entry.AutoSourced = true
			entry.ObserveCount = 1 // M5.11: first observation → count=1
			if err := h.Project.Add(entry); err != nil {
				send(ObserveResult{
					Name:    entry.Name,
					Tagline: entry.Tagline,
					OK:      false,
					Err:     fmt.Sprintf("write failed: %v", err),
				})
				return
			}

			send(ObserveResult{
				Name:    entry.Name,
				Tagline: entry.Tagline,
				OK:      true,
			})
			h.observeAcceptCt.Add(1) // M5.13: successful save → stats counter
		}()
	}
}

// OnPostToolUse tracks memory_observe calls for observability. The actual
// async filter is launched by ObserveEnqueue (called from the tool's Execute);
// this observer is a no-op placeholder for future telemetry (call counts, etc.).
func (h *Hook) OnPostToolUse(_ context.Context, ev hooks.PostToolUseEvent) {
	if ev.Name != "memory_observe" {
		return
	}
	// Future: increment telemetry counter, log call stats.
}

// wrapContext renders a single <context source="X">...</context> block.
// Trailing newline before the closing tag keeps the rendered markdown
// readable when the model echoes it back; the opening tag is on its
// own line for the same reason.
func wrapContext(source, body string) string {
	return fmt.Sprintf("<context source=%q>\n%s\n</context>", source, body)
}
