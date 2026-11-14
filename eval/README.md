# seek eval harness

Behavior comparison tests for the agent loop. Each case fixes a
**prompt** + **expected metrics**; the runner pipes the prompt
through `seek -json` and checks the resulting tool-call stream
against the case's `expect.json`.

This is **not** a unit-test suite — it's a black-box probe of LLM
behaviour against the real DeepSeek API, with all the stochasticity
that implies. A single failing run is signal, not proof; trends
across versions are what matter.

## Layout

```
eval/
├── run.sh          # eval/run.sh [case] [binary]   (defaults: all cases, ./seek)
├── cases/
│   └── <name>/
│       ├── README.md   # what the case probes + the bug it came from
│       ├── prompt.txt  # exact text passed via `seek -p`
│       └── expect.json # PASS/FAIL bounds; see "Metric vocabulary" below
└── results/        # checked-in JSONL of each pass; one row per case
```

## Running

```bash
# Run every case against ./seek
eval/run.sh

# One specific case
eval/run.sh tool-args-hallucination

# Against a different binary (e.g. an older release for A/B comparison)
eval/run.sh tool-args-hallucination /tmp/seek-baseline
```

The runner appends one row to `results/<UTC-date>-<binary-version>.jsonl`
per case it executed, e.g.:

```json
{"case":"tool-args-hallucination","pass":true,"metrics":{"unknown_field_errors":0,"think_calls":1,"turns":4},"prompt_tokens":12834,"completion_tokens":612,"elapsed_s":21}
```

## Metric vocabulary (extracted by `run.sh` from the JSONL stream)

| Metric | What it counts |
|---|---|
| `unknown_field_errors` | `tool_end` events whose `error` contains `unknown field` |
| `think_calls` | `tool_start` events with `name=think` |
| `total_tool_calls` | every `tool_start` event |
| `read_calls` | `tool_start` events with `name=read` |
| `grep_calls` | `tool_start` events with `name=grep` |
| `list_dir_calls` | `tool_start` events with `name=list_dir` |
| `bash_chains` | `tool_start` events for `bash` whose raw args contain `;` or `&&` — the multi-command-chaining anti-pattern. `;` isn't even a separator on cmd.exe (Windows), so a POSIX-style chain fails wholesale; `&&` is legal on cmd but still the anti-pattern (target shape: one command per call, or parallel dedicated-tool calls). `|` deliberately NOT counted — pipelines and grep alternations are legitimate |
| `bash_git_calls` | `tool_start` events for `bash` whose raw args contain a `git` invocation (`\bgit\b`) — read-only git belongs in the dedicated git tool (whitelist + auto-fix); mutating git (commit/push) legitimately lives in bash, so bind this metric only in read-only prompts |
| `bash_before_dedicated` | `bash` tool_starts occurring BEFORE the first `grep`/`read` tool_start — bash as FIRST resort. Ordering is the honest anti-pattern signal when a case's task legitimately forces shell recovery later (e.g. `read` elided an over-1024-byte line so verbatim text needs awk): a total `bash_calls` bound cannot tell recovery from overreach, this can. A run with no grep/read at all counts every bash call |
| `git_calls` | `tool_start` events with `name=git` |
| `git_subcommand_dupes` | `tool_end` events for `git` whose result contains `repeated from args` — the model repeated the subcommand as `args[0]` and the tool auto-fixed it; counts the construction mistake at zero behavioral cost (see `eval/cases/git-subcommand-shape/README.md`) |
| `probe_reads` | `tool_end` events for `read` whose result contains `0 lines emitted` **and** `from line` — a read past EOF that returned nothing, i.e. the model was probing because it couldn't tell whether more pages existed (see `docs/test-plan-read-tool.md` §3.2) |
| `write_refusals` | `tool_end` events for `write` whose error contains `write refused` — the fsobserve blind-overwrite guard refusing (see `internal/fsobserve/fsobserve.go` `Explain`) |
| `write_ok` | `tool_end` events for `write` with no error — the in-CWD write default-allow path (writes inside CWD aren't "dangerous", so even print-mode `PrefDeny` lets them through) |
| `denied_tool_ends` | `tool_end` events whose `error` contains `permission denied` — `internal/permission.ErrDenied` surfaced as a tool *result*, never a fatal error (the run continues and exits 0) |
| `agent_ends` | `agent_end` events — one per seek invocation; bind `exact_` in runner-chained cases to catch a run that died mid-flight (no `agent_end`) |
| `session_id_missing` | `agent_end` events without `session_id` (`cmd/seek/main.go` `jsonLine.SessionID`, stable contract; `--no-save` legitimately omits it — only bind this in cases that save) |
| `session_id_distinct` | distinct `session_id` values across `agent_end` events — a fresh run plus `-resume` of that session must report the same id → 1 |
| `nonzero_exits` | `runner_fact` lines with `exit_code != 0` — a runner.sh's channel for process-level results (exit codes aren't in the JSONL stream); 0 for default-invocation cases |
| `turns` | `turn_end` events |
| `completion_tokens` | cumulative generated tokens (`agent_end.completion_tokens`) — verbosity/thoroughness proxy |
| `cache_hit_tokens` | MAX `agent_end.cache_hit_tokens` across the stream (not the first event) — in a runner-chained fresh+resume case the hit lands on the resume run, whose re-sent prefix finds the provider's server cache warm; 0 means the provider's cache detail never reached `agent_end` at all (see `eval/cases/glm-lane-contract/`) |
| `review_line_refs` | count of `line N` / `LN` references in the assistant's text — rough "how many findings" proxy (deliberately ignores severity words so it doesn't echo the effort vocabulary) |

