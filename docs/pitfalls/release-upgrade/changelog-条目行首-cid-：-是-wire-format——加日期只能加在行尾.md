### CHANGELOG 条目行首 `- <cid>：` 是 wire format——加日期只能加在行尾
- **Saw**: 手工裁剪「已进 main、未部署」节的机器写条目时，最自然的写法是「日期 + 卡号 + 一句话」（`- 2026-11-19 c_cjewsn：…`）。但这样写之后，同一张卡下次 merge 时 butler 会**再追加一条整卡面的长条目**
- **Why**: butler 侧 `internal/drive/changelog.go` 的幂等判定是 `strings.Contains(content, "- "+cid+"：")`——它要的是行首紧邻的 `- <cid>：` 这一整串；日期插在中间，子串就不存在了，机器认为该卡还没记过。`scripts/deploy.sh` 的 `changelog_promote` 同样只按行首 `- ` 识别条目，行首形状不能动
- **Fix**: 裁剪时保持行首 `- <cid>：<一句话>` 原样，把日期作为**尾部**括注（`…（2026-11-19）`）。卡 c_nqvbdu 按此裁 c_cjewsn / c_idisqf / c_3mny65 三条，5 518 B → 340 B
- **Lesson**: 「机器也在写这个文件」的地方，任何手工润色都得先问「机器用什么形状认领这一行」。前缀是认领凭据，后缀是自由的——不确定性往里加，别往行首加
- **Refs**: `docs/notes-changelog-trim-2026-11-20.md`；butler `internal/drive/changelog.go:changelogAppend`、`scripts/deploy.sh:changelog_promote`
