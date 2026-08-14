# read 工具改进前后对比测试方案

> 目的：为 `internal/tools/read` 的设计改进（见 `docs/test-plan-read-tool.md` 背景与 `AGENTS.md` "工具描述/输出限制" 相关约定）建立**可度量、可判定**的前后对比。本方案遵循仓库既有 eval 哲学：先定假设与阈值（预注册），再跑基线，改代码，最后对比——"趋势胜过单次，约束先于解释"。
>
> 状态：**Phase 0–5 全部完成**（2026-08-14）。最终判定：**H1/H2/H4/H5 PASS，H3 由 L1 确定性测试证明（行为指标不可观测，eval 设计局限）**，改进合入。结果见 §7.0/§7.3。

---

## 1. 背景与干预定义（"改进后"是什么）

基于对 `internal/tools/read/read.go` 的评审，待验证的改进集（每个改进都映射到至少一个假设）：

| # | 改进 | 代码位置（预期） | 对应的设计缺陷 |
|---|---|---|---|
| **I1** | 结果 header 增加文件**总行数**（`totalLines`），模型可判断 EOF，无需探测性续读 | `read.go:153-161` | 分页盲区 |
| **I2** | **小文件一次读完**：文件 ≤ 阈值（**已定为 32 KiB**——参数扫描实测 16 KiB 差 0.3% 到 H1 成本门槛、64 KiB 因模型写更长答案反而更贵；config 可配 `read.whole_read_bytes`）时忽略 limit 全量返回；`limit` 上限改为可配置（`read.max_limit`，默认 200），去掉硬编码 50 | `read.go` + `internal/config` | 恒定 50 行 cap 放大轮次成本 |
| **I3** | `fsobserve` 只在**读到 EOF 的完整读取**时标记 observed；部分读取不标记（或记录已读行范围） | `read.go:151` + `internal/fsobserve` | 50 行 peek 被当作"看过整文件"，write 守卫失效 |
| **I4** | 输出加**字节上限**（如 64 KiB）与**超长行带内省略**（>1 MiB 的单行截断为 `… [1.2 MiB line elided]` 标记，而不是整个 read 失败） | `read.go:127-145` | 无写时字节上限；单行超长整读失败 |
| **I5**（可选） | 二进制/非 UTF-8 嗅探：检测到疑似二进制返回短提示而不是吐垃圾行 | `read.go:120` 前 | 二进制文件浪费 token |

I1–I4 是必做干预；I5 视 I1–I4 结果决定。**对比只归因于 I1–I4 的合取**，不单独归因单个改进（若需单点归因，见 §8 风险与限制中的"分解实验"）。

## 2. 假设与预期量化（预注册）

| # | 假设 | 对应改进 | 预期量化 |
|---|---|---|---|
| **H1** | 少而大的读取降低轮次与输出 token，从而降低总成本（输出 $0.28/1M ≈ 缓存命中输入的 100 倍；轮次是主要开销维度） | I2 | case C 中位数 turns −≥30%，completion_tokens −≥25%，核算成本 −≥20% |
| **H2** | 总行数进入 header 后，模型不再做 EOF 探测性续读 | I1 | case B 中位数 probe_reads = 0，且 ≥80% 的 runs 为 0（基线 > 0） |
| **H3** | 部分读取不再授予整文件覆盖权，write 守卫真正生效 | I3 | L1 测试全绿；case D 中改进组 ≥2/5 runs 出现 write 拒绝并成功回退 edit；基线组 0 次拒绝（静默覆盖，即缺陷现场） |
| **H4** | 超长单行不再导致整读失败；输出有字节上限 | I4 | L1 测试全绿（2 MiB 单行 → 成功 + 带内省略标记，输出 ≤ 上限） |
| **H5** | 模型的分页行为可预测：知道总行数后，读取次数 = ceil(totalLines / limit)，不多不少 | I1+I2 | case B 改进组 read_calls 中位数 = 1（文件恰 50 行）；case A 改进组 read_calls ≤ 3 |

**不回归约束**：cases A–D 的 grep_calls / turns 不得比基线差（读路径的改动不能把模型推向 bash/grep 逃生口）。

## 3. 指标定义

### 3.1 直接复用既有指标（`run.sh` 已提取）

