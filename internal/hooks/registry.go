package hooks

import (
	"context"
	"fmt"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Registry is a typed dispatcher for hook implementations.
//
// Decorator slots (prePrompt, preToolUse) preserve registration order:
// later hooks see earlier outputs. Observer slots dispatch in registration
// order too, but by design observers cannot influence each other, so the
// order is not a semantic contract.
type Registry struct {
	prePrompt    []PrePromptHook
	preToolUse   []PreToolUseHook
	preTurn      []PreTurnObserver
	postTurn     []PostTurnObserver
	postToolUse  []PostToolUseObserver
	sessionStart []SessionStartObserver
	sessionEnd   []SessionEndObserver

	onPanic func(stage string, v any)
}

// NewRegistry returns an empty Registry. A nil *Registry is also valid and
// every dispatch helper is nil-safe.
func NewRegistry() *Registry { return &Registry{} }

// SetPanicHandler installs a callback for observer panics. Decorator
// panics propagate; observer panics are recovered so extension code
// cannot kill the agent loop. Default behaviour is a silent swallow.
func (r *Registry) SetPanicHandler(fn func(stage string, v any)) {
	if r == nil {
		return
	}
	r.onPanic = fn
}

// Register inspects h against every supported interface and adds it to
// each slot it satisfies. Panics if h implements none — almost always
// indicates a typo in the hook's method signature.
func (r *Registry) Register(h any) {
	matched := false
	if x, ok := h.(PrePromptHook); ok {
		r.prePrompt = append(r.prePrompt, x)
		matched = true
	}
	if x, ok := h.(PreToolUseHook); ok {
		r.preToolUse = append(r.preToolUse, x)
		matched = true
	}
	if x, ok := h.(PreTurnObserver); ok {
		r.preTurn = append(r.preTurn, x)
		matched = true
	}
	if x, ok := h.(PostTurnObserver); ok {
		r.postTurn = append(r.postTurn, x)
		matched = true
	}
	if x, ok := h.(PostToolUseObserver); ok {
		r.postToolUse = append(r.postToolUse, x)
		matched = true
	}
	if x, ok := h.(SessionStartObserver); ok {
		r.sessionStart = append(r.sessionStart, x)
		matched = true
	}
	if x, ok := h.(SessionEndObserver); ok {
		r.sessionEnd = append(r.sessionEnd, x)
		matched = true
	}
	if !matched {
		panic(fmt.Sprintf("hooks: Register(%T): value implements no hook interface", h))
	}
}

// Stack returns the registered hooks per slot, in dispatch order. Useful
// for debug logging right after wiring — call Stack and dump to confirm
// composition before the first user message lands.
type Stack struct {
	PrePrompt    []string
	PreToolUse   []string
	PreTurn      []string
	PostTurn     []string
	PostToolUse  []string
	SessionStart []string
	SessionEnd   []string
}

func (r *Registry) Stack() Stack {
	if r == nil {
		return Stack{}
	}
	s := Stack{}
	for _, h := range r.prePrompt {
		s.PrePrompt = append(s.PrePrompt, fmt.Sprintf("%T", h))
	}
	for _, h := range r.preToolUse {
		s.PreToolUse = append(s.PreToolUse, fmt.Sprintf("%T", h))
	}
	for _, h := range r.preTurn {
		s.PreTurn = append(s.PreTurn, fmt.Sprintf("%T", h))
	}
	for _, h := range r.postTurn {
		s.PostTurn = append(s.PostTurn, fmt.Sprintf("%T", h))
	}
	for _, h := range r.postToolUse {
		s.PostToolUse = append(s.PostToolUse, fmt.Sprintf("%T", h))
	}
	for _, h := range r.sessionStart {
		s.SessionStart = append(s.SessionStart, fmt.Sprintf("%T", h))
	}
	for _, h := range r.sessionEnd {
		s.SessionEnd = append(s.SessionEnd, fmt.Sprintf("%T", h))
	}
	return s
}

// ---- Decorator dispatch ----

// ApplyPrePrompt runs all PrePromptHooks in order. Each sees the working
// state produced by the previous one: UserText threads forward, Prepend
// accumulates. An error from any hook aborts the chain and bubbles up to
// the agent (which surfaces it as an ErrorEvent).
//
// Empty UserText from a hook means "no change" — the working value
// carries over unmodified. The returned PrePromptOut.UserText is always
// the effective text the caller should append (never empty unless the
// original input was empty and no hook changed it).
func (r *Registry) ApplyPrePrompt(ctx context.Context, in PrePromptIn) (PrePromptOut, error) {
	if r == nil || len(r.prePrompt) == 0 {
		return PrePromptOut{UserText: in.UserText}, nil
	}
	working := in
	var prepend []deepseek.Message
	for i, hook := range r.prePrompt {
		out, err := hook.OnPrePrompt(ctx, working)
		if err != nil {
			return PrePromptOut{}, fmt.Errorf("hooks: PrePrompt[%d] %T: %w", i, hook, err)
		}
		if out.UserText != "" {
			working.UserText = out.UserText
		}
		if len(out.Prepend) > 0 {
			prepend = append(prepend, out.Prepend...)
		}
	}
	return PrePromptOut{UserText: working.UserText, Prepend: prepend}, nil
}

// ApplyPreToolUse runs all PreToolUseHooks in order. The first hook to
// return Deny short-circuits — later hooks are not consulted. Args
// rewrites from earlier hooks are visible to later hooks (so a redactor
// chain works as expected).
//
// Returned Out.Deny non-empty: the agent should NOT invoke the tool, and
// should feed Deny back to the LLM as the tool result.
// Returned Out.Args non-nil: replaces the args the agent passes to the
// tool. nil means "use the original".
func (r *Registry) ApplyPreToolUse(ctx context.Context, in PreToolUseIn) (PreToolUseOut, error) {
	if r == nil || len(r.preToolUse) == 0 {
		return PreToolUseOut{}, nil
	}
	working := in
	var lastArgs []byte
	for i, hook := range r.preToolUse {
		out, err := hook.OnPreToolUse(ctx, working)
		if err != nil {
			return PreToolUseOut{}, fmt.Errorf("hooks: PreToolUse[%d] %T: %w", i, hook, err)
		}
		if out.Deny != "" {
			return PreToolUseOut{Deny: out.Deny}, nil
		}
		if out.Args != nil {
			lastArgs = out.Args
			working.Args = out.Args
		}
	}
	return PreToolUseOut{Args: lastArgs}, nil
}

// ---- Observer dispatch ----

func (r *Registry) NotifyPreTurn(ctx context.Context, ev PreTurnEvent) {
	if r == nil {
		return
	}
	for _, obs := range r.preTurn {
		r.runObserver("PreTurn", func() { obs.OnPreTurn(ctx, ev) })
	}
}

func (r *Registry) NotifyPostTurn(ctx context.Context, ev PostTurnEvent) {
	if r == nil {
		return
	}
	for _, obs := range r.postTurn {
		r.runObserver("PostTurn", func() { obs.OnPostTurn(ctx, ev) })
	}
}

func (r *Registry) NotifyPostToolUse(ctx context.Context, ev PostToolUseEvent) {
	if r == nil {
		return
	}
	for _, obs := range r.postToolUse {
		r.runObserver("PostToolUse", func() { obs.OnPostToolUse(ctx, ev) })
	}
}

func (r *Registry) NotifySessionStart(ctx context.Context, ev SessionStartEvent) {
	if r == nil {
		return
	}
	for _, obs := range r.sessionStart {
		r.runObserver("SessionStart", func() { obs.OnSessionStart(ctx, ev) })
	}
}

func (r *Registry) NotifySessionEnd(ctx context.Context, ev SessionEndEvent) {
	if r == nil {
		return
	}
	for _, obs := range r.sessionEnd {
		r.runObserver("SessionEnd", func() { obs.OnSessionEnd(ctx, ev) })
	}
}

func (r *Registry) runObserver(stage string, fn func()) {
	defer func() {
		if v := recover(); v != nil {
			if r.onPanic != nil {
				r.onPanic(stage, v)
			}
		}
	}()
	fn()
}
