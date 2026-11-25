# 判读引擎：结构化判读面（`seek judge`）

**所属版本**：独立 feature（平台化线，与 [`feature-remote-control-plane.md`](feature-remote-control-plane.md)「Frontend 端口」、[`archive/feature-acp.md`](archive/feature-acp.md)「ACP 直连」并列的第三条"外部驱动 seek"路径——这一条驱动的是**推理**，不是交互）
**前置阅读**：[`feature-inspect-rpc.md`](feature-inspect-rpc.md)（method 命名稳定性纪律 + `jsonLine` 字段集稳定的先例）、[`feature-web-frontend.md`](feature-web-frontend.md) §2.5（`--json` 事件流的契约口径）、[`v8.md`](v8.md) §3（I1 边界 / I2 一次性进程）、`cmd/seek/main.go` 的 `jsonLine` / `applyToolsWhitelist` / `--no-save` 分支、`pkg/deepseek/types.go` 的 `ResponseFormat`
**状态**：📐 **设计稿**。只出文档不改代码；落地另开卡（§6 是拆卡建议）。前置实验已跑（2026-11-24，§8.1 实测基线 → [`../notes/2026-11-24-judge-format-baseline.md`](../notes/2026-11-24-judge-format-baseline.md)）；§6 已按实测重排。工作项 1–4 已落（§6 表内逐条标注）；工作项 5 的**纯逻辑内核**已落（`internal/judge`，card c_42a7rf，CLI/退出码/并发仍在其后半）。

**一句话**：seek 有完整的 agent loop、多车道定价、精确到三档的价格表、`--tools` 白名单和 JSONL 事件面——但它们是**给人用**的形状（探索、多轮、流式）。把 seek 当**判读引擎**交给外部程序（第一批消费者：信息流产品 winnow），只差四件事：一个结构化输出面、三个隔离开关、把 `--no-save` 与记忆解耦、一处成本硬顶。**~5 天，零新依赖，不碰 `pkg/agent` / `pkg/deepseek` 接口 / `permission`。**

---

## 1. 为什么这不该是 `-p --json` 的后处理

| | agent loop（`seek -p`） | 判读引擎（`seek judge`） |
|---|---|---|
| 形状 | 探索 → 工具 → 多轮 → 收敛 | 固定输入 → 固定输出 |
| 消费者 | 人（TUI）、UI（`--json` 事件流） | 程序（一行一条终态） |
| 可复现 | 尽力（模型决定下一步） | **verdict 级必须**（实测：reasons 逐字不可复现，§8.1-4） |
| 失败语义 | 人看着办 | 按行隔离，单行 error 不拖垮整批 |
| 提交粒度 | 每轮一次 | 一条一判定 |

`--json` 是**流式事件面**（`text_delta` 逐段吐，给 UI 逐字上屏用）。判读要的是**终态结果面**。硬塞在一起会让两边同时变差：UI 那边要处理"这批输入里的第几条"，程序这边要自己拼 delta、自己容错 JSON。

而且 `jsonLine` 已经承担了对 web 前端的兼容承诺（[`feature-web-frontend.md`](feature-web-frontend.md) §2.5：字段集稳定、新增非破坏）。再加一层语义进去，等于让一个契约服务两类消费者。

**结论：新子命令，不是新 flag。**

---

## 2. 现状审计（v0.12.3，逐条核过代码）

### 2.1 已有，可直接用

