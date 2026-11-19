# dsh web 客户端交互语言规格（seek web 前端重设计 brief）

> 状态：波次第 1 张（c_wwpdxp）产出，只读解剖，未改任何 seek 代码。
> 解剖对象：`~/code/github/deepseek-harness` clone @ `0a53fb5`（卡片写 47f9438，clone 实际在此提交之后的 release 合并点，UI 包结构一致；下文 dsh 引用一律相对 clone 根）。
> 姊妹篇：[`docs/dsh-analysis.md`](dsh-analysis.md)（架构/事件流/借鉴清单；两赛道论见其 §8.0，本文不重复）。
> 引用纪律同 dsh-analysis.md：结论一律 `路径:行号`（dsh 侧相对 clone 根，seek 侧相对本仓根），抽查过 6 处子代理引用均命中。

---

## a) 布局骨架

### 三栏 frame 与让位链

整壳是一个 CSS grid 三栏：`gridTemplateColumns: ${sidebar}px minmax(0,1fr) ${details}px`（`packages/client/ui-layout/src/client/AppFrame.tsx:179`）。全部几何常量冻结在 solver 里（`packages/client/ui-layout/src/client/columns.ts:21-39`）：

| 常量 | 值 | 含义 |
|---|---|---|
| `CENTER_MIN` | 640 | 中栏下限，仅最终回退可破 |
| `SIDEBAR_MIN/MAX/DEFAULT` | 264/420/280 | 侧栏拖拽区间与初始宽 |
| `SIDEBAR_COLLAPSED` | 56 | 折叠轨 = 16px 内边距夹 24px 图标列 |
| `SIDEBAR_AUTO_COLLAPSE` | 1024 | 视口窄于此自动折叠（deepsuite LG 断点） |
| `DETAILS_MIN/MAX/DEFAULT` | 300/520/360 | 右栏拖拽区间与初始宽 |

空间不够时的**让位链**是纯函数、无迟滞（`columns.ts:62-77`）：先压 details 到下限 → 仍不够则 details **派生归零**（用户偏好值不回写，窗口再变宽自动恢复）→ 中栏吸收剩余赤字。侧栏永不让位；窄视口下手动重展开会反过来挤压中栏（`AppFrame.tsx:146-151`）。details 归零时**子树保持挂载**（width 0 不卸载，`AppFrame.tsx:35-38`；边框接缝由 `[data-details-collapsed]` 摘掉，`AppFrame.module.css:47-49`）——右栏状态（滚动位置/展开态）开关不丢。

拖拽把手：8px 命中条骑在列边框上（绝对定位、pointer capture、rAF 节流，`AppFrame.tsx:44-88`）；details 侧悬停浮现 12×32 圆角 pill 作为视觉把手，侧栏只有命中条（`AppFrame.module.css:78-108`）。拖拽期间列宽过渡动画整体暂停（`[data-dragging]` 摘 transition，否则 eased 轨道会脱离指针，`AppFrame.module.css:15-17`）。

列宽动画：`grid-template-columns 0.3s cubic-bezier(0.4,0,0.2,1)`（`AppFrame.module.css:10`），全站 `prefers-reduced-motion` 兜底关动画。

### 侧栏结构（上→下）

`packages/client/ui-sidebar/src/client/SidebarRoot.tsx:140-219`：

```
┌─────────────────────────────┐
│ [鲸鱼logo+产品名]  [折叠钮] │  logo 区；展开态 logo 本身可点=新会话
│ [+ 新会话]                  │  展开态带文字标签；轨态只剩图标
│ ┌─────────────────────────┐ │
│ │ sidebar.workspaces slot │ │  会话/工作区浏览区（撑满剩余高度）
│ │ （ui-workspace 会话列表）│ │  按 workspace 分组或扁平，可搜索
│ └─────────────────────────┘ │
│ [footer.actions 槽]         │
│ [⚙ 设置]                    │  底部固定
└─────────────────────────────┘
```

折叠是**滑动+交叉淡出**：内容冻结在展开宽度（inline width）原地淡出，由滑动的列裁剪，中途不回流；150ms 落定后宽内容卸载、四个上部控件以同序进 56px 轨（`SidebarRoot.tsx:27,60-79`）。轨图标 18px、展开 16px（`SidebarRoot.tsx:184`）。折叠轨的 logo 悬停换成「展开面板」图标（`SidebarRoot.tsx:169-184`）。

