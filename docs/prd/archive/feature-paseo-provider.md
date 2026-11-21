# Feature: seek 接入 paseo 作为 provider（可行性设计稿）

**所属版本**：独立 feature（平台化线，与 [`feature-remote-control-plane.md`](../feature-remote-control-plane.md) 平行的另一条"外部驱动 seek"路径）
**前置阅读**：[`feature-acp.md`](feature-acp.md)（柱 P：`seek acp` 现状）、`internal/acp/server.go`、`cmd/seek/main.go` 的 `runACP`/`acpBackend`/`buildProvider`、`~/.paseo/config.json` 的 `agents.providers.zcode`（acp 型 provider 先例）、butler 仓 `internal/drive`（`paseo.go` 派发、`lane.go`/`usagepaseo.go` 车道额度闸）
**状态**：📐 **设计稿**。只出文档不改代码；落地另开卡（§6 里程碑是拆卡建议）。paseo 侧行为来自对 Paseo.app `app.asar` 内 ACP adapter（`createAcpProviderConnection` / `AcpRuntime`，2026-11-12 快照，SDK `@agentclientprotocol/sdk` v1）的逆向阅读——结论以 capability 协商语义为准，不依赖其内部实现细节。

**一句话**：seek 已经会说 ACP（`seek acp`，Zed 实测过），paseo 已经有 `extends: "acp"` 的 provider 通道（zcode-acp 桥先例）——所以**不需要再造 zcode-acp-server 那样的桥进程**，`seek acp` 本身就是 paseo 要 spawn 的 ACP server。真正的缺口在 seek 侧三件事：① `session/new` 的 cwd 今天被忽略，而 paseo **不**给子进程设 cwd（工作目录只从协议到达）；② ACP 模式下 flag 全不解析 → 权限钉死 Deny，无头派单跑不了 bash；③ 无 key 时启动即退，paseo 的能力探测进程过不了 initialize。补齐后验收 = butler drive 派一张最小卡走 `--provider seek` 全流程写回。

---

## 1. 背景与结论

- **需求侧**：fleet 里 agent 干活的"车道"由 paseo provider 构成（claude / codex / zcode）。seek 背后是 DeepSeek（默认 V4.1-Flash），按 token 计价、无订阅额度窗口——把它接成一条车道，就是给 butler drive 的 `--providers` 清单加一个**最便宜、fail-open 的兜底车道**。
- **供给侧**：seek 的 ACP（v7 柱 P）是 stdio newline JSON-RPC，实现了 `initialize` / `session/new` / `session/prompt` / `session/cancel` + `session/update` 通知，真 Zed 验证过。paseo 的 acp 型 provider 正是 spawn 一个这样的进程当 ACP server 驱动。
- **zcode 先例的误导性**：zcode 需要 `zcode-acp-server` npm 桥，是因为 zcode 本体不会说 ACP（桥把 `zcode app-server --stdio` 的内部协议翻成 ACP）。seek 本体**就是** ACP server——桥这一层整体不存在工作量，配置块写对即可。
- **结论**：可行性成立，且工作量小（seek 侧一个中等卡 + paseo 侧一处主会话人工配置 + 一张验收卡）。三个必答问题分见 §2（形态）/ §3（env 与 key）/ §4（验收）。

## 2. ① 进程与协议形态

### 2.1 paseo 侧生命周期（asar 逆向证实）

| 阶段 | paseo 做什么 | seek 现状 |
|---|---|---|
| provider 注册 | spawn 一个**探测进程**，发 `initialize`（`clientInfo: paseo`，10s 超时），读回 `agentCapabilities` 后关闭该进程 | ✅ `initialize` 回显 protocolVersion + `loadSession:true`（有持久化时）+ `promptCapabilities.image:true`。**2026-11-19 起** `loadSession` 不再恒 false |
| 能力折算 | `loadSession:true` → session.persistence；`sessionCapabilities.list` → session.list；`promptCapabilities.image` → prompt.image | seek 报 `loadSession:true` → paseo 开 session.persistence 并调 `session/load`（**2026-11-19 已实现**，见 §2.4）；不报 list → **不**调 `session/list` |
| 每个会话 | **再 spawn 一个独立进程**，`session/new {cwd, mcpServers, _meta._paseo{…}}` → `session.ready` | ✅ 处理（D-1 已修：cwd 生效，见 §2.2；**2026-11-21 起 `mcpServers` 也已生效**，stdio 条目按声明起连、工具以 `mcp__<server>__<tool>` 进会话，卡 c_oyt6ft / guide-zed §5.3） |
| 对话轮 | `session/prompt {sessionId, prompt[]}`，串行排队（promptLane），收 `session/update` 流，等 `stopReason`；打断 = `session/cancel` | ✅ 全支持（Zed 已验证同一路径） |
| 进程退出 | stdin 关闭 → seek rc=0 退出；会话中异常退出 → `session.runtime_failed` | ✅ EOF 干净退出（e2e 有测） |

