### 用 `git stash` 换基线跑测试，会被同 checkout 的另一个会话 `reset --hard` 连带清空

- **Saw**: 为判断一批无头验收的红是不是自己引入的，`git stash push -u` → 跑基线 → `git stash pop`；pop 报成功、`git status` 也照常列出改动。几分钟后改动全没了，只剩一个未跟踪的新文件。
- **Why**: `git stash pop` 只把内容写回**工作区**（不进 index、不留任何保护）。这个 checkout 同时有别的 agent 会话在跑，它们在窗口里 commit 了四个提交、随后 `reset: moving to <旧提交>`（`git reflog` 可见 `6ab6edca…` 之后紧跟一次 reset）。`reset --hard` 不区分「我自己写的」和「stash 刚落回的」，一并丢弃。`-u` 只保证收进 stash，不保证落回来时还在。
- **Fix**: 从 stash commit 自己恢复 —— `git reflog` 里找 `On main: <msg>`，`git cat-file -t <sha>` 确认为 commit 后 `git checkout <sha> -- <paths>`；比 `git stash pop` 的残留可靠（pop 输出无冲突 ≠ 文件在位）。恢复后逐文件核 `git status` + 跑一遍该目录的测试/脚本确认逐字节回位（本例跑了 `go test ./internal/controlplane/` 与四个无头脚本）。
- **Lesson**: 多 agent 共享的 checkout 上**工作区不是私有的**：「先 stash → 跑基线 → 再还原」整段有一个可被外力清零的窗口。要跑基线就别碰工作区：`git show <rev>:<path>` 导出到临时目录，或在 worktree 里跑。stash 只适合单人独占的仓。
- **Refs**: `git reflog`（`reset: moving to` 那一条）· 本次 web 工作区树改动的 `webfrontend.html` 恢复
