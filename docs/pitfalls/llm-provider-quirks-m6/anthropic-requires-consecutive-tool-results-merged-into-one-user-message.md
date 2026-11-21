### Anthropic requires consecutive tool results merged into ONE user message
- **Saw**: sending separate `role="user"` messages each containing a single `tool_result` block returned 400: "tool_result blocks must all appear in the same user message"
- **Why**: Anthropic's Messages API has a uniqueness constraint: a `tool_result` content block can only appear inside a user message, AND all `tool_result` blocks that respond to the same assistant turn must be in a single user message. Separate messages violate this even if they are logically sequential
- **Fix**: `buildRequest()` in `pkg/llm/provider/anthropic/client.go` scans ahead: when it sees a `role="tool"` message, it collects ALL consecutive `role="tool"` messages and folds them into one user message with a `[]contentBlock` array. Commit `a1fde05`
- **Lesson**: Anthropic's message format is not the same shape as OpenAI's even where they look similar. Tool results in particular have hard structural constraints — don't assume "one message per tool result" works
- **Refs**: `pkg/llm/provider/anthropic/client.go:buildRequest`