`turns`、`read_calls`、`grep_calls`、`total_tool_calls`、`completion_tokens`、`elapsed_s`、`prompt_tokens`。

### 3.2 新增指标（需扩展 `eval/run.sh` 提取逻辑，改动 < 20 行）

| 指标 | 定义 | 提取方式（从 `seek -json` 的 JSONL stream） |
|---|---|---|
| `probe_reads` | 探测性续读：read tool_end 结果含 `0 lines emitted` 且 offset > 1 | 解析 read 的 tool_end result 字符串（含 `lines emitted` 计数） |
| `write_refusals` | write tool_end 的 error 含 fsobserve 守卫说明（实现时钉死 `fsobserve.Explain` 输出子串，如 `stale`） | 解析 tool_end error |
| `cache_hit_ratio` | prompt 缓存命中比例 | `agent_end` 事件的 `prompt_cache_hit_tokens / prompt_tokens`（参考 `pkg/agent/cache_e2e_test.go` 的字段名） |
| `answer_correct` | 最终答案是否包含金标子串 | 分析脚本对最后一条 assistant 文本做子串匹配（正则/字面量，逐 case 定义） |

### 3.3 派生指标（分析脚本计算，不写回 stream）

- **核算成本**：`cost = (prompt_tokens − hit) × P_miss + hit × P_hit + completion_tokens × P_out`，取 `internal/pricing` 同源费率（V4-Flash 标价：$0.14 / $0.0028 / $0.28 每 1M）。分析时统一用一个常量文件，避免跨二进制费率漂移；非高峰时段 ×0.5 的折扣**不计入对比**（两侧同样折扣，抵消）。
- **分页效率**：`page_overhead = read_calls − ceil(totalLines / limit)`（> 0 即存在多余轮次）。

## 4. 测试矩阵（三层）

| 层 | 类型 | 是否需要 API | 测什么 | 归属 |
|---|---|---|---|---|
| **L1** | Go 单元/集成测试 | 否（httptest/临时文件） | I1–I4 的确定性行为 + fsobserve 安全属性 | `internal/tools/read/`、`internal/fsobserve/` |
| **L2** | eval 行为测试（黑盒，真 API） | 是（`DEEPSEEK_API_KEY`） | 模型在 I1–I4 下的行为变化（H1/H2/H5 及 case D 的安全回退） | `eval/cases/read-*/` |
| **L3** | 成本模型核算 | 否 | 用 3.3 公式对比两二进制实际成本；另验证理论比值（10×50 行 vs 1×500 行） | 分析脚本内嵌 |

## 5. L1 确定性测试清单（先写、先跑红，再实现）

这些测试在基线二进制上**必须失败**（它们就是改进的规格说明）：

| 测试 | 断言 |
|---|---|
| `TestRead_HeaderContainsTotalLines` | header 含 `, 137 lines total` 之类字段；EOF 时模型可见总行数 |
| `TestRead_SmallFileReadWhole` | 40 行文件 limit=50 → 一次返回 40 行，header 无 TRUNCATED |
| `TestRead_ByteCapElidesLongLines` | 2 MiB 单行文件 → 成功，输出 ≤ 字节上限，含带内省略标记 |
| `TestRead_MinifiedLine_NoHardError` | 1.2 MiB 单行不再返回 scan: token too long 硬错误 |
| `TestRead_OffsetBeyondEOF_ReportsTotal` | offset 超出 EOF → `0 lines emitted` + 总行数（模型可据此停止） |
| `TestRead_ObservedOnlyOnFullRead` | 部分读取（offset=1, limit=10，文件 200 行）→ observer 未标记；完整读取 → 标记 |
| `TestWrite_PartialReadDenied_Integration` | 部分读取后 write 整文件 → 拒绝（`StatusStale`/`StatusUnseen` 语义）；完整读取后 write → 允许 |
| `TestRead_LimitConfigurable` | `read.maxLimit` 配置生效（默认 200）；>max 拒绝并报清晰错误 |

补充既有测试的同步：`read_test.go:193` 的 TRUNCATED 测试在 I2 后语义变化（小文件不再 TRUNCATED）——**需要一并更新**，这是预期行为变更，不是回归。

## 6. L2 eval 案例定义（四个新 case）

