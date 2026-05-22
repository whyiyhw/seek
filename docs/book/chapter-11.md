# 第 11 章：M5.3 — Skill 系统与 `AGENTS.md` 自动加载

> 对应代码：`internal/skill/`、`internal/tools/skilltool/`、`internal/projectmd/`
> 起点：M5.2 之后，会话历史可分叉、可压缩、可持久化。但模型仍然只懂"通用编程"——它不知道你这个项目的命名约定，不知道你团队的"修 bug 必须先写测试"的规矩。
> 终点：项目根目录的 `AGENTS.md` 在启动时自动注入到 system prompt；用户/项目级的"任务说明书"以 Markdown 文件形式落盘，模型按需调用 `Skill` 工具拉取内容。

---

这一章讲两个看起来不同、其实是同一类问题的子系统：**怎么把"用户/项目特定的指令"喂给模型**。

- **AGENTS.md**：一份"项目宪法"——这个 repo 的约定、跳坑指南、不要做什么。**每个会话强制注入**，是 system prompt 的一部分。
- **Skill**：一组"任务说明书"——遇到 X 类问题，按 Y 步骤做。**按需注入**，模型自己决定什么时候用。

两个看起来重叠，但有清晰的分工。先讲 Skill（更结构化、有趣的设计决策更多），再讲 AGENTS.md。

---

## 11.1 Skill 解决的问题

考虑一个真实场景：你的团队规定，"修 bug 必须先写一个**复现该 bug 的测试**，确认它 fail，再写 fix，确认测试通过"。

这条规矩在哪里告诉模型？三个朴素选择都不对：

**塞 system prompt**：所有会话不论问什么都带上这条。聊一个跟测试毫无关系的问题（比如 "解释一下 SSE 协议"）也要付这个上下文成本。多团队多约定时 system prompt 膨胀。

**用户每次手动复述**：行不通——用户用 seek 就是想少打字。

**写在 README 里指望模型读**：模型不会主动去读 README，除非你告诉它有这么个文件 + 它现在该读。

**Skill 系统是第四个选择**：把这条规矩写在一个 Markdown 文件里（`fix-bug-first-test.md`），给它一个名字和一句"何时使用"的描述。seek 启动时把所有 skill 的**清单**（name + 一句话描述）注入 system prompt——只有清单，没有正文。模型看到清单后自己决定："用户在让我修 bug——我应该调用 `Skill(name='fix-bug-first-test')` 把详细步骤拉过来。"

**清单很短**（一个 skill 一行），可以堆 50 条不破缓存预算。**正文按需拉取**（通过工具调用），只在真要用时进上下文。这个分工是整个系统的核心。

---

## 11.2 文件格式：Markdown + 简化的 frontmatter

一个 skill 长这样：

```markdown
---
name: fix-bug-first-test
description: Use when the user asks to fix a bug. Reproduces the bug as a failing test first, then implements the fix.
---

# Bug fix protocol

1. Read the bug report. If unclear, ask for the exact failing input or stack trace.
2. Identify the smallest test case that reproduces it. Write it as a failing test...

(rest of the markdown body)
```

YAML frontmatter + Markdown body——Jekyll/Hugo/Obsidian/Claude Code 用的都是这套格式。它已经是社区事实标准了，没必要发明新的。

### 不用完整 YAML 库

`internal/skill/skill.go` 自己手写了一个**严格简化的 frontmatter 解析器**：

```go
func parseFrontmatter(lines []string) map[string]string {
    out := map[string]string{}
    for _, raw := range lines {
        line := strings.TrimRight(raw, "\r")
        if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
            continue
        }
        idx := strings.Index(line, ":")
        if idx <= 0 { continue }
        key := strings.TrimSpace(line[:idx])
        val := strings.TrimSpace(line[idx+1:])
        // 剥除外层引号
        if len(val) >= 2 &&
            ((val[0] == '"'  && val[len(val)-1] == '"') ||
             (val[0] == '\'' && val[len(val)-1] == '\'')) {
            val = val[1 : len(val)-1]
        }
        out[key] = val
    }
    return out
}
```

只支持 top-level `key: value`，不支持嵌套 map、列表、多行 block scalar。理由就一条：**PRD 规定 skill 的 frontmatter 只用 `name` 和 `description`**。把完整 YAML 拉进来意味着引入 `gopkg.in/yaml.v3` 这个依赖（七位数行代码，编译变慢，CVE 面增大），换来的能力 0% 使用。

