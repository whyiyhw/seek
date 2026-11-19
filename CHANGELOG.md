# 更新日志（CHANGELOG）

## v0.12.0（2026-11-18）

v0.12.0 — 多一条包月车道、多一张嘴：**OpenCode Go** 以 $10/月订阅价加入 DeepSeek / GLM / OpenAI 兼容车道；`seek serve` 长出**按住说话**（录音 → DeepSeek ASR → 一条真正的会话）；子代理可后台跑、可按 id 唤醒续话；MCP server 的工具拿到命名空间与提示文案；DeepSeek 文档输入（`@report.pdf`）与可选的 `safety_check` 落地。内核也开始公开减重：`-tools` 白名单和只读的摩擦报告，是 v8 里第一批能拿数字验证的刀。

### Notable changes

- **OpenCode Go 车道（新）**. `--provider=opencode` 接 OpenCode Go 订阅（$10/月，请求窗口限额、开放权重模型池）。seek 以自有 `User-Agent` + 每会话稳定的 `x-opencode-session` 头自报身份——缺这个头会静默退化成按 token 扣费。车道默认模型 `deepseek-v4.1-flash`；首启向导、子代理路由表、pricing 行（该车道只作参考单价展示）同步更新，`--base-url` 现在也能覆盖 GLM 与 opencode 的端点。
- **按住说话（新）**. `seek serve --frontend=web --frontend=voice` 给网页加一个 🎤 按钮：按住、说话（≤120 秒）、松开——音频经 DeepSeek ASR 转写（按秒计费），以独立会话 `voice:<device-id>` 进入控制平面，和别的会话一样用 `/bind` 绑定项目。麦克风需要安全上下文（localhost 直接可用；走 tailnet 要用 HTTPS 前置）。播报侧（TTS）的 `Speaker` 积木与计价已落地，CLI 开关尚未接。
- **文档输入（新）**. `@report.pdf` 走 DeepSeek Files API：提交期路由（copy-at-submit，本地资产库保留唯一副本）、复用 `file_id` 保住前缀缓存、`@rich:` 取图文混排布局；重放时文件标记淡显。`read_url` 在 R0 验证门里与 function tools 共存实测为红，**判定不接**。
- **`safety_check`（新，需显式开启）**. 配到 `high` 时 DeepSeek 车道带上模型安全校验参数；被拒会作为正常回合返回，而不是报错中断。
- **MCP 深度集成（行为变更）**. 工具默认注册为 `server__tool`（如 `semble__search`）；`mcp.json` 的 `prefix: false` 在裸名空闲时保留裸名，撞名仍回退到前缀形态。sysprompt 新增 `MCPSection` 段（每会话静态；空段与之前逐字节一致）承载 `hint` / `mcp_prompt` 文案；server 注册顺序确定化。
- **子代理：后台 + 续话**. `run_in_background` 的子代理完成事件进入主会话流（状态徽标、`/agents` MODE 列）；带 `session_id` 可连上下文与血统一起复活；`-resume` 回灌「已完成未通知」的子代理，完成事件不再丢。
- **类型化 agent 定义**. `~/.seek/agents/` 与 `<project>/.seek/agents/` 下的 SKILL.md 式定义（frontmatter 与 skill 共用一套方言）；`seek agents list/check/create/remove` 是唯一写入口，per-type 工具白名单接进权限层，内置 explore / judge / general 三个模板并带默认车道。
- **hooks 扩容**. 新增 SubagentStart/Stop、Stop、PreCompact、Notification 观察点与 shell 事件；`pre_prompt_inject` 是 opt-in 的单缝注入通道（默认 2048 B / 上限 8192 B、带截断标记、注入字节进 audit），守护测试锁住「注入后前缀前段字节不变」。
- **ACP 完整化**. `session/new` 返回 `configOptions`、`session/set_config_option` 运行时换模型、`SEEK_ACP_PROVIDER` / `SEEK_ACP_MODEL` 可钉定；`tool_call` 放出 kind/locations/rawInput/rawOutput/content，思考流走 `agent_thought_chunk`，文本 content 块不再污染整帧——结果只走 `rawOutput`。
- **web 工作台 W0–W4**. 流式聊天（delta 累积、工具行折叠）、会话浏览器（cursor 分页）、记忆与统计视图、后台子代理任务面板（SSE tail-follow）、`?token=` 换票 cookie + 写面 Origin/Host 闸，以及 CSP、gzip、CI 体积 gate。
- **薄内核旋钮与度量**. 顶层 `-tools` 白名单（会话启动即冻结，未知名 exit 2）；`/rpc` 新增只读 `stats/friction`（从 transcript 折叠出按工具的失败/被拒/重复）；`scripts/kernel-deps.sh` 内核依赖方向报告；`eval/ablate.sh` 裸核 vs 完整双跑对照；selftest 改发现制（新脚本自动进名单）；autopilot Outcome 带 token/成本与 diffstat。
- **修复**. `list_dir` 默认深度的输出补上「重新调用拿更深一层」的带内提示（`bash-overreach` 反复红绿交替的根因）；`seek agents remove` 先校验名字再碰文件系统；worktree GC 加年龄兜底；session / subagent ID 熵 24→64 位；`seek session export` 把 transcript 渲染成 markdown。
- **Quality**. eval 增加 ablate 双跑车道与趋势雷达；两个用例按实测重标定（`bash-overreach` 改绑「bash 不得先手」的排序界，`code-review-effort-max` 放宽 runaway 上限）；减法第 4、5 刀（含 `sandbox.Wrap` 的 linux/other 变体）清掉孤儿读写路径；29 篇已交付 PRD 归档进 `docs/prd/archive/`。

