### worktree 里未跟踪的实测证据目录，验证侧判「可返工」——证据随收尾销毁
- **Saw**: 卡 c_xhbqae 的改动、自测、gate/kill-switch 实证全部过，独立验证四项皆过，唯一疑点 `git status` 的 `?? .butler-evidence-c_xhbqae/`（res-gate/res-kill 行、fake-seek、bytecheck 未入 git）→ 返工第 1 轮。（该目录后归位至 `docs/evidence/c_xhbqae/`，此处保留当时路径原貌。）
- **Why**: `.butler-evidence-<card>/` 是卡在 worktree 里落的实测产物，只有 commit 进分支才算可复核证据；未跟踪文件不在改动集里，worktree 收尾/合并时随目录一起消失——验收要的证据行在 git 里，不在磁盘上。
- **Fix**: 整目录 `git add` + commit 进当前分支（5abb095b），commit 后 `git status --porcelain` 为空。
- **Lesson**: 证据与代码改动同一次收尾落盘——跑完实测就 commit 证据目录，别把它留成 `??`；验证侧读的是分支状态，不是工作目录。落点统一为 `docs/evidence/<card>/`（2026-11-26 归位，仓库根不再出现证据目录，引用一律写新路径）。
- **Refs**: `.butler-evidence-c_xhbqae/`（后归位至 `docs/evidence/c_xhbqae/`）、卡 c_xhbqae
