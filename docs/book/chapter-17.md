# 第 17 章：M8 — Skill 生命周期管理

> 对应代码：`internal/skill/`(目录包扩展)、`internal/skillmgr/`、`internal/skillstats/`、`internal/skillcli/`、`internal/tools/skilltool/`(stats 集成)、`cmd/seek/skill_*.go`、TUI `/skill` 命令
> 起点：第 11 章。Skill 是"把一个 markdown 文件丢进目录就生效"——零安装、零状态、和 Claude Code 生态零摩擦。这在自举阶段是对的。
> 终点：完整的"装—用—看—卸"闭环。`seek skill install` 支持 local / git / https 三种来源、`seek skill list/status/stats` 查询、`seek skill update` 重新拉源、`.install.json` sidecar 记录怎么来的、`.stats.jsonl` 记录被用过几次、TUI `/skill` 镜像同一套子命令、`seek skill create` 脚手架新 skill。

---

## 17.1 v0 Skill 的三个缺口

v0 (M5.3) 的 Skill 模型很美:**一个 markdown 文件丢进目录就生效**。startup 时全扫一次, 加入 manifest, 模型按需 `Skill(name=...)`。100 行代码兜底全部功能, 测试只占一页, AGENTS.md 兼容、Claude Code 兼容、迁移成本为零——典型的"少做事, 做对"的胜利。

但随着 skill 数量增长、外部贡献到来, 三个问题浮出水面:

1. **没有"安装"**。用户从 GitHub 复制粘贴一段 markdown, 存到哪个目录、文件名叫什么、是否会被 loader 看到, 全靠经验。要换电脑、要给同事推荐时, 没有可复制的命令。
2. **没有"被调用过"的痕迹**。`/skills` 只显示"哪些被加载了", 回答不了"哪些真的被模型用过"、"上次什么时候用过"、"哪个最常用"。Skill 作者无法知道自己写的东西是否在帮人;用户无法识别哪些是死代码。
3. **没有"生效保障"**。装错目录、frontmatter 写错、name 冲突被遮蔽——目前只在启动 banner 一行汇总, 事后无从回溯。`/skills` 不告诉你 `foo` 是从哪个文件加载的、是否被同名 skill 遮蔽。

PRD v2 把这三件事补成一套子系统。v0 的设计原则不变——**skill 是注入到模型上下文的 markdown, 不是远程能力**——本章只补齐生命周期和可观测性。

---

## 17.2 三个并行的变化

PRD v2 §2 把变化压成三块, 各自可独立上线, 合起来构成闭环:

```
┌─────────────────────────────────────────────────────────────────┐
│  1. Skill 升级为目录包(兼容 Anthropic Agent Skills 规范)        │
│     foo.md → foo/SKILL.md + 可选 references/ examples/ scripts/  │
│     单文件 .md 永远兼容; 没有 version/license 等扩展字段          │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  2. seek skill 子命令族                                         │
│     install / list / status / uninstall / update / stats / create│
│     CLI 和 TUI slash 镜像同一份能力 (/skill install ...)         │
└─────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────┐
│  3. 调用统计(轻量、本地、append-only)                            │
│     ~/.seek/skills/.stats.jsonl                                  │
│     每次 Skill(name=...) 调用写一行(ts,name,session,project,…)   │
└─────────────────────────────────────────────────────────────────┘
```

三块共用一个目标:**让"哪些 skill 在工作"从模糊感觉变成可查的事实**。

---

## 17.3 目录包: 与 Anthropic Agent Skills 规范对齐

第 11 章讲过格式变化, 这里展开"为什么是这个形态"。

v2 不发明格式——直接采用 Anthropic Agent Skills 的目录约定:

```
my-skill/
├── SKILL.md            # 必需。frontmatter + body
├── references/         # 可选约定。SKILL.md body 用 [link](references/foo.md) 引用
├── examples/           # 可选约定。代码片段、示例数据
├── scripts/            # 可选约定。SKILL.md body 引用的辅助脚本
├── assets/             # 可选约定。图片等
└── README.md           # 可选。给人看, loader 不读
```

