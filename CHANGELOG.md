# 更新日志（CHANGELOG）

## 已进 main、未部署

- c_i2cyb7：验证基建跨仓统一：scripts/selftest.sh 注册入口（与 butler 同款协议：case 分发/--list/拒路径形态/透传退出码），登记 acp-smoke（真二进制 ndJson 冒烟——initialize/session/new 双锚/坏 cwd fail-loud/EOF 干净退出，SEEK_HOME 沙盒免 LLM）与 weight；验证车道凭既有 Bash(bash scripts/selftest.sh:*) 白名单即可跑 seek 仓验收
- c_bnjrw5：acp D-1：session/new 的 cwd 生效——`acp.AnchorSession` 同时 chdir 进程 + `Policy.Reanchor`（git/grep/list_dir 与 read/edit/write/bash 两条解析约定一起锚定），sessionId 取项目目录名，ProjectID 按 `paths.ProjectID(abs(cwd))`；缺省仍当前目录，坏 cwd fail-loud
- c_7hudp6：control-plane 动工前坐实三项待验证：session id 字段、print 模式权限行为、larkws ACK 形状
- c_xcy7ff：control-plane M1a：类型层 + bindings store——Frontend/Inbound/Reply/ConvID 与原子写持久化
- c_u7w55t：seek 接入 GLM 订阅车道：openai-compatible provider 配置 + 工具循环冒烟
- c_seuoef：control-plane M3：执行器——exec seek 子进程 + JSONL 解析 + per-Conv 串行与自愈
- c_3adk2z：skill 自动蒸馏设计稿：新颖性判定 → SKILL.md 草稿 → 人审闸（feature-skill-distill.md）
- c_n2k6qi：dsh 事件溯源不变量在 seek 的适用性评估：approval/todo fold 化设计笔记
- c_evvow4：memory 与 skill 联动现状盘点：蒸馏漏斗里 L/M/S 与 SKILL.md 的断层
- c_xelskv：control-plane M6a：serve 生命周期——多 frontend 扇入、优雅 drain、build-tag 依赖隔离
- c_nap4wk：control-plane M7a：guide-serve 文档骨架 + Telegram frontend 纸面接缝验证
- c_odyzbq：comparison.md 定位改造：从 roadmap 驱动降级为参考 + 平台化验收口径段
- c_27i3zi：seek 接入 paseo 作为 provider：可行性设计稿 docs/prd/feature-paseo-provider.md
- c_uh2x4q：internal/permission isWithin 符号链接 CWD 下误判相对路径 outside：write 被误挡
- c_hw47pe：pricing 表支持 glm 车道计价：glm-5.3 三口价 + $ 统计不落 0
- c_7krtrc：eval 扩容：把 c_7hudp6 验证过的三项契约锁成回归用例
- c_w7kob6：pitfall→eval 半自动管道 seed：把 docs/pitfalls.md 条目转成 eval 用例草稿
- c_3k3o6r：control-plane M1b：seek serve 命令骨架 + config 接线——空平面可启停
- c_x7fgcv：control-plane M2：路由与指令层——/bind /projects /switch /new /cancel + DirectAt 过滤
- c_twt3el：减法机制 L2/L3 设计稿：删代码与缩文档流程（feature-slim-loop.md）
- c_vt7fiw：减法机制 L1：weight.sh 体重秤 + docs/budget.json 预算清单（只读只报）
- c_rtjjsl：减法机制 L1：首轮 deadcode/unused 扫描产出瘦身候选清单（只报不删）
- c_rewul5：SEEK_ACP_YOLO 无头权限开关：ACP 模式 flag 全不解析、默认 Deny 拒 bash（paseo provider 前置缺口 ②）
- c_vv2mde：seek 车道首单验收：叙事稿 paseo 线状态同步 + ACP 无头 provider  pitfalls 条目
- c_mvfi4i：SEEK_ACP_YOLO 车道接 IsRemoteMutating bash guard：yolo 只放本地、照堵远程变更（PRD §3.3 同船项）
- c_rjg6hs：ACP 无头车道跳过 cron auto-tick（PRD M1 同船项）
- c_ur7bup：编排层差距盘点与设计：后台子代理/类型化 agent/模型路由/hooks 面（feature-orchestration.md）
- c_23nhhr：验收：seek 车道冒烟——docs/ 下新建 tmp-paseo-acceptance.md 写一句话并 commit
- c_4c6dar：paths.ProjectID 符号链接敏感：定口径（规范路径约定 vs 内部归一）
- c_ajavza：首批真删（每版一刀第 1 刀）：deadcode 首扫 4 个零引用导出符号删除
- c_j5qzb7：skill-distill 实现一期：/distill 扩展——会话→SKILL.md 草稿落 .drafts/（人审闸，自动路径不做）
- c_qucxtu：web MVP 两小时版：浏览器聊天驱动 seek——webfrontend 适配器 + embed 单页 + serve 接线
