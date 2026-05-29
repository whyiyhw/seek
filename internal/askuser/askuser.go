// Package askuser is the in-process channel + policy that lets a
// model-callable tool block on the user's choice from a small list
// of options. Same architecture as internal/permission's askFn — the
// tool calls Ask() which blocks on a user-supplied callback; the TUI
// drives that callback from a pending-question UI element.
//
// What lives here:
//   - Question: the shape an ask_user tool call produces (label,
//     options, multi-select flag).
//   - Answer: the shape the TUI sends back (chosen option ids OR
//     free-text when the user picked "Other").
//   - Policy: the goroutine-safe holder for the callback and the
//     mode (Ask / Disabled). cmd/seek constructs one at startup and
//     plumbs it into both the tool and the TUI.
//
// Why not bolt it onto internal/permission: permission gates "may I
// do this?" — a boolean. askuser is "which one?" — a choice with
// free-text fallback. Squashing them would force both UIs through a
// common but lossy Answer shape; the cost of one ~150-line package
// to keep the two flows distinct is well below the ongoing cost of
// pretending they're the same thing.
package askuser

import (
	"errors"
	"fmt"
	"sync"
)

// Mode controls how Ask resolves a request.
type Mode int

const (
	// ModeAsk consults the askFn callback. The default for the
	// interactive TUI.
	ModeAsk Mode = iota
	// ModeDisabled returns an error from every Ask call. Used for
	// non-interactive runs (print mode, -p) where there's no human
	// in the loop; the tool surfaces the error so the model can
	// rephrase its question as plain text instead.
	ModeDisabled
)

// Option is one row in the picker the user sees.
type Option struct {
	// ID is what the tool gets back when the user selects this row.
	// Must be unique within a Question; the validation lives in
	// the tool layer (this package trusts its callers).
	ID string `json:"id"`
	// Label is the short row text. One line. Renderers truncate
	// past ~80 chars.
	Label string `json:"label"`
	// Description is optional secondary text shown muted next to
	// the label. Use for "what does this option mean?" — leave
	// empty when the label is self-explanatory.
	Description string `json:"description,omitempty"`
	// Preview (v2) is optional plain-text content shown in a
	// side-panel when the user hovers this option in a wide
	// terminal (>=100 cols). Use for ASCII mockups, code snippets,
	// or visual comparisons. Plain monospace only — no markdown.
	// TUI truncates at ~12 lines × 80 cols with an explicit
	// "[truncated]" marker. Narrow terminals collapse the preview
	// to an indented block under the option row.
	Preview string `json:"preview,omitempty"`
}

// Question is what the askuser tool builds and hands to Ask. The
// "Other / free-text" row is NOT in Options — the TUI auto-appends
// it. Keeping the auto-row out of Options means tool code can't
// accidentally collide ids with it.
type Question struct {
	// Question is the prompt the user sees as a header. Should be
	// terse — long context belongs in the conversation, not the
	// picker.
	Question string
	// Header (v2) is an optional short chip-style label shown
	// alongside the question — useful in multi-question batches
	// where each question needs a 1-2 word topic marker (e.g.
	// "Framework", "Storage", "Auth"). Single-question pickers
	// typically leave it empty (the Question line itself is the
	// label).
	Header string
	// Options is the model-provided list. Constrained to 2–4 rows
	// (tool layer enforces). The "Other" row is added by the TUI.
	Options []Option
	// MultiSelect=true switches the picker into Space-toggle mode.
	// Enter then confirms the set of toggled rows. Single-select
	// (false) is the default and matches every other picker in
	// seek (Enter on a highlighted row accepts immediately).
	MultiSelect bool
}

// Batch (v2) is a multi-question payload — the tool can ask 1–4
// independent questions in one call. The TUI renders them as a
// stack: answered questions dim out, the current one shows the
// active picker, pending questions show as placeholders. Esc on
// the current picker preserves prior answers and marks the
// remainder cancelled.
//
// Single-question pickers go through this same path internally
// (wrapped to a 1-element Batch by the tool layer), so the TUI
// only deals with one shape. v1 result-shape backward-compatibility
// lives in the tool layer, not here.
type Batch struct {
	Questions []Question
}