注意 `idx <= 0` 而不是 `idx < 0`——`<= 0` 拦截了"行以冒号开头"的情况（无 key 名），这种数据是错的，跳过比静默接受好。

### 容忍 BOM 与前导空行

文件第一行不一定是字面的 `---`：

- Windows 编辑器（Notepad、某些 VS Code 配置）会在 UTF-8 文件开头加 BOM（`﻿`）
- 用户从 Web 复制粘贴时容易带上前导空行

`Parse` 先两步清洗：

```go
text := strings.TrimPrefix(string(data), "﻿")  // 去 BOM
trimmed := strings.TrimLeft(text, "\n\r ")          // 去前导空白
if !strings.HasPrefix(trimmed, "---") {
    return nil, fmt.Errorf("skill %s: missing frontmatter ...")
}
```

清洗发生在 Go 端，**用 `﻿` 转义而不是字面 BOM**。这是一个 Go 特定的坑：

> **坑 #14：源码里字面 UTF-8 BOM 是编译错误**
>
> Go 的 scanner 只允许 BOM 出现在源文件**最开头**（兼容某些工具产出的文件）。任何其他位置——包括 string literal 里——都会被报为 `illegal byte order mark (syntax)`。
>
> 写代码时如果直接从一份带 BOM 的样本里 copy "去掉这个字符" 的逻辑，你会粘到一个不可见的 BOM，编译失败，错误信息指向那一行但**你肉眼看不到那个字符**。
>
> 规则：源码里需要任何不可打印字符——BOM、零宽空格、特殊 unicode——一律写 `\uXXXX` 转义。"我从浏览器复制粘贴" 是 footgun。

### 名字必须是 kebab-case

```go
var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
```

`fix-bug-first-test` ✅，`FixBug` ❌，`fix_bug` ❌，`1-test` ❌。

理由：skill 名字会作为参数传给 `Skill` 工具，而工具参数是一个 JSON 字符串字段。我们希望这个字段只有**一种规范形式**——模型不会因为大小写差异、下划线/连字符差异而反复猜。kebab-case 是 URL、CLI 子命令、npm 包名的事实标准，最不容易和别的命名风格冲突。

`description` 是单行——多行 description 在 manifest 里展开会让 system prompt 视觉上糟糕，限制成一行强迫作者把 "何时用" 想清楚。

---

## 11.3 4+1 层优先级扫描

Skill 文件可以放在 5 个地方：

| 优先级 | 路径 | 用途 |
|---|---|---|
| 1（最高） | `<project>/.seek/skills/` | seek 原生项目级 skill |
| 2 | `<project>/.claude/skills/` | 兼容 Claude Code 的项目级 skill |
| 3 | `$XDG_CONFIG_HOME/seek/skills/`（默认 `~/.config/seek/skills/`） | seek 原生用户级 skill |
| 4 | `~/.claude/skills/` | 兼容 Claude Code 的用户级 skill |
| 5（fallback） | `go:embed`（编译进二进制） | 内置 skill |

**为什么 4 层而不是 2 层（项目+用户）**：seek 把项目级和用户级各拆成两个目录，是为了**兼容 Claude Code 用户的迁移**。已经在用 Claude Code 的人，`.claude/skills/` 里大概率已经有积累——直接读，不要逼用户搬家。原生的 `.seek` 优先级更高，新写的 skill 鼓励放在那里，但旧的 `.claude` 不强制迁移。

**项目 > 用户 > builtin**：项目级覆盖用户级，用户级覆盖内置。这跟 git config、ESLint、几乎所有配置类工具的层级一致——具体的覆盖通用的。

### first-writer-wins

```go
// internal/skill/skill.go
func (s *Set) Add(sk *Skill) bool {
    if _, exists := s.byName[sk.Name]; exists {
        return false  // ← 先到先得
    }
    s.byName[sk.Name] = sk
    return true
}

// loader 按优先级顺序遍历
for _, d := range dirs {
    loadFromDir(set, d.path, d.label)
}
loadEmbedded(set)  // builtin 最后
```

