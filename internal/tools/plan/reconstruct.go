// Reconstruction of plan state from a session transcript.
//
// Why event-sourcing instead of a session field. PlanState is bounded
// (≤ N steps × ≤ 200 chars), but the transcript is the single source
// of truth for everything else in the session — adding a sibling
// field would invite drift the first time we forget to update both.
// Replaying is cheap (one linear scan, O(messages)) and lets the
// session JSONL schema stay frozen at v2.
//
// What we look for:
//
//  1. The latest `tool` message whose Name == "propose" and whose
//     Content starts with "[plan: approved]" — that's the seeding
//     point. Everything before it is from earlier proposals (adjust
//     loops, prior `/plan` runs) and is intentionally discarded so
//     the active plan reflects the user's last decision.
//
//  2. The matching assistant tool_call (matched by ToolCallID) whose
//     function arguments JSON carries the steps array. The result
//     string DOES echo steps as text, but parsing the args is more
//     robust against future result-format tweaks.
//
//  3. Every `tool` message whose Name == "plan" that appears AFTER
//     the propose result, in transcript order. Each one's matching
//     assistant tool_call gives us {action, index}; we apply the
//     state mutation. Corrupt or unknown actions are skipped (no
//     panic) so a partially-corrupt transcript still produces a
//     usable state — same defensive posture as session.Repair.

package plan

import (
	"encoding/json"
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

const (
	approvedMarker = "[plan: approved]"
)

// ReconstructFromTranscript scans the given messages and returns the
// plan state implied by the most recent approval and any subsequent
// plan tool calls. Returns (nil, -1) when no approved plan is present
// — callers should treat that as "no active plan, do not seed".
func ReconstructFromTranscript(msgs []deepseek.Message) ([]Step, int) {
	// Pass 1: find the index of the last "[plan: approved]" tool
	// message. Iterate from the tail so we land on the most recent
	// approval if multiple exist (re-propose flow).
	approvalIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == deepseek.RoleTool && m.Name == "propose" && strings.HasPrefix(m.Content, approvedMarker) {
			approvalIdx = i
			break
		}
	}
	if approvalIdx < 0 {
		return nil, -1
	}

	// Pass 2: locate the matching propose tool_call to extract the
	// approved steps. The matching call is in an earlier assistant
	// message whose ToolCalls contains an entry with ID equal to the
	// tool result's ToolCallID.
	approvalCallID := msgs[approvalIdx].ToolCallID
	stepTexts := extractProposeSteps(msgs[:approvalIdx], approvalCallID)
	if len(stepTexts) == 0 {
		// Approval result exists but we couldn't recover the steps —
		// can't render a meaningful task list without them. Treat as
		// "no usable plan" so the TUI stays clean rather than
		// rendering empty rows.
		return nil, -1
	}

	steps := make([]Step, len(stepTexts))
	for i, txt := range stepTexts {
		steps[i] = Step{Text: txt, Status: StatusPending}
	}
	currentIdx := -1

	// Pass 3: apply every plan tool call that follows the approval.
	// We match each plan tool result to its assistant call (same
	// ToolCallID lookup) to parse {action, index}. The result content
	// is human-readable — parsing args is more reliable.
	for i := approvalIdx + 1; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role != deepseek.RoleTool || m.Name != toolName {
			continue
		}
		action, idx, ok := extractPlanCall(msgs[:i], m.ToolCallID)
		if !ok {
			continue
		}
		if idx < 1 || idx > len(steps) {
			continue
		}
		stepIdx := idx - 1
		switch action {
		case "start":
			// Demote any previously in-progress step.
			if currentIdx >= 0 && currentIdx != stepIdx && steps[currentIdx].Status == StatusInProgress {
				steps[currentIdx].Status = StatusPending
			}
			steps[stepIdx].Status = StatusInProgress
			currentIdx = stepIdx
		case "complete":
			steps[stepIdx].Status = StatusCompleted
			if currentIdx == stepIdx {
				currentIdx = -1
			}
		case "skip":
			steps[stepIdx].Status = StatusSkipped
			if currentIdx == stepIdx {
				currentIdx = -1
			}
		default:
			// Unknown action — skip (defensive against future tool
			// versions or corruption).
		}
	}

	return steps, currentIdx
}

// extractProposeSteps walks the assistant messages in msgs looking
// for a tool_call with ID == callID and Function.Name == "propose";
// parses its arguments JSON and returns the steps slice. Returns nil
// on any miss / malformed args.
func extractProposeSteps(msgs []deepseek.Message, callID string) []string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != deepseek.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != callID || tc.Function.Name != "propose" {
				continue
			}
			var args struct {
				Steps []string `json:"steps"`
			}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil
			}
			return args.Steps
		}
	}
	return nil
}

// extractPlanCall walks the assistant messages in msgs looking for a
// tool_call with ID == callID and Function.Name == "plan"; parses
// {action, index}. Returns (_, _, false) on any miss / malformed args.
func extractPlanCall(msgs []deepseek.Message, callID string) (string, int, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != deepseek.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != callID || tc.Function.Name != toolName {
				continue
			}
			var a struct {
				Action string `json:"action"`
				Index  int    `json:"index"`
			}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &a); err != nil {
				return "", 0, false
			}
			return a.Action, a.Index, true
		}
	}
	return "", 0, false
}