// Answer is what the TUI sends back through the reply channel. The
// two field families are mutually exclusive: either the user picked
// from the offered options (ChosenIDs non-empty) OR they typed a
// free-text response via the auto-appended "Other" row (FreeText
// non-empty). When the user cancels (Esc on a single-select picker
// without committing), Cancelled is true and both other fields stay
// empty.
type Answer struct {
	ChosenIDs []string
	FreeText  string
	Cancelled bool
}

// Request is what Policy.Ask emits on the channel the TUI is
// reading. Reply MUST receive exactly one Answer — askFn blocks
// on it, so a missing reply hangs the tool.
type Request struct {
	Question Question
	Reply    chan<- Answer
}

// BatchRequest (v2) is what Policy.AskBatch emits. Reply receives
// a single []Answer aligned by index with the batch's Questions.
// Length always equals len(batch.Questions); cancelled / unasked
// questions get zero-value Answer{Cancelled: true} entries.
type BatchRequest struct {
	Batch Batch
	Reply chan<- []Answer
}

// Policy holds the callback the tool dispatches to. Construct via
// New; concurrency mirrors permission.Policy — mode/askFn can flip
// at runtime from the TUI goroutine while the tool goroutine is
// inside Ask(), so a mutex serialises the transitions.
//
// Two callback slots live here (v1 + v2). v2 is the canonical
// path now (the ask_user tool always calls AskBatch); v1 askFn
// stays for direct internal callers (e.g. propose tool) that
// only ever ask one question at a time. When askBatchFn is nil
// but askFn is set, AskBatch falls back to looping single Ask
// calls — preserves backward behaviour for any caller that
// wires only the v1 callback.
type Policy struct {
	mu         sync.RWMutex
	mode       Mode
	askFn      func(Question) Answer
	askBatchFn func(Batch) []Answer
}

// New returns a Policy starting in ModeAsk. SetAskFn must be called
// before Ask is invoked, otherwise Ask returns ErrNoCallback.
func New(mode Mode) *Policy {
	return &Policy{mode: mode}
}

// SetAskFn registers the callback that turns a Question into an
// Answer. Called from the TUI's wiring code at startup. The
// callback is expected to BLOCK until the user has chosen (or
// cancelled). Called from the tool's goroutine, not the TUI's, so
// the callback can safely use channel ops.
func (p *Policy) SetAskFn(fn func(Question) Answer) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.askFn = fn
}

// SetAskBatchFn (v2) registers the batch callback. Like SetAskFn
// but takes a Batch and returns []Answer aligned by question
// index. Called from the same TUI wire-up code; both callbacks
// can coexist (and typically share state, e.g. the same TUI
// request channel demuxed by type).
func (p *Policy) SetAskBatchFn(fn func(Batch) []Answer) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.askBatchFn = fn
}

