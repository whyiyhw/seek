package main

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/acp"
)

// TestSubagentProgressPump_CoalescesAndOrders pins the condensed-progress
// behaviour paseo relies on: token deltas are coalesced into lines, a tool
// marker flushes pending text BEFORE the marker (so the reasoning region
// reads in execution order), and a final flush drains the tail.
func TestSubagentProgressPump_CoalescesAndOrders(t *testing.T) {
	var got []subagentProgress
	sink := func(sp subagentProgress) { got = append(got, sp) }
	ctx := withSubagentProgress(context.Background(), sink)
	p := newSubagentProgressPump(ctx, "sub-1")

	p.write("first line\n")
	p.write("second ")
	p.write("partial")
	p.note("→ grep")
	p.write("tail text")
	p.flush()

	want := []string{
		"first line",
		"second partial",
		"→ grep",
		"tail text",
	}
	if len(got) != len(want) {
		t.Fatalf("progress lines = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].SubSid != "sub-1" {
			t.Errorf("line %d sub-sid = %q, want sub-1", i, got[i].SubSid)
		}
		if got[i].Text != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i].Text, want[i])
		}
	}
}

// TestSubagentProgressPump_ChunkBoundary guards against an unbounded live
// buffer: enough deltas past the soft threshold must flush without a newline.
func TestSubagentProgressPump_ChunkBoundary(t *testing.T) {
	var got []subagentProgress
	ctx := withSubagentProgress(context.Background(), func(sp subagentProgress) { got = append(got, sp) })
	p := newSubagentProgressPump(ctx, "s")
	for i := 0; i < 6; i++ {
		p.write(strings.Repeat("x", 60)) // 360 bytes total > 2× the 160 threshold
	}
	if len(got) < 2 {
		t.Fatalf("chunk boundary did not flush early: %d lines", len(got))
	}
	p.flush()
}

// TestSubagentProgressPump_UTF8Boundary guards the chunk flush against
// cutting a multi-byte rune (Chinese is 3 bytes) — a split would show as
// U+FFFD replacement chars in paseo's reasoning region.
func TestSubagentProgressPump_UTF8Boundary(t *testing.T) {
	var got []subagentProgress
	ctx := withSubagentProgress(context.Background(), func(sp subagentProgress) { got = append(got, sp) })
	p := newSubagentProgressPump(ctx, "s")
	// "过程说明" = 4 Chinese runes = 12 bytes. Repeated past the 160-byte
	// threshold the flush must land on a rune boundary.
	unit := "过程说明"
	for i := 0; i < 20; i++ {
		p.write(unit)
	}
	p.flush()
	for _, line := range got {
		if !utf8.ValidString(line.Text) {
			t.Fatalf("invalid UTF-8 in progress line %q", line.Text)
		}
	}
	if len(got) == 0 {
		t.Fatal("no progress lines emitted")
	}
}

// TestSubagentProgressPump_NilSinkNoop is the non-ACP-lane guarantee: with no
// sink installed the pump never emits and never panics, so TUI / print / rpc
// lanes stay byte-for-byte unchanged.
func TestSubagentProgressPump_NilSinkNoop(t *testing.T) {
	p := newSubagentProgressPump(context.Background(), "s")
	p.write("anything\nmore")
	p.note("→ bash")
	p.flush()
	if p.sink != nil {
		t.Fatal("nil ctx must install a nil sink")
	}
}

// TestWithSubagentProgress_Isolation: a sink installed for one turn's ctx is
// not visible through an unrelated ctx (no cross-turn / cross-session leak).
func TestWithSubagentProgress_Isolation(t *testing.T) {
	called := false
	withSink := withSubagentProgress(context.Background(), func(subagentProgress) { called = true })
	if subagentProgressFrom(context.Background()) != nil {
		t.Fatal("bare ctx unexpectedly has a progress sink")
	}
	if subagentProgressFrom(withSink) == nil {
		t.Fatal("wrapped ctx lost its progress sink")
	}
	subagentProgressFrom(withSink)(subagentProgress{SubSid: "s", Text: "x"})
	if !called {
		t.Fatal("installed sink did not fire")
	}
}

// TestACPProgressSink_CancelledTurnDropsLateFlush: the cancellation path.
// After the turn ctx is cancelled, a late flush must emit NO frame — a frame
// after the termination/response breaks paseo's frame-before-response
// invariant. Also asserts the standard agent_thought_chunk shape + the
// parent's session id on the wire.
func TestACPProgressSink_CancelledTurnDropsLateFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ups []acp.SessionUpdate
	sink := newACPProgressSink(ctx, "parent-session", func(u acp.SessionUpdate) { ups = append(ups, u) })

	sink(subagentProgress{SubSid: "sub-1", Text: "live"})
	if len(ups) != 1 {
		t.Fatalf("live progress frames = %d, want 1", len(ups))
	}
	m, ok := ups[0].Update.(map[string]any)
	if !ok || m["sessionUpdate"] != "agent_thought_chunk" || ups[0].SessionID != "parent-session" {
		t.Fatalf("frame = %+v, want agent_thought_chunk on parent-session", ups[0])
	}
	if c, ok := m["content"].(map[string]any); !ok || !strings.Contains(c["text"].(string), "sub-1") {
		t.Fatalf("frame content = %v, want sub-sid attribution", m["content"])
	}

	cancel()
	sink(subagentProgress{SubSid: "sub-1", Text: "late"})
	if len(ups) != 1 {
		t.Fatalf("late frame after cancel = %+v, want none", ups[1:])
	}
}
