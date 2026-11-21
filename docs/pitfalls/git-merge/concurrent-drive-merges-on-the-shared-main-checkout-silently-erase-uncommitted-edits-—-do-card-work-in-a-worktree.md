### Concurrent drive merges on the SHARED main checkout silently erase uncommitted edits — do card work in a worktree
- **Saw**: mid-card (2026-11-18), an uncommitted `pkg/agent/events.go` edit vanished: `git status` clean, edit gone, two new merge commits (`c_5gy5h6`, `c_wwpdxp`) on main that landed while the edit sat in the working tree. The butler drive flow merges into the same checkout other sessions edit from.
- **Why**: a drive merge (or its cleanup) runs checkout/reset-class git operations on the shared working tree; any uncommitted foreign changes are not theirs to preserve, and lose.
- **Fix**: implement every card in its own `git worktree` on a `drive/...` branch (e.g. `/tmp/seek-wt-<card>` from main), test there, commit there, then merge back from the main checkout in one short window — same discipline the drive sessions' own "worktree 沙盒" acceptance already implies.
- **Lesson**: on this repo the main checkout is a SHARED merge target, not a private workspace. Uncommitted work there has no owner protecting it.
- **Refs**: butler drive flow (merges `drive/c_*` → main); `git worktree add /tmp/seek-wt-acptools -b drive/acp-toolcall main`
