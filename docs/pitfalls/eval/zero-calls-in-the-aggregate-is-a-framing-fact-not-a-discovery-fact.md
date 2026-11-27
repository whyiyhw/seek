### 看到「零次调用」就归因到工具不可发现，是两类缺口的混淆——先用一个自我申报的 eval 分辨
- **Saw**: 40 会话 6,474 次调用里 `references`（LSP 找引用）0 次，同期 bash 2,100 次 grep——据此误判「工具没被发现」。新用例 `references-trigger` 措辞换成「我正要改这个签名，谁在调它」：3/3 轮，每轮第一或第二个工具即 `references`，输出正确（17 条引用，含被 `ComposeSubagent` 淹没那条）。
- **Why**: 调用数为 0 只说明自然任务里没人那样自我申报；探索型工具的触发条件是任务框架，不是工具清单——混同发现率与框架率会把改描述当解药。
- **Fix**: 新增 `eval/cases/references-trigger`（`record_only`，~10s/轮，`references_calls:1`）；README 明写勿因那个 0 改 `internal/tools/references` 描述。
- **Lesson**: 读聚合计数先分两类：能力/发现缺口（坏了/没加载/描述错参数）与框架缺口（任务不会那样自我陈述）。判据：框架成该工具本职的 eval 跑 3 轮，全绿 ⇒ 框架缺口，杠杆在任务措辞；全红 ⇒ 发现缺口，才改描述/schema。
- **Refs**: `eval/cases/references-trigger/README.md`、`docs/prd/archive/feature-lsp.md`、`eval/README.md` §Calibration