按优先级**从高到低**遍历，第一个 Add 进去的赢。同名的低优先级 skill 静默被遮蔽。这种语义和直觉一致——**用户在项目里写的就是终态**，他不会想"为什么我写的 skill 被一个内置版本盖掉了"。

### 同目录内的确定性顺序

```go
sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
```

每个目录内按文件名排序。这样在"两个文件碰巧给了同一个 skill name" 这种**应该不发生**的情况下，赢家是固定的（字典序更前的）。测试不会因为目录读取顺序非确定性而 flake。

这是一条非常便宜的纪律性约定：**任何"理论上不该发生"的状态都给一个确定行为**。让 bug 变成可复现而不是 heisenbug。

---

## 11.4 按需注入：manifest vs body

第 11.1 节提过的核心分工，这里展开为什么是这样：

```go
// internal/skill/skill.go
func (s *Set) Manifest() string {
    if s.Len() == 0 { return "" }
    var b strings.Builder
    b.WriteString("# Available skills\n\n")
    b.WriteString("Each skill describes when it should be applied. To use one, call the `Skill` tool with the skill's `name`...\n\n")
    for _, sk := range s.List() {
        fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
    }
    return b.String()
}
```

manifest 长这样（典型）：

```
# Available skills

Each skill describes when it should be applied. To use one, call the `Skill` tool with the skill's `name`; the tool returns the skill's instructions, which you should then follow.

- fix-bug-first-test: Use when the user asks to fix a bug...
- review-pr: Use when reviewing a PR diff...
- dual-model: Use for complex multi-step tasks needing planning then execution.
```

10 个 skill ≈ 1 KB，可以舒服地塞进 system prompt 不污染缓存前缀。

如果把每个 skill 的 body 都塞进去呢？一个典型 skill body 500-2000 字，10 个就是 10-20 KB。这本身不是问题（DeepSeek V4 有 1M context），**但每次用户新增/编辑/删除任何一个 skill，整个 system prompt 的字节都变**——缓存前缀字节不再一致，**前缀缓存命中率立刻归零**。本来命中率能跑到 70%+ 的会话，因为加了一个新 skill 全部变 miss。

按需注入避开这个问题：manifest 短且稳定（除非真的加/删 skill，否则字节不变），body 只在模型主动调 `Skill` 工具时进入历史（且历史是按消息追加，不影响 system prompt 的缓存前缀）。

> 这是一个典型的"看起来更简洁的设计反而代价更高"的例子。直觉是"把所有可用内容都给模型，让它自己选"；现实是"给得多 → 缓存断 → 每次请求都全价"，比少给信息但保持缓存稳定贵得多。

### `Skill` 工具：模型主动取

```go
// internal/tools/skilltool/skilltool.go
const toolName = "Skill"

const description = "Fetch the instructions for a skill listed in the Available skills section..."
```

工具名故意用 TitleCase，和 `read` / `grep` / `edit` 这些小写工具在 trace 里视觉上区分——一类是"操作文件系统"，另一类是"获取指令"，语义级别不同。

Execute 里有个值得记的细节：

```go
sk := t.set.Get(a.Name)
if sk == nil {
    var names []string
    for _, x := range t.set.List() {
        names = append(names, x.Name)
    }
    return "", fmt.Errorf("Skill: %q not found. Available: %v", a.Name, names)
}
```

模型有时会拼错 skill name（typo、记错了 manifest 上的字符）。如果只返回 "not found"，下一轮模型只能盲猜重试。把 available 列表一起返回，模型可以**当轮**纠正（看到列表里类似的名字 → 改正 → 重试）——少一轮 round trip。

这跟第 5 章 `UnmarshalStrict` 把 "valid fields" 写进错误信息是同一个原则：**让错误自带恢复信息**，把"哪里错"和"应该怎么办"放在一起返回。

---

## 11.5 内置 skill：`go:embed` 保持单二进制

```go
//go:embed builtin/*.md
var builtinFS embed.FS
```

内置 skill 直接编进二进制。`builtin/dual-model.md` 和 `builtin/go-test-runner.md` 是 seek 本身预置的两个示例 skill——分发时不需要单独的资源文件，`go install` 一条命令拿到所有东西。

加载时 `embed.FS` 当一个普通文件系统读：