这意味着 GitHub 上任何符合 Claude Code skill 规范的仓库, 用 `seek skill install <repo>` 都能**零转换**装上。`my-skill/scripts/foo.sh` 的执行路径和 Claude Code 一致:模型读到 SKILL.md 里的 "Run `scripts/foo.sh`" 指令 → 调 `bash` 工具 → 用户走 permission y/N inline 审批。seek **不**为 `scripts/` 提供任何特殊执行管线——同一份 SKILL.md 在 Claude Code 和 seek 上行为相同。

### Loader 同时支持两种形态

```go
// internal/skill/loader.go (示意)
//  ~/.seek/skills/foo.md         → TypeSingleFile
//  ~/.seek/skills/bar/SKILL.md   → TypePackage
//  embed: builtin/baz.md         → TypeBuiltin
```

扫描目录时, 对每个 entry 看:
- 是文件 + `.md` 后缀 → 单文件 skill
- 是目录 + 含 `SKILL.md`(或 `skill.md` 大小写 fallback) → 目录包 skill

同目录下 `foo.md` 和 `foo/SKILL.md` 同名同时存在——如果真的发生(不该), first-writer-wins (第 11 章的规则), 后者被记入 `Set.shadowed[name]`, `seek skill status foo` 会列出来。

> **macOS APFS 默认大小写不敏感, `SKILL.md` 和 `skill.md` 在同一目录下塌成同一个文件**(commit `b7d7996` 测试期间撞到):
>
> ```
> $ touch SKILL.md
> $ touch skill.md     # ← 实际上没有第二个文件, 是同一个 inode
> $ ls
> SKILL.md
> ```
>
> 直接写测试"两者并存, 大写应该赢"在 macOS 默认 FS 上会失败:第二次写覆盖了第一次, 文件系统层面只有一份。修法是 *在测试里探测 FS case sensitivity*:写 `FS_CASE_PROBE`, 读 `fs_case_probe`, 同一文件能读到就 `t.Skip`。教训:**跨平台 FS 测试要检测它依赖的属性, 而不是假设 Linux**——经典 4 件候选:大小写敏感、symlink、执行位、mtime 精度。

### v2 frontmatter 扩展但向前兼容

字段表已在第 11 章 §11.2 列过。这里强调一条架构性纪律:

```go
// internal/skill/skill.go
var recognisedFields = map[string]struct{}{
    "name":          {},
    "description":   {},
    "version":       {},
    "license":       {},
    "author":        {},
    "keywords":      {},
    "allowed-tools": {},
}

// Parse:未识别的 key 落进 Skill.Extra
for k, v := range fields {
    if _, ok := recognisedFields[k]; ok { continue }
    if sk.Extra == nil { sk.Extra = map[string]string{} }
    sk.Extra[k] = v
}
```

**未识别字段不丢, 进 `Extra`**。理由:Anthropic Agent Skills 规范是活的, 未来可能加 `category`、`min-claude-version`、`maturity` 等字段。"未识别 → 丢弃"会让从 Anthropic 装来的 skill 在 seek 这边丢失元数据;"未识别 → fail-load" 会让 seek 老版本无法装新 skill。**记录但不消费**是这种"对接活规范"场景的通用应对。`seek skill status` 把 Extra 中的字段都展示出来, 让用户知道 seek 看到了但还没处理。

---

## 17.4 安装来源:local / git / https 三条路径

`seek skill install <source>` 看 source 长什么样选择策略:

```
$ seek skill install ./my-skill/                       # 本地路径
$ seek skill install github.com/user/repo              # git URL
$ seek skill install github.com/user/repo@v1.2.0       # git URL + ref
$ seek skill install github.com/user/repo/sub          # git URL + subpath
$ seek skill install https://example.com/foo.tar.gz    # HTTPS tarball
```

`internal/skillmgr/skillmgr.go` 是入口, dispatch 到三个独立 backend:

### Local (M8.1a, commit `f56cd69`)

