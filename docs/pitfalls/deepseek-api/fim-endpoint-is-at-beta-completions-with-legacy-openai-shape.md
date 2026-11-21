### FIM endpoint is at `/beta/completions` with legacy OpenAI shape
- **Saw**: when wiring the FIM client I tried to send it to `/chat/completions` with a `prompt` field — got 400 back
- **Why**: DeepSeek's fill-in-the-middle lives at `/beta/completions` (note the path) and uses the **legacy OpenAI text-completion schema**: `{prompt, suffix, ...}` → `{choices[0].text, ...}` — not the chat/messages shape
- **Fix**: `pkg/deepseek/fim.go` uses dedicated `FIMRequest` / `FIMResponse` types and the right endpoint. Commit `2af3b62`
- **Lesson**: don't assume an OpenAI-compatible provider's auxiliary endpoints follow the chat shape. Read the docs end to end before extending the client
- **Refs**: `pkg/deepseek/fim.go`, `pkg/deepseek/client.go:endpointFIM`
