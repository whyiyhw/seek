package memory

import (
	"context"
	"fmt"
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

	// Now overrides the SessionStart timestamp for testing. Production
	// callers leave it zero and the hook uses time.Now().UTC().
	Now func() time.Time
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
func (h *Hook) OnSessionStart(_ context.Context, _ hooks.SessionStartEvent) {
	if h.Project == nil {
		return
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now()
	}
	_, _ = h.Project.RunGC(now)
}

// wrapContext renders a single <context source="X">...</context> block.
// Trailing newline before the closing tag keeps the rendered markdown
// readable when the model echoes it back; the opening tag is on its
// own line for the same reason.
func wrapContext(source, body string) string {
	return fmt.Sprintf("<context source=%q>\n%s\n</context>", source, body)
}
