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
├── ablation/       # dual-run lanes + comparison tables (NOT trend data)
├── transcript-baseline/  # offline transcript snapshots (see "transcript-stats 与口径")
└── fixtures/       # synthetic transcripts for scripts/transcript-stats --selftest
```

## Running

```bash
# Run every case against ./seek
eval/run.sh

# One specific case
eval/run.sh tool-args-hallucination

# Against a different binary (e.g. an older release for A/B comparison)
eval/run.sh tool-args-hallucination /tmp/seek-baseline

# N-round distribution sampling (see "Judgment" below)
eval/run.sh --rounds 5 tool-args-hallucination
```

The runner appends one row to `results/<UTC-date>-<binary-version>.jsonl`
per case it executed, e.g.:

```json
{"case":"tool-args-hallucination","pass":true,"metrics":{"unknown_field_errors":0,"think_calls":1,"turns":4},"prompt_tokens":12834,"completion_tokens":612,"elapsed_s":21}
```

## Judgment: single sample vs N rounds (E1)

Historically every case ran **once** and that single sample was the
PASS/FAIL verdict — noise could flip either way (the v0.12.0 pre-release
run had 3 reds of which 2 were false alarms; see
[`docs/prd/feature-eval-v2.md`](../docs/prd/feature-eval-v2.md) §1).
`--rounds N` (default 1 = historical behaviour, byte for byte) changes
the judgement to a **distribution**:

- Each round is a full case lifecycle (`setup.sh` → seek → `teardown.sh`)
  appending its own result row, carrying `round: k` (the field appears
  **only** when `--rounds > 1`, so single-round rows keep their exact
  historical shape for the trend readers).
- After the rounds, a **gate** case passes iff `passed_rounds / rounds >=
  min_pass_rate` — **inclusive**: 3/5 against 0.6 passes. The comparison
  runs on IEEE doubles, and division and decimal parsing round to the
  same double, so a written threshold that is exactly k/n (0.6, 0.75,
  0.8, 0.5) behaves as intended — but `0.67` is **not** 2/3 and will
  gate one round stricter than a naive "2 of 3" reading suggests.
  Calibration (E2) should pick k/n-representable thresholds.
- A `record_only: true` case is **never gated**: per-round rows and the
  summary line record it (`RECORD <case> (k/N rounds, record-only — not
  gated)`), it counts in a separate "recorded" column, and it cannot
  make the run red or green. This is the machine-enforced home for
  cases whose bounds only bound runaways or whose failures are not
  behavioral — four cases are marked (see the calibration table).
- **Crashed rounds** (seek died mid-flight, no `agent_end`) are simply
  failing rounds — their metrics come out near-zero and any bound like
  `exact_agent_ends` fails; the denominator always counts them. No
  separate error class yet (`max_error_rate` is an open question in the
  PRD §6, deliberately not invented here).
- Interruption safety is structural: each round's row is appended only
  after that round completes (one `echo`, one line), so Ctrl-C mid-run
  loses nothing already written and cannot produce half-line JSONL.

With `--rounds > 1` an **aggregate row** follows the round rows:

```json
{"case":"bash-overreach","type":"aggregate","rounds":5,"passed":4,"pass_rate":0.8,"min_pass_rate":0.67,"record_only":false,"verdict":"pass","ran_at":"…"}
```

It deliberately carries **no `pass` key**: `scripts/eval-trend.sh`
selects sample rows on `pass` being a boolean, so aggregate rows can
never double-count as samples — the trend keeps seeing one sample per
round, which is exactly the distribution data it wants.

`min_pass_rate` / `record_only` live in `expect.json` (not a separate
calibration table): expect.json is already the per-case judgement
authority — a second file would be a parallel source of truth for the
same case, plus a name-join to maintain (the drift risk the repo's
transcript-vs-state discipline exists to avoid). Both keys are
validated at case start; anything malformed (non-number, ≤ 0, > 1,
non-boolean, unparseable JSON) exits 2 before any round runs — never
silently coerced to 1.0/false.

The values are calibrated — see ["Calibration table (E2)"](#calibration-table-e2-2026-11-21)
below ([`docs/prd/feature-eval-v2.md`](../docs/prd/feature-eval-v2.md) §三 E2;
landed 2026-11-21, card c_347nnh).

### Judgment self-test (deterministic, no key)

`scripts/eval-rounds-test.sh` (auto-discovered by the
[`scripts/selftest.sh`](../scripts/selftest.sh) lane, name
`eval-rounds-test`) pins the judgement mechanics with an
invocation-counting fake binary: explicit `--rounds 1` is byte-identical
to the no-flag run (the no-flag-vs-pre-E1 equivalence stays pinned by
`scripts/eval-ablate-test.sh`'s git-history blob compare); a 5-round run
writes 5 round-tagged rows whose crashed rounds judge red **and stay in
the denominator**; the inclusive boundary is pinned from both sides
(3/5 vs 0.6 → green, 3/5 vs 0.7 → red) and re-pinned on the `--rounds=3`
form (2/3 vs 0.66 → green, vs 0.67 → red — the "0.67 is not 2/3"
lesson, testable); record-only never colours the exit code at either
round count; malformed `--rounds` / `min_pass_rate` / `record_only` /
broken JSON exit 2 with zero rows written; and a round-bearing results
dir fed through `eval-trend.sh` counts the round rows only (aggregate
rows skip, proving no double-count).

## Calibration table (E2, 2026-11-21)

One row per existing case under `eval/cases/` — **30 cases**. The PRD's
"21 例" predates the nine `mcp-semble-routing-*` arms added 2026-11-17/18
by the MCP D-line (21 + 9 = 30). History columns are computed from the
372 committed sample rows in `results/` — all-time, plus the window
since each case's last bound/prompt repair (all-time is contaminated
wherever a repair landed mid-history; the window is the honest basis).

**Rounds policy** ([PRD §2.5](../docs/prd/feature-eval-v2.md), pinned by
card c_347nnh): **5 rounds only for known-flaky cases and the
load-bearing axes; every other case 3 rounds.** (PRD §五's "cheap read
cases may get 5, axes 3" contradicts §2.5; the card resolved to §2.5.)
These counts are **lane policy** for the release/nightly lanes (E6) —
the runner itself takes one global `--rounds`.

**Threshold rule**: gate values are decimals that are exactly k/n at
the chosen round count (the 0.5 / 0.6 / 0.75 / 0.8 menu — E1's
inclusive `>=` on doubles); an absent `min_pass_rate` is the default
1.0 (zero tolerance). **Tolerance principle**: a case tolerates only
the failure mass its history attributes to *non-regression noise*
(environment blocks, one-off exploration variance); a clean recent
window keeps the strict gate — a red there is new signal, which is the
point of having a gate.

| case | history (all-time → window) | expected | rounds | gate | record-only |
|---|---|---|---|---|---|
| bash-overreach | 4/15 → post-recal study: 5/5 replay + 1/6 live true-overreach | ~0.83 | 5 | **0.8** | no |
| code-review-effort-low | 11/12 → 9/9 | ~1.0 | 5 | — | **yes** |
| code-review-effort-max | 6/14 → 3/11 under the old 6000 ceiling | ~0.9 | 5 | — | **yes** |
| edit-unicode-read-first | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |
| git-multi-query-no-bash | 23/31 → 10/10 | 1.0 | 3 | 1.0 | no |
| git-subcommand-midtask | 23/23 → 9/9 | 1.0 | 3 | 1.0 | no |
| git-subcommand-shape | 24/25 → 9/9 (lone red 2026-08-18) | 1.0 | 3 | 1.0 | no |
| glm-lane-contract | 1/7 → env-bimodal | env | 5 | — | **yes** |
| grep-refine-not-flood | 8/11 → 7/10 | ~0.7 | 5 | **0.6** | no |
| instruction-following | 11/11 → 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a1-mixed | 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a1-nl | 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a1-symbol | 10/10 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a2-mixed | 5/6 (one transient API crash) | ~0.9 | 3 | **0.6** | no |
| mcp-semble-routing-a2-nl | 5/5 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a2-symbol | 5/5 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a3-mixed | 5/5 | 1.0 | 3 | 1.0 | no |
| mcp-semble-routing-a3-nl | 0/5 — by design (see below) | ~0 | 3 | — | **yes** |
| mcp-semble-routing-a3-symbol | 5/5 | 1.0 | 3 | 1.0 | no |
| print-deny-semantics | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |
| read-eof-blind | 14/19 → 9/9 (red mass pre 2026-11-13) | 1.0 | 3 | 1.0 | no |
| read-tail-fact | 17/19 → 9/9 | 1.0 | 3 | 1.0 | no |
| read-whole-file | 14/19 → 9/9 | 1.0 | 3 | 1.0 | no |
| redundant-tool-calls | 12/12 → 10/10 | 1.0 | 3 | 1.0 | no |
| session-id-contract | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |
| tool-args-hallucination | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |
| tool-selection | 10/14 → 8/8 since the 11-13 repair | 1.0 | 3 | 1.0 | no |
| write-elided-edit-fallback | 9/10 (one unexplained red 11-16) | ~0.9 | 3 | **0.6** | no |
| write-guard-fallback | 19/19 → 9/9 | 1.0 | 3 | 1.0 | no |
| yolo-allows | 10/10 → 9/9 | 1.0 | 3 | 1.0 | no |

Gate semantics at the chosen round counts: `1.0` = 3/3 (or 5/5), zero
tolerance; `0.6 @ 3` = 2/3 (tolerate 1 red); `0.6 @ 5` = 3/5 (tolerate
2); `0.8 @ 5` = 4/5 (tolerate 1). The `expect.json` files carry exactly
these values (`min_pass_rate` written only where ≠ 1.0; `record_only:
true` on the four marked rows); expect.json remains the per-case
judgement authority — this table is the calibration record, not a
second source the runner reads.

### Known-flaky dispositions

The three named in the PRD's triggering incident, plus two the history
forced:

1. **`bash-overreach` — boundary set, stays gated** (`min_pass_rate
   0.8` @ 5 rounds). Post-recalibration studies (case README,
   2026-11-18): all 5 saved streams pass the ordering gate; true
   overreach ("opens with bash, never calls `list_dir`") occurs
   ~1-in-6 — both 2026-11-19 committed rows are exactly that shape and
   judge red (`bash_before_dedicated` 1 and 2 vs the 0 bound). 0.8 =
   4/5 tolerates precisely that noise; ≥2 red rounds (persistent
   overreach) fail the gate. Constructed-row verification (2026-11-21,
   fake-binary sandbox — one-off, not a checked-in mechanism): 1
   overreach round in 5 → `PASS bash-overreach aggregate (4/5 rounds >=
   min_pass_rate 0.8)`; 2 in 5 → `FAIL … (3/5 rounds < min_pass_rate
   0.8)`; a single no-flag overreach round → red row, exit 1.
2. **`code-review-effort-low` / `-max` — record_only** (the PRD §2.1-3
   assignment, acceptance ③). Their bounds are runaway ceilings only —
   the effort signal is swamped by run-to-run variance (case READMEs);
   the recorded distribution is the product, so they run 5 rounds but
   never colour the exit code.
3. **`glm-lane-contract` — record_only (environment)**. Failures are
   lane-environment, not behaviour: the GLM plan's 5-hour quota 429s
   block a whole window at once (2026-11-19: 6/7 committed rows red,
   `nonzero_exits=5`, zero `agent_ends`) — a blocked window is 100%
   red, no threshold separates that from regression. Re-gating is an
   E6 decision (run it inside a quota window with a real key; PRD
   §六.2).
4. **`mcp-semble-routing-a3-nl` — record_only (deterministic
   measurement red)**: 0/5 by design — `min_mcp_calls: 1` IS the
   §5.4① judgment instrument (does `mcp_prompt` guidance route nl
   probes to MCP? today: no — three-arm report 2026-11-18). Gating
   would be permanently red until the guidance changes; the go/no-go
   belongs to the MCP D-line (D5), which reads recorded rows either
   way.
5. **`grep-refine-not-flood` — promoted to the flaky class**
   (`min_pass_rate 0.6` @ 5): 7/10 in the current window and every red
   is the same shape (7–12 total tool calls vs the 6 bound —
   exploration variance), not drift. Bounds stay first-pass; a
   tightening pass may re-strict the gate.

Two gated cases carry tolerate-1 for observed one-off noise:
`mcp-semble-routing-a2-mixed` (`0.6 @ 3`; its one red is a documented
transient API EOF crash — crash rounds are red rounds by doctrine, but
a single transient should not colour a 3-round gate) and
`write-elided-edit-fallback` (`0.6 @ 3`; one unexplained red 2026-11-16
in a 9/10 window).

### Load-bearing axes

| planned case | rounds | gate / baseline |
|---|---|---|
| `plan-e2e` (E3) | 5 | baseline 待 E3 回填 |
| `subagent-bg-resume` (E4) | 5 | **case 已落地**（c_vollxv，2026-11-21）：gate 先取 **0.6**（PRD §六.3 的 doctrine 下限——承重轴实测 < 0.6 时修产品提示而非放界）；**real-key baseline 未测**（无人值守车道假 key 纪律），机制面由 `scripts/eval-e4-case-test.sh` 假二进制钉死（全绿行 + 五种具名红 + record-only + rounds 聚合兼容）。已知噪声源：run 1 的「子代须先于父进程退出跑完」竞态（无 agent 侧 wait 原语），标定后若实测通过率显著低于预期，先核该形态再动界 |
| `serve-web-stream` (E5) | 5 | baseline 待 E5 回填 |

Rounds reserved per §2.5 (承重轴); the gate values are those cards' to
calibrate against their own baselines (PRD §六.3: if a load-bearing
case measures < 0.6, the fix belongs in the product's prompts, not the
threshold). E2 neither ran nor pre-gated them; E4's 0.6 is the doctrine
floor pending its own real-key baseline (a release-lane,
human/main-session action per the provider-lane rule).

**Live re-measurement**: the expected-pass column is history-derived;
real-key rounds under this policy (5-round re-runs of the flaky set,
3-round lanes) are a human/main-session action per the provider-lane
rule below — this calibration used committed rows plus constructed-row
verification only.

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
catches writes in the first place). That real-binary arm pins the
provider lane to `SEEK_EVAL_PROVIDER=deepseek` on purpose (card
c_f734of): the default lane scrubs `DEEPSEEK_API_KEY` from the child,
so a runner that carries no lane credentials dies at the setup wizard
and writes **nothing** — indistinguishable, at the assertion, from
"the sandbox redirect broke". The `SEEK_HOME` pin itself sits above
the lane block and is lane-independent.

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
| `agent_calls` | `tool_start` events with `name=agent` — subagent spawns + continuations (`internal/tools/agent`) |
| `agent_bg_calls` | `agent` tool_starts whose parsed args carry `run_in_background=true` — the D1 detached-spawn shape; the E4 model-side assertion that the spawn was actually background |
| `agent_continue_calls` | `agent` tool_starts whose parsed args carry a non-empty `session_id` — the P1-b continuation shape; the E4 model-side assertion that the follow-up quoted the lineage |
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
| `bg_started_events` | E4 disk-side family: `runner_fact` lines with `check=bg_started_events` (max `value`) — background `started` events in the isolated SEEK_HOME's `projects/*/subagents.jsonl`, reported by subagent-bg-resume's runner after its invocations |
| `bg_completed_events` | `completed` terminals joined to those background sub_sids — the child finished before parent-process teardown cancelled it (a cancelled child folds to `failed reason=canceled` and is excluded) |
| `bg_notified_events` | `notified` consumption markers joined to the **completed** background set — the completion notice specifically (`internal/subagent` Notifier write-side, feature-orchestration §4.1 D3/D5); a failure-notice marker does not count |
| `bg_notified_transcript` | parent-session transcript rows that are user-role `<context>` messages carrying the `[agent: background done sub-sid=<full>]` wire prefix for a background child — the "notice entered the main session stream" surface itself; prefix + sub_sid only, never the summary prose |
| `bg_resumed_lineage` | `started` events whose `resumed_from` is non-empty AND names a sub_sid present in the index — the continuation run's lineage link |
| `bg_artifacts_missing` | completed runs whose `transcript.jsonl` is absent from `projects/<pid>/sessions/<parent-sid>/subagents/<sub-sid>/` (`paths.SubagentSessionDir`) — 0 is the pass state |

The nine agent/bg keys are **data-gated** in the result row: they appear
only when the round's stream carries an agent surface (an `agent`
tool_start or a check-bearing `runner_fact`), so rows of non-agent cases
keep their exact historical bytes — the same discipline as the
`round`-field gate, and load-bearing for
[`scripts/eval-ablate-test.sh`](../scripts/eval-ablate-test.sh)'s no-env
byte-equivalence pin. Readers must treat an absent agent key as 0; a
case binding these bounds must guarantee the surface (subagent-bg-resume's
runner emits its fact channel unconditionally, so even a failed round
has the keys — a red always names its metric, never "unknown metric").
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
| `max_turns` | passed to `seek -max-turns` (NB it also rides the `max_` prefix scan, so it doubles as a turns ceiling — pre-existing behaviour) |
| `min_pass_rate` | gate threshold over the round set, `0 < m <= 1`, default 1.0, INCLUSIVE `>=` — see "Judgment" above (E2 calibrates; validated, exit 2 on malformed) |
| `record_only` | boolean, default false — record the case, never gate it (see "Judgment" above) |

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

## transcript-stats 与口径（离线，不调模型）

`scripts/transcript-stats` 是**离线**分析器：不调模型、不发请求、不需要 key，
只回放已经落盘的 `~/.seek/sessions/*.jsonl`。它和 `eval/cases` 的分工是：

| | `eval/cases` + `run.sh` | `scripts/transcript-stats` |
|---|---|---|
| 判什么 | **边界纪律**：这一步有没有越界（二值 PASS/FAIL） | **真实任务分布上的行为形状**：打了多少轮、多少调用是白打的、read 是不是在翻页循环 |
| 数据来自 | 现跑一次（真 API，随机性，单次判定） | 历史 transcript（确定性、可复算、零成本） |
| 回答 | 「这个 bug 修好了吗」 | 「这一类任务平均长什么样」 |
| 粒度 | 单例 | 全部会话 / 按 model×月份分组 |
| 用在 | 回归闸门、发版前 | 连续效率趋势、参数调优的改前/改后对照 |

它填的是 [`docs/prd/feature-eval-v2.md`](../docs/prd/feature-eval-v2.md) §1.3
诊断的留白——用例集由 pitfalls 台账孵化，本质是「历史 bug 的回归集」，测不了
「复杂任务上的效率分布」。这个工具补的正是后者：同一批 transcript 上量出
**连续量**，而不是再补一条二值断言。

首个基线报告：[`eval/transcript-baseline/2026-11-19.md`](transcript-baseline/2026-11-19.md)
（含四个关键数字：重复率 6.16% / read 重读率 67.99% / 翻页占比 92.00% /
offset 步长 `+50` 991 次）。

### 口径定义（复算以 `scripts/transcript-stats` 的头注释为唯一真源）

| 指标 | 定义 |
|---|---|
| session | 一个 `*.jsonl`；首行元数据（`schema_version`/`model`/`created_at`/`turns`），其余为 `deepseek.Message`，`tool_calls[]` 挂在 assistant 行 |
| fingerprint | 同一会话内 `(tool name, arguments 原始字节)` 完全相同 |
| `dup_extra` | Σ per-fingerprint (次数 − 1) —— 「白打出去的调用数」，按工具分列 |
| `dup_repeated` | Σ 出现次数 > 1 的 fingerprint 的**全部次数** / 总调用 —— 头条重复率口径（与 6.2% 调研数字同口径） |
| read 文件身份 | 参数里 `path` > `file_path` > `file` 第一个存在的键。解析失败的 read 单列 `unresolved` 计数，**绝不并进同一个桶**（并桶会凭空造出「同文件重读」假信号）。三键兼容是防御性契约：实测 2026-11-19 全量 read 参数里 `path` 是绝对主体、`file_path` 一次未出现、`file` 有 2 次——但键漂移的失败模式（静默丢信号）代价远高于三行兼容代码 |
| `reread_rate` | Σ 同会话同文件 (`read 次数 − 1`) / read 总数 —— 多余调用口径 |
| `pagination_share` | 被重读文件里 offset 出现过 >1 个不同值的比例（offset 缺省视作 1） |
| `offset_steps` | 同会话同文件 offset 集合**去重后升序**相邻差（跳页步长分布，top5） |
| 分组 | 指标同时按 `model × created_at[:7]` 汇总——5 月 v0.x 时代占 82% 的调用量，不分组会把版本混杂读成趋势 |

读取侧纪律：权限受限文件跳过并计数（`skipped_files`）；解析失败行计数
（`malformed_lines`）而不中断；数据路径可参数化（`--sessions-dir`）供 fixture
使用。`--selftest` 用 `eval/fixtures/transcript-stats/` 的新旧两种 schema 样例
断言四形态（`path` 键 / `file_path` 键 / 完全重复调用 / 翻页读），并带
`--read-key-mode path-only|naive-bucket` 两组反例，证明单键解析会丢信号或造
假信号。裸跑即自测的入口：`bash scripts/selftest.sh transcript-stats-test`。

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
