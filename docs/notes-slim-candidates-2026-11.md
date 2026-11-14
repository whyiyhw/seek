# 减法机制 L1：首轮 deadcode / unused 扫描——瘦身候选清单（2026-11）

> 卡片：butler `c_rtjjsl` · 分支 `drive/c_rtjjsl` · 扫描日期 2026-11-12 · 基线 commit `5b76ed6`
> **本卡不删任何东西。** 下面所有条目都是候选 + 证据 + 风险分级，删除/收缩动作由后续卡片逐条审批执行。
> 每条候选都附机械复核命令，任何人可在任意机器重跑得到同样结论。

## 执行状态（后续减法卡回填）

| 刀 | 卡 | 删了什么 | 备注 |
|---|---|---|---|
| 首刀 2026-11-13 | c_ajavza | A1/A2/A5/A12（describeAt、WithStatusCallback、DefaultPath、WithHTTPClient） | deadcode 51→47；连带 emitStatus 永_nil 链记为下轮候选 |
| 第 2 刀 2026-11-13 | c_7qjnko | A 类剩余全部（A3/A4/A6–A11、A13、A14）+ C 类全部（C1–C9）+ 连锁（teatest `model` 字段、3 个孤儿 import、testing import）+ gofmt -s 33 文件 + §6.1 计数指针化（appendix-a 不再复述条目数） | 删除前逐条重跑 deadcode 双模式 + U1000 + 全仓 grep 复核零引用；deadcode 43→35、-test 模式清单条目清零。B 类（§2）不满足「零引用」标准未动 |
| 第 3 刀 2026-11-13 | c_l3cw6d | 裁决 4：modernize@v0.23.0 -fix 全量落地（两轮共 285 fix、106 文件、净 -77 行；omitzero 2 条按裁决剔除——dream.go fix 后整文件还原）；连带删自定义 `max`（§5 唯一纯删项）。**修复 modernize 自身引入的回归 1 处**：slices.Backward 修正在 pkg/agent/agent.go `AttachPredictedNext` 把写穿索引改成写 range 副本（静默 no-op，绿灯漏网——该路径零覆盖），已改回带索引写并补回归测试 `TestAttachPredictedNextWritesThroughIndex`（pitfall 已入册 Tooling 分区） | 验收：`go test -race ./...` 82 包全绿；deadcode 双模式 35/-test 1 与第 2 刀基线一致（-test 的 1 条即下述队列里的 skillSetWith）；gofmt -s 零偏差；modernize 残留命中恰为剔除的 2 条 Omitempty。第 9 条复核结论已回填 §2 |

**第 2 刀新增候选（后续刀的队列，本刀按范围纪律未删）**：
- `skilldistill_test.go:163 skillSetWith`：skill-distill M-SD1 合入后新增的死测试助手（C 类同性质），扫描基线 5b76ed6 之后才出现，不在本清单原始条目内。
- `internal/hooks` emitStatus 永_nil 链（首刀遗留）与 `internal/tui/teatest` `modelCh` write-only 通道（本刀删 FinalModel 后唯一读者消失，字段仍有写入方故 U1000 不报）：同属「删除注入器后留下的孤儿读写路径」，需语义判断，不满足机械零引用标准。
- `testModelBuilder.skipDefSize/skipReady` 字段（本刀删 WithSize/SkipReady 后恒 false，Build 仍读取）：行为零变化，可连 Build 无条件化一起删。


---

## 0. 方法与工具（含取舍说明）

### 0.1 deadcode 的引入方式

按卡片首选方案，以 **go.mod `tool` 指令**引入 `golang.org/x/tools/cmd/deadcode`，不进运行时依赖（tool 指令不影响主二进制的 import graph，CI 的 `pkg/deepseek` 零外部依赖纪律不受影响——该纪律只约束 `pkg/deepseek` 包本身和运行时 require 块，本仓 require 块本就有 TUI 全家桶）。

