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

### 相关踩坑

Shell Hooks 实现中的关键教训，以下是来自 [`docs/pitfalls.md`](../pitfalls.md) 的详细记录：

**1. Shell-hook SkipReason 需要每事件处理而非统一通道**

- **Saw**：标记为 `SkipReason="syntax: bad"` 的 hook（`bash -n` 启动检查失败）在 `OnPreToolUse` 中产生 `Deny: 'hook "broken" exited with code -1'`，拒绝了本应静默跳过的操作。
- **Why**：`runHook` 中对 `code != 0` 统一视为 deny（`pre_tool` 的契约）。但 SkipReason 需要被视为"跳过"而非"拒绝"——不同的 event 入口点对"跳过"有不同的含义。
- **Fix**：每个事件入口点自行检查 `h.SkipReason` 再决定行为。`OnPreToolUse` continue 跳过该 hook；observer 无操作。`runHook` 仍防御性地在 SkipReason 时返回 `code=0`。

**2. limitedWriter 需要指针接收器——值接收器丢失字节计数**

- **Saw**：cap-output 辅助函数在测试中超过 64 KiB 上限，因为 `l.n` 不增长。
- **Why**：`func (l limitedWriter) Write`（值接收器）每次调用获得一份新副本，`l.n += n` 修改丢弃。`io.Writer` 接口同时被值和指针接收器满足，但只有指针接收器实际更新计数。
- **Fix**：改为指针接收器 `func (l *limitedWriter) Write`。

**3. Trust 提示必须在 bubbletea 启动前——askuser channel 尚未排空**

- **Saw**：shell-hooks trust 对话框通过 `askuser.Policy` 触发，但初次信任发生在 TUI 启动前，`SetAskFn` 尚无回调。
- **Fix**：构建独立 `stdinTrustPrompt`，在 `tea.NewProgram` 前通过 stdin 完成信任确认。

详见 [`docs/pitfalls.md`](../pitfalls.md) 全文——130+ 条持续更新的踩坑记录。
