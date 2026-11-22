# W 试验田：I4 人审晋升闸闭环设计（evolution-lab）

> 状态：📐 设计稿（2026-11-21，card `c_x5hpbn`，docs-only 零代码）。W 本体**未放行**——等 `c_2aymtn` 复测数（[`../prd/v8.md`](../prd/v8.md) §9.5 补记④）的人裁定，见 §5 冲突清单第 1 条。
> 前置阅读：[`../prd/v8.md`](../prd/v8.md) §3 I4 / §4 柱 W / §9.5 R2 落地记录；[`../prd/feature-eval-v2.md`](../prd/feature-eval-v2.md)（E1 判定口径 / E2 标定 / E6 lane）；[`../../eval/README.md`](../../eval/README.md) §Judgment / §Calibration / §发版前跑什么。
> 一句话：**core 在 worktree 里改壳（工具描述 / 系统提示段 / skill），从 main 树重放 eval v2 做配对 A/B，读数过机械 verifier 后交人审；人批准才进 main，分支永不自动出去。**

## 0. 定位与边界

- v8 §4 柱 W 是自进化闭环的正主，排第三波；依赖的 R2（§9.5）、S1（`internal/friction`）、T2（§9.2）均已落地，但**止损线读数尚待人裁**（§5-1），故本稿只做闭环设计，不动代码。
- I4 原文：自动化不越过人审闸——试验田里 core 可以改壳，晋升进 main 由人决定。本稿把这句话拆成可执行的四个问题：改什么（§1）、怎么重放（§2）、闸怎么设（§3）、跑起来常驻面多大（§4）。
- 与既有纪律的关系：§8「❌ 把 verifier、评估或壳自身的配置放在 core 写得到的地方」不被本设计突破——core 写得到的是**候选分支**，判分权威（expect.json / lane 名单 / case prompt）一律 main 侧，见 §1 两通道不变量。

## 1. 改什么：三类可变体与隔离形状

### 1.1 三类可变体

| 变体类 | main 上的权威落点 | 载体 | 进重放的通道 |
|---|---|---|---|
| 工具描述 | `internal/tools/<name>/` 的 `const description`（如 `bash.go:36`、`read.go:37`）——包级常量，骑每个 tool schema（[`../../eval/README.md`](../../eval/README.md) §Tool-description tuning） | 编译期进二进制 | 候选分支上改常量 → `go build` 出候选 binary |
| 系统提示段 | `internal/sysprompt/sysprompt.go` 的 `rootTpl` / `Compose`（`sysprompt.go:47/:146`） | 编译期进二进制 | 同上 |
| skill | SKILL.md（Anthropic Skills 布局）：项目级 `<project>/.seek/skills/`（git 内）或用户级 `~/.seek/skills/`（`internal/skill/loader.go:46-47` 两级解析） | 数据文件 | 候选分支内暂存（如 `lab/candidates/<id>/skills/<name>/SKILL.md`）→ 重放时注入沙盒 SEEK_HOME |

前两类的「必须重编译才生效」沿用 R2 的论证（v8 §9.5 配置位置段）：重编译本身就是人审可见动作，编译期常量是 core 写不到运行时配置面的同一堵墙。

skill 类的硬边界：**试验田永不写真实 `~/.seek/skills/`**（AGENTS.md 写禁区——只有 `internal/skillmgr` / `internal/skillstats` 可写用户级 skill 状态）。core 改的是 worktree 里的暂存副本；晋升后由人经 `seek skill install`（用户级）或 `skillmgr.WriteProjectSkill`（项目级）安装——memory→skill bridge 的先例（[`../prd/feature-memory-skill-bridge.md`](../prd/feature-memory-skill-bridge.md)）：评审键击本身就是人闸。

### 1.2 隔离形状