Bounds in `expect.json` use the prefix to declare direction:

| Key shape | Meaning |
|---|---|
| `max_<metric>` | metric must be `≤ value` |
| `min_<metric>` | metric must be `≥ value` |
| `exact_<metric>` | metric must equal value |

`expect.json` also carries scalar config:

| Key | Meaning |
|---|---|
| `description` | human-readable summary |
| `max_turns` | passed to `seek -max-turns` |

## Adding a case

1. `mkdir eval/cases/<name>` (kebab-case)
2. `README.md` explaining what behaviour the case probes — link to the original failing session, bug, or PRD claim if any
3. `prompt.txt` — the exact `-p` input; multi-line is fine
4. `expect.json` — start with `{"max_turns": 10}` and add bounds as you learn what good vs. bad looks like

Seeding from the pitfalls log: `scripts/pitfall-to-eval.sh --list` ranks
`docs/pitfalls.md` entries by model-behaviour likelihood (a triage
heuristic, not a decision), and `--make <line> <slug>` scaffolds a draft —
README.md + prompt.txt with the pitfall excerpt embedded, **no
expect.json**. The runner skips a case without expect.json, so a
scaffolded draft cannot run until a human writes the bounds; that skip is
the review gate.

Optional per-case hooks (run from the repo root, same cwd as the seek invocation):

- `setup.sh` — prepare fixtures before the run, e.g. copy `fixtures/` into `eval/tmp/<case>/` so the model can mutate them without dirtying the repo.
- `teardown.sh` — clean up after the run; runs even when the invocation failed.
- `runner.sh` — **replace the default seek invocation entirely** (`-yolo -no-save … -p`). For cases whose contract is about the CLI shape itself rather than the model's tool-call stream: running without `-yolo` (print-mode permission defaults), chaining multiple invocations (fresh run + `-resume` same session id), isolating `SEEK_HOME`, … Contract: executed with `SEEK_EVAL_BINARY`, `SEEK_EVAL_REPO_ROOT`, `SEEK_EVAL_PROMPT_FILE`, `SEEK_EVAL_MAX_TURNS` in the environment; writes the JSONL event stream to stdout, optionally followed by `{"type":"runner_fact","exit_code":N}` lines per invocation (consumed by the `nonzero_exits` metric). Cases without a `runner.sh` keep the default invocation and its historical behaviour.

## Tool-description tuning (relocated from AGENTS.md 2026-11-14)

The `const description` inside each `internal/tools/<name>/` is the highest-leverage behavioural lever: it rides every tool schema to the API, so the model MUST read it to build a valid `tool_call` — unlike AGENTS.md, which weaker/faster models may skim.

Pattern (proven in `eval/cases/tool-selection/`):

1. **Eval case first** — `prompt.txt` + `expect.json` defining the desired behaviour in measurable terms.
2. **Baseline** — run and record to `eval/results/` before changing anything.
3. **Edit the description** — 10–20 words of targeted guidance; zero runtime cost, the string is already sent.
4. **Compare** — re-run; success is the tool-call sequence changing, not just binary PASS/FAIL.

This repo's own tuning history:

| Tool | Guidance added | Observed effect |
|---|---|---|
| `bash` | "Prefer dedicated tools for repo inspection" | Model stops reaching for `bash ls` / `bash cat` |
| `edit` | "Read target lines first — don't guess whitespace" | Model reads before constructing `old_string` |
| `git` | "Prefer grep+read over git show/cat-file" | Model uses `grep`+`read` instead of `git show HEAD:file` |
| `list_dir` + `bash` (2026-11-13) | "One call answers count-/size-questions about a tree" / "counting files or comparing sizes is list_dir work, not find/wc/stat" | Model stops reaching for `bash find \| wc -l` / `find -exec stat \| sort` (`eval/cases/bash-overreach` drift repair) |

## When to read results

Results are checked into git so you can `git log eval/results/` and watch how the agent's behaviour drifts as the codebase, prompts, or model versions change. A single PASS does not mean the bug is fixed; a sustained PASS rate across many runs does.

## Cost

Each pass is one full `seek` invocation against DeepSeek's API. Budget roughly **$0.02–0.05 per case** depending on prompt length and tool-call fan-out. The full 3-case suite is well under $0.20.

Rows also carry `cache_hit_tokens` (for cost accounting — see `docs/test-plan-read-tool.md` §3.3) and `final_text` (joined assistant text, capped at 2000 chars, for offline gold-answer scoring).
