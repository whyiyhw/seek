# git 工具调用构造错误：description/prompt 调优前后对比测试方案

> 目的：验证对 `internal/tools/git` / `internal/tools/bash` 的 `const description`（以及随 `Description()` 自动注入 sysprompt 工具列表的文案）与 `AGENTS.md`/`CLAUDE.md` 的调优，能否降低三类 git 调用构造错误。遵循仓库既有 eval 哲学：先定假设与阈值（预注册），再跑基线，改代码，最后对比——"趋势胜过单次，约束先于解释"。
>
> 状态：**Phase 0 预注册完成；Phase 1 基线执行中**（2026-08-18）。判定见 §7。

---

## 1. 背景与干预定义（"改进后"是什么）

### 1.1 被测失败模式（2026-08-18 真实会话，Windows cmd.exe）

| # | 失败模式 | 现场表现 | 既有覆盖 |
|---|---|---|---|
| **FM-A** | git 工具调用把 subcommand 重复进 `args[0]` | `{"subcommand":"show","args":["show","-s",...]}` → `fatal: ambiguous argument 'show'` | 已有：`git-subcommand-shape`/`git-subcommand-midtask` case + 工具自动修复 + `git_subcommand_dupes` 指标 |
| **FM-B** | bash 里写 POSIX 命令链 | `git log ...; echo ---; git log ...` —— cmd.exe 不认 `;`，整串喂给 git → `fatal: ambiguous argument 'echo'` | **无**（本次新增 `bash_chains` 指标） |
| **FM-C** | 只读 git 查询走 bash 而非 git 工具 | 同一会话用 bash 跑 git log/status | **无**（本次新增 `bash_git_calls` 指标） |

### 1.2 干预（每个改进映射到至少一个假设）

| # | 干预 | 代码位置 | 对应缺陷 |
|---|---|---|---|
| **I1** | git 工具 `description` 追加：one query per call；多个独立查询 = 多个并行调用；args 数组一个元素一个 flag/path，**无 shell 语法**（禁 `;`/`&&`/`|` 链式）；可能被当成 revision 的 pathspec 前加 `--` | `internal/tools/git/git.go` | FM-B/FM-C 的"一条命令查多个东西"心智 |
| **I2** | git 工具 schema `args` 的 description 同步：`--` 元素分隔 revision 与 pathspec；元素内禁止 shell 分隔符 | 同上 schema | FM-A 的形变、FM-B |
| **I3** | bash 工具 `description` 追加：shell 是 cmd.exe（Windows）/`/bin/sh`（WSL）；`;` 不是 cmd 分隔符——**禁止链命令**，一次一个命令，多件事用并行专用工具；只读 git（log/diff/status/show）归 git 工具，bash 只留变更型 git 与非 git 工作 | `internal/tools/bash/bash.go` | FM-B、FM-C |
| **I4** | `AGENTS.md` "Tool usage workflow" 追加一条 git 调用形状规则；`CLAUDE.md` 镜像 | 仓库根 | FM-A/B/C 的长效文档约束 |

工具描述经 `internal/sysprompt` 从各工具 `Description()` 动态组装进系统提示的工具列表——I1–I3 即"改 prompt"，且是 AGENTS.md 认定的最高杠杆点（描述随每次调用的 schema 注入，贴近生成步骤）。

**明确不做**：不改 `prompt.txt` 加"不要用 bash"禁令（那测的是指令服从，不是描述有效性）；不动已有两个 shape case 的 expect.json（回归守卫保持原样）。

## 2. 假设与预期量化（预注册）

| # | 假设 | 对应改进 | 预期量化 |
|---|---|---|---|
| **H1** | FM-A 保持 0（回归） | I2 | 两阶段全部 runs `git_subcommand_dupes` = 0（基线已由自动修复兜底，期望维持） |
| **H2** | FM-C 可复现且干预后下降 | I3、I4 | 基线 ≥3/10 runs 出现 `bash_git_calls` > 0；干预后 ≤ 基线，目标 0/10 |
| **H3** | FM-B 可复现且干预后下降 | I1、I3 | 基线 ≥3/10 runs 出现 `bash_chains` > 0；干预后 ≤ 基线，目标 0/10 |
| **H4** | 描述调优后整体达标 | I1–I3 | 干预后 ≥8/10 runs 全 PASS（`git_calls ≥ 4`、`bash_calls = 0`、`bash_chains = 0`、`bash_git_calls = 0`、`dupes = 0`） |
| **H5** | 无成本劣化 | I1–I3 | 干预后 `completion_tokens` 中位数 ≤ 基线 × 1.25（描述变长不得诱发更啰嗦的作答） |