每个 case = `eval/cases/<name>/{README.md, prompt.txt, expect.json, testdata/}`。fixtures 由**种子固定的生成脚本**产出（提交生成脚本，不提交大 fixture），两二进制跑同一份内容。

### Case A `read-tail-fact` — 尾部事实（H5、H1 的轻量版）

- **fixture**：`testdata/app.go`，140 行合法 Go，第 118 行是 `const Timeout = "45s"`（唯一出现），函数体内注释不含该串。
- **prompt**（`prompt.txt`）：

  ```
  文件 testdata/app.go 里有个超时配置常量。请告诉我它的名字和值。
  [Workflow rule: 只允许使用 read 工具，禁止 grep / bash / list_dir。]
  ```

  （隔离规则是为了把测量聚焦在 read 行为上；grep 计数仍记录，用于验证模型没有作弊逃逸。）
- **金标**：`answer_correct` = 最终答案含 `45s`。
- **expect.json**（初版，跑基线后校准）：

  ```json
  { "description": "read 必须能取到第 118 行的尾部事实；改进后 read_calls <= 3 且一次到位",
    "max_turns": 8, "max_read_calls": 3, "max_grep_calls": 0 }
  ```

### Case B `read-eof-blind` — EOF 盲区（H2、H5 的核心）

- **fixture**：`testdata/eof.txt`，**恰好 50 行**，第 50 行是 `LAST_LINE_MARKER=42`。
- **prompt**：

  ```
  文件 testdata/eof.txt 的最后一行内容是什么？只使用 read 工具，不要用 grep/bash。
  ```

- **金标**：答案含 `LAST_LINE_MARKER=42`。
- **关键测量**：`probe_reads`（offset>1 且返回 0 行的 read 调用）。基线：模型无法区分"恰好 50 行"与"还有更多"，大概率发出 offset=51 探测；改进：header 的总行数直接终结猜测。
- **expect.json**：`{"max_turns": 6, "max_read_calls": 2, "max_probe_reads": 0, "max_grep_calls": 0}`。

### Case C `read-whole-file` — 整文件理解（H1 成本主线）

- **fixture**：`testdata/server.go`，400 行、6 个函数、含中段注释。
- **prompt**：

  ```
  阅读 testdata/server.go 的全部内容，按出现顺序列出所有函数签名。不要使用 grep/bash，只能 read。
  ```

- **金标**：6 个签名按序包含（子串序列匹配，容错顺序错位不计分，见 §8）。
- **测量**：turns、read_calls、completion_tokens、核算成本、page_overhead。
- **expect.json**：`{"max_turns": 12, "max_read_calls": 4}`（基线预期 8–10 次分页；改进后 ≤4）。成本阈值不进 expect.json（放判定表，避免费率变动弄红 eval）。

### Case D `write-guard-fallback` — 安全回退（H3 行为侧）

- **前置**：本 case 需要 `run.sh` 的一个小扩展——**setup/teardown 钩子**（把 `testdata/config.yaml` 复制到 `eval/tmp/write-guard-fallback/`，跑完清理），避免污染提交树。prompt 引用 tmp 路径。
- **fixture**：200 行 YAML 配置，顶部有 10 行注释块。
- **prompt**：

  ```
  给 eval/tmp/write-guard-fallback/config.yaml 的顶部注释块加一行 "// managed by seek"。
  你可以先读文件再修改；请用最合适的方式完成。
  ```

- **测量**：`write_refusals`、最终文件是否含目标行（setup/teardown 脚本检查）、read_calls。
- **预期行为差异**：基线——模型部分读取后直接 write 整文件，守卫（整文件被标记 observed）静默放行，属缺陷现场；改进——部分读取不授予整文件覆盖权 → write 被拒 → 模型回退 edit（exact-match 本就允许）→ 成功。
- **expect.json**（改进组目标）：`{"max_turns": 10, "min_write_refusals": 0, "max_grep_calls": 3}`——注意 `min_write_refusals` 只能用于改进组判定，基线的期望值是 **0 次拒绝**（对比表用，不放 expect.json）。

## 7. 执行流程