**版本取舍**：钉 `v0.48.0` 而非 `@latest`。实测 `@latest`（v0.52.0）会连带：
- 把 `go` 指令从 `1.25.8` 顶到 `1.26.0`（低于 1.26 的工具链无法再构建本仓）；
- 升级 4 个运行时直接依赖（x/net 0.58→0.61、x/sys 0.47→0.49、x/term 0.45→0.47、x/text 0.41→0.43）。

对一张「只报不删」的扫描卡，这个改动面不可接受。v0.48.0 与仓内既有 x/tools indirect 同版本，最终 go.mod diff 仅 +1 行 `tool` 指令 +3 行 `// indirect`（x/mod、x/telemetry、x/tools——deadcode 自身构建所需），go.sum +6 行。

### 0.2 扫描矩阵

| 扫描 | 命令 | 说明 |
|---|---|---|
| deadcode（生产视角） | `go tool deadcode ./...` | 从 main 包（`cmd/seek`，全仓唯一 main）做可达性分析 |
| deadcode（含测试视角） | `go tool deadcode -test ./...` | 把测试也当根，用于把「仅测试引用」从「真死」中分离 |
| staticcheck U1000 | `go run honnef.co/go/tools/cmd/staticcheck@latest -checks U1000 ./...` | 补 deadcode 看不到的未用类型/变量/常量。模块无关模式单跑，**不进 go.mod**（本机全局 staticcheck 二进制是 go1.25.0 构建的，读不了 go1.25.8+ 的导出数据，故走 @latest） |
| gofmt | `gofmt -s -l .` 对照 `gofmt -l .` | 差集 = 纯 `-s` 简化点 |
| gopls modernize | `go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@v0.23.0 ./...` | **不带 `-fix`，只报不改**。v0.20.0 及以下读不了本工具链（go1.27.3）的导出数据；v0.24.0 已移除该包路径；gopls v0.22/v0.24 CLI 均无 `modernize` 子命令。v0.23.0 是当前唯一可跑的钉扎版本 |

版本偏差是本次扫描最大的环境坑：**结论的复现以工具链版本为前提**（go1.27.3 darwin/arm64）。跨平台项（见 §1 的 sandbox Wrap）需换 GOOS 复核。

---

## 1. 候选 A：全死代码——连测试都不引用（14 项，风险最低）

12 个函数（deadcode 两种模式都判死）+ 2 个仅 staticcheck U1000 能看到的未用符号。这是「下一轮减法卡」最安全的起点。

| # | 符号 | 位置 | 证据 | 量级 | 复核 |
|---|---|---|---|---|---|
| A1 | `describeAt` | internal/checkpointcli/checkpointcli.go:358 | deadcode 双模式死 + U1000 + 非测试引用 0 | ~12 行 | `go tool deadcode ./... \| grep describeAt` |
| A2 | `WithStatusCallback` | internal/hooks/shell_runner.go:117 | 同上 | ~3 行 | `go tool deadcode ./... \| grep WithStatusCallback` |
| A3 | `WithClock` | internal/hooks/shell_runner.go:142 | 同上 | ~3 行 | 同上换 `WithClock` |
| A4 | `ResolveProjectHooksPath` | internal/hookscli/hookscli.go:363 | 同上 | ~6 行 | 同上 |
| A5 | `DefaultPath` | internal/mcpconfig/config.go:59 | 同上 | ~3 行 | 同上 |
| A6 | `WithModel` | internal/suggester/suggester.go:79 | 同上 | ~3 行 | 同上 |
| A7 | `RenderPixelBanner` | internal/tui/banner.go:69 | 同上；其调用的 `bannerWithLettersRevealed` 仅测试引用（B 类） | ~3 行 | 同上 |
| A8 | `WithTimeoutFn` | internal/tui/teatest/teatest.go:207 | 同上；teatest 包注释自述「只实现测试所需子集」，此项在子集之外 | ~5 行 | 同上 |
| A9 | `TestModel.FinalModel` | internal/tui/teatest/teatest.go:252 | 同上（子集外） | ~4 行 | 同上 |
| A10 | `TestModel.Quit` | internal/tui/teatest/teatest.go:284 | 同上（子集外） | ~3 行 | 同上 |
| A11 | `TestModel.GetProgram` | internal/tui/teatest/teatest.go:290 | 同上（子集外） | ~4 行 | 同上 |
| A12 | `WithHTTPClient` | pkg/deepseek/client.go:50 | deadcode 双模式死；**唯一注意点**：`pkg/` 是导出 API，模块外部理论可 import（见风险注） | ~3 行 | 同上 |
| A13 | `triggerSubprocess`（var） | internal/routines/trigger.go:284 | U1000（deadcode 只看函数，变量靠它补） | 数行 | `go run honnef.co/go/tools/cmd/staticcheck@latest -checks U1000 ./...` |
| A14 | `promptSubmittedMsg`（type） | internal/tui/messages.go:49 | U1000 | 数行 | 同上 |