关键点：

- **必需方法集恰好是 seek 已实现的全部**。ACP spec baseline = `session/new` + `session/prompt` + `session/cancel`（+ `initialize`）；paseo 多用的 `session/load`/`list`/`set_config_option` 等全部经能力探测门控，seek 不申报就不会被调。seek 对未知 request 回 `-32601`，与 SDK 的 methodNotFound 语义一致——即使 paseo 未来多调一个可选项，也只是该功能不可用，不会挂会话。
- **传输同形**：paseo 用官方 SDK 的 `ndJsonStream`（newline JSON-RPC，无 Content-Length），与 `internal/acp` 完全同帧。`PROTOCOL_VERSION = 1`，seek 回显客户端版本。
- **一次 paseo 会话 = 一个 seek 进程**（外加注册时一个短命探测进程）。seek 的"一进程一 session"MVP 与此天然匹配，无多 session 并发问题（feature-acp.md backlog ③ 在此场景不构成缺口）。
- **env**：`spawnProcess` 用 `{...launch.env, ...provider.env}` 合并——config 里 provider 块的 `env` 逐字注入子进程（zcode 先例实证）。
- **不再丢弃的字段**：`session/new` / `session/load` 的 `mcpServers`（2026-11-21 接通，见 §2.1 表与 `docs/guide-zed.md` §5.3）。仍丢弃：`_meta._paseo.systemPrompt/providerOptions/toolPolicy`（paseo 级系统提示词不生效——seek 用自己的 sysprompt；drive 场景卡面 prompt 走用户消息，**这正是想要的**语义）。paseo 会话建立后按返回的 `modes`/`configOptions` 决定是否发 `set_config_option` 类调用；seek 两者皆空 → paseo 把 session.configure 能力过滤掉、UI 不给这个 provider 出模型选择。注意：**不要**在 paseo 里给 seek provider 配 providerOptions，否则可能触发对 seek 的设置调用（回 -32601，理论无害但没必要试）。

### 2.2 D-1（必做缺口）：`session/new` 的 cwd 必须生效

> **已落地（drive/c_bnjrw5）**：`acp.AnchorSession`（`internal/acp/session.go`）——`os.Chdir`（git/grep/list_dir 的进程锚）+ `Policy.Reanchor`（read/edit/write/bash 的策略锚 + cwd 内写白名单）一次完成，ProjectID 按 `paths.ProjectID(abs(cwd))`、sessionId 取目录名；缺省仍为当前目录，cwd 不可用则 fail-loud 拒建会话。方案取下文 2 的简化版（锚定共享栈而非整套重建：AGENTS.md/sysprompt、memory、hooks project 上下文、LSP root、plan artifacts 仍按启动 cwd——paseo 探测进程下无 AGENTS.md 加载需求，残余留给后续卡按需补）。

**这是本设计最重要的发现。** paseo spawn ACP 进程时**不传 cwd**（`spawnProcess` 只给 env/stdio，子进程继承 paseo daemon 的 cwd——GUI 拉起，通常是 `/`）；工作目录**只**通过 `session/new` 的 `cwd` 参数到达。而 seek 的 `acpBackend.NewSession` 今天返回固定的 `"seek"` 会话 id、完全忽略 `p.Cwd`（`cmd/seek/main.go:2396`）——Zed 场景下恰好 spawn 时就带对了 cwd，所以从没暴露。

不做 D-1 的后果：paseo 下的 seek 会以 daemon cwd 运行——权限沙箱的"cwd 内写白名单"指向错误目录、AGENTS.md/project 指引加载不到、session 归档落错 project、bash 在 `/` 里跑。**不是降级，是全错。**

方案（按 MVP 从简到全）：

