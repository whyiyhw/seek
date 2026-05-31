# seek Code Review — 结构化代码审查 / Structured diff review

seek 内置了 **code-review 能力**——对工作区变更或分支差异进行结构化审查，发现正确性 bug 和重构机会。支持 `quick` 到 `max` 四档检查深度。

> seek has built-in code review — a structured analysis of working-tree changes. Four effort levels from `quick` (precision-first, blocking-only) to `max` (exhaustive, inter-file).

内置 skill 方法论与 effort framing 见 [`internal/skill/builtin/code-review.md`](../internal/skill/builtin/code-review.md)；设计决策见 [`docs/prd/feature-code-review.md`](prd/feature-code-review.md)。

---

## 1. 快速开始 / Quick start

```bash
# 审查当前工作区变更（quick 级别，默认）
/code-review

# 或者等价的快捷方式
/review

# 指定审查深度
/code-review thorough

# 审查与某个分支的差异
/code-review high main

# 高深度 + 自动修复
/code-review max --fix
```

---

## 2. 审查深度 / Effort levels

| 级别 | 命令 | 说明 | 适用场景 |
|------|------|------|---------|
| **quick** | `/code-review quick`（或 `/review`） | 只报高置信度阻塞 bug。简洁，几秒钟出结果 | 修了一个小问题，想快速确认没引入新 bug |
| **medium** | `/code-review medium` | 中等深度。阻塞 bug + 明显的代码异味 | 日常 commit 前检查 |
| **high** | `/code-review high` | 深入检查。跨文件数据流、并发问题、错误处理遗漏 | 拉取请求（PR）前自审 |
| **max** | `/code-review max` | 穷举。包括长尾设计问题、性能隐忧、测试覆盖盲区 | 关键模块重构、上线前最后一道防线 |

> **默认**：`/code-review`（无参数）= `/code-review medium`

---

## 3. 工作模式 / Work modes

### a) 审查 + 输出（`--comment`）

审查结果输出到对话中，供你阅读：

```bash
/code-review high --comment
```

输出格式：

```
📋 Code Review: working tree changes
   Effort: high  |  Files: 3  |  +42/-7

🔴 BLOCKER (1)
  • services/user.go:132 — nil pointer dereference on `user.Profile`
    when Profile is nil after partial update. Reproduce: PATCH
    /users/:id without a profile field.

🟡 WARNING (2)
  • services/user.go:88 — unused parameter `ctx` in verifyEmail
  • models/user.go:45 — exported field `EmailVerified` missing doc comment

⚪ INFO (1)
  • handlers/user_test.go:12 — test uses fixed user ID; consider
    t.Name() + random suffix for parallel safety
```

### b) 审查 + 自动修复（`--fix`）

审查发现问题后，自动通过 plan-mode 修复：

```bash
/code-review medium --fix
```

流程：
1. 运行 code-review 识别问题
2. 汇总问题列表
3. 通过 propose 路径逐个修复（每个修复一步，你可在 approve 前 review）

修复粒度由 effort 控制——`quick --fix` 只修阻塞 bug，`max --fix` 修所有可自动修的问题。

### c) 分支对比 / Branch diff

```bash
# 对比当前分支与 main
/code-review high main

# 对比与指定分支
/code-review medium feature-branch

# 同时审查 + 修复跨分支差异
/code-review max main --fix
```

---

## 4. 审查范围 / What's reviewed

- **git working tree** 中的未提交变更（默认）
- **跨分支差异**（指定分支参数时）
- **文件范围**：所有变更文件。包括新增、修改、删除
- **语言无关**：对 Go、TypeScript/JavaScript、Python、Rust、Markdown、YAML 等都有意义

审查关注点（按 effort 级别递增）：

| 类别 | quick | medium | high | max |
|------|-------|--------|------|-----|
| 空指针 / nil dereference | ✅ | ✅ | ✅ | ✅ |
| 未处理错误 | ✅ | ✅ | ✅ | ✅ |
| 资源泄漏（锁、文件句柄） | — | ✅ | ✅ | ✅ |
| 并发安全（data race 模式） | — | ✅ | ✅ | ✅ |
| 上下文超时传递 | — | — | ✅ | ✅ |
| 日志安全（敏感信息） | — | — | ✅ | ✅ |
| API 向后兼容 | — | — | ✅ | ✅ |
| 测试覆盖盲区 | — | — | ✅ | ✅ |
| 性能隐忧（N+1、重复分配） | — | — | — | ✅ |
| 设计一致性问题 | — | — | — | ✅ |

---

## 5. 内部实现 / How it works

```
/review 或 /code-review
     │
     ▼
  gatherChangedFiles() — git diff 采集变更
     │
     ▼
  codeReviewPrompt() — 构建审查 prompt（含变更内容 + effort framing）
     │
     ▼
  Agent.Prompt() — 提交给模型执行审查
     │
     ├── [--comment] → 输出到对话
     │
     └── [--fix] → propose 路径 → 每项修复一步 → approve 后执行
```

内置 skill 文件 `internal/skill/builtin/code-review.md` 包含了审查方法论和 effort 定义，随 seek 二进制发布，无需安装。

---

## 6. 与 plan-mode 的关系 / Integration with plan-mode

`--fix` 模式复用了 v2 plan-mode 的 propose 路径：

1. code-review 生成问题列表
2. 每个问题作为一个 step 进入 propose 的 task-list
3. 你在 approve 前可以调整（删除不想修的步骤、调整修复顺序）
4. 进入 execute 后模型逐一修复
5. 已完成但不满意可以用 skip 跳过

---

> **下一步**：查看所有内置技能 → `seek skill list`
> **参考**：审查方法论全文 → `internal/skill/builtin/code-review.md`
