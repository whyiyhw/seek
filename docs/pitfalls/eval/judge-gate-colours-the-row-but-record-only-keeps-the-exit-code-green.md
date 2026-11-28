### judge gate 翻红 ≠ exit 翻红——record_only 用例上两条轴各自独立
- **Saw**: `judge.mode=gate` 且 judge 判 red 后，`code-review-effort-low` 的行里 `pass:false`、`failures:["judge: …"]`，但控制台是 `RECORD … (0/1 rounds, record-only — not gated)`、exit 0。
- **Why**: 染色只发生在 `run_one` 的行级 failures 扫描（judge 追加一行）；exit/汇总先被 `run_case` 的 `record_only` 分支吃掉——record-only 永不计入 pass/fail 总计。两条轴正交，`judge.mode` 不是 exit 开关。
- **Fix**: 不改语义（record-only 是刻意口径：code-review 两例的机械界只有 runaway 上限）。2026-11-24 开 gate（卡 c_xhbqae）时在 `eval/README.md` judge 节 + 两例 expect.json description 写明「judge 红染行不染 exit；端到端拦截只见于非 record-only 用例」。
- **Lesson**: 判断「gate 生效」要分两层——行级 `pass`/`failures`（judge 层）与 exit/汇总（case 层 `record_only`/`min_pass_rate`）；给 record_only 用例开 gate 前先问它拦不拦得住。
- **Refs**: `eval/run.sh`；`eval/cases/code-review-effort-low/expect.json`
