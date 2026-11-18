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
- c_nl4nqg：web 多标签·per-conv 演进：conv-uuid+双 conv 隔离+多 tab 扇出一致（W1.3）
- c_zxeqih：docs 修复：feature-orchestration.md 状态行冲突标记残留（920f9e1 遗留）
- c_n3pghs：web 流式平面缝·一：executor RunStream + 导出 RunEvent（W0.1）
- c_7yofgo：web 任务面板·一：subagents.jsonl 只读 reader + GET /subagents（W3.1）
- c_ydglne：web 资产面·三：memory/projects/stats method 集（W4.3）
- c_axrls3：web 流式平面缝·二：StreamSender sibling 接口 + router upcast 接线（W0.2）
- c_jxhkbr：web serve CLI --listen flag 补缺（W1.1 尾巴）
- c_pbg5kp：web 流式平面缝·三：agent_start 加 model 字段（W0.3）
- c_qcmul4：web 流式接线：SendStream→SSE delta/tool/usage 事件族 + reply 收口带 session_id（W1.4，服务端 pricing 算 $，X-Accel-Buffering:no）
- c_hinacy：web 资产面·四：sessions 浏览器 UI·列表+消息分页（W4.4）
- c_w5lidm：web 鉴权演进：?token= 303 换票 cookie + 写面 Origin/X-Token 闸 + Host 校验（W1.2）
- c_3736yl：web 任务面板·二：SSE topic subagents tail-follow + 推送（W3.2）
- c_ph2zax：web 页面资产管线：CSP 头 + gzip + CI 体积 gate（W1.5）
- c_wno3o3：web 资产面·五：memory 浏览器 + stats 视图（W4.5）
- c_igze7m：web 资产面·六：hooks/audit 只读 + hooks/list + inspect-rpc 状态注记（W4.6）
- c_mn2wdp：web 前端·guide-serve §web 文档收尾 + 体积 gate 上线（W2.4）
- c_evbzyh：web 前端·指令快捷按钮 + 绑定状态常显（W2.2 增量）
- c_ujgmpu：web 任务面板·三：后台子列表/状态/tokens UI + 详情（W3.3）
- c_5ckroy：deepseek-app-api·S2：pkg/deepseek ASR 客户端 + pricing.ASRCost
- c_2lu6vb：deepseek-app-api·F2：internal/filerefs 检测器（后缀表+stat 门，照 imgrefs 先例新包）
- c_wsf7us：orchestration·M-O3.1：hooks 新观察点（SubagentStart/Stop、Stop、PreCompact、Notification）+ shell 事件 + audit
- c_npafpi：web 前端·状态栏 tokens/$ 实时累积 + session_id 短显（W2.3）
- c_fepxy2：orchestration·M-O1.3：resume 回灌（fold completed-未 notified 子）+ notified 写面
- c_bkdnej：deepseek-app-api·F1：pkg/deepseek files 客户端（UploadFile multipart + file 分块 wire 形态）
- c_k4iejt：deepseek-app-api·S1：pkg/deepseek TTS 客户端 + pricing.TTSCost
- c_sv3zwn：v5 收伞评估：M11.5+ 候选梳理（run_in_background×worktree 解禁、巡检 cron 化等）
- c_hkqvwk：edit-read-before 结构性方案评估（低优先，产出三选一决策）
- c_np5gzu：inspect-rpc IDE 线：--rpc stdio transport + Registry 重构（做前先答是否还需要）
- c_z4mn6y：slim-loop·L3 首跑（M-SL3）：goal run 夜间自瘦身 × worktree × 人审 diff
- c_azqudp：orchestration·M-O2.3：agentsmgr 写路径 + seek agents list/check CLI
- c_5xtm74：web 前端聊天增量：流式气泡 delta 累积→reply 整泡替换 + 工具行折叠（W2.1，textContent 纪律保持；无头验收脚本 web-stream-manual.sh + CDP 驱动）
- c_oixx62：slim-loop·M-SL3 首跑重跑：seek goal run 载体 × worktree × 人审 diff
- c_2i6sm6：v8·S1：摩擦记账——从 transcript 折叠出按工具的失败/被拒/重复统计（只读）
- c_zcygo2：v8·U1：内核依赖方向报告脚本（只报不拦）
- c_5fdsqb：memory↔skill 打通·设计卡 P0：蒸馏落点（review 第四动作）+ 生命周期对齐
- c_miutmh：v8·T1：顶层 -tools 白名单 flag（裸核对照用，会话启动时冻结）
- c_qur4ib：部署位 A：seek 建 deploy/local-manifest.json 纳入 deploy-local 自动同步
- c_vunyoz：v8·R1：autopilot Outcome 补证据字段（token/成本 + diffstat）并进 Report 文本
- c_lz5wxa：bridge·M-B4：seek skill stats --idle（Set 外连接）+ ⚡ 模态双存告警 + 测试
- c_icrm3y：bridge·M-B1：skillmgr.WriteProjectSkill 新入口 + 测试
- c_mnicm3：docs/prd 归档减法（v8 D5 拍板）：已交付 PRD 移入归档区，weight.sh 回预算
- c_aqbdaz：selftest 登记缺口机制化：新脚本自动进 scripts/selftest.sh（含补登 kernel-deps.sh）
- c_x3bed4：MCP-D2：sysprompt 新增 MCPSection 段（Header 第五段，SkillManifest 同款 per-session 静态；空段=现状逐字节不变）
- c_n63pat：web 前端·W4.4：资产面 sessions 浏览器（列表 + 消息 cursor 分页浏览）
- c_f6ayyg：语音 v2·V2：push-to-talk 入站——VoiceFrontend.Run 录音→ASR→Inbound
- c_m2whz2：编排 M-O3.2：pre_prompt_inject 授权注入通道 + 三区模型落地——opt-in shell stdout 经 Prepend 单缝注入（max_bytes 默认 2048/上限 8192 + 截断标记），deny 路径只断注入不断 turn，audit 记 injected_bytes，守护测试锁「注入后 prefix 前段字节不变」，状态栏字节数提示拍板不做
- c_4ejhhs：MCP-D1：MCP 工具默认 server__tool 前缀 + LoadServers 注册顺序确定化（bridge.go）
