# 更新日志（CHANGELOG）

> 索引，不放细节：一行一卡 = 卡片 ID + 一句话；细节住 PRD / `git log --all --grep c_xxxxxx`。
> 本段只记「已进 main、未部署」，部署后整段清空。预算见 `docs/budget.json`（frozen：追加须同船缩等量）。

## 已进 main、未部署

- c_bnjrw5：acp D-1：session/new cwd 生效（AnchorSession + Reanchor）
- c_7hudp6：control-plane 前置三项契约坐实（session id / print 权限 / ACK）
- c_xcy7ff：control-plane M1a：类型层 + bindings store
- c_u7w55t：GLM 订阅车道：openai-compatible + 工具循环冒烟
- c_seuoef：control-plane M3：执行器（子进程 + JSONL + per-Conv 串行自愈）
- c_3adk2z：skill 自动蒸馏设计稿（feature-skill-distill.md）
- c_n2k6qi：dsh 事件溯源适用性评估（notes-event-sourcing-adoption.md）
- c_evvow4：memory 与 skill 联动盘点（notes-memory-skill-gap.md）
- c_xelskv：control-plane M6a：serve 生命周期（扇入 / drain / build-tag）
- c_nap4wk：control-plane M7a：guide-serve + Telegram 纸面验证
- c_odyzbq：comparison.md 降级为参考 + 平台化验收口径
- c_27i3zi：paseo provider 设计稿（feature-paseo-provider.md）
- c_uh2x4q：permission isWithin 符号链接 CWD 误判 outside
- c_hw47pe：pricing 支持 glm 车道计价（glm-5.3 三口价）
- c_7krtrc：eval 扩容：三项契约锁成回归用例
- c_w7kob6：pitfall→eval 半自动管道 seed
- c_3k3o6r：control-plane M1b：seek serve 骨架 + config 接线
- c_x7fgcv：control-plane M2：路由指令层（/bind /projects /switch /new /cancel）
- c_twt3el：减法机制 L2/L3 设计稿（feature-slim-loop.md）
- c_vt7fiw：减法机制 L1：weight.sh 体重秤 + docs/budget.json
- c_rtjjsl：减法机制 L1：deadcode 首轮候选清单（只报不删）
- c_rewul5：SEEK_ACP_YOLO 无头权限开关
- c_vv2mde：paseo 车道首单验收：narrative 同步 + ACP 无头坑
- c_mvfi4i：SEEK_ACP_YOLO 接 IsRemoteMutating bash guard
- c_rjg6hs：ACP 无头车道跳过 cron auto-tick
- c_ur7bup：编排层差距盘点（feature-orchestration.md）
- c_23nhhr：验收：车道冒烟建 tmp-paseo-acceptance.md
- c_4c6dar：paths.ProjectID 符号链接敏感口径
- c_ajavza：首批真删（每版一刀第 1 刀）：4 个零引用导出符号
- c_j5qzb7：skill-distill 一期：/distill + .drafts/ 草稿区
- c_qucxtu：web MVP：浏览器聊天驱动 seek（webfrontend + embed）
