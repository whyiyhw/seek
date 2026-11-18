# MCP 生态集成：以 Semble 为第一验证目标

**主题**：seek 的 MCP 客户端（M5.4）已交付，但"能连 MCP server"不等于"agent 会用"。本 PRD 以 Semble 语义代码搜索为第一验证目标，定义 MCP 工具在 agent 循环中的最佳实践：prompt 工程、工具路由、效果评估。

**状态**：📐 设计完成（可开卡，2026-11-16）。MCP 客户端基础设施已于 M5.4 交付（`45c71af`）；§二–§六 是深度集成的落地设计与 D1–D5 卡片拆分，§七起为背景材料。设计核实过的代码落点以本文为准——旧版所写 `pkg/agent/prompt.go` 不存在，system prompt 组装实际在 `internal/sysprompt`。

## 一、现状审计

### 1.1 已交付的 MCP 基础设施

| 模块 | 文件 | 功能 | 状态 |
|------|------|------|------|
| `pkg/mcp/` | client.go, types.go | JSON-RPC 2.0 over stdio，含 initialize / list_tools / call_tool | ✅ `45c71af` |
| `internal/mcpconfig/` | config.go | 从 `~/.seek/mcp.json` 加载配置，兼容 Claude Code 格式 | ✅ `45c71af` |
| `internal/tools/mcptool/` | bridge.go | MCP Tool → seek Tool 适配，含冲突时前缀（`server__tool`） | ✅ `398448e` |
| `cmd/seek/main.go` | L1413–1440 | 启动时调用 LoadServers 注册 MCP 工具到 Registry | ✅ 已集成 |
| `docs/book/chapter-12.md` | — | 完整的设计文档 | ✅ `b55aede` |

用户只需在 `~/.seek/mcp.json` 中添加配置，重启即可使用 MCP 工具。

### 1.2 但"能用"不等于"会用"——三个具体缺口

MCP 桥接只是给了 agent 调用外部工具的**能力**。对照代码，缺口比旧版 PRD 记录的更具体：

**缺口 1：工具名无归属标识，路由决策缺输入。** Semble MCP server 实际暴露两个工具（2026-11-16 核实）：`search(repo, query, content?)` 与 `find_related(file, line)`。按 bridge 现行 just-enough 策略（chapter-12 §12.6，仅在冲突时加前缀），它们以裸名 `search` / `find_related` 注册。模型看到 `search` 这个名字，既不知道它来自 Semble、也不知道它做语义代码搜索——名字本身不携带任何路由信息。Claude Code 对 MCP 工具一律加 `mcp__<server>__<tool>` 前缀，正是为了让模型从工具名直接读出归属。

**缺口 2：server 的工具 description 已直发模型，但缺"何时该用我"的路由语义。** `pkg/agent/translate.go` 把每个 Tool 的 `Description()`/`Schema()` 原样放进每次请求的 tools 数组，Bridge 对 MCP 工具透传 server 给的 description（`bridge.go:36`）。这条最高杠杆通道（见 `eval/README.md` "Tool-description tuning"）已经通到 MCP 工具——但 server 写的 description 通常描述"我是什么"，不描述"与宿主的 grep/read 相比何时选我"。路由知识只有宿主（seek）有：它知道自己有 grep、references、read。

**缺口 3：system prompt 对 MCP 零感知。** `internal/sysprompt.Compose` 的 `Header` 只有四段（Cwd / ProjectSection / SkillManifest / Date），`rootTpl` 常量里的 "Available tools:" 只列内置工具。模型收到的系统级工具世界观里根本没有 MCP 工具的位置——工具 schema 虽然在请求里，但"这个工具如何参与我的工作流"（搜索→read→edit 闭环中站在哪一环）没有任何系统级说明。

## 二、引导与路由的设计空间

### 2.1 三层落点

引导 MCP 工具使用，seek 里有三条现成通道，杠杆与成本各不相同：

| 层 | 通道 | 每请求成本 | 字节纪律 | 先例 |
|----|------|-----------|---------|------|
| L1 工具 schema 层 | Bridge 的 `Description()` | 0（字符串本来就发） | bridge 实例 session 内固定即可 | 内置工具的 description tuning 表（eval/README.md） |
| L2 system prompt 层 | `Header` 新增 `MCPSection`（SkillManifest 同款第五段） | 无 MCP = 0 字节；有 MCP ≈ 每 server 5–8 行 prefix 增量 | **per-session 静态**，session 内逐字节不变；server 按 name 字典序 | `SkillManifest`（per-session 静态、跨 session 可变） |
| L3 项目层 | 项目 `AGENTS.md` 的 Tool usage workflow 节 | 已有通道，不新增 | 用户/项目自管 | 本 repo AGENTS.md 的 grep→read 工作流 |

