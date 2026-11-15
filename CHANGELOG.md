# 更新日志（CHANGELOG）

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
- c_r5dm2f：v0.11 发布准备：版本号 + CHANGELOG 汇总 + tag 草稿（发版动作留人）
- c_mvv5td：book 与 PRD 重复事实清单（缩并备料，只盘点不改）
- c_7afk5i：文档预算首轮缩并：budget.json 超限区开刀（每版一刀文档刀）
- c_jvn3ky：skill-distill 人审 CLI：/skill drafts 列表·预览·审批安装一条流
- c_oh2dl3：/distill dry-run 预览：显示将产出的草稿不落盘
- c_kql3ip：eval 扩容：GLM 车道契约用例——路由/计价/cache 字段锁回归
- c_7pptol：web frontend 设计稿：control-plane 首个真实渠道 + inspect-rpc 数据面并入评估（feature-web-frontend.md）
- c_c26lvq：DeepSeek 全家桶 API 接入评估设计稿：read_url/文件输入/ASR-TTS/安全参数（feature-deepseek-app-api.md）
- c_bg7hax：pitfalls 台账清点：190 条过时/已修复标记（只标记不删）
- c_3gp2e7：AGENTS.md 预算 ratchet 下一刀：3084→≤2800（每版一刀 · 向 1800 目标收，repo=whyiyhw/seek）
- c_26ltej：README 双语 v0.11 叙事刷新：平台化定位（多端/自进化/成本三轴）
- c_tbqxnn：session export：会话导出为 markdown 对话记录
- c_bickfj：编排 P1-a：后台子代理生命周期骨架——spawn 可后台化 + 完成事件进 session 流
- c_zdjkgf：编排 P1-d：后台子代理完成通知的 TUI 呈现——状态徽标在跑→已完成迁移 + /agents MODE 列（bg/送达✓）
- c_nym2sn：pricing.go 费率刷新：flash 现价 ¥2/M 高峰 miss（快照停 2026-09-10 已过期）+ v4-pro 独立行核对
- c_mb5d7p：session/subagent ID 熵扩宽 24→64 位（NewRunID 同款修法，v0.11 CI 实录同因，repo=whyiyhw/seek）
- c_wxbdkz：R0 验证门：read_url 与 function tools 共存性实测（feature-deepseek-app-api §2.2，红则①降级不接）
- c_lxferp：eval 基线趋势：结果 JSONL 汇总脚本——与上一版对比报告
- c_onl4et：信号：eval-trend 自测未登记 selftest.sh——c_lxferp 验证「未跑成」阻塞放行
- c_o77s3m：编排 P3-a：hooks 注入面设计——受控注入区与缓存稳定区分离
- c_iha3fr：web 聊天 markdown 最小渲染：代码块+行内码样式（§2.6 落地，零依赖，c_vn44rc 首卡）
- c_kkpwe7：编排 P2-a：类型化 agent 定义格式——配置文件与加载器
- c_uzmzzi：book v0.11 章节更新：control-plane/GLM 车道/skill-distill 进设计书
- c_jqwd72：skill-distill 三期：复发累积（N≥2）与蒸馏质量打点报表
- c_7xmecm：编排 P2-b：per-type 工具白名单接线——permission 层收紧
- c_x7ehom：serve web 地址可配置：serve 段 web_addr（解 MVP 硬编码 loopback）
- c_c4cs4a：web 会话历史·W4.1：POST /rpc JSON-RPC 2.0 envelope + method registry + 错误码
- c_fzqsbi：worktree GC 加年龄兜底：never-discarded 孤儿 ref 永生（6 个 6 月实例手工清除实录）
- c_jdwywj：skill-distill：StrengthenDraft 按 SessionID 去重缺口（二期遗留，c_jqwd72 巡检确认未修）
- c_u4ugnu：编排 P1-b：后台子代理续话复活——agent 工具 session_id 续话与 run_in_background 组合（PriorMessages 上下文延续、resumed_from 血统、短 id 唯一解析、后台续话完成经 notice → Notifier 进主会话流）
- c_3d2bil：编排 P2-c：内置类型包升级——explore/judge/general 类型化+默认路由
- c_zam44t：web 会话历史·W4.2：session/list|get|messages 三 method（cursor 分页=全文件顺序、尾读不锁）
- c_snerqc：web 平面契约自测脚本：serve HTTP 三断言进 selftest 家族
