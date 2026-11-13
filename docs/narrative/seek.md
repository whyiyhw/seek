# whyiyhw/seek 项目叙事稿

> 数据截至 2026-11-13（北京）；负责人会话（agent，2026-11-12 用户全权移交）。
> 模板与写作纪律见 butler 仓 `docs/narrative/README.md`；事件 `#N` 指 butler 事件 id（memory_search 可查）。

## 目标

**以 projects 表为准**（`project_get whyiyhw/seek`，2026-11-12 落库）：把 seek 从终端
coding agent 核心迭代成**能自我进化的本地 agent 平台**——① 能力对标 Claude Code、
成本断崖（DeepSeek-first）；② 多端通用（`Frontend` 端口，飞书第一个）；③ 自进化闭环
（失败 → eval → 修复 → 发布）。治理层是 butler（卡片/派发/验证/合并），
seek 是执行与产品层；seek 自身也通过 paseo provider 车道回到 butler 的调度池。
**不做公开跑分竞赛**（用户拍板 #2575：无意义；eval 降位为内部回归工具）。

## 现状

- **一夜平台化落地（11-12 夜至 11-13 晨，事件 #2618/#2660）**：control-plane 装配线
  M1a→M1b→M2→M3→M6a→M7a **全部交付进 main**（类型层/bindings、serve 骨架、路由指令、
  执行器含自愈、生命周期与 build-tag 隔离、guide 文档 + Telegram 纸面验证）；M4/M5
  （飞书 frontend 真联调）待用户提供自建应用凭证。与 CC 对照的 roadmap 驱动已降级
  为参考（comparison.md 改造完成）。
- **token 三车道落地**：GLM 订阅车道进 main（provider 注册 + pricing 三口价），
  真 key 烟测全链通且 **cache_hit_tokens=10944**——GLM 前缀缓存与 seek 缓存纪律咬合
  （#2897）；DeepSeek 关键点验证口径不变。
- **自进化内环首轮收成**：eval 14→20 例（3 契约回归 + 3 pitfall 衍生，pitfall→eval
  管道落地）；skill 自动蒸馏设计稿就绪（feature-skill-distill.md，四层新颖性漏斗 +
  人审闸）；dsh 事件溯源评估结论=approval 全量 fold 不值得、resume 缝隙部分做
  （notes-event-sourcing-adoption.md）。
- **butler 侧**：验证车道坑 8 越界问题修复（c_jnyf3y，机械面收进调度器）、Stop hook
  多仓误报修复（c_f3wwqq）、go vet 清零；剩余一张 c_hi3lv5（验证 verdict 口径改造）
  拟人判 ask_human 等用户拍板。
- 顺手修掉：permission symlink CWD 误判（c_uh2x4q）。

## 关键决策

- **平台化取代跑分对标**（#2575，2026-11-12 用户拍板）：目标=自我进化平台 + CC 能力
  + 更便宜 + 多端；eval 只做内部回归。
- **移交 agent 全权管理**（#2575）：butler 当治理层，开发走卡片/派发/验证流程；
  本仓 AGENTS.md 纪律（pitfalls、doc-budget、eval-first）不变。
- **参考 dsh 不照抄**（`docs/dsh-analysis.md` §0/§8）：取「事件溯源 ⇒ 前缀缓存稳定」
  的不变量思想与可替换缝；弃 226 包 monorepo 与运行时组装复杂度——
  seek 的优势正是编译期纪律 + 单二进制。
- **多端走 sidecar 不动核心**（feature-remote-control-plane.md D-C1）：
  常驻只活在 `seek serve`，对零-daemon 铁律做有意、隔离的偏离。

## 否掉的方案

- **跑分超越作为目标**：用户否（#2575）——没有意义。
- **无脑抄 dsh 插件框架**：dsh-analysis.md 结论——两条赛道（官方云 harness vs 本地
  单机开发者），编译期注册是 seek 的护城河不是债。
- **把常驻塞进核心 agent loop**：feature-remote-control-plane.md §1.4，违背 v5 零-daemon。
- **记分牌式同题对跑（公开口径）**：#2573 待拍版方案的核心卖点，#2575 拍板后降位为
  内部回归，不对外宣传。

## 下一步

- **多端收口**：M4/M5 飞书 frontend（等用户给 App ID/Secret，晨间已提醒）。
- **paseo 车道**：两缺口卡已开——session/new cwd 生效（c_bnjrw5）、SEEK_ACP_YOLO
  无头权限（c_rewul5）；落地后 butler drive `--provider seek` 最小卡验收。
- **自进化**：skill-distill 从设计转实现（开卡时机：飞书线或 paseo 线任一收口后）；
  eval 用例随每次 dogfood 失败持续扩容。
- **治理**：c_hi3lv5 验证口径改造等用户拍板（能力外项与真 doubt 分流）。