- **worktree 分支 `lab/<candidate-id>`**，base = 当前 main，经 `internal/worktree.Manager`（`Create`/`Status`/`Cleanup`/`PruneDiscarded`，内核既有包——试验田用产品自己的 worktree 机制，dogfood）。分支不 push、不并 main；处置走 `Cleanup(ifDirty=discard)` + `PruneDiscarded` 过期清 ref。
- **两通道不变量（本设计的核心约束）**：候选的全部突变面 = **一个 binary 路径 + 一个沙盒 SEEK_HOME 目录**，别无其他。判分权威——`expect.json`（每例判定权威）、lane 名单（`eval/lane-*.txt`）、case 的 prompt/runner——一律 main 侧：重放从 **main 干净树**起跑，binary 参数指向候选构建产物（`eval/run.sh <case> <binary>` 既有接缝，eval/README §Running），skill 经调用方自带的非空 SEEK_HOME 注入（run.sh 原样尊重调用方值，§9.1 引文）。推论：候选分支里改 `eval/` 给自己松闸，对本次重放**无效**（重放读 main），且这类改动必然出现在人审 diff 里——可见的作弊未遂。
- 前缀缓存注记：提示/描述变体天然改变前缀字节。机制要求的是字节**确定性**不是字节不变性（I5 不冲突）；代价是候选轮的缓存 miss，属试验成本，计入 §2 成本预期。

## 2. 怎么重放：接 eval v2（复用不另造）

### 2.1 跑法

从 **main 干净树**起跑（发版命令同款位置，eval/README §发版前跑什么）；唯一来自候选侧的输入是 binary 与注入的 skill：

```bash
# 候选 binary：lab worktree 里构建
git -C <lab-worktree> … # 候选分支上 go build -o /tmp/seek-lab-<id> ./cmd/seek

# ① 靶集：候选自报的目标 case，5 轮（从 main 树跑，binary 指候选）
eval/run.sh --rounds 5 <target-cases…> /tmp/seek-lab-<id>

# ② 回归网：release 名单，5 轮（同 binary）
eval/run.sh --lane release --rounds 5 /tmp/seek-lab-<id>
```

- **靶集**由候选声明（映射示例：改 bash 描述 → `bash-overreach` + `tool-selection`），映射表随晋升 bundle 交人审。靶集含 flaky 例时照其标定界跑（如 `bash-overreach` 0.8@5）。
- **几轮**：统一 5。理由：E2 轮数政策里 5 轮属 flaky/承重层级；对稳定例，同一个 1.0 闸门 5/5 比 3/3 严格——是**收紧不是放松**（发版道统一 5 轮的同款论证，eval/README §发版前跑什么）。
- **回归网用哪条 lane**：`release`（承重轴 + 22 稳定例，gate 1.0）。已知 flaky 与 record-only 刻意不在 release 名单，它们的观测归日常 trend；若靶集本身就是 flaky 例，它在①已按标定界覆盖。

### 2.2 判分口径

**零新判分代码**：每例 `passed_rounds / rounds >= min_pass_rate`（E1 含边界 ≥ 口径），`min_pass_rate` / `record_only` 在 `expect.json`（每例判定权威）；E2 标定表（eval/README §Calibration）是标定记录不是第二真源。record-only 例照跑照录不判红——候选的改善若落在 record 层（如 `mcp-semble-routing-a3-nl` 的路由行为），读记录行的方向性移动，这正好复用 record-only 机制的本义。

### 2.3 provider 车道与基线臂

- **筛选道 = OpenCode Go 订阅道**（§9.1 测试 key 口径的缺省：四旋钮全不设即订阅道，无人值守合法、不烧计量 key）。注意口径（§5-3）：E2 界在 deepseek 车道历史上标定，订阅道（缺省模型 `deepseek-v4.1-flash`）上只做**配对 A/B 的比较读数**——两臂同道同轮数即内部有效；绝对界带车道不确定度。
- **基线臂**：优先复用同车道、同轮数、够新（≤30 天）的已提交行（E2 同源做法：`eval/results/` 历史行）；不新鲜则同车道用 main binary 重跑一次。两臂同 lane、同 rounds、同 SEEK_HOME 形状（除注入的候选 skill）。
- **最终确认道 = 发版 release lane（计量真 key）**，人工/主会话动作——E5/E6 已立的口径（real-key baseline 是 release-lane human action per the provider-lane rule）。筛选不烧计量 key，确认才烧，且由人按下去。
- 成本：每候选 ≈ 1–2 次 release 道量级的跑（订阅道则订阅内）+ 靶集段；被 §4 在飞上限钳住，无无界烧钱。

