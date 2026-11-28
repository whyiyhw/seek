### 用 `git stash` 换基线跑测试，会被同 checkout 的另一个会话 `reset --hard` 连带清空

- **Saw**: 为跑基线判红：`git stash push -u` → 跑基线 → `git stash pop`；pop 报成功、`git status` 照常列出改动，几分钟后改动全没了，只剩一个未跟踪新文件。
- **Why**: `git stash pop` 只把内容写回工作区（不进 index、无保护）；同 checkout 其他会话其间 commit 四个提交，随后 `reset: moving to <旧提交>`（`git reflog` 可见 `6ab6edca…` 后紧跟一次 reset），`reset --hard` 不区分「会话自己写的」和「stash 刚落回的」，一并丢弃；`-u` 只保证收进 stash。
- **Fix**: 从 stash commit 恢复：`git reflog` 找 `On main: <msg>`，`git cat-file -t <sha>` 确认后 `git checkout <sha> -- <paths>`；恢复后核 `git status` 并跑测试确认回位（`go test ./internal/controlplane/` 与四个无头脚本）。
- **Lesson**: 多 agent 共享的 checkout 工作区不是私有的，「stash → 基线 → 还原」整段有被外力清零的窗口；跑基线用 `git show <rev>:<path>` 导出到临时目录或在 worktree 里跑，stash 只适合单人独占的仓。
- **Refs**: `git reflog`（`reset: moving to` 那条）· `webfrontend.html` 的恢复
