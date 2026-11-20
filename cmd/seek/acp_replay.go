// acp_replay.go — the transcript replayer behind session/load.
//
// ACP's `session/load` contract is not just "accept a session id": the
// agent must "stream the entire conversation history back to the client
// via notifications" before the response resolves. Clients (paseo,
// Zed) render exactly the updates a live turn would have produced, so
// the replay has to speak the SAME update vocabulary — user/assistant
// message chunks plus tool_call / tool_call_update pairs — rather than
// inventing a summary shape of its own.
//
// The function below is pure: messages in, updates out, no I/O. That
// keeps the interesting part (which role maps to which update, how tool
// results pair back to their calls) table-testable without a live
// client or an agent.
package main

import (
	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// acpReplayUpdates converts a session's stored messages into the
// session/update sequence a client would have received live.
//
// Mapping (deliberately narrow — the transcript carries more state than a
// client can render):
//
//   - system        → skipped. The system prompt is seek's, and it is
//     re-composed for THIS process; replaying a previous run's copy would
//     show the model a prompt it never sees now.
//   - user          → user_message_chunk with the text as stored. Marker
//     blocks ("[image: …", "[file: …") ride along as plain text: the
//     client's job is to show what was said, not to re-attach bytes.
//   - assistant     → agent_message_chunk for Content (reasoning_content
//     is not persisted into history by the agent, so there is nothing to
//     put on the thought channel), then one tool_call per ToolCall with
//     status completed — by definition anything in a saved transcript
//     already ran.
//   - tool          → tool_call_update for that CallID carrying
//     rawOutput; the call itself was announced by the preceding
//     assistant message.
//
// Empty content is skipped rather than emitted as an empty chunk: some
// clients create a new message bubble per chunk, and an empty one
// renders as a blank row.
func acpReplayUpdates(sessionID string, msgs []deepseek.Message, cwd string) []acp.SessionUpdate {
	var out []acp.SessionUpdate
	// toolNames maps a CallID to the tool name, so the closing
	// tool_call_update carries `name` too — clients that index by name
	// (rather than by id) would otherwise render a nameless row.
	toolNames := map[string]string{}

	emit := func(update map[string]any) {
		out = append(out, acp.SessionUpdate{SessionID: sessionID, Update: update})
	}

	for _, m := range msgs {
		switch m.Role {
		case deepseek.RoleSystem:
			continue

		case deepseek.RoleUser:
			if m.Content == "" {
				continue
			}
			emit(map[string]any{
				"sessionUpdate": "user_message_chunk",
				"content":       map[string]any{"type": "text", "text": m.Content},
			})

		case deepseek.RoleAssistant:
			if m.Content != "" {
				emit(map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"type": "text", "text": m.Content},
				})
			}
			for _, tc := range m.ToolCalls {
				toolNames[tc.ID] = tc.Function.Name
				u := map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    tc.ID,
					"title":         acpToolTitle(tc.Function.Name, tc.Function.Arguments),
					"name":          tc.Function.Name,
					"kind":          acpToolKind(tc.Function.Name),
					"status":        "completed",
				}
				if locs := acpToolLocations(tc.Function.Arguments, cwd); locs != nil {
					u["locations"] = locs
				}
				if tc.Function.Arguments != "" {
					u["rawInput"] = acpRawValue(acpTruncate(tc.Function.Arguments, acpRawCap))
				}
				emit(u)
			}

		case deepseek.RoleTool:
			u := map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    m.ToolCallID,
				"status":        "completed",
			}
			if name := toolNames[m.ToolCallID]; name != "" {
				u["name"] = name
			}
			if m.Content != "" {
				u["rawOutput"] = acpRawValue(acpTruncate(m.Content, acpRawCap))
			}
			emit(u)
		}
	}
	return out
}