1. **fail-loud（最小）**：`NewSession` 时若 `p.Cwd` 与进程 cwd 不一致，返回 error 拒绝建会话。至少不会静默跑错目录，但等于不可用——只配当过渡。
2. **per-session 重建（推荐）**：一进程一 session 的现状下，`NewSession(p)` 按 `p.Cwd` 重建该会话的整套栈（policy 以新 cwd 起、session store 按 project 归档、AGENTS.md 重读、agent 重挂工具），等价于 TUI 启动时以 `abs` 为轴的那套接线挪到 NewSession 触发。顺带兑现 feature-acp.md backlog ⑥（per-session cwd），Zed 侧同样受益。
3. （远期）多 session 各持独立栈——backlog ③，与 paseo 场景无关，不做。

### 2.3 D-0（注册前置）：探测进程必须活着过 initialize

`seek acp` 的 acp 分支在 `buildProvider`（`cmd/seek/main.go:844`）**之后**；无 DeepSeek key 时 buildProvider 直接返回错误、进程退出 → paseo 探测进程 initialize 超时/失败 → **provider 注册不上**。所以"机器上有 key"是注册硬前置（来源见 §3.1），不是"用到时才报错"。另注意探测进程是短命 spawn：`seek acp` 启动路径上的任何慢副作用都会被无谓触发——**cron auto-tick**（`cmd/seek/main.go:1532`，acp 分支之前执行）会在每个探测/会话进程里拉起 tick goroutine。建议 acp 模式跳过 auto-tick（与 `--no-cron-tick` 等价的分支内判断），实现卡顺手带上。

### 2.4 session/load（2026-11-19 落地）

paseo 侧行为（asar 实证）：`initialize` 探测到 `loadSession:true` 才开 `session.persistence`；"继续对话"时**先 spawn 进程**、`initialize`、然后 `connection.loadSession({sessionId, cwd, mcpServers})`，且**在 `session/load` 解析之前**就把 native id 注册好——它靠 "ACP agents replay the loaded session's updates before session/load resolves" 这条注释解释顺序。因此实现必须满足两条硬约束：

1. **通知先于响应**。回放帧全部写出后才回 result；`internal/acp` 的 dispatch 里 `session/load` 走同步路径（不可打断、不持有 prompt 需要的锁），回放与响应共用同一把写锁保证顺序。
2. **session/new 返回的 id 必须是持久 transcript id**。原先是项目目录名（`filepath.Base(cwd)`），既指不到文件、也会随目录变化——paseo 存下来永远 load 不回来。现在返回 `session.ID`（JSONL 文件名）；`--no-save` 无持久化时退回项目名，且 `loadSession` 申报 false。

实现要点（`cmd/seek/acp_replay.go` + `acpBackend.LoadSession`）：读 transcript → 跑与 `-resume` 同一套 `Repair()` → `AnchorSession(cwd)` 重锚 → 按 session 记录的 model 恢复车道（`subagent.ResolveRoute` 反查 provider）→ `*b.sess = *loaded` **就地换指针**（main 的 subagent parent-sid / x-opencode-session / checkpoint 闭包都捕获了原指针，reassign 会让它们继续描述旧会话）→ `ag.Reset(messages)` → 逐帧回放。回放映射：system 跳过（系统提示词按进程重算）、user/assistant → `*_message_chunk`、assistant 的 tool_calls → `tool_call(status=completed)`、tool 结果 → `tool_call_update(rawOutput=…)`。

顺带补上的另一半：**ACP 车道原先不落盘**（只有 TUI / print / rpc 保存 session），`session/load` 无米下锅。`acpBackend.Prompt` 现在按 turn 保存（`agent.TurnEnd` 后一写，与 runPrint 同频；整文件原子重写，按事件写会把文件重写几百次）。

真 stdio e2e（两个独立进程，`SEEK_HOME` 隔离）：P1 `session/new` → prompt → 关闭；P2 `initialize`（caps `loadSession:true`）→ `session/load` → 收到 2 帧回放（`user_message_chunk`, `agent_message_chunk`）→ 跟随 prompt 正确答出 P1 埋的词；未知 id 回 `-32000` 并带上该 id。注意 `--no-save` 在 ACP 车道**够不到**（`flag.Parse` 停在 `acp` 位置参数，见 §3.3），所以实际部署里 `loadSession` 恒为 true；`acpBackend.Initialize` 的 `store != nil && sess != nil` 判断是给 store 为空的历史/测试路径留的belt-and-braces（那时 load 明确回「persistence is off」而不是假装成功）。

## 3. ② provider env、DeepSeek key 来源与无头注意事项

