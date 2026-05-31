# Feature: OS 沙箱（seatbelt / landlock）—— 无人值守执行的内核级牢笼（v7 柱 O · seed）

**所属版本**：v7（v0.8.x）· 柱 O
**前置阅读**：[`v7.md`](v7.md) §7.1/§7.2、[`feature-autopilot.md`](feature-autopilot.md)（柱 N —— 本柱主要服务的无人值守路径）、[`feature-permission-refactor.md`](feature-permission-refactor.md)（permission 是逻辑闸，沙箱是其下的内核闸）、memory `project_containerization_decision`（容器化已否决，本柱不是它）
**状态**：📐 seed —— 够定 scope/决策/估时，实施前细化为完整 PRD。
**估时**：~4-6 天（跨平台沙箱本身 fiddly）

**一句话**：给 seek 的危险操作（bash/write/edit、autopilot 子代理）套一层**内核级沙箱**，把"`--yolo` / 睡觉时自动改代码"从"靠权限逻辑 + worktree 逻辑隔离"升级到"OS 真的拦得住"。

---

## 1. 动机

- 柱 N Autopilot 让 agent **在无人时改代码并 commit**。MVP 的边界是 worktree **逻辑隔离** + 默认无远程——但那是"我们的代码不让它越界"，不是"OS 拦着它越界"。一个被 prompt-injection 带偏、或自己写飞的子代理，逻辑边界挡不住 `rm -rf ~` 或读 `~/.ssh`。
- `--yolo` 同理：全免审批时，唯一防线是模型自觉。
- **Reasonix `main-v2` 有 `sandbox/seatbelt_darwin`**——同论题竞品已经把这层做了，反向验证这是对的原语。
- 这是柱 N 的**可信度倍增器**：有了它，"ships while you sleep" 才敢真放着跑。

## 2. 目标 / 不做什么

### 目标
1. **opt-in OS 沙箱**：confine 文件**写**到允许集合（project root + worktrees + tmp），其余拒；按平台尽量 confine 网络。
2. **主要挂 autopilot 无人值守 + `--yolo`**：交互模式默认不开（不破坏正常用法）；autopilot/cron 无人值守路径可默认开。
3. **内核能力，零 runtime 依赖**：macOS seatbelt、Linux landlock —— 单二进制不变。
4. **防御纵深，不替代 permission**：permission = 逻辑"该不该"，沙箱 = 内核"能不能"。两层并存。

### 不做什么
- ❌ **容器化 / Docker**（重 runtime，已否决；本柱是内核 syscall，不是容器）。
- ❌ **完整 syscall 过滤 / seccomp 策略语言**（MVP 只做 FS confine + 力所能及的网络）。
- ❌ **Windows 强隔离**（无对等内核原语；降级为不开沙箱 + 文档注明，靠 permission + worktree）。
- ❌ 改 `pkg/deepseek`。permission 接口尽量不碰（沙箱在进程/exec 层包，见 D2）。

## 3. 关键决策（seed 级，实施前细化）

### D1 — 平台原语 + 跨平台不对等（必须正视）
| 平台 | 原语 | 能 confine | 缺口 |
|---|---|---|---|
| macOS | **seatbelt**（`sandbox_init` / `sandbox-exec` profile） | 文件 **+ 网络** | `sandbox-exec` 名义 deprecated（仍可用）；倾向 seatbelt API |
| Linux | **landlock**（LSM，Linux 5.13+，`golang.org/x/sys/unix`） | **仅文件系统** | landlock **管不了网络**——网络收紧需 seccomp/netns，MVP 可不做或后续补 |
| Windows | — | — | 无对等；降级（不开 + 提示） |

→ **MVP 诚实定位**：FS 写入 confine 跨 mac/linux 都做；网络 confine 仅 macOS（landlock 做不到）。文档写清这个不对称。

### D2 — 在哪一层套（不碰 permission 核心）
**倾向：进程/exec 层。** 两种挂法：
- (a) **re-exec 自己进沙箱**：autopilot/yolo 启动时，seek 用沙箱 profile re-exec 自身 —— 整个进程（含所有子代理 + bash 子进程）都在牢里。最强，但实现重。
- (b) **只沙箱 bash 子进程**：bash 工具 spawn 时套沙箱（类比柱 K 的 `detachStdin`/`killProcessGroup` 在 exec 层做平台分支）——edit/write 是 seek 进程内的 syscall，需 seek 进程自己 enter landlock/seatbelt。
- 实施前定。倾向 (a) 用于 autopilot（干净、全覆盖），(b) 不够（edit/write 漏网）。**不扩 permission 接口**——沙箱是 exec/进程层的正交防线。

### D3 — 允许集合（默认拒）
- **写**：project root + 各 worktree + `$TMPDIR` + seek 自己的 `~/.seek`（session/checkpoint）。其余拒。
- **读**：宽松（读基本无害；但敏感目录如 `~/.ssh`/`~/.aws` 可选拒）。
- **网络**（仅 macOS）：默认拒，**放行 LLM endpoint + 用户配的 webhook host**（autopilot 要 push）。

### D4 — 触发 / 配置
- `--sandbox`（显式开）；`sandbox.enabled` config；**autopilot 无人值守默认开**（可 `--no-sandbox` 关）。
- 误拒可观测：沙箱拒绝某操作时给清晰错误（"sandbox denied write to <path>; outside project/worktree"），不静默卡死。

## 4. 测试（实施时细化）
- macOS seatbelt：沙箱内写 project ✅ / 写 `~` ❌；网络放行 LLM、拒其他。
- Linux landlock（5.13+ gated，CI skip-if-unsupported）：写 worktree ✅ / 写 `/etc` ❌。
- 降级：Windows / landlock-unsupported 内核 → 不开 + 提示，不崩。
- 与 autopilot 集成：无人值守 run 在沙箱内完成 + 越界被拦。
- 误拒路径有清晰错误。

## 5. 里程碑（seed）
| M | 内容 |
|---|---|
| M-O.1 | macOS seatbelt：FS + 网络 confine + 允许集合 + 误拒错误 |
| M-O.2 | Linux landlock：FS confine（网络缺口文档化）+ 内核 gated 降级 |
| M-O.3 | 接线：`--sandbox` / config / autopilot 默认开 + Windows 降级 + 文档 |

## 6. 风险 / 预埋 pitfall
- **跨平台不对等**（landlock 无网络）——别假装对等，文档 + 错误信息写清。
- **landlock 内核版本**（需 5.13+）——运行时探测，不支持则降级不崩。
- **`sandbox-exec` deprecation**（macOS）——优先 seatbelt API；若用 `sandbox-exec` 注明风险。
- **误拒打断正常活**——所以默认**不在交互模式开**，只 autopilot/yolo 开；误拒给可诊断错误。
- **沙箱 ≠ permission**——别让任一层的存在使另一层松懈；两层都保留（纵深）。
- **若发现必须改 permission 接口**——停下评估（可能该升级为完整 PRD 而非 seed）。

## 7. 与其它柱的关系
- **柱 N Autopilot**：本柱是它的安全加固；柱 N MVP **不阻塞**于本柱（worktree 逻辑隔离先顶上），本柱让它**可信到敢无人值守长跑**。
- **permission（已交付）**：逻辑闸在上、内核闸在下，纵深防御。
- **容器化（已否决）**：明确区分——本柱零 runtime 依赖，单二进制不变。
