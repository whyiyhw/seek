package plan

import (
	"encoding/json"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// proposeMsgPair builds the assistant tool_call + tool result pair for
// a propose() call. callID is the ToolCallID; approved=true uses the
// "[plan: approved]" marker that ReconstructFromTranscript scans for.
func proposeMsgPair(callID string, steps []string, approved bool) []deepseek.Message {
	args, _ := json.Marshal(struct {
		Problem string   `json:"problem"`
		Steps   []string `json:"steps"`
	}{Problem: "p", Steps: steps})

	result := "[plan: adjust requested]\nuser ..."
	if approved {
		result = "[plan: approved]\nProposal accepted by user."
	}

	return []deepseek.Message{
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{{
				ID:       callID,
				Function: deepseek.ToolCallFunc{Name: "propose", Arguments: string(args)},
			}},
		},
		{
			Role:       deepseek.RoleTool,
			Name:       "propose",
			ToolCallID: callID,
			Content:    result,
		},
	}
}

// planMsgPair builds the assistant tool_call + tool result pair for a
// plan(action, index) call.
func planMsgPair(callID, action string, index int) []deepseek.Message {
	args, _ := json.Marshal(struct {
		Action string `json:"action"`
		Index  int    `json:"index"`
	}{Action: action, Index: index})
	return []deepseek.Message{
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{{
				ID:       callID,
				Function: deepseek.ToolCallFunc{Name: toolName, Arguments: string(args)},
			}},
		},
		{
			Role:       deepseek.RoleTool,
			Name:       toolName,
			ToolCallID: callID,
			Content:    "[plan: ... ]",
		},
	}
}

func TestReconstruct_EmptyTranscript(t *testing.T) {
	t.Parallel()
	steps, cur := ReconstructFromTranscript(nil)
	if steps != nil || cur != -1 {
		t.Fatalf("got (%v, %d), want (nil, -1)", steps, cur)
	}
}

func TestReconstruct_NoApprovalReturnsNil(t *testing.T) {
	t.Parallel()
	msgs := proposeMsgPair("c1", []string{"a", "b"}, false)
	steps, cur := ReconstructFromTranscript(msgs)
	if steps != nil || cur != -1 {
		t.Fatalf("got (%v, %d), want (nil, -1) for rejected proposal", steps, cur)
	}
}

func TestReconstruct_ApprovedAllPending(t *testing.T) {
	t.Parallel()
	msgs := proposeMsgPair("c1", []string{"alpha", "beta", "gamma"}, true)
	steps, cur := ReconstructFromTranscript(msgs)
	if len(steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(steps))
	}
	if cur != -1 {
		t.Fatalf("cur = %d, want -1", cur)
	}
	for i, s := range steps {
		if s.Status != StatusPending {
			t.Fatalf("step %d status = %q, want pending", i, s.Status)
		}
		want := []string{"alpha", "beta", "gamma"}[i]
		if s.Text != want {
			t.Fatalf("step %d text = %q, want %q", i, s.Text, want)
		}
	}
}

func TestReconstruct_ApprovedThenStartAndComplete(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPair("c1", []string{"a", "b", "c"}, true)...)
	msgs = append(msgs, planMsgPair("c2", "start", 1)...)
	msgs = append(msgs, planMsgPair("c3", "complete", 1)...)
	msgs = append(msgs, planMsgPair("c4", "start", 2)...)

	steps, cur := ReconstructFromTranscript(msgs)
	if cur != 1 {
		t.Fatalf("cur = %d, want 1 (step 2 in progress)", cur)
	}
	if steps[0].Status != StatusCompleted {
		t.Fatalf("step 0 = %q, want completed", steps[0].Status)
	}
	if steps[1].Status != StatusInProgress {
		t.Fatalf("step 1 = %q, want in_progress", steps[1].Status)
	}
	if steps[2].Status != StatusPending {
		t.Fatalf("step 2 = %q, want pending", steps[2].Status)
	}
}

func TestReconstruct_MultipleApprovalsKeepLatest(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	// First approval — superseded.
	msgs = append(msgs, proposeMsgPair("c1", []string{"old1", "old2"}, true)...)
	msgs = append(msgs, planMsgPair("c2", "complete", 1)...)
	// Second approval re-seeds with different steps.
	msgs = append(msgs, proposeMsgPair("c3", []string{"new1", "new2", "new3"}, true)...)
	msgs = append(msgs, planMsgPair("c4", "start", 2)...)

	steps, cur := ReconstructFromTranscript(msgs)
	if len(steps) != 3 {
		t.Fatalf("len = %d, want 3 (post-reseed)", len(steps))
	}
	if steps[0].Text != "new1" {
		t.Fatalf("step 0 text = %q, want new1", steps[0].Text)
	}
	if cur != 1 || steps[1].Status != StatusInProgress {
		t.Fatalf("expected step 2 in_progress, got cur=%d status=%q", cur, steps[1].Status)
	}
	// Steps from the old plan must NOT leak: step 0 should not be completed.
	if steps[0].Status != StatusPending {
		t.Fatalf("step 0 should be pending after re-seed, got %q", steps[0].Status)
	}
}

