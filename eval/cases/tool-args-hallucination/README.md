# tool-args-hallucination

## What this probes

Whether the LLM invents tool parameters that don't exist in the
schema. The think tool, in particular, declares
`{task, reflect, context}` with `additionalProperties: false` — any
extra field (`depth`, `levels`, `steps`, …) is rejected by the
strict JSON decoder in `internal/tools/tool.go:UnmarshalStrict`.

When the model hallucinates a field, the agent feeds the error back
as a tool result. A well-behaved model should self-correct within
the same turn. A drifting model retries the same wrong call,
burning tokens.

## Failing session that motivated this case

`~/.seek/sessions/20260522-121611-d416be.jsonl` — DeepSeek-chat
called `think` with a `depth` field after reading 17 chunks of
`docs/PRD.md`. The decoder rejected it; the model recovered on the
next turn but the error counts as a real-world capability regression.

## Expected behaviour

- At least 1 `think` call (the prompt explicitly asks for one)
- Zero tool-end events with `unknown field` in the error

If `unknown_field_errors > 0`, the model is hallucinating tool
arguments — either the tool's `description` is too vague, or the
schema isn't surfacing to the model loudly enough.

## Caveat: low base rate

The original bug appeared maybe 1 in N runs even in the failing
session. A single PASS here is **not** evidence the bug is fixed;
look for a sustained zero across many results-file rows.
