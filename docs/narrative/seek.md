# whyiyhw/seek 项目叙事稿

> 数据截至 2026-11-26（北京；采集 2026-11-26T05:12Z，main `76dce969`）；负责人会话（agent，2026-11-12 用户全权移交）定期更新。
> 模板与写作纪律见 butler 仓 `docs/narrative/README.md`（seek 仓内没有该文件）；事件 `#N` 指 butler 事件 id（`memory_search` / `GET /events` / 看板详情可查）。
> 上一版 `cc35355f`（2026-11-24，#15210，采集 20:05Z）；其后 `544c378a`（v0.13.1 发版物）同步改了本稿几行。本稿由卡 c_vmrguu 刷新。

## 目标

**以 projects 表为准**（`project_get whyiyhw/seek`，最近更新 2026-11-17；D1 拍板改写 #7859）：
把 seek 从终端 coding agent 核心，迭代成**能自我进化的本地 agent 平台**。形态走 v8 论题——
**薄内核 + 外围按需 + 用证据筛形态**（内核只留边界/记录/裁判/缓存拼装与基础工具，其余外置；
每个零件可度量、可拆掉对照）：① 成本断崖（DeepSeek-first）；② 多端通用（`Frontend` 端口）；
③ 自进化闭环（失败 → eval → 修复 → 发布）。完成标准（同表）：一次过率 / 每任务花费 / 人审分钟
有基线；有裸核/完整对照表；内核违例数可追（`scripts/kernel-deps.sh`）。止损线见 `docs/prd/v8.md`
§9（第二波无改善不开第三波）。**不做公开跑分**（#2575：eval 降位内部回归；外部锚只做私有校准）。
治理层是 butler（卡片/派发/验证/合并），seek 是执行与产品层；seek 自身也经 paseo 车道回到调度池。

## 现状

- **v0.13.1 已发，之后 main 前进了 68 个提交 / 17 个 drive 合并**：补丁版 11-25 06:12Z 发布
  （#16276，7 资产，tag `v0.13.1`→`544c378a`）；至 11-26 05:03Z main=`76dce969`（与 origin 同步）。
  本窗口（自上一版采集 20:05Z 起）合入 18 张 seek 卡 = tag 之后的 17 张 + c_sd3x7o（20:08Z 先落、
  已含在 tag 里）。**CHANGELOG「已进 main、未部署」已 17 条 / 6242B**（超 6000B 预算 242B，
  `scripts/weight.sh` 判 over）——下一版发版卡得先收口。本机部署位已同步到 main
  （`~/.local/bin/seek --version` 自证 `v0.13.2-0.20261126042801-76dce969fe12`），未进任何 release。
- **二线车道死路一轮收口**：c_koutux 两个结构性缺陷（开流零重试 + `streamBody` 不判 `sc.Err()`）
  修复 `d3fc6e36` 经 merge `8cb2a385` 进 main（#17329 verdict=pass，覆盖
  opencode/glm/opendesign/compatible 四道）；c_trrsn7 四处根因（#16161）与 c_rnhce7 的 `max_tokens`
  单一口径（#16217）随 v0.13.1 出（`53d60834`）；c_dxrjjl 模型×lane 错配闸 + opendesign 子代理路由
  `44aedfc3` 经 merge `ead02026` 进 main（#17492，真机补证 #17557）。
- **judge 线开了头**：`internal/judge` 判读内核进 main（c_42a7rf，`d565d669` / merge `b796ddd0`，#17455）；
  gate 已于 11-24 开（#15437：开关就是 per-case `judge.mode`，`SEEK_EVAL_JUDGE=0` 一行关回；两个保留
  理由——单轮余量薄、凭证路径非正式——留档未清零）。**CLI 接线仍是断点**：c_jdo7rp 90 分钟被杀
  （#17027）、c_qjuags（`--max-cost`）同因（#17161），两卡 blocked。
- **验证读数仍由「人核 + 归因」主导**：本窗口 seek 合并 18 张卡，verdict 分布 pass 8 / doubt 6 /
  未验证 3 / 人工改判 1。doubt 六张里：三张是同一批 `~/.seek` 写入事件（serve 18:07:51Z 与
  projects worktrees 17:17:30Z，一次人核可解 c_skczdu/c_yc2thl/c_idinaj，见
  #16892/#16893/#16894）；一张是 c_7vpiz5 自报反向实验写进真实 `~/.seek` 再删除（#16888）；
  两张各一处枚举缺口（c_3xyt7w 的 `projects/*/sessions` #16473、c_lh6tlq 的
  `projects/*/worktrees` #16385）。这不是代码质量崩塌，是**存疑归因缺机器闸**。