func TestReconstruct_AdjustBetweenApprovals(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPair("c1", []string{"a", "b"}, true)...)
	msgs = append(msgs, planMsgPair("c2", "complete", 1)...)
	// User adjusts mid-execute → assistant re-proposes (NOT approved).
	msgs = append(msgs, proposeMsgPair("c3", []string{"x", "y"}, false)...)
	// Adjust comes back, assistant re-proposes again, approved this time.
	msgs = append(msgs, proposeMsgPair("c4", []string{"x2", "y2"}, true)...)

	steps, cur := ReconstructFromTranscript(msgs)
	if len(steps) != 2 || steps[0].Text != "x2" {
		t.Fatalf("expected x2/y2 steps, got %+v", steps)
	}
	if cur != -1 {
		t.Fatalf("cur should reset after re-approval, got %d", cur)
	}
}

func TestReconstruct_CorruptedPlanArgsSkipped(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPair("c1", []string{"a", "b"}, true)...)
	// Inject a malformed plan tool_call: invalid JSON args.
	msgs = append(msgs,
		deepseek.Message{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{{
				ID:       "c2",
				Function: deepseek.ToolCallFunc{Name: toolName, Arguments: `{"action":}`}, // bad JSON
			}},
		},
		deepseek.Message{Role: deepseek.RoleTool, Name: toolName, ToolCallID: "c2", Content: "err"},
	)
	msgs = append(msgs, planMsgPair("c3", "complete", 1)...)

	steps, _ := ReconstructFromTranscript(msgs)
	if steps[0].Status != StatusCompleted {
		t.Fatalf("step 0 should still be completed despite corrupt earlier call, got %q", steps[0].Status)
	}
}

func TestReconstruct_OutOfRangeIndexSkipped(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPair("c1", []string{"a"}, true)...)
	msgs = append(msgs, planMsgPair("c2", "complete", 99)...) // out of range
	msgs = append(msgs, planMsgPair("c3", "complete", 1)...)

	steps, _ := ReconstructFromTranscript(msgs)
	if steps[0].Status != StatusCompleted {
		t.Fatalf("step 0 = %q, want completed (in-range call should still take)", steps[0].Status)
	}
}

func TestReconstruct_StartReplacesPreviousInProgress(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPair("c1", []string{"a", "b"}, true)...)
	msgs = append(msgs, planMsgPair("c2", "start", 1)...)
	msgs = append(msgs, planMsgPair("c3", "start", 2)...) // no complete on step 1

	steps, cur := ReconstructFromTranscript(msgs)
	if cur != 1 {
		t.Fatalf("cur = %d, want 1", cur)
	}
	if steps[0].Status != StatusPending {
		t.Fatalf("step 0 = %q, want pending (was demoted)", steps[0].Status)
	}
	if steps[1].Status != StatusInProgress {
		t.Fatalf("step 1 = %q, want in_progress", steps[1].Status)
	}
}

func TestRestore_PreservesStatuses(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Restore([]Step{
		{Text: "x", Status: StatusCompleted},
		{Text: "y", Status: StatusInProgress},
		{Text: "z", Status: StatusPending},
	}, 1)
	steps, cur := tool.Snapshot()
	if cur != 1 {
		t.Fatalf("cur = %d, want 1", cur)
	}
	if steps[0].Status != StatusCompleted || steps[1].Status != StatusInProgress || steps[2].Status != StatusPending {
		t.Fatalf("statuses not preserved: %+v", steps)
	}
}

func TestRestore_ClampsBadCurrentIdx(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Restore([]Step{{Text: "x", Status: StatusPending}}, 99)
	_, cur := tool.Snapshot()
	if cur != -1 {
		t.Fatalf("cur = %d, want -1 (clamped)", cur)
	}
}

func TestRestore_EmptyClears(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	tool.Seed([]string{"a", "b"})
	tool.Restore(nil, -1)
	steps, cur := tool.Snapshot()
	if steps != nil || cur != -1 {
		t.Fatalf("got (%v, %d), want (nil, -1)", steps, cur)
	}
}