判定规则：H2/H3 看率差（基线 vs 干预后，`bash_*` > 0 的 run 占比）；H4 看 PASS 率；H1/H5 是护栏，破了即判干预有副作用。

## 3. 指标定义

### 3.1 新增指标（`eval/run.sh` + `eval/README.md` 已同步）

- **`bash_chains`**：`tool_start` 事件中 `name=bash` 且原始 args 含 `;` 或 `&&`。`;` 在 cmd.exe 根本不是分隔符（整串报错）；`&&` 在 cmd 合法但仍是链式反模式。`|` 刻意不算——管道与 `--grep="a|b"` 交替合法。匹配的是**调用前**的 args JSON，失败的链也计数。
- **`bash_git_calls`**：`tool_start` 事件中 `name=bash` 且 args 含 `\bgit\b` 调用（词边界排除 `github.com` 之类；`cd x && git log` 链也能抓到）。变更型 git（commit/push）合法属于 bash，但本 eval 的 prompt 只读，0 是正确目标。

### 3.2 复用指标

`git_calls`、`git_subcommand_dupes`、`bash_calls`、`turns`、`completion_tokens`、`total_tool_calls`（定义见 `eval/README.md` 词汇表）。

## 4. 用例（`eval/cases/git-multi-query-no-bash/`）

prompt（中文，刻意**不禁止 bash**）：查 scroll / drift / inline 三个关键词的 `git log --all --grep` 历史 + 一次 `status --short`，汇总。

expect.json 边界：

```
max_turns 10, min_git_calls 4, max_bash_calls 0,
max_bash_chains 0, max_bash_git_calls 0, max_git_subcommand_dupes 0
```

`min_git_calls: 4` 严格：3 个 grep + 1 个 status，各一次调用；把 grep 合并进一次调用也算违规（违反 one-query-per-call）。该 prompt 形状与触发 FM-B 的真实会话逐字同构。

## 5. 运行协议

- 宿主：WSL（`/mnt/d/www/github/seek`）；seek 二进制为交叉编译的 **seek-linux**（Windows .exe 无法在 WSL 执行；eval 测的是 LLM 的工具构造行为，纯 API 侧，与宿主平台无关——`bash_chains` 数的是链式形态，不依赖 cmd.exe 的失败表现）。
- 二进制版本：`v0.10.1-...-0fde5dc+`（dirty，含新指标代码）。
- **Phase 1（基线）**：`git-multi-query-no-bash` ×10 + `git-subcommand-shape` ×3 + `git-subcommand-midtask` ×3，共 16 次；结果快照 `*_BASELINE.jsonl`。
- **Phase 2（干预）**：I1–I4 落地，`go test ./...` 全绿，重建 seek-linux。
- **Phase 3（干预后）**：同 Phase 1 的 16 次，结果快照 `*_POST.jsonl`。
- 样本量依据：README 与 `git-subcommand-shape` 的 caveat 要求 ≥8 runs 才有意义的基础率；10+10 由用户拍板。

## 6. 风险与限制

- **单会话偏差**：eval 是全新单 prompt 会话，而 FM-A/B 的动机现场是长会话中段——长上下文依赖的 tic 可能测不出（`git-subcommand-midtask` README 已记录同一局限）。基线冒烟已复现 FM-B/C（3/3 bash 链），故本方案的可测性已获实证。
- **`bash_chains` 误报**：命令内合法出现的 `;`（如字符串字面量）会计入；eval prompt 不诱发此类命令，噪声可接受。
- **Linux 宿主差异**：链式命令在 `/bin/sh` 下不会像 cmd.exe 那样报错，但指标数的是"链"这个形态本身，判定不依赖错误表现。
- **成本**：26 次真实 API 调用（DeepSeek），预估 $1 量级；单次运行 ~100s。