三层不互斥：L1 保证模型**看工具名就认得出**（缺口 1），L2 给出**路由规则与工作流位置**（缺口 3），L3 是项目作者对特定 repo 的覆盖（如"本仓库 vendor/ 目录不要索引"）。

### 2.2 原方案 A/B/C 的重定位

旧版 PRD 的三个候选方案（A prompt 引导 / B 调度层路由 / C 搜索→编辑闭环封装），经代码核实后重定位如下：

- **方案 A（prompt 引导）→ 展开为 §三 的三层设计，是主路径。** 与本 repo 的行为调优哲学一致：`eval/README.md` 已确立"tool description 是最高杠杆的行为杠杆"——grep→read 工作流就是纯靠 rootTpl + workflowReminder + AGENTS.md 引导成型的，没有一行调度代码。MCP 引导应复用同一机制。
- **方案 B（调度层自动路由）→ 落点修正 + 降级为数据触发的后手（§四）。** 原设想在 `pkg/agent/agent.go` 加 toolRouter 拦截模型选择——**否决这个落点**：它侵入 agent 核心循环，把"选工具"从模型手里拿走，与本 repo 用描述调行为的哲学相反。修正后的后手形态是 **grep 工具结果内的 `[hint:]` advisory**（bash 工具的 `[hint: ...]` 先例，eval/cases/bash-overreach 修复所用）：grep 空结果/低信噪比时在结果尾部加一行"考虑 mcp 搜索类工具"。零循环侵入，只在引导失灵时兜底。
- **方案 C（`plan_and_edit` 封装闭环）→ 明确否决。** seek→edit 的编排已经由模型在单轮内自主完成（grep→read→edit 就是证据）；把编排封装成复合工具是拿灵活性换抽象，且复合工具的 schema 与权限面都会膨胀。Semble 结果天然带 file 定位，read/edit 衔接靠 L2 引导一句话即可。

## 三、Prompt 引导设计（D2/D3 卡依据）

### 3.1 MCPSection：Header 第五段

`sysprompt.Header` 增加 `MCPSection string` 字段，`Compose` 在 SkillManifest 之后追加（空串 = 不追加任何字节）。要点：

- **无 MCP 用户零失效**：`MCPSection == ""` 时 `Compose` 输出与现状逐字节相同——现有 golden test（`sysprompt_test.go`）原样通过，这是设计约束也是回归护栏。
- **有 MCP 用户接受一次性 prefix 失效**：与 `/new` 或 skill 清单变化同级，可接受。
- **session 内静态**：内容 = 启动时注册成功的 server 名单 + 文案，运行期不随工具调用变化（调用统计绝不进 prompt——那是 cache-volatile 数据）。
- **server 按 name 字典序**：堵住 chapter-12 §12.6 记录的 map 迭代序不确定缺陷在 prompt 层的复发。
- **subagent 自动继承**：`ComposeSubagent` 复用同一 `Header`，子代理与父代理看到同一份 MCPSection，无需额外接线。注意编排 P1 的模型路由（explore→GLM 量车道）会换 provider，MCP 工具非 DeepSeek-only，路由后仍可用。

### 3.2 文案生成规则

MCPSection 由 `mcptool` 在 LoadServers 成功后组装（新函数，如 `PromptSection(servers []ServerInfo) string`），按 server 分组，每 server 目标 ≤8 行：

```text
MCP tools (external servers loaded this session):

- semble (semantic code search, local & indexed):
  Tools: semble__search(repo, query, content?), semble__find_related(file, line).
  Routing: natural-language intent ("where is auth handled", "find the retry
  logic") → semble__search; known exact symbol/string → grep (cheaper, exact).
  After reading a hit, semble__find_related(file, line) finds semantically
  similar code. Follow up with read() for exact line context before editing.
```

每个 server 的说明文案来源优先级：

1. **`mcp.json` 的可选 `hint` 字段**（用户/项目自定义，最高优先）——用户比 seek 更懂自己的 server 用途；
2. **seek 内置 known-server 文案表**（第一版仅 `semble` 一条，键 = server name）；
3. **通用降级文案**：server name + 工具名列表 + server description 首句（不解读、只陈列），杜绝 seek 对不认识的 server 瞎编用途。

路由规则段（仅当该 server 是搜索类时给出；判定 = known-server 表标注，不自作聪明分类）源自 Semble 的 `is_symbol_query` 启发式（§7.3）翻译成 prompt 语言：**精确符号/字符串 → grep；自然语言意图 → 语义搜索；已定位代码找同类 → find_related**。

