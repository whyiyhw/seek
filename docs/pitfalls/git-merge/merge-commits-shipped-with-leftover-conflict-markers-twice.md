### Merge commits shipped with leftover conflict markers (twice)
- **Saw**: `920f9e1` (merge main into `drive/c_u4ugnu`) landed `docs/prd/feature-orchestration.md` with `<<<<<<< HEAD` / `=======` / `>>>>>>> main` inside the PRD 状态行 — second bite of the class (first: CHANGELOG during the `c_zam44t` merge). Both conflicts sat inside ONE very long line, never reading as a "pick a side" block.
- **Why**: git commits conflict markers without complaint; manual per-file review was the only gate and failed twice on this long-single-line shape.
- **Fix**: after resolving any merge, BEFORE `git commit`: `git grep -n '^<<<<<<< \|^>>>>>>> '` and `git diff --cached --check`. Post-hoc audit: `git diff --check <merge>^1 <merge>` — NOT `git show --check <merge>` (its `--cc` condensed diff suppresses these hunks; verified on `920f9e1`: show silent, diff-vs-parent flags all three marker lines). Cleanup: card `c_zxeqih`.
- **Lesson**: merges touching docs with long single-line status fields get a mechanical marker-grep before commit — two bites is a pattern, not bad luck.
- **Refs**: `docs/prd/feature-orchestration.md` 状态行; `920f9e1`, `e8eface`.