## 7. 结果

### 7.1 原始数据

- Phase 1 基线（二进制 0fde5dc+）：`eval/results/*_BASELINE.jsonl`（17 行：批处理 16 行 + 冒烟验证 1 行 —— 同二进制同 prompt，harness 校验用；该行同样复现 tic：`bash_calls=3, bash_chains=3, bash_git_calls=3`。§7.2 统计按预注册协议只取批处理 10 行——计入冒烟行只会让基线更差、对比更强。运行日志在 gitignored 的 `.tmp/`，数据以 jsonl 快照为准）
- Phase 3 干预后（二进制 438e5bf+，16 runs）：`eval/results/*_POST.jsonl`
- 干扰核查：438e5bf（用户中途提交的 TUI 布局提交）只动 `internal/tui/view.go` + docs，不触 sysprompt/tools/eval/AGENTS；headless `-json` eval 不走 TUI 渲染路径 → 对比归因干净。post 二进制经 `grep -c` 验证含新描述字符串（"One query per call" ×2、"NEVER chain commands" ×1）。
- 测量后措辞微调（不重新测量，语义未变）：`bash_git_calls` 正则由 `\bgit\b` 改为命令 token 匹配（合成数据单测通过：`.git`/`github.com`/`gitignore`/`go build` 排除，`cd /x && git push` 等链式命中；旧正则对 `cd .git`/`ls .git` 有误报）；bash 描述里 "'&&' chains are fragile" 改为 "chains defeat per-command output and permission granularity"（`&&` 在 cmd 下合法，原措辞不准确）。

### 7.2 主 case `git-multi-query-no-bash`（各 10 runs）

| 指标 | 基线 | 干预后 | 判定 |
|---|---|---|---|
| PASS 率 | 4/10 | **9/10** | H4 PASS（≥8/10） |
| `bash_git_calls > 0` 的 run | 6/10 | **1/10** | H2 PASS |
| `bash_chains > 0` 的 run | 5/10 | **1/10** | H3 PASS |
| `git_subcommand_dupes > 0` 的 run | 1/10（单次 run 内 4 次！） | **0/10** | H1：干预后 PASS；基线护栏被打破一次（FM-A 现场实证） |
| `completion_tokens` 中位数 | 2972 | 3116 | H5 PASS（≤ 3715 上限） |
| `git_calls` 均值 | 5.7 | 7.0 | 达标形状更彻底（并行多调用） |

### 7.3 回归守卫（各 3 runs）

| case | 基线 | 干预后 |
|---|---|---|
| `git-subcommand-shape` | 2/3（1 次 bash 链溜过"不要用 bash"禁令） | **3/3** |
| `git-subcommand-midtask` | 3/3 | 3/3 |

### 7.4 判定

**H1–H5 全部达成目标方向，干预合入。**

- FM-B/FM-C（bash 链、bash 跑只读 git）从 5-6/10 降到 1/10；FM-A（subcommand 重复）在基线 run 5 以 4 次连发的形态复现，干预后归零。
- 唯一残留：post run 2 一次 `bash_calls=1`（链式 git）——tic 被强烈抑制但非零；同时该 run `git_calls=10`，说明模型主路径已正确。README 哲学：趋势胜过单次，n=10 下 6/10→1/10 的 Fisher 双侧 p≈0.057，方向一致的三指标合取支持"描述调优有效"结论。
- 一个附带证据：基线 `git-subcommand-shape`（prompt 明令"不要用 bash 跑 git"）仍漏进 1 次 bash 链 —— 禁令不如描述；干预后该 case 3/3。
- 成本护栏 H5 未破：描述变长没有诱发更啰嗦作答。

### 7.5 遗留

- post 阶段有 1 次残留（1/10）：长会话中段是否更顽固（`git-subcommand-midtask` README 记录的局限）需真实长会话观察；eval 是全新单 prompt 会话。
- 若未来要压到 0：可在 bash 工具对 `git` 开头的只读命令加 hint（仿 `internal/tools/bash/hint.go` 的 plan-analyze 提示），或把 git 白名单做成模型不可绕过的唯一路径（当前拒绝列表已在 `internal/tools/git` 内）。