**预估收益**：直接 ~55 行 + 连锁未用字段/导入，合计约 80–120 行。
**风险分级：删函数 / 低**。全部在 `internal/`（包外不可导入）+ 1 个 `pkg/deepseek` 导出项。A12 的唯一保留理由是「给外部消费者留注入自定义 http.Client 的口」——本仓是单二进制 CLI，`WithBaseURL`（B 类，测试在用）已经在承担测试注入口，`WithHTTPClient` 连测试都不用。判删需人在减法卡上拍板一次。
**复核命令**（整类一把梭）：

```bash
go tool deadcode ./... | grep -E "describeAt|WithStatusCallback|WithClock|ResolveProjectHooksPath|DefaultPath|WithModel|RenderPixelBanner|WithTimeoutFn|FinalModel|TestModel.Quit|GetProgram|WithHTTPClient"
```

---

## 2. 候选 B：生产构建不可达、仅测试引用（39 项，风险中——不是「可直接删」）

这些符号在 `go tool deadcode ./...`（生产视角）全部报死，但 `go tool deadcode -test ./...` 里活着——**删除必须连带删/改测试**，所以单列一层，逐项要人判断语义。

按包分组（完整 39 项以复核命令输出为准）：

| 包 | 项数 | 符号 | 语义判读 |
|---|---|---|---|
| internal/tui/teatest | 15 | NewTestModel、Send、Output、WaitFor、WaitFinished、FinalOutput、safeBuffer.Read/Write、WithInitialTermSize/CheckInterval/Duration/FinalTimeout 等 | **测试 harness 包，被测试引用是它的本职**——「生产不可达」对它是伪信号，不构成减法候选（真正死的是它的 4 个 A 类成员）。唯一减法方向：上游 teatest 出 v2 后整包移除（包注释自带的退出条件） |
| internal/controlplane | 5 | OpenBindingStoreAt、BindingStore.Path/List/Delete、NewConvID | bindings 存储的注入构造器/方法：生产用 `OpenBindings` 走默认路径，测试用 `*At` 注入临时目录。典型**测试接缝**。删 = 测试改硬路径，不建议；保留成本也低 |
| internal/paths | 3 | CronTickLock、CronRuns、CronTriggers | cron 的路径函数。生产不可达但**不是测试引用**（它们不在 `-test` 死名单里消失的原因是…… 见复核：`grep -rn "CronTickLock" --include="*.go"` 有测试引用）。若 cron tick 链路真废弃，这三个连同 routines 的调用点一起审视 |
| 其余 16 包各 1 项 | 16 | bgjob.WithRingCap/WithMaxJobs、childenv.Withheld、hooks.WithExecutor、hooksconfig.Sha256File、lspclient.newClient、routines.noopNotifier、routines.OpenStoreAt、sandbox.Wrap、mcptool.FormatErrors、skilltool.New、banner.bannerWithLettersRevealed、upgrade.sha256Hex、deepseek.WithBaseURL、mcp.NewClient、assets.StoreFile 等 | 大多同「controlplane 模式」：Option/注入器，生产走默认值、测试传自定义值。逐项二选一：**(a)** 认定为测试接缝，保留并在注释里写明；**(b)** 连测试一起删 |

