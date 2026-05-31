# seek OS 沙箱 — 内核级安全边界 / OS sandbox

沙箱是 seek **无人值守安全**的核心。当 autopilot 或 `--yolo` 模式需要限制子进程的写操作时，沙箱提供**内核级**的 jail——不依赖 Docker、不依赖容器运行时，零额外依赖。

> The sandbox provides **kernel-level** confinement for unattended execution — no Docker, no container runtime, zero extra dependencies.

当前支持两个平台：**macOS Seatbelt** 和 **Linux Landlock**。不支持的平台（Windows、BSD）静默降级为 worktree 逻辑隔离。

设计与决策见 [`docs/prd/feature-sandbox.md`](prd/feature-sandbox.md)。

---

## 1. 它能做什么 / What it confines

| 操作 | macOS (Seatbelt) | Linux (Landlock) |
|------|-----------------|-------------------|
| **文件读** | ✅ 允许全部 | ✅ 允许全部 |
| **文件写** | ✅ **只允许白名单目录** | ✅ **只允许白名单目录** |
| **网络** | ✅ **默认禁止，可放行** | ❌ Landlock 不管网络 |
| **exec** | ✅ 允许（sandbox-exec 限制）| ✅ 允许 |
| **内核要求** | 内置（需 `sandbox-exec`） | Linux ≥ 5.13（Landlock ABI ≥ 1） |

### macOS
- **Seatbelt**（`sandbox-exec`）：虽然标记为 deprecated，但功能完好，零 cgo，完全符合 seek 的架构（`CGO_ENABLED=0`）
- 网络默认**禁止**，通过 `AllowNetwork` 放行
- 用 SBPL（Seatbelt Profile Language）生成策略

### Linux
- **Landlock**：Linux 内核 LSM，从 5.13 开始可用
- **内核版本校验**：自动探测 ABI 版本并降级不支持的权限（如 `REFER` 需要 ABI 2、`TRUNCATE` 需要 ABI 3、`IOCTL_DEV` 需要 ABI 5）
- 通过 **re-exec trampoline** 实现：进程 exec 自身→应用 landlock→再 exec 目标命令。**fail-closed**——如果 jail 创建失败，进程退出而不执行命令
- **网络不管**——Landlock 不管理网络（这是设计使然）
- 运行 Landlock 需要 `no_new_privs`（trampoline 自动设置）

---

## 2. 如何启用 / How to enable

沙箱默认集成在 autopilot 子代理中，**自动检测**可用性：

```go
// autopilot 模式下，子代理的 bash 自动包砂箱
// macOS: sandbox-exec 存在即可
// Linux: 内核 ≥ 5.13 + Landlock 未禁用
if sandbox.Available() {
    bt = bt.WithSandbox(sandbox.Options{
        WritableDirs: []string{worktreePath},
    })
}
```

### 验证沙箱是否可用

```bash
# 查看 seek 日志中是否出现 sandbox 相关行（stderr）
seek autopilot run "一个小测试" 2>&1 | grep -i sandbox
```

### Linux 确认 Landlock 支持

```bash
# 检查内核是否支持 Landlock
grep landlock /proc/self/status
# → "Landlock: supported" 或 "not"

# 查看 ABI 版本
cat /proc/sys/kernel/landlock/abi
# → 1、2、3 等（越大功能越多）
```

如果内核不支持但需要 sandbox，可以用系统包管理器升级内核，或回退到 worktree 逻辑隔离（autopilot 默认行为）。

---

## 3. 架构设计 / Architecture

### macOS: seatbelt

```
bash("rm -rf /etc")          sandbox-exec -p <SBPL> rm -rf /etc
     │                              │
     ▼                              ▼
  [denied, writes] ←───   seatbelt 内核模块
                              │
                         read+exec ✅   write: 只允许 worktree/ + /tmp/
                         network ❌
```

### Linux: Landlock trampoline

```
bash("rm -rf /etc")
     │
     ▼
  seek（原进程）
     │  Detects Landlock ABI >= 1
     ▼
  seek re-exec（trampoline argv）
     │  ↓ no_new_privs
     │  ↓ landlock_create_ruleset（fail-closed）
     │  ↓ landlock_restrict_self
     ▼
  /bin/sh -c "rm -rf /etc"
     │
     ▼
  [denied by Landlock——/etc 不在 writable dirs 中]
```

trampoline 的关键设计决策：
- **Fail-closed**：landlock 创建失败 → exit nonzero，绝不降级执行
- **进程内生效**：Landlock 限制的是**当前进程**，所以读/写/edit 工具受同一策略约束
- **ABI 版本掩码**：规避内核版本差异（ABI 1 有 `LANDLOCK_ACCESS_FS_EXECUTE`、ABI 2 有 `REFER` 等）

---

## 4. 用户配置 / User configuration

当前沙箱配置由 autopilot 自动处理，无用户可见的 config 项。可选的能力扩展（如 `AllowNetwork`）在代码中是 `sandbox.Options` 字段，尚无 CLI flag。

未来可能添加：
- `seek sandbox check` — 检查当前平台沙箱可用性
- `--sandbox-mode` — enforce / permissive / off

---

## 5. 已知限制 / Known limitations

| 限制 | 说明 |
|------|------|
| **Windows 不支持** | Windows 没有等效的内核级沙箱原语，回退到 worktree 隔离 |
| **Landlock 不管网络（Linux）** | Linux 端网络只能靠防火墙或 namespace 隔离——未来可考虑 bpf |
| **Seatbelt 已标记 deprecated** | 但无替代方案，功能完好，macOS 未移除 |
| **内核要求** | Linux < 5.13 不支持 Landlock，macOS 需要 sandbox-exec |
| **不可逃逸** | 一旦 landlock 应用，进程无法解除——这是功能而非 bug |

---

> **下一步**：了解 autopilot 如何利用沙箱 → [`guide-autopilot.md`](guide-autopilot.md)
> 
> **参考**：Landlock 官方文档 — <https://landlock.io>