- **状态控制三卡已拍板、零落地**（#17344/#17363）：非额度三态死亡仍一律落 blocked「需人工检查」；
  11-26 03:37Z 三张（c_pozbmd / c_jobprc / c_osfttl）在同一拍以「session 正常结束但未写回」死亡
  （#17407、#17408、#17413）。
  断流防丢两步（保险 commit + progress 快照）已进 drive 派单 prompt（c_norxjd，#15950/#15972），
  但本窗口仍有四张卡要主会话接手才闭：c_4rp2py（死会话半成品收尾，#17442/#17466）、c_fpnlir
  （验证会话 verdict 成孤儿，#17446）、c_42a7rf（代拍合并读 /cards 超时，#17459）、c_dxrjjl
  （合并收口，#17492）。
- **内核与卫生**：23 内核包、违例 1（`pkg/agent → internal/hooks`，定性见 `v8.md` §9.6）；pitfalls
  live=392；AGENTS.md 3189 词 / 3200 上限（frozen）；eval 用例 45 例（含 8 题外部锚），release lane
  24 条（承重轴 2/3 在册）。
- **看板**：全仓 34 张（todo 25 / doing 3 / blocked 4 / review 2）；seek 仓 16 张（todo 10 / doing 2
  —本稿与 c_ozeqxx—/ blocked 4 / review 0）。瓶颈依旧是人拍板与归因，不是卡不够。

## 关键决策

- **目标改写为 v8 论题**（#7859）：拿掉「能力对标 Claude Code」，换成薄内核 + 外围按需 + 用证据筛
  形态；完成标准收敛为三基线 + 裸核对照表 + 违例可追。
- **按数据止损，不硬开第三波**（#13073）：R2 后 2.46 天窗口（样本 56 卡，薄）一次过率 32.1% vs
  基线 64.3%、人审中位 181′ vs 113′，两数无改善 → 第三波（U3/T3/W）不开、R3 判缓、S2 缓开；
  并钉死「堵点移位 ≠ 路线证伪」的读法。
- **承重轴名单按闸门语义定，不按「case 有没有落地」**（#16405）：E4 `subagent-bg-resume` 在册，
  E3 `plan-e2e` 留在名单外——`record_only: true` 的行永不染色，写进闸门只会让闸门永久绿。
- **`--tools none` 是整值哨兵，不是列表特例**（#16382）：空串=全量注册表是已发布契约，零工具必须有
  自己的拼写；让 `none` 走列表解析会静默吞掉或退化成未知工具名。
- **二线车道故障按客户端结构性缺陷修，不按「等上游」处置**（#16420 → #16777）：开流零重试与吞错
  是四车道共用的一条裸奔路径，重试 + 判 `sc.Err()` 才是治具；「强制 HTTP/1.1」刻意不做（#16420）。
- **模型×lane 错配用单源 + 分权修**（#16458、#17492）：`resolveProviderName` 单源抽取，
  `modelcap.Serves` / `LaneOf` 两问分离，`providerOrder` 修掉 map 遍历随机序。
- **OpenDesign 走共享 wire id，不建车道级注册表**（#15199、#15262）：同 id 互相覆盖、按 id 反推车道
  会因 map 遍历而不确定；key 落 `~/.seek/config.json` 而非 env，四个显示缺口写进 `guide-config.md` §3。
- **慢道超时按车道取值**（#17111）：eco（0.27× 速率）150 分钟、其余 90，单点在
  `internal/drive/lane.go`；显式压短值原样尊重，不动快道预算。
- **状态机加转移不加状态**（#17344/#17363）：五态不动；死亡事实落一份（事实卡先行），卡级 ≤2 重派
  走既有 rework 轮次（不依赖 serve 可读性），车道级窗口读 lane-deaths 视图、读不到 fail-open——
  「凡死亡必人工」的默认路径退役。
- **收活前先落盘，再写回**（#15950）：prompt 加保险 commit + progress 快照两步，让断流死在写回与
  快照之间也能被收活闸按产物救活，而不是按「未写回」重派重做。
- **审批模式靠后缀重建，路径唯一**（#16843）：`[plan: approved] (auto-approve-per-step)` 后缀读成
  batch，resume 侧在 `planTool.Restore` 之前装回；无后缀旧会话字节不变。
- **测试隔离必须是两半：隔离 + 落点断言**（#16878）：只 Setenv 不改断言，下一次回归照样全绿；
  c_7vpiz5 的修法是补「字节落在哪」的断言，并把全仓 attach/store 同族按同一口径清点。
- **合并闸声明是机器本地文件**（#14385）：闸是本机合并点的质量门，不是仓库对外契约；克隆缺声明
  则宽容跳过（opted-in 仓缺文件升级为告警，c_u2int2）。
- **pitfalls 一条目一文件**（#11009）：11-13 起 46 次合并冲突有 31 次撞同一文件——拆成
  `docs/pitfalls/<段>/<slug>.md`，计数现算不入库。
- **web 对齐 dsh：抄皮不抄骨，按整体结构重做**（#8533 → #16807）：W5 十四卡「能力映射」式拆分
  仍不像 dsh，改按 dsh 的整体骨架拆 W6 九卡链（口径源 `docs/notes-web-w6-dsh-shell.md`，`ee2dfb56`）。
