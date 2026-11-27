### 一条目一文件的 split 在条目内容改过一个字后产出 -2 副本——在途分支带回的旧格式台账会造重复条目
- **Saw**: 卡 c_6mbjfq：`git merge main`（main 已跑 c_uxznyn 拆分，`docs/pitfalls.md` 只剩静态桩）后再并 `drive/c_5m3fm6`；`scripts/pitfalls-split.sh --dry-run` 报 `would write 1`：`同一页面里第二份 function loadSessions …` 那条被改过一个字（57051286 feat 提交），不再逐字节同于现存文件，split 按 slug 避让写出 `…-2.md`——同一坑两文件。
- **Why**: 「已迁移 = 跳过」判据是逐字节相等（`cmp -s`）：旧条目有任何在途编辑（错字、Refs、`--write`）即漂移，脚本当新条目避让——把「内容有差异」当「条目不同」。
- **Fix**: `--dry-run` 报 `-2` 即 delta 是对已迁移条目的编辑，手工落到现存文件（本卡：Refs 补 `web-stop-manual`），源文件取桩侧；`would write 0` 才可放心跑。别为 split 干净把错字改回去——那掩盖真实编辑。
- **Lesson**: 「一条目一文件」消灭追加冲突，不消灭内容漂移：逐字节跳过的脚本遇差异 `-2` 避让——机械安全、语义重复。
- **Refs**: `scripts/pitfalls-split.sh`（`run_split` slug 避让、`cmp -s` 跳过判据）；`docs/pitfalls/README.md`「Split mechanics」；同族「派生生成块的合并冲突不能『挑一侧』」条。