```
$ seek skill install ./my-skill/
  → cp -r ./my-skill/ ~/.seek/skills/my-skill/
  → 解析 my-skill/SKILL.md, 验 name / description
  → 写 .install.json: {type: "local", url: "./my-skill"}
```

最简单一档。本地路径意味着"作者自己开发中"——`update` 重新做一次 `cp -r`, 哪边新的拿哪边。

### HTTPS tarball (M8.1b, commit `9aa8f53`)

```
$ seek skill install https://example.com/foo.tar.gz --sha256 abc123...
  → curl 下载到 tmpfile
  → 校验 sha256(如果给了 --sha256)
  → 识别 .tar.gz / .zip, 解压到 ~/.seek/skills/foo/
  → 校验 SKILL.md 存在
  → 写 .install.json: {type: "https", url: ..., checksum_sha256: ...}
```

`--sha256` 是可选的, 但显式给了之后 update 也校验——意味着升级要么"开发者更新了 url, 给了新 sha256", 要么"refuse 因为内容意外变化"。tarball 是 content-addressable 的便宜版本:HTTPS 服务静态托管 + 校验码, 不需要 git 服务器。

### Git clone (M8.1c, commit `6865a64`)

```
$ seek skill install github.com/user/repo@v1.2.0
  → git clone --depth=1 --branch v1.2.0 https://github.com/user/repo
  → git rev-parse HEAD → resolved_commit
  → 若有 subpath: 只保留 repo/sub/, 其余删
  → mv to ~/.seek/skills/<name>/
  → 写 .install.json: {type: "git", url, ref, resolved_commit, subpath}
```

三件值得记:

1. **`--depth=1`** 配合 `--branch=<ref>` 显著减少 clone 体积——大多数 skill repo 不大, 但默认 full clone 会拉历史拖慢首次 install。
2. **`resolved_commit` 是 update 的核心**。git 的 ref 可以是 tag、branch、commit SHA。tag 可能被人 force-update, branch 推进, 只有 commit SHA 是不可变的——install 时记下当时的 resolved commit, update 时 `git fetch && git rev-parse origin/<ref>` 比对, 知道是否有新版本可拉。
3. **subpath** 让一个 repo 装多个 skill。`github.com/user/skills/foo` 和 `github.com/user/skills/bar` 装两次, 各拿一个目录, 没有"必须一个 repo 一个 skill"的强制。

---

## 17.5 `.install.json` sidecar:为什么不和 SKILL.md frontmatter 合并

每个被 `seek skill install` 装上的 skill 旁边有一份 sidecar:

```json
// ~/.seek/skills/foo/.install.json
{
  "schema_version": 1,
  "installed_at": "2026-04-30T10:22:00+08:00",
  "type": "git",
  "url": "https://github.com/user/repo",
  "ref": "v1.2.0",
  "subpath": "skills/foo",
  "resolved_commit": "a1b2c3d4..."
}
```

设计上的一个关键决定:**sidecar 是单独文件, 不合并进 SKILL.md frontmatter**。

合并看起来"更整洁":一个文件描述完整信息, 不需要管理两份。但实际不行:

- **SKILL.md 是作者维护的**, 跟随 upstream 同步。如果 install 信息塞 frontmatter, 每次 `git pull` 都会冲突(`installed_at` 一定不一样)。
- **`.install.json` 是 seek 写的, 用户不应该手动改**。混在一起意味着"用户能编辑的字段"和"seek 私有的字段"边界模糊, 出错时锅找不到。
- **作者发 skill 到外部时不该泄露本地路径**。如果 `installed_at` / `resolved_commit` 在 SKILL.md frontmatter 里, 作者忘了清就上传到 GitHub, 接下来每个装这个 skill 的人都把作者的 ts/path 复制过来——污染。

分离让两个文件**职责清晰、来源明确、清理简单**:`SKILL.md` 是 *作者* 的、`.install.json` 是 *seek 的*、要分享 skill 只需要打包 SKILL.md 和其他公开内容, sidecar 留下。

---