## 3. 晋升闸（I4）

### 3.1 触发读数：三条件全满足才组 bundle 进人审

1. **靶集全绿**：靶集每例过自身 expect.json 闸门（5 轮口径）。
2. **回归网零伤**：release 名单 gated 例零 FAIL。
3. **配对改善且无退**：至少一项——gate 翻转（基线臂红 → 候选绿）或 record 层方向性移动（同 N 下 pass 计数严格增加）；且无任何 gated 例 pass_rate 相对基线臂落到界下。

效率侧指标（turns / tool_calls / tokens 的配对差）**进材料不进触发**——工具描述调优的本义是「tool-call 序列改变」（eval/README §Tool-description tuning 第 4 步），这类改善由人审读对照表判定，不做自动晋升依据。筛选不达三条件 = 自动拒绝，不进人审（lab core 可改候选重开新分支再筛）。

### 3.2 人审看什么：promotion bundle（写一次，R2 bundle 同形状）

| 材料 | 内容 |
|---|---|
| diff | lab 分支 vs merge-base（含 skill 暂存副本全文） |
| 配对对照表 | 每例：基线臂 vs 候选臂的 rounds / pass_rate / 闸门 / 关键 metric（turns、tool_calls、tokens） |
| 证据链 | 重放 JSONL 行 id（`eval/results/` append-only 行——裁判与人审引用的都必须是真实存在的行） |
| verifier 机械报告 | 见 3.4 |

### 3.3 拒了怎么退回（三分支）

- **晋升**：人在主会话执行 merge（分支永不自动并 main）；skill 类经 skillmgr 安装；butler 卡流转 done。
- **拒·废弃**：`worktree.Cleanup(ifDirty=discard)` → `PruneDiscarded` 过期清 ref；拒因写进卡面事件。
- **拒·留察**：分支与 bundle 保留，卡面 `wake_at` 定再审日（如 30 天，或关联改动落地后）；再开 = 换新基线臂重新走 §2 筛选。

### 3.4 与 R2 verifier 的接缝：晋升产物过 verifier，在人眼之前

复用 `internal/verifier` 四条机械不变量，原样不改装：

1. 主树快照零漂移（试验田全程在 worktree——正是「worktree 隔离子代理写主树、测试全绿」那个历史事故的回归形状，fixture 已在）；
2. worktree 内 `go test ./...` / `go vet` 绿（skill-only / docs-only 候选也跑，应零影响）；
3. bundle 里的证据 id 必须真实存在，否则降级 doubt；
4. 任何机械 fail 钳制总判——裁判说什么都翻不动。

doubt（如无人值守假 key 下裁判 401）**不阻塞**人审但带 caution 标：I4 的最终权威是人，verifier 是分诊不是替代。wiring（verifier 目前挂在 autopilot `fleet.Run` 内按任务触发；lab bundle 是新触发点）归 W 本体实施卡定，本稿不拍实现。

## 4. 机制预算预答