侧栏内滚动条**跟指针走**：指针在列内才画，离开后保留 2000ms 再隐（防穿越边缘闪烁；用列的 BOX 判定离开而非 DOM 事件，`SidebarRoot.tsx:31-35,85-122`）。

### 中栏分区（会话区）

`packages/client/ui-conversation` 的骨架：会话头（标题 + 动作位：schedule 弹层 order 10、jobs 弹层 order 20、View tab>1 才渲染，`ui-conversation/src/client/skeleton/ConversationSession.tsx:138,145-160`）→ 转录区（最大宽度可拖拽调、localStorage 持久化，`ConversationRoot.tsx:17-129`）→ composer 链（模型座位、plan chip、dock 条：goal order 10、stats）→ 右缘 TurnNavigator 竖轨。子代理以面包屑 lineage 呈现，不是分支树（`ConversationSession.tsx:34-52`）。

### ASCII 总图

```
┌──────────┬────────────────────────────────────────────┬───────────┐
│ sidebar  │                  center                    │  details  │
│ 264-420  │                minmax(0,1fr)               │ 300-520   │
│ (轨 56)  │           CENTER_MIN 640 保底              │ (关=0宽   │
│          │                                            │  不卸载)  │
│ logo+折钮│ ┌────────────────────────────────────────┐ │ ┌───────┐ │
│ 新会话    │ │ 会话头: 标题|⏰提醒|⌗jobs|view tabs    │ │ │选中工具│ │
│ ┌──────┐ │ ├────────────────────────────────────────┤ │ │完整 I/O│ │
│ │work- │ │ │ 转录流(宽度可拖):                       │ │ │JSON/终 │ │
│ │spaces│ │ │  user 气泡 → turn-process 折叠控制      │ │ │端/差异 │ │
│ │会话列 │ │ │  → [Think折叠行][工具卡折叠行]交错      │ │ └───────┘ │
│ │表+搜  │ │ │  → assistant 文本(流式 md)             │ │           │
│ │ 索    │ │ │  → turn-tail: 操作+token/耗时 pill     │ │           │
│ └──────┘ │ │        ▗TurnNavigator 右缘竖轨▖        │ │           │
│ footer   │ ├────────────────────────────────────────┤ │           │
│ ⚙设置    │ │ composer 链: [审批卡覆盖层(若有)]       │ │           │
│          │ │ [plan✕chip][模型·推理][goal条][stats]   │ │           │
│          │ │ [输入框            发送/停止]           │ │           │
└──────────┴─┴────────────────────────────────────────┴─┴───────────┘
     ↑ 8px 拖拽条骑列边；details 侧悬停现 12×32 pill；overlay 层 z-20 罩全帧
```

### 信息密度要点

- 正文 14px/行高 24（默认设置，见 b 节），工具行/思考行标题一律低一档 13px；转录默认 **compact 模式**（完成的 turn 过程内容整体收进折叠控制行，`packages/client/ui-chat/src/chat-settings.ts:18`）。
- 密度调节手段只有一个：正文字号 12–17px 整数步进（`packages/client/ui-theme/src/theme-settings.ts:24-30`），全部层级随动（b 节公式）。
- 空态策略：面板级统一「渲染 null」而非空态文案（jobs/schedule/goal/plan 四包一致，见 c 节）；唯一大空态是会话 Hero（标题+鲸鱼 SMIL 动画，`ui-conversation/src/client/skeleton/EmptyHero.tsx:100-155`）。

---

## b) 视觉 token 表

三层体系（`packages/client/ui-theme/src/styles/`）：**静态色板**（`design-platform.css:5-154`，`--dsw-static-*`）→ **语义别名**（同文件 :156-338，`--dsw-alias-*`/`--dsw-specific-*`）→ 组件 CSS 只消费别名。组件 CSS modules 里没有任何裸色值（抽查全部 primitives/chat/sidebar/layout 无一例外）。

### 色板（实际值摘录）

