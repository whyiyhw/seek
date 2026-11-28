### JSON object key order is data — unmarshalling into a map shuffles prompt bytes
- **Saw**: rendering MCP tool parameter signatures for the sysprompt MCP section, the obvious `json.Unmarshal` of the input schema into `map[string]…` properties would emit `search(repo, query, content?)` with parameters in random order — nondeterministic bytes inside the system-prompt prefix.
- **Why**: `encoding/json` into a Go map discards document order and map iteration is deliberately randomized per run; anything whose output must be byte-stable across sessions (sysprompt segments feed the DeepSeek prefix cache) cannot round-trip an ordered JSON object through a map.
- **Fix**: `schemaParams` in `internal/tools/mcptool/prompt.go` token-walks the `properties` object with `json.Decoder` — keys kept in document order, each value skipped via a `RawMessage` decode; commit with card `c_lgtpgq`.
- **Lesson**: JSON object key order is insignificant to a parser but load-bearing for byte-deterministic rendering — ordered output needs a token-stream read, never a map.
- **Refs**: `internal/tools/mcptool/prompt.go` `schemaParams`; `internal/sysprompt/sysprompt.go` (package doc).