### 3.3 与 L3 的关系

L2 只写与内置工具的路由关系（通用）；项目特有的覆盖（"本仓库某目录别索引"、“这个 monorepo 用 X server 而不是 Y"）归 AGENTS.md——项目作者已有通道，seek 不在 L2 替他们做决定。本 repo 自己的 AGENTS.md "Tool usage workflow" 节是否加 Semble 条目，等 D4 数据说话（避免无证据时把官方工作流改成依赖一个可选组件）。

## 四、工具路由设计（D1 卡 + Phase 3 决策依据）

### 4.1 命名：默认 server 前缀（D1）

**变更 bridge 现行策略**：MCP 工具一律注册为 `<server>__<tool>`（如 `semble__search`），不再"仅冲突时前缀"。

- 理由：路由可辨识性是引导的前置（缺口 1）——`semble__search` 自带归属与语义，`search` 什么都不说；L2 文案里的路由规则也依赖稳定的可引用名字。
- 先例：Claude Code 的 `mcp__<server>__<tool>` 三段式即为此目的；seek 用双下划线单前缀即可（无 `mcp` 顶层域需求）。
- 代价：存量用户已习惯的裸名变化——MCP 用户基数尚小（M5.4 后无已知大规模部署），一次性变更成本最低点就是现在。
- **同卡顺手修 chapter-12 已知缺陷**：`LoadServers` 按 server name 字典序遍历 `mcpServers`（排序后遍历），使同一份 mcp.json 两次启动的注册名/顺序逐字节确定。
- 兼容逃生口：`ServerEntry` 加可选 `"prefix": false` 字段可退回裸名（只为兜底，不做宣传）。

### 4.2 软路由：模型自主选择 + 引导（主路径）

不写路由代码。模型在 L1（带归属的工具名 + server description）+ L2（路由规则段）双引导下自选。这与内置工具间已有的 grep/references/list_dir 分工机制完全同构——references 工具没配 gopls 时模型自己降级 grep，靠的就是 description 里的降级条款。

### 4.3 硬路由后手：grep 结果内 hint（Phase 3，数据触发）

若 D4 评估显示引导失灵（触发条件见 §5.4），落点是 grep 工具的**结果内 advisory**，不是调度层：

```text
[hint: no strong match for this natural-language query — if a semantic search
MCP tool is available (see MCP tools section), it may locate intent-based
targets faster; otherwise try broader terms or list_dir]
```

仅当（a）注册表中存在搜索类 MCP 工具 且（b）grep 零命中或结果全部 <N 字符上下文时追加。先例：bash 工具的 `[hint: ...]`（`eval/cases/bash-overreach` 修复路径）。零核心循环侵入，不改正文只加尾注。

### 4.4 明确否决

- **`agent.go` toolRouter 拦截**（原方案 B 落点）：侵入核心循环；查询分类错误时模型无法纠正（它根本看不到被改写的路由）；与"描述调行为"哲学冲突。
- **查询预处理层**（在消息进 API 前改写/注入）：违反 prefix-cache 字节纪律（AGENTS.md "Token & prefix-cache constraints"）。

## 五、效果评估设计（D4 卡依据）

### 5.1 三臂对照口径

同一 binary、同一 prompt 集、同一 repo（seek 自身），仅环境差异：

| 臂 | mcp.json | 引导（MCPSection） | 度量什么 |
|----|----------|-------------------|---------|
| A1 基线 | 无 semble 条目 | — | grep/read 现状工作流的天花板 |
| A2 | 配 semble | 关（`mcp_prompt: false` 开关，同卡新增） | 工具**存在但无引导**——隔离引导的净效应 |
| A3 | 配 semble | 开（含 D3 文案） | 完整深度集成形态 |

臂间隔离机制：每臂独立 `SEEK_HOME`（`mcpconfig.Load` 走 `$SEEK_HOME/mcp.json`，天然支持），eval 的 `runner.sh` 钩子负责铺设（`eval/README.md` 的 SEEK_HOME 先例已有）。

### 5.2 指标集

**主指标（有效性）**
- `gold_hit`：final answer 是否命中 gold `file:line`（±5 行容差）。从已入库的 `final_text`（2000 char）离线评分，脚本判 + 人工抽检。

**效率指标（成本收益）**
- `turns`、`completion_tokens`（vocab 已有）；
- `first_gold_read`：第几个 tool_call 首次 read 到 gold 文件（离线 jq 算，不进 vocab）——"绕路程度"的直接量。

