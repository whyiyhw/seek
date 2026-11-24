# 更新日志（CHANGELOG）

## v0.12.3（2026-11-21）

v0.12.3 — **Zed 面成形**的一版：feature-acp 在册缺口全清（backlog ①–⑦ 与 P0-1…P0-4 全数收口），`seek acp` 车道从「能连上」走到「完整协作」——编辑器里带自己的 MCP 服务、开多个面板、贴图、看上下文余量、走权限审批、切 plan 模式，每轮怎么结束也有了一等公民的答案。同一版里：任务第一次活得比 seek 长（`seek task run` + `ps/attach/kill`）；web 面补齐远程审批、hooks 审计、markdown 渲染与会话整理；二线车道（OpenCode Go）的图片与 PDF 透传打通；eval v2 保护网铺完（E2–E6，含 release lane 与夜间入口）。跳过 0.12.2：main 在 v0.12.1 之后一直以 `v0.12.2-0.<时间戳>-<hash>` pseudo-version 构建，号码被开发段占用，直接进 0.12.3。

### Notable changes

- **MCP 透传 `mcpServers`（backlog ⑦，行为新增）**. `session/new` / `session/load` 里客户端声明的 `mcpServers` 以前被直接丢弃，现在按声明接通（仅 stdio，复用 `~/.seek/mcp.json` 那套客户端栈，无第二套实现）：工具注册进同一 registry，名字 `mcp__<server>__<tool>`（点号过不了工具名安全字母表，server 与工具段都折进安全字符）。起不来的 server 只写 stderr 不让 `session/new` 失败；生命周期跟随会话（结束即回收子进程）；不带声明时零变化。
- **多会话复用进程（backlog ③，行为新增）**. 一个 `seek acp` 进程服务 N 个会话：第二次 `session/new` 新开会话（新的持久 transcript id），不再重锚已有会话；per-session 独立四轴——cwd 锚、权限策略（A 面板的 always-allow 不会放进 B 面板的 bash）、历史栈、模型车道。跨会话回合串行不串扰；单会话 wire 行为逐字节不变。sysprompt/skills 等仍按启动目录进程级共享（ACP v1 无 session/delete）。
- **图片输入 + 版本协商（backlog ⑤，行为新增）**. `initialize` 的 `protocolVersion` 从回显改成真协商（客户端报 2 得到 1，降级即响应）；图片块（base64 + mimeType）按 wire 顺序落进 prompt——图文混排保持原位，不是追加到末尾。视觉模型原生附图、非视觉模型行内注明（visionRouter）。
- **上下文余量上报 `usage_update`（P0-4，行为新增）**. 每个完成的模型回合一帧 `usage_update`：协议顶层只有 `used`（末回合 prompt+completion）+ `size`（窗口上限），seek 的补充 `percent`/`severity` 放协议预留的 `_meta.seek.ctx`——与 TUI/Web 的余量条完全同口径。跨过阈值的那一帧额外多一帧告警（带 `/compact` 提示，每帧重复是噪声、只报「刚跨过」）；`SEEK_ACP_CTX_ALERT=0` 可关。
- **审批门 `session/request_permission`（backlog ①，行为新增）**. 非 yolo 的 ACP 车道权限姿态从 Deny 升 Ask：bash / cwd 外写经协议弹给客户端，三选项 allow_once / allow_always / reject_once；无应答默认拒（超时缺省 600s，`SEEK_ACP_PERMISSION_TIMEOUT` 可调），客户端断线不挂死回合。`SEEK_ACP_YOLO=1` 短路优先、行为与之前一致。
- **会话终止可观测（P0-3，行为新增）**. 每轮 `session/prompt` 在响应帧之前发一条结构化终止通知 `_seek_turn_stop`（五值枚举 end_turn / max_tokens / silent_timeout / cancelled / internal_error + detail）；响应的 `stopReason` 用满规范枚举（断流答 `max_tokens`、拒答答 `refusal`，不再藏进泛型 JSON-RPC 错误）；主循环与工具 panic 恢复成 `internal_error` 事件而不是无声杀死进程。帧每轮恰一条、永远先于响应——两者皆缺即进程本身死亡，客户端可直接归因。
- **`session/list` + `session/load`（P0-1，行为新增）**. `initialize` 申报 `sessionCapabilities.list`；`session/list` 返回历史会话清单（id/cwd/title/updatedAt，与 load 同一 store，标题取首条 user message）。`session/load` 从恒 false 变为真恢复：读回 JSONL、重锚 cwd、恢复车道、历史回放完才响应——ACP 会话现在每回合落盘。旧目录名 id 的会话无法恢复，返回说明性错误。
- **plan 模式协议化（backlog ④，行为新增）**. `/plan` 经 ACP 规范自己的 session-modes 语义暴露（非自创扩展）：`modes` 能力申报 + `session/set_mode` 切换（先发 `current_mode_update` 通知再回响应）；`plan` == 只读门，其余 slash 维持 TUI-only（选型理由在案）。
- **任务寿命脱离进程 + `seek ps/attach/kill`（v8·V1，行为新增）**. `seek task run "<cmd>"` 起一个活得比 seek 长的任务：wrapper `setsid` 自成会话，输出落 `~/.seek/tasks/<id>.log`，退出码由 wrapper 自己写回事实日志——回收不依赖发起进程活着。`ps` / `attach`（超时只停观察绝不杀）/ `kill`（SIGTERM→SIGKILL）每次读目录即退出：零常驻、零轮询线程、零新工具 schema。僵尸与 lost（OOM/外部 SIGKILL）如实呈现，三平台探测分文件实现。
- **web 远程审批卡片（W5，行为新增）**. 权限 y/N 的决定权送到浏览器：审批请求经 SSE 弹卡片（描述+命令摘要+y/N/a），决定走 `/rpc approval/decide` → 子进程 stdin（`seek -p --json --approval-stdin`）→ 被门禁的工具放行或拒绝。幂等结构性成立（每 id 恰一次，复读读回首决定）；`/rpc` 写型 method 补上 writeGate。
- **web 面一组**：会话侧栏常驻化（弹出退役、可收起、搜索/新建）；消息正文渲染 markdown 子集（标题/列表/链接，手写 DOM-API 渲染器——模型输出永不进 HTML 解析路径）；输入区多行 Enter/Shift+Enter + `/` 命令补全 + `@` 最近会话引用（配 `/resume <id>`）；hooks 实时审计视图（`/events?topic=hooks` verbatim 推流 + 面板）；会话重命名与归档；顶栏 ContextMeter 与生成中停止按钮；主题切换（亮暗+防 FOUC）；工具调用树；附件上传（拖放/粘贴）。
- **`auto_compact`（新功能，默认关）**. `~/.seek/config.json` 的 `auto_compact` 段或 `SEEK_AUTO_COMPACT`：`{"at_tokens": 350000}` 绝对数（优先）或 `{"at_fraction": 0.35}` 按当前模型窗口算。回合自然结束且真空闲时判一次；语义沿用 `/compact` 的 fork-snapshot（压缩前历史留在磁盘，`--resume` 找得回）；失败清 latch 一次不关功能。
- **二线车道的图片与文件透传（行为变更）**. 能力判定集中到 `internal/modelcap`（静态表 + 按网关声明的 `modalities.input` 装运行时 grant）——OpenCode Go 车道（deepseek-v4.1-flash）贴图不再被单 id 白名单误拒。PDF 三 adapter 各自编码（`file`/`document`/`inlineData` wire 均 PDF-only）；Word/PPT 提交期降级。纯文本 wire 字节不变。
- **R2 独立 verifier（v8，新包 `internal/verifier`）**. autopilot 任务完成后按需触发验证：确定性层（测试命令、deliverable diff、主树隔离检查）+ 全新上下文裁判会话对照 `done_when` 逐断言；机械不变量钳制裁判（测试红=fail、主树漂移=fail、无证据的 pass 降级 doubt），「子代理写进主树+测试全绿」的历史骗术骗不过它（已 fixture 化回归）。
- **eval v2 保护网（E2–E6）**. 多轮聚合（`--rounds`）与 `min_pass_rate` 标量界；通过率标定表；三条承重轴用例（plan-e2e 真 TUI 全链判分——首跑即抓出 resume 折叠真 bug；subagent-bg-resume；serve-web-stream 真 serve 子进程双断言）；`--lane release/core` 名单制 + ci.yml 夜间 core×2（streak≥3 才红，只观测不阻塞）；eval 车道缺省切 OpenCode Go 订阅（缺省就不烧计量 key，回计量必须显式 `SEEK_EVAL_PROVIDER=deepseek`）。
- **健壮性**. agent 循环「死刑」降级 ×2：length 截断 tool_calls 自愈、reasoning-only 空响应并入自愈（穷尽重试后合成占位消息续跑，连续 2 次才硬停）；`/compact` 溢出死锁救援（compact 自身装不进剩余预算时降级）；web SSE 重连清理；hooks 五观察点触发接线；no-remote 闸拒绝文案改说真话（点明车道与「用户在场时应由用户执行」）。
- **工具链与工程**. go1.27.3 单一源全线迁移；`cmd/seek/main.go` 继续按主题拆文件（planBridge、subagent glue、envgate）；pitfalls 台账一条一文件布局（255 live 全量分流 triage）；transcript-stats 离线分析器与历史效率基线。