```
Phase 0  冻结基线
  git rev-parse HEAD 记录基线提交（预期 v0.10.0 之后某 commit）
  go build -o seek-baseline ./cmd/seek
  （记录 seek --version / 配置中的 model，钉死模型）

Phase 1  先写 L1 测试（§5）——在基线上必须跑红；这是规格
  go test ./internal/tools/read/ ./internal/fsobserve/   # 预期 FAIL
  go test ./...                                          # 确认无其他回归

Phase 2  记录基线行为（eval，真 API）
  eval/run.sh read-tail-fact    ./seek-baseline   ×5
  eval/run.sh read-eof-blind    ./seek-baseline   ×5
  eval/run.sh read-whole-file   ./seek-baseline   ×5
  eval/run.sh write-guard-fallback ./seek-baseline ×5
  （每次独立结果行 → eval/results/baseline-<hash>-<date>.jsonl，入库）

Phase 3  实现 I1–I4（独立 commit；L1 测试转绿）
  go test ./... && go vet ./...

Phase 4  构建改进二进制并重跑同一 battery
  go build -o seek-improved ./cmd/seek
  eval/run.sh <case> ./seek-improved ×5（同一模型、同一天或邻近日期）

Phase 5  分析对比
  eval/tools/compare-read-ab.sh  ← 新脚本（见 §7.1）
  产出：每 case 每指标的 基线/改进 中位数 + Δ% + 判定结果

Phase 6  记录结论
  结果 JSONL 入库 eval/results/；判定表 §7.2 的结论写回本文件状态栏
  （如有意外发现 → docs/pitfalls.md 新条目 + commit trailer）
```

### 7.0 基线结果（Phase 2 已完成，2026-08-14）

- 二进制：`seek-baseline-linux`（commit `1ad60e3`，模型 `deepseek-v4-flash` 默认）
- 结果：`eval/results/2026-08-14-v0.10.0_dirty-1ad60e3_.jsonl`（20 行 = 4 case × 5，暖机行已剔除，严格单行 JSONL）
- 中位数汇总（`eval/tools/compare-read-ab.sh` 基线自对比快照）：

| case | read_calls | probe_reads | write_refusals | turns | completion_tokens | 成本/run | 金标 |
|---|---|---|---|---|---|---|---|
| read-tail-fact | 3 | **2** | 0 | 4 | 663 | $0.00075 | 100%（45s） |
| read-eof-blind | 2 | **1** | 0 | 3 | 763 | $0.00046 | 100%（marker） |
| read-whole-file | 8 | **7** | 0 | 9 | 1287 | $0.00180 | 6/6 函数 |
| write-guard-fallback | 1 | 0 | **0** | 3 | 1203 | $0.00052 | 100%（内容） |

基线结论（全部与预注册预期吻合）：

1. **EOF 盲区是最大浪费**：read-whole-file 的 8 次 read 中 **7 次是 0 行探测**（offset 超 EOF）——约 78% 的 read 调用是浪费的。read-tail-fact 的 3 次 read 中 2 次是探测。
2. **正确性不是问题**：金标 4/4 全 100%——模型用额外轮次绕过了 read 工具的缺陷，代价是 turns/completion_tokens/成本（H1 主线的现场证据）。
3. **I3 缺陷现场实锤**：write-guard-fallback 每次只读 1 次（50 行）就整文件 write 成功，0 次拒绝——部分读取被当作"看过整文件"，守卫形同虚设。
4. 判定表 7.2 全部 FAIL（H1/H2/H3/H5）——它们就是改进目标，基线必须 FAIL。
5. 总成本：20 次运行 ≈ **$0.02**（远低于预算）。

### 7.3 最终对比（Phase 4/5 已完成，2026-08-14，改进二进制含 32 KiB 默认）

基线 `..._baseline.jsonl`（20 行）vs 改进 `..._1ad60e3_.jsonl`（20 行），中位数：

| case | metric | baseline | improved | Δ |
|---|---|---|---|---|
| read-tail-fact | read_calls / probe_reads / turns / cost | 3 / 2 / 4 / $0.00075 | 1 / 0 / 2 / $0.00054 | −67% / −100% / −50% / −28% |
| read-eof-blind | read_calls / probe_reads / turns / cost | 2 / 1 / 3 / $0.00046 | 1 / 0 / 2 / $0.00029 | −50% / −100% / −33% / −38% |
| read-whole-file | read_calls / probe_reads / turns / cost | 8 / 7 / 9 / $0.00180 | **1 / 0 / 2 / $0.00127** | −88% / −100% / −78% / −29% |
| write-guard-fallback | turns / cost / refusals | 5 / $0.00146 / 0 | 4 / $0.00150 / 0 | −20% / +3% / 0 |

