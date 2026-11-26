### PRD said "sessions are archived per-project" — they are not; bucket by header CWD
- **Saw**: M-SD2 实现时 PRD 的指针（`~/.seek/projects/<pid>/sessions/<sid>/`，paths.go L179）实际是 checkpoint/subagent SIDECAR 空间而非 session transcripts——transcripts 平铺在 `~/.seek/sessions/<id>.jsonl`，只有 JSONL header 里有 `cwd` 字段。
- **Why**: `ProjectSessionDir` 是为 checkpoint 子系统命名/文档化的，PRD 把路径形状当成了归档位置；此前没有任何东西需要按项目分桶，所以没有现成 helper。
- **Fix**: `session.Store.ListProject(projectID)`（M-SD2）只读 header（`loadMeta` 纪律）并按 `paths.ProjectID(info.CWD)` 过滤，跳过 forked sessions（`ParentID != ""`）以免 /branch & /compact 子女重复计数。Commit c_dkgvnp.
- **Lesson**: 设计文档引用某路径为「X 所在处」时，向写 X 的代码求证，别只看 path helper 的名字。会话的按项目分桶 = header `cwd` 的哈希，磁盘上没有东西替你分组。
- **Refs**: `internal/session/session.go` (ListProject), `internal/paths/paths.go` (ProjectSessionDir comment), `docs/prd/feature-skill-distill.md` §3.1 D