## 17.6 `seek skill update`:三种来源各自的"刷新"语义

```bash
$ seek skill update foo          # 只更新 foo
$ seek skill update --all        # 更新所有有 .install.json 的 skill
```

`update` 读 `.install.json` 知道这个 skill 是怎么来的, 然后:

| type | update 做什么 |
|---|---|
| `local` | 重新 `cp -r url -> skills/<name>/`(假设作者改了源码) |
| `https` | 重新下载 tarball, 校验 sha256 (若 install 时给了), 解压覆盖 |
| `git` | `git fetch` + `git checkout ref` + 更新 `resolved_commit` |
| 无 `.install.json` | 报错"无法 update unmanaged skill"——用户用 `cp` 装的, seek 不知道从哪拉 |

最后一条值得记: **不是所有 skill 都能被 update**。一个手动放进 `~/.seek/skills/` 的 skill 没有 sidecar, `update` 拒绝处理。这避免了"seek 自作主张去某处拉东西覆盖用户文件"——所有 update 行为必须有显式的 install-time 记录授权。

---

## 17.7 调用统计: `.stats.jsonl` 与 PIPE_BUF 单写原子性

每次模型调用 `Skill(name=foo)` 工具成功, `internal/tools/skilltool` append 一行到 `~/.seek/skills/.stats.jsonl`:

```json
{"ts":"2026-04-30T10:23:00+08:00","name":"foo","session_id":"a3f...","project_id":"c4a1...","model":"deepseek-v4-flash","provider":"deepseek"}
```

六个字段, 平面 schema, 一行一条 JSONL。

### 为什么 JSONL 而不是 SQLite / 自定义 KV

- **append-only 不需要 schema 迁移**。下一版需要新字段? 新写的行有, 旧行没有——`encoding/json` 解析 missing 字段为零值, 旧 row 也 *能读*。版本字段都不需要, 真正的 schema 改动用新文件名(`.stats.v2.jsonl`)。
- **grep/jq 直接可用**。用户想看"过去一周哪个 skill 最热", 一行 shell 解决:
  ```bash
  jq -r .name ~/.seek/skills/.stats.jsonl | sort | uniq -c | sort -rn
  ```
  这跟 SQLite 的"功能更强但门槛高几个档"完全相反——日常分析的 80% 用 jq 就够。
- **零外部依赖**。`pkg/deepseek` 零依赖原则一脉相承。SQLite 拉进 cgo, 跨平台编译变难;自定义 KV 是"再发明一个数据库"。

### 并发原子性的核心:一次 `write(2)`

stats 写入是个高频路径(每次 `Skill` 调用都写), 且可能在多 goroutine 下发生(TUI + 后台 hook + agent dispatch)。但 *没有锁*:

```go
// internal/skillstats/skillstats.go
func (w *Writer) Append(e Entry) error {
    raw, _ := json.Marshal(e)
    line := append(raw, '\n')
    if len(line) > 3072 {
        return fmt.Errorf("entry too large; JSONL contract relies on PIPE_BUF atomicity")
    }
    f, _ := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
    defer f.Close()
    _, err := f.Write(line)  // ← 单次 write(2)
    return err
}
```

依据是 POSIX 保证:**用 `O_APPEND` 打开的文件, 单次 `write(2)` 调用如果 payload < `PIPE_BUF`(macOS/Linux 是 4 KiB), 多进程/多线程并发写入不会交错**——内核把"原子定位到末尾 + 写入字节"作为一个原子操作。

所以纪律是:
- `json.Marshal` 先生成完整 `[]byte`(行内构造, 单线程)
- `+ '\n'` append 换行
- **一次 `f.Write(line)`**, 不分多次写
- payload 守门员(3 KiB 上限, 留 1 KiB 余量;stats 行典型 ~250 字节)

跟一个互斥锁相比这套方案的好处是:**跨进程也成立**——seek 多开几个进程同时往 stats 文件 append, 不打架。Go mutex 只在进程内有效, 不适合 stats 这种"用户可能开了多个 seek 窗口"的场景。

