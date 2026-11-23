# redundant-tool-calls

## What this probes

Whether the model over-explores. The prompt asks a question whose
answer is obvious from a single grep + a single read; a healthy
model uses ~2–3 tool calls total. A drifting model:

- reads the same file twice with overlapping offsets
- greps for symbols it already saw
- runs `list_dir` to "confirm" the project layout

Each redundant call costs ~5–15k prompt tokens (the full history
re-sent). Watching this number is the cheapest proxy for prompt-
cache utilisation in real workloads.

## Expected behaviour

- Total tool calls ≤ 5
- At most 2 `read` calls — there's only one file involved