| 能力 | 位置 | 状态 |
|---|---|---|
| 子命令分发 | `cmd/seek/main.go` 的 `os.Args[1]` switch（`skill`/`memory`/`agents`/`serve`/`acp`/`goal`/`autopilot` 同款） | ✅ 照抄 |
| 一次 chat 调用 | `pkg/deepseek.Client.Chat` | ✅ |
| 价格三档 | `internal/pricing`：`InputMissPerMTok` / `InputHitPerMTok` / `OutputPerMTok` | ✅ |
| 参数校验 + exit 2 | `errToolsUsage` 先例（`main.go` 顶部 unwrap → `os.Exit(2)`） | ✅ |
| 工具白名单 | `applyToolsWhitelist` | ✅ |
| `--no-project-md` | 已有 flag | ✅ |
| 多车道 | `--provider` / `--model` 启动时选定，车道默认价在 `pricing.go` | ✅ |
| `temperature` | `ChatRequest.Temperature *float64` 已有 | ✅ |

### 2.2 四个缺口（必须补）

#### 缺口 ①：无法表达「零工具」

```go
// cmd/seek/main.go — applyToolsWhitelist
names := parseToolsFlag(flagVal)
if len(names) > 0 {          // ← 空 slice 时不白名单
    reg.Whitelist(names)
}
```

`parseToolsFlag("")` 返回空 slice → 不进白名单 → **全量注册表**。而 `--tools` 的 help 明写「Empty or absent = full default registry」。

**后果**：最小隔离是 `--tools read`（一个只读工具）。纯判读（L1 层，见 §5）拿不到「无工具面」——模型会因为看到 `bash`/`edit` 的 schema 而产生"我该去查一下"的冲动，多花一轮 token，且引入不可复现的路径。

**修法**：新增哨兵值（`--tools=none`）或独立 flag。**不要改空串语义**——那是已发布的契约，`v8` T1 的裸核对照实验依赖它。

> ✅ **已交付**（2026-11-25，card c_lh6tlq）：`-tools none` → `tools.Registry.Clear()`，注册表清空、请求体不带 `tools` 字段（E2E 断言原始字节）；哨兵整值生效，`none,read` 混用 exit 2 并说明「sentinel, not a tool」；`-tools ""` / 缺省仍逐字节等同全量注册表（原 E2E 回归不变）。

#### 缺口 ②：`--no-save` 连带关掉记忆链

```go
// cmd/seek/main.go
if !*noSave {
    memory.LoadOrCreate(abs)      // M 层
    memory.LoadSoul()             // L 层（跨项目用户画像）
    reg.Add(memorytool.NewRecall(memProject)) ...
}
```

「不产生垃圾会话」与「带着品味判读」互斥。判读引擎的典型配置是 `--no-save`（不给用户目录留日均几十条 session）+ 只读记忆（判据要参考 `soul.md`），**现在做不到**。

**修法**：拆成两个正交开关。

| 开关 | 管什么 |
|---|---|
| `--no-save` | 不落 transcript（不变） |
| `--memory=off\|ro\|rw` | 记忆读写（新增，默认 `rw` 保持现状） |

#### 缺口 ③：环境状态无条件渗入提示词

| 来源 | 关闭开关 | 后果 |
|---|---|---|
| AGENTS.md | ✅ `--no-project-md` | — |
| skills | ❌ 无（`skill.Load` 无条件） | 用户装了 20 个 skill，system prompt 多 20 行 |
| MCP | ❌ 无（`mcptool.LoadServers` 无条件） | 用户配了 3 个 server，工具 schema 全量进前缀 |
| memory M/L | ❌ 只能靠 `--no-save` 连带关 | 见缺口 ② |

**同一命令在两个用户的机器上字节不同 → 判定不同。** 这是可复现性最直接的破坏，也是外部集成方最难 debug 的一类问题。

**修法**：`--isolated`（一把关掉 skills + MCP + project_md + memory），或三个独立 flag。**默认值不改成 off**——TUI 用户不受影响。

#### 缺口 ④：结构化输出定义了但没接

```go
// pkg/deepseek/types.go
type ResponseFormat struct {
    Type string `json:"type"`
}
// ChatRequest 里
ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
```

全仓 grep `ResponseFormat` 只有这两处——**定义了，从未使用**。

所以今天的结构化输出只能靠提示词约束 + 祈祷，再加一层正则容错。