> **PIPE_BUF 这条不是"性能优化", 而是"无锁正确性的依据"**。任何 append-only JSONL 设计都应该问自己:行有没有上限? 写有没有可能分多次? 这两个答案不对, 数据就会以"半行 + 半行"的形态烂掉。skillstats 显式守这两条, 测试里有故意构造 >3 KiB payload 验证 reject 行为。

### Read 端容忍尾部破损

```go
// internal/skillstats/skillstats.go
// Read: 不完整的最后一行 silent skip
```

理由:并发 append + 进程被 kill = 文件末尾可能有一截写到一半的行。`Read` 用 `bufio.Scanner` 逐行解析, **解析失败的行跳过, 不返回错误**。"宁可少看一条记录, 也不能让一个破损的尾巴让整个统计 fail"——这是日志型存储的通用容忍纪律。

---

## 17.8 CLI / TUI 子命令族

`seek skill <verb>` 一套子命令, TUI 里的 `/skill <verb>` 镜像同一组(commit `87bd195` 把逻辑提到 `internal/skillcli`, 两侧共用):

| 命令 | 做什么 |
|---|---|
| `install <source> [--name X] [--sha256 X] [--force]` | 装一个 skill, 自动检测 source 类型 |
| `uninstall <name> [--purge-stats]` | 删除 `~/.seek/skills/<name>/`, 默认保留 `.stats.jsonl` 历史 |
| `update <name>` / `update --all` | 重新从 .install.json 记录的源拉 |
| `list [--source user|project|builtin]` | 表格:name, source, type, 调用次数, 上次使用 |
| `status <name>` | 单 skill 诊断:文件路径、shadowed 路径、frontmatter 字段、install 来源、调用 top-5 时间、按 model/provider 分布、token 估算 |
| `stats [--top N] [--since <duration>]` | 排行榜 |
| `create <name> [--description "..."] [--into <dir>]` | 脚手架, 生成 `<name>/SKILL.md` + references/ + README.md |

> **`status` 是 PRD v2 §7 验收 #5 的核心断言工具**。用户看到模型没在用某个 skill, 第一反应是"它装上了吗", 第二反应是"它被遮蔽了吗"。`seek skill status foo` 一条命令就回答:
>
> ```
> name:         foo
> source:       /Users/whyiyhw/.seek/skills/foo/SKILL.md (package, v1.2.0)
> shadowed-by:  <none>
> shadowing:    /Users/whyiyhw/.claude/skills/foo.md (single-file)
> frontmatter:  name=foo, description=..., allowed-tools=[read,edit]
> install:      git github.com/user/repo@v1.2.0 (resolved a1b2c3d4)
> calls:        12 over 30 days; last 2026-04-29 14:22 (deepseek-v4-flash)
> est tokens:   manifest 142, body 1820
> ```
>
> 没有 `status`, debug "为什么我的 skill 没生效"需要翻文件、对照 frontmatter 字段、看 manifest 输出, 至少 10 分钟。有了 `status`, 是一条命令。

### `seek skill create`(M8.7)

最后加上的一块, commit `0e6189f`。生成的 SKILL.md 模板:

```markdown
---
name: my-skill
description: |
  Use this skill when ...
version: 0.1.0
allowed-tools:
  - read
  - grep
---

# My Skill

## When to use this

(replace this paragraph with the precise conditions ...)

## Steps

1. ...
```

随附:
- `references/` 空目录 + 一行 README 说明用途
- `README.md` 模板(给人看, 不进 loader)

`--into <dir>` 默认 `<cwd>/.seek/skills/`(项目级), 可以指定其他位置。**项目级 vs 用户级**的选择是有意的: 新写的 skill 通常是为当前项目专用, 先在项目级跑通, 觉得好了再 `mv` 到 `~/.seek/skills/` 推到团队。

---

## 17.9 模型驱动的 skill 安装: `skill_fetch` / `skill_commit` 工具

