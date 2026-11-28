### 同名函数被 main 重写时 HEAD 侧的独有副作用行不进冲突块——两侧各删各的就静默丢
- **Saw**: c_ux7lvk merge main：main 把 `openSession` 连侧栏常驻化重写并挪位，分支侧同名函数有本卡独有的两行 `treeRows.clear()`/`treeResults.clear()`；冲突块只落声明区与 CSS，旧位置函数体整段消失、两行清理蒸发，无冲突标记。
- **Why**: 三方合并按行对齐、不认函数：main 重写+挪位后，HEAD 对旧函数体的增量挂在「main 已删除」区间，git 静默取 main；增量只有落在双方都保留的行旁才起冲突块。
- **Fix**: 解完用 `git diff HEAD -- <file>` 找出被整段删除的函数，把独有副作用逐行搬进 main 新版；21679dfb 补回 `treeRows/treeResults` 清理。
- **Lesson**: 「解完冲突」≠「两侧内容都在」：单侧改在另一侧删除区间的增量是静默丢重灾区。分支函数带独有副作用时，merge 前先 `git diff <merge-base>..HEAD -- <file>` 列清单，解完逐项 grep 幸存。
- **Refs**: `internal/controlplane/webfrontend.html` `openSession`；同族 `docs/pitfalls/git-merge/同一插入点的双方增量，自动合并区会静默取一侧不报冲突——锚点要-grep-幸存核对.md`
