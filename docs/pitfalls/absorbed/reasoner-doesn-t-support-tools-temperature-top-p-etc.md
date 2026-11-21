<!-- pitfalls-absorbed: original-section: - -->
### [已吸收] Reasoner doesn't support `tools`, `temperature`, `top_p`, etc.
- **Saw**: a request to `deepseek-reasoner` with a `tools` array came back 400
- **Why**: reasoner is configured as a pure CoT-then-answer model; tool calling, sampling parameters, function calling, and parallel tool calls are all unsupported
- **Fix**: the `think` tool calls reasoner via a fresh, parameter-free `Chat()` call (no tools, no temperature) using a history of just `[system, user]`. Commit `8264f52`
- **Lesson**: a "model" in the API can be a parameter-restricted variant. Maintain a per-model capability map if you switch between them programmatically
- **Refs**: `internal/tools/think/think.go`
- **已吸收**: the legacy `deepseek-reasoner` alias this entry constrains sunset 2026-07-24; think now reasons via the caller's current model in thinking mode (`modelFunc`), no fixed reasoner call → entries "`deepseek-reasoner` without `Thinking`…" and "legacy aliases sunset 2026-07-24" below; `internal/tools/think/think.go` header
