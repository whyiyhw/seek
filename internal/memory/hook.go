package memory

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/hooks"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

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
//
// Both Project and Soul may be nil: callers (cmd/seek/main.go) load
// what they can and pass nils for the rest. A Hook with both nil is a
// no-op — Register it anyway so the registration order doesn't drift
// based on which fields loaded successfully.
type Hook struct {
	Project *Project
	Soul    *Soul

	// Distiller, when non-nil and $SEEK_AUTO_DISTILL=1, drives the
	// M5.7 auto-distill SessionEnd path: at session end, if the
	// satisfaction signal clears the threshold, the reasoner is
	// asked for ≤3 candidates and any that come back land in M
	// with AutoSourced=true (skipping the y/n review modal).
	//
	// Off by default — gated on the env var to keep the safety net
	// intact while we tune satisfaction-signal thresholds.
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

	// Now overrides the SessionStart timestamp for testing. Production
	// callers leave it zero and the hook uses time.Now().UTC().
	Now func() time.Time

	// autoDreamRan is set by tests that need to wait for the
	// auto-dream goroutine. Production callers ignore it (the
	// channel is nil and the goroutine is fire-and-forget).
	autoDreamDone chan struct{}
}

// envAutoDistill is the env-var name gating M5.7's auto-distill. Set
// to "1" / "true" / "yes" to opt in. Default (empty / any other value)
// = off, because hallucinated entries are the explicit risk PRD §3
// originally barred this feature over.
const envAutoDistill = "SEEK_AUTO_DISTILL"

func autoDistillEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(envAutoDistill)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// OnPrePrompt builds the deterministic <context> Prepend messages from
// the current L+M state. Empty inputs produce no message — an empty L
// section should not inject a "<context>(empty)</context>" wrapper
// because that's still bytes the prefix cache has to absorb.
//
// Byte-stability requirement (PRD §8 #7): identical L+M disk state →
// identical Prepend bytes. The map iteration in Project.Index is
// already sorted by Name; L's section body comes straight from the
// file. Both flow through here without further transformation.
func (h *Hook) OnPrePrompt(_ context.Context, _ hooks.PrePromptIn) (hooks.PrePromptOut, error) {
	var prepend []deepseek.Message

	if h.Soul != nil && strings.TrimSpace(h.Soul.Stable) != "" {
		prepend = append(prepend, deepseek.Message{
			Role:    deepseek.RoleUser,
			Content: wrapContext("memory.soul", h.Soul.Stable),
		})
	}

	if h.Project != nil {
		idx := h.Project.Index()
		if len(idx) > 0 {
			var sb strings.Builder
			sb.WriteString("Project memory index — call memory_recall(name) to fetch full content for any entry below.\n\n")
			for _, e := range idx {
				fmt.Fprintf(&sb, "- %s: %s\n", e.Name, e.Tagline)
			}
			prepend = append(prepend, deepseek.Message{
				Role:    deepseek.RoleUser,
				Content: wrapContext("memory.index", strings.TrimRight(sb.String(), "\n")),
			})
		}
	}

	return hooks.PrePromptOut{Prepend: prepend}, nil
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
func (h *Hook) runAutoDream(ctx context.Context) {
	projects, err := ListProjects()
	if err != nil || len(projects) == 0 {
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
	addition := FormatLCandidatesMarkdown(cands)
	pending := strings.TrimSpace(soul.Pending)
	if pending == "" {
		pending = addition
	} else {
		pending = pending + "\n\n" + addition
	}
	soul.SetSections(soul.Stable, pending)
	_ = soul.Save()
}

// OnSessionEnd is the M5.7 auto-distill trigger. Gated on the env var
// + the satisfaction signal + having all the wiring (Project + Distiller
// + HistoryProvider). Failure mode: log and move on — auto-distill is
// best-effort enhancement, never load-bearing.
//
// Why SessionEnd and not periodic-during-session: a settled history is
// the right substrate to distill from. Mid-session decisions can still
// be reversed; end-of-session is when we know what stuck.
func (h *Hook) OnSessionEnd(ctx context.Context, _ hooks.SessionEndEvent) {
	if h.Project == nil || h.Distiller == nil || h.HistoryProvider == nil {
		return
	}
	if !autoDistillEnabled() {
		return
	}

	history := h.HistoryProvider()
	sig := ScoreSatisfaction(history)
	if !IsSatisfied(sig) {
		return
	}

	// Bound the reasoner call so SessionEnd doesn't hang process
	// shutdown indefinitely if the network is slow.
	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cands, err := h.Distiller.Distill(rctx, history)
	if err != nil {
		// Swallow — same philosophy as RunGC failure. We don't
		// want a dud auto-distill to make session exit fail.
		return
	}
	for _, c := range cands {
		_ = h.Project.Add(Entry{
			Name:        c.Name,
			Tagline:     c.Tagline,
			Content:     c.Content,
			Tags:        c.Tags,
			AutoSourced: true,
		})
	}
}

// wrapContext renders a single <context source="X">...</context> block.
// Trailing newline before the closing tag keeps the rendered markdown
// readable when the model echoes it back; the opening tag is on its
// own line for the same reason.
func wrapContext(source, body string) string {
	return fmt.Sprintf("<context source=%q>\n%s\n</context>", source, body)
}