```go
func loadEmbedded(set *Set) (int, []error) {
    entries, _ := builtinFS.ReadDir("builtin")
    for _, e := range entries {
        data, _ := builtinFS.ReadFile("builtin/" + e.Name())
        sk, _ := Parse(data, "builtin:"+strings.TrimSuffix(e.Name(), ".md"))
        set.Add(sk)
    }
    // ...
}
```

`Source` 字段标成 `"builtin:dual-model"` 而非文件路径——`/skills` 列出时用户能看到"这条是内置的"，无歧义。

`embed.FS` 是 Go 1.16+ 的标准库特性，零外部依赖。这跟 `pkg/deepseek` 不引第三方库是同一个理由：每个能用 stdlib 解决的事情都用 stdlib，依赖只在没有 stdlib 替代时才引。

---

## 11.6 `AGENTS.md` 自动加载

讲完 Skill，来看另一半：**项目宪法**。

```go
// internal/projectmd/projectmd.go
const Filename = "AGENTS.md"
const maxAscend = 5
const maxBytes  = 64 * 1024
```

启动时从 cwd 向上找 `AGENTS.md`，找到第一个就停，最多向上爬 5 层。文件内容 ≤ 64 KB 全注入，超出截断。

注入点是 system prompt 的尾部：

```go
// cmd/seek/main.go 的 RebuildAgent closure
sp := fmt.Sprintf(systemPromptTpl, abs, policy.Yolo())
if section := projMD.Section(); section != "" {
    sp = sp + "\n" + section
}
if manifest := skills.Manifest(); manifest != "" {
    sp = sp + "\n" + manifest
}
```

`projMD.Section()` 返回：

```
# Project instructions (from /Users/whyiyhw/code/seek/AGENTS.md)

<file contents>
```

文件路径写进 section 标题，让模型知道这些指令的来源——和"用户消息"区分开。

### 为什么是 `AGENTS.md` 而不是 `CLAUDE.md` / `.cursorrules`

各家 AI 编程工具都有自己的"项目指令文件"约定：
- Cursor: `.cursorrules`
- Claude Code: `CLAUDE.md`
- Aider: `.aider.conf.yml`（部分内容） + `CONVENTIONS.md`

`AGENTS.md` 是社区在 2025-2026 年逐步收敛出来的中立命名——**不绑定具体工具**。Cursor 和 Aider 在它们的最新版本里都支持读 `AGENTS.md`，Claude Code 用户也越来越多在 repo 里同时维护 `CLAUDE.md` 和 `AGENTS.md`（这个 repo 就是这么做的，两个文件内容几乎完全一样）。

seek 只读 `AGENTS.md`，不读 `CLAUDE.md` / `.cursorrules`，是有意的：

- **单一来源**：当两个文件存在且不一致时，应该读哪个？读 `AGENTS.md` 还是合并？合并冲突时谁赢？每个答案都增加复杂度，且每个答案都会让某些用户失望。
- **中立**：seek 不应该把"假装 Claude Code"作为目标。如果用户想让 seek 看到 `CLAUDE.md`，他可以创建一个 symlink（`ln -s CLAUDE.md AGENTS.md`）——成本低、显式、可逆。

这是一个"少做事，做对"的设计姿态。

### 向上爬到根目录

```go
for i := 0; i < maxAscend; i++ {
    candidate := filepath.Join(dir, Filename)
    data, err := os.ReadFile(candidate)
    if err == nil {
        return shape(candidate, data), nil
    }
    parent := filepath.Dir(dir)
    if parent == dir { break }   // 到文件系统根
    dir = parent
}
```

如果用户在 `<repo>/src/auth/internal/jwt/` 下启动 seek，向上爬 5 层到 `<repo>`，找到 `AGENTS.md`——和在 repo 根目录启动效果一样。

为什么限 5 层？让 seek 不会在深嵌套路径下扫到文件系统根。理论上更深一点也行，但 5 层覆盖了 99% 的真实项目结构，再深的项目极少见。

### 64 KB 截断

```go
if len(data) > maxBytes {
    r.Content = string(data[:maxBytes]) +
        fmt.Sprintf("\n\n…[truncated %d bytes; AGENTS.md is too large — keep it under %d KB]\n",
            len(data)-maxBytes, maxBytes/1024)
    r.Truncate = true
}
```

