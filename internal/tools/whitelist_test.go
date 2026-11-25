package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// stubTool is a minimal Tool for registry-composition tests.
type stubTool struct{ name string }

func (s stubTool) Name() string            { return s.name }
func (s stubTool) Description() string     { return "stub " + s.name }
func (s stubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "stub", nil
}

func newTestRegistry(names ...string) *Registry {
	r := New()
	for _, n := range names {
		r.Add(stubTool{name: n})
	}
	return r
}

// Whitelist keeps exactly the listed tools: Names() reflects the
// filter, Lookup() of a dropped tool goes nil, and Wire() — the bytes
// actually sent to the backend — carries only survivors (v8·T1
// acceptance: the tools array is exactly the whitelist).
func TestWhitelist_KeepsOnlyListed(t *testing.T) {
	reg := newTestRegistry("read", "edit", "write", "bash", "grep", "monitor", "git")

	reg.Whitelist([]string{"read", "edit", "write", "bash", "grep"})

	want := []string{"bash", "edit", "grep", "read", "write"} // sorted
	got := reg.Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
	if reg.Lookup("monitor") != nil {
		t.Errorf("Lookup(monitor) after whitelist = non-nil, want nil")
	}
	if reg.Lookup("read") == nil {
		t.Errorf("Lookup(read) after whitelist = nil, want the survivor instance")
	}

	wire := reg.Wire()
	if len(wire) != len(want) {
		t.Fatalf("Wire() has %d tools, want %d", len(wire), len(want))
	}
	for i, wt := range wire {
		if wt.Function.Name != want[i] {
			t.Errorf("Wire()[%d] = %q, want %q (sorted)", i, wt.Function.Name, want[i])
		}
	}
}

// Empty keep list is a no-op — the -tools "empty or absent = full
// default registry" contract lives in Whitelist itself.
func TestWhitelist_EmptyKeepIsNoop(t *testing.T) {
	reg := newTestRegistry("read", "edit", "bash")
	before := reg.Names()

	reg.Whitelist(nil)
	reg.Whitelist([]string{})

	got := reg.Names()
	if len(got) != len(before) {
		t.Fatalf("Names() after empty whitelist = %v, want unchanged %v", got, before)
	}
}

// Names absent from the registry are ignored by the filter itself —
// loud validation is the caller's job (cmd/seek exits 2 first). This
// keeps Whitelist safe for callers that derive the keep list from
// config that may name tools a particular build doesn't register.
func TestWhitelist_UnknownNamesIgnored(t *testing.T) {
	reg := newTestRegistry("read", "edit", "bash")

	reg.Whitelist([]string{"read", "no-such-tool"})

	got := reg.Names()
	if len(got) != 1 || got[0] != "read" {
		t.Fatalf("Names() = %v, want [read]", got)
	}
}

// Duplicated entries in keep must not panic — Add panics on duplicate
// names, so a filter that re-added survivors would blow up on the
// second occurrence. Set semantics, deduped by construction.
func TestWhitelist_DuplicateKeepEntries(t *testing.T) {
	reg := newTestRegistry("read", "edit", "bash")

	reg.Whitelist([]string{"read", "read", "edit", "read"})

	got := reg.Names()
	if len(got) != 2 || got[0] != "edit" || got[1] != "read" {
		t.Fatalf("Names() = %v, want [edit read]", got)
	}
}

// Clear is the `-tools=none` sentinel's registry half: zero tools, and
// Wire() carries nothing (the request then has no `tools` field at all).
// Kept distinct from Whitelist's empty-keep no-op on purpose — the two
// contracts ("absent = full registry" vs "none = zero tools") must never
// collapse into each other.
func TestClear_EmptiesRegistryAndWire(t *testing.T) {
	reg := newTestRegistry("read", "edit", "bash")

	reg.Clear()

	if got := reg.Names(); len(got) != 0 {
		t.Fatalf("Names() after Clear = %v, want empty", got)
	}
	if reg.Lookup("read") != nil {
		t.Error("Lookup(read) after Clear = non-nil, want nil")
	}
	if wire := reg.Wire(); len(wire) != 0 {
		t.Fatalf("Wire() after Clear = %v, want empty", wire)
	}
}

// Filtering down to a single tool and then Wiring must produce exactly
// one entry — the degenerate "bare core of one tool" lane.
func TestWhitelist_SingleSurvivor(t *testing.T) {
	reg := newTestRegistry("read", "edit", "bash")

	reg.Whitelist([]string{"bash"})

	wire := reg.Wire()
	if len(wire) != 1 || wire[0].Function.Name != "bash" {
		t.Fatalf("Wire() = %v, want exactly [bash]", wire)
	}
}