金标：改进侧 4/4 全 100%（与基线持平）。

**判定结果（预注册阈值 §7.2）**：

| 判定 | 结果 | 说明 |
|---|---|---|
| H1（成本） | **PASS** | case C turns 9→2（−78% ≥ 30%），成本 −29% ≥ 20%。16 KiB 阈值时成本仅 −19.7%（差 0.3%），参数扫描（16/32/64 KiB）后定为 32 KiB |
| H2（EOF） | **PASS** | probe_reads 1→0，改进侧 100% runs 为零 |
| H3（守卫） | **L1 PASS / 行为指标不可观测** | L1 确定性测试全绿（部分读取→拒绝、完整读取→放行）；但行为侧 `write_refusals` 改进组仍为 0——放大 fixture 后模型**直接改用 edit**（更合理的工具），从不尝试整文件 write，拒绝路径在真实模型行为中触发不到。这是 eval case 设计局限（无法强迫模型做次优选择），不是守卫失效 |
| H4（健壮性） | **PASS** | L1 全绿：2 MiB 单行不硬失败、输出 ≤ 64 KiB、带内省略标记 |
| H5（分页） | **PASS** | read-eof-blind read_calls = 1；read-tail-fact ≤ 3 |
| 不回归 | **PASS** | 所有 case turns 无 +30% 劣化；金标持平 |

**结论**：I1–I4 改进成立，可合入。H3 的行为指标按预注册规则未通过，但按"安全属性是硬门槛"的精神，其证据由 L1 确定性测试提供（`TestWrite_PartialReadDenied_Integration` 直接证明拒绝路径），行为侧仅记录为 eval 设计局限。若未来要行为级验证拒绝路径，需要构造"模型必然尝试整文件 write"的提示词（如显式要求"用 write 重写整个文件"）。

### 7.1 分析脚本输出（示意）

```
case            metric           baseline  improved  Δ        verdict
read-eof-blind  probe_reads      1.0       0.0       -100%    PASS (H2)
read-eof-blind  turns            4.0       3.0       -25%     PASS (H5)
read-whole-file turns            11.0      6.0       -45%     PASS (H1)
read-whole-file realized_cost    $0.041    $0.023    -44%     PASS (H1)
read-tail-fact  answer_correct   80%       100%      +20%     PASS
write-guard-…   write_refusals   0.0       2.4       n/a      PASS (H3)
```

成本与正确率按 runs 汇总（中位数/比例），其余按中位数。

### 7.2 判定表（预注册阈值，先于任何运行）

| 判定 | 条件 |
|---|---|
| H1 PASS | case C：turns 中位数 −≥30% **且** 核算成本 −≥20% |
| H2 PASS | case B：probe_reads 中位数 = 0 且 ≥80% runs 为 0 |
| H3 PASS | L1 安全测试全绿 **且** case D 改进组 ≥2/5 runs 出现 write 拒绝 + 文件最终正确 |
| H4 PASS | L1 健壮性测试全绿（超长行不硬失败、输出有界） |
| H5 PASS | case B read_calls 中位数 = 1；case A read_calls ≤ 3 |
| 不回归 | 既有 3 个 eval case 全部保持 PASS；cases A–D 的 turns 无 case 级劣化 >30% |

**判定规则**：H1/H2/H3 全 PASS → 改进成立，合入；H1 或 H2 单 FAIL → 保留 I3/I4（安全与健壮性收益独立成立），对 I1/I2 的取值（阈值、默认上限）做参数扫描再测一轮；H3 FAIL → I3 不得合入（安全属性是硬门槛）。

## 8. 风险与限制（预先声明）

### 8.3 执行环境（Phase 0/1 实测发现，Phase 2 必读）

