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

## 加样例的规矩

新增形态（例如 `list_dir` 的重复、`grep` 的翻页）要同时改
`scripts/transcript-stats` 的 `_selftest()` 断言，不能只加文件——否则样本
存在但无断言，等于没测。