- **无人值守 eval 默认不烧计量 key**（#9336）：缺省走 OpenCode Go 订阅道，回计量 key 必须显式指名。
- **autopilot 车道自升 Yolo，用 `flag.Args()` 判道**（#16864）：唯一约束是 no-remote 闸；
  「子命令在前、全局 flag 在后」在 Go flag 语义下互斥，`--yolo` 没有可表达的位置。

## 否掉的方案

- **跑分超越作为目标**：#2575（用户否，无意义）；外部锚只做私有校准、永不外发（#14002）。
- **抄 dsh 插件框架 / React 运行时**：`docs/dsh-analysis.md` §0/§8（两条赛道，编译期注册是护城河不是
  债）；web 同理「抄皮不抄骨」（#8533）。
- **现在开第三波 / R3 / S2**：#13073 + `v8.md` §9（止损线已触发；R3 治具对象结构性为零）。
- **把 E3 `plan-e2e` 写进 release lane**：#16405（record_only 行永不染色 = 把闸门关掉）。
- **为 OpenDesign 建车道级注册表**：#15199（只按模型 id 索引的表会同 id 互相覆盖）。
- **用计量 key 跑无人值守 eval**：#9336 + `v8.md` §9.1。
- **web-gate 登记进验证白名单**：#14759（口径复核 #14774）——跨目录读数不同线，登记只会复现「存疑」。
- **复用 drive/c_raxldd 旧草稿分支**：#15423（tip 与最终提交同父，是同功能旧草稿 +297/−610，
  合并会回退 main 上更全的 `guide-config.md` §3）。
- **rebase 逐个重放收尾**：#16519（会把 main 已合并的提交重放，冲突与重复消费都放大）；改分支内
  「重落主线」。
- **把 `none` 当列表特例 / 匿名特例**：#16382（要么零、要么明确白名单）。
- **把常驻塞进核心 agent loop**：`docs/prd/feature-remote-control-plane.md` §1.4（违背零-daemon
  精确化，v8 D2）。

## 下一步

- **主线一 · judge 本体**：内核已进 main；c_jdo7rp（CLI 接线 JSONL in/out + 有界并发 + 退出码
  0/1/2）与 c_qjuags（`--max-cost` + exit 3）两卡 blocked（均 90 分钟机制杀、零残值），需翻 todo
  复用续做（勿重做内核）；之后才是 release lane 接入（`eval/README.md` §E7）。另有两张新卡：c_tnzudu
  （路由表与推断口径对齐，c_dxrjjl 复审发现的死胡同提示）、c_ozeqxx（自动会话标题落 header 的
  additive `title` 字段，#17579）。
- **主线二 · web W6 九卡链**（口径源 `docs/notes-web-w6-dsh-shell.md` + dsh 中文实拍四图
  `ee2dfb56`）：c_pozbmd（blocked，依赖 c_ru7rbn 已于 #17259/#17280 收口，可翻）→ c_onxxge →
  c_ovly6d → c_ljtmy4 → c_mo7quv → c_ekuuof → c_w74tuo → c_eq3eak → c_3dz3kg。每卡都带亮/暗 ×
  1440/390 截图过目（`scripts/web-page-review.sh`，条款 c_mqz4kg）。
- **主线三 · 状态控制（butler 仓）**：c_ivcu2a 事实源先行落笔 → c_fnnjzl（≤2 次自动重派）、
  c_vfssar（车道熔断）读它、不各造窗口；c_arsxj5（晨报在飞健康段）独立。信号卡 c_osfttl（eco 四连
  死）已转 review（#17558 代拍暂缓，主检出 MERGE_HEAD 挂着），是「车道病死人工复查」的末代。
- **断流/收活缺口**：c_ryng2v（五形态审计：验证会话后台等长命令被当结束、漏派验证、验证车道拒
  只读检查、`.butler-commit.md` 顺序合并必冲突、坑 8 并发下闭合不了——c_fpnlir/c_o7o23h 实录）、
  c_ozjcdb（pass 卡代拍合并失败无独立重试路径，c_42a7rf 白等 2 小时+）。两张都是「死亡不是活」的治具。
- **工程债**：c_f3jdf4（drive stash dance 吞主检出未跟踪件：c_sozboy 合并 1492 行仅靠 fsck 救回，
  #17313）；c_ckla3l（eval 结果文件并发写零保护，E7 待开卡 #1，90 分钟机制杀 #17053）。
- **发布与读数**：CHANGELOG 17 条未部署且超预算 242B（`weight.sh` 判 over），下一版发版卡先收口；
  v0.14.0 范围未定（须带一张减法卡——`docs/notes-slim-candidates-2026-11.md` A 类 14 项已全消费且
  已复核零引用 #16280，新刀口需重扫）；`v8.md` §9.1 同口径复测待排（5–7 天样本攒足后定趋势）。
- **本稿口径**：信息源 `project_get` + `memory_search` / `GET /events` + `/cards` + `git log main`；
  数字均可用命令复现（内核 23 包 / 违例 1、pitfalls live=392、eval 45 例、release lane 24 条、
  CHANGELOG 6242B）。
