### 同名函数被 main 重写时 HEAD 侧的独有副作用行不进冲突块——两侧各删各的就静默丢
- **Saw**: c_ux7lvk merge main：main 把 `openSession` 连着侧栏常驻化整体重写并挪了位置；分支侧同名函数里有两行本卡独有的 `treeRows.clear()`/`treeResults.clear()`。冲突块只落在声明区与 CSS；旧位置的函数体整段以「main 已挪走」的形态消失，两行清理随之蒸发——没有任何冲突标记提示。
- **Why**: 三方合并按行对齐，不认函数。main 侧重写+挪位后，HEAD 对旧函数体的增量（那两行）挂在「main 侧已删除」的区间里，git 视为 main 的整理 + HEAD 无改动，静默取 main；只有当 HEAD 增量恰好落在双方都保留的行邻近时才会起冲突块。
- **Fix**: 解同名函数重写类冲突时，解完必须 diff 出 HEAD 侧被整体删除的函数（`git diff HEAD -- <file>` 里找自己分支加过的行），把独有副作用逐行搬进 main 新版；merge commit 21679dfb 把 `treeRows/treeResults` 清理补回 main 版 `openSession`。
- **Lesson**: 「解完冲突」≠「两侧内容都在」——冲突块只保双方都改的行，单侧改在另一侧删除的区间里的增量是静默丢的重灾区。分支侧函数带独有副作用（登记表清理、钩子注销）时，merge 前先 `git diff <merge-base>..HEAD -- <file>` 列出自己的增量清单，解完逐项 grep 幸存。
- **Refs**: `internal/controlplane/webfrontend.html` `openSession`；同族但不同形状：`docs/pitfalls/git-merge/同一插入点的双方增量，自动合并区会静默取一侧不报冲突——锚点要-grep-幸存核对.md`。