M8 的核心是用 CLI 安装 skill: `seek skill install github.com/foo/bar`。但 seek 是一个 agent——用户更自然的姿势是告诉模型"装一个前端设计 skill", 让模型自己完成安装流程, 而不是退出对话去敲命令。

这就需要两个新工具:
- **`skill_fetch(source)`** — 下载技能包到临时目录并校验, 返回元数据预览
- **`skill_commit(staging_path, name, source, scope)`** — 把已验证的包从临时目录搬进正式位置

### 为什么拆成两步, 而不是一步"install"

理由跟 `read` 和 `grep` 分开一样:**在确认之前先预览**。如果 `skill_fetch` 直接安装, 模型连"这个 skill 是干什么的"都没看到就把文件写进了用户磁盘——一个名字叫 `clean-code` 但包含 `scripts/rm-rf.sh` 的恶意包就绕过了所有审查。

两步流程:
1. 模型调用 `skill_fetch(source)` → 拿到 `name`, `description`, 文件清单, 正文预览 (≈500字)
2. 模型 **读完预览后再决定**:值得装 → 调用 `skill_commit`;不值得 → 直接丢弃临时目录

这跟 `staged installation` 模式一致——先暂存, 再提交。

### scope 参数:用户级的隐私 vs 项目级的共享

`skill_commit` 的 `scope` 参数是最核心的设计决策, 也是**必须由用户选择, 模型不能默认**的字段:

- **`user` 级** (`~/.seek/skills/<name>/`): 只有当前机器、当前用户可用。私有的个人效率 skill、公司内部 skill 放这里
- **`project` 级** (`<cwd>/.seek/skills/<name>/`): 会 git 提交到仓库, 所有克隆这个项目的人都能用。团队的代码规范、项目特定的 review checklist 放这里

选择错误的后果严重:
- 把私密 skill 装到 project 级 = 不小心 git push 了内部工具
- 把团队共享 skill 装到 user 级 = 同事不知道有这条规则, 每个人都要手动装一次

所以 `skill_commit` 的 API 明确要求:**在调用前, 模型必须用 `ask_user` 工具让用户选择 scope**。这不是 UX 建议, 是 API 契约:

> "I'll use X — say so if you want Y"（当用户取消选择器时的 fallback 策略）

### 校验层: staging path 安全检查

`skill_commit` 收到路径后, 做三层校验:
1. 路径必须在 `os.TempDir()` 下, 且前缀匹配 `seek-skill-staging-`——防止模型传入任意路径
2. `name` 必须与 `skill_fetch` 返回的一致——防止模型"狸猫换太子"
3. `source` 原样传给审批提示——让用户在确认时看到"这个 skill 是从哪来的"

这些校验写在 `internal/tools/skillinstall/skillinstall.go` 里, 测试覆盖了正常路径和每层校验的绕过尝试。

### 与 CLI 的关系

`skill_fetch` / `skill_commit` 不是替代 `seek skill install`。它们走同一个底层 (`internal/skillmgr.InstallPackage`), 只是入口不同:

- **CLI**: 用户手动操作, 适合批量安装、脚本化
- **工具**: 模型自动操作, 适合自然语言工作流——"帮我装一个检查 Go 错误处理的 skill"

两条路径共享相同的安装逻辑、校验规则、scope 处理。CLI 不需要 `ask_user`(用户直接在命令行给了 scope), 工具必须走 `ask_user`(模型不知道用户的意图)。

---

## 17.10 一个观察:可观测性是 v0 到 v1 的换挡

回头看 v0 → v2 这条 skill 子系统的演进:

- **v0 (M5.3)**:"丢文件就生效"——零状态, 学习成本最低, 用户只需要懂 markdown
- **v2 (M8.x)**:install / update / status / stats——加上状态, 但状态都是 *外显的*

v2 没有把 v0 推翻——单文件 .md 永远兼容, 没有 "manifest" 或者 "registry" 这种集中状态——但它把"工具背后的事实"全部 *显式落盘*。`.install.json` 让"从哪儿来"可查;`.stats.jsonl` 让"被用过吗"可查;`shadowed` 字段让"为什么没生效"可查。

