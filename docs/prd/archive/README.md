# Archived PRDs

Designs that **were superseded before / after delivery** and no longer reflect the project's direction, but are kept for historical context: why a particular path was rejected, what alternatives were considered, and the design推演 that's still cited by the继任 PRD.

These are NOT roadmap items, NOT shipped specifications, and NOT planning surface. They live here so future-us doesn't re-design the same dead branch from scratch.

| 文件 | 取代它的 PRD | 归档理由 |
|---|---|---|
| [`feature-plan-tasklist.md`](feature-plan-tasklist.md) | [`../feature-plan-mode.md`](../feature-plan-mode.md) | Scope 误判——只覆盖了"执行追踪可视化"，错过 plan 模式核心价值在确认门（ANALYZE → PROPOSE → APPROVE）。继任 PRD 把 task-list 面板 explicitly 推迟到 v2 后由数据驱动决定；当前 plan 模式以工作流闭环 ship，未做面板。 |

## 政策

- 归档不是删除——`git log` + `git blame` 不变；inbound 链接更新到 `archive/<file>.md`
- 不在归档内做新内容——若需要进一步设计，新建 `feature-<topic>-vN.md`
- 归档文件**保留其原状态 banner**（"已废弃 / SUPERSEDED"）—— 读者落到文件本身时仍能立刻看到弃用原因