**特例必须标注——`sandbox.Wrap`（internal/sandbox/sandbox_darwin.go:29）**：`Wrap` 在三个 GOOS 变体（darwin:29 / linux:54 / other:18）各有实现，deadcode 只跑了 darwin/arm64。darwin 侧仅测试引用；**linux/other 侧未扫**。复核命令：

```bash
GOOS=linux   go build ./... && GOOS=linux   go vet ./internal/sandbox/ 2>&1 | head -3
GOOS=windows go build ./...   # other 变体进 windows 构建
grep -rn "sandbox.Wrap\|\.Wrap(" --include="*.go" --exclude="*_test.go" .   # 生产引用=0（本次已验）
```

**第 9 条复核结论（2026-11-13，卡 c_l3cw6d 回填）——判：保留（三变体整组），linux/other 两变体登记为下一刀候选。**
证据：① GOOS=linux / GOOS=windows `go build ./...` + `go vet ./internal/sandbox/` 全过（含第 3 刀 modernize 改动后）；② deadcode 跨 GOOS 复扫（host 构建 + GOOS env；注意 `GOOS=x go tool deadcode` 会把工具本体也按目标平台编译导致 exec format error，须先 host 构建二进制再传 env）：linux 变体（sandbox_linux.go:54）与 other 变体（sandbox_other.go:18）在生产与 -test 双模式**均死**——`sandbox_linux_test.go` 零 `Wrap` 引用；darwin 变体 -test 模式活——`sandbox_darwin_test.go` 经 `Wrap` 起 seatbelt 真实进程做行为断言；③ 生产消费路径实为 `sandbox.Argv`（internal/tools/bash/bash.go:205，v7 柱 O 落地时走 trampoline 设计）+ `sandbox.Available()`（cmd/seek/main.go:631），`Wrap` 是旧入口、包文档「Use Available() … Wrap() …」表述已与实现错位。判保留的理由：darwin 变体是活跃行为测试接缝，删 = 连坐删真实 seatbelt 断言；linux/other 变体虽双模式死但属平台孪生实现，单删两枚造成 API 面不对称且收益仅 ~20 行。下一刀若删 linux/other 两变体，需连带修订包文档的 Wrap 段落。

**预估收益**：若 16 个散项全判删（含测试），~100–150 行；若全判「测试接缝保留」，收益 0 但换来一份明确的接缝清单（本身也是本卡的产出物）。
**风险分级：删函数 / 中**（每个都可能连坐测试；controlplane 一组还在活跃开发期——M2 刚合入，宁可不动）。

**复核命令**：

```bash
comm -23 <(go tool deadcode ./... | sort) <(go tool deadcode -test ./... | sort)
```

---

## 3. 候选 C：死测试助手——连测试自己都不用（10 项，风险低）

deadcode `-test` 模式 + U1000 双重坐实：这些是**测试文件里写了却没人调**的助手。删了不影响任何测试语义（没有测试引用它们），纯瘦身。

| # | 符号 | 位置 | 证据 |
|---|---|---|---|
| C1 | `fakeStream`（type）+ `(*fakeStream).prompt` | cmd/seek/json_test.go:18,22 | deadcode -test 死 + U1000（type 和 method 各报一条，同一逻辑单元） |
| C2 | `testModelBuilder.WithModel` | internal/tui/testhelpers_test.go:188 | deadcode -test 死 |
| C3 | `testModelBuilder.WithCWD` | internal/tui/testhelpers_test.go:193 | 同上 |
| C4 | `testModelBuilder.WithSize` | internal/tui/testhelpers_test.go:220 | 同上 |
| C5 | `testModelBuilder.SkipReady` | internal/tui/testhelpers_test.go:231 | 同上 |
| C6 | `assertContains` | internal/tui/testhelpers_test.go:303 | deadcode -test 死 + U1000 |
| C7 | `assertNotContains` | internal/tui/testhelpers_test.go:310 | 同上 |
| C8 | `contains` | internal/tui/testhelpers_test.go:319 | 同上 |
| C9 | `indexOf` | internal/tui/testhelpers_test.go:323 | 同上 |

