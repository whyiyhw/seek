package main

import (
	"encoding/json"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
	plantool "github.com/whyiyhw/seek/internal/tools/plan"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// planbridge_test.go — the resume half of the c_n2k6qi §6.2 batch gap.
//
// reconstruct now reports the approval mode it read from the transcript
// marker suffix; these tests pin the consumer: RestoreApprovalMode must
// put the mode back on the bridge BEFORE planTool.Restore, so the single
// StepChanged path re-arms preApproved when a step is still in flight —
// the "plan(start) re-arms the gate" contract propose teaches the model.

// planBridgeFixture wires a bridge the way cmd/seek does on resume:
// PrefAsk + PlanExecute (post-approval), a real plan tool, and an agent
// whose event channel is nil (EmitEvent no-ops outside a Prompt).
func planBridgeFixture(t *testing.T) (*planBridge, *permission.Policy, *plantool.Tool) {
	t.Helper()
	ag, err := agent.New(agent.Config{
		Client: deepseek.New(deepseek.WithAPIKey("t")),
		Model:  deepseek.ModelV41Flash,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	pol, err := permission.New(t.TempDir(), permission.PrefAsk)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	pol.SetWorkflow(permission.WorkflowPlanExecute)

	tool := plantool.New(nil)
	bridge := &planBridge{
		getAgent: func() *agent.Agent { return ag },
		policy:   pol,
		planTool: tool,
	}
	// Re-create the tool with the bridge as sink so Restore/StepChanged
	// flow through the production path (the constructor takes the sink).
	tool = plantool.New(bridge)
	bridge.planTool = tool
	return bridge, pol, tool
}

// TestRestoreApprovalMode_ReArmsPreApproved is the end-to-end resume
// assertion (acceptance ①): a transcript whose governing approval was
// the auto-approve-per-step variant, with a step still in progress,
// must leave policy.preApproved armed after the resume sequence.
func TestRestoreApprovalMode_ReArmsPreApproved(t *testing.T) {
	var msgs []deepseek.Message
	msgs = append(msgs, resumeProposePair("c1", []string{"a", "b"}, true)...)
	msgs = append(msgs, resumePlanPair("c2", "start", 2)...)

	bridge, pol, tool := planBridgeFixture(t)

	steps, cur, batch := plantool.ReconstructFromTranscript(msgs)
	if len(steps) == 0 {
		t.Fatalf("reconstruct found no plan")
	}
	bridge.RestoreApprovalMode(batch)
	tool.Restore(steps, cur)

	if !pol.PreApproved() {
		t.Fatalf("preApproved = false after resuming a batch plan with step 2 in flight; the re-arm contract is broken (the bug this pins)")
	}

	// The next plan(start) must keep the gate armed, not disarm it.
	if _, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{"action": "start", "index": 2})); err != nil {
		t.Fatalf("plan(start) after resume: %v", err)
	}
	if !pol.PreApproved() {
		t.Fatalf("preApproved = false after plan(start) on a resumed batch plan")
	}
	// plan(complete) disarms, same as the live path.
	if _, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{"action": "complete", "index": 2})); err != nil {
		t.Fatalf("plan(complete) after resume: %v", err)
	}
	if pol.PreApproved() {
		t.Fatalf("preApproved = true after plan(complete); the gate must disarm")
	}
}

// TestRestoreApprovalMode_PlainApprovalStaysDisarmed is the contrast
// case (acceptance ①): a plain "[plan: approved]" transcript (no
// suffix) must behave exactly as before the fix — no re-arm, no gate.
func TestRestoreApprovalMode_PlainApprovalStaysDisarmed(t *testing.T) {
	var msgs []deepseek.Message
	msgs = append(msgs, resumeProposePair("c1", []string{"a", "b"}, false)...)
	msgs = append(msgs, resumePlanPair("c2", "start", 2)...)

	bridge, pol, tool := planBridgeFixture(t)

	steps, cur, batch := plantool.ReconstructFromTranscript(msgs)
	bridge.RestoreApprovalMode(batch)
	tool.Restore(steps, cur)

	if batch {
		t.Fatalf("reconstruct reported batch=true for a plain approval")
	}
	if pol.PreApproved() {
		t.Fatalf("preApproved = true for a per-call approval; that would silently loosen the gate")
	}
	// plan(start) after resume stays disarmed on the plain path.
	if _, err := tool.Execute(t.Context(), mustJSON(t, map[string]any{"action": "start", "index": 2})); err != nil {
		t.Fatalf("plan(start): %v", err)
	}
	if pol.PreApproved() {
		t.Fatalf("preApproved = true after plan(start) on a plain resumed plan")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// resumeProposePair / resumePlanPair mirror the persisted transcript
// shape: tool rows carry no `name` (Message.Name is omitempty), the
// tool name lives in the assistant tool_call — the exact shape that
// once made resume answer "no active plan".
func resumeProposePair(callID string, steps []string, batch bool) []deepseek.Message {
	args, _ := json.Marshal(struct {
		Problem string   `json:"problem"`
		Steps   []string `json:"steps"`
	}{Problem: "p", Steps: steps})
	result := "[plan: approved]\nProposal accepted by user."
	if batch {
		result = "[plan: approved] (auto-approve-per-step)\nProposal accepted by user with auto-approve-per-step."
	}
	return []deepseek.Message{
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{{
				ID:       callID,
				Function: deepseek.ToolCallFunc{Name: "propose", Arguments: string(args)},
			}},
		},
		{Role: deepseek.RoleTool, ToolCallID: callID, Content: result},
	}
}

func resumePlanPair(callID, action string, index int) []deepseek.Message {
	args, _ := json.Marshal(struct {
		Action string `json:"action"`
		Index  int    `json:"index"`
	}{Action: action, Index: index})
	return []deepseek.Message{
		{
			Role: deepseek.RoleAssistant,
			ToolCalls: []deepseek.ToolCall{{
				ID:       callID,
				Function: deepseek.ToolCallFunc{Name: "plan", Arguments: string(args)},
			}},
		},
		{Role: deepseek.RoleTool, ToolCallID: callID, Content: "[plan: ...]"},
	}
}
