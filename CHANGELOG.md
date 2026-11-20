# 更新日志（CHANGELOG）

## v0.12.1（2026-11-18）

v0.12.1 — 一次只动内核的小发行：**`propose` 多了一格必填**。计划里「怎么算做完」从散文变成 `done_when` 字段——没有完成条件的提案会被工具直接拒收，并要求模型回去继续分析；`reviewed_by` 记录「谁来验结果、从哪个源验」。配套新增内置 skill `plan-contract`（关卡该加在哪、决策该交给脚本/异源/人、教训什么时候不该变成永久规则）。同时开始拆 `cmd/seek/main.go`：`envgate.go` 是第一个搬出去的文件，纯搬移、零行为变化。

### Notable changes

- **`done_when`（行为变更，必填）**. `propose` 的 schema 现在要求 `done_when`：1–12 条断言（建议 3–5，每条 ≤240 字符），每条都要说清拿什么去对照——一个测试、一条命令加预期输出、一个文件加要 grep 的词、一条必须仍成立的不变量、一个页面。缺失时工具返回可恢复的错误并把模型推回 ANALYZE，而不是让一份「描述」冒充计划。通过后的工具结果会把这些断言回显一遍，执行期间一直在工作记忆里。
- **`reviewed_by`（新，可选）**. 一句话写明结果评审的来源与材料（「新开会话，喂 plan + diff + 原始消息，人决定改不改」）。写出它时，工具结果会明确禁止模型自称已验证。
- **plan 快照多两节**. `~/.seek/projects/<id>/plans/*.md` 新增 `## Done when`（复选框列表，位于 Problem 与 Steps 之间）与 `## Reviewed by`。旧快照不受影响，节标题在 PRD §8.12 里追加为锁定项。
- **新内置 skill `plan-contract`**. 讲计划**内容**而不讲工具机制（后者仍是 `plan-mode`）：关卡该加在什么地方（撤不回的一步之前 / 没人逐项看的产出往下走之前）、每个决策按答案出处路由到「检」（脚本）/「查」（异源 agent 或换车道）/「定」（人，批量问）、以及复盘的三种下落——规则、关卡、或带关闭日的待定条目（并且「测不出来」按撤回算，不按通过）。写作动机是仓库自己的账：`docs/pitfalls.md` 现在 249 条、关闭 0 条。
- **内核减重第一刀**. `cmd/seek/main.go` 开始按主题拆文件：`envgate.go`（`envBoolTrue` / `autoDistillEnabled`）。纯搬移，`go build` / `go vet` / `go test -race ./...` 全绿，CLI 表面逐字节不变。
- **修复**. `seek serve --frontend=web -h` 不再以退出码 2 打印 usage；`-h` / `--help` 现在在任何位置都算求助，`--help` 也会列出动词。OpenCode Go 车道补上浏览器登录与实时模型目录。

> **前缀缓存**：`propose` 的 schema 字节变了，升级后的第一次会话会全量重算 prompt 前缀（一次），之后照常命中。

更早的版本见 [GitHub Releases](https://github.com/whyiyhw/seek/releases)——每个 release body 与其 annotated tag 同文。本文件只保留当前版本，历史版本查 git 历史（`git show vX.Y.Z:CHANGELOG.md`）。

## 已进 main、未部署
- **ACP `session/load`（会话恢复，行为变更）**。`loadSession` 之前恒为 `false`，客户端没有「继续会话」。现在 `session/load` 读回该 JSONL、重锚 cwd、恢复会话的 model 车道，再把历史按 `user_message_chunk` / `agent_message_chunk` / `tool_call(_update)` 回放完才回响应。`session/new` 的 `sessionId` 从目录名改成**持久 transcript id**；ACP 车道原先不落盘、现在每回合存。e2e 验过。
- **二线车道的图片透传（行为变更）**。以前贴图只有 DeepSeek Flash 能收：`deepseek.IsVisionModel` 是一张只含 `deepseek-flash` 的单 id 白名单，被三个调用点共用（TUI/print/ACP 共用的提交期路由 + 两条发送路径的历史门），而它们都不知道车道。于是 OpenCode Go 车道（`--provider=opencode`，`deepseek-v4.1-flash`）贴图会得到「当前模型不支持图片输入」的提示并被丢弃——实测那个模型吃图。现在能力判定集中到新包 `internal/modelcap`（静态表覆盖各车道默认模型 + `internal/opencatalog` 按网关自己声明的 `modalities.input` 装运行时 grant，形状照抄 `pricing.SetModelRates` / `budget.SetLimits`），`pkg/llm.Message` 增加 `Images`，三个 adapter 各自编码（OpenAI 兼容车道数组形 `image_url` / Anthropic base64 image block / Gemini `inlineData`），`runTurnLLM` 与 `runTurnDeepSeek` 在同一站做「能力门 → ResolveImages → PrepareMessages」。纯文本消息的 wire 字节逐字节不变（前缀缓存）。提示文案不再点名 `/model deepseek-flash`——那个 id 只在 DeepSeek 车道存在，其它车道的选择器里没有它。文件（PDF/Word/PPT）仍不透传，`[file: … 未透传]` 提示保留。
- **no-remote 闸的拒绝文案改了（行为变更，只改文案）**。`git push` / `gh pr create` 一类远端动作在无人值守车道上仍被拦，但返回的拒绝不再声称「autopilot 无人值守」——那句话在 `seek acp` + `SEEK_ACP_YOLO` 上是错的（人在场、而且可能就是本人要求的）。现在它说清三件事：这个闸是尽力而为的字符串匹配、不是安全边界；它挂在哪条车道（`autopilot` / `goal` / `acp`，逐车道点名）；用户在本会话明确要求这项操作时，该由用户在终端或经脚本执行，而不是绕过去。bash 的拒绝串沿用 `[bash: blocked — ` 前缀并补了一行 `Hint:`，与 plan-analyze 的拒绝同形，模型能一致地自恢复。`IsRemoteMutating` 签名与拦截面零变化。
- c_2lbcck：bridge·M-B2：skilldistill.ConvertToSkill + ⚡ review s 全状态机 + 测试
- c_eyuxqm：MCP-D4：eval 三臂对照（A1 无 semble / A2 有无引导 / A3 全开）+ mcp_calls metric + Phase 1 判定数据
- c_clip4i：eval 隔离钉死：run.sh 强制 SEEK_HOME 沙盒 + 无人值守真 key 禁用规矩落文
