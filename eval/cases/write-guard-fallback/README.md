# write-guard-fallback

**Probes**: fsobserve 守卫的"部分读取 ≠ 看过整文件"（H3）——read 只看了 300 行配置的前一部分就尝试整文件 write。

- fixture `testdata/config.yaml`：300 行长行（约 64 KB，**超过 32 KiB 整体读取阈值**，保证走窗口化路径）；顶部 10 行注释块；由 `setup.sh` 复制到 `eval/tmp/write-guard-fallback/`（模型可安全改动），`teardown.sh` 记录最终文件是否含目标行后清理。
- 基线（I3 未落地）：部分读取即标记 observed → write 整文件**静默放行**，`write_refusals = 0`——这就是 `TestWrite_PartialReadDenied_Integration` 描述的缺陷现场。
- 改进（I3）：部分读取不标记 → 第一次 read 截断（200/300 行）后 write 被拒（`write refused: ...`）→ 模型回退 edit（exact-match 本就允许）→ 文件正确。
- 指标：`write_refusals`（tool_end error 含 `write refused`）；金标由 teardown 产物离线判定。
- 关联：`docs/test-plan-read-tool.md` §6 Case D；`internal/fsobserve/fsobserve.go` `Explain`。
