### 合并 main 时 pitfall 台账已改「一条目一文件」——挑 one-side 解冲突会把新条目整段吞掉
- **Saw**: 卡 c_rqcuaa 分支基于迁移前单文件 `docs/pitfalls.md`（追加 macOS sed 条目 + 改计数块），`git merge main` 撞冲突；`git checkout --theirs docs/pitfalls.md`——main 侧是 14 行静态桩，新条目与计数增量连带消失，且没有任何东西会红。
- **Why**: 2026-11-20 的 c_uxznyn 已拆成 `docs/pitfalls/<section>/<slug>.md`（union 安全）；旧分支的改动与桩无语义交集：旧容器 vs 要搬的内容，any-side 都错。
- **Fix**: 取 main 桩（`--theirs`），把新增 `### ` 条目各落成 `docs/pitfalls/<section>/<slug>.md`（逐字）；`scripts/pitfalls-split.sh` 可转换仍带旧格式的分支，但只认工作树里还在的条目——被 one-side 丢掉的救不回来。判据：`bash scripts/pitfalls-count.sh --check` 与 `grep -r '<条目标题>' docs/pitfalls/`。
- **Lesson**: 合并前先看台账当前形态（`git show main:docs/pitfalls.md | head`）；容器类改动在途时，冲突含义是「内容要搬家」而非「选一侧」。
- **Refs**: `docs/pitfalls.md`（桩）；`scripts/pitfalls-split.sh`；`scripts/pitfalls-count.sh --check`；`docs/pitfalls/tooling/macos-sed-is-bsd-…md`。
