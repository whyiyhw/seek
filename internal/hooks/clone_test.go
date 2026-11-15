package hooks

import (
	"context"
	"testing"
)

// TestCloneWithout_ExcludesHookAndIsolatesFutureRegistrations: the
// clone drops exactly the excluded hook instance (pointer identity)
// from every slot it occupies, keeps everything else in order, and
// is isolated from later Register calls on the original — the two
// properties the subagent child-registry wiring depends on
// (children see the parent hook surface minus the background
// Notifier, and hooks registered after the closure was made still
// show up because the closure resolves per spawn).
func TestCloneWithout_ExcludesHookAndIsolatesFutureRegistrations(t *testing.T) {
	r := NewRegistry()

	memory := &prePromptFake{out: PrePromptOut{UserText: "kept"}}
	notifier := &prePromptFake{out: PrePromptOut{UserText: "excluded"}}
	audit := &postToolUseFake{}

	r.Register(memory)
	r.Register(notifier)
	r.Register(audit)

	clone := r.CloneWithout(notifier)
	if clone == nil {
		t.Fatal("CloneWithout returned nil for non-nil receiver")
	}

	// Excluded hook is gone; survivor dispatch still fires in order.
	out, err := clone.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "hi"})
	if err != nil {
		t.Fatalf("ApplyPrePrompt on clone: %v", err)
	}
	if out.UserText != "kept" {
		t.Errorf("clone UserText = %q, want %q (survivor missing or misordered)", out.UserText, "kept")
	}
	if memory.calls != 1 || notifier.calls != 0 {
		t.Errorf("calls after clone dispatch: memory=%d notifier=%d, want 1/0", memory.calls, notifier.calls)
	}

	// Observer slot survived too.
	clone.NotifyPostToolUse(context.Background(), PostToolUseEvent{})
	if audit.calls != 1 {
		t.Errorf("audit calls = %d, want 1 (observer lost in clone)", audit.calls)
	}

	// Isolation: registering on the ORIGINAL after cloning must not
	// leak into the clone.
	latecomer := &prePromptFake{out: PrePromptOut{UserText: "late"}}
	r.Register(latecomer)
	out2, err := clone.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "hi"})
	if err != nil {
		t.Fatalf("ApplyPrePrompt on clone after original Register: %v", err)
	}
	if out2.UserText == "late" {
		t.Error("late registration on original leaked into clone")
	}

	// And the original still dispatches everything (exclusion is
	// clone-scoped, never destructive on the receiver).
	out3, err := r.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: "hi"})
	if err != nil {
		t.Fatalf("ApplyPrePrompt on original: %v", err)
	}
	if out3.UserText != "late" {
		t.Errorf("original UserText = %q, want %q (original damaged by clone)", out3.UserText, "late")
	}
}

// TestCloneWithout_NilIsPlainSnapshot: CloneWithout(nil) copies every
// slot — the "just give me a snapshot" degenerate form.
func TestCloneWithout_NilIsPlainSnapshot(t *testing.T) {
	r := NewRegistry()
	a := &prePromptFake{out: PrePromptOut{UserText: "a"}}
	b := &preToolUseFake{}
	r.Register(a)
	r.Register(b)

	clone := r.CloneWithout(nil)
	if got := len(clone.prePrompt); got != 1 {
		t.Errorf("clone prePrompt len = %d, want 1", got)
	}
	if got := len(clone.preToolUse); got != 1 {
		t.Errorf("clone preToolUse len = %d, want 1", got)
	}
}

// TestCloneWithout_NilReceiver: a nil *Registry is valid throughout
// the package; CloneWithout keeps that contract (returns nil, which
// every dispatch helper tolerates).
func TestCloneWithout_NilReceiver(t *testing.T) {
	var r *Registry
	if c := r.CloneWithout(nil); c != nil {
		t.Errorf("nil receiver CloneWithout = %v, want nil", c)
	}
}