这跟第 13 章那条"诚实度"是同一个姿态:**当某件事重要到用户会问, 它就不该是隐式状态**。`/skills` 早期只显示 name 列表, 因为那时 skill 少, "我装了什么"心里有数。现在 skill 多了, "哪个真的有用"成了问题——可观测性必须跟上。

但 *方法* 上不发明轮子:JSONL + grep 比 SQLite 直观, sidecar 比合并 frontmatter 边界清晰, POSIX `O_APPEND` 比应用层锁简单。**复杂度长在功能上, 不长在机制上**——这是这一章里反复出现的姿势。

---

## 本章小结

- v0 的"丢文件就生效"模型在 skill 增长后有三个缺口:**没有 install / 没有调用痕迹 / 没有生效保障**, v2 补这三块
- 目录包 (`<name>/SKILL.md`) 直接对齐 Anthropic Agent Skills 规范, **零格式发明**, 单文件 .md 永远兼容
- frontmatter 扩展字段(version/license/author/keywords/allowed-tools)记录但不强制;**未识别字段进 `Extra`** 以兼容未来 Anthropic 规范追加
- macOS APFS 默认大小写不敏感, `SKILL.md` 和 `skill.md` 测试需要探测 FS case sensitivity 而不是假设 Linux(**v2 坑**)
- 三种安装来源:local(`cp -r`)、https(tarball + sha256)、git(clone + ref + resolved_commit + subpath)
- **`.install.json` 是 sidecar, 不和 SKILL.md frontmatter 合并**——分离让作者/seek 的字段边界清晰, 避免 git pull 冲突和上传时的"作者本地路径"泄露
- `update` 区分 managed(有 sidecar)和 unmanaged, 后者拒绝处理——避免 seek 自作主张去某处覆盖文件
- `.stats.jsonl` 用 JSONL + 一次 `write(2)` + payload < `PIPE_BUF`(3 KiB 守门)实现**无锁、跨进程的 append 原子性**——比 mutex 或 SQLite 简单, grep/jq 友好
- `seek skill status` 是诊断核心:source + shadowed + frontmatter + install + stats + token 估算一条命令出齐
- `skill_fetch` / `skill_commit` 提供模型驱动的安装流程, 两步分离实现"预览再确认", scope 参数**必须由用户通过 `ask_user` 选择**, 模型不能默认
- 整个 v2 不推翻 v0, 只是把"事实"从隐式变成 *外显的* 文件;**复杂度长在功能上, 不长在机制上**

---

至此, v0.3.x 的 Skill 生命周期闭环已经讲完。下一章我们进入 M9, 看 Plan Mode 如何将目前已累积的所有工具能力组合成交互式规划工作流, 彻底打开 v1.0 的大门。

代码会继续演进; PRD 目录 `docs/prd/` 是未来章节的种子。读完这本书的读者应该已经具备能力:**任意 commit 处暂停, 读 PRD + 代码, 自己写一章续作**。

---

*对应 commit:`b7d7996`(M8.0 目录包 + 4 形态 frontmatter)、`f56cd69`(M8.1a local install)、`9aa8f53`(M8.1b https tarball)、`6865a64`(M8.1c git clone)、`a362374`(M8.2 update + sidecar)、`be05864`(M8.3 stats writer + skilltool 集成)、`4b0c939`(M8.4a CLI install/uninstall/update)、`266d6f2`(M8.4b CLI list/status/stats)、`87bd195`(M8.5 抽出 internal/skillcli + TUI `/skill`)、`fc34bb2`(M8.6 端到端测试 + 文档)、`0e6189f`(M8.7 `seek skill create`)、`90731db`(M8.8 `skill_fetch`/`skill_commit` 模型安装工具)、`d86d509`(M8.8 fix scope 必须用户选择)。运行 `go test -race ./internal/skill/... ./internal/skillmgr/... ./internal/skillstats/... ./internal/skillcli/... ./internal/tools/skillinstall/...` 验证。详 PRD 见 `docs/prd/v2.md`。*
