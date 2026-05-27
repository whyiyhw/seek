# 第 20 章：M9.2–M9.3 — Shell Hooks 可扩展性

> **对应版本**：v0.4.1
> **对应代码**：`internal/hooks/shell_runner.go`、`internal/hooksconfig/`、`cmd/seek/hooks_trust.go`
> **PRD**：[`docs/prd/feature-shell-hooks.md`](../prd/feature-shell-hooks.md)
> **验收**：12/12 验收标准全部通过，`go test -race` 全绿
> **起点**：第 19 章（Checkpoint 安全网）。用户有了可撤销的操作后，下一个需求是"可扩展的操作前/后拦截"。

---

## 内容预告

本章待撰写。核心内容：

1. **动机**：把 `internal/hooks` Registry 开放给用户（此前只对 Go 内部代码开放）
2. **设计约束**：
   - stdout 不进 prompt 字节序列（prefix-cache 确定性零妥协）
   - 只允许 deny（`pre_tool` 非零退出）或 observe；不允许改 prompt/args
   - "信任已配置"而非"信任每次执行"（hooks.toml 写入 = 一次性授信）
3. **hooks.toml 配置**：用户级 `~/.seek/hooks.toml` + 项目级 `.seek/hooks.toml` 叠加
4. **Trust 询问**：VS Code 风格，sha256 跟踪改动，首次弹确认后才执行 bash
5. **ShellRunner**：`pre_tool` deny + 五个 observer，`bash -n` 静态检查，timeout 超时保护
6. **Audit log**：并发安全 append，`seek hooks audit --denied` 查看被拒绝的操作
7. **CLI**：`seek hooks {list,check,trust,audit}`

**关键提交**：`66a4312`（M9.2 hooks 核心 + 12 验收测试） → `50a7f01`（M9.3 stdin trust 提示 + audit log）

阅读本章前建议先读 PRD，再 `go test -race ./internal/hooksconfig/... ./internal/hooks/... -run Shell` 跑一遍验收测试理解边界行为。
