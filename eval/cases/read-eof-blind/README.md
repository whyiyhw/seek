# read-eof-blind

**Probes**: EOF 盲区（H2/H5 的核心）——当文件行数恰好等于读取上限时，模型无法从结果判断是否还有下一页。

- fixture `testdata/eof.txt`：**恰好 50 行**，`LAST_LINE_MARKER=42` 在第 50 行。
- 基线：读 1–50 行后，header 只说 "50 lines emitted"，模型不知道是否还有内容——谨慎的模型会发 offset=51 探测（被 `probe_reads` 指标捕获），浪费一整轮。
- 改进：header 带 EOF/总行数信息 → 模型直接作答。
- 金标（离线）：答案含 `LAST_LINE_MARKER=42`。
- 关联：`docs/test-plan-read-tool.md` §6 Case B；`probe_reads` 指标定义见 `eval/README.md`。
