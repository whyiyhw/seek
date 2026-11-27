# c_xhbqae 证据：judge gate record → gating

日期 2026-11-24。所有运行：真 `eval/run.sh` + 真 `eval/tools/judge.py` + 真
`eval/cases/*/expect.json`（改动后），只把 seek 二进制换成本目录的确定性
`fake-seek`（SUT 调用无 `-model` → 输出一个「错常量」答案；judge 调用带
`-model` → 输出 `{"pass": false, ...}`）。结果落 `docs/evidence/c_xhbqae/res-*`，
不碰正式 `eval/results/`。

## ① gate 生效：judge 判红被拦/染色

```
$ SEEK_EVAL_RESULTS_DIR=…/res-gate bash eval/run.sh read-tail-fact fake-seek
FAIL  read-tail-fact  [judge: fail]  (0s)
      judge: constructed judge-red: answer names the wrong constant
eval: 0 passed, 1 failed
rc=1
```
行：`{"case":"read-tail-fact","pass":false,"failures":["judge: constructed judge-red: answer names the wrong constant"],"judge_verdict":"fail","judge_model":"kimi-k3.1-pro"}`

```
$ SEEK_EVAL_RESULTS_DIR=…/res-gate bash eval/run.sh code-review-effort-low fake-seek
FAIL  code-review-effort-low  [judge: fail]  (0s)
      judge: constructed judge-red: answer names the wrong constant
RECORD  code-review-effort-low  (0/1 rounds, record-only — not gated)
eval: 0 passed, 0 failed, 1 recorded (record-only)
rc=0
```
行：`{"case":"code-review-effort-low","pass":false,"failures":["judge: …"],"judge_verdict":"fail","judge_model":"kimi-k3.1-pro"}`
（record_only：judge 红染行进 failures，不染 exit——README judge 节已写明。）

## ② kill-switch 关闭路径

```
$ SEEK_EVAL_JUDGE=0 SEEK_EVAL_RESULTS_DIR=…/res-kill bash eval/run.sh read-tail-fact fake-seek
PASS  read-tail-fact  (0s, {…})
eval: 1 passed, 0 failed
rc=0
```
行：`jq 'select(.case=="read-tail-fact") | has("judge_verdict")'` → `false`（零 judge 键）。

`killswitch-bytecheck.sh`（同真 run.sh + 真 judge.py + 同 fake，三种 expect 变体）：

```
--- v1 vs v2 (kill-switch vs no-judge-block) ---
IDENTICAL (byte-for-byte after del(.ran_at,.elapsed_s))
--- v1 vs v3 (kill-switch vs record era) ---
>   "judge_model",
>   "judge_reason",
>   "judge_verdict",
v1 rc=0
v2 rc=0
v3 rc=0
```
- v1 = 真 expect.json（gate）+ `SEEK_EVAL_JUDGE=0`；v2 = 删掉 judge 块的
  expect.json（judge 层引入前的形状）→ **逐字节一致**（仅 `ran_at`/`elapsed_s`
  两个时间字段归一）。
- v3 = `judge.mode=record`（record 时代）：行为与 v1 相同（`pass:true`、
  `failures:[]`、rc=0，永不染色），差集恰好是三个 data-gated 键——kill-switch
  比 record 时代更彻底：连键都不出现。

## ③ 回归

```
eval-judge selftest: PASS (8 legs)
eval-rounds selftest: PASS (…)
eval-lane selftest: PASS (…)
eval-ablate selftest: PASS (… no-env run.sh byte-identical to ce39c968…)
eval-trend selftest: PASS
pitfalls-count: OK
```

## ④ judge 本体与校准集零改动

`git diff --stat`：只有 `docs/prd/feature-eval-v3.md`、`eval/README.md`、
两个 `expect.json`（+ 本卡的 pitfalls 条目）。`eval/tools/judge.py`、
`eval/judge-calibration/**` 零改动。
