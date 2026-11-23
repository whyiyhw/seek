# tool-args-hallucination

## What this probes

Whether the LLM invents tool parameters that don't exist in the
schema. Every tool whose arguments go through
`internal/tools/tool.go:UnmarshalStrict` declares an explicit field set
with `additionalProperties: false`, and the decoder uses
`DisallowUnknownFields` — any extra field (`depth`, `levels`,
`steps`, …) is rejected and the error names the valid fields. (Most of
the registry — read / grep / git / edit / bash / plan / propose / …;
`ask_user` is the documented exception, its v1/v2 payloads share a
loose decode path.)

When the model hallucinates a field, the agent feeds the error back
as a tool result. A well-behaved model should self-correct within
the same turn. A drifting model retries the same wrong call,
burning tokens.

## History (case retired → rewritten, 2026-11-22)

The case was born on the `think` tool: its schema was
`{task, reflect, context}` and the triggering session
(`~/.seek/sessions/20260522-121611-d416be.jsonl`) called it with a
`depth` field after reading 17 chunks of `docs/PRD.md`.

`think` was retired on 2026-11-22 (card c_cs4dw6) — 109 real project
sessions used it twice. That removed the case's original subject. The
probe is not about `think`, though: strict-decode rejection on
hallucinated arguments is a property of **every** tool, so the case is
rewritten rather than retired, with the subject generalised to a
multi-tool audit run (grep + read + git + list_dir + bash as the model
sees fit).

## Expected behaviour

- **Zero** tool-end events with `unknown field` in the error
  (`max_unknown_field_errors: 0`)
- **At least 5 tool calls** in the round (`min_total_tool_calls: 5`) —
  the liveness floor. Without it a run that answers from memory and
  calls nothing passes vacuously, and "no unknown-field errors" stops
  meaning anything.

If `unknown_field_errors > 0`, the model is hallucinating tool
arguments — either the tool's `description` is too vague, or the
schema isn't surfacing to the model loudly enough.

## Caveat: low base rate

The original bug appeared maybe 1 in N runs even in the failing
session. A single PASS here is **not** evidence the bug is fixed;
look for a sustained zero across many results-file rows.