- **常驻面：零 daemon、零 schedule、零新工具 schema**。重放按候选按需触发（R2 同款：触发面=每次按需，非常驻）。夜间观测已由 E6 core lane 的 CI schedule 承担——试验田**不自建第二条夜间道**（重复观测面）。常驻痕迹只有：lab 分支 git ref（受上限）+ `eval/results/` append-only 行（小）+ 既有 `PruneDiscarded` 周期清理。
- **在飞上限**：lab 活分支 ≤3，对齐 D4「在飞卡按人当天能审完的量」的精神；超限不开新候选。
- **一进一出**：
  - 进：至多一个薄 driver（bash，估 ≤200 LOC：串 worktree→build→两段重放→bundle 组装）。首选**零进**——本稿 §2/§3 即操作手册，先纯文档流程跑通前几个候选，driver 只在手序被证笨重后开卡（届时按 feature-slim-loop 走配对减法）。
  - 出（配对减法候选，按序）：① 手工工具描述调优流程（eval/README §Tool-description tuning 的四步 pattern）被吸收为试验田标准轨道，调优史表保留为历史记录；② R3 维持关闭的承诺重申——试验田不给 verdict 另立台账落点，要落先按 v8 §9.5 补记⑤ 复测再拍。
- 本卡（设计稿）自身：零新增机制、零代码——机制预算由本节预答，不产生任何常驻面。

## 5. 口径冲突清单（v8 §W / R2 / eval v2 三方）

| # | 张力 | 处置 / 待谁裁定 |
|---|---|---|
| 1 | **止损线未裁定 vs W 属第三波**：v8 §9「第二波结束时一次过率/人审分钟无改善→不开第三波」；§9.5 补记④ 复测存疑率 24.5%→57.7%（归因治理口径收紧，非 R2 因果），一次过率口径的改善判定悬置 | **主闸**。W 本体开卡前需用户明确裁定止损线不触发（或触发后回头找堵点）。本卡即「等复测数放行」的卡面来源 |
| 2 | **release lane「只在发版前跑」vs 试验田每候选跑**：eval v2 §2.2/E6 把 release 道定为发版前人工闸门，成本论证基于此；本稿将其扩展为回归网 | 订阅道缓解成本，但口径变化应在 W 本体落地时记进 eval README（release 名单语义扩展）。→ 待 eval v2 E6 卡线追认 |
| 3 | **E2 界的跨车道有效域**：标定历史来自 deepseek 车道行；订阅道绝对界带不确定度 | 本稿以「配对比较读数 + 发版道终认」消化，不改 E2 表。若订阅道行数攒够，E2 追加按车道分列标定（归后续卡，非阻塞） |
| 4 | **skill 用户级写禁区**：AGENTS.md 立 `~/.seek/skills/` 只有 skillmgr/skillstats 可写 → 「core 在用户级 skill 上试验」结构性不可行 | 本稿已消化：skill 候限定 worktree 暂存 + 沙盒注入 + 晋升经 skillmgr，与 v8 §4 W 原文「core 在 worktree 里改…skill」字面兼容。无需裁定，记边界 |
| 5 | **verdict 落点缺位**（v8 §9.5 补记③3 同源）：试验田 screen/judge 读数要累积需显式落点 | 本稿先用卡面事件 + bundle 文件承载（零新机制）；落点问题与 R3 复测同判——先复测再拍 |
| 6 | **autopilot pref 车道坑**（§9.5 补记③2 pitfall：autopilot 车道不升 pref、`--yolo` 无处放）：若 W 本体把 lab core 会话挂在 autopilot/fleet 下会撞同一坑 | 本稿不预设挂靠点；W 本体开卡时单独定（或先修 pref 车道，另卡） |

1 是主闸（阻塞 W 本体）；2–6 是接缝注记，不阻塞本设计稿。

## 附：本卡验收对照

| 验收项 | 落点 |
|---|---|
| ① 设计稿存在且含五节 | 本文件 §1–§5 对应卡面 ①改什么/②怎么重放/③晋升闸/④机制预算/⑤冲突清单 |
| ② 与 v8.md / feature-eval-v2.md 无口径冲突或列清单 | §5（六条，1 阻塞 / 5 注记） |
| ③ 未改 AGENTS.md | 本卡 diff 仅新增本文件 |
| ④ go test ./... 绿 | docs-only，commit 前实跑留证（见卡面写回） |
| ⑤ 机制预算预答、本卡零新增 | §4 |
| ⑥ 架构页不适用 | 无组件/边变化；W 本体实施时再评估 |