**修法**：接上 `{"type":"json_object"}`（DeepSeek 支持的最低要求面），**不做 JSON Schema**（见 §4 D4）。

#### 前置 ④：`-tools` 校验排在 MCP fork 之后（错误路径比正常路径贵）

2026-11-24 实测：配了 MCP 的机器上，`seek --tools nosuch` 先 fork 全部 server（`mcp: loaded 9 tool(s)`）才报 exit 2；`--tools read` 同样白付 fork，只在提示词省 244 tok。

**根因**：唯一一次校验在 `applyToolsWhitelist`，它必须排在最后一个 `reg.Add` 之后（MCP 工具名只有 `ListTools` 之后才存在），而 `LoadServers` 就在它前面——校验天然晚于它要拦的副作用。

**修法**（已交付 2026-11-25，card c_xujwvq）：`checkToolsWhitelistPreMCP`（`cmd/seek/tools_precheck.go`）在 `LoadServers` 之前跑，读同一份 `mcpconfig.Config` 做三分判定：

| 判定 | 条件 | 动作 |
|---|---|---|
| 已成立 | 已注册内建名，或 `postMCPTBuiltInTools`（MCP 之后才注册的内建） | 放行 |
| 可证明不可能 | 非内建，且**没有任何**已配置 server 能提供它 | 当场 exit 2（同一 stderr 措辞，零 subprocess） |
| 推迟 | 命中已配置 server 的 `<server>__` 前缀，或存在 `"prefix": false` 的 server（可注册任意裸名） | 放行到加载后校验 |

「MCP 提供的名字必须加载后才能校验」不复存在：**前缀可判定就前置拒，不可判定的显式推迟**。推迟不是漏洞而是契约的一部分——`TestCheckToolsWhitelistPreMCP_Table` 三分表锁死，E2E 用子进程 pid 文件证明「拒绝时零 fork」（`TestToolsPrecheckE2E_UnknownNameSpawnsNoMCPServer`）。判读/CI 车道的错误路径从此不再付 9 个 server 的启动延迟。

---

## 3. 接口契约

### 3.1 命令形状

```bash
seek judge \
  --rubric   ./taste/info-flow.md \
  --in       items.jsonl \
  --out      verdicts.jsonl \
  --provider deepseek --model deepseek-v4.1-flash \
  --max-cost 0.05 \
  --parallel 4 \
  --isolated
```

`--rubric` 是**调用方持有的文件**，不是 seek 的内置提示词。理由见 §4 D1。

### 3.2 输入：JSONL，id 由调用方给