（C2–C5 是 builder 的四个 option 方法，builder 本体在用——只死这四个口。）
**预估收益**：~60–80 行测试代码。
**风险分级：删函数 / 低**（无任何引用方，`go test ./...` 绿即证）。
**复核命令**：

```bash
go tool deadcode -test ./... | grep "_test.go"
go run honnef.co/go/tools/cmd/staticcheck@latest -checks U1000 ./... | grep "_test.go"
```

---

## 4. gofmt -s：32 个文件有格式偏差，但 0 个 `-s` 简化点

`gofmt -s -l .` 与 `gofmt -l .` 输出**完全相同的 32 个文件**（差集为空）；再逐文件对比 `gofmt -d` 与 `gofmt -s -d` 的 diff 行数，无一文件因 `-s` 多出一行——**即 32 个文件全是普通格式偏差（多为注释对齐），不存在任何 `[]T{x}` → `{x...}` 一类的 -s 简化机会**。

抽样证据（internal/diff/diff.go）：`DefaultContext = 3  //` 双空格对齐 → gofmt 要求单空格；internal/routines/flock_windows.go：syscall 参数行注释对齐。

这是「一致性修复」而非「减法」（行数收益≈0），列为候选只为完整性。构成：**21 个 `*_test.go` + 10 个生产文件 + 1 个 testdata**（生产文件：internal/diff/diff.go、internal/memory/distill.go、internal/routines/flock_windows.go、internal/routines/webhook.go、internal/subagent/templates.go、internal/subagent/types.go、internal/suggester/match.go、internal/tools/exitworktree/exitworktree.go、internal/worktree/worktree.go、internal/worktreecli/worktreecli.go；testdata：eval/cases/read-tail-fact/testdata/app.go，不参与构建）。全清单见复核输出。

**风险分级：格式修复 / 极低**（`gofmt -s -w` 一次到位，纯机械）。
**复核命令**：

```bash
gofmt -s -l .                     # 32 个文件
comm -23 <(gofmt -s -l . | sort) <(gofmt -l . | sort)   # 空 = 无 -s 专属简化点
```

---

## 5. gopls modernize：198 条简化点（生产 71 / 测试 127）

工具：`modernize@v0.23.0`（引入取舍见 §0.2）。**本卡一条都不改**。分类统计：

> **执行状态：裁决 4 已由第 3 刀（c_l3cw6d，2026-11-13）落地**——两轮 `-fix` 全量应用后，modernize 残留命中恰为 2 条 Omitempty（internal/memory/dream.go:34/35，按裁决剔除留人判：改了会变序列化输出）。执行时点命中 208 条（较扫描基线 198 多出 ~10 条，系扫描后合入的 web_search / P1-c 路由等新代码带入），连同唯一纯删项（自定义 `max`）全部落地；期间捕获并修复 modernize slices.Backward 写副本回归 1 处（见执行状态表第 3 刀、pitfalls「write-to-copy no-op」条）。

| 类别 | 条数 | 一句话 |
|---|---|---|
| rangeint（三段 for → `for range int`） | 73 | 最大头，纯机械 |
| splitseq/fieldsseq（`range strings.Split` → `SplitSeq`） | 32 | 免分配，机械 |
| atomictypes（`var x int32` + atomic.Xxx → `atomic.Int32`） | 17 | 全在测试 |
| waitgroupgo（`go f()` + `wg.Add` → `wg.Go`） | 17 | 全在测试 |
| slices 循环简化（mapsloop/slicescontains 族） | 14 | 机械 |
| slices.Backward（倒序 range） | 12 | 机械 |
| min/max 替代 if-else | 10 | 机械，生产代码里 8 处 |
| strings.Index/IndexByte/CutPrefix/SplitN 族 | 12 | 机械 |
| 其余（context.WithCancel→AfterFunc、`interface{}`→`any`、omitempty→omitzero ×2） | 5 | omitzero 两条要人看语义 |
| **内置 max 替代自定义 max** | **1** | **唯一纯删除项：internal/hooks/shell_runner_test.go:521 自定义 `max` 函数等价于内建，可整函数删除** |