**行为指标（引导是否起效/过头）**
- `mcp_calls`：`tool_start` 事件 name 含 `__` 中缀的次数（D1 后 MCP 工具名可辨识；内置工具名均不含 `__`）——**唯一新增的 run.sh vocab 项**；
- `grep_calls`（vocab 已有）。

**成本口径（缓存账）**
- 臂间 `cache_hit_tokens` 对比 + A3 首 turn prompt_tokens 增量 = MCPSection + 工具 schema 的 prefix 持续成本；收益 = 后续轮次缩减。净账 = 每 run 总 token（prompt+completion）。

### 5.3 eval 落地

- 新 case 组 `eval/cases/mcp-semble-routing/`，三个 prompt：
  1. **NL 定位**（正例）："找出权限拒绝如何变成工具结果而非致命错误，给出 file:line"——期望 A3 走 `semble__search`；
  2. **符号定位**（**防过度引导的反例**）："找到 `ModeLabel` 的函数定义"——期望 A3 仍走 grep，`max_mcp_calls: 0`；
  3. **混合**："LSP references 在什么条件下降级 grep"——观察自然分布。
- `runner.sh` 三段执行（A1/A2/A3 各跑当前 case），`expect.json` 分臂绑界（如 case 1：`min_mcp_calls` 仅对 A3 生效——实现为 runner 输出带臂标的行，或三臂各一个 case 目录，D4 卡内定，倾向后者：简单）。
- **样本量**：每臂每 case ≥5 run（eval README 纪律：单跑是信号不是证明）。
- Semble 索引预热：case 的 `setup.sh` 先跑一次 semble 索引（首次查询触发索引会污染 A3 的时延/轮数——这不是引导效应）。

### 5.4 判定规则（Phase 1 验收 & Phase 3 触发）

**Phase 1（引导）验收 = 全部成立**：
1. A3 vs A2：case 1 的 `mcp_calls` 从 ≈0 → 每 run ≥1（引导让模型找得到工具）；
2. A3 vs A1：case 1 的 `completion_tokens` 降 ≥30%（保守阈值——Semble 宣称的 98% 是对"读全文件"口径，seek 基线已是 grep+offset 精打细算的工作流，可比空间小得多）；
3. A3 case 2 `mcp_calls` 仍 = 0（引导没有把符号查询也推向语义搜索）；
4. A3 `cache_hit_tokens` 相对 A1 无异常塌方（MCPSection 没破坏缓存纪律）。

**Phase 3（grep 结果内 hint）触发 = 引导被证伪**：A3 中 case 1 仍有 >30% 的 run `mcp_calls = 0` **且** 这些 run 的 `gold_hit` 低于 A1。两个条件都要——只前者（模型不用）可能是工具确实不好用，只后者（成功率低）可能是任务本身难。此时才开 hint 卡；否则 Phase 3 永远不做（数据说了算）。

### 5.5 日常统计（可选，Phase 3 的长期数据源）

`internal/mcpstats`：MCP 工具调用 JSONL（skillstats 六字段同款：ts/name/server/session_id/project_id/model）。写侧挂 Bridge.Execute，读侧暂不做（有 eval + 短期判定已够，避免无消费者的写路径）。开卡与否由 D5 决策时定。

## 六、卡片拆分（每张 ≤2h，依赖有序）

| 卡 | 标题 | 范围（改动点） | 验收 | 依赖 |
|----|------|---------------|------|------|
| D1 | MCP 工具默认 server 前缀 + 注册顺序确定化 | `bridge.go`（前缀策略）、`LoadServers`（字典序遍历）、`mcpconfig`（`prefix:false` 逃生口）；改 `bridge_test.go` | 同配置两次启动注册名单逐字节一致（测试锁）；`semble__search` 形名；无 MCP 配置时行为零变化 | — |
| D2 | sysprompt MCPSection 通道 | `sysprompt.go`（Header 字段 + Compose 追加）、`main.go` 接线（LoadServers 后组装注入）、`sysprompt_test.go`（新增"有 MCP"golden + 现有 golden 不动） | 空 MCPSection 输出与现状逐字节同（原 golden 原样绿）；子代理 prompt 含同段（`ComposeSubagent` 测试） | D1（名单形态先定） |
| D3 | hint 字段 + 文案组装 + semble 内置文案 | `mcpconfig`（`hint` 字段）、`mcptool.PromptSection()`（分组+排序+优先级）、known-server 表（semble 条目）、`mcp_prompt:false` 开关 | 文案字节确定性测试（同输入同输出）；三层文案优先级各有用例；§3.2 示例文案为 golden | D2 |
| D4 | eval 三臂对照 + mcp_calls metric | `eval/run.sh` vocab 加 `mcp_calls`、新 case 组（3 case × runner/setup）、三臂基线采集（每臂×case ≥5 run）+ 对照摘要文档 | 三臂对照表入库 `eval/results/`；§5.4 四项判定数据齐 | D2（A3 需要 MCPSection）；文案终版（D3）后重跑 A3 |
| D5 | Phase 3 决策卡（读数，非实现） | 按 §5.4 规则判 Phase 3 go/no-go，决策记录回填本 PRD §四 | 本文件 §四 Phase 3 状态从"数据触发"变为"go（开 hint 卡）/ no-go（附数据）" | D4 |