> **前缀缓存**：本版工具 schema 与 ACP 能力申报字节有变，升级后的第一次会话会全量重算 prompt 前缀（一次），之后照常命中。Zed 侧建议重开 agent 面板让 `initialize` 重新协商。

更早的版本见 [GitHub Releases](https://github.com/whyiyhw/seek/releases)——每个 release body 与其 annotated tag 同文。本文件只保留当前版本，历史版本查 git 历史（`git show vX.Y.Z:CHANGELOG.md`）。

## 已进 main、未部署
- c_cv5dge：docs 账：budget.json 的 docs/prd/archive 线 653500→728500 重锚（actual 662240 +10%）——补 3d6f4895 归档 feature-deepseek-app-api.md（+46948B）时漏做的同 change 调线 + 活 PRD 在库加深（feature-acp 等）；摘要化否决（v0/v1 承载活引用）；上调已经人拍板（2026-11-22 凌晨，批），随合并生效
- c_lnsdvu：seek web：/bind 项目候选可点选（project/list ∪ serve.projects → 点行发 /bind <路径>，零新 method）
- c_k3pnlw：web 整备·markdown 渲染补齐：表格/引用/段落/代码块容器/悬挂缩进（W5 后返工）
- c_heffj3：seek web：/bind 目录浏览器（fs/list 只读 method，project_roots∪projects∪memory 根内可下钻；页面 /bind 参数期弹 📁 浏览目录，复用 #menu 弹层）
- c_xphkui：PRD §13 补「设计规范双 skill 必读」节：把 .seek/skills 两份规范接进 web 卡口径源（修 skill 发现断点）
- c_lqtt2m：web 整备·390px 手机宽响应式：单栏折叠/按钮组换行/ellipsis/设置面板堆叠
- c_mglkox：seek：v8 §9.1 同口径复测（R2 落地后窗口）——决定 R3 与第三波止损读数
- c_vcnacz：web-page-review.sh：页面无头截图审查入口沉淀（沙盒 serve+夹具+两主题×桌面/390px 截图矩阵）
- c_ufw6jv：sysprompt 身份块事实修正与精简：~5 MB 过期、provider 缺 glm、9 行并 5 行
- c_25d2ey：web 整备·图标与按钮体系统一：残形图标/emoji 混排/4-5 套按钮风格收敛（对齐 dsh 视觉语言）
- c_t7vdgs：seek 仓 .butler/ 声明退役出版控：git rm --cached + .gitignore，闸声明转为机器本地文件
- c_nkkyqf：seek web 审查补视觉计数 + 审查口径换成可检查条目（抄 craft 三份，标豁免项）
- c_5qrafk：信号：web-gate 验证车道声明错配两例（c_25d2ey 跳过补证、c_lbys7d 存疑）——selftest 登记或统一 not-in-lane 口径
- c_lbys7d：seek web 暗色三处次级文字压在 accent 底 3.59:1 + 归档行 opacity .55 掉到 2.29:1

## 未发版（在途分支）
- c_qjaksw：**subtraction**——退役 fim_complete 工具链。删 `internal/tools/fimcomplete/`、`pkg/deepseek` FIM client（fim.go / fim_test.go / endpointFIM）、sysprompt 工具文档行、D8 deepseekOnly 表项（think 保留独扛过滤测试）、benchmark FIM 指标（FIMMinCalls 与 fim 比率；`fim-patch` 任务保留、prompt 去 fim_complete 指引）、i18n provider banner 的 FIM 字样；文档同步（dual-model / comparison / guide-architecture / book ch5+ch14 / pitfalls-triage #629 标注已退役 / feature-orchestration D8 / examples 两页 / 双 README）。依据：实测 FIM 调用比率 0%/11%（`docs/pitfalls/deepseek-api/fim-usage-ratio-is-inherently-low-—-model-prefers-edit-write-over-fim-complete.md`）——模型不用，快路径名存实亡，删净比养着便宜。
- c_cs4dw6：**subtraction**——退役 `think` 工具与 `dual-model` 内置 skill（继 fim_complete 之后第二刀）。删 `internal/tools/think/`、sysprompt 两条强引导（工具文档行 + 工作流第 3 条）、D8 deepseekOnly 表与 provider 过滤链整条（表空即死代码，IsDeepSeekOnlyTool/滤网/测试连带删）、`builtin/dual-model.md`（think→execute→think(reflect) 闭环失去核心）、内置 agent 模板（explore/plan）白名单 think 项、i18n provider banner 的 Reasoner 字样；ACP 分类映射只删 "think" 键、类别字符串 "think"（plan/propose 映射）是协议概念保留；TUI completionRe 注释如实标注为退役工具遗产（解析路径通用，留观察）。依据与 fim 同源口径：109 个真实项目会话仅 2 次调用，sysprompt 强引导几乎不被使用——深度推理由主会话 thinking 模式（/effort）承载更直接。文档同步：双 README / comparison / guide-skills / guide-architecture / book ch3+ch5+ch6+ch11+ch14（退役注记，历史叙事保留）/ feature-orchestration D8 段 / examples 两页。**复验补刀（2026-11-22 夜）**：`think_calls` 指标从 `eval/run.sh` 词表删除（两处字节锚自测 norm 同步剥键——删键与加键同一纪律），`tool-args-hallucination` 用例改用多工具审计任务重写（原对象 think 消失，绑 `min_total_tool_calls: 5` 防空过）、`redundant-tool-calls` 的 think 断言与文档清理、`code-review-effort-low` 的 `/effort/think` 提法更正、`docs/guide-subagent.md` explore 工具清单与 `examples/docs/skills.html` 内置技能表（四个现役内置）同步、`docs/prd/feature-plan-mode.md` 五处 think/dual-model 退役注记（含流程图后一段总注记）。复验发现 think_calls 的 eval 残留确有阈值依赖（ablation 2026-11-18 实测 min=1 got=0 判红），一并修净。
- c_6xktw4：web 对齐 dsh·统计趋势（stats/trend 聚合 + 手绘 SVG 折线/悬停/窗口切换 + 侧栏 per-session cost 列）
- c_lnsdvu：web 端 /bind 项目候选点选（行为变更）。顶栏 `/bind` 与 `/projects` 两钮从「预填 `/bind ` 等手补路径 / 直发 `/projects` 文本回复」改为开项目点选弹层——复用 `/`、`@` 同一个 #menu 候选弹层与同一条 pick 路径：候选 = `/rpc project/list` 的 root ∪ `/rpc settings/get` 的 projects（config `serve.projects` 配了但 seek 尚未打开过的目录），clean 后绝对路径去重；行显 basename（同名补父目录消歧）+ 绝对路径 + N 个会话/未开过，当前绑定行标「← 当前」；点行/Enter = 发送同款 `/bind <root>` 指令文本（已绑定态也合法且总是重置会话，统一走 /bind 不引 /switch 分叉），弹层头行固定告知「点选会重置当前会话（/bind 语义）；路径也可手打」；空态/取数失败各一行提示不静默。**零新 RPC method、零写面**；`/projects` 聊天文本回复字节不变（手打仍可看）；点选不预检目录存在性——已移动/删除的 root 点了拿 Router「绑定失败：目录不存在」应答如实透传。页面源 +6.2KB；验收 = 页面钉子显式改写 + 沙盒 E2E 双脚本（`scripts/web-bindpick-manual.sh` + `-headless.mjs`，curl 字节钉 + 无头七景）全绿。fs/list 类任意目录下钻不做（新只读 method 另开卡过 inspect-rpc 纪律）。
- c_heffj3：web `/bind` 目录浏览器——新增只读 method `fs/list`（registry 族纪律：params `{path?, show_hidden?, limit?}`，path 缺省=首个可浏览根；名字+is_dir only、depth 1、limit 200/1000+truncated、名字稳定序）+ 页面目录模式（`/bind ` 参数期弹「📁 浏览目录…」，复用 #menu 弹层与 ↑↓/Enter/Esc/Tab 导航、层头=路径+上一级+选这个目录+过滤框，零新增全局键盘监听；失败层内一行提示）。安全面=根集收敛：`serve.project_roots`（新可选配置，`~/.seek/config.json`）∪ `serve.projects` ∪ memory 项目根，空则并入 HOME；双侧 EvalSymlinks + 分隔符前缀、先判根集再 stat（根外无存在性预言），根外 -32602 带逃生口文案——/bind 本就能校验任意路径，本口只新增枚举信息面故收紧；已有项目根始终可达，/srv 部署加一行配置即扩。stdio `--rpc` 三 method 仍冻结。验收：fs_test.go 表驱动 + 页面钉子 + `scripts/web-dirpick-manual.sh`/`-headless.mjs` 沙盒 E2E（curl 契约 + 无头七景）全绿；净增 +38.0KB（记账见 feature-web-frontend §13.3，超卡面预估按先例超在记账不在 gate；web-gate 的 trip 点是 main 既有欠棘轮 +225KB，非本卡）。