64 KB ≈ 一万英文字符。一份手写的项目指令几乎永远到不了——典型 `AGENTS.md` 在 1-10 KB。如果有人指错了文件（指向 `CHANGELOG.md` 这种自动生成的几 MB 大文件），截断让 seek 还能启动并打印一个明确的"你这文件太大"提示，而不是把 context window 全部填满。

截断行本身写得很明确——告诉用户多少字节被砍、应该控制在多少 KB 以内。让错误自带修复方法。

### 不在 `/reset` 时重新读

```go
// cmd/seek/main.go
RebuildAgent: func() (*agent.Agent, error) {
    // AGENTS.md is loaded once at startup and reused (re-reading on /reset would
    // surprise users who edit the file mid-session — we want the file's behaviour
    // to be "loaded at launch", not "hot-reloaded"; documented behaviour is
    // easier to reason about than clever).
    // ...
}
```

`/reset` 重建 agent 时**不重新读** `AGENTS.md`——用启动时缓存的版本。

不热加载的理由不是技术上做不到，是**行为可解释性**。如果热加载，用户在 session 中间编辑了 `AGENTS.md` 但忘了，`/reset` 之后模型行为变了，他得花十分钟才能搞清楚"为什么模型现在不按我之前期望的来"。"启动时加载，会话期间不变"是一句话能解释的规则。

这是和"什么时候自动同步配置"相关的一个普遍取舍——**便利 vs 可预测**。对小团队/单人项目，便利赢；对会议室里多人协作的 LLM 工具，可预测赢。seek 选择了可预测。

---

## 11.7 一个观察：Skill 和 AGENTS.md 的"按需 vs 强制"

回头看：
- **AGENTS.md = 强制注入**，每个会话都带，体积有硬上限
- **Skill = 按需注入**，manifest 进 system prompt，body 按需拉

两者的根本区别在"指令的覆盖面"：
- AGENTS.md 是**关于这个 repo 的通用约定**——任何一个会话都可能需要遵守（命名规范、提交格式、不要碰哪些目录）
- Skill 是**关于特定任务类型的步骤**——只在做那类任务时才相关（修 bug、review PR、写测试）

如果你给一个新功能写 prompt 工具，先想清楚它属于哪一类：是"任何会话都要遵守的"还是"特定任务才用到的"。两类塞错地方都会让 context 或行为出问题——AGENTS.md 当 skill 用会让每个会话都背着不相关的步骤说明；skill 当 AGENTS.md 用会让模型经常忘记调用，错过该用的时机。

---

## 本章小结

- Skill = "任务说明书"，AGENTS.md = "项目宪法"。看起来重叠，但服务的"指令覆盖面"不同
- Skill 文件 = Markdown + 极简 YAML frontmatter（只 `name` + `description`）；name 必须 kebab-case 以避免多种规范形式
- 4+1 层优先级扫描：项目 `.seek` > 项目 `.claude` > 用户 `.config/seek` > 用户 `.claude` > `go:embed` 内置；first-writer-wins
- 同目录内文件名排序，让"不该发生但发生了"的同名冲突有确定胜者
- manifest 进 system prompt，body 等 `Skill` 工具调用时才返回——避免新增/编辑 skill 把整个缓存前缀打掉
- Skill 工具的 missing-name 错误里**附带 available 列表**，让模型当轮纠正（**第 5 章 `UnmarshalStrict` 同款原则**）
- 源码里需要不可打印字符一律 `\uXXXX` 转义；字面 BOM 是编译错误（**坑 #14**）
- `AGENTS.md` 是社区收敛出的中立命名，seek 只读它一个——单一来源比"试图兼容所有约定"简单
- 启动时一次性加载 `AGENTS.md` 不热加载：可预测 > 便利

下一章进入 M5.4 — MCP client。我们会看到 JSON-RPC 2.0 over stdio 是怎么 spawn 一个子进程并和它对话的、外部 server 的工具如何动态注入到 Agent 的工具表里、以及 `filesystem` MCP server 端到端跑通的样子。

---

*对应 commit：`2c53248`（Skill loader + 多优先级 + Skill 工具）、`dd1bcd0`（内置 dual-model skill）、`e8894ff`（AGENTS.md 自动加载）。运行 `go test ./internal/skill/... ./internal/projectmd/... ./internal/tools/skilltool/...` 验证。*