顺序即依赖：D1 → D2 → D3 → D4 → D5。D1/D2 可同日连做；D4 的 A1/A2 臂可在 D3 前先采（不依赖文案）。

## 七、Semble 内部实现要点（供参考）

分析 Semble 源码后，其核心设计可供 seek 未来参考：

### 7.1 Tree-sitter AST 分块

```
source → tree-sitter parse → _merge_node_inner() 递归遍历 AST
  → 按函数/类边界分组 → _merge_adjacent_chunks() 合并小 chunk 到 ~1500 chars
  → 回退策略：不支持的语言用 chunk_lines() 行级分块
```

**对 seek 的启示**：如果要在 Go 中做代码分块，可以用 `github.com/smacker/go-tree-sitter`。

### 7.2 Hybrid 检索算法

```
查询 → 同时跑 BM25 + Dense Embedding (model2vec)
  → 分别转 RRF (Reciprocal Rank Fusion) 分
  → alpha * semantic + (1-alpha) * bm25 融合
  → boost: 符号定义 ✕3、文件名匹配、多 chunk 文件
  → penalise: 测试文件、生成文件降权
```

**对 seek 的启示**：BM25 部分可以纯 Go 实现（`github.com/geange/go-bm25`）作为 `grep` 的增强替代，不需要 embedding 也能获取得分 0.673 NDCG@10（BM25 自身），比 ripgrep 的 0.126 高很多。

### 7.3 Query 类型检测

```python
_SYMBOL_QUERY_RE = re.compile(
    r"^(?:"
    r"[A-Za-z_][A-Za-z0-9_]*(?:(?:::|\\|->|\.)[A-Za-z_][A-Za-z0-9_]*)+"
    r"|_[A-Za-z0-9_]*"
    r"|[A-Za-z][A-Za-z0-9]*[A-Z_][A-Za-z0-9_]*"
    r"|[A-Z][A-Za-z0-9]*"
    r")$"
)
```

符号查询 → `alpha=0.3`（更偏 BM25 精确匹配）
NL 查询 → `alpha=0.5`（均衡语义 + 精确）

**对 seek 的启示**：grep 工具可以检测查询类型，对符号查询走字面量匹配，对 NL 查询提示使用 MCP 搜索。（深度集成版此启示已被 §4.3 的 grep-hint 后手吸收——检测落在 grep 工具自身，不落在调度层。）

**MCP 工具面（2026-11-16 核实官方仓库）**：`search(repo, query, content: code|docs|config|all)` 与 `find_related(file, line)`；无独立 index 工具，索引按需触发并缓存、本地路径文件变更自动重建索引。

## 八、不做的（明确排除）

1. **不把 Semble 的 Python 代码迁入 seek** —— Model2Vec / vicinity / tree-sitter 的 Python 依赖链不符合 seek 的单二进制哲学
2. **不使用 Semble 作为库嵌入** —— 即使有 Go embedding 模型，16M 静态模型的效果上限有限，性价比不如 MCP 桥接
3. **不实现 MCP server** —— seek 只做客户端，不做为工具被调用的 server 端
4. **不在 `pkg/agent` 调度层做工具路由拦截**（原方案 B 的落点，§4.4）—— 侵入核心循环、模型不可见被改写的路由
5. **不做 `plan_and_edit` 式搜索→编辑复合工具**（原方案 C，§2.2）—— 编排归模型，封装换不来灵活性
6. **known-server 文案表不做自动分类/联网补全** —— seek 对不认识的 server 只陈列不解读（§3.2 降级文案），编用途比没用途更糟

## 九、关联文档

- `docs/book/chapter-12.md` —— MCP client 实现设计文档（已交付）
- `pkg/mcp/` —— MCP 协议客户端源码
- `internal/tools/mcptool/` —— MCP 工具桥接源码
- `internal/sysprompt/sysprompt.go` —— system prompt 组装（D2 改动点）
- `eval/README.md` —— 行为评估口径与 tool-description tuning 先例（D4 依据）
- [MinishLab/semble](https://github.com/MinishLab/semble) —— 语义代码搜索项目