静态层两主题**完全同值**（唯一例外 neutral-bluish-60：亮 245,246,247 / 暗 249,250,251，`design-platform.css:63,139`）；换肤全靠别名层翻转。中性灰用带蓝的 neutral-bluish 阶：

| 阶位 | 值 |
|---|---|
| neutral-bluish-00 / 50 / 100 / 200 | rgb(255,255,255) / rgb(249,250,251) / rgb(235,238,242) / rgb(225,229,238) |
| neutral-bluish-500 / 700 / 800 / 850 | rgb(151,157,166) / rgb(97,102,107) / rgb(53,54,56) / rgb(44,44,46) |
| neutral-bluish-875 / 900 / 950 | rgb(35,35,36) / rgb(27,27,28) / rgb(21,21,23) |
| deepseek-400 / 450 / 500（品牌蓝） | rgb(103,158,254) / rgb(86,134,254) / rgb(65,118,230) |
| red-400/600、green-400/500、amber-400/500/600 | rgb(242,90,90)/rgb(236,19,19)；rgb(78,209,126)/rgb(34,197,94)；rgb(247,173,49)/rgb(245,158,11)/rgb(221,134,41) |

### 语义别名（亮 / 暗，摘录关键行）

| 用途 | 亮 | 暗 |
|---|---|---|
| bg-base | neutral-bluish-00（白） | neutral-bluish-950 `rgb(21,21,23)` |
| bg-layer-1/2/3 | 全 00 | 875 / 850 / 800 |
| border-l1/l2/l3/l4 | 黑 α .04/.10/.12/.16 | 白 α .06/.12/.16/.20 |
| label-primary / secondary / tertiary / caption | bluish-1000（近黑）/ 700 / 600 / 400 | bluish-50 / 300 / 400 / 600 |
| brand-primary（主按钮底） | **bluish-1000 近黑** | **bluish-50 近白** |
| button-info-fill / hover | deepseek-500 / 400 | deepseek-400 / 500 |
| interactive-bg-hover / active | 黑 α .06 / .10 | 白 α .08 / .14 |
| state-business（品牌态） | deepseek-500 | deepseek-400 |
| state-error / success / warn primary | red-600 / green-500 / amber-500 | red-400 / green-500 / amber-500 |
| sidebar-fill / nav-active / nav-hover | bluish-50 / bluish-100+deepseek-100 accent / bluish-75 | bluish-900 / bluish-750+800 accent / bluish-850 |
| toast-bg / tooltip-bg | bluish-800 / 850 | bluish-750 / 750 |
| markdown-code-block / inline-code | bluish-50 / bluish-100 | bluish-900 / bluish-850 |

出处：亮 `design-platform.css:156-246`，暗 `:248-338`。

**关键设计决策**：主行动按钮是单色（亮=黑底白字，暗=白底黑字），品牌蓝只用于「业务态」（链接、选中、info 按钮、徽标），不用作主按钮——这使彩色只出现在状态语义上（`--dsw-alias-brand-primary` 亮暗值见 :179/:271）。

### 字号阶梯

- **正文字号设置**：12–17px 整数、默认 14（`theme-settings.ts:24-30`），bootstrap 写在 body 上（`ui-theme/src/boot-theme.ts:21`）。
- **派生公式**（`gradient-shadow-text.css:32-34`）：`--dsh-content-font-delta = 设置值−14`；次级 `= min(设置−1, max(13, 设置−2))`（14→13、17→15、12→11）。
- **Markdown 阶梯**（同文件 :36-154，Figma 0.875 等比导出后手工补偿）：h1 21/700/行高30、h2 19/700/28、h3 18/700/26、h4 14/600/24、正文 14/400/24、表格 13、small 与 code 固定 12（code 12/19 mono、code-block 11/19 mono、code-block-small 11/16）。全部标题随 `font-delta` 平移。
- **UI 阶梯**（:156-245）：24/20/16/14/13/12/11，行高 32/28/28/22-24/20/18/14；强调一律 500，标题 600，h1-h3 700。注：Figma 的 510 权重全部落 `font-weight:500`（可变字重跨平台不可靠，`design-platform.css:1-3`）。
- 字族：系统栈（PingFang SC / Microsoft YaHei 全覆盖）+ 独立 code 栈（SF Mono→JetBrains→Consolas，**刻意不带裸 `monospace` 尾**——Windows CJK 会掉进 SimSun，`base.css:6-10`）。