```jsonl
{"id":"a1","text":"…","meta":{"source":"hn","ts":"2026-11-24T08:00:00Z"}}
{"id":"a2","text":"…","meta":{"source":"rss"}}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `id` | ✅ | **由调用方给，seek 绝不生成**。生成 id 会产生状态，而判读引擎必须零状态 |
| `text` | ✅ | 被判定内容，纯文本 |
| `meta` | ❌ | 透传。seek 不解析内容，原样回填到输出 |

### 3.3 输出：一行一条，同 id，带终态

```jsonl
{"id":"a1","verdict":"keep","reasons":["引用了具体 RFC 编号","与调用方在做的东西直接相关"],"signals":{"relevant":"yes","novel":"yes","actionable":"no"}}
{"id":"a2","verdict":"gray","reasons":["不能确认是不是同一件事的转载"],"signals":{"relevant":"gray","novel":"gray","actionable":"no"}}
{"id":"a3","error":"model returned malformed verdict"}
{"_meta":{"model":"deepseek-v4.1-flash","rubric_sha256":"8f3a…","items":50,"kept":12,"dropped":30,"gray":8,"prompt_tokens":15420,"cache_hit_tokens":14000,"output_tokens":2100,"cost_usd":0.0047}}
```

**五条不变量**：

1. **`id` 回填，不靠顺序对齐**。输出顺序仍与输入一致，但契约不保证——调用方必须按 `id` 对回自己的库
2. **`verdict` 三值**：`keep` / `drop` / `gray`。与 `internal/verifier` 的 `pass\|fail\|doubt` 同构（`v8.md` §9.5）。**`gray` 是设计的核心**——不确定的不扔，单独堆给人工过。这条抄自 skill 蒸馏的四层漏斗纪律（[`feature-skill-distill.md`](feature-skill-distill.md) §4：「宁可漏过，不可放过」）
3. **`reasons` 是短句，不是散文**。散文不可校验，也扫不出问题
4. **`_meta` 恒为最后一行**，带 `rubric_sha256` 供审计——判据变了导致判定变了，能定位到是哪一次。另带 `temperature` / `effort` / `model`（实测 §8.1-4：三者决定漂移幅度，缺了无法解释两次判定的差异）
5. **单条失败只污染该行**，绝不整批失败

**留着 `signals` 的位置**：判据拆成几个正交信号（相关 / 新颖 / 可动作 / 可疑），比一个 0-100 分可解释得多——分数不可纠正，正交信号可以逐条推翻。但 `signals` 的具体键由调用方的 rubric 决定，seek 只透传不解释。

### 3.4 退出码

| 码 | 含义 | 调用方该做什么 |
|---|---|---|
| 0 | 全部判完 | 正常消费 |
| 1 | 部分失败（输出里有 `error` 行） | 读 error 行，决定重试哪些 id |
| 2 | 用法/配置错误 | **不要重试**，修参数 |
| 3 | 预算耗尽（`--max-cost`） | 收下已产出的，下一批再来 |

`1` 与 `2` 必须分开——`1` 是"有东西没判成"，`2` 是"你参数写错了"。这个区分是给 CI 用的，照抄 `errToolsUsage` 的既有约定。

---

## 4. 六个设计决策

### D1：rubric 是文件，不是内联 prompt

理由三条：

1. **可 diff** —— 判据是调用方的资产，它的变更应该可追溯（"上周我把哪条放宽了"）
2. **可跑评** —— 改完判据跑对照，看分数升还是降（`eval/` 是唯一的客观信号，见 §9）
3. **可审计** —— `_meta.rubric_sha256` 记指纹

**反面：让 seek 用 `soul.md` 当判据。** 那是**用户机器上的环境状态**，会让判读行为随别人的机器漂移，不可 diff、不可跑评、不可复现。判据必须由调用方持有。

### D2：`--isolated` 是显式选项，但外部集成方必须开

默认值保持现状（TUI 用户零感知）。但契约文档里要写死一句：

> 需要可复现判定的调用方**必须**传 `--isolated`，否则判定结果依赖于执行者的 `~/.seek` 状态。

这要求缺口 ② 的耦合先被拆开，否则 `--isolated` 无法表达"关 skills/MCP 但保留 memory 只读"。

### D3：默认单条调用 + 有界并发，批处理显式开

| | 单条一次 | 一批一次 |
|---|---|---|
| 失败隔离 | ✅ 一条坏不污染其余 | ❌ 一条坏可能拖垮整批 |
| 并发 | ✅ 天然可并行 | ❌ 串行 |
| 缓存 | ✅ rubric 前缀字节相同 → 命中 | ✅ 同左 |
| 跨条目上下文 | ❌ 不知道三条讲同一件事 | ✅ 知道 |
| 成本 | 略高（每调用一次 overhead） | 略低 |

默认 `--parallel 4`——照抄 `internal/controlplane/executor.go` 的 `MaxParallel` 姿态（PRD D-C4「全局并发上限，默认 ~4」）。需要跨条目判断（去重、话题聚类）时显式 `--batch`。

### D4：不实现 JSON Schema

诱惑是 `--schema verdict.schema.json` 做完整校验。**别做。**

1. stdlib 没有 JSON Schema；引依赖违反 `pkg/deepseek` 零外部依赖的硬约束
2. 自写子集会成为长期负债（永远有人需要 `oneOf`）
3. **判读的输出形状是固定的**（id / verdict / reasons / signals），不需要通用 schema

**做法**：Go 里校验固定信封——`id` 非空且是已知输入 id、`verdict` 在枚举内、`reasons` 非空。失败 → 该行变 `{"id":"…","error":"…"}`，继续下一条。

### D5：从流式改一次性，但保留 replay 摘要

判读不需要流式（没人盯着单条的 token 一个个出来），但它需要**可复盘**：

```jsonl
{"_trace":{"call_seq":1,"req_sha256":"…","model":"…","temperature":0,"attempts":1}}
```

只记摘要，不记全文——原文在调用方那里，seek 不该复制。这是 `AGENTS.md`「不建平行状态，只吐事实」的直接兑现。

### D6：`temperature=0`，但契约必须声明不确定性

`types.go` 已有 `Temperature *float64`，判读路径固定传 0。

但**不要说"结果是确定的"**。LLM 即使 temperature 0 也有波动（浮点非确定性、批处理差异、provider 版本漂移）。契约里要写：

> `verdict` 是**建议**，不可作为唯一决策依据。`gray` 必须由人复核。

这不是免责声明，是**设计约束**——它保证调用方必须保留人环，也就保证了这个引擎天然安全。

---

## 5. 分层：三种判读，只做两种

| 层 | 形状 | 工具 | 成本 | 做不做 |
|---|---|---|---|---|
| **L1 纯判** | 一条文本 → 判定 | 无（`--tools=none`） | ~$0.0001/条 | ✅ 先做 |
| **L2 判 + 查** | 需要验证陈述（抓原文、读附件） | `read` / `webfetch`（只读） | +工具轮次 | ✅ 做，复用 `--tools read,webfetch` |
| **L3 判 + 干** | 判定后执行动作 | `bash` / `write` / `edit` | 高 | ❌ **明确不做** |

**L3 不是判读引擎，是执行器。** 混进来会让权限模型、成本核算、失败语义全部变糊。需要"判完了自动去办"时，那是两个进程的事：`judge` 输出 → 调用方决定 → 调 seek 的另一个面（`-p` 或 ACP）。

L2 零新机制——`applyToolsWhitelist` 现成。

---

## 6. 实现落点与工作量

| 序 | 项 | 落点 | 工作量 | 依据 |
|---|---|---|---|---|
| 1 | `--effort` / `--temperature` 旗标 | **已落地**（c_trrsn7）：`cmd/seek/main.go` 加两个旗标 + `flag.Visit` 区分「没传 / `--effort off`」；`pkg/agent.Config.Temperature *float64` 下到两条车道；三个二线适配器（openai / anthropic / gemini）的线格式各带一个指针字段 | 0.5 天 | 实测 §8.1-3：成本 3.2× |
| 2 | 空返回的机器可判定信号 | `--json` 事件流加 `finish_reason` 或 error 事件（`pkg/agent/agent.go` 的 `placeholderTurn` 自愈闸 / `emptyResponseMarker`、`budgetExhaustedMarker` 两个常量——按符号找，行号会漂） | 0.5 天 | 实测 §8.1-2：静默空返回 exit 0 |
| 3 | `--tools=none` 哨兵 | `applyToolsWhitelist` 分支（不改空串契约） | 0.5 天 | 实测 §8.1-5：安全边界 |
| 4 | 参数校验先于 MCP fork | `cmd/seek/main.go` 启动段（内建名预校验；「MCP 名校验依赖加载完成」的冲突显式解决） | 1 天 | 实测 §8.1-5：缺口③加重 |
| 5 | `seek judge` 骨架（JSONL in/out、有界并发、退出码、`_meta`） | 新文件 `cmd/seek/judge.go` + `internal/judge/`（纯逻辑，可表驱动测试）。**前半已落地**（c_42a7rf）：`internal/judge` = 输入行解析（`{id,text,meta}`，meta 原样回填）、单条判读（一次 `deepseek.Chat`，system=固定前导+rubric、user=条目 DATA，请求不带 tools / 不带 response_format）、verdict 三值解析与正则容错、按行失败隔离、`_meta` 聚合（model/rubric_sha256/items/kept/dropped/gray/tokens/cost/temperature/effort）+ `JSONL()` 渲染（`_meta` 恒最后一行）。**未落**：`cmd/seek/judge.go` 的旗标/JSONL 文件 I/O/退出码/有界并发——它们依赖工作项 6/7 | 2-3 天 | — |
| 6 | `--isolated` + 拆 `--no-save` × `--memory` | `cmd/seek/main.go` 启动段 | 1 天 | 缺口② |
| 7 | 成本硬顶 `--max-cost` | `internal/pricing` 现成三档 + 累加 | 0.5 天 | — |
| —（可选） | `ResponseFormat` 接线 + 固定信封校验 | `pkg/deepseek` 接已有字段；校验在 `internal/judge` | 1 天 | 实测 §8.1-1 降级：输出变复杂后再上 |

**总计 ~6-7 天**（1–4 是 judge 之外所有 `-p` 程序消费者共享的 seek 侧修补，先落地先受益；5–7 是 judge 本体）。原排布以 `ResponseFormat` 为必修、总计 ~5 天——实测把它降级、把 1/2 两项提上来，重排依据全部在 §8.1。

**不碰**：`pkg/agent`（判读不走 agent loop，直接一次 `Chat`）、`pkg/deepseek` 的接口（只接已有字段）、`internal/permission`（判读面不开 bash，权限问题不存在）。

**测试 bar**（照 `AGENTS.md` 的失败路径要求）：

- 取消：`ctx.Done()` 中途切断，半截输出必须是合法的行（不能吐半行 JSON）
- malformed：模型吐非法 JSON / 缺 `id` / 编造了输入里没有的 `id` → 该行 `error`，其余行不受影响
- 并发：`-race` 下 N 条并发，输出行序与输入一致（或明确不保证，取其一并测死）
- 预算：`--max-cost` 触发时退出码 3 且已产出的行**完整**
- 持久化：零状态——跑完 `~/.seek/` 除 `--memory=rw` 外无任何新增文件

---

## 7. 明确不做

| 不做 | 为什么 |
|---|---|
| **当库 import** | Go 的 `internal/` 规则挡住，而判读需要的东西（memory / pricing / paths / sysprompt）全在 `internal/`。想库化 = fork 或拆包，是另一个项目，与本 PRD 无关 |
| **HTTP 服务判读** | 那是调用方该有的东西。seek 提供**进程**，不提供服务。要 HTTP 就走 `seek serve` + ACP，那是另外两条 PRD |
| **队列 / 重试 / 去重 / 调度** | 调用方的事。seek 要么给 `cron`（它自己跑），要么给一个纯函数（调用方自己排） |
| **持久化判读结果** | 结果属于调用方的数据库。seek 只吐 JSONL，**零状态** |
| **通用 schema DSL** | 见 D4 |
| **内置 IMAP / RSS / HTML 正文提取** | 破单二进制。取件是调用方的事 |
| **向量库 / 语义索引** | `v8.md` §8 已明确否决 |

---

## 8. 写代码前的前置实验

用今天的 seek 跑一遍，验证三件事（半小时）：

```bash
seek -p "$(cat taste/info-flow.md)