**预估收益**：不是行数减法（多数是等行数改写），是**可读性/免分配**收益；唯一行数收益是删自定义 `max`（~4 行）。omitzero 两条（internal/memory/dream.go:34 等）语义上「嵌套 struct 字段的 Omitempty 无效」，改了会改变序列化输出，需要人判。
**风险分级：机械改写 / 低**（`-fix` + `go test -race ./...` 绿即证）；omitzero 单独标 / 中。
**复核命令**：

```bash
go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@v0.23.0 ./... | sed 's|.*/drive-c-rtjjsl/||' | tee /tmp/modernize.txt | wc -l   # 期望 199（198 条 + exit status 行）
grep "user-defined max" /tmp/modernize.txt   # 唯一删除项
```

---

## 6. docs 侧：「同一事实两处定义」抽查

### 6.1 pitfalls.md ↔ book/appendix-a：索引复写了 56 个标题，且计数已陈旧

- `docs/pitfalls.md`：**297 KB / 190 条**（`grep -c '^### '`），全仓最大单文件。
- `docs/book/appendix-a.md`（13 KB）自述定位是「踩坑录索引」：A.1/A.2 两张表把书中坑 ↔ pitfalls 条目**逐字引用标题**做对照。
- 实测：190 个条目标题里 **59 个的前 40 字符可在 book/ 中逐字找到，其中 54 个落在 appendix-a**（4 个在 chapter-07 的叙事里——书复述坑是 appendix-a 声明的既定方针「两份内容重叠但不相等」，不算意外重复）。
- **坐实的漂移**：appendix-a 写「截至本书写作时超过 130 条（仍在持续增长）」——现状 190 条，索引头部计数已落后 ~60 条；被引用的 56 个标题若在 pitfalls.md 里改写，索引静默失效，无任何检查。

**候选**（缩文档 / 中风险）：给 appendix-a 加一个 CI 可跑的校验（脚本逐标题 grep pitfalls.md，对不上就 fail——复用 `scripts/extract-pitfalls.sh` 的思路），或者反向：索引只存锚点/行号不存标题文本。**不建议**把 book 的叙事性复述当重复删掉——那是书的本体。
**复核命令**：

```bash
grep -c '^### ' docs/pitfalls.md        # 期望 190
grep -oE '^### .+' docs/pitfalls.md | sed 's/^### //' | cut -c1-40 | while read k; do grep -qF "$k" docs/book/appendix-a.md && echo "$k"; done | wc -l   # 期望 54（54/59 落在 appendix-a 索引，4 在 chapter-07 叙事）
```

### 6.2 notes-memory-skill-gap.md ↔ v1.md：§1 现状盘点复述 v1.md §6

- `docs/prd/notes-memory-skill-gap.md`（14.7 KB，2026-11-12 备料笔记）自述依据 `v1.md`；其 §1「蒸馏漏斗现状」大量复述 v1.md §6 已有事实（证据：`memory_observe` 在 v1.md 出现 36 处 / notes 2 处，`auto_sourced` 40 处 / 12 处——notes 的漏斗表格是 v1.md 事实的另一份表述）。
- **真正的结构性问题**：它的下游设计稿 `docs/prd/feature-skill-distill.md`（30.8 KB，📐 未排期）的「前置阅读」列了 v1/v2/narrative/AGENTS.md/两个 go 文件，**唯独不链这份备料笔记**——同一主题两份事实底座并行，将来只会在其中一份上更新。
- notes 的**独有价值**（不可删的理由）：6 项重复点/7 项失联点盘点、以及坐实的 PRD↔代码偏差（v1.md「硬豁免永不 stale」vs `score.go` 30 天宽限实现）——这个偏差至今没回写 v1.md。

**候选**（缩文档 / 中风险）：feature-skill-distill 落地排期时，把 notes 的独有事实（尤其 PRD↔代码偏差）回写 v1.md 对应小节 + PRD 前置阅读补链 notes，然后 notes 整体移入 `docs/prd/archive/`（archive 目录已有先例）。**现在不动**——设计未排期，notes 是唯一完整盘点。
**复核命令**：

