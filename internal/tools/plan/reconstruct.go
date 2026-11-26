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
//     the active plan reflects the user's last decision. The
//     "(auto-approve-per-step)" variant tag that propose appends
//     AFTER the closing bracket is also read: it is the transcript's
//     only record of the approval mode, and dropping it (prefix match
//     only) permanently disarmed the "plan(start) re-arms the gate"
//     contract after `-resume` (c_n2k6qi §6.2).
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
	"slices"
	"strings"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

const (
	approvedMarker = "[plan: approved]"
	// batchTag is propose's variant tag for an "auto-approve per step"
	// approval. It is appended AFTER the closing bracket — the marker
	// itself stays byte-identical so the prefix scan above still finds
	// the seeding point.
	batchTag = "(auto-approve-per-step)"
)

// batchApproval reports whether an approved propose result carries the
// auto-approve-per-step tag. This is the transcript's ONLY record of
// the approval mode: the runtime flag (planBridge.batch, which drives
// policy.preApproved on plan(start)) is not persisted anywhere, so a
// prefix-only read silently downgraded every resumed batch plan to
// per-call y/N and the "plan(start) re-arms the gate" contract the
// result text teaches the model stopped holding (c_n2k6qi §6.2).
func batchApproval(content string) bool {
	rest := strings.TrimPrefix(content, approvedMarker)
	rest = strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(rest, batchTag)
}

// toolNameAt resolves the tool name for the tool-role message at
// index i. Persisted tool messages carry NO `name` field: on the
// DeepSeek/OpenAI wire the name lives only in the assistant
// tool_call (keyed by tool_call_id), and Message.Name is omitempty —
// the agent loop leaves it empty when appending tool results, so the
// saved JSONL rows have content+tool_call_id only. Matching on
// m.Name alone therefore missed every real transcript (found by the
// plan-e2e eval lane: resume always answered "no active plan" while
// unit fixtures stayed green because they hand-filled Name). When
// Name is blank we recover it from the matching assistant call.
func toolNameAt(msgs []deepseek.Message, i int) string {
	if msgs[i].Name != "" {
		return msgs[i].Name
	}
	id := msgs[i].ToolCallID
	for j := i - 1; j >= 0; j-- {
		for _, tc := range msgs[j].ToolCalls {
			if tc.ID == id {
				return tc.Function.Name
			}
		}
	}
	return ""
}

// ReconstructFromTranscript scans the given messages and returns the
// plan state implied by the most recent approval and any subsequent
// plan tool calls. Returns (nil, -1, false) when no approved plan is
// present — callers should treat that as "no active plan, do not seed".
//
// The batch flag reports whether the governing approval was the
// auto-approve-per-step variant. It is part of the return value rather
// than a separate query so a caller cannot seed the steps and forget
// the approval mode — the exact half-restore that broke the re-arm
// contract after `-resume` (c_n2k6qi §6.2).
func ReconstructFromTranscript(msgs []deepseek.Message) ([]Step, int, bool) {
	// Pass 1: find the index of the last "[plan: approved]" tool
	// message. Iterate from the tail so we land on the most recent
	// approval if multiple exist (re-propose flow).
	approvalIdx := -1
	for i, m := range slices.Backward(msgs) {
		if m.Role != deepseek.RoleTool {
			continue
		}
		if toolNameAt(msgs, i) != "propose" || !strings.HasPrefix(m.Content, approvedMarker) {
			continue
		}
		approvalIdx = i
		break
	}
	if approvalIdx < 0 {
		return nil, -1, false
	}
	batch := batchApproval(msgs[approvalIdx].Content)

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
		return nil, -1, false
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
		if m.Role != deepseek.RoleTool || toolNameAt(msgs, i) != toolName {
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

	return steps, currentIdx, batch
}

// extractProposeSteps walks the assistant messages in msgs looking
// for a tool_call with ID == callID and Function.Name == "propose";
// parses its arguments JSON and returns the steps slice. Returns nil
// on any miss / malformed args.
func extractProposeSteps(msgs []deepseek.Message, callID string) []string {
	for _, m := range slices.Backward(msgs) {
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
	for _, m := range slices.Backward(msgs) {
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