### 3.1 key 来源（优先级沿用 seek 现状）

`config.KeyFor`：`DEEPSEEK_API_KEY` env > `~/.seek/config.json` 的 `providers.deepseek.api_key`（setup wizard 写入）。

- **推荐**：走 `~/.seek/config.json`（本机已跑过 setup，key 已在盘）。provider env 块**不放 key**——key 留在 seek 自己的领地，不进 paseo config、不进 daemon env、不进任何日志（与 butler"token 只传文件路径"的规矩同构）。这也是 seek 跟 zcode 桥的实质差异之一：zcode 必须靠 env 钉 `ZCODE_PROVIDER`/`ZCODE_MODEL` 因为它包装的是另一个 CLI；seek 自己读自己的配置，env 只用来钉**行为开关**（§3.2）。
- env 注入 key 仅在需要"paseo 车道用另一把 key"时才考虑，MVP 不做。
- 代理：DeepSeek API 国内直连，无需任何代理 env；daemon 已注入的 `HTTPS_PROXY=7897` 对 deepseek.com 无碍（Verge TUN 规则放行/直连均可达），不特意配置。

### 3.2 provider 配置块（paseo 侧落地形状，主会话人工写入 `~/.paseo/config.json`）

```jsonc
"seek": {
  "extends": "acp",
  "label": "seek (DeepSeek)",
  "command": ["/usr/local/bin/seek", "acp"],   // 绝对路径：daemon 的 PATH 不可靠（zcode 先例同为绝对路径）
  "env": {
    "SEEK_ACP_YOLO": "1"                        // 见 D-2；首验期可加 "SEEK_ACP_LOG": "/tmp/seek-acp.log"
  }
}
```

写完 `paseo reload`。注意 `/usr/local/bin/seek` 必须是**含 D-1/D-2 的构建**（当前安装版未必有）——落地卡出二进制后先 `go build` 覆盖安装再 reload。

### 3.3 D-2（必做缺口）：无头权限姿态

现状：`seek acp` 以 `os.Args[1]=="acp"` 短路（`cmd/seek/main.go:1699`），而 `flag.Parse` 在第一个位置参数处停止 → **一个 flag 都不消费** → pref = `PrefDeny`（`main.go:920`）。Deny 下：cwd 内写/编辑放行，**bash 全拒**（read-only 白名单除外，`internal/permission/permission.go:615`）。drive 卡要 `git commit` / `go test` / `go vet`——跑不了。

方案：**`SEEK_ACP_YOLO=1` env → `SetPref(PrefYolo)`**，照抄无人值守先例（`goal`：`main.go:1716`，autopilot：`main.go:1687`，注释原话"Unattended loop → elevate to yolo … no human to ask"）。并建议同船把 `autopilot.IsRemoteMutating` 接成 bash command guard（照抄 autopilot 的做法）——yolo 放开本地操作但照堵 push/PR 类远程动作，与 drive 卡自身的沙盒纪律（"绝不 push"）互为双保险。显式 env 开关而非默认 yolo：Zed 等交互场景仍可保持 Deny/后续接 `session/request_permission`（feature-acp.md backlog ①，留作后续增强，paseo 端有 permission 渲染能力等着对接）。

> ✅ **D-2 已落地**（卡 c_rewul5，2026-11-12）：`internal/acp/headless.go`（`YoloEnvVar`/`YoloFromEnv`）+ `cmd/seek` 在 ACP 车道把它并进 `initialPref` 解析（在 `sysprompt.Compose` **之前**，非事后 SetPref——否则系统提示词 Mode 标签还写 deny，模型根本不会去试 bash）。实现细节与设计稿的一处偏差：env 只在 `os.Args[1]=="acp"` 车道生效（防 shell 残留 env 静默 yolo TUI）。用法/警告写进 `docs/guide-zed.md` §5.1。~~`IsRemoteMutating` guard~~ **guard 已同船补上**（卡 c_mvfi4i，2026-11-12）：`acpYolo` 车道在主会话 bash（`goalBash` 接线）与 subagent runner（`bashGuard`）两处挂 `WithDeny(autopilot.IsRemoteMutating)`——yolo 只放本地，push/PR 类远程动作照堵；gate 在 `acpYolo` 而非裸 `acpLane`（Deny 的 Zed 交互会话不受影响）。cron-tick 跳过**仍未**同船（后续卡）。

### 3.4 其它无头注意

