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

## 4. 消费记录（卡 `c_nqvbdu`，2026-11-20）

上面三条已按本清单第 2 节裁短（`docs/pitfalls.md`「CHANGELOG 条目行首 `- <cid>：` 是 wire format」条记了为什么日期只能挂尾）：

| 卡号 | 裁前 | 裁后 | 现文 |
|---|---:|---:|---|
| `c_cjewsn` | 3 799 B | 114 B | `- c_cjewsn：v8·R2：独立 verifier——done_when 断言的异会话裁判（第二波首卡）（2026-11-19）` |
| `c_idisqf` | 807 B | 112 B | `- c_idisqf：/compact 溢出死锁救援：compact 自身装不进剩余预算时的降级路径（2026-11-19）` |
| `c_3mny65` | 912 B | 114 B | `- c_3mny65：main.go 减重第二刀：planBridge 与 subagent glue 各拆一文件（纯搬移）（2026-11-20）` |

合计 5 518 B → 340 B，**省 5 178 B**；「已进 main、未部署」节 18 316 B → 13 139 B，全文 21 378 B → 16 200 B。
与清单预估差 48 B：日期括注（各 ~16 B）是清单第 2 节的建议形态里没有的，见下条。

**口径修正（与本清单第 2 节的字面建议不同）**：卡的验收口径是「时间+卡号+一句话」，但**日期不能放行首**——
butler 的幂等判定是 `strings.Contains(content, "- "+cid+"：")`（`internal/drive/changelog.go:changelogAppend`），
`scripts/deploy.sh` 的 `changelog_promote` 也按行首 `- ` 认条目。日期插在 `- ` 与卡号之间会让子串失配，
同一张卡下次 merge 会再追加一条整卡面长条目。故取「`- <cid>：<一句话>（YYYY-MM-DD）`」：日期挂尾，行首 wire 形状不动。

合并窗口里补记的两条（`c_56yrcj` 94 B、`c_554rlg` 126 B）核对过——均为单句卡标题，未膨胀，**不裁**。

### 4.1 重派轮补记（同卡 `c_nqvbdu`，2026-11-20，merge main 后）

重派轮先在分支内 `git merge main` 吸收合并窗口（c_txz6vz / c_ldu44u / c_3acz3q / c_3mryc3 /
c_kwiajf / c_y2akdi 六张卡，节 +4 065 B），窗口又带进**两条同族整卡面条目**，按本清单第 2 节同规则裁：

| 卡号 | 裁前 | 裁后 | 现文 |
|---|---:|---:|---|
| `c_ldu44u` | 998 B | 129 B | `- c_ldu44u：价卡/模型目录漂移防御：verified-on 台账 + 核价巡检脚本（六条同族坑收口）（2026-11-20）` |
| `c_3acz3q` | 1 046 B | 125 B | `- c_3acz3q：v8·U1 后继：内核依赖八违例逐条定性——倒置/搬移/误报，修订内核清单（2026-11-20）` |

两条省 1 790 B。窗口其余新增不裁：`c_txz6vz` 人写 release note（1 426 B，人写不裁）与
`c_txz6vz`/`c_3mryc3`/`c_kwiajf`/`c_y2akdi` 四条单句机器条目（187/128/167/106 B）。

节体积台账（同一量法，`## 已进 main、未部署` 至文件尾）：18 316 B（裁前）→ 13 138 B（第一轮裁后）
→ 17 203 B（merge main 后/本轮裁前）→ **15 413 B（现在）**。较裁前 −2 903 B；剔除 main 窗口
新增的 4 065 B 合法内容后，纯裁剪效果 −6 968 B（原三条 −5 178 + 本轮两条 −1 790）。

清单第 3 节的预警在此应验一次：源头（butler `changelogEntry` 截断）未修之前，每张带背景段的卡
merge 时仍会注入整卡面长条目——本轮两条即实例，存量人工裁剪对它们是唯一防线。
