package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// RepeatGuard counts identical tool calls within a session and appends a
// escalating nudge to the result when the same call repeats.
//
// # The failure it addresses
//
// The most expensive model behaviour is not a wrong answer, it is a loop:
// the same `go build` that failed, run six times unchanged; the same file
// read four times because earlier reads scrolled out of attention; the
// same grep re-issued with identical arguments. Each iteration costs a
// full request AND grows the history that every later request re-sends.
// Nothing in the transcript tells the model it is repeating itself —
// each result looks locally reasonable, and the previous identical result
// is thousands of tokens back.
//
// The counter is the missing signal. Borrowed from deepseek-harness's
// guard/repeat-tool-reminder (packages/guard/repeat-tool-reminder/
// src/index.ts:28-79), including its escalating [3,5,8] thresholds:
// nudge, then insist, then say so bluntly.
//
// # Why this is not a filter layer in pkg/agent
//
// CLAUDE.md forbids post-hoc result mutation inside pkg/agent, and it is
// right to: that is where in-transit rewriting would wreck the prefix
// cache. This guard is a Tool decorator instead, so the reminder is
// produced at WRITE time as part of the tool's own result. It only ever
// appends to a NEW tool result — it never touches a message already sent
// — so the request prefix keeps growing append-only and cache hits are
// unaffected.
//
// A RepeatGuard is safe for concurrent use: read-only tools dispatch as a
// parallel batch.
type RepeatGuard struct {
	mu     sync.Mutex
	counts map[string]int
}

// repeatThresholds are the call counts that trigger a reminder. Chosen to
// escalate rather than nag: three identical calls can be legitimate
// (retry after an edit), eight never is.
var repeatThresholds = []int{3, 5, 8}

// maxArgPreview bounds how much of the argument blob appears in the
// reminder text. The reminder is model-visible context, and echoing a
// 30 KiB write payload back at the model to tell it it is looping would
// itself be the more expensive mistake.
const maxArgPreview = 500

// NewRepeatGuard returns a guard with no calls recorded. One per session;
// sharing across sessions would leak counts between conversations.
func NewRepeatGuard() *RepeatGuard {
	return &RepeatGuard{counts: map[string]int{}}
}

// record bumps the counter for (name, args) and returns the new count.
func (g *RepeatGuard) record(name string, raw json.RawMessage) int {
	key := name + "\x00" + canonicalArgs(raw)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[key]++
	return g.counts[key]
}

// canonicalArgs normalises an argument blob so that logically identical
// calls collide regardless of key order or insignificant whitespace.
//
// Matching on the WHOLE argument object, not just the tool name, is what
// makes the guard usable: `read` on twelve different files is healthy
// work, `read` on the same file twelve times is a loop. A name-only
// counter would flag the first and miss nothing useful.
//
// Non-object or malformed JSON falls back to the raw bytes — a tool whose
// arguments do not parse still deserves loop detection, and the raw form
// is a sound equality key even if it is not a canonical one.
func canonicalArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	// encoding/json marshals map keys in sorted order, so re-marshalling
	// a decoded value yields a canonical form for free.
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// reminderFor returns the model-visible nudge for the count-th identical
// call, or "" when this count is not a threshold.
func reminderFor(name string, raw json.RawMessage, count int) string {
	hit := false
	for _, t := range repeatThresholds {
		if count == t {
			hit = true
			break
		}
	}
	if !hit {
		return ""
	}

	preview := canonicalArgs(raw)
	if len(preview) > maxArgPreview {
		preview = preview[:maxArgPreview] + "…(truncated)"
	}

	var advice string
	switch {
	case count >= 8:
		advice = "You are almost certainly stuck in a loop. Stop repeating this call. " +
			"State plainly what you have tried and what is blocking you, or ask the user " +
			"with ask_user — another identical call will not produce a different result."
	case count >= 5:
		advice = "Repeating an identical call rarely produces a different result. " +
			"Change the arguments, try a different tool, or explain what you are stuck on."
	default:
		advice = "If the previous results were not what you needed, change the arguments " +
			"or approach rather than re-running the same call."
	}

	return fmt.Sprintf("[repeat-guard] This is call #%d to `%s` with identical arguments: %s\n%s",
		count, name, preview, advice)
}

// WithRepeatGuard wraps t so identical repeated calls carry a reminder.
//
// The returned Tool preserves the optional interfaces t implements —
// ReadOnlyTool and StreamingTool. That preservation is the whole reason
// this is four types instead of one: pkg/agent upcasts every tool
// (agent.go dispatches ReadOnlyTool batches concurrently and prefers
// StreamingTool's ExecuteStream), and a decorator that silently dropped
// those would turn parallel reads sequential and kill live output from
// the think tool — with no error anywhere to explain why.
func WithRepeatGuard(t Tool, g *RepeatGuard) Tool {
	if g == nil {
		return t
	}
	base := guardedTool{Tool: t, guard: g}

	st, streaming := t.(StreamingTool)
	ro, readOnly := t.(ReadOnlyTool)

	switch {
	case streaming && readOnly:
		return guardedStreamingReadOnly{guardedStreaming{base, st}, ro}
	case streaming:
		return guardedStreaming{base, st}
	case readOnly:
		return guardedReadOnly{base, ro}
	default:
		return base
	}
}

// guardedTool is the base decorator: it forwards Name/Description/Schema
// via the embedded Tool and only interposes on Execute.
type guardedTool struct {
	Tool
	guard *RepeatGuard
}

func (w guardedTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	count := w.guard.record(w.Tool.Name(), raw)
	result, err := w.Tool.Execute(ctx, raw)
	return appendReminder(w.Tool.Name(), raw, count, result, err)
}

type guardedStreaming struct {
	guardedTool
	st StreamingTool
}

func (w guardedStreaming) ExecuteStream(ctx context.Context, raw json.RawMessage, push func(StreamDelta) error) (string, error) {
	count := w.guard.record(w.Tool.Name(), raw)
	result, err := w.st.ExecuteStream(ctx, raw, push)
	// The reminder is appended to the RETURNED result only, never pushed
	// as a delta: deltas are live UI, and the returned string is what
	// lands in history. Pushing it would show the user a warning that
	// then also appears in the transcript.
	return appendReminder(w.Tool.Name(), raw, count, result, err)
}

type guardedReadOnly struct {
	guardedTool
	ro ReadOnlyTool
}

func (w guardedReadOnly) ReadOnly() bool { return w.ro.ReadOnly() }

type guardedStreamingReadOnly struct {
	guardedStreaming
	ro ReadOnlyTool
}

func (w guardedStreamingReadOnly) ReadOnly() bool { return w.ro.ReadOnly() }

// appendReminder attaches the nudge to whichever channel the model will
// actually see.
//
// The error path matters more than the success path here: the archetypal
// loop is a command that keeps FAILING. pkg/agent's buildToolResultMsg
// documents that "errors always win" — a non-nil error replaces the
// result entirely in the tool-result message — so a reminder appended
// only to the result string would be silently dropped in exactly the
// case it exists for. Wrapping with %w keeps errors.Is intact
// (permission.ErrDenied is checked by callers), and the text is appended
// AFTER the original message so any prefix-matching on error strings
// still works.
func appendReminder(name string, raw json.RawMessage, count int, result string, err error) (string, error) {
	note := reminderFor(name, raw, count)
	if note == "" {
		return result, err
	}
	if err != nil {
		return result, fmt.Errorf("%w\n\n%s", err, note)
	}
	if strings.TrimSpace(result) == "" {
		return note, nil
	}
	return result + "\n\n" + note, nil
}
