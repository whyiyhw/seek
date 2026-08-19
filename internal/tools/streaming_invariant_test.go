// Package tools_test hosts cross-package marker-protocol invariants.
// External test package on purpose: the concrete tool packages import
// internal/tools, so an internal test file could not import them
// without a cycle.
package tools_test

import (
	"testing"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/internal/tools/think"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// TestNoToolStreamsAndIsReadOnly pins the invariant the TUI's ToolDelta
// routing relies on: a tool may implement StreamingTool (per-delta live
// output) OR be marked ReadOnlyTool (concurrent dispatch), never both.
//
// The agent dispatches a tool batch partitioned — ReadOnlyTool calls
// run concurrently, everything else (including all streaming tools)
// runs on one sequential goroutine. The TUI routes ToolDelta into the
// live-region buffers it shares with chat streaming and clears them
// unconditionally at ToolExecEnd (internal/tui/update_agent.go), so a
// tool that streamed AND dispatched concurrently would interleave its
// deltas with siblings' and lose its live output to a sibling's end
// event. At most one delta writer at a time is guaranteed ONLY by
// keeping every StreamingTool off the concurrent side.
//
// When a new StreamingTool lands, add it to this table — and if it
// genuinely needs both, the TUI must move ToolDelta routing to a
// per-CallID live region first (see the NOTE in update_agent.go).
func TestNoToolStreamsAndIsReadOnly(t *testing.T) {
	ds := deepseek.New(deepseek.WithAPIKey("t"))
	streamingTools := []tools.Tool{
		think.New(ds, func() string { return "" }, nil),
	}
	for _, tool := range streamingTools {
		if _, streams := tool.(tools.StreamingTool); !streams {
			t.Errorf("%s: listed as streaming but does not implement StreamingTool", tool.Name())
			continue
		}
		if _, ro := tool.(tools.ReadOnlyTool); ro {
			t.Errorf("%s: implements BOTH StreamingTool and ReadOnlyTool — "+
				"it would stream from the concurrent dispatch side and corrupt the TUI live region; "+
				"either drop the ReadOnly marker or give the TUI per-CallID ToolDelta routing first",
				tool.Name())
		}
	}
}