### 圆角 / 阴影 / 动效

- 圆角频次（全 CSS modules 直方图）：**8px（卡片/气泡）> 12px（弹层/菜单）> 6px（行内块）**；另有 4/10/14、胶囊 999 或 22-28、圆点 50%。工具块原语用独立变量（`--dsl-terminal-radius` 等）。
- 阴影三档（`gradient-shadow-text.css:5-9`）：lv1 `0 2px 4px rgba(0,0,0,.05)`；lv2 叠两层软影；lv3 `0 0 1px rgba(0,0,0,.2) + 0 12px 32px rgba(0,0,0,.08)`（弹层级）。阴影**不随主题翻转变色**（低透明度黑对深底同样成立）。
- 动效三档（`base.css:11-14`）：fast 0.1s / 默认 0.2s / slow 0.3s，统一 `cubic-bezier(0.4,0,0.2,1)`；全部消费点带 `prefers-reduced-motion` 关停。
- 间距无全局 token——各组件 CSS module 内联（4/6/8/12/16/24 的 4px 栅格惯例），骨架级间距由列宽常量与 sidebar 内边距（轨 16px）锚定。

### 暗色策略

单开关 `body[data-ds-dark-theme]` 翻转整个别名层；静态层不动。边框从「黑 α」换成「白 α」，hover/active 同理从黑 α 换白 α。设置页 AppearanceRow 持久化偏好（`ui-theme/src/client/AppearanceRow.tsx`）。**对 seek 的启示**：token 分层让换肤=改一个 attr，组件零改动——纯 CSS variables + data 属性即可复刻，无需任何运行时。

---

## c) 交互模式清单

### 通用律（四包+审批+技能解剖的共性）

1. **空态渲染 null**：无数据时控件整体不出现（jobs/schedule/goal/plan 全部如此）——「不用则不长控件」，侧栏/头部不摆死位。
2. **错误一律内联**：`role="alert"/"status"` 短句 + title 悬浮原文，绝无全局 Toast 跑腿（唯一例外：模型**选择被拒**走 transient Toast，因为它发生在菜单即将关闭的瞬间，`ui-model-selection/src/client/ModelSelect.tsx:54-59`）。
3. **pending 语义本地禁用**：动作发出即禁用按钮，服务端快照回来才解——零乐观 UI；需要防冲突的写操作带 CAS revision（goal 四动词，`ui-goal/src/client/index.ts:69-106`）。
4. **弹层三件套**：外点关闭（`useDismissOnOutsidePointer`）+ Escape 关闭**并把焦点还给 trigger**（jobs `ui-jobs/src/client/JobListAction.tsx:127-132`）+ 不可见即收起。
5. **进行中视觉=StateDot 像素追逐**（蓝 8 格 1s 阶梯 keyframe，`ui-primitives/src/StateDot.module.css:57-69`）或行级 sweep 扫光；完成=绿/警告=琥珀/错误=红静态点。

### 各面板速查

