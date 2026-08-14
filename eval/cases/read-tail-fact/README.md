# read-tail-fact

**Probes**: 尾部事实可达性——read 能否取到 50 行窗口之外的信息。

- fixture `testdata/app.go`：140 行，`const Timeout = "45s"` 唯一出现在第 118 行（"45s" 全文件仅此一处，已程序化断言）。
- 基线（50 行 cap）：必须 3 次分页才能到达第 118 行；改进后（I2 整体读取）1 次到位。
- 金标（离线）：最终答案包含 `45s`（名字 `Timeout` 也要在，但以值为准）。
- 关联：`docs/test-plan-read-tool.md` §6 Case A。

**注意**：本 case 的 `max_read_calls: 3` 对基线与改进都宽松——区分度主要在离线金标与 turns。
