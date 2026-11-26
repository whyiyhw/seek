### A `float64` + `omitempty` knob cannot express "explicit zero" — and the zero IS the point
- **Saw**: `--temperature`'s shape was `Temperature float64 \`json:"temperature,omitempty"\``. That makes `--temperature 0`, the determinism pin, read absent.
- **Why**: `encoding/json` `omitempty` fires on `0.0`; `float64` has no third state, `*float64` three.
- **Fix**: `llm.ChatRequest.Temperature *float64` + `agent.Config.Temperature *float64` tagged `json:",omitempty"` (nil omits, 0 emits `"temperature":0`); `cmd/seek/main.go` `parseTemperatureFlag` checks 0..2.
- **Lesson**: 0 can be deliberate: the wire needs three states, also `false`/`int`.
- **Refs**: `pkg/llm/llm.go`, `pkg/agent/agent.go`, `runTurnDeepSeek`/`runTurnLLM`, `pkg/llm/provider/{openai,anthropic,gemini}/client.go`, `pkg/agent/temperature_test.go`, `pkg/llm/provider/*/client_test.go` `Test*_Temperature*`, `ChatRequest.Temperature`, `Config.Temperature`, `docs/pitfalls/go-language/json-omitempty-is-silently-ineffective-on-time-time-and-other-struct-types.md`