## v0.11.0（2026-11-13）

v0.11.0 — seek grows a remote control plane: `seek serve` turns the binary into one long-running, channel-agnostic service you can drive from a chat app or a browser; GLM (Zhipu bigmodel.cn) joins the provider list; sub-agents can run on a model of their own; and `/distill` turns a finished session into a reviewable skill draft.

### Notable changes

- **`seek serve` — remote control plane (new)**. Run seek as one resident process — drive it from a browser with the bundled web frontend (next item), or from any chat channel via the pluggable `Frontend` port (the port binds to no specific vendor). Each conversation binds to a project directory (`/bind` / `/projects` / `/switch` / `/new` / `/cancel`); messages exec the real `seek -p` inside that directory, keep the session id for multi-turn resume, serialize per conversation, and self-heal on crash. Graceful drain on shutdown; a macOS launchd template ships in `deploy/`. Guide: [`docs/guide-serve.md`](docs/guide-serve.md).
- **Web frontend**. `seek serve --frontend=web` serves an embedded chat page (zero build chain) with SSE streaming replies and token auth — point a browser at your machine or tailnet and drive seek from there.
- **GLM provider**. `glm` joins `deepseek` / `openai` / `anthropic` / `gemini`: set `GLM_API_KEY` (or a `providers.glm` block) and seek talks to the pinned bigmodel.cn OpenAI-compatible endpoint. glm-5.3 carries its own pricing card, so the live dollar stats stay accurate off the DeepSeek lane.
- **Per-sub-agent model routing**. The `agent` tool takes optional `provider` / `model` params — fan out cheap explore agents on one lane while the parent writes on another. Omitted params inherit the parent lane unchanged.
- **`/distill` — session → skill draft**. Distill the current session transcript into a SKILL.md draft under `~/.seek/skills/.drafts/` — drafts land in a dot directory no session ever loads, and installing one stays an explicit human `seek skill install`. Preview without writing anything via `/distill --dry-run`; review drafts on the CLI with `/skill drafts` (list · preview · approve-install). The opt-in auto path (batch-distill on session start) never installs on its own.
- **ACP headless hardening**. The `cwd` your editor passes with `session/new` now anchors every tool to that workspace (git, grep, read/edit/write/bash alike). For unattended runs, `SEEK_ACP_YOLO=1` auto-approves local actions while still blocking remote-mutating bash commands, and skips the cron auto-tick.
- **Fixed**: write/edit permission checks misjudged relative paths as "outside the workspace" whenever the shell sat behind a symlink (macOS `/tmp` → `/private/tmp` being the common case), wrongly denying edits. Both sides of the check now resolve symlinks through the same pipeline.
- **Quality**: behavioral eval suite grew 14 → 21 cases (session-id contract, print-mode permission semantics, tool-selection and edit-fallback regressions, glm-lane contract); three dead-code / gofmt / modernize sweeps.

## 已进 main、未部署