// Mode returns the current mode.
func (p *Policy) Mode() Mode {
	if p == nil {
		return ModeDisabled
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// SetMode updates the active mode. Used by non-interactive launches
// to disable the channel after init.
func (p *Policy) SetMode(m Mode) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = m
}

// ErrDisabled is returned by Ask when the policy is in ModeDisabled.
// The tool layer surfaces this verbatim to the LLM so it knows to
// rephrase as plain text instead of retrying.
var ErrDisabled = errors.New("askuser: interactive question not available in this mode — rephrase as a plain-text question in your reply")

// ErrNoCallback is returned by Ask when no callback has been
// registered yet. Programming error; surfaces during integration
// tests if main.go forgets to wire SetAskFn.
var ErrNoCallback = errors.New("askuser: no callback registered (programming error)")

// Ask is the synchronous entry point the tool calls. Snapshots the
// current mode + callback under the lock, releases the lock, then
// calls the callback (which blocks). Returning the lock before the
// blocking call is the load-bearing invariant — without it, every
// concurrent Ask would serialise on the same RW lock.
func (p *Policy) Ask(q Question) (Answer, error) {
	if p == nil {
		return Answer{}, ErrDisabled
	}
	p.mu.RLock()
	mode := p.mode
	fn := p.askFn
	p.mu.RUnlock()

	if mode == ModeDisabled {
		return Answer{}, ErrDisabled
	}
	if fn == nil {
		return Answer{}, ErrNoCallback
	}
	return fn(q), nil
}

// AskBatch (v2) is the multi-question entry point. Returns
// []Answer with length == len(b.Questions); cancelled / unasked
// questions have Answer{Cancelled: true}.
//
// Falls back to the v1 single-Ask loop when no batch callback is
// registered — this keeps any caller that only wired SetAskFn
// working (the answers come back sequentially via the v1
// picker UX, just without the stack render).
//
// Lock semantics mirror Ask: snapshot under RLock, release
// before invoking the callback (which blocks on user input).
func (p *Policy) AskBatch(b Batch) ([]Answer, error) {
	if p == nil {
		return nil, ErrDisabled
	}
	if len(b.Questions) == 0 {
		return nil, fmt.Errorf("askuser: AskBatch with empty Questions")
	}
	p.mu.RLock()
	mode := p.mode
	batchFn := p.askBatchFn
	singleFn := p.askFn
	p.mu.RUnlock()

	if mode == ModeDisabled {
		return nil, ErrDisabled
	}
	if batchFn != nil {
		ans := batchFn(b)
		// Defensive: if the callback returns the wrong length,
		// pad / truncate to len(Questions) with cancelled
		// placeholders rather than crash. Programming bug in TUI,
		// but the tool result should still be usable by the LLM.
		switch {
		case len(ans) == len(b.Questions):
			return ans, nil
		case len(ans) < len(b.Questions):
			pad := make([]Answer, len(b.Questions))
			copy(pad, ans)
			for i := len(ans); i < len(pad); i++ {
				pad[i] = Answer{Cancelled: true}
			}
			return pad, nil
		default:
			return ans[:len(b.Questions)], nil
		}
	}
	if singleFn == nil {
		return nil, ErrNoCallback
	}
	// v1 fallback: loop. No stack-render UX, but functionally
	// correct. Mid-batch cancel semantics: if Q_i is cancelled,
	// Q_(i+1..N) are NOT asked and get cancelled placeholders.
	answers := make([]Answer, len(b.Questions))
	for i, q := range b.Questions {
		a := singleFn(q)
		answers[i] = a
		if a.Cancelled {
			for j := i + 1; j < len(answers); j++ {
				answers[j] = Answer{Cancelled: true}
			}
			break
		}
	}
	return answers, nil
}

// ValidateBatch (v2) checks structural rules for a Batch — 1–4
// questions, each individually valid. Errors point at the
// specific failing question index + the per-question error
// (e.g. "question 2: option 0: id is required").
func ValidateBatch(b Batch) error {
	n := len(b.Questions)
	if n < 1 || n > 4 {
		return fmt.Errorf("batch must have 1-4 questions, got %d", n)
	}
	for i, q := range b.Questions {
		if err := Validate(q); err != nil {
			return fmt.Errorf("question %d: %w", i, err)
		}
	}
	return nil
}

// Validate checks the structural rules for a Question. Returns nil
// for a valid question; otherwise a wrapped error pointing at the
// specific rule that failed. Called by the tool's Execute before
// dispatching to Ask — failing here means the model wrote bad
// arguments and we want a clear "fix it" message back, not a
// half-rendered picker.
func Validate(q Question) error {
	if q.Question == "" {
		return errors.New("question is required")
	}
	n := len(q.Options)
	if n < 2 || n > 4 {
		return fmt.Errorf("must have 2-4 options, got %d", n)
	}
	seen := map[string]bool{}
	for i, o := range q.Options {
		if o.ID == "" {
			return fmt.Errorf("option %d: id is required", i)
		}
		if o.Label == "" {
			return fmt.Errorf("option %d (%s): label is required", i, o.ID)
		}
		if seen[o.ID] {
			return fmt.Errorf("option ids must be unique; %q appears twice", o.ID)
		}
		seen[o.ID] = true
		// "other" is reserved for the auto-appended free-text row.
		// Models that try to add their own "Other" would collide;
		// reject early with a clear message.
		if o.ID == "other" {
			return errors.New(`option id "other" is reserved — the TUI auto-appends an "Other / type your own" row; don't include it in options`)
		}
	}
	return nil
}
