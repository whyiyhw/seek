package agent

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Compact-overflow rescue (card c_idisqf; docs/pitfalls.md § "Grep tool
// with broad pattern can deadlock").
//
// /compact's summariser call sends the FULL history plus a summariser
// prompt. Once that history overflows the model's context window the
// API rejects the rescue request itself — the one call that was supposed
// to shrink the session is the one that cannot fit, and every further
// prompt 400s the same way: a hard deadlock with no in-app recovery.
//
// The rescue is a DEGRADATION, entered only when the request provably
// does not fit (estimate over budget, or the API said so):
//
//   - The normal path is byte-identical to the pre-rescue shape — the
//     fitting request is never touched (prefix-cache discipline;
//     TestSummarise_NormalPath_BytesUnchanged guards it).
//   - Stage 1 caps oversized message BODIES in place (keep-head +
//     marker). Truncating content cannot break tool_call/result pairing
//     the way dropping a lone tool result would (an orphaned tool
//     message is a second kind of 400).
//   - Stage 2 drops the OLDEST messages — never the system prompt,
//     never the summariser turn — skipping forward past tool results
//     whose parent assistant message went with the dropped side (same
//     orphan rule; it may over-drop the tail of a multi-tool batch —
//     one extra tool result lost beats an invalid request). Keeping the
//     TAIL is the selection: a compact briefing's job is to let work
//     CONTINUE, and the live task state lives in recent turns — the
//     ancient turns are what the summary was going to compress anyway.

const (
	// rescueBodyCap is the per-message content cap stage 1 applies. 8 KiB
	// holds a full capped grep/read output with room for the truncation
	// notice, and keeps a couple hundred messages inside a 128K window.
	rescueBodyCap = 8 << 10

	// rescueOutputReserve is subtracted from the window for the summary
	// itself. The summariser asks for ≤ ~400 words ≈ 600 tokens; 4096
	// covers it plus wire overhead with margin. Some APIs also validate
	// input + max_tokens ≤ window, so the reserve never goes below
	// cfg.MaxTokens (rescueBudget applies that).
	rescueOutputReserve = 4096
)

// rescueNote is appended to the summariser prompt only when trimming
// fired, so the model states what era its briefing covers instead of
// hallucinating continuity over the elided messages.
const rescueNote = "\n\n[Context rescue: the conversation above is INCOMPLETE — the oldest messages were dropped and oversized bodies truncated to fit the context window. Start the briefing with one line stating how far back it reaches.]"

// rescueBudget returns the input-token budget for a summariser request
// on the given model: the model's window minus a reserve for the
// summary output (never below cfg.MaxTokens). Unknown models get
// budget.Default — conservative for a 1M-context model (it trims a
// summary's oldest material needlessly) but correct for a small one,
// and the reactive retry covers the opposite error.
func rescueBudget(model string, maxTokens int) int {
	reserve := rescueOutputReserve
	if maxTokens > reserve {
		reserve = maxTokens
	}
	b := budget.Limit(model) - reserve
	if b < 1024 {
		b = 1024 // pathological floor: still leaves a workable head+prompt
	}
	return b
}

// msgTokens estimates one message's token cost: ASCII ≈ 4 bytes/token,
// non-ASCII (CJK and friends) ≈ 1 token per rune. The CJK rate is
// deliberately conservative — real tokenizers give ~0.6-0.7 tokens per
// Han rune — so mixed-language histories err HIGH and the rescue fires
// early rather than never. The 8 covers role/separator/id overhead.
func msgTokens(m deepseek.Message) int {
	n := 8
	for _, tc := range m.ToolCalls {
		n += len(tc.ID) + len(tc.Function.Name) + len(tc.Function.Arguments)/4
	}
	if m.Content == "" {
		return n
	}
	wide := 0
	for _, r := range m.Content {
		if r >= 0x80 {
			wide++
		}
	}
	runes := utf8.RuneCountInString(m.Content)
	return n + (runes-wide)/4 + wide
}

// estimateTokens approximates the input-token count of a message slice.
// Deterministic and allocation-free — the same estimate drives the
// pre-flight check and the trim loop's cut selection.
func estimateTokens(msgs []deepseek.Message) int {
	total := 0
	for _, m := range msgs {
		total += msgTokens(m)
	}
	return total
}

// rescueStats reports what a trim did, for the caller's logging/tests.
type rescueStats struct {
	active    bool // any trimming happened
	dropped   int  // oldest messages dropped (stage 2)
	truncated int  // oversized bodies capped (stage 1)
}

// rescueTruncMarker terminates a stage-1 capped body. Constant (no
// byte count inside) so HasSuffix gives exact idempotence: a second
// trim pass — the reactive retry re-trims an already-capped slice —
// skips capped bodies instead of shaving the marker off.
const rescueTruncMarker = "\n…[compact rescue: truncated]"

// trimForRescue returns msgs untouched when estimateTokens already fits
// budgetTokens (the normal path — callers rely on this for byte
// stability). Otherwise it returns a TRIMMED COPY: stage 1 caps
// oversized bodies, stage 2 drops the oldest messages until the
// remaining suffix fits. The input slice is never mutated.
func trimForRescue(msgs []deepseek.Message, budgetTokens int) ([]deepseek.Message, rescueStats) {
	var stats rescueStats
	if estimateTokens(msgs) <= budgetTokens {
		return msgs, stats
	}
	stats.active = true
	out := slices.Clone(msgs)

	// Stage 1 — cap giant bodies in place.
	for i := range out {
		m := &out[i]
		if m.Role == deepseek.RoleSystem || len(m.Content) <= rescueBodyCap ||
			strings.HasSuffix(m.Content, rescueTruncMarker) {
			continue
		}
		cut := rescueBodyCap
		for cut > 0 && !utf8.RuneStart(m.Content[cut]) { // don't split a rune
			cut--
		}
		m.Content = string(m.Content[:cut]) + rescueTruncMarker
		stats.truncated++
	}
	if estimateTokens(out) <= budgetTokens {
		return out, stats
	}

	// Stage 2 — drop oldest. head is the undroppable prefix (system
	// messages); the loop never selects a cut at len(out)-1, so the
	// summariser turn survives. suffixCosts[i] = estimate(out[i:]) makes
	// cut selection a single O(n) backward pass.
	head := 0
	for head < len(out) && out[head].Role == deepseek.RoleSystem {
		head++
	}
	suffixCosts := make([]int, len(out)+1)
	for i := len(out) - 1; i >= 0; i-- {
		suffixCosts[i] = suffixCosts[i+1] + msgTokens(out[i])
	}
	cut := len(out) - 1 // worst case: everything droppable goes
	for i := head; i < len(out)-1; i++ {
		// A cut AT a tool result would strand it without its parent
		// assistant message (dropped side) — an orphan the API rejects.
		if out[i].Role == deepseek.RoleTool {
			continue
		}
		if suffixCosts[i] <= budgetTokens {
			cut = i
			break
		}
	}
	if cut <= head {
		return out, stats // nothing droppable helped; send the stage-1 result
	}
	stats.dropped = cut - head
	kept := make([]deepseek.Message, 0, head+len(out)-cut)
	kept = append(kept, out[:head]...)
	kept = append(kept, out[cut:]...)
	return kept, stats
}

// prepareSummariseMessages applies the rescue to a summariser request's
// message slice (history with the summariser prompt already appended)
// at the given token budget, appending the elision note to the prompt
// when trimming fired. Callers derive the budget via rescueBudget (and
// halve it for the reactive retry).
func prepareSummariseMessages(msgs []deepseek.Message, budgetTokens int) []deepseek.Message {
	trimmed, stats := trimForRescue(msgs, budgetTokens)
	if !stats.active {
		return trimmed
	}
	// The last message is the summariser prompt; tell the model what it
	// is NOT seeing so the briefing stays honest about its coverage.
	out := slices.Clone(trimmed)
	out[len(out)-1].Content += rescueNote
	return out
}

// isContextOverflowError reports whether err looks like the API's
// "your input does not fit the window" rejection. Matching is loose on
// purpose: it only gates a single trimmed retry, so a false positive
// costs one already-necessary call, and providers word the message
// differently enough that a strict match would miss real ones. 5xx are
// excluded outright — trimming cannot fix an upstream outage.
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *deepseek.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 500 {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "maximum context") ||
		strings.Contains(s, "context length") ||
		strings.Contains(s, "context window") ||
		strings.Contains(s, "too long") ||
		strings.Contains(s, "input length")
}