| 面板 | 入口 | 数据 | 状态机 | 动作 |
|---|---|---|---|---|
| **plan chip** | composer 内嵌 chip，slot `conversation.input.plan`（`ui-plan/src/client/index.ts:55`） | 投影 `{active,pending}`；退出走 `commands.execute('/plan off')`（:61） | 未开启/加载均渲染 null（`PlanModeControl.tsx:32-34`）；错误内联 | 唯一按钮「Plan ✕」；会话占用时禁用 |
| **goal 条** | 输入区 dock `conversation.input.dock` id=goal order 10（`ui-goal/src/client/index.ts:81-86`）+ chat 内 `/goal` 右对齐气泡（`goal-command-input.ts:36-71`） | 投影 goal{id,objective,phase,revision}；edit/pause/resume/clear 全带 CAS（index.ts:69-106） | 无目标→null；blocked 时 phase 徽标+title 提示 blockedReason（`GoalBar.tsx:126`） | pause/resume 二选一；edit 内联（Enter 存/Esc 弃）；clear 无确认弹窗（CAS 兜底） |
| **jobs 弹层** | 会话头动作区 slot order 20（`ui-jobs/src/client/index.ts:32-41`） | 纯 store 镜像（control frame `type:'jobs'` 增量），零轮询 | 无 job→整个控件 null；live 前、settle 后排序（`JobListAction.tsx:77-85`） | 只读；开列表起 1s tick 实时时长；行=StateDot+kind 徽标+label+时长 |
| **schedule 弹层** | 同头位 order 10（`ui-schedule/src/client/index.ts:25-34`） | 投影只读，零 RPC | 空列表→null；**逾期优先**排序，逾期行 warn-tertiary 底色（CSS :78-80） | 只读；频率/本地时间/相对时间三段元数据，纯函数格式化可复用（:42-81） |
| **会话列表** | sidebar `sidebar.workspaces`（`ui-workspace/src/client/index.ts:138`） | 本地过滤+300ms 防抖远程 `session.search` 合并（`WorkspaceBrowser.tsx:36-59,927-962`） | 搜索态四分支文案 `search.pending/unavailable/noMatches/hasMore`（:775-790）；空态 `empty.none` | 行菜单 Rename/Fork/Archive；当前会话 `aria-selected`+高亮；**blank 会话不带时间戳与动作**（「首 prompt 才赋资格」，`rows/Rows.tsx:457-461`） |
| **审批卡** | SSE `approval/request` → pending interaction（precedence 仲裁）→ 注入 composer 链（`ui-approval/src/client/index.ts:80-92`） | SSE 捕获 host waterfall；决策 RPC | **不插队进 chat 流**，而是**覆盖 composer 座位**（overlay 兄弟节点，输入条 DOM 驻留不重挂，`ui-conversation/.../ConversationRoot.tsx:361-375`）；会话列表同步显黄点 | 仅两钮：拒绝(outline)+允许一次(primary)（`ApprovalPanel.tsx:44-51`，已抽查）；**无「总是允许」、无 y/n 快捷键**；点击后双钮禁用，RPC 失败回滚可重点 |
| **技能** | 输入框 `/` 触发源 + 工具卡 key=skill（`ui-skill/src/client/index.ts:69-72,137-189`） | RPC `skills.list` 按会话缓存单飞 | running/ok/error(stopped)；无空态 | 选中落字面量 `/name `；行展开显 Instructions pre + inspect 按钮 |
| **模型选择** | 命令面板 `/model` + composer 座位（`ui-model-selection/src/client/index.ts:126-179`） | RPC `session.modelCatalog`/`session.selectModel`；SSE adapters-updated 等三源失效 | status idle/loading/ready/selecting/error；三层 pane 菜单，Escape 先退层再关（`ModelSelect.tsx:139-152`） | `menuitemradio`+check；加载错误留菜单内 Retry，选择被拒走 Toast（:54-59） |

### chat 流式渲染（重点）

**turn 节点序**（事件投影，`ui-chat/src/client/conversation-nodes/` 各文件）：user 气泡 → turn-process 折叠控制行（「N 次工具调用 · M 条消息 · K 个 subagent」或「思考了一会儿」，`TurnProcessNodeView.tsx:14-40`）→ assistant-step（reasoning/text 每 step 一节点）与 tool-call 按事件 seq 交错 → turn-tail（操作行+统计 pill）。排序按 anchorSeq+rank（`chat-snapshot-builder.ts:240-291`）。运行中尾部 TurnStatus「深度思考中」，≥15s 加计时钟（`ChatView.tsx:165-198`）。

