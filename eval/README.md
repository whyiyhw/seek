# seek eval harness

Behavior comparison tests for the agent loop. Each case fixes a
**prompt** + **expected metrics**; the runner pipes the prompt
through `seek -json` and checks the resulting tool-call stream
against the case's `expect.json`.

This is **not** a unit-test suite — it's a black-box probe of LLM
behaviour against a real model API (default lane since 2026-11-20:
the OpenCode Go subscription's DeepSeek — see § "Provider lane"),
with all the stochasticity that implies. A single failing run is
signal, not proof; trends across versions are what matter.

## Layout

```
eval/
├── run.sh          # eval/run.sh [case] [binary]   (defaults: all cases, ./seek)
├── ablate.sh       # eval/ablate.sh [case] [binary] — bare-core vs full dual run (below)
├── cases/
│   └── <name>/
│       ├── README.md   # what the case probes + the bug it came from
│       ├── prompt.txt  # exact text passed via `seek -p`
│       └── expect.json # PASS/FAIL bounds; see "Metric vocabulary" below
├── results/        # checked-in JSONL of each pass; one row per case
├── tools/          # shared case engines/scorers (mcp-arm-*, mcp-semble-score, mcp-first-gold-read, …)
└── ablation/       # dual-run lanes + comparison tables (NOT trend data)
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

## Isolation: the SEEK_HOME sandbox

Every seek invocation run.sh launches — default lane and `runner.sh`
cases alike — inherits a **pinned `SEEK_HOME`**:

- caller sets a non-empty `SEEK_HOME` → respected verbatim (an outer
  sandbox redirecting the redirect);
- otherwise → forced to `eval/tmp/seek-home` (gitignored), and an
  inherited `SEEK_SESSIONS_DIR` is stripped — it ranks ABOVE
  `SEEK_HOME` in `session.NewStore`'s resolution ladder and would
  silently bypass the sandbox for sessions.

seek resolves all user-data writes (sessions, `projects/`,
dream-state, cron, skill stats, upgrade-check) through
`paths.Home()` = `$SEEK_HOME`-or-`~/.seek`, so an eval run
**cannot** write the real `~/.seek` — not even a run whose API call
fails instantly (a fake-key probe still wrote four paths there before
this pin landed; card c_clip4i). Deliberate side effect: evals no
longer inherit the running machine's user skills / memory /
mcp.json — measurements are hermetic and comparable across machines.

The API key does NOT live in the sandbox: `internal/config` resolves
env var first (`DEEPSEEK_API_KEY` > `$SEEK_HOME/config.json`), so a
key that used to sit in `~/.seek/config.json` must be exported for
eval runs once SEEK_HOME is pinned.

Who may burn a real key at all: unattended lanes (batch cards,
resident loops, steward sweeps) run **fake-key by rule** — a real key
is only for a human-named `--only` run or a card that explicitly
authorizes it ([`docs/prd/v8.md`](../docs/prd/v8.md) §9.1). The
pinning mechanism AND the zero-real-HOME guarantee are held by
`scripts/eval-home-isolation-test.sh` (selftest lane
`eval-home-isolation-test`: mechanism asserts via a fake binary +
a real-binary canary-HOME A/B whose control arm proves the canary
catches writes in the first place).

## Metric vocabulary (extracted by `run.sh` from the JSONL stream)

| Metric | What it counts |
|---|---|
| `unknown_field_errors` | `tool_end` events whose `error` contains `unknown field` |
| `think_calls` | `tool_start` events with `name=think` |
| `total_tool_calls` | every `tool_start` event |
| `read_calls` | `tool_start` events with `name=read` |
| `grep_calls` | `tool_start` events with `name=grep` |
| `mcp_calls` | `tool_start` events whose tool name contains the `__` infix — MCP bridge tools register as `<server>__<tool>` (D1 default prefix, `internal/tools/mcptool/bridge.go`); no built-in name contains `__`. Reads 0 before D1 landed (the bridge prefixed only on conflicts then), even when MCP tools are used — the metric assumes D1's naming (`docs/prd/feature-mcp-client.md` §5.2) |
| `list_dir_calls` | `tool_start` events with `name=list_dir` |
| `bash_chains` | `tool_start` events for `bash` whose raw args contain `;` or `&&` — the multi-command-chaining anti-pattern. `;` isn't even a separator on cmd.exe (Windows), so a POSIX-style chain fails wholesale; `&&` is legal on cmd but still the anti-pattern (target shape: one command per call, or parallel dedicated-tool calls). `|` deliberately NOT counted — pipelines and grep alternations are legitimate |
| `bash_git_calls` | `tool_start` events for `bash` whose raw args contain a `git` invocation (`\bgit\b`) — read-only git belongs in the dedicated git tool (whitelist + auto-fix); mutating git (commit/push) legitimately lives in bash, so bind this metric only in read-only prompts |
| `bash_before_dedicated` | `bash` tool_starts occurring BEFORE the first `grep`/`read`/`list_dir` tool_start — bash as FIRST resort. Ordering is the honest anti-pattern signal when a case's task legitimately forces shell recovery later (e.g. `read` elided an over-1024-byte line so verbatim text needs awk): a total `bash_calls` bound cannot tell recovery from overreach, this can. A run with no dedicated call at all counts every bash call (`list_dir` joined the set 2026-11-18 — adding a tool can only lower the metric, so no existing bound tightens) |
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

## Trend reports

`scripts/eval-trend.sh` aggregates every `results/*.jsonl` into
`trend-report.md` + `trend-report.json` (both checked in, regenerated on
demand): per-case last-5 pass rate, first-failure time, consecutive-failure
streak, and drift-suspect flags — most notably `suspect:gap-uncovered`
(failing run whose previous coverage was >30 days earlier: the failure date
is only an **upper bound** on when behaviour drifted — the bash-overreach
lesson) and `suspect:no-baseline` (case with ≤5 recorded runs, first run
<30 days old, already failing). `--selftest` drives the whole flag matrix
from a fake results dir. Env knobs (`TREND_WINDOW`, `TREND_GAP_DAYS`,
`TREND_NOW`, …) are documented in the script header.

Note for tooling: the early result files (pre v0.5) are pretty-printed
concatenated JSON objects — one result spans many lines — so readers must
parse with jq, never line-by-line (see `docs/pitfalls.md`).

## Ablation: bare-core vs full dual run (v8 pillar T)

`eval/ablate.sh [case] [binary]` runs every case **twice** — once with
the default (full) tool registry, once with the bare v8 kernel five
(`-tools read,edit,write,bash,grep`) — and prints a per-case F/B
comparison table (pass, turns, tool_calls, prompt/completion tokens,
cost) plus a TOTAL row. The table also lands in
`eval/ablation/<date>-<version>.md`; per-lane JSONL rows stay in
`eval/ablation/{full,bare}/`. Output files are **not** checked in
automatically — commit a `.md` by hand when a run is meant to backfill
[`docs/prd/v8.md`](../docs/prd/v8.md) §9.

**Both lanes write to `eval/ablation/`, never to `eval/results/`** —
that dir is the trend reports' data source, and ablation rows there
would fabricate regressions. The isolation rides on two `run.sh` env
knobs (both unset = historical behaviour, byte for byte). The
provider lane is orthogonal: `ablate.sh` sets no `SEEK_EVAL_PROVIDER*`
knobs, so both ablation lanes ride run.sh's DEFAULT provider lane
(the OpenCode Go subscription since the 2026-11-20 user call —
ablation runs are batch by definition and must not burn the metered
key either; the explicit `SEEK_EVAL_PROVIDER=deepseek` fallback is a
human decision, see § "Provider lane"):

| Env var | Effect |
|---|---|
| `SEEK_EVAL_RESULTS_DIR` | redirect the JSONL sink (default `eval/results`) |
| `SEEK_EVAL_EXTRA_ARGS` | extra seek args appended to the default invocation (space-separated flag list, word-split by design); also passed through to `runner.sh` cases in the env — runners that build their own invocations opt in by forwarding it (none do yet, so their F/B rows are lane-invariant; `ablate.sh` footnotes them) |

Cost in the table uses the deepseek-v4.1-flash card
(`internal/pricing`); rows carry no model id, so on other lanes read
the F/B ratio, not the absolute `$`. Per-case FAILs are ablation data,
not a script failure — `ablate.sh` exits 0 whenever both lanes
measured, 2 on setup errors.

### Self-test (deterministic, no key)

`scripts/eval-ablate-test.sh` (auto-discovered by the
[`scripts/selftest.sh`](../scripts/selftest.sh) lane, name
`eval-ablate-test`) re-verifies the ablation plumbing mechanically —
the original card's sandbox acceptance could not be re-run (the
permission layer refuses ad-hoc fake binaries), so the fake binary now
lives inside a checked-in test. A fake `seek` prints a fixed JSONL
stream and switches streams when it sees `-tools`; the test asserts
the dual run exits 0, the table lands in `eval/ablation/` with
lane-distinct F/B cells (`tool_calls 1/2` proves `SEEK_EVAL_EXTRA_ARGS`
actually reached the invocation), `eval/results/` gains nothing, and —
with both env knobs unset — `run.sh`'s stdout, stderr and result rows
are byte-identical to the last pre-knob `run.sh` blob (extracted
mechanically from git history; only fields that differ between any two
runs of the *same* script — timestamps, elapsed seconds, date stamps,
sandbox paths — are normalised). The test cleans up its own artifacts
under `eval/ablation/` (mtime-guarded: pre-existing ablation data is
untouched) and fails loudly if normalisation ever degrades to empty
output — an early revision shipped exactly that false-green.

## Provider lane: test traffic on the OpenCode Go plan (c_ffzic2)

**口径（v8.md §9.1）：测试默认 OpenCode Go 套餐 DeepSeek（订阅内）；计量真 key 仍仅 `--only` 人工指名或卡面明示授权。** Batch/unattended eval runs (three-arm comparisons, regressions, ablation lanes) must never burn the metered DeepSeek key — since the 2026-11-20 user call (疑点 A option (a)) the harness's **default lane IS the OpenCode Go subscription's `deepseek-v4.1-flash`**: all four knobs unset steers every seek invocation there, and the only way back to the metered key lane is the explicit `SEEK_EVAL_PROVIDER=deepseek` (a human decision — the env form of 人工指名).

Four `run.sh` env knobs (all four unset = **default opencode lane**, not the historical metered lane):

| Env var | Effect |
|---|---|
| `SEEK_EVAL_PROVIDER` | `deepseek` \| `opencode` \| `compatible`. Unset = `opencode` (OpenCode Go plan; seek's own lane, default model `deepseek-v4.1-flash`). `deepseek` = explicit fallback to the metered key lane — the historical invocation shape (no `-provider` flag, env `DEEPSEEK_API_KEY` untouched by the scrub). `compatible` = any OpenAI-compatible endpoint |
| `SEEK_EVAL_MODEL` | model id, passed as `-model`; with `opencode` unset it IS `deepseek-v4.1-flash` (seek's lane default) |
| `SEEK_EVAL_BASE_URL` | endpoint override (full POST target), passed as `-base-url` **and** exported as `OPENCODE_BASE_URL` so a `providers.opencode.base_url` config preset can't out-rank the flag inside seek |
| `SEEK_EVAL_API_KEY` | lane credential, exported as the lane's canonical env var (`DEEPSEEK_API_KEY` / `OPENCODE_API_KEY` / `OPENAI_API_KEY`); never write real keys into card text, logs or expect files |

While the opencode/compatible lane is active, `run.sh` **scrubs `DEEPSEEK_API_KEY` from the child environment** — the metered key has zero reach into a switched lane (seek's `compatible` case would otherwise read it as a fallback credential). The deepseek lane is the metered lane itself, so only there the env key survives. Credential resolution itself happens inside seek: `OPENCODE_API_KEY` env > `providers.opencode.api_key` > the stored browser login.

**Token rule (hard): the harness never reads — and never refreshes — `~/.seek/opencode-auth.json`.** The refresh token is single-use; a side refresh invalidates the copy seek holds. An expired login surfaces as loud API errors and a human re-logs in; it is never self-healed by eval tooling.

**runner.sh forwarding contract.** Cases that build their own invocations (e.g. the mcp-semble-routing arm engine) opt in by forwarding the exported lane args — the same opt-in shape as `SEEK_EVAL_EXTRA_ARGS`:

```bash
"$SEEK_EVAL_BINARY" -yolo -no-save -max-turns "$SEEK_EVAL_MAX_TURNS" \
  -json ${SEEK_EVAL_PROVIDER_ARGS-} -p "$prompt"
```

`SEEK_EVAL_PROVIDER_ARGS` is empty only on the explicit `deepseek` fallback lane (`${VAR-}` is the `set -u`-safe reference; the default opencode lane forwards `-provider opencode`), and the lane's exported credential env vars ride along automatically. Wiring the arm runner this way is the follow-up for the mcp-semble line (card c_eyuxqm ② 复核 and later MCP-lane tests default to this provider).

**Hermetic proof.** `eval/tools/fake_openai_compat.py` is a local fake OpenAI-compatible endpoint (one text delta + usage + `[DONE]`, same wire shape as `fake_paas_glm.py`) that records every POST's path / auth / model to a JSONL log. `scripts/eval-provider-test.sh` (auto-discovered by the [`scripts/selftest.sh`](../scripts/selftest.sh) lane, name `eval-provider-test`) drives the REAL seek binary against it with fake keys and asserts **both the default path (all four knobs unset → `-provider opencode`, metered key scrubbed, wire model `deepseek-v4.1-flash`) and the explicit `deepseek` fallback path (no `-provider`, metered key intact)**, plus the four knobs mapping into argv/env, the request target/credential/model actually switching on the wire, a config-preset endpoint hijack being out-ranked, both env-side and config-side metered-key canaries never reaching the endpoint, and the runner contract above holding. Real-plan smoke (≥3 runs + metered-key usage delta check) is a human/main-session action, not a selftest.

Cost note: rows on a provider lane carry no model id, so cross-lane cost comparisons read the ratios, not the absolute `$` (same caution as the ablation table).

## Cost

Each pass is one full `seek` invocation. On the default lane (since 2026-11-20: the OpenCode Go subscription's `deepseek-v4.1-flash`) it bills the subscription, not the metered API. On the explicit `SEEK_EVAL_PROVIDER=deepseek` fallback it is one metered DeepSeek call — budget roughly **$0.02–0.05 per case** depending on prompt length and tool-call fan-out (full 3-case suite well under $0.20).

Rows also carry `cache_hit_tokens` (for cost accounting — see `docs/test-plan-read-tool.md` §3.3) and `final_text` (joined assistant text, capped at 2000 chars, for offline gold-answer scoring).
