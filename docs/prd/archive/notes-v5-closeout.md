# Notes：v5 收伞评估——M11.5+ 候选三分建议

> 性质：评估笔记（`notes-` 前缀），呈用户拍板；拍板前不为任何候选建卡、不动里程碑表。三分口径：**进 v5 续期 / 并入 orchestration 等 PRD / 不做**。
> 状态：✅ **已拍板·照三分执行**（2026-11-17，用户主会话拍板：三分全批——①②并入/不做按 §0 归位，③ v5 不续期；orchestration PRD「暂拒」处已补去向指针）。
> 日期：2026-11-16 · 卡 `c_sv3zwn` · 分支 `drive/c_sv3zwn` · 依据：`docs/prd/archive/v5.md` §6、`docs/prd/feature-orchestration.md`（状态行 + §4.1）、`docs/prd/archive/feature-routines.md` §9、`docs/prd/feature-slim-loop.md` §2.2、`internal/tools/agent/agent.go:395-403`、butler 2026-11-16「全部开卡」拍板
> 背景：v5 umbrella M11.0–M11.4 全部交付，PRD 索引行「M11.4+ 待定」悬空。本文件收伞：逐候选给去向建议——**无一建议进 v5 续期**。

---

## 0. 三分速览

| 候选 | 建议 | 去向与触发 |
|---|---|---|
| `run_in_background` × `isolation=worktree` 解禁 | **并入 orchestration**（P1 残留，不占里程碑号） | 代码拒因注释自带 revisit 条款（`agent.go:395-398`「Revisit when cleanup can ride the notice path」），设计归宿 orchestration §4.1 D1；触发 = web 任务面板驱动改码型后台子代理、或交互会话实测需求出现（§1.1） |
| 跨 worktree 合并辅助（v5 §6 遗留） | **并入 orchestration**（与上项同故事） | 上项的下半段（并行 bg 子代理各自 worktree → 三方合并回主分支）；同进同出，单独无意义（§1.2） |
| routines 工效三小件（`/routines` 面板、`cron logs`、`cron edit`） | **不做**（本轮不立项；按需单卡） | feature-routines.md §9 原地销账；各 ~0.5–1 天 dot 级，不够 umbrella；触发 = 日常 cron 使用痛点（§1.3） |
| 减法巡检 cron 化 | **不做** | feature-slim-loop.md §2.2 原地（明写「M-SL3 之后的可选项」）；M-SL1 已定属 butler 晨间节奏、L3 首跑 c_z4mn6y 未跑（§1.4） |
| `--max-cost` 熔断 / cron 依赖图 / 跨机同步 | **不做**（现阶） | routines §9 brainstorm 原地；`--max-cost` 等 runaway 实例出现再升（§1.3） |
| v5 §6 旧候选：type registry 开放给 skill / 可选 webhook server | **划掉**（已被吸收） | 前者由 orchestration P2 typed agents（已交付）吸收；后者由 web-frontend + remote-control-plane + mobile-push（均交付）吸收 |
| **v5 续期（开 M11.5+）** | **不建议** | 无候选够「柱」格；编排后续已有 orchestration 在飞（2026-11-16 开卡 4 张）；版本演进规则第 3 条：交付后归档不追加（§2） |

## 1. 候选明细

### 1.1 bg×worktree 解禁——为什么归 orchestration 而非 v5

现状与拒因：P1-a（c_bickfj）交付 `SpawnBackground` 时暂拒组合，拒因是 **worktree 清理骑在工具的同步路径上**——sync 路径 `settleWorktree` 在 `Spawn` 返回后跑 `wtMgr.Cleanup`，后台路径工具立即返回、没有 owner 跑清理（`internal/tools/agent/agent.go:395-403`，注释自带 revisit 条件）。P1-b（c_u4ugnu）后台续话沿用同拒。

解锁工作量（中等）：cleanup 挪进 Manager 后台 runner goroutine（子终态 → 清理 → kept/cleaned 行并进通知 payload）；Manager 需持有 wtMgr（现归 agent 工具私有）；清理沿用 `context.Background()` 纪律（父回合取消不搁浅 worktree，`settleWorktree` 先例）。

归置理由：v5 柱 G 交付的是**同步** worktree 原语；异步生命周期（含其清理语义）归 orchestration P1——PRD 状态行明写「暂拒」是 P1-a 口径。headless 并行 worktree fleet 已被 autopilot（v7 柱 N）覆盖，交互版无实测需求——**暂不立卡、写明触发条件**即可。

### 1.2 跨 worktree 合并辅助——同进同出

v5 §6 brainstorm 项。autopilot 已覆盖 headless 聚合；交互会话版是 §1.1 的下半段（能后台 spawn 改码子代理之后，才有「各自 worktree 的产出怎么合回来」的问题）。建议与 §1.1 一单设计。

### 1.3 routines 其余增强——真需求但 dot 级

§9 现状核对：5-field cron 已交付；`logs` / `edit` / `/routines` 面板 / `--max-cost` 未做（routinescli 子命令实测仅 create/list/delete/run/tick/config）；webhook 端口已被 web-frontend 吸收。三小件是真工效需求但各 ~0.5–1 天，按需单卡挂 §9 原地销账，不值得 umbrella。`--max-cost` 有防 runaway 价值，但 per-job `--timeout` 30min 已兜底，等真实实例再升。依赖图 / 跨机同步 brainstorm 未定，不立项。

### 1.4 巡检 cron 化——时机未到且已有节奏

slim-loop §2.2 明写「首版不另设提醒机制（cron 化属 M-SL3 之后的可选项）」。现状：M-SL1 双周巡检 2026-11-16 已拍板属 **butler 晨间刷新节奏**（不开 seek 卡）；L3 首跑 c_z4mn6y 刚进 todo。等 L3 稳定且「晨间顺带」被证明不够用再回本项。另注：slim 机制 ≤500 行预算含 cron 包装——晚做省行数。（顺带记录：feature-slim-loop.md 头部「依赖两张在飞 L1 卡」已过期——c_vt7fiw/c_rtjjsl 2026-11-13 已 done；归 L3 首跑卡顺手修，本卡不动。）

## 2. 为什么不续 v5

1. 版本演进规则第 3 条：版本交付后 PRD 归档不再追加；M11.4 收尾已含文档同步，umbrella 职责终了。
2. 编排轴的真后续（resume 回灌 M-O1.3、agentsmgr M-O2.3、hooks 注入面 M-O3.x）已有 orchestration PRD 承接且在飞（c_fepxy2 / c_azqudp / c_wsf7us / c_m2whz2）。
3. 剩余候选全 dot 级或 brainstorm 级，无一够柱。

## 3. 拍板后动作

- **同意三分**：本文状态行改「已拍板·照三分执行」；orchestration PRD 状态行的「暂拒」处补一句去向指针（指向本文 §1.1 触发条件）；候选按去向各自开卡 / 留原地。
- **部分改判**：改判项按拍板口径归位，其余照旧。
