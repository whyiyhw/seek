# transcript-stats fixtures

`scripts/transcript-stats --selftest` 的合成 transcript，**两文件覆盖四形态**。
只读断言，不调模型、不需要 key、不碰 `~/.seek` 真实数据（自测把文件复制到
mktemp 目录再跑，退出即删）。

| 文件 | schema | read 参数键 | 覆盖的形态 |
|---|---|---|---|
| `20260522-old-schema-path-key.jsonl` | 2 (`deepseek-chat`, 2026-05) | `path` | 翻页读（a.md 三页 `1→51→101`）、**完全重复调用**（b.md 同 offset 同参数读两次）、正常单读的对照、一行截断的损坏行（计入 `malformed_lines`） |
| `20261119-new-schema-file_path-key.jsonl` | 3 (`deepseek-v4-flash`, 2026-11) | `file_path` | 翻页读（c.md `1→51`）、`file_path` 单读（d.md，无重复）、**完全重复调用**（edit 同一 `old_string` 两次） |

## 为什么新 schema 样例用 `file_path`

真实数据里 `file_path` 从未出现在 `read` 参数中（见
`eval/transcript-baseline/2026-11-19.md` §「双键兼容」），read 工具今天也只收
`path`。fixture 刻意用 `file_path` 是为了让**兼容性断言**在两种世界里都有效：

- 今天：证明分析器认得第二个键（如果只认 `path`，这条样例整批变成
  `unresolved`，`auto` 与 `path-only` 的数字就会撞在一起 → 断言转红）。
- 将来若真的改了键：这条样例仍能证明兼容层没被顺手删掉。

## 三种 `--read-key-mode` 的期望数字（同一份 fixture）

| 模式 | `unresolved` | `files` | `reread_extra` | `reread_rate` |
|---|---|---|---|---|
| `auto`（默认，三键兼容） | 0 | 4 | 4 | 50.00% |
| `path-only`（只认 `path`） | 3 | 2 | 3 | 37.50% |
| `naive-bucket`（只认 `path` + 失败并桶） | 0 | 3 | 5 | 62.50% |

三者数字**必须互不相同**——这是「单键解析会丢信号 / 并桶会造假信号」的机械
证明。`--selftest` 逐项断言这三个数字，所以任何把兼容改回单键（或把失败并进
一个桶）的回归都会立刻转红。

另外 `--selftest` 还会：

- 构造一个权限受限文件，断言 `skipped_files=1`（以 root 跑时降级为 SKIP）；
- 在 `eval/transcript-baseline/` 存在时，断言基线报告引用的四个数字与
  `2026-11-19.json` 逐个相等（报告漂移 → 转红）。

## `compact-chain/` — compact 链样本

`compact-chain/` 是**独立子目录**（`--selftest` 对它单独跑一遍 `analyse`），
因为上面两个文件构成「四形态」的封闭断言组，混进链样本会改动那四个数字。
四条会话覆盖三件必须互不相同的东西：

| 文件 | 形状 | 覆盖的断言 |
|---|---|---|
| `parent.jsonl` | 无 `parent_id`；**带一条 567B 的 system 消息**；读 `/w/a.md`+`/w/b.md` | 压缩比的分母（锁「分母不含 system」）、已知重读的文件集合来源 |
| `child-read-reread.jsonl` | `parent_id` → parent，摘要 66B，前 10 调用读了 a.md（父读过）+ c.md（父没读过） | 压缩比分子；已知重读命中 |
| `child-read-only-known.jsonl` | 同上，前 10 调用只读 b.md（父读过） | 已知重读命中的第二条 |
| `plain-no-fork.jsonl` | 同 cwd，无 `parent_id`，**也不是任何链的父**，d.md 读两遍 | 参照系重读率 50.00% |

期望数字（`--selftest` 逐项断言）：

| 指标 | 值 | 口径 |
|---|---|---|
| `children` / `parents_found` / `orphans` | 2 / 2 / 0 | 有 `parent_id` 的会话数 / 父在扫描范围内 / 断链 |
| 压缩比 | 66 / 173 = **0.3815** | 子会话首条**非 system** 消息 content（扣 76B `SUMMARY_PREFIX` 样板）/ 父历史 Σcontent（**不含 system**） |
| 已知重读率 | 2 / 3 = **66.67%** | 子会话前 10 个 tool_call 里命中父已读文件集合的 read 数 / 同期 read 数 |
| 参照系重读率 | 1 / 2 = **50.00%** | 同 cwd、无 `parent_id`、且不是链父会话的会话聚合 Σ(次数-1)/read |

三数**必须互不相同**：它们塌成一个数就意味着某条口径被并进了另一条。
反向验证做过两次（每条都确认断言转红再改回来）：

| 改动 | 转红的断言 | 说明 |
|---|---|---|
| 压缩比分子改用子会话全历史 `content_bytes` | `ratio0` 0.3815 → 1.3757 | 分子必须是摘要，不是整个子会话 |
| 分母不排除 system 角色 | `chain.compression.mean` 0.3815 → 0.3194 | 真实 session 的 message 区以 system 开头（461/461 实测），算进去会让比值恒等于 system 占比 |

**父会话不算参照系**，这是踩过一次的口径错误：把 `parent.jsonl` 也算进
baseline 会让参照系变成 25.00%，因为父会话本身就是被压缩掉的那段历史，
它的 read 不属于「没压缩的会话长什么样」。

## 加样例的规矩

新增形态（例如 `list_dir` 的重复、`grep` 的翻页）要同时改
`scripts/transcript-stats` 的 `_selftest()` 断言，不能只加文件——否则样本
存在但无断言，等于没测。

改动某个口径时，反向验证是必须的：把被测的那一处改错（例如把压缩比的分子
换成 `content_bytes`），确认断言转红，再改回来。只跑正向通过不算验证——
那可能只是因为断言写得太松。
