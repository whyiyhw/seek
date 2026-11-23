package agent

// Mid-turn user injection: a user message queued while Prompt is in
// flight lands at the NEXT inference boundary — after the current tool
// batch's results are committed to history, before the next
// TurnStart. That is the only legal insertion point (see the drain
// sites in agent.go): assistant(tool_calls) must stay immediately
// followed by its matching tool messages, or DeepSeek rejects every
// later request with "tool_calls must be followed by tool messages"
// (docs/pitfalls/agent-loop/esc-mid-stream-poisoned-the-session-*).
//
// Cost model: appending at the tail leaves every prior byte untouched,
// so the prefix cache still hits. This is the same argument as
// hooks.PrePromptOut.Prepend (docs/prd/feature-hooks-injection.md §4.1)
// at a finer granularity — Prepend is the PROMPT frontier, this is the
// inner TURN frontier.
//
// Why this exists rather than reusing the TUI's queuedText path:
// queuedText only fires when Prompt() returns (the whole round is
// over), so on a long tool chain the user waits for every remaining
// call. Claude Code's "steer" behaves the same way this does —
// delivery at the next inference boundary, not mid-generation — and
// that boundary already exists in this loop.
//
// Sizing discipline: an injected message becomes permanent history, so
// it is re-sent on every later request of the session. The bash
// cd-prefix advisory measured 410 KB across 40 sessions (3.1% of every
// tool-result byte ever sent) for text nobody acted on — see
// internal/tools/bash/advisory.go. Hence hard caps here, rejected at
// queue time rather than truncated silently.

import "strings"

import "github.com/whyiyhw/seek/pkg/deepseek"

// Injection caps.
const (
	// maxInjectedBytes caps one injected message. Matches the upper end
	// of the hook-injection band (feature-hooks-injection §5.3:
	// max_bytes default 2048, ceiling 8192) — same reasoning: injected
	// bytes are permanent, so the budget is deliberately conservative.
	maxInjectedBytes = 8192
	// maxPendingInjections caps how many messages may be queued for one
	// turn. Four covers "no wait", "use X instead", "also check Y"
	// without turning the turn boundary into a chat channel.
	maxPendingInjections = 4
)

// QueueUserMessage queues text for delivery at the next inference
// boundary of the in-flight Prompt. Returns false when no Prompt is
// running (nothing to inject into) or when a cap would be exceeded —
// the caller then falls back to its own queue/steer handling rather
// than silently dropping the text.
//
// Safe to call from a different goroutine than Prompt's (the TUI
// does); Prompt itself remains single-call-at-a-time per Agent.
func (a *Agent) QueueUserMessage(text string) bool {
	if a == nil || strings.TrimSpace(text) == "" || len(text) > maxInjectedBytes {
		return false
	}
	a.injectMu.Lock()
	defer a.injectMu.Unlock()
	if !a.injectActive || len(a.injectQueue) >= maxPendingInjections {
		return false
	}
	a.injectQueue = append(a.injectQueue, text)
	return true
}

// beginTurnSession marks a Prompt as in flight and clears anything a
// previous (cancelled) turn left behind. Called once at Prompt entry,
// BEFORE the goroutine starts, so a queue attempt racing the call lands
// in this round or is rejected — never in the next one.
//
// The clear is also what keeps "Esc stops everything" honest: a message
// queued for a turn the user then killed must not resurface later.
func (a *Agent) beginTurnSession() {
	a.injectMu.Lock()
	a.injectActive = true
	a.injectQueue = nil
	a.injectMu.Unlock()
}

// endTurnSession clears the in-flight flag and discards anything still
// queued. Registered as a defer in PromptFiles so a late
// QueueUserMessage after the round returned reports false.
func (a *Agent) endTurnSession() {
	a.injectMu.Lock()
	a.injectActive = false
	a.injectQueue = nil
	a.injectMu.Unlock()
}

// commitInjected drains the queue, appends each message to history with
// the same mode-reminder suffix every user turn carries, and emits one
// InjectedMessage per commit. Reports whether anything landed — the
// caller uses that to decide between continuing the loop and settling.
//
// Call sites are the ONLY two legal insertion points, both after a tool
// batch's results are in history:
//   - top of an iteration with turn > 0 (turn 0 is the user's own prompt),
//   - the settle point, where an injection turns "about to return" into
//     "one more turn" instead of silently dropping the text.
func (a *Agent) commitInjected(turn int, out chan<- Event) bool {
	a.injectMu.Lock()
	pending := a.injectQueue
	a.injectQueue = nil
	a.injectMu.Unlock()
	if len(pending) == 0 {
		return false
	}
	for _, text := range pending {
		msg := deepseek.Message{Role: deepseek.RoleUser, Content: text + modeReminder(a.cfg.ModeLabel)}
		a.appendMessage(msg)
		out <- InjectedMessage{Index: turn, Message: msg}
	}
	return true
}