- **stdout 是协议通道**：acp 模式日志全走 stderr（现状已保证）；首验排查用 `SEEK_ACP_LOG` tee 原始收发（已有，`main.go:2557`）。
- **无桌面依赖**：DeepSeek 直连 API key 认证，无 OAuth/验证码/桌面 App 会话——zcode 桥"headless 不能用 start-plan 套餐"那类坑在本路径**不存在**（那是订阅套餐绑桌面验证码的产物；seek 用 API key）。
- **模型钉法**：acp 模式吃不到 `--model`，用默认 `deepseek.ModelV41Flash`（`main.go:2751`）——恰是"最便宜车道"的题中之义。将来要可选型号：加 `SEEK_MODEL` env 或实现 ACP `modes`/`configOptions` 让 paseo UI 出模型选择，均留后续卡。
  > ✅ **两条路都已落地**（卡 c_6thhqc，2026-11-18，"C 一步到位"拍板）：①env 钉定改名 `SEEK_ACP_PROVIDER`/`SEEK_ACP_MODEL`（与 `SEEK_ACP_YOLO` 同款 ACP-scoped 命名，`internal/acp/headless.go`，在 `buildProvider` 之前注入）；②完整 configOptions——`session/new` 返回 `configOptions`（`id:"model"` select，value id 为 `"<provider>:<model>"`，按 key 在不在门控）+ `session/set_config_option` 分发 + `agent.SetLane` 回合间整车道换挡（prompt 的 cancel 注册前移到 dispatch 同步段，换挡窗口无竞争；回合中切换回 `-32000` 拒）。wire 形状逆向自本机 `@agentclientprotocol/sdk` schema + zcode-acp-server 参照桥（`~/.paseo/zcode-acp/`）。用法与限制见 `docs/guide-zed.md` §5.2。`modes`（plan-mode 映射）仍留后续。

## 4. ③ 验收路径：butler drive 最小卡

### 4.1 前置（主会话人工执行，非本卡范围）

1. seek 侧 M1（§6）合入并重装 `/usr/local/bin/seek`；
2. §3.2 provider 块写入 `~/.paseo/config.json` + `paseo reload`；
3. 冒烟：`paseo run --provider seek --cwd /tmp/seek-smoke "回复 PONG 两个字，别的什么都不做"`——收到回复即握手全通（探测 → spawn → new → prompt → update → end_turn）。

### 4.2 最小卡定义

一张 docs-only 卡（不碰代码，验证的只是车道本身）：repo=`whyiyhw/seek`，标题形如「验收：seek 车道冒烟——在 docs/ 下新建 `tmp-paseo-acceptance.md` 写一句话并 commit」。走 drive 全流程开 worktree 分支 `drive/<卡id>`（但ler 现成机制，多仓派发按 repo 路由）。

### 4.3 派发与车道

- **第一步（人工通道）**：`butler drive run --only <卡id> --provider seek`——单车道直验，不掺额度选择。
- **第二步（纳入批量）**：~~`--providers` 清单加 `seek`~~ **用户拍板：不纳入（2026-11-13）**——DeepSeek 按量计费涉及真金白银，不进自动车道清单、不做额度尽时的兜底；seek 车道仅保留 `--only --provider seek` 人工通道（验收与定向任务用）。将来要启用须用户再拍板，agent 不得依据本 PRD 原推荐自行添加。
- **额度闸零改动**：`paseo usage ls` 没有 seek 的插件条目 → butler 判 `laneUnknown` → **fail-open 照派**（`internal/drive/usagepaseo.go` 现成逻辑），唯一代价是 seek 车道排序退到"已知有余量"车道之后——与"兜底"定位恰好一致。后续可选优化：DeepSeek 余额 API 探针（类比 `bigmodel.go`），非必需、不阻塞验收。
- 1308 类额度暂停文件、会话自愈等机制与 seek 无关（DeepSeek 按 token，不撞订阅窗口），不适用即不生效。

### 4.4 通过标准（全绿才算）

1. 会话正常 spawn，prompt 流回（`agent_message_chunk` → `end_turn`），无 `session.runtime_failed`；
2. 卡片工作完成：worktree 分支上有 commit、文件内容正确；
3. `.butler-commit.md` 写回（kind=progress/note 即可）；
4. drive 收活点的**新鲜上下文验证会话**（claude-judge）机械绿 → 卡过 review 置 done（`drive/<卡id>` 分支待人工合并，流程不变）。