条目：<text>

只输出 JSON：{\"verdict\":\"keep|drop|gray\",\"reasons\":[...]}" \
  --json --no-save --no-project-md --tools read --max-turns 1
```

1. **单条成本**（读 `turn_end` 的 `prompt_tokens`/`completion_tokens` × `pricing.go` 三档）
2. **模型对固定 JSON 形状的服从率**（十次里几次吐合法 JSON）——决定缺口 ④ 的紧迫度
3. **`--tools ""` 是否真全量**（代码上是，真机复核一次）

第 2 条的结论直接决定 `ResponseFormat` 是"必须"还是"锦上添花"。

### 8.1 实测基线（2026-11-24，已跑）

完整记录与数据：[`../notes/2026-11-24-judge-format-baseline.md`](../notes/2026-11-24-judge-format-baseline.md)（50 跑批 + 专项；样本为合成——本机无本地邮件，**结论只对格式成立，不对判据成立**，永不进 `eval/` 当 case）。五条主结论，全部已回写本 PRD：

1. **服从率 100%**（50/50，长文 5/5）→ 缺口④降级为可选加固，§6 重排。
2. **真正的失败模式是静默空返回**：exit 0、中文 marker 伪装成 `text_delta`、烧满 token 才吐（`--max-tokens` 压低 100% 复现）→ §6 新增工作项 2；§3.3 不变量 5 的具体形状是「调用方匹配 `[empty:` 前缀」，而不是看 exit code。
3. **成本 92% 在 output、`sessionEffort` 写死 max** → §6 新增工作项 1（低 effort 输出 ~150 tok vs ~600 tok，3.2× 差）。
4. **确定性只到 verdict 级**：reasons 逐字永不重复；漂移方向全往 gray 走（安全侧的抖法，但 gray 占比高于直觉）→ §1「必须」收敛为 verdict 级；§3.3 `_meta` 补 temperature/effort/model。
5. **注入防住是组合运气**（判据措辞 + `--max-turns 1` + 工具面三者叠加），不是架构保证 → 缺口①升级为安全边界；缺口③加重：`--tools read` 下 MCP 子进程照样 fork、`--tools nosuch` 先 fork 再 exit 2 → §6 新增工作项 4。

---

## 9. 契约成立的三个前提

**判读引擎的价值 = 稳定的判据 × 稳定的输入形状 × 可审计的输出来源。三者缺一，它就是个贵的 if 语句。**

- 判据 → rubric 文件 + `rubric_sha256`
- 输入 → JSONL + 调用方给的 id
- 来源 → `_meta` 带 model / rubric hash / cache 命中 / 成本

**它不提供的**是「判得对」。那个没有捷径——只能靠调用方维护的 `eval/` 一点点标定。seek 侧的责任是**让它可测**（`_meta` 可复现、单条可重跑），不是让它正确。

---

## 10. 与其它 PRD 的关系

- **[`feature-inspect-rpc.md`](feature-inspect-rpc.md)**：那边是**读数据面**（session/memory/stats 的只读 method 集），这边是**跑推理面**。两者不重叠：需要"seek 帮我判一批"走本 PRD 的子进程；需要"seek 里存了什么"走 `/rpc`。
- **[`feature-remote-control-plane.md`](feature-remote-control-plane.md)**：那边把 seek 变成"随时够得着"（交互驱动）；这边把它变成"可以喂料"（批处理驱动）。共享「零 daemon、直连子进程、核心 agent loop 不动」的哲学。
- **[`v8.md`](v8.md)**：本 PRD 不改内核边界（I1），不改一次性进程模型（I2）——`judge` 是一个新的**端**，跑完即退，零常驻。
- **[`archive/feature-paseo-provider.md`](archive/feature-paseo-provider.md)**：那是 ACP 车道（每会话一个进程，双向交互）；这是判读车道（一次批处理，单向）。前者被编辑器驱动，后者被程序驱动。

**架构约束继承**：`pkg/deepseek` 零外部依赖不变；prefix-cache 纪律不变（判读是一次新会话，rubric 前缀在批内字节相同 → 批内命中）；工具 schema 零改动。
