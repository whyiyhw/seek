# 更新日志（CHANGELOG）

## v0.13.0（2026-11-24）

v0.13.0 — **回合控制、判分层、web 收边**的一版。人第一次能在回合中途改主意：流式期间按 Enter 不再二选一（干等到整轮结束，或丢掉已跑完的工具结果），而是压进下一个推理边界。机器第一次只在**失败形状**上说话：retry-loop / failure-run 两条确定性规则，绝不延长回合——同批设计的 silence-run 被 390 份真实会话回放否决（70% 会话误报），当版删除。eval 长出第三层：LLM-as-judge 判分层 + 外部锚（Terminal-Bench-lite 私有题集、跨 harness 对照），真车道标定收口 0.88 agreement / false-pass=0，gate 仍留人拍。web 面从「能用」走到「整齐」。减法照纪律又走三刀。

### Notable changes

- **流中插入用户消息（行为新增）**. `QueueUserMessage` 压进带锁队列，只在两个合法边界排空——turn>0 的循环头（工具批结果已入 history，tool_calls 与工具结果仍相邻不会 400）与「模型想收口」的结算点（把 about-to-return 变成额外回合，而不是静默丢弃）。上限入队即拒（单条 8KB、单轮 4 条），不静默截断——注入文本是永久历史，每个后续请求都要重发。TUI 的注入接缝是 optional sibling 接口，成功打「⤵ 已在下一个边界插入」并把注入块写回 scrollback；被拒则逐字节走旧的排队/转向路径。
- **回合边界引导（新 `internal/guidance`）**. 不调模型的确定性规则，只认失败形状：retry-loop（同一 name+规范化 args 连续失败 3 次）、failure-run（连续 3 次工具失败）。三条硬约束：turn 0 不注入、人类插话优先（同边界只发一条）、只走循环头。依据是实测：「成功路径提示」在 40 会话里触发 3,871 次而真实增量 0.6pp——提示要有受众，动作已完成时提示只是评论。
- **交互事实侧车 `seek ui-trace`**. 注入 / 排队 / 转向 / 撤回 / Esc 在 transcript 里同形或无形（注入与转向落盘是字节相同的 `user` 行，撤回什么都不写），无法从任何 transcript 派生，故 `~/.seek/ui-trace.jsonl` 记事实、`seek ui-trace` 出报告。
- **eval v3：判分层 + 外部锚**. J1 judge 层（LLM-as-judge over final_text，opt-in、校准前 record-only）；J2 校准集与 agreement 报告——五轮 34 条真车道迭代收口 0.88 / false-pass=0 / gate-eligible；J3 首批真用例接线。A1/A2 Terminal-Bench-lite 私有题集 8 题 + 车道 + 机制自测；A3 跨 harness 对照协议。配套加固：judge.py 判决解析两种流式形态、锚点崩溃形两修、judge-calibrate 把「车道中断」与「质量差」分流。
- **web 面收边**. 图标/按钮体系：全页 UI 去 emoji 换内联 Lucide sprite（vendor path，零运行时依赖，守住 CSP 与单文件零构建），按钮收敛三档；390px 手机宽（单栏抽屉、按钮组换行、设置面板堆叠）；统计面板跨会话趋势（`stats/trend` + 手绘 SVG，侧栏 per-session cost）；`/bind` 项目点选 + 目录浏览器（新只读 method `fs/list`，根集收敛 `serve.project_roots` ∪ projects ∪ memory 根）；markdown 渲染补齐 GFM 表格/引用/段落/代码块容器；暗色次级文字升主文色（3.59/2.29 → ≥6.0）；时间戳短格式、「N 轮」、主区空态、发送禁用态收口。
- **工具与车道**. bash/grep：成功路径 `[hint:]` 降噪 67%（实测无增量）、剥冗余 `cd` 前缀、grep 补 `-v/-l/-c` 等五个 shell 等价参数；`ask_user` 在无头车道报 `ErrDisabled` 而非「programming error」——默认值反转（askPolicy 出生即 Disabled，TUI 入口显式开 Ask），模型改问纯文本而不是重试同一个调用；`-no-save` 铸一次性会话 id，修 OpenCode Go 登录道 headless 400；sysprompt 身份块事实修正（去体积硬数字、补 GLM、9 行并 5 行）。
- **减法三刀**. `fim_complete` 全链退役（pkg/deepseek FIM client、sysprompt 工具文档行、benchmark FIM 指标）——实测调用比率 0%/11%；`think` 工具 + `dual-model` 内置 skill + D8 deepSeekOnly 过滤链整条退役——109 个真实项目会话仅 2 次调用；deadcode B 类（生产视角死、仅测试引用活着）首批删除（第 6 刀，逐项语义裁决、删前双模式重跑 + 全仓 grep 复核）。三刀都按同一口径：模型不用，养着比删净贵。
- **工程与闸**. 合并三闸归位 doc-budgets / pitfalls-count / web-gate（selftest 按测量线口径，卡面标 `not-in-lane` 的合并点必跑）；`.butler/merge-gates.json` 退出版控（闸声明是机器本地文件）；web 审查证据包补视觉趋势计数；`web-page-review.sh` 无头截图审查入口沉淀；pitfalls 台账按一条一文件继续分流。

> **前缀缓存**：本版 bash/grep 工具描述有变，升级后的第一次会话会全量重算 prompt 前缀（一次），之后照常命中。

更早的版本见 [GitHub Releases](https://github.com/whyiyhw/seek/releases)——每个 release body 与其 annotated tag 同文。本文件只保留当前版本，历史版本查 git 历史（`git show vX.Y.Z:CHANGELOG.md`）。

## 已进 main、未部署
- c_ff4pcp：seek memory：主动策展第一刀——tagline 注入即计有效使用（active-memory 最小切片）
- c_evmhdn：seek v0.13 发版：CHANGELOG 收口 + annotated tag + GitHub Release（捆绑减法第 6 刀）
- c_myz47s：叙事稿提炼：seek
- c_n72agp：seek web：hooks/audit SSE 实时流——补 inspect-rpc §6 #6/#7（tail follow 的既定分流承接）
- c_xhbqae：seek eval：judge gate 开启（record → gating）——用户拍板，kill-switch 在位可逆
- c_sd3x7o：叙事稿提炼：seek