任何一步挂：卡置 blocked + blocker 写明挂在哪一步——尤其优先怀疑 D-1（症状：agent 在错误目录干活/写文件被权限拒）与 D-2（症状：bash 工具全 deny，错误信息带 "re-run seek with --yolo"）。

## 5. 风险 / 开放问题

- **paseo adapter 是私有的**：本设计对 paseo 侧行为的断言来自当前版本 asar 快照。但接口契约是公共 ACP spec + capability 协商——只要 seek 严格按 spec 的必需子集说话、可选能力如实申报（`loadSession` 现按持久化能力申报 true/false），paseo 升级不应破坏。若未来 paseo 把某可选项变必需，seek 回 -32601 会显式失败（fail-loud），不会静默错。
- **`_meta._paseo.systemPrompt` 不生效**：paseo 里给 agent 配的系统提示词到不了 seek（seek 用自己的 sysprompt + AGENTS.md）。drive 场景无碍（卡面即用户消息）；若未来想透传，需 seek 读 `_meta`（开放问题，暂不做）。
- **会话持久化（2026-11-19 已补）**：`loadSession:true` → paseo 把 `session/new` 返回的 id 存为 native session id，"继续对话"时先 spawn 进程再 `session/load`，seek 读回该 JSONL、重锚 cwd、恢复模型车道并把历史回放成 `user_message_chunk` / `agent_message_chunk` / `tool_call(_update)`。因此 `session/new` 的 sessionId **必须**是持久 transcript id（原先是项目目录名，无法 load），`--no-save` 下退回项目名且不申报 `loadSession`。实现：`cmd/seek/acp_replay.go` + `acpBackend.LoadSession`。
- **验收用的 seek 二进制版本**：装机版落后于源码时症状诡异（没有 D-1/D-2 的 acp 进程"能跑但全错"）——§4.1 第 1 步的重装不可省。
- **cron auto-tick 副作用**（§2.3）：探测进程短命、tick goroutine 被杀，理论上有半途 cron 任务的风险；M1 里跳过即除根。

## 6. 里程碑（落地拆卡建议）

| M | 内容 | 执行方 |
|---|---|---|
| M1 | seek 侧：D-1 `session/new` cwd（per-session 栈重建，兑现 backlog ⑥）+ D-2 `SEEK_ACP_YOLO`（+ `IsRemoteMutating` guard）+ acp 跳过 cron auto-tick + 测试（cwd 切换、yolo env 开关、无 key 启动失败的探测语义）。**实拆四卡**：D-2 先落地（c_rewul5，guard/tick 未同船）；guard 补齐 = c_mvfi4i；D-1 = c_bnjrw5；cron-tick 跳过 = c_rjg6hs（`cronAutoTickEnabled` 单门 + 单测）。**M1 全部完成** | drive 卡（代码，seek 仓） |
| M2 | paseo 侧：重装 seek 二进制 → 写 provider 块 → `paseo reload` → §4.1 冒烟 | 主会话人工（沙盒外配置文件） |
| M3 | 验收：§4.2 最小卡走 `--only --provider seek` 全流程 → 纳入 `--providers` 清单 | drive 卡 + 主会话观察 |

## 7. 与其它 PRD 的关系

- **[`feature-acp.md`](feature-acp.md)**：本卡是其续篇——backlog ⑥（per-session cwd）从"优化项"升格为 paseo 场景**必做**（§2.2）；backlog ①（request_permission 审批）与 ③（多 session）维持不动；backlog ⑦（Zed mcpServers 透传）**2026-11-21 已落地（卡 c_oyt6ft）**——paseo 场景同构存在，两条车道共用同一实现（`internal/acp/mcp.go` + `cmd/seek/acp_mcp.go`），见 §2.1 表。
- **[`feature-remote-control-plane.md`](../feature-remote-control-plane.md)**：两条平行的"外部驱动 seek"路径——本卡走 **ACP/stdio**（paseo 当 ACP client，每会话一个直连子进程），控制平面走 **Frontend 端口**（`seek serve` sidecar，收发分离）。不共享代码，共享"零 daemon、直连子进程、核心 agent loop 不动"的哲学；paseo 车道落地不依赖控制平面任何部分。
- **架构约束继承**：`internal/acp` 不引入新依赖；协议字节确定性（prefix-cache）不受影响（ACP 是外层传输，不动消息历史）；工具 schema 零改动。
