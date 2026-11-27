### The per-turn `workflowReminder` taught `read→grep→read` while AGENTS.md and the book promised `grep→read`
- **Saw**: every user turn carried `[Workflow: read→grep→read(limit≤50). edit→read first.]` (appended in `pkg/agent/agent.go`, recency-biased so it outranks the system-prompt copy) — the *before* chain that `docs/book/appendix-b.md:5`'s tuning round replaced with `grep→read`, and the opposite of AGENTS.md's `grep first` → `read(offset=N)`.
- **Why**: the constant was rewritten in `3dddae3c` (a chore bundling the read tool's new `limit` param) and nothing cross-checks it against the documented workflow — two instruction surfaces, no tie-breaker: drift is silent by construction.
- **Fix**: reorder to `[Workflow: grep→read(offset,limit≤50). edit→read first.]` — grep first, read only the window; still a byte-constant string, prefix cache unaffected. (this change; commit pending)
- **Lesson**: an always-injected string is an instruction surface too. When the documented workflow changes, grep for `[Workflow:` — the reminder does not update itself.
- **Refs**: `pkg/agent/agent.go:323` (+ call site `:452`), `docs/book/appendix-b.md:5`, AGENTS.md "Tool usage workflow"