**事件→节点对照**（`event-projection.ts` 及各节点文件）：user/message→user/steering/context；step/start+assistant/chunk→assistant-step；tool/call+tool/result→tool-call；turn/start+turn/end→turn-tail；turn/end reason=error/max-tokens→turn-error/turn-max-tokens；command/run→command；compaction/*→compaction；llm/retry→model-retry；agent/inbox/spliced→inbox；未知→unknown 兜底节点（**每个事件都有落点，永不丢行**，`fallback.ts:18-25`）。

**工具调用卡片**（`ui-tool`）：

- 默认**折叠**（`ToolRow.tsx:111`，已抽查）；每个工具名一个 keyed toolview（bash/edit/write/read/grep/glob/todo_write/web_search/web_fetch/ask_user_question），未注册走 GenericToolCard（`ui-tool/src/client/apply.ts:33-41`、`ToolCallTree.tsx:40-43`）；子调用递归嵌套、深度上限 256（`conversation-nodes/tool.ts:18`）。
- 折叠标题行 = 状态图标（error 红 StateDot / stopped 琥珀 / 其余工具变体图标）+ 本地化标题 + `·` + **参数摘要一行**（bash=command、read=path、search=query……变体选键见 `tool-call-model.ts:146-183`）+ 后缀（diff 类显示 `+N -M`，`ToolRow.tsx:136-141`）。文件路径是可点链接；悬停图标位浮现 chevron（`DisclosureRow.tsx:60-70`）。
- 展开后：输入=格式化 args（code 变体用 CodeBlock）；输出=按类型结构化卡——TerminalBlock / DiffBlock / ReadBlock / SearchBlock（附 recovery 行）/ WebBlock / AskQuestionCard（`ToolRow.tsx:202-265`）；Inspect 按钮跳 trajectory。
- **超长输出 head-tail 截断**：chat 内 cap=8 行（head=ceil(8/2)+tail），中段折叠成「…N more」展开钮（`ui-primitives/src/head-tail-cap.ts:23-27`；原语默认 16）。文本 IN/OUT 不截断。各卡带 copy。
- **运行中反馈**：行级 2.6s sweep 扫光 + 视觉隐藏 "running" 文案（`ToolRow.tsx:80-87`）；TerminalBlock 首行 StateDot 追逐动画。

**markdown 流式**：streaming 态用增量解析器——冻结除末 2 块外的所有顶层块、只重解析尾部，块 key=源偏移保证 DOM 复用（`ui-primitives/src/markdown/incremental.ts:34,92-129`）；settled 全量重解析（补 math/脚注等）；中断加「已停止」标记（`AssistantMarkdown.tsx:114`）。chunk 按 animation-frame 节流发布（`conversation-nodes/assistant.ts:410-416`）。**这一套是「流式不闪烁」的核心**，纯算法可移植。

**ReasoningRow**：默认折叠，标题「Think」；折叠摘要 settled 显首行 / running 显最新一行并节流滚到末尾（`ReasoningRow.tsx:14-38`）。

**统计**：composer dock 的 StatsLine（轮数/步数 | LLM·工具耗时 | tok/s·TTFT | 缓存命中%·tokens，溢出省略+悬浮全量，`StatsLine.tsx:135-185`）；每轮 turn-tail 的 token pill + 时钟 pill，点开对话框显示路由/cache 读写/输出明细（`TurnUsagePanel.tsx:99-235`）。

**滚动**：跟随阈值 24px（`ChatView.tsx:17`，已抽查）；仅 pinned 时跟随流内容；新 user 提交强制回底；上滚出回底 chevron；加载更早历史用锚行保视口；滚动位置跨挂载保存/恢复；ResizeObserver 盯列高变化续跟（`ChatView.tsx:384-546`）。

**TurnNavigator**：右缘竖轨，每 turn 一个 mark，≥2 turn 才显示；点击滚动锚定；hover 预览 prompt/响应摘要（各 160 字符）；活动 mark 由阅读线 hit-test 决定（`TurnNavigator.tsx:59`、`ChatView.tsx:47-63`）。

---

## d) seek 映射表

seek web 现状：单文件手写 HTML（内联 CSS/JS、零构建链），`internal/controlplane/webfrontend.html`（1267 行）经 `//go:embed` 进单二进制；页面=聊天主流+四个互斥面板（📚 会话 W4.4 / 🧠 记忆 W4.5 / 📊 统计 W4.5 / 🤖 后台任务 W3.3，`webfrontend.html:925-941`，已抽查）+ 🎤 按住说话（V2）。

### 面板映射

| dsh 面板 | seek 对应 | 现状 | 数据面差距 |
|---|---|---|---|
| 三栏 AppFrame + 折叠轨 | 无（单栏页面） | **需重写壳** | 纯前端，无差距；让位链/轨宽常量可直接照抄（a 节） |
| 会话列表（ui-workspace） | W4.4 sessions 面板（已交付） | 有列表，无状态点/分组/Fork | RPC `session/list` 已有 title/turns/tool_calls/model（`internal/rpc/methods/session.go:92-100`）；**缺**：会话运行态（点色）、pending 审批标志、服务端搜索（现本地过滤）、workspace 分组概念 |
| 转录流（ui-chat） | 聊天气泡流 W2.1 | 有流式气泡+usage 状态栏 | SSE 有 delta/tool/usage（`webfrontend_stream.go:153-173`）；**缺**：reasoning（显式过滤，`webfrontend_stream.go:31-34`，已抽查——策略决定不推，对齐需重新决策）、tool args/results 载荷（现只推 name+phase）、turn 边界事件、工具卡结构化渲染（前端全缺） |
| 工具调用卡片（ui-tool） | 无 | **全新** | 见上；`tool` 事件需扩 args 摘要与 result 头尾截断（截断可先做前端，SSE 不推全文与 §2.5 过滤纪律一致） |
| 审批卡（ui-approval） | **无对应物**（web 子进程 PrefDeny 自动拒：`cmd/seek/main.go:1098-1101` 的 `initialPref` 仅在 `--yolo`/ACP yolo 时翻 `PrefYolo`，而 `internal/controlplane/executor.go:164,309` 显示子进程仅在 binding Yolo 时才追加 `--yolo`） | **空白=最大差距** | 需：serve 侧 askFn 桥到 SSE `approval/request` + 决策回注 + 前端 composer 覆盖卡；TUI 的 y/N 语义已有（`internal/tui/messages.go:12-17` 可参照措辞） |
| plan chip | 无（plan-mode 仅 TUI，状态栏 PLAN badge） | 规划中 | 需 plan 状态事件（propose/plan 工具调用已在转录事件里，可投影）+ `/plan` 命令通道 |
| goal 条 | 无 goal 概念 | — | 跳过或远期（seek 无 goals 域） |
| jobs 弹层 | 🤖 后台任务面板 W3.3（subagents topic） | 有（整页切换） | 数据已有（`webfrontend_topic.go` subagents 流）；对齐=改成会话头弹层+StateDot 语言 |
| schedule 弹层 | seek cron 有 CLI（`docs/guide-cron.md`）无 RPC | 规划（M12 cron method） | 缺 `cron/list` 数据面；逾期着色/三段时间格式可直接抄 c 节纯函数 |
| 技能面板 | seek skill 子系统 CLI/TUI 完备，web 无 | 规划 | 缺 `skill/list` RPC（loader 在 `internal/skill`，只读暴露成本低） |
| 模型选择器 | 无（serve 会话模型固定） | 规划 | 缺 model catalog 数据面；seek 两层 provider（deepseek 一等+llm 通用）天然映射「模型·推理等级」两段式 |
| TurnNavigator/DetailsPanel | 无 | 远期 | 依赖转录锚点化（先做 turn 边界事件） |
| 记忆浏览器 | 🧠 记忆 W4.5 | 有 | `memory/list/get/projects` 已齐（`internal/rpc/methods/memory.go`），无差距 |
| 统计 | 📊 统计 W4.5 | 有 | `stats/cache/friction` 已齐 |
| 设置（外观/字号） | 无 | 规划 | 前端 localStorage 即可起步；token 体系见 b 节 |

### 数据面盘点结论

- **已有可用**：SSE `delta/tool/usage` + subagents topic + voice；RPC session/list/get/messages、memory/*、project/*、stats/*、hooks/*。
- **对齐 dsh 交互语言所缺**（按优先级）：① 审批请求/决策双通道（阻塞语义=serve 会话挂起等 UI）；② 工具事件带摘要级 args/result（不是全文）；③ turn 生命周期事件（start/end+error reason）；④ 会话状态增量（供列表状态点）；⑤ model catalog / skill list / cron list 三个只读 RPC。
- 注意权衡：dsh 的 reasoning 实时展示 vs seek 现行「reasoning 不下发」是**策略差**不是技术差（`webfrontend_stream.go:31-34`），重设计时要显式拍板，别当成缺件。

---

## e) 可抄边界

1. **运行时零引入（红线）**：React、Cordis、任何 npm 构建链不进 seek 单二进制——两赛道论（dsh-analysis §8.0）。seek webfrontend.html 的零依赖手写路线保留；dsh 的 slot/插件体系是**宿主规模**的解，不搬。
2. **可直接复刻（抄值不抄码）**：全部 CSS 自定义变量与值（b 节表即提取稿）、列宽常量与让位链算法（a 节）、字号派生公式、head-tail 截断算法、markdown 增量解析策略（冻结+尾部重解析）、滚动跟随 24px 阈值方案、StateDot 像素追逐 keyframe、审批覆盖 composer 的 DOM 结构思路、空态 null / 错误内联 / pending 禁用三律。这些在 MIT 下连「参考」都算不上义务，但仍建议在代码注释里标注来源（审查可溯）。
3. **直接搬 CSS 片段时**：MIT §1 要求保留版权与许可声明。机制：dsh 自己用 `THIRD_PARTY_NOTICES.md` 披露第三方（README.md:63）；seek 现无该文件——**建议实施波次建 `THIRD_PARTY_NOTICES.md`**，凡逐字拷贝 ≥1 条规则的片段，按「文件 → 来源 repo@commit → MIT 声明」登记（等价于 dsh 机制反向使用）。
4. **不可抄**：鲸鱼 logo/品牌词/「DeepSeek」文案（商标不在 MIT 内）；`--dsw-*` 前缀建议换成 seek 自己的命名空间（如 `--skw-*`），只借值不借名，避免两套 token 在未来混合部署时打架。
5. seed.ts 订正：卡片猜它是「种子演示数据」——实际 `packages/client/web/src/seed.ts` 是插件加载器的**冻结模块表**（React/Cordis/store/slots/primitives 单例注入，:1-37）；真正的演示数据是 webworker preview 的 fixture overlay（`packages/experimental/webworker-runtime/src/fixture-manifest.ts` + showcase overlay），见附录截图的产出方式。

---

## 附录：参考截图（尽力项，已达成）

存于 [`notes-assets/`](notes-assets/)（相对本文档），全部来自 clone 预构建 dist 的本地静态伺服 + CDP 无头截图（脚本范式借 `project_x/scripts/page-review.mjs`，沙盒内随机高位端口，clone 零改动——VFS 镜像由 packer 输出到 /tmp 副本再伺服）：

| 文件 | 内容 |
|---|---|
| `dsh-01-hero-light.png` | 空环境首启：Hero 空态（「Into the Unknown」）+ 完整侧栏 + composer 的模型/权限 pill（1440×900 亮色） |
| `dsh-02-showcase-light.png` | showcase 演示会话转录（亮色，120 个聊天锚点行）：用户右对齐气泡 + 助手无气泡文本 + 工具折叠行 |
| `dsh-03-showcase-dark.png` | 同会话暗色（body[data-ds-dark-theme] 真实 CSS 分支）：可见 To-dos 折叠行（图标+标题+「2 completed · 1 in progress」+chevron）、右缘 TurnNavigator 刻度轨、回底按钮、StatsLine「29 turns · 30 steps \| LLM 0s · Tool call 0s」 |
| `dsh-04-rail-narrow.png` | 900px 视口：侧栏自动折叠成 56px 图标轨（logo/新会话/搜索/设置四控件入轨） |
| `dsh-05-showcase-mid.png` | 转录中部：用户/助手成对流 + 底部 To-dos 折叠行 + StatsLine（showcase 数据偏文本对，工具卡多为折叠态；展开态结构见 c 节代码引用） |

产出链备忘（复现用）：`apps/web` 的 dist 不含 preview 镜像，需 `pnpm build:preview`（`apps/web/package.json:28` 会跑 `dsh-pack-vfs-image`）；本次用已构建的 packer lib 直出镜像到 /tmp。`preview.html?preview-fixture=none|vfs-example` 直选数据源（`webworker-runtime/src/client/source-chooser.ts:196-203`）；首启有**连环引导弹窗**（Internal Testing Notice「Continue」→ API key 设置「Configure later」，均为持久设置），截图脚本逐轮点掉再取景；showcase 会话需在侧栏点开「WebWorker Preview Showcase」treeitem（`div[role=treeitem]`，`ui-workspace/src/client/rows/Rows.tsx:413-421`），否则恢复的是空白 New Session。