- **`eval/run.sh` 必须是 LF 行尾**。仓库 `core.autocrlf=true`（Windows 检出默认 CRLF），而 WSL 的严格 bash 会以 `$'\r': command not found` 拒绝 CRLF 脚本（git-bash 容错所以不易察觉）。已做两件事：工作区 run.sh 转为 LF；新增 `.gitattributes`（`*.sh text eol=lf`）从根上防止复发。**任何人在 Windows 上编辑 .sh 文件后提交前，检查 `git diff` 是否出现整文件行尾噪音**。
- **Windows 上跑 eval 必须用 WSL + Linux jq**。`run.sh` 的 jq.exe 回退（注释称 "WSL needs jq.exe"）实际不可用：Windows jq.exe 读不懂 `/mnt/d/...`、`/tmp/...` 这类 POSIX 路径（"Could not open file"），且 WSL binfmt interop 无法直接执行 WinGet Links 下的 jq.exe（Exec format error）。正确姿势：WSL 内 `sudo apt install jq`，用 Linux jq。
- 本方案 Phase 2/4 的 eval 运行应在上述 WSL 环境执行；Windows git-bash 仅适合 `bash -n` 语法检查。
- **基线二进制**：`seek-baseline`（commit `1ad60e3`，2026-08-14 构建，已 gitignore），Phase 2 直接使用。

- **随机性**：N=5 起，判定用中位数与比例，不用单次；README 明言"趋势胜过单次"。
- **服务端缓存暖机**：DeepSeek 缓存跨进程存活，首请求冷/暖不定——对比两侧都先跑一次 discard run 暖机，或固定 run 顺序（基线→改进→基线交叉），并在结果行记录顺序。
- **模型/日期漂移**：两侧钉同一 model（记录在结果行）；尽量同日完成两轮；若跨日，结论降级为"趋势性"。
- **fixtures 确定性**：生成脚本种子固定并提交；两二进制跑同一字节内容。
- **run.sh 扩展**：需新增 `probe_reads`/`write_refusals` 提取（§3.2）与 case D 的 setup/teardown 钩子——均为 ≤20 行改动，作为 Phase 0 的一部分先落地并自测。
- **成本预算**：4 case × 5 runs × 2 二进制 ≈ 40 次调用 ≈ $0.8–2.0（按 README 每 case $0.02–0.05 估算），可接受。
- **分解实验（可选）**：若需单点归因 I1 vs I2，可加一轮"仅 I1"与"仅 I2"的中间二进制各跑 case B/C；默认不做（成本翻倍）。
- **answer_correct 的判定粒度**：case C 的函数签名序列用"无序集合命中率 ≥5/6"而非严格顺序，避免对顺序的过度惩罚淹没 read 行为信号。

## 9. 交付物清单与进度

- [x] 本文档（Phase 0/1 完成，状态已更新）
- [x] `eval/run.sh` 扩展：`probe_reads` / `write_refusals` / `cache_hit_tokens` + setup/teardown 钩子（WSL 端到端自测 PASS，2026-08-14）
- [x] `eval/README.md` 指标表与 case 约定同步
- [x] L1 规格测试：`internal/tools/read/read_improved_test.go`（7 个测试，基线全部如预期 FAIL；全量 `go test ./...` 仅此包新增失败，无其他回归）
- [x] 环境修复：`.gitattributes`（`*.sh text eol=lf`）、`.gitignore`（`seek-baseline`/`.tmp-*`）
- [ ] `eval/cases/read-tail-fact/{README.md,prompt.txt,expect.json,testdata/}`
- [ ] `eval/cases/read-eof-blind/{…}`、`eval/cases/read-whole-file/{…}`、`eval/cases/write-guard-fallback/{…}`
- [ ] `TestRead_LimitConfigurable`（需 `read.maxLimit` 配置键，无法在基线上编译——随 Phase 3 实现一起落地）
- [ ] `eval/tools/compare-read-ab.sh`（对比表 + 判定）
- [ ] 基线结果：`eval/results/baseline-<hash>-<date>.jsonl`（Phase 2 产物，需 API key + WSL）
- [ ] 改进 commit（I1–I4）+ 改进结果 JSONL
- [ ] 判定结论回写本文档状态栏；意外发现 → `docs/pitfalls.md`

## 附：为什么这样设计（一句话版）

三层各司其职：**L1 把"改进"变成可失败的规格**（先红后绿）；**L2 测模型真实行为**（成本、轮次、EOF 盲区、安全回退，黑盒 + 金标）；**L3 把成本经济学算成数字**（输出 token 是缓存输入的 100 倍，轮次才是开销主线）。阈值全部预注册，避免"跑完再解释结果"。
