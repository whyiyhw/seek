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

// proposeMsgPairBatch is proposeMsgPair with the auto-approve-per-step
// approval variant — the exact bytes propose.approveResult emits for
// the "approve_batch" pick: "[plan: approved] (auto-approve-per-step)"
// followed by the re-arm instruction paragraph.
func proposeMsgPairBatch(callID string, steps []string) []deepseek.Message {
	pair := proposeMsgPair(callID, steps, true)
	pair[1].Content = "[plan: approved] (auto-approve-per-step)\n" +
		"Proposal accepted by user with auto-approve-per-step. You are now in plan-execute substate."
	return pair
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
	steps, cur, batch := ReconstructFromTranscript(nil)
	if steps != nil || cur != -1 || batch {
		t.Fatalf("got (%v, %d, %v), want (nil, -1, false)", steps, cur, batch)
	}
}

func TestReconstruct_NoApprovalReturnsNil(t *testing.T) {
	t.Parallel()
	msgs := proposeMsgPair("c1", []string{"a", "b"}, false)
	steps, cur, batch := ReconstructFromTranscript(msgs)
	if steps != nil || cur != -1 || batch {
		t.Fatalf("got (%v, %d, %v), want (nil, -1, false) for rejected proposal", steps, cur, batch)
	}
}

func TestReconstruct_ApprovedAllPending(t *testing.T) {
	t.Parallel()
	msgs := proposeMsgPair("c1", []string{"alpha", "beta", "gamma"}, true)
	steps, cur, batch := ReconstructFromTranscript(msgs)
	if len(steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(steps))
	}
	if cur != -1 {
		t.Fatalf("cur = %d, want -1", cur)
	}
	// Contrast for the batch-tag read: a plain "[plan: approved]" result
	// (no "(auto-approve-per-step)" suffix) must NOT report batch mode —
	// pre-existing sessions keep their exact previous behaviour.
	if batch {
		t.Fatalf("batch = true for a plain approval, want false")
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

	steps, cur, _ := ReconstructFromTranscript(msgs)
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

// TestReconstruct_PersistedWireShapeNoToolName is the real-transcript
// regression found by the plan-e2e eval lane (2026-11-21): saved tool
// rows carry NO `name` field (Message.Name is omitempty and empty at
// save time for tool results — the name lives in the assistant
// tool_call), so every Name==… gate above missed real transcripts and
// resume answered "no active plan" while these unit fixtures stayed
// green. Same chain as ApprovedThenStartAndComplete, Name blanked.
func TestReconstruct_PersistedWireShapeNoToolName(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPair("c1", []string{"a", "b"}, true)...)
	msgs = append(msgs, planMsgPair("c2", "start", 1)...)
	msgs = append(msgs, planMsgPair("c3", "complete", 1)...)
	// Blank the persisted names — exactly what Save writes to JSONL.
	for i := range msgs {
		if msgs[i].Role == deepseek.RoleTool {
			msgs[i].Name = ""
		}
	}

	steps, cur, _ := ReconstructFromTranscript(msgs)
	if len(steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2 (nil means approval not found — the bug this pins)", len(steps))
	}
	if steps[0].Status != StatusCompleted {
		t.Fatalf("step 0 = %q, want completed", steps[0].Status)
	}
	if steps[1].Status != StatusPending {
		t.Fatalf("step 1 = %q, want pending", steps[1].Status)
	}
	if cur != -1 {
		t.Fatalf("cur = %d, want -1", cur)
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

	steps, cur, _ := ReconstructFromTranscript(msgs)
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

	steps, cur, _ := ReconstructFromTranscript(msgs)
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

	steps, _, _ := ReconstructFromTranscript(msgs)
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

	steps, _, _ := ReconstructFromTranscript(msgs)
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

	steps, cur, _ := ReconstructFromTranscript(msgs)
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

// TestReconstruct_BatchApprovalTagRestored pins the c_n2k6qi §6.2
// resume gap: propose's auto-approve-per-step variant appends
// "(auto-approve-per-step)" AFTER the closing bracket, and a
// prefix-only read threw that away — so a resumed batch plan silently
// downgraded to per-call y/N (plan(start) disarmed instead of re-arming
// the per-step gate). The suffix is the transcript's only record of the
// approval mode; it must come back as the third return value.
func TestReconstruct_BatchApprovalTagRestored(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPairBatch("c1", []string{"a", "b"})...)
	msgs = append(msgs, planMsgPair("c2", "start", 2)...)

	steps, cur, batch := ReconstructFromTranscript(msgs)
	if !batch {
		t.Fatalf("batch = false for an auto-approve-per-step approval, want true — the suffix was dropped")
	}
	if len(steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(steps))
	}
	// The step list must reconstruct exactly as on the plain path.
	if cur != 1 || steps[1].Status != StatusInProgress || steps[0].Status != StatusPending {
		t.Fatalf("steps not reconstructed: cur=%d statuses=%q/%q", cur, steps[0].Status, steps[1].Status)
	}
	// And the tag must be read off the latest approval, not an earlier
	// one: a plain re-approval supersedes a batch approval.
}

// TestReconstruct_BatchTagReadFromLatestApproval is the supersede
// direction of the same read: a batch approval followed by a plain
// re-approval must report batch=false, otherwise resume would re-arm a
// gate the user explicitly declined the second time round.
func TestReconstruct_BatchTagReadFromLatestApproval(t *testing.T) {
	t.Parallel()
	var msgs []deepseek.Message
	msgs = append(msgs, proposeMsgPairBatch("c1", []string{"old"})...)
	msgs = append(msgs, planMsgPair("c2", "complete", 1)...)
	msgs = append(msgs, proposeMsgPair("c3", []string{"new1", "new2"}, true)...)

	steps, cur, batch := ReconstructFromTranscript(msgs)
	if batch {
		t.Fatalf("batch = true after a plain re-approval, want false")
	}
	if len(steps) != 2 || steps[0].Text != "new1" || cur != -1 {
		t.Fatalf("latest approval did not win: steps=%+v cur=%d", steps, cur)
	}
}

// TestReconstruct_BatchTagToleratesSpacing keeps the read robust to
// the harmless whitespace variation a hand-edited / future-formatted
// transcript could carry. The marker itself stays a strict prefix; only
// the separator before the tag is flexible.
func TestReconstruct_BatchTagToleratesSpacing(t *testing.T) {
	t.Parallel()
	msgs := proposeMsgPairBatch("c1", []string{"a"})
	msgs[1].Content = "[plan: approved]  (auto-approve-per-step)\nrest…"

	_, _, batch := ReconstructFromTranscript(msgs)
	if !batch {
		t.Fatalf("batch = false with doubled-space separator, want true")
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
