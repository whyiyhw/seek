### DeepSeek rejects assistant messages with neither `content` nor `tool_calls`
- **Saw**: a turn that streamed only `reasoning_content` broke every later call with `invalid_request_error: Invalid assistant message: content or tool_calls must be set`.
- **Why**: `runTurnDeepSeek`/`runTurnLLM` build `assistant := Message{Role: RoleAssistant}` and stream into it; a turn ending before `[DONE]` leaves `Content=""`, `ToolCalls=nil`, so after `StripReasoningContent` the message is role=assistant with no fields.
- **Fix**: empty-response guard in both: if `assistant.Content == "" && len(assistant.ToolCalls) == 0`, return an error instead of the message; the Prompt loop drops the turn; `TestAgent_EmptyChoicesUsageOnly` updated to expect the error.
- **Lesson**: a reasoning-only stream is not a valid assistant turn — guard both provider paths; "model said nothing" poisons subsequent requests.
- **Refs**: `pkg/agent/agent.go:runTurnDeepSeek`, `pkg/agent/agent.go:runTurnLLM`, `pkg/agent/agent_test.go:TestAgent_EmptyChoicesUsageOnly`
