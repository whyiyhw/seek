### 合并 main 时 pitfall 台账已改「一条目一文件」——挑 one-side 解冲突会把新条目整段吞掉
- **Saw**: 本卡（c_rqcuaa）分支基于迁移前的单文件 `docs/pitfalls.md`（追加了一条 macOS sed 条目 + 改了头部计数生成块），`git merge main` 撞冲突；`git checkout --theirs docs/pitfalls.md` 是最省事的一步——main 侧是 14 行静态桩，本卡那条新条目（以及生成块里的计数增量）**连带消失**，工作树里没有任何东西会红。
- **Why**: 2026-11-20 的 c_uxznyn 把台账拆成 `docs/pitfalls/<section>/<slug>.md`（一条目一文件，合并天然 union 安全），留下的 `docs/pitfalls.md` 只是说明桩。旧分支的单文件改动与这个桩**语义上没有交集**：冲突标记两侧看上去都是「同一文件的两种版本」，但一侧是要丢弃的旧容器、一侧是要搬迁的内容，any-side 都错。
- **Fix**: 解冲突取 main 的桩（`--theirs`），然后把本分支新增的每个 `### ` 条目单独落成 `docs/pitfalls/<section>/<slug>.md`（正文逐字，slug 由标题折叠）；脚本 `scripts/pitfalls-split.sh` 对仍带旧格式条目的分支可原地转换，但它只认「工作树里还存在的条目」——已经被 one-side 丢掉的内容它救不回来。判据是 `bash scripts/pitfalls-count.sh --check`（格式 + 目录合法性 + live 计数）与 `grep -r '<条目标题>' docs/pitfalls/` 命中。
- **Lesson**: 合并前先看台账**当前形态**（`git show main:docs/pitfalls.md | head`）再决定冲突解法；文档容器类改动（拆分/迁移）在途分支上，冲突的含义是「内容要搬家」而不是「选一侧」——one-side 是最快也最贵的那种绿。
- **Refs**: `docs/pitfalls.md`（桩）；`scripts/pitfalls-split.sh`（可重复转换）；`scripts/pitfalls-count.sh --check`；`docs/pitfalls/tooling/macos-sed-is-bsd-…md`（本卡搬迁后的落地文件）。