```bash
grep -rn "notes-memory-skill-gap" --include="*.md" . | grep -v "docs/prd/notes-"    # 现状：仅 prd/README.md:43 一处入边
grep -c "auto_sourced" docs/prd/v1.md docs/prd/notes-memory-skill-gap.md
```

### 6.3 notes-event-sourcing-adoption.md ↔ AGENTS.md：结论已吸收，但「部分做」建议未实现、notes 是唯一完整出处

- `docs/prd/notes-event-sourcing-adoption.md`（16.4 KB）的核心结论已被 AGENTS.md:128 / CLAUDE.md:116 吸收（"Approval is the shape to avoid … hybrid, hand-synchronised" 一段即该笔记结论的规则化表述）。
- 但笔记的「部分做一条」——resume 时读 `[plan: approved] (auto-approve-per-step)` 后缀恢复 batch/preApproved——**实测未实现**（`grep -n batch internal/tools/plan/reconstruct.go` 零命中；marker 只在 propose.go:344 发出，reconstruct.go 只匹配前缀）。即笔记仍是这个已知 resume 缝隙的唯一完整陈述，删了就丢事实。

**候选**（缩文档 / 低-中风险）：等该缝隙「修复或明确否决」后，笔记降级归档（`docs/prd/archive/`），AGENTS.md 现有规则句已够日常用；归档前不收缩。
**复核命令**：

```bash
grep -n "batch" internal/tools/plan/reconstruct.go          # 空输出 = 建议未实现（笔记仍承载开放事实）
grep -n "shape to avoid" AGENTS.md CLAUDE.md                # 结论吸收点
```

### 6.4 顺带观察（不构成本轮候选，只记录）

- `docs/pitfalls.md` 297 KB 单文件持续增长，迟早要分卷（按类别/年份拆 + 顶层索引），但拆分方案是设计决定，超出 L1「只报不删」。
- `docs/prd/archive/`（32 KB）已有归档先例，§6.2/§6.3 的归档候选有现成去处。

---

## 7. 收益/风险总表

| 候选 | 类型 | 预估收益 | 风险 | 前置条件 |
|---|---|---|---|---|
| A（§1）14 项全死符号 | 删函数 | ~80–120 行 | 低 | 减法卡拍板 `pkg/deepseek.WithHTTPClient` 是否留 API 口 |
| B（§2）16 个散项 Option/注入器 | 删函数（连测试）或注释定性保留 | 0–150 行 | 中 | 逐项人判「接缝 or 死」；sandbox.Wrap 需 GOOS=linux/windows 复核 |
| C（§3）10 项死测试助手 | 删测试代码 | ~60–80 行 | 低 | 无 |
| gofmt（§4）32 文件 | 格式修复 | ≈0 行（一致性） | 极低 | 无 |
| modernize（§5）198 条 | 机械改写 + 1 项纯删（自定义 max） | 可读性；~4 行 | 低（omitzero 2 条中风险，单独人判） | 无 |
| docs 6.1 索引陈旧 | 缩文档 | 消除静默漂移 | 中 | 先加校验脚本再谈收缩 |
| docs 6.2 备料笔记 | 缩文档/归档 | ~15 KB 去重 | 中 | feature-skill-distill 排期时一并做 |
| docs 6.3 评估笔记 | 缩文档/归档 | ~16 KB | 低-中 | resume 缝隙先修复或否决 |

**总口径**：纯代码行数减法上限 ~300 行（A+B+C 全删）；文档侧可归档 ~31 KB。本卡的真正资产是这份可机械复核的清单本身。

---

## 8. 复核环境快照

- 基线：`drive/c_rtjjsl` @ `5b76ed6`（main 同步点）
- 工具链：go1.27.3 darwin/arm64
- 工具版本：deadcode v0.48.0（go.mod tool）/ staticcheck v0.8.2（go run @latest）/ modernize cmd @ gopls v0.23.0（go run）
- 本仓唯一 main：`cmd/seek/main.go`（deadcode 根）
- **再次声明：本卡不删任何东西。**
