### git add 只加不清：共享 checkout 里并行会话已 stage 的条目会被你的 commit 整体扫走
- **Saw**: 在另一会话持有 staged 文件的共享 checkout 里提交本卡改动：`git add <本卡三文件>` 后 commit 进了 15 个文件——其他会话已 stage 的 12 个（含 5 个未跟踪新文件）一并入册（drive/c_ckla3l 的 87a9c5af，事后重做为 248bb8cd）。
- **Why**: `git add` 向现有 index 增量追加、不清空，commit 消费整个 index 而非刚 add 的路径；共享 checkout 的 index 是共享可变状态，「我只 add 了这三个」≠「commit 只会有这三个」。
- **Fix**: `git reset --soft HEAD~1` 退回（index 原样保留）；对 12 个外来路径 `git reset HEAD --` 退 stage、只留本卡 3 个重新提交，再把 12 个路径原样 re-add 还原并行会话的 index。事前防御：commit 前 `git diff --cached --stat` 对账，超出即停。
- **Lesson**: 多会话共享 checkout 的每步 git 写操作前都要对 index 对账；staged 清单里有不属于本卡的路径，先摘干净再 commit。
- **Refs**: 同族先例 `docs/pitfalls/tooling/git-stash-then-a-concurrent-reset-hard-in-the-same-checkout-wipes-the-restored-work.md`
