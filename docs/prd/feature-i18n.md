# i18n：视图层多语言消息目录

**主题**：给 seek 一个 stdlib-only 的界面文案本地化机制——`internal/i18n` 消息目录 + 启动时一次性语言解析 + `/lang` 运行时切换。人类读的散文型文案（提示、横幅、命令反馈）跟随用户语言；模型读的字符串一律保持英文。

**状态**：🚀 v1 已上线（机制 + 首批 key + `/lang` + 配置持久化）。后续扩面见 §五。

**触发起因**：2026-09 用户看到 `deepseek api error: unknown_error: Insufficient Balance` 全英文报错后问「我没做 i18n 吗」——确认整个代码库没有任何本地化机制（唯一 locale 痕迹是 statusbar.go 里一条关于 CJK 宽度的注释，README.zh.md 只是文档双语）。本 PRD 补上机制并划定**哪些 surface 永远不翻译**。

---

## 一、目标与范围

### 1.1 解决的问题

- 中文终端用户（seek 的主要受众）看到的界面提示、错误反馈、占位符全是英文。
- 没有机制就没有扩面：先立规矩（解析时机、回退链、目录奇偶性测试），再逐步把文案搬进来。

### 1.2 设计哲学：翻译「给人看的」，不翻译「给模型看的」和「给宽度算的」

| Surface | 翻译? | 理由 |
|---|---|---|
| TUI 散文文案（提示、横幅、队列指示、thinking 占位、菜单 hint、命令反馈） | ✅ | 纯人类消费 |
| 工具结果 / agent 错误文本（喂回 LLM 的字符串） | ❌ 永远 | DeepSeek 前缀缓存按**字节精确匹配**计费（命中便宜 ~10×）；模型可见面必须跨会话、跨语言切换字节稳定。`Insufficient Balance` 那类报错原文来自服务端，本地化无从谈起 |
| 状态栏（statusbar.go） | ❌ 永远 | 刻意 ASCII 化的宽度敏感区（foldStatusBar 的 width-oracle 纪律）；CJK / 全角标点会重蹈 soft-wrap→banner 幽灵覆辙 |
| 斜杠命令 description（菜单与 /help） | ❌ v1 暂缓 | 30+ 条长文案，独立批次处理（§五） |
| 系统提示 / sysprompt | ❌ 永远 | 同「工具结果」——提示前缀必须字节稳定 |

### 1.3 范围之外

- ❌ 复数 / 性别 / ICU 复杂复数形态——en/zh 都不需要；`%s`/`%d` 位置占位符够用
- ❌ gotext / `x/text/message` 运行时机制——`x/text` 虽已是直接依赖（宽度计算），gotext 对一个两语言 CLI 是杀鸡用牛刀；map + 回退链零依赖、可测试
- ❌ RTL 语言（ar/he）——加入时需重审方向性，v1 不承诺

## 二、机制设计

### 2.1 语言解析（每会话一次）

优先级（高→低），在 `cmd/seek` 启动时、紧跟第一次 `config.Load()` 之后解析并 `i18n.SetDefault`：

1. `SEEK_LANG` 环境变量（按次覆盖，CI / 一次性调用用）
2. `~/.seek/config.json` 的 `language` 字段（`/lang` 写入的持久选择）
3. `LC_ALL`，然后 `LANG`（zh_CN.UTF-8 终端零配置得到 zh）
4. English（源语言，永远的地板）

**缓存纪律**：与 `sysprompt.Header.Date` 同款——语言是会话级常量，绝不逐 turn 重算。唯一许可的运行时变更是 `/lang`（见 2.4），且只有视图层读它，模型前缀不受影响。`Resolve` 会归一化 locale 标签（`zh_CN.UTF-8` / `zh-Hans` / `ZH` → `zh`），不支持的语言静默落到下一优先级。

### 2.2 Bundle 与回退链

`Bundle` = 语言码 + 不可变 map。`T(key, args...)` 三级回退：**本语言目录 → 英文目录 → key 本身**。最后一级保证缺条目时 UI 显示丑陋但可见的 key，而不是空白。带 args 时按 `fmt` 格式串处理；占位符位置跨语言必须一致（有奇偶性测试守护）。

英文文本住在 `messages_en.go` 而非散落调用点——英文渲染本身与翻译可 diff，目录演进有单一事实源。

### 2.3 全局默认 Bundle

包级 `atomic.Pointer[Bundle]`，`init` 装英文，`SetDefault` 原子换（/lang 与 cmd/seek 是仅有的两个调用方）。原子换保证 `-race` 下 mid-render 换语言无撕裂。测试不接线也能跑：默认英文，现有断言英文渲染的测试全部不受影响。

### 2.4 `/lang` 命令

- `/lang` → 报告当前语言
- `/lang <en|zh>` → 换默认 Bundle + Load-Modify-Save 持久化到 config.json（SEEK_HOME 覆盖照常生效）
- 不支持的值 → 点名错误值 + 支持列表，不换不写
- 持久化失败（只读 HOME / config 解析失败）→ 降级为「仅本会话生效」并给出警告行，命令本身不失败
- 已渲染进 scrollback 的旧行保持原语言——只影响换语言之后的渲染

### 2.5 防漂移测试

- `TestCatalogueParity`：每个翻译目录的 key 集合与英文**完全一致**（缺 = 回退到英文的静默漂移，多 = 死文本）
- `TestCataloguePlaceholderParity`：per-key 的格式动词多重集（%s/%d/%q…）跨目录相等——翻译丢一个 `%d` 只会在运行时炸，测试让它提前炸
- `TestTranslationSmoke`：合成参数渲染全部 key，`%!`（BADVERB）即失败
- `TestConcurrentSwapAndRead`：8 读 1 换并发压力（CI `-race` 下有意义）

## 三、配置

`config.Config` 新增 `Language string`（`json:"language,omitempty"`）。空 = 自动检测；老 config 文件无此字段照常工作（unknown-key 忽略 / absent 默认）。

## 四、v1 交付的 key

`view.starting`、`view.provider_banner`、`view.thinking{,_s,_ms}`、`view.queue.{steering,queued}`、`menu.hint.{simple,toggle}`、`cmd.unknown`、`lang.{status,switched,unsupported,persist_failed}`。命名空间 `<surface>.<element>`。

## 五、路线图

| 批次 | 内容 | 状态 |
|---|---|---|
| v1 | 机制 + §四 key + `/lang` + 持久化 + 防漂移测试 | ✅ |
| v2 | 斜杠命令 description / `/help` 菜单面 | ⬜ |
| v3 | 首跑 wizard、setup 流程文案 | ⬜ |
| v4 | print-mode / `-json` 之外的启动报错 | ⬜（`-json` 输出永远是机器契约，不翻译） |

## 六、阶段交付

- ✅ `internal/i18n/`：`i18n.go`（Normalize/Resolve/Bundle/T/默认 Bundle）+ `messages_en.go` + `messages_zh.go` + `i18n_test.go`（回退链、优先级、奇偶性、占位符、并发、渲染冒烟）
- ✅ `internal/config`：`Language` 字段
- ✅ `cmd/seek/main.go`：启动一次性 `i18n.SetDefault(i18n.New(i18n.Resolve(...)))`
- ✅ `internal/tui`：view.go（starting / provider banner / thinking / queue / menu hints）+ commands.go（`cmd.unknown`、`/lang` 注册与 handler）
- ✅ `internal/tui/commands_test.go`：/lang 状态报告、切换+持久化往返、不支持值拒绝、/help 列表收录
