### A `$(...)` around a function that starts a background server deadlocks — the fetch waits for the child's stdout
- **Saw**: `scripts/web-trend-manual.sh`'s first draft did `URL=$(start_serve "$HOME_M")`; `start_serve` backgrounds `seek serve --frontend=web` then echoes the banner URL; the script printed `== fixtures: multi ==` and sat five minutes — serve alive, banner in its log, nothing hung on the network.
- **Why**: command substitution reads until EOF on the pipe; the backgrounded serve inherited that pipe as stdout, so the substitution never finished. Banner capture and readiness loop were correct — the defect was purely the call site's shape.
- **Fix**: `start_serve` sets globals (`URL`/`BASE`/`TOK`) and returns; call sites do `start_serve "$HOME_M" || exit 1` then `URL_M="$URL"`. A comment at the function records the trap.
- **Lesson**: never wrap a "start a long-running child" step in `$(...)` — capture via a variable the function sets, or redirect the child's stdout (`>log 2>&1`) inside it; symptom: the script goes quiet right after a step while the started process is healthy.
- **Refs**: `scripts/web-trend-manual.sh`, sibling `scripts/web-sessions-manual.sh` / `web-memory-manual.sh`

