### 看到「零次调用」就归因到工具不可发现，是两类缺口的混淆——先用一个自我申报的 eval 分辨

- **Saw**: 40 会话 6,474 次调用里 `references`（LSP 语义找引用）**0 次**，同期 bash 里跑了 2,100 次
  grep。据此写成「工具没被发现，该改描述」。新加的 `references-trigger` 用例把任务措辞换成
  「我正要改这个签名，谁在调它」——3/3 轮，每轮第一或第二个工具就叫 `references`，输出正确
  （17 条引用，含被 `ComposeSubagent` 淹没的那条）。
- **Why**: 调用数为 0 只说明**自然任务**里没人那样自我申报。探索型工具的触发条件是任务框架
  （「我在做什么」），不是工具清单——挂在 `tools` 数组里的工具，语义匹配上就会用。把「发现率」
  和「框架率」混成一个 0，会把改描述当成解药，而真正的杠杆在派任务的措辞。
- **Fix**: 新增 `eval/cases/references-trigger`（`record_only`，~10s/轮，`references_calls:1` 每轮）；
  用例 README 明写「不要因为那个 0 就改 `internal/tools/references` 的描述」。
- **Lesson**: 读聚合计数前先分两类缺口：**能力/发现缺口**（工具坏了、没加载、描述说错参数）与
  **框架缺口**（任务不会那样自我陈述）。判据是一个把任务框架成该工具本职的 eval，跑 3 轮：
  全绿 ⇒ 框架缺口，杠杆在派任务措辞（卡面 `next_step`、人写 prompt）；全红 ⇒ 才是发现缺口，
  才轮到改描述 / schema。别用一个 0 同时赌两个假设。
- **Refs**: `eval/cases/references-trigger/README.md`、`docs/prd/archive/feature-lsp.md`（柱 L 瘦身口径）、
  `eval/README.md` §Calibration 的 `references-trigger` 行
