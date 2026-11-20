# CHANGELOG「已进 main、未部署」超长条目裁剪清单（2026-11-20）

> 卡片：butler `c_554rlg`（减法卡：pitfalls/CHANGELOG 派生态机械化）。
> **本卡不改 CHANGELOG 历史条目**——按卡面口径，seek 侧只产出这份人工清单 + 源头的修法说明；
> 真正的裁剪动作与 `changelogEntry` 的截断逻辑都在 butler 仓，交回主会话/人执行。

## 1. 现状读数（`bash scripts/weight.sh`，2026-11-20）

| 项 | 值 | 预算 |
|---|---:|---:|
| `CHANGELOG.md` 全文 | 18 378 B | 6 000 B |
| 「已进 main、未部署」节 | 15 268 B / 21 条 | —— |

条目分两类：

- **人写条目 8 条**（`- **…**。` 形态，1 220 B / 690 / 1232 / 1657 / 1290 / 1550 / 802 …）：这些是 release note 的正文，长度是内容本身，**不该机械裁**。
- **机器写条目 13 条**（`- c_xxxxxxx：<卡标题>` 形态）：由 butler `internal/drive/changelog.go` 的
  `changelogEntry` 在 merge 时生成=**卡标题原样抄进去**。卡标题里带「背景：… 任务：… 验收：…」是常态，
  于是整张卡面被抄成一行。

## 2. 需要人工裁的三条（机器写、已膨胀）

| 行 | 卡号 | 现在 | 卡标题首句即可 |
|---|---|---:|---|
| 32 | `c_cjewsn` | 3 799 B | `- c_cjewsn：v8·R2：独立 verifier——done_when 断言的异会话裁判（第二波首卡）`（98 B） |
| 40 | `c_idisqf` | 807 B | `- c_idisqf：/compact 溢出死锁救援：compact 自身装不进剩余预算时的降级路径`（96 B） |
| 41 | `c_3mny65` | 912 B | `- c_3mny65：main.go 减重第二刀：planBridge 与 subagent glue 各拆一文件（纯搬移）`（98 B） |

三条合计 5 518 B → 292 B，**省 5 226 B**（文件 18 378 → 13 152 B，仍超 6 000 B 预算，剩余部分是人写条目，
按上面「人写不裁」的口径留给下一张 release 卡：这些条目本该在部署时转入「已上线」节再随版本归档）。

裁剪口径（与源头实现同一套，便于核对）：

1. 剥掉 `[负责人 …]` 这类前导标签（`changelogTitleTagRe` 已在做）；
2. 按**首个** 句号「。」/「 背景：」/「（验收」/「 任务：」切第一句；
3. 结果上限 ~120 字符（rune 计，不是字节），超出加「…」。

## 3. 源头修法（butler 仓，本卡不做）

`~/code/github/butler/internal/drive/changelog.go:36-39` 的 `changelogEntry` 目前只剥标签：

```go
func changelogEntry(cid, title string) string {
	return "- " + cid + "：" + changelogTitleTagRe.ReplaceAllString(strings.TrimSpace(title), "")
}
```

建议改成「剥标签 → 切第一句 → 截断 120 rune（加 …）」，并在 `changelog_test.go`
的 `TestChangelogEntry_剥前缀标签` 旁边补两个用例：带「背景：」的整卡标题、超长无标点标题。

**为什么要改源头而不是只清存量**：存量清一次就完了，源头不改，下一张带背景段的卡照样注入 3 KB。
`changelogAppend` 是幂等的（同卡号已在则不追加），所以历史条目被人工裁短之后不会被机器写回长版本 ——
**但**同卡号的行只要还在，机器就不会再碰它，所以「人工裁短」对存量是安全的。
