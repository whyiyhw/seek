### 共享主检出上动手前不问「有别人在写吗」——你的 commit 会扫走他们的 stage，他们的会扫走你的
- **Saw**: c_3j2ovf 在共享主检出直接改 12 个文件，c_ckla3l 同期也在写：`git add` 后 `git commit` 得 `no changes added to commit`（其 reset 重写 index）；其 `87a9c5af` 把我的全部改动 commit，soft reset 逐路径重做成 `248bb8cd`（剩 3 个），`git commit --only` 才落地（`80921d65`）。index 重写两次。
- **Why**: 默认 cwd 即共享主检出，规则层未提 worktree（`grep -c worktree AGENTS.md` = 0）；开工时 `git status` 已列约 20 个未碰过的路径。
- **Fix**: ①动笔先 `git status` + `git worktree list`，有别人路径即 `enter_worktree`；②改到一半：`git commit --only <路径...>`，提交前 `git diff --cached --stat` 对账；③不留 staged 文件在共享 checkout。
- **Lesson**: `git status` 里没碰过的文件 =「有人在写」的警报；11 个 worktree、`enter_worktree` 与 `~/.paseo/worktrees/*/drive-c-*` 都在。
- **Refs**: 同族 `docs/pitfalls/tooling/git-stash-then-a-concurrent-reset-hard-in-the-same-checkout-wipes-the-restored-work.md`、`docs/pitfalls/tooling/git-add-只加不清-共享-checkout-里并行会话已-stage-的条目会被你的-commit-整体扫走.md`；reflog `87a9c5af` 15 文件→ `248bb8cd` 3 → `80921d65` 12；卡 c_3j2ovf / c_ckla3l；`enter_worktree` / `exit_worktree`
