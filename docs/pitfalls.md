# seek pitfalls log

Things that surprised us, and what we learned. **Single source of truth** — when something non-obvious bites, write it here.

## Format

```markdown
### Short symptom title
- **Saw**: what was observed (visible behaviour, not internal cause)
- **Why**: root cause in one or two sentences
- **Fix**: what we changed; commit hash
- **Lesson**: the takeaway for future-us
- **Refs**: optional — file paths, external docs, related entries
```

Keep entries **terse**. If you find yourself writing a paragraph, the lesson is probably hiding in there — pull it out.

## Policy

- AI assistants working in this repo: every time you fix a non-obvious bug or discover a surprising constraint, **append an entry here** AND add a `Pitfall:` trailer to the commit (see [`CLAUDE.md`](../CLAUDE.md)).
- Humans: same convention. Use `scripts/extract-pitfalls.sh` to sanity-check that recent commits' `Pitfall:` trailers line up with entries here.

---

## Filesystem / concurrency

### Two parallel writers of the SAME content-addressed blob race on a shared `.tmp` filename
- **Saw**: checkpoint package's concurrent-snapshot test failed with ~half the expected events; the warnings logged "rename ... .tmp ... : no such file or directory" for many goroutines
- **Why**: `storeBlobLocked(sha, content)` used a fixed `<bp>.tmp` filename, then `os.Rename(tmp, bp)`. Two goroutines hashing the same content (e.g. concurrent edits to small files with identical pre-state) both: (a) Stat — miss; (b) WriteFile(tmp); (c) Rename(tmp, bp). Whichever rename wins erases the .tmp file. The losing goroutine's later WriteFile-to-the-same-tmp succeeds, but its rename of that file fails because the .tmp path is already renamed-away — same name, but stale fd handle wasn't the issue, the file path was simply gone
- **Fix**: swap fixed-name `<bp>.tmp` for `os.CreateTemp(dir, base+".tmp.*")` so every writer gets a unique scratch path, then absorb the "target already exists" race after `Rename` returns an error by checking `Stat(bp)` and dropping our tmp copy silently. Commit (M9.1)
- **Lesson**: content-addressed storage is forgiving about losing the race (the winner's bytes equal yours) but UNforgiving about sharing a `.tmp` filename. Either use a unique tmp name per writer OR hold a per-blob mutex. The unique-tmp approach is simpler and lock-free
- **Refs**: `internal/checkpoint/file.go:storeBlobLocked`, `internal/checkpoint/checkpoint_test.go:TestFile_ConcurrentSnapshots`

## Hook / memory

### OnSessionStart must reset snapshot state for --resume correctness
- **Saw**: after implementing the M5.9 snapshot+delta strategy, `--resume` would inject a stale snapshot based on the previous session's first `OnPrePrompt` call, because `snapshotInjected` was still `true` from the prior agent lifetime
- **Why**: the Hook struct fields `snapshotInjected` and `snapshotEntryNames` survive across agent lifetimes when the registry is reused (which happens on `--resume`). Without explicit reset, the second session's first `OnPrePrompt` would skip `injectSnapshot()` and produce no soul/index context at all — the model would lose all memory context
- **Fix**: `OnSessionStart` now resets `snapshotInjected = false` and `snapshotEntryNames = nil` before running GC. This ensures the first `OnPrePrompt` after every session boundary (fresh or resume) rebuilds the snapshot from current data. Commit (M5.9)
- **Lesson**: any Hook struct field that tracks per-session state MUST be reset in `OnSessionStart`. The Hook is reusable across agent lifetimes (--resume), not 1:1 with sessions. Don't assume zero-init at construction covers all paths
- **Refs**: `internal/memory/hook.go:OnSessionStart`, `internal/memory/hook.go:Hook.snapshotInjected`

## TUI / terminal

### Explicit `Accept-Encoding: gzip` header disables Go's auto-decompression
- **Saw**: webfetch tool returned the raw gzip-compressed bytes of an HTTPS HTML response instead of decoded text. The model saw garbage where the doc body should be and reported "gzip 压缩的 HTML，无法正常解码显示内容"
- **Why**: in webfetch.go we set `req.Header.Set("Accept-Encoding", "gzip")` manually. Go's `http.Transport` normally requests gzip on the caller's behalf AND transparently decodes the response body — but when the user explicitly sets `Accept-Encoding`, the transport interprets it as "the caller wants to decode this themselves" and skips decompression. Documented at `net/http.Transport.DisableCompression`: *"If the Transport requests gzip on its own and gets a gzipped response, it's transparently decoded in the Response.Body. However, if the user explicitly requested gzip it is not automatically uncompressed."*
- **Fix**: remove the manual `Set("Accept-Encoding", ...)` line. Go's transport adds it for us AND decodes — both ends transparent. Commit `4efcdd4`
- **Lesson**: never set `Accept-Encoding` manually unless you're actually going to decode by hand. The header acts as an opt-in into "DIY decompression" mode that you almost certainly don't want. Same gotcha applies to brotli (`Accept-Encoding: br`) if you ever add it — but Go's stdlib doesn't auto-handle br at all, so adding br means committing to manual decode anyway. For v1 we don't need br; the no-set default gets gzip transparently
- **Refs**: `internal/tools/webfetch/webfetch.go` (header set site), `internal/tools/webfetch/webfetch_test.go:TestExecute_AutoDecodesGzip` (regression test serving gzipped content)

### "starting seek …" placeholder stuck for seconds (or forever)
- **Saw**: launching the TUI showed only the welcome banner / placeholder, no input box, no status bar; could last from a second to "never resolves"
- **Why**: bubbletea is supposed to send a `WindowSizeMsg` on startup so the layout can size itself, but on some terminal / tmux / `go run` combinations that first message is delayed or dropped. Without dimensions, `relayout()` returned early and `m.ready` stayed false
- **Fix**: in `Init()` we now also emit a synthetic `WindowSizeMsg` derived from `term.GetSize(os.Stdout.Fd())`. If the real one ever arrives, it just re-runs idempotent relayout. Commit `df683a9`
- **Lesson**: don't rely on framework-promised startup messages — query the dimensions yourself if your render path can't proceed without them
- **Refs**: `internal/tui/model.go:initialSizeCmd`

### Mouse drag-to-select did nothing
- **Saw**: opening the TUI, clicking and dragging in the terminal had no effect; couldn't copy any text out
- **Why**: `tea.WithMouseCellMotion()` makes bubbletea capture every mouse event, which preempts the terminal's native click-and-drag selection
- **Fix**: removed the option from `tui.Run`. Mouse scroll is gone; keyboard scroll (PgUp/PgDn, Ctrl+U/D) covers it. Commit `08449cd`
- **Lesson**: don't enable mouse capture unless you actually have pointer-driven UI. The trade `copy ⇄ mouse scroll` is asymmetric — copy is the load-bearing feature
- **Update (alt-screen era)**: this trade INVERTED once history moved into an in-app viewport. See "Mouse wheel did nothing in alt-screen mode" below — mouse capture is back on, and the click-drag escape hatch is now a modifier-key (Option / Shift)

### Mouse wheel did nothing in alt-screen mode
- **Saw**: under the current alt-screen TUI, scroll wheel and trackpad gestures had no effect; the only way to read past context was PgUp/PgDn — even though the viewport visibly *looked* scrollable
- **Why**: `tea.WithMouseCellMotion()` was removed in `08449cd` (inline-mode era — see entry above) to preserve native click-and-drag selection. Under alt-screen the in-app viewport is the *only* way to see past content (terminal scrollback is gone), so the asymmetry from `08449cd` flipped — wheel scroll became load-bearing
- **Fix**: re-enabled `tea.WithMouseCellMotion()` in `tui.Run`; added a `case tea.MouseMsg:` arm in `Update()` that forwards to the history viewport, whose default `Update()` already handles `MouseButtonWheelUp/Down` via `ScrollUp/ScrollDown(MouseWheelDelta)` (`bubbles/viewport@v1.0.0/viewport.go:463-494`). Also fixed `appendHistory()` to capture `wasAtBottom` before `SetContent` and only call `GotoBottom()` when true — otherwise every streamed `MessageEnd` / `ToolExecEnd` would yank the user back to the bottom (same logical bug as "Streaming kept yanking the viewport to the bottom" below, regressed across the M4.5 → alt-screen migration). Commit (this one)
- **Selection escape hatch**: app-side mouse capture suppresses native click-drag selection. Modern terminals bypass the capture when the user holds **Option** (⌥) on macOS Terminal.app / iTerm2, or **Shift** on most Linux terminals (xterm, gnome-terminal, alacritty, kitty) — that drag goes to the terminal, not the app
- **Lesson**: the mouse-capture cost/benefit flips with rendering mode. Inline → keep it off (terminal owns scroll, copy is the sole mouse interaction worth caring about). Alt-screen → turn it on (app owns scroll, the modifier-key bypass covers the copy use case). Revisit asymmetric trade-offs whenever the underlying architecture changes
- **Refs**: `internal/tui/run.go`, `internal/tui/update.go:case tea.MouseMsg`, `internal/tui/model.go:appendHistory`

### Spacebar scrolled the conversation a page
- **Saw**: typing a space in the input box also paged the viewport down; typing 'b' paged up; PgUp/PgDn worked but only by accident
- **Why**: my `Update` was forwarding every `KeyMsg` to **both** the textarea and the viewport. The viewport's default keymap includes ` `, `b`, `f`, `j`, `k` for scrolling, all of which are also normal text input
- **Fix**: explicit per-key routing in `handleKey()` — global keys → handled inline; PgUp/PgDn/Ctrl+U/Ctrl+D → viewport only; everything else → textarea only. Commit `7daaa3b`
- **Lesson**: in bubbletea, never forward a `KeyMsg` to two consumers unless you've manually filtered which keys go where. The "default keymaps overlap" footgun is invisible until you trip on it
- **Refs**: `internal/tui/update.go:handleKey`

### Streaming kept yanking the viewport to the bottom
- **Saw**: scrolling up to read earlier output mid-stream would snap back to the bottom on the next token
- **Why**: every `agentEventMsg` unconditionally called `viewport.GotoBottom()`
- **Fix**: capture `wasAtBottom := m.viewport.AtBottom()` before applying the event; only `GotoBottom()` if true. Commit `7daaa3b`
- **Lesson**: auto-scroll should respect user intent — if they've scrolled away, that's an implicit "stop following"

### Garbage `]11;rgb:fae0/fae0/fae0\[1;1R` in the input box
- **Saw**: opening the TUI, the input field had escape-sequence-looking junk pre-filled; pressing Enter would send it to the LLM
- **Why**: `glamour.WithAutoStyle()` lazily probes the terminal background via an **OSC 11** query (`ESC ] 11 ; ? ESC \`). The terminal's response (`ESC ] 11 ; rgb:... ESC \`) plus a stray cursor-position report would arrive on stdin **after** bubbletea had taken over the TTY, so they were parsed as keystrokes and inserted into the textarea
- **Fix**: in `cmd/seek` we now call `termenv.NewOutput(os.Stdout).HasDarkBackground()` **before** entering bubbletea's alt-screen. The query/response handshake completes synchronously while we still own stdin. The result is passed as `Options.GlamourStyle` and glamour gets `WithStandardStyle(style)` (no runtime query). Commit `24342b3`
- **Lesson**: any library that auto-probes the terminal must run before your TUI grabs the TTY, or its probe responses will leak as input. Env override `SEEK_STYLE=dark|light` for users with weird terminals
- **Refs**: `cmd/seek/main.go:detectGlamourStyle`

### Alt-screen mode breaks scrollback, copy, and content persistence
- **Saw**: in M4 we ran with `tea.WithAltScreen()`. Symptoms accumulated: terminal scrollback dead (only an in-app viewport had history), copy worked only within the visible viewport, exiting seek wiped the whole conversation, OSC query responses had nowhere to land cleanly
- **Why**: alt-screen swaps the terminal to a secondary buffer. That buffer has no scrollback by design — it's meant for full-screen apps (vim, less) where you accept "what's on screen is all there is". For a chat-style coding agent the trade is wrong: users expect to scroll back arbitrarily, copy any prior message, and have the conversation remain in the shell after they exit
- **Fix**: M4.5.1 switches to inline mode. `tea.WithAltScreen()` removed; committed history (user prompts, tool results, completed assistant messages) is published to the terminal's native scrollback via `tea.Println`. The bubbletea live region holds only volatile state (active tools, streaming assistant text, input, status). Commit `5d1c78c`
- **Lesson**: alt-screen is the wrong default for any "conversation that has history". Use it only for genuinely full-screen modal UIs (file pickers, log viewers). Inline mode + `tea.Println` for committed content matches Claude Code, gh, gemini CLI — and it's not an accident
- **Replay 2026-05** (this lesson was relearned the hard way): a later attempt to fix layout drift switched seek BACK to alt-screen. Mouse wheel and copy broke again; see "Inline-mode drift was self-inflicted by floor-pin padding" below for the second migration back to inline

### Inline-mode drift was self-inflicted by floor-pin padding
- **Saw**: under inline mode the input line drifted off the terminal floor after a few turns (commits `16b13ef`, `59bd9a9`, `f218c57` each chased a different facet of the symptom; the eventual "fix" was to migrate to alt-screen, which broke mouse wheel and click-drag copy)
- **Why**: `View()` was padding `sb` with `strings.Repeat("\n", pad)` every frame to pin the input to the absolute terminal floor. The padding length came from a manual `scrollbackLines` counter that lost sync against `tea.ClearScreen`, multi-line `tea.Println` from `/help`, async window resize, and discarded `tea.Println` cmds (`_ = tea.Println(...)` patterns). The whole class of bugs was the *floor-pin ambition*, not inline mode itself
- **Fix**: second migration back to inline mode — `tea.WithAltScreen()` removed AND no floor-pin padding. The live region's height varies frame to frame; bubbletea's standard renderer handles that natively via cursor-up + EraseScreenBelow. The welcome banner is printed once to stdout BEFORE `tea.NewProgram` so it lives in terminal scrollback and never re-enters the redraw loop. The separator above the input is conditional on "is there live content above it" — at idle there is nothing to separate from
- **Rule**: do NOT pad the live region with trailing newlines under any condition. If you find yourself reaching for `m.height - rowsIn(...)` math, you're recomputing the M3-era drift. The cursor sits below the live region; that's correct. If the terminal looks "empty below the input", that's *also* correct — same as the shell prompt after any command
- **Lesson**: when an architectural decision (alt-screen) was made to fix a symptom (drift), question whether the symptom's root cause was correctly identified. The drift wasn't inherent to inline mode — it was inherent to *trying to outsmart the renderer*. Claude Code, gh, and gemini CLI all run inline without any floor-pin math; they look stable because they don't try to control where the live region lands
- **Refs**: `internal/tui/run.go`, `internal/tui/view.go:View`, related entry "Alt-screen mode breaks scrollback, copy, and content persistence" above and the M3-era "Status bar scrolled away with live content" / "/clear left input pinned to TOP" entries below for the original symptom set
- **Refs**: PRD §4.9, `internal/tui/run.go`, related entry "Mouse drag-to-select did nothing"

### Esc-cancelled stream silently dropped the user's prompt
- **Saw**: pressing Esc mid-stream to cancel an assistant's response also erased the user's prompt from the input box. The user had to re-type it to re-submit
- **Why**: the Esc handler in `handleKey` cleared `queuedText` ("" = empty string) to "stop everything", but the user's submitted prompt was already sitting in `queuedText` at that point. The textarea had been cleared on Enter, so nothing was recoverable
- **Fix**: Esc now restores `queuedText` into the textarea before clearing it; `streamEndMsg` also restores `promptHistory` into the textarea if the stream was cancelled and the input is still empty. Commit `af4b4d0`
- **Lesson**: when you clear volatile state on user-cancel, check whether that state contains something the user would want back. "Cancel the current operation" and "clear the edit buffer" are different intents — treat them separately
- **Refs**: `internal/tui/update.go:handleKey`, `internal/tui/update.go:streamEndMsg`

### Status bar scrolled away with live content instead of staying pinned to the terminal bottom
- **Saw**: after a few turns, the status bar drifted upward from the terminal's bottom edge. In long sessions it could be several lines above the bottom, making it hard to spot at a glance
- **Why**: inline mode replaces the previous View() in-place, but `tea.Println` lines accumulate above the live region, pushing the cursor (and thus the View() output) downward. The status bar, rendered at the end of View(), moved with everything else — the comment said "not pinned" but the user expectation was that a status bar stays at the screen edge
- **Fix**: added `scrollbackLines` counter to Model, incremented at every `tea.Println` call site (counting actual newlines). `View()` now calculates `cursorRow = welcomeFixedLines + scrollbackLines` and adds padding before the status bar to fill the remaining terminal height, pinning it to the bottom. Commit (this one)
- **Lesson**: "status bar at the bottom" requires knowing where the cursor is in the terminal. In inline bubbletea, `tea.Println` moves the cursor; you must track the cumulative offset to compute remaining vertical space. A `scrollbackLineCount` helper and explicit counting at each print site is verbose but reliable
- **Refs**: `internal/tui/model.go:scrollbackLines`, `internal/tui/view.go:View()`

### `View()` rendered "starting seek …" for a frame
- **Saw**: a brief flash of the literal string "starting seek …" on launch
- **Why**: `View()` returned a placeholder string when `m.ready == false` (i.e. before the first WindowSizeMsg); even the synthetic one needs one Update cycle
- **Fix**: render the welcome banner in the not-ready branch instead of the placeholder — same content the viewport will hold once we know the dimensions, so no visual jump. Commit `766a045`
- **Lesson**: a "loading" message that shows for less than one frame should just be a softer first frame, not a string change

### `_ = tea.Println(line)` silently discards the cmd — scrollbackLines count drifts
- **Saw**: functions returning `tea.Model` (not `(tea.Model, tea.Cmd)`) called `_ = tea.Println(line)` and incremented `scrollbackLines` — but the line never appeared in the terminal because discarding `tea.Println`'s return discards the print itself. The phantom `scrollbackLines` count shifted the status bar padding calculation in `View()` by the number of discarded prints
- **Why**: `tea.Println` returns a `tea.Cmd`. Assigning it to `_` means bubbletea never executes it. The pattern in `distillAcceptCurrent` and `exitDistillReview` existed before the `scrollbackLines` counter was added (commit bb27684), but the new counter made the bug visible: phantom lines were counted in `scrollbackLines` but never rendered, causing `cursorRow = welcomeFixedLines + m.scrollbackLines` to over-count and the status bar to sit lower than it should after distill review
- **Fix**: changed `distillAcceptCurrent`, `distillDropCurrent`, `enterDistillEdit`, and `exitDistillReview` from `tea.Model` → `(tea.Model, tea.Cmd)` so the `tea.Println` cmd propagates through the key handler to `tea.Batch`. Added `TestWelcomeBannerLineCount` to pin the `welcomeFixedLines` constant against the actual banner output. Commit (this one)
- **Lesson**: `_ = tea.Println(s)` is a silent no-op pattern that looks like it prints but doesn't. Any function returning `tea.Model` that calls `tea.Println` is a pre-existing bug — the callers pass `nil` as the cmd, so the line is lost. When adding instrumentation that depends on side effects at the same call site (like `scrollbackLines`), check whether the print actually fires or you're counting ghosts
- **Refs**: `internal/tui/distill.go:exitDistillReview`, `internal/tui/distill.go:distillAcceptCurrent`, `internal/tui/banner.go:welcomeFixedLines`, `internal/tui/banner_test.go:TestWelcomeBannerLineCount`

### `/model` updated the display but not the Agent's model — API calls still used the old model
- **Saw**: user ran `/model deepseek-v4-pro` in the TUI; status bar showed `deepseek-v4-pro`, but DeepSeek's backend stats showed all calls as `deepseek-v4-flash`. The model switch had zero effect on actual API requests
- **Why**: `cmdModel` (and `applyModelChoice` for the picker) only updated `m.opts.Model` (status bar display) and called `SetModel(args)` (updated `sessionModel` in main.go). But the Agent's `a.cfg.Model` is frozen at creation time (`agent.New`), and `/model` never rebuilt the agent or updated its config. `RebuildAgent` is only called on `/new`. So every subsequent API call still used the original model from startup
- **Fix**: added `Agent.SetModel(model)` in `pkg/agent/agent.go` that mutates `a.cfg.Model` in place (safe between turns). Both `cmdModel` and `applyModelChoice` now call `m.opts.Agent.SetModel(args)` after updating the display. Commit (this one)
- **Lesson**: any struct whose config is baked at construction and read on every operation must expose a mutator if a CLI/TUI command claims to change the config at runtime. "Effective on next prompt" requires the agent's copy changed, not just the host variable and status bar
- **Refs**: `pkg/agent/agent.go:SetModel`, `internal/tui/commands.go:cmdModel`, `internal/tui/commands.go:applyModelChoice`

### `/lang` only updated the display and session variable, not the live Agent → no effect until `/new`
- **Saw**: running `/lang zh` in the TUI updated the status bar and session file, but the LLM continued responding in English. The change only took effect after `/new`, which also lost the conversation history
- **Why**: the language directive was embedded in the system prompt (frozen at agent creation time). `/lang` only updated `sessionLang` (the host variable) and `m.opts.Lang` (the status bar display), but never modified the live Agent's message state. The LLM continued to see the original system prompt with the old language directive. Unlike `/model` (which had `Agent.SetModel`) and `/effort` (which had `Agent.SetEffort`), there was no `Agent.SetLang()` at all — per-message injection via `langReminder` + `workflowReminder` was chosen over system-prompt mutation because it avoids rebuilding the entire system prompt template while keeping the instruction in the most recent context (recency bias)
- **Fix**: added `Lang` to `agent.Config`, `Agent.SetLang(lang)` to update it, and `langReminder(lang)` which returns a per-message language suffix injected into every user turn before `workflowReminder`. The suffix only activates when `lang="zh"`; `""` or `"en"` means no suffix (system prompt directive suffices). Updated `applyLangChoice` in the TUI to call `m.opts.Agent.SetLang(value)` so the live agent's config is updated immediately. Commit (this one)
- **Lesson**: every per-session setting that claims to change behaviour on-the-fly must update the live Agent instance, not just the host variable and display. Per-message injection (like `workflowReminder`) is a viable alternative to system-prompt mutation when the system prompt template is complex and the setting's scope is "from the next user message onward"
- **Refs**: `pkg/agent/agent.go:SetLang`, `pkg/agent/agent.go:langReminder`, `internal/tui/commands.go:applyLangChoice`, `cmd/seek/main.go:SetLang`

### `/clear` left the input pinned to the TOP of the terminal instead of the bottom
- **Saw**: after `/clear` (or `Ctrl+L`), the input box and status bar rendered at row 1 of the visible viewport with empty space below — breaking the load-bearing "input is always at the bottom" invariant. The bug surfaced as the input glided back down only after the next streamed turn pushed enough content to fill the screen
- **Why**: `cmdClear` returned `tea.ClearScreen` (ANSI `\x1b[2J\x1b[H` — erase visible + cursor home) but left `m.scrollbackLines` at its pre-clear value. View()'s two bottom-pinning branches both depend on this counter being truthful: the welcome-pad branch (`view.go:138`) was gated on `m.turns == 0` (a stale proxy that excluded post-clear state); the status-bar-pin branch (`view.go:211`) computed `cursorRow = welcomeFixedLines + scrollbackLines` — with a stale 47, `remaining` went negative and no padding was emitted. Result: the next View() drew the live region wherever the cursor parked, which after ClearScreen is row 1
- **Fix**: (1) `cmdClear` now resets `m.scrollbackLines = 0` (truthful: nothing is visibly above). (2) `Ctrl+L` in `update_key.go` does the same — it bypasses `cmdClear` so the reset has to be duplicated. (3) Welcome-pad gate changed from `m.turns == 0` to `m.scrollbackLines == 0` — the more direct expression of the actual invariant ("nothing has been Println'd above us"). Commit (this one)
- **Lesson**: `tea.ClearScreen` is a write-only side effect — bubbletea never tells the Model "the screen is empty now". Any state that mirrors terminal layout (cursor row, scrollback line count, padding math) must be reset by hand at the same call site. Using a derived state (`turns == 0`) as a proxy for a layout fact (`scrollbackLines == 0`) sets up exactly this kind of skew the moment something else changes the underlying fact
- **Refs**: `internal/tui/commands.go:cmdClear`, `internal/tui/update_key.go` (KeyCtrlL case), `internal/tui/view.go:138`, `internal/tui/view.go:211`

### Esc-cancel prompt restore can leak a prior chat prompt into the API-key / branch-name field
- **Saw**: user presses Esc to cancel a streaming turn, then opens `/setup` to configure an API key. The prior chat prompt is restored into the textarea, and on Enter it's saved as the API key into `~/.seek/config.json`. Same path for `/review` branch-name entry
- **Why**: `handleStreamEnd` unconditionally restores `m.promptHistory[last]` into `m.input` when the textarea is empty (line 68). It had no guard for `m.setupKeyEntry` or `m.reviewBranchEntry` — the branch that reads the input for API key / branch name. The restore fires for any Esc-cancelled stream regardless of which mode the user is currently in
- **Fix**: added `!m.setupKeyEntry && !m.reviewBranchEntry` guards to the Esc-cancel restore condition. The two special-entry modes now skip the prompt restore. Also added paste-fold resolution to the same two branches (same leak vector: a folded paste marker would be read as the actual value)
- **Lesson**: any time you write into a shared textarea from a background event handler, check which "input mode" is active. The Esc-cancel restore is a convenience feature for the normal chat flow; it must be a no-op when the textarea serves a different purpose
- **Refs**: `internal/tui/update_agent.go:68`, `internal/tui/update_key.go` (reviewBranchEntry + setupKeyEntry branches)

### Agent.Messages() races with the Prompt goroutine when called from TUI (Ctrl+C, streamEnd)
- **Saw**: CI `-race` failure when Ctrl+C is pressed during a streaming turn. `persistSession` in `update_key.go:226` calls `Agent.Messages()` which copies `a.messages` without synchronisation while the Prompt goroutine simultaneously appends the next message. In production (race detector off) this can produce a torn slice header and a corrupted saved session
- **Why**: `Agent` had a `sync.RWMutex` field but it was never used — no lock was acquired anywhere. `Messages()` copied the slice header with a plain `len()` + `copy()`, and all `a.messages = append(...)` writes in the Prompt goroutine were unprotected. The struct docstring even said "NOT safe for concurrent calls" but `persistSession` was called from the TUI goroutine (Ctrl+C, streamEnd, approval) while Prompt runs in its own goroutine
- **Fix**: added `mu.RLock()`/`mu.RUnlock()` to `Messages()`, `mu.Lock()`/`mu.Unlock()` to `Reset()`, and a new `appendMessage()` helper that locks before appending. All write sites in `Prompt()` now use `a.appendMessage(...)` instead of raw `a.messages = append(a.messages, ...)`
- **Lesson**: "NOT safe for concurrent calls" in a docstring is a promise to callers, not a defence. If a method is public (`Messages()`) and there is any code path that calls it from a different goroutine than the one that writes, add the lock. `-race` will catch it, but you want the fix before CI runs — not after.
- **Refs**: `pkg/agent/agent.go`, `internal/tui/update_agent.go:persistSession`, `internal/tui/update_key.go:226`

### /clear (cmdNew) swaps session/tracker before checking RebuildAgent error, leaving half-mutated state
- **Saw**: `/new` fails with a transient network error (RebuildAgent timeout). Before the error check, the code already swapped `m.opts.Session` to a fresh empty session and reset `m.opts.Tracker`. The old agent with its message history survives, but the new session file (empty) was already written to disk and the usage tracker (zeroed) lost the cumulative cost. Subsequent user prompts send the OLD messages (m.opts.Agent unchanged) through the API but record them under the NEW session ID with zero cost tracked
- **Why**: the original code ordered operations as: persist old session → create new session + save to disk → reset tracker → call RebuildAgent → check error. The error check happened last, but the two state mutations (session swap, tracker reset) were already committed
- **Fix**: reordered to: persist old session → call RebuildAgent → check error → (only on success) swap session + reset tracker
- **Lesson**: when a function has multiple state-mutating steps and the last one can fail, order the steps so that the failure-prone one comes first and the mutations only happen after it succeeds. "Validate then mutate" applies at the function level too.
- **Refs**: `internal/tui/commands.go:cmdNew`

### replay.go skipped RoleTool messages and raw-rendered Markdown — --resume scrollback diverged from live
- **Saw**: every `--resume`d session with tools showed no tool results, raw Markdown source instead of styled output, and spurious `▸ seek` blocks for reasoning-only turns. Users seeing their scrollback change shape after resume lost trust in the replay feature
- **Why**: `renderReplayHistory` unconditionally skipped `deepseek.RoleTool` messages (line 32: `continue`). `formatReplayAssistant` used raw `msg.Content` instead of `renderMarkdown()`. It also emitted a `▸ seek` block for any assistant message with tool_calls, even when `msg.Content == ""` — the live path at `update_agent.go:178` gates on `m.curContent != ""`. Tool call arguments were not truncated while the live path uses `truncateOneLine(args, 80)`
- **Fix**: added RoleTool rendering as `↳ name(args) → N bytes` lines using a `toolCallMap` tracked across messages. Added glamour Markdown rendering via `newMarkdownRenderer()`. Returned `""` from `formatReplayAssistant` when `msg.Content == ""` (pure tool-call / reasoning-only). Applied `truncateOneLine(args, 80)` to tool-call arguments
- **Lesson**: "replay should mirror live" is a documented invariant. Every divergence between replay.go and the live committed-renderer path (view.go + update_agent.go) is a bug. When adding a new display feature to the live path, update replay.go in the same commit or the --resume path silently drifts.
- **Refs**: `internal/tui/replay.go`, `internal/tui/update_agent.go:178`, `internal/tui/run.go:56`

### `initialSizeCmd` clobbers `teatest.WithInitialTermSize` in test environments
- **Saw**: writing a teatest case with `WithInitialTermSize(80, 40)`; the model still rendered as 80x24, clipping the help overlay panel so the title scrolled off-screen and assertions on top-of-panel strings ("Help — seek") timed out even though the overlay was clearly rendering
- **Why**: `Init()` returns `initialSizeCmd()` which calls `term.GetSize(os.Stdout.Fd())`. In `go test` stdout isn't a TTY, so it fails and the function falls back to the hard-coded `(80, 24)`, sending a `WindowSizeMsg` that overrides whatever size teatest provided. Even sending an explicit `WindowSizeMsg` after `NewTestModel` racing against `initialSizeCmd`'s goroutine is non-deterministic
- **Fix**: assert on strings that ARE in the bottom-of-panel visible viewport ("Shift+Tab", "Scrollback:") instead of the title. Document the constraint inline in the test so the next writer doesn't fight it. Commit (this one)
- **Lesson**: when adding teatest cases, design assertions for a 24-row viewport — bubbletea writes only what fits, not the full rendered buffer, regardless of what your code generates. If you genuinely need a taller viewport, patch `initialSizeCmd` to honour a test-only env var rather than racing it
- **Refs**: `internal/tui/model.go:initialSizeCmd`, `internal/tui/render_test.go`

### `tm.Output()` is a single-use reader — sequential `WaitFor` calls drain it
- **Saw**: a test with two consecutive `teatest.WaitFor(t, tm.Output(), ...)` calls: first asserted on string A (passed), second on string B (timed out with empty buffer), even though both A and B were clearly in the program's output dump from the first failure
- **Why**: `tm.Output()` returns the same underlying pipe/reader on every call. `teatest.WaitFor` reads bytes from it incrementally; once the predicate matches it stops, and the bytes it consumed are gone. The second `WaitFor` reads from the same position — anything not since written by the program is unavailable. When the program is idle (e.g. help overlay rendered once, no spinner re-renders) there are no new bytes
- **Fix**: one `WaitFor` per test; if multiple signals must be checked, compose them into a single predicate (`bytes.Contains(b, a) && bytes.Contains(b, c)`). Commit (this one)
- **Lesson**: treat `tm.Output()` as a stream, not a buffer. Multiple sequential reads work only when the model produces new output between them — never assume earlier bytes are still readable
- **Refs**: `internal/tui/render_test.go` (the `waitFor` helper takes a single needle for this reason)

### Ctrl+J newline inserts via `InsertString("\n")` broke cursor/viewport scrolling
- **Saw**: typing Ctrl+J (multi-line newline) when the textarea had 3+ lines of content caused the cursor to disappear or appear at the wrong position — the first line didn't scroll up as new content was added below
- **Why**: `handleKey` intercepted `tea.KeyCtrlJ` and called `m.input.InsertString("\n")` to insert a newline. `InsertString` writes the character but bypasses the textarea's `Update()` method entirely, so it never updated the internal cursor position, line tracking, or viewport scroll offset. The cursor ended up off-screen
- **Fix**: removed the `case tea.KeyCtrlJ:` intercept entirely. The textarea already has `ta.KeyMap.InsertNewline.SetKeys("ctrl+j")` configured (model.go:311), so Ctrl+J now falls through to `m.input.Update(msg)` and the textarea handles it natively, including viewport scrolling and cursor tracking. Commit (this one)
- **Lesson**: never bypass a bubbletea component's `Update()` for key-driven mutations — `InsertString` is a data-level operation that skips the component's state machine (cursor, scroll, visual offsets). If a key binding already exists on the component (`InsertNewline`), let the event reach it naturally
- **Refs**: `internal/tui/update.go:handleKey` (removed KeyCtrlJ case), `internal/tui/model.go:311`

### `welcomeBelowLines` mismatch causes bottom block to jump 4 lines on first send
- **Saw**: after pressing Enter to send the first message, the status bar and bottom rule visibly shifted upward by 4 lines relative to their position on the welcome screen. The welcome screen's padding put them just off the bottom of the viewport; the post-send layout put them at the correct terminal bottom, creating a jarring jump
- **Why**: `welcomeBelowLines = 4` (input + status) but the actual rendered bottom block is 7 lines: separator(1) + input(3 + trailing \n = 4) + status(1) + rule(1). The welcome screen formula `height - 14 - 4` under-counted by 3 lines, causing the view to overflow the viewport. The active-chat bottom-pin formula uses dynamic `bottomHeight + 2` which correctly accounts for all components, so the transition between the two padding regimes produced a visible shift
- **Fix**: changed `welcomeBelowLines` from 4 to 7. The padding formula now gives `height - 14 - 7 = height - 21`, which exactly fills the viewport. Updated test expectations (12→9, 42→39). Commit (this one)
- **Lesson**: `welcomeBelowLines` must be manually kept in sync with the actual bottom block components rendered in `View()`. Any addition or removal (separator, rule, textarea height change, extra `\n` after input) requires updating it. The active-chat path avoids this by measuring bottomBuf height dynamically — consider refactoring the welcome path to do the same if bottom components change again
- **Refs**: `internal/tui/banner.go:welcomeBelowLines`, `internal/tui/view.go:View()`, bubbletea textarea `View()` padding loop (always emits `\n` after each of `m.height` lines)

---

## Agent loop

### Esc mid-stream poisoned the session: orphan `tool_calls` rejected on every subsequent turn
- **Saw**: after pressing Esc during a turn, the next prompt failed instantly with `An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'`. Every retry produced the same error — session effectively bricked
- **Why**: `pkg/agent/agent.go` had `runTurn` return a nil error when the SSE stream was cut by ctx cancellation, leaving `finish=""` and a partially-assembled assistant message. The outer loop appended the assistant message to history, then broke out (because `finish != "tool_calls"`) WITHOUT running the tools. Result: the assistant message carried tool_call IDs with no matching tool result messages, and DeepSeek rejects that shape on the next API call
- **Fix**: `runTurn` now checks `ctx.Err()` after the stream loop drains and returns it as an error; `Prompt` detects user-cancel via `errors.Is(err, context.Canceled)` and bails BEFORE appending the partial assistant message. Existing on-disk sessions are repaired by `session.Repair()`, called from `cmd/seek` after Load. Commit `986a485`
- **Lesson**: when an LLM message carries `tool_calls`, it's a paired structure with matching `tool` messages — never persist the head without the tail. Any code path that can be interrupted between "assistant tool_calls emitted" and "all tools dispatched" needs to either complete the pair (synthesize stub tool results) or discard the head
- **Refs**: `pkg/agent/agent.go:runTurn`, `internal/session/session.go:Repair`

### Followup: ctx-cancel was one of FOUR paths to the same orphan state
- **Saw**: the original 986a485 fix only covered user-cancel. Boundary stress tests added later proved three more routes to identical corruption: server hangs up without `[DONE]`, SSE chunk fails JSON decode, server emits `finish_reason="stop"` alongside `tool_calls` (against spec but possible through proxies). All produced the same orphan `tool_calls` in history; all were undetectable by the original cancel-path test alone
- **Why**: the invariant lives at the `tool_calls ↔ finish_reason` coupling, not at "ctx was cancelled". Any path that returns from `runTurn` with `len(ToolCalls)>0 && finish != "tool_calls"` is corrupt. The original fix happened to address one cause (ctx-cancel sets `finish=""` indirectly via the stream's premature close); the others share the symptom but not the cause
- **Fix**: explicit invariant check in `Prompt` between `runTurn` and the append: refuse to commit a turn whose tool_calls don't match its finish_reason, emit `ErrorEvent` instead. Commit `d022ee0`
- **Lesson**: when fixing a bug whose symptom is a state-shape violation, write the test against the SHAPE not the cause. "Pressed Esc → history broken" is the cause-shaped test; "any path that produces tool_calls without finish=tool_calls → history broken" is the shape-shaped one. The shape-shaped version generalises and catches the regressions the cause-shaped one misses
- **Refs**: `pkg/agent/agent.go` Prompt invariant check; test battery `TestAgent_StreamTruncatedMidToolCall`, `TestAgent_FinishReasonMismatch_DropsOrphanToolCalls`, `TestAgent_DecodeErrorMidStream_DropsTurn`, `TestAgent_MultiTurn_RoundTripsCleanHistory`

### Symlinks inside CWD let `write`/`edit` escape the CWD gate
- **Saw**: a symlink `<cwd>/escape → /tmp/other` accepts writes via `Check(KindWrite, Path: "<cwd>/escape/x")` even in `ModeDeny`. The file lands at `/tmp/other/x` — outside the working directory the policy is supposed to protect
- **Why**: `permission.isWithin` works on path strings (filepath.Abs + filepath.Rel). It does NOT resolve symlinks via `filepath.EvalSymlinks` before comparing, so the policy sees "this path starts with cwd → allow". The subsequent `os.WriteFile` follows the symlink and writes outside
- **Fix**: NOT fixed in this commit — pinned by `permission.TestIsWithin_SymlinkInsideCWDPointingOutsideIsAllowed` and `write.TestWrite_SymlinkInsideCWDLetsContentEscape`. Rationale: seek's threat model is single-user local tool (a user who wanted to write outside cwd could run `bash` directly anyway), AND symlinked vendor/build/cache dirs are legitimate workflows that EvalSymlinks would break
- **Lesson**: path-based access control sees a graph different from what the filesystem layer below it walks. For a hardened multi-tenant tool this would be a CVE; for seek it's a documented limitation. Either way, pin the current behaviour so any tightening is deliberate
- **Refs**: `internal/permission/permission.go:isWithin`; test cross-references above

### `Policy.mode` was raced by /yolo flips against concurrent `Check`
- **Saw**: a `-race` test on concurrent Check + SetMode lit up four data-race warnings on the `Policy.mode` field. In production this could fire when the user pressed `/yolo` while a tool dispatch was in flight (TUI goroutine writes mode, agent goroutine reads it via Check)
- **Why**: the Policy struct held `mode` and `askFn` as plain fields with no synchronization. The bug never manifested before because tool dispatch is sequential and `/yolo` between dispatches was the common case — but `-race` catches the *contract* violation regardless of timing luck
- **Fix**: `sync.RWMutex` on Policy; Check snapshots mode/askFn/cwd under RLock and releases BEFORE calling askFn (which blocks on the user for arbitrary time). Commit `73c5f3d`
- **Lesson**: a permission gate is a synchronization primitive whether you designed it as one or not. The moment any field is mutable post-construction, it needs the mutex. Discovered by writing a `-race` test specifically for the concurrent path, which is exactly the discipline AGENTS.md now requires
- **Refs**: `internal/permission/permission.go:Policy`; test `TestCheck_ConcurrentCallsRaceFree`

### `json.Unmarshal` silently drops unknown fields — LLM typos produce useless errors
- **Saw**: model called `list_dir({"directory": "/path", "depth": 1})` (wrong field name). Error returned: `list_dir: path is required` — no mention of `directory` being unknown, no hint about valid fields. Model retried with identical args and hit the same wall
- **Why**: Go's `json.Unmarshal` drops unknown fields silently. The `directory` key was ignored, `path` stayed zero-value, and the following nil check produced the generic "required" error with zero diagnostic value
- **Fix**: `internal/tools/tool.go` introduces `UnmarshalStrict` (uses `json.Decoder.DisallowUnknownFields`) and `MissingField` helpers. Error now reads: `list_dir: bad arguments: json: unknown field "directory". Got: {"directory":...}. Valid fields: path, depth, show_hidden`. All eight tool `Execute` methods updated. Regression test: `TestListDir_UnknownFieldSurfacesActionableError`. Commit (this one)
- **Lesson**: any `json.Unmarshal` target in an LLM tool boundary must use `DisallowUnknownFields`. Silent drops make self-correction loops impossible — the model has no information to act on
- **Refs**: `internal/tools/tool.go:UnmarshalStrict`; `internal/tools/listdir/listdir_test.go`

## DeepSeek API

### `reasoning_content` rule for V4 thinking is the OPPOSITE of the old reasoner — conditional on tool_calls
- **Saw**: a multi-turn V4 thinking-mode session with a tool_call in turn 1 hit a 400 on turn 2: `The 'reasoning_content' in the thinking mode must be passed back to the API`. We had `pkg/deepseek.StripReasoningContent` unconditionally clearing the field on every assistant message — exactly the wrong shape for V4
- **Why**: V4 changed the contract (api-docs.deepseek.com/guides/thinking_mode). Pre-V4 `deepseek-reasoner` REJECTED requests that retained prior `reasoning_content`. V4 thinking REVERSES that for tool-call turns specifically: "if the model performed a tool call, the intermediate assistant's `reasoning_content` must participate in the context concatenation and must be passed back to the API in all subsequent user interaction turns." For plain assistant turns (no tool_calls) `reasoning_content` may still be dropped to save prompt tokens
- **Fix**: `StripReasoningContent` is now conditional — keeps `reasoning_content` on assistant messages where `len(ToolCalls) > 0`, strips it everywhere else. Wire-level test in `TestAgent_PreservesReasoningContentOnToolCallTurns` pins both branches by asserting the resend body. Earlier "always strip" pitfall (previous text of this entry) was correct for pre-V4 and is now archived inline below
- **Lesson**: provider contracts can REVERSE between major versions of the same vendor's models. A pitfall that was load-bearing for one generation can be 100% wrong for the next. When upgrading model families, re-validate every "must" you previously wrote down — don't trust the old guide. The doc reference matters more than the symptom file when it's been a year
- **Refs**: `pkg/deepseek/stream.go:StripReasoningContent`, `pkg/agent/agent_test.go:TestAgent_PreservesReasoningContentOnToolCallTurns`, api-docs.deepseek.com/guides/thinking_mode

  Archived pre-V4 rule (kept for historical context, do NOT re-apply unconditionally):
  > `deepseek-reasoner` (pre-V4 standalone model, retired 2026-07-24) rejected requests that retained prior `reasoning_content`. That was true at the time. V4 thinking mode flipped it for tool-call turns.

### Prefix cache hits are best-effort and short prompts don't trigger
- **Saw**: three back-to-back identical short prompts (110 tokens each) all showed `prompt_cache_hit_tokens=0`
- **Why**: DeepSeek's prefix cache is disk-backed and "best effort". A prefix needs to be at least ~64 tokens (and there's no SLA — the server may evict even longer ones)
- **Fix**: nothing to "fix" code-wise; documented in `docs/prd/v0.md §4.8.1`. Our M2 smoke (system prompt + 5 tools = ~2KB prefix) consistently hits 70%+ across turns; the M0 11-token smoke can't
- **Lesson**: "cache hit ratio" benchmarks should always use realistic-sized prompts; otherwise you'll conclude "the cache doesn't work" when it does

### Empty tool result + `omitempty` on `Message.Content` → "missing field `content`" from DeepSeek
- **Saw**: a session crashed mid-loop with `deepseek api error: invalid_request_error: Failed to deserialize the JSON body into the target type: messages[15]: missing field 'content'` right after a `memory_observe(...) → 0 bytes` call
- **Why**: `memorytool.Observe.Execute` returns `("", nil)` by design — its enqueue is async, so the synchronous return is a "succeed silently" signal. But `deepseek.Message.Content` is tagged `json:"content,omitempty"`, so the tool-result message with `Content=""` serialised with no `content` key at all. DeepSeek strictly requires the field on tool-role messages and rejects the whole turn. Removing `omitempty` isn't an option — assistant messages that only carry `tool_calls` legitimately omit content
- **Fix**: centralise tool-result message creation in `pkg/agent.buildToolResultMsg` and substitute `"(no output)"` when the tool returns empty + nil error (error path still wins via the `tool error: ...` prefix). Retrofit `session.Session.Repair` with `backfillEmptyToolContent` so historical sessions saved before this guard can still resume. Pinned by `TestBuildToolResultMsg`, `TestAgent_EmptyToolResult_WirePresent`, and `TestRepair_BackfillsEmptyToolContent`
- **Lesson**: `omitempty` on a shared struct field is a hidden contract risk — a field that's legitimately optional in one role/context can be mandatory in another. When designing a "silent success" return value, also check what the *next* serialisation step does with an empty string
- **Refs**: `pkg/agent/agent.go:buildToolResultMsg`, `internal/session/session.go:backfillEmptyToolContent`, `internal/tools/memorytool/observe.go`

### `reasoning_effort` values are `high|max` on DeepSeek V4 — not OpenAI's `low|medium|high`
- **Saw**: while wiring the `/effort` TUI command, the existing comment on `ChatRequest.ReasoningEffort` claimed `"low"|"medium"|"high"` (an OpenAI o-series carry-over). Building a 3-rung picker against that surface would have exposed values DeepSeek may silently ignore or reject
- **Why**: DeepSeek V4 documents only two `reasoning_effort` levels — `high` and `max` — alongside the `thinking.type` toggle. The earlier OpenAI-style trio looked correct because the field name matches, but the value sets are not interchangeable
- **Fix**: corrected `pkg/deepseek/types.go:103` comment to document `high|max`; `/effort` exposes `off|high|max` only; `internal/tools/think` keeps `high` as its baseline and bumps to `max` when the session is already at `high`. Wire pins in `TestAgent_EffortOverridesThinking` and `TestThink_BumpEffort`
- **Lesson**: when a Go field's name matches a sibling provider's parameter, the **value enum is not** part of the name match. Re-read the vendor's doc page for the value set every time, even when the field looks "the same"
- **Refs**: `pkg/deepseek/types.go`, `internal/tools/think/think.go:bumpEffort`, `internal/tui/commands.go:effortChoices`

### FIM endpoint is at `/beta/completions` with legacy OpenAI shape
- **Saw**: when wiring the FIM client I tried to send it to `/chat/completions` with a `prompt` field — got 400 back
- **Why**: DeepSeek's fill-in-the-middle lives at `/beta/completions` (note the path) and uses the **legacy OpenAI text-completion schema**: `{prompt, suffix, ...}` → `{choices[0].text, ...}` — not the chat/messages shape
- **Fix**: `pkg/deepseek/fim.go` uses dedicated `FIMRequest` / `FIMResponse` types and the right endpoint. Commit `2af3b62`
- **Lesson**: don't assume an OpenAI-compatible provider's auxiliary endpoints follow the chat shape. Read the docs end to end before extending the client
- **Refs**: `pkg/deepseek/fim.go`, `pkg/deepseek/client.go:endpointFIM`

### Reasoner doesn't support `tools`, `temperature`, `top_p`, etc.
- **Saw**: a request to `deepseek-reasoner` with a `tools` array came back 400
- **Why**: reasoner is configured as a pure CoT-then-answer model; tool calling, sampling parameters, function calling, and parallel tool calls are all unsupported
- **Fix**: the `think` tool calls reasoner via a fresh, parameter-free `Chat()` call (no tools, no temperature) using a history of just `[system, user]`. Commit `8264f52`
- **Lesson**: a "model" in the API can be a parameter-restricted variant. Maintain a per-model capability map if you switch between them programmatically
- **Refs**: `internal/tools/think/think.go`

### FIM usage ratio is inherently low — model prefers edit/write over fim_complete
- **Saw**: both benchmark tasks (`self-hosting`, `fim-patch`) showed FIM ratio well below 50% (0% and 11% respectively) despite `fim-patch` being explicitly designed as a small inline edit. The model consistently chose `edit`/`write` instead of `fim_complete`.
- **Why**: two compounding causes. (1) Denominator: ratio was `fimCalls / totalToolCalls`, which counts `read`/`bash`/`grep` — tools that *can't* use FIM — diluting the numerator. (2) Pairing constraint: a `fim_complete` call doesn't write to disk; its output must be applied by a subsequent `edit` (book ch6 §6.5). So `FIM / (FIM+edit)` caps at ~50% even when the model takes the FIM path every chance it gets. (3) The model's training corpus also biases toward `edit`/`write` for general-purpose tools, but that's secondary to the math.
- **Fix**: PRD §6 acceptance criterion was revised in commit `0e4153e` from "ratio ≥ 50%" to per-task absolute count `FIMMinCalls` (`self-hosting: 0`, `fim-patch: 1`). The ratio still appears in the JSON report for visibility but no longer participates in the pass/fail verdict. Designing a meaningful ratio metric (candidate: FIM-saved tokens / total prompt tokens) was deferred to v1.1, when there's more real-world usage data to anchor it.
- **Lesson**: when a metric's denominator includes terms that can never be the numerator, the ceiling is structurally lower than you think. Verify the upper bound of any "X / Y ≥ Z%" metric *before* setting Z, especially for pairing constraints like "every FIM needs a follow-up edit". An aspirational threshold that turns out to be mathematically unreachable trains everyone to ignore the metric.
- **Refs**: `cmd/seek/benchmark.go`, `docs/prd/v0.md §6 / §8`, commit `0e4153e`

### Streamed tool calls arrive as deltas keyed by `index`
- **Saw**: first attempt at handling tool calls assumed each chunk contained a complete `tool_call` — wound up with split, garbled JSON in arguments
- **Why**: DeepSeek streams tool calls in fragments: chunk N might emit `{"index":0, "id":"call_1", "function":{"name":"read", "arguments":"{\"pa"}}` and chunk N+1 emits `{"index":0, "function":{"arguments":"th\":\"x\"}"}}`. They must be merged by `index`
- **Fix**: `pkg/agent` accumulates a `map[int]*ToolCall` during the stream, finalising on `EventDone`. Added `Index` field to `deepseek.ToolCall` (omitempty so it doesn't leak into request bodies). Commit `719b84e`
- **Lesson**: streaming tool calls need explicit assembly state. Read the streaming spec, not just the non-streaming one

### "Respond with JSON only" instructions to the reasoner are unreliable
- **Saw**: `/distill` and `seek -dream` both ask the reasoner for a strict JSON array via system prompt. The reasoner happily produces ` ```json\n[...]\n``` ` fences, "Here are the candidates:" prose preambles, or a single `{...}` object when there's exactly one candidate — despite the instruction
- **Why**: chain-of-thought training pulls the model toward "explain itself" output. The system prompt is a strong nudge but not a hard constraint — and there's no JSON-mode flag on `deepseek-reasoner` (it doesn't accept the standard `response_format` parameter most chat models take)
- **Fix**: every reasoner-output parser ships with three tolerances: strip leading ` ``` ` / ` ```json ` fence + trailing ` ``` `; trim leading prose up to the first `[`/`{`; accept single objects as 1-element arrays. See `internal/memory/distill.go:ParseCandidates` and `dream.go:ParseLCandidates`
- **Lesson**: never rely on a "respond with JSON only" instruction alone, especially for reasoner-class models. Build the tolerant parser the moment you wire the first reasoner call; the fence wrap and prose preamble WILL show up
- **Refs**: `internal/memory/distill.go`, `internal/memory/dream.go`

### `deepseek-reasoner` without `Thinking` parameter silently behaves like fast-chat
- **Saw**: picking `deepseek-reasoner` produced fast-chat output — no chain-of-thought, no `reasoning_content` field in responses despite the model id selecting "the reasoner"
- **Why**: V4 made thinking a request-level parameter (`Thinking.Type="enabled"`) rather than a separate model id. DeepSeek kept the legacy `deepseek-reasoner` alias alive for backwards compat but per their docs (api-docs.deepseek.com) it corresponds to "V4-Flash + thinking mode" — and **sending the alias without also setting `Thinking` falls back to plain V4-Flash, no CoT**. Same opt-in pattern for `deepseek-v4-pro`. NOTE: an earlier version of this entry guessed the alias routed to V4-Pro; it does not — DeepSeek's docs explicitly map it to V4-Flash, and the alias is scheduled for removal 2026-07-24
- **Fix**: `pkg/deepseek.ShouldEnableThinking(model)` is the canonical "is this a reasoning model" predicate (currently `ModelV4Pro` + `ModelReasoner`). `pkg/agent.runTurnDeepSeek` consults it and sets `req.Thinking = &ThinkingMode{Type: "enabled"}` for those models; fast-chat models stay opt-out by default. The same explicit-Thinking pattern is now wired into `internal/memory/distill.go` and `internal/memory/dream.go` so /distill and -dream don't break when callers pass `ModelV4Flash` directly (or when the reasoner alias retires)
- **Lesson**: backward-compat aliases that route through a parameterised backend can change semantics silently — and your assumption about which backend they hit may itself be wrong. When a provider exposes the same feature as both "model id" AND "request parameter", read their docs for the exact mapping AND normalise to one shape at the boundary; don't trust the alias's old name to promise behaviour the new shape doesn't deliver by default
- **Refs**: `pkg/deepseek/types.go:ShouldEnableThinking`, `pkg/agent/agent.go:runTurnDeepSeek`, `internal/memory/distill.go`, `internal/memory/dream.go`

### DeepSeek legacy aliases `deepseek-chat` / `deepseek-reasoner` sunset 2026-07-24
- **Saw**: code defaulting to `deepseek.ModelChat` / `deepseek.ModelReasoner` would silently stop working in production on 2026-07-24; pricing table mistakenly billed `ModelReasoner` at V4-Pro rates (~3.1× too high) because we'd assumed it routed to V4-Pro
- **Why**: per api-docs.deepseek.com, both aliases are deprecated and route to V4-Flash — `deepseek-chat` with thinking disabled, `deepseek-reasoner` with thinking enabled. The names imply two different "models" but they're two configurations of the same backend
- **Fix**: switched all defaults from `ModelChat` → `ModelV4Flash` (cmd/seek, pkg/agent.New, setup ping, fim default, distill/dream), corrected `internal/pricing/pricing.go` to bill `ModelReasoner` at V4-Flash rates, replaced model-id literals in user-facing strings (picker, /think description, placeholder tip, /model usage hint) with explicit V4 names. The constants `ModelChat`/`ModelReasoner` remain so existing session files and configs load until sunset; `ShouldEnableThinking(ModelReasoner)` still returns true for the same reason
- **Lesson**: when a provider deprecates a name with a *named successor and an exact mapping*, switch your defaults to the successor *before* the sunset date and treat the old constants as compat-only — don't wait for the dependent code to break in production. Always re-read the provider's docs when you find yourself guessing about routing; we were three months wrong about which backend `deepseek-reasoner` hit
- **Refs**: `pkg/deepseek/types.go` (const block deprecation notes), `internal/pricing/pricing.go`, `internal/tui/commands.go:knownModelsForProvider`

### `<project>/.seek/project-id` is per-machine state — must be gitignored
- **Saw**: `git status` in seek's own repo showed `.seek/` untracked. If committed, every clone would pick up the original author's `sha256("/Users/alice/code/seek")[:16]` as their project-id and route their own per-machine M under `~/.seek/projects/<alice's hash>/`. Not data-corrupting (each user's M still lives under their own home), but the directory name becomes a "shared but meaningless" string with no relation to any local path
- **Why**: `LoadOrCreate` writes `.seek/project-id` best-effort on every run to support single-user "project moved" recovery — the pointer travels with the project tree so a `mv` doesn't lose M history. That recovery use case is per-user; cross-user propagation was never intended
- **Fix**: `.seek/project-id` added to repo `.gitignore`. Pattern is narrow (just the file) rather than `.seek/` because `<project>/.seek/skills/` is meant to be team-shared
- **Lesson**: auto-generated per-machine pointer files MUST be gitignored at creation — adding "we'll gitignore it later" is exactly when it slips into a `git add .`. If a file is written best-effort on first run, its gitignore entry needs to ship in the same commit
- **Refs**: `internal/memory/project.go:writeProjectPointer`, `.gitignore`

### PrePromptHook output must be byte-stable across runs or prefix-cache collapses
- **Saw**: while testing the M-index injection (M5.2), early implementations iterated over `map[string]Entry` directly. Two consecutive sessions with identical on-disk M produced *different* injected bytes (map iteration order in Go is randomised per-process) and `prompt_cache_hit_tokens` dropped to near-zero on the second turn
- **Why**: DeepSeek's prefix cache keys on the exact byte sequence of the prompt history. Decorator hooks (PrePromptHook) sit BEFORE the cache lookup — their output becomes part of the prefix. If the bytes vary across runs at the same logical state, every Prompt is a cache miss on every old message
- **Fix**: `Project.Index()` sorts by Name; `FormatLCandidatesMarkdown` sorts + dedupes its sources; integration test `TestHook_OnPrePrompt_ByteStable` SHA-256-checks two `Hook.OnPrePrompt` invocations against the same disk state and fails the build if they diverge
- **Lesson**: every byte produced by a `PrePromptHook` must be deterministic from on-disk content. Map iteration, time.Now()-stamping, and "let the LLM format it" are all silent prefix-cache killers. Lock in determinism at the source (sorts, sums, content-addressed renders) and assert it with a round-trip hash test
- **Refs**: `internal/memory/hook.go`, `internal/memory/project.go:Index`, `internal/memory/hook_test.go:TestHook_OnPrePrompt_ByteStable`

---

### Approval callback that blocks on a channel needs ctx-aware select on BOTH ends
- **Saw**: implementing per-call approval (M4.5.5), the agent goroutine occasionally hung when Ctrl+C fired during a tool's permission.Check — the askFn was already past its send and blocked on `<-resp`, with no way to escape
- **Why**: a naïve `askFn := func(a) { ch <- a; return <-resp }` has TWO blocking ops. SIGINT cancels the outer ctx, but the goroutine sitting on either send or receive doesn't notice unless we explicitly select on `ctx.Done()` at each step
- **Fix**: wrap both the send and the receive in `select { case ... : ; case <-ctx.Done(): return false }`. The "deny if cancelled" semantics also matches user expectation — Ctrl+C means stop, not "block forever waiting for me to decide". Commit `7c96bd7`
- **Lesson**: any blocking channel op in a host-supplied callback needs ctx-aware select on every step. The convenience of `ch <- x; <-resp` syntax hides two deadlock points
- **Refs**: `cmd/seek/main.go` askFn closure, `internal/permission/permission.go` ApprovalRequest

### Go's constant float→int conversion isn't auto-applied
- **Saw**: `cannot convert 65536 * WarnFraction (constant 52428.8) to type int` when writing budget test boundaries
- **Why**: Go's constant folding refuses to silently truncate a constant float result to an int, even when both operands are known at compile time. The test had `int(65536 * WarnFraction)` where WarnFraction was a `const 0.80`
- **Fix**: convert one operand to a runtime value first (`int(float64(65536) * frac)`), or use a tiny helper that does the conversion at runtime. Commit `d038455`
- **Lesson**: when you see "constant X.X of type float64" cannot convert, the answer isn't to add more parentheses — it's to make the expression non-constant

## Go language

### DeepSeek rejects assistant messages with neither `content` nor `tool_calls`
- **Saw**: after a model streaming turn produced only `reasoning_content` (thinking) but no actual content or tool_calls, every subsequent API call failed with `invalid_request_error: Invalid assistant message: content or tool_calls must be set`
- **Why**: `runTurnDeepSeek` and `runTurnLLM` construct `assistant := Message{Role: RoleAssistant}` with empty fields, then stream content/tool_calls into it. If the model only emitted reasoning tokens (or nothing at all) before `[DONE]`, the returned assistant has `Content=""` and `ToolCalls=nil`. On the next turn, `StripReasoningContent` strips the (absent) reasoning_content, leaving role=assistant with no fields at all — DeepSeek's API requires every assistant message to carry either content or tool_calls
- **Fix**: added an empty-response guard in both `runTurnDeepSeek` and `runTurnLLM`: if `assistant.Content == "" && len(assistant.ToolCalls) == 0`, return an error instead of the empty message. The Prompt loop then surfaces the error and drops the turn without corrupting history. Updated `TestAgent_EmptyChoicesUsageOnly` to expect the error (it previously asserted empty responses were safe). Commit (this one)
- **Lesson**: a streaming response with only reasoning_content is not a valid assistant turn — guard both the DeepSeek and LLM provider paths. The "model said nothing" edge case is not an error to tolerate; it poisons subsequent requests
- **Refs**: `pkg/agent/agent.go:runTurnDeepSeek`, `pkg/agent/agent.go:runTurnLLM`, `pkg/agent/agent_test.go:TestAgent_EmptyChoicesUsageOnly`

### DeepSeek HTTP 5xx and empty SSE bodies are transient — retry once before failing
- **Saw**: two failure modes were showing up in normal use without any retry: (a) `deepseek api error: internal_error: Internal Server Error` returned synchronously from `ChatStream`; (b) the stream completed cleanly but emitted no deltas, so the agent's empty-response guard (see entry above) fired and the user had to manually re-send. Both correlated with DeepSeek-side blips — the same underlying outage class
- **Why**: `pkg/deepseek.ChatStream` had no retry layer at all. Every transient upstream failure surfaced as a hard error to the agent and forced the user to "继续" by hand. The empty-stream case was particularly bad because it looked like a model behaviour bug, not an infrastructure one
- **Fix**: added a one-shot retry in `pkg/deepseek/stream.go` covering both (a) HTTP 5xx + transport errors during request setup (sync, before the channel exists), and (b) SSE bodies that close without producing any data delta (in the goroutine, before any event reaches the caller). Fixed 500ms backoff. The retry is gated on `emittedAny == false` — once a single delta has been pushed to the output channel, retry is permanently off because the caller's UI/state has committed to the partial stream and a re-send would duplicate content. 4xx codes are not retried (those are auth/config, not transient). Tests cover both retry triggers, the cap (no infinite loops), the post-emit lockout, the 4xx pass-through, and ctx-cancel during backoff. Commit (this one)
- **Lesson**: when the same outage class manifests as both a sync HTTP error AND a clean-but-empty stream, you need retry at both layers — not just the obvious one. The `emittedAny` flag is what makes "transparent" retry safe vs. dangerous: never retry after the caller has seen anything. Prefix cache makes the cost of retry near-zero (byte-identical request), so the policy can be aggressive on the trigger side as long as the safety gate holds. Also: do NOT retry 4xx — quotas, auth, schema errors don't get better with time
- **Refs**: `pkg/deepseek/stream.go:openChatStream`, `pkg/deepseek/stream.go:pumpChatStream`, `pkg/deepseek/stream_test.go:TestChatStream_RetryOn500`, related entry above on the empty-response guard

### Backticks in raw string literals close the string
- **Saw**: `cmd/seek/main.go:40` suddenly failed `go vet` with `expected ';', found bash` after I added a string mentioning `` `bash ls` ``
- **Why**: the system prompt is a raw string literal (backtick-delimited). The inner backticks closed the string mid-sentence, then the surrounding text became Go syntax
- **Fix**: replaced inner backticks with single quotes (`'bash ls'`). Commit `7daaa3b`
- **Lesson**: raw strings are the cleanest for multi-line content **until** you need backticks inside. Use `"..."` with escapes, or `+`-concatenate, or use single quotes / unicode look-alikes for inline code

### Shadowing the `cap` builtin in a local helper
- **Saw**: a local helper named `cap(s string, limit int) string` worked fine but felt wrong; reviewers would flag it
- **Why**: `cap` is a Go builtin (slice/channel capacity). Shadowing it inside a package is legal but confusing — and would silently break code in the same file that wanted `cap(slice)`
- **Fix**: renamed to `clip`. Commit `8264f52`
- **Lesson**: check `go doc builtin` before naming a function. Builtins that are also common English words (`cap`, `len`, `new`, `make`, `copy`, `close`) are easy to step on

### `for j, r := range string` gives BYTE indices, not rune indices
- **Saw**: a styled banner that used multi-byte runes (`█` is 3 UTF-8 bytes) silently lost letters past the first one when a cutoff column was applied. The unstyled version using `for j := range []rune(...)` worked fine; only the styled path drifted
- **Why**: `for j, r := range str` walks runes for `r` but reports j as the BYTE position the rune starts at. Compare with `for j := range []rune(str)` where j IS the rune index. When a cutoff is computed as a rune position (like `letterEndCols` here) and applied against a byte-indexed j, every multi-byte rune past the first one pushes j further ahead of the rune position; the comparison `j <= cutoff` becomes false too early
- **Fix**: convert to `[]rune(...)` once and iterate the slice — `j` then matches whatever rune-position math the rest of the code uses. Commit `eae5f88`
- **Lesson**: in Go, `range string` is a footgun the moment you care about positions. If positions are coordinate-system-sensitive (cutoffs, column-aligned masks, etc.), pin yourself to one coordinate system end-to-end. The mixed-coordinate bug is invisible at compile time and on ASCII-only content
- **Refs**: `internal/tui/banner.go:renderBanner`

### Literal UTF-8 BOM in a Go string literal is a compile error
- **Saw**: `internal/skill/skill.go` failed to build with `illegal byte order mark (syntax)` at the line `strings.TrimPrefix(string(data), "<BOM>")`
- **Why**: Go's scanner permits a BOM only at the very start of a source file. Anywhere else — even inside a string literal — it's rejected as a stray BOM, not a Unicode codepoint
- **Fix**: use the escape `"﻿"` instead of pasting the BOM byte. Commit `2c53248`
- **Lesson**: when you need a special invisible character in source, write it as an escape. Pasting "the thing" from a doc is exactly the foot-gun the scanner is protecting you from
- **Refs**: `internal/skill/skill.go:Parse`

### Top-level `var` slice and `func` that reference each other → init cycle
- **Saw**: `internal/tui/commands.go` failed `go vet` with `initialization cycle for commands` after I made cmdHelp read from a top-level `commands` slice
- **Why**: `var commands = []command{ {handler: cmdHelp}, ... }` references `cmdHelp` at init time; `cmdHelp` reads `commands` — Go's initialiser can't sequence both
- **Fix**: turned `commands` into `func allCommands() []command { return ... }` so the slice is built lazily on first call. Commit `08449cd`
- **Lesson**: when a top-level `var` and the functions it lists reference each other, demote the var to a func. Initialisation order isn't worth fighting

### `json:",omitempty"` is silently ineffective on `time.Time` and other struct types
- **Saw**: added `StaleSince time.Time` with `,omitempty` to `internal/memory.Entry`. Every fresh entry's memory.jsonl line still contained `"stale_since": "0001-01-01T00:00:00Z"` — the omitempty tag did nothing
- **Why**: `encoding/json`'s omitempty checks for the Go *zero value* of a few specific kinds (false, 0, "", nil, empty array/map/slice). For struct values like `time.Time`, the zero value is a *struct*, not any of those — so omitempty never fires and the (non-zero-bit-pattern) struct is always emitted. `time.Time{}.IsZero()` returns true at runtime, but encoding/json doesn't call IsZero
- **Fix**: switched to `json:",omitzero"`, added in Go 1.24+. omitzero specifically checks IsZero() for types that implement it, including time.Time. Project's go.mod is already 1.25.x so this is free
- **Lesson**: omitempty on `time.Time` or any struct field is a no-op. Use `,omitzero` (Go 1.24+), or `*time.Time` pointer if you need backwards-compatible behaviour. Eyeballing a JSON file after every schema change catches this fast
- **Refs**: `internal/memory/memory.go:Entry.StaleSince`

### Word-level vs character-level similarity for LLM-produced trait merging
- **Saw**: using Levenshtein character edit distance (threshold ≥0.75) to merge dream candidates caused false positives — "low evidence trait" vs "high evidence trait" scored 0.84 (3 char edits / 19 chars) and got merged despite being semantically different. At the same time, genuine rephrasings like "prefers explicit error handling over panic" vs "prefers explicit error handling" scored 0.74 and falsely stayed separate
- **Why**: Short trait descriptions from the LLM often share long template substrings ("evidence trait") while differing on the actual preference word. Character-level edit distance is blind to word boundaries — it treats "low vs high" as 3 edits out of 19, which looks "similar" by ratio. Conversely, a real rephrasing with extra words ("over panic") adds 11 edits to a 42-char base, depressing the ratio below the threshold
- **Fix**: switched to Jaccard word-overlap similarity (threshold ≥0.55). Words split on spaces handle English natural language rephrasings correctly. For CJK text (no spaces), fall back to Levenshtein character ratio — CJK characters carry semantic weight individually, so char-level comparison is appropriate there
- **Lesson**: no single string-similarity metric works equally well for multi-word English and CJK. Use word-level for space-delimited languages (the common case for LLM output) and character-level for scripts without word boundaries. Test border cases on both
- **Refs**: `internal/memory/merge.go:traitSimilarity`

---

## Tooling / environment

### Path-string assertions / raw paths in JSON literals broke on windows-latest CI
- **Saw**: `go test ./...` red on windows-latest with three failure shapes: (1) `strings.HasSuffix(p, "/foo")` failing because Windows gives `\foo`; (2) `strings.Contains(p, ".seek/skills")` for the same reason; (3) `read: bad arguments: invalid character 'U' in string escape code` when a test built JSON via `json.RawMessage(`{"path":"`+p+`"}`)` and `p` was `C:\Users\...` — `\U` is an invalid JSON string escape
- **Why**: macOS/Linux developers writing tests against `filepath.Join` outputs and then asserting via Unix-style literals (`/foo`) or embedding paths directly into JSON without escaping. Both work locally; both blow up the moment a Windows runner sees them. The `\U` case is sneakier — JSON requires backslashes to be escaped (`\\`), but raw concatenation skips that step
- **Fix**: (1)+(2) use `filepath.Base(p)` for last-segment checks or `filepath.ToSlash(p)` when substring matching; (3) build JSON via `json.Marshal(map[string]string{"path": p})` instead of string concat — the marshaller escapes backslashes correctly
- **Lesson**: any test that touches paths needs to be reviewed with "what does this do on Windows?" in mind. Three antipatterns to grep for periodically: `HasSuffix(.*"/`, `Contains(.*"/`, `json.RawMessage(.*+.*+`. Especially the JSON one — it looks like obviously-correct test setup until a backslash lands inside
- **Refs**: `internal/skillmgr/skillmgr_test.go` `update_test.go`, `internal/skill/loader_test.go`, `internal/tools/read/read_test.go`, `internal/tui/filepicker_test.go`

### Go's `time.ParseDuration` rejects "30d" / "1w" — only ns…h
- **Saw**: a `flag.Duration` with default `30*24*time.Hour` accepted `--since=720h` but choked on `--since=30d` with "parse error" — the natural unit a CLI user would write
- **Why**: `time.ParseDuration` (since 1.0) only supports `ns`, `us`/`µs`, `ms`, `s`, `m`, `h`. There is no day or week unit because they're not exactly 24h / 7d (DST, leap seconds), and the stdlib won't fake it
- **Fix**: document the limitation in the flag help text (`--since DURATION (e.g. 24h, 168h, 720h)`), and have tests use hour-based values. Don't reinvent parsing on top — too many edge cases. M8.4b commit
- **Lesson**: when exposing a duration flag, either accept Go's surface and tell the user, or write a custom parser that handles `d`/`w` explicitly. Half-measures (silently approximating "30d" as 720h) confuse users who write `31d` and don't see what they expected
- **Refs**: `cmd/seek/skill_query.go:cmdSkillStats`

### macOS APFS made `SKILL.md` + `skill.md` tests collide silently
- **Saw**: a loader test that wrote both `SKILL.md` and `skill.md` into one directory and expected the uppercase to win failed with "uppercase didn't win" — the second write silently overwrote the first, and `fileExists` for both names returned true (same inode)
- **Why**: APFS (and HFS+ before it) are case-insensitive by default; `SKILL.md` and `skill.md` resolve to the same file. The "two files coexisting" scenario the loader has to handle is only reachable on case-sensitive filesystems (Linux ext4/btrfs, macOS APFS formatted case-sensitive explicitly)
- **Fix**: probe the temp dir for case sensitivity (write FS_CASE_PROBE / fs_case_probe, read back) and `t.Skip` when insensitive. Commit lands in M8.0
- **Lesson**: cross-platform filesystem tests need to detect the property they depend on, not assume Linux. The classic candidate set is: case sensitivity, symlink permissions, executable bit, mtime resolution
- **Refs**: `internal/skill/loader_test.go:caseSensitiveFS`

### Auto-mode classifier blocked inline API keys on the command line
- **Saw**: tried to run `DEEPSEEK_API_KEY=sk-... go run ...` and got the action denied; reason cited shell history exposure
- **Why**: the harness's safety classifier flags any command that embeds a credential-looking string. Even one-off smoke tests with a real key trigger it
- **Fix**: write the key to `.env` (already in `.gitignore`) via the file-writing tool — that path doesn't touch the classifier's "secret in shell history" check. Then `set -a && . .env && set +a && go run ...` runs the binary without putting the key on any command line
- **Lesson**: when the classifier blocks something for a stated reason, find the path that doesn't trigger that reason rather than working around the classifier itself

### `glamour@v1.0.0` required a specific `lipgloss` pre-release commit
- **Saw**: `go get` for all four charm packages at once errored: glamour wanted `v1.1.1-0.20250404203927-76690c660834`, not the latest `v1.1.0`
- **Why**: glamour's `go.mod` pinned a development version of lipgloss with API changes that hadn't shipped in a tagged release
- **Fix**: pull lipgloss at the exact required pseudo-version first, then everything else. Commit `9be599b`
- **Lesson**: when fetching a family of related packages, do them one at a time so version conflicts surface with names attached, not as a confusing batch error

### `go run ./cmd/seek` is slow enough to feel broken
- **Saw**: between command turns, the TUI launched noticeably slower than expected; iteration on UI tweaks was painful
- **Why**: `go run` re-links a temporary binary every invocation. For a project the size of seek (deps included) that's 1-3 seconds of overhead per launch
- **Fix**: prefer `go build -o /tmp/seek ./cmd/seek && /tmp/seek` (or `go install` once + run the installed binary) during iteration
- **Lesson**: `go run` is for one-shot scripts. For anything you launch repeatedly, build once

### macOS bash 3.2 has no `mapfile` / `readarray`
- **Saw**: `.githooks/pre-commit` died with `mapfile: command not found` on a fresh clone, blocking every commit
- **Why**: `mapfile` and its alias `readarray` are bash 4+ builtins. Apple stopped shipping newer bash with macOS (post-3.2 is GPLv3-licensed); `/bin/bash` is permanently 3.2 and `#!/usr/bin/env bash` happily resolves to it unless the user has Homebrew bash first in PATH
- **Fix**: replaced both `mapfile -t arr < <(...)` calls with `arr=(); while IFS= read ...; do arr+=("$line"); done < <(...)` — bash 3.2 supports arrays + process substitution, just not `mapfile`
- **Lesson**: any `#!/usr/bin/env bash` script that ships to a team with macOS users must stick to bash 3.2 features. Reach for `read` loops; avoid `mapfile`/`readarray`, `${var^^}`/`${var,,}`, `declare -n` namerefs, and `BASH_REMATCH` in extglob contexts
- **Refs**: `.githooks/pre-commit`

### `omitempty` on a slice field omits both nil AND empty (`[]T{}`)
- **Saw**: the JSONL header line was supposed to omit the `messages` key when serialising the session header. The slice was set to `nil` before encoding — but `json:",omitempty"` would also silently omit it if it were an empty (`len==0`) non-nil slice
- **Why**: Go's JSON encoder treats `omitempty` on a slice as "omit when nil or len==0". Distinct from a pointer: a non-nil pointer to an empty struct is NOT omitted. Easy to assume "omitempty = omit-when-nil" and get bitten when the slice is zeroed via `append` returning nil vs `[]T{}`
- **Fix**: relies on this behaviour deliberately — `header.Messages = nil` before encoding ensures omission. The test suite verifies the header line has no `messages` key
- **Lesson**: `omitempty` on slice means "absent when nil OR empty". If you need to distinguish nil-vs-empty on the wire, don't use omitempty — use a pointer to a slice (`*[]T`)
- **Refs**: `internal/session/session.go:Save`

### `json.Encoder.Encode()` always appends `\n` — the JSONL primitive
- **Saw**: building JSONL output, we needed each JSON object on its own line. `json.MarshalIndent` + manual `\n` write looked like the obvious path
- **Why**: `json.Encoder.Encode()` is documented to write exactly one JSON value followed by a newline character. This is the built-in JSONL primitive — no manual newline needed, and the decoder's `dec.More()` + `dec.Decode()` loop reads one object at a time regardless of line boundaries
- **Fix**: JSONL writer uses `enc.Encode(&obj)` in a loop; reader uses `dec.More()` loop. Zero manual newline handling anywhere
- **Lesson**: `json.Encoder` is the JSONL encoder — it's in the name of the object, not in a library. If you find yourself writing `json.Marshal` + `"\n"`, step back and use `Encoder` instead
- **Refs**: `internal/session/session.go:Save`, `internal/session/session.go:decodeJSONL`

### `edit` tool: small models burn turns retrying byte-mismatched `old_string`
- **Saw**: a v4-flash session ran ~10 `edit` calls in a row on an HTML file dense with Unicode (`─` `🌙` `○` `▊`), each returning `expected 1 replacements but old_string occurs 0 times`. The model could read the file, copy what looked like the same characters, and still miss — usually whitespace or Unicode normalisation. Net cost: ~20 wasted tool turns to land one edit
- **Why**: the `edit` tool did exact byte matching only. Two failure classes dominated: (1) Unicode NFC vs NFD — visually identical strings encoded differently (`é` as `U+00E9` vs `e`+`U+0301`); (2) when the match missed, the error was `occurs 0 times — broaden context` with no signal about where the near-match lives, so the next attempt was effectively a blind guess. Small models can't easily recover from "0 matches, no hint"
- **Fix**: two-tier matching + diff-rich error hints in `internal/tools/edit/edit.go`. Tier 1 stays exact (preserves file bytes 1:1 in the common case). Tier 2 retries after NFC-normalising both sides; on success the file is rewritten in NFC and the result says so. When both tiers miss, the error embeds the closest line window in the file — scored by canonicalised line equality across a sliding window (NFC + strip Unicode Cf "format" chars like ZWS/ZWJ/BOM + collapse whitespace runs) — AND a unified diff between the needle and that window with the raw bytes preserved, so whitespace-only and zero-width-character differences become visible on adjacent `-`/`+` lines. Schema bytes unchanged → prefix cache preserved
- **Lesson**: byte-exact matching is the right default but a hostile baseline for non-top-tier models. The fix is in the tool layer, not the prompt: (a) one cheap normalisation tier (NFC) catches the most common Unicode miss; (b) the error message is part of the contract — "no match, here are the closest 5 lines as a unified diff" is *qualitatively* different from "0 matches, broaden context" because invisible-byte differences (whitespace, NFC/NFD, zero-width chars) are unrecoverable without seeing both sides on adjacent lines. The scorer canonicalises to find the candidate, but the diff itself uses raw bytes — normalising the diff would erase the very signal the model needs. Aider's multi-tier matcher is the open-source canonical example; Cursor solves the same problem by routing through a small fine-tuned "apply" model. We took the Aider path because it's pure tool-layer and doesn't change the prefix cache surface
- **Refs**: `internal/tools/edit/edit.go:closestCandidate`, `internal/tools/edit/edit.go:canonForCompare`, `internal/tools/edit/edit_test.go` (NFC fallback + closest-candidate + ZWS diff cases)

### `/compact` panicked with nil Session on `--no-save` path
- **Saw**: running `/compact` in a session started with `--no-save` caused a nil-pointer dereference in `handleCompactDone`: `m.opts.Session.ID` evaluated before the nil guard
- **Why**: `handleCompactDone` was written assuming `m.opts.Session` is always set. On the `--no-save` path `m.opts.Session` stays `nil` by design (no persistence configured). The code tried `snapshotID = m.opts.Session.ID` outside the `if m.opts.Session != nil` guard
- **Fix**: guard the entire fork+persist block with `if m.opts.Session != nil && m.opts.Store != nil`; the notice message also branches on whether `snapshotID` is set. Commit `c3082ec`
- **Lesson**: when a struct field can be nil by design (not by bug), every method that touches it must check. Naming it `opts.Session` suggests "optional" — treat it as such everywhere, not just at startup
- **Refs**: `internal/tui/update.go:handleCompactDone`

---

### Cumulative cost re-priced retroactively when model or tier changed mid-session
- **Saw**: switching from `deepseek-v4-flash` to `deepseek-v4-pro` mid-session made the status bar's `cost $X` jump ~3.1× even though DeepSeek had already billed the prior turns at V4-Flash rates. Same shape happened crossing the 00:30/08:30 Beijing off-peak boundary: cumulative cost halved or doubled at the tier flip
- **Why**: `internal/tui/statusbar.go` rendered cost as `pricing.Cost(s.Model, s.Tier, s.Usage)` where `s.Usage` is cumulative tokens. Each render re-derived the cost from cumulative tokens × CURRENT (model, tier). The cache.Tracker only stored `[]deepseek.Usage` — no record of which model/tier was active when each turn ran — so there was no way to price each turn at its own rate. Result: every render was a fresh re-pricing of the entire session at whatever model/tier was active right now
- **Fix**: `internal/cache.Tracker` now stores `[]turnRecord{Usage, Model, Tier, Cost}` and locks in `Cost = pricing.Cost(model, tier, usage)` at `Record(usage, model, tier)` time. New accessors `CumulativeCost()` and `LastCost()` return summed/last locked-in dollars. `StatusSnapshot` carries a `CumulativeCost float64` field which the bar formats directly; no recompute path remains. All seven `Tracker.Record` call sites (TUI, RPC, runPrint, runJSON, benchmark, session-resume, compact) pass the current `(model, tier)`. Pinned by `internal/cache.TestTracker_CumulativeCostLockedInAtRecord` (model-switch round-trip: Flash → Pro → Flash, expects 1.43 not 0.84) and `TestTracker_TierTransitionDoesNotRePrice`
- **Lesson**: aggregate state (cumulative usage) loses the per-event dimensions (model, tier) that determined its derived values (cost). If you later re-derive against current dimensions, you silently mutate the past. Either store the derived value at write time, or store the dimensions alongside the aggregate so re-derivation has the right keys. Don't store half the info and hope nothing changes
- **Refs**: `internal/cache/cache.go:Tracker`, `internal/tui/statusbar.go:rightSegments`, `internal/cache/cache_test.go:TestTracker_CumulativeCostLockedInAtRecord`

### Cumulative prompt tokens are meaningless as a context-limit signal
- **Saw**: the ctx% indicator in the status bar climbed past 100% after ~55 turns even though the actual context window (the most recent full prompt) was well within the 1M limit
- **Why**: `Tracker.Cumulative().PromptTokens` is the sum of ALL turns' prompt tokens. Because each turn resends the full history, this sum grows as O(turns²): turn 1 = 10k, turn 2 = 20k, … turn 55 = 550k, sum = 15M. The actual per-turn cost grows linearly, not exponentially
- **Fix**: status bar now uses `Tracker.Last().PromptTokens` — the most recently completed turn's prompt token count, which is the correct "how full is the window right now?" signal. Commit `e240ea5`
- **Lesson**: when a multi-turn LLM client re-sends full history every time, "cumulative prompt tokens" is not a budget signal — it's an accounting artifact. Always budget against the per-turn value
- **Refs**: `internal/cache/cache.go:Tracker`, `internal/tui/view.go:renderStatusBar`

### `finish_reason="length"` looks like a normal stop
- **Saw**: long analysis responses were silently truncated; the TUI appeared to finish normally, the model's answer was mid-sentence, and the user had no indication that the response was cut
- **Why**: `finish_reason="length"` means the model hit `max_tokens` and stopped generating — not that the answer was complete. The original code treated `"length"` identically to `"stop"`
- **Fix**: `runTurnDeepSeek` now emits an `ErrorEvent` on `finish_reason="length"`: "response truncated (finish_reason=length, max_tokens=N) — use /compact to free context or ask me to continue". Also raised `MaxTokens` default from server default (~4096) to 8192. Commit `e240ea5`
- **Lesson**: check every possible `finish_reason` value against the API docs; "stop" is the only one that means "normal completion". All others need surfacing

---

## LLM provider quirks (M6)

### Anthropic requires consecutive tool results merged into ONE user message
- **Saw**: sending separate `role="user"` messages each containing a single `tool_result` block returned 400: "tool_result blocks must all appear in the same user message"
- **Why**: Anthropic's Messages API has a uniqueness constraint: a `tool_result` content block can only appear inside a user message, AND all `tool_result` blocks that respond to the same assistant turn must be in a single user message. Separate messages violate this even if they are logically sequential
- **Fix**: `buildRequest()` in `pkg/llm/provider/anthropic/client.go` scans ahead: when it sees a `role="tool"` message, it collects ALL consecutive `role="tool"` messages and folds them into one user message with a `[]contentBlock` array. Commit `a1fde05`
- **Lesson**: Anthropic's message format is not the same shape as OpenAI's even where they look similar. Tool results in particular have hard structural constraints — don't assume "one message per tool result" works
- **Refs**: `pkg/llm/provider/anthropic/client.go:buildRequest`

### Gemini omits tool call IDs; system message is `systemInstruction`, not a messages-array entry
- **Saw**: two surprises at once when wiring Gemini: (1) function calls in the SSE stream had no `id` field, breaking the call-result pairing that every other provider relies on; (2) sending a `role="system"` message in the `contents` array returned 400
- **Why**: (1) Gemini's `functionCall` part schema has `name` and `args` but no ID field — it assumes you correlate by turn position. (2) System context goes in a separate top-level `systemInstruction` field, not in `contents`
- **Fix**: (1) IDs are generated deterministically as `gemini_0`, `gemini_1`, … by incrementing a `toolIdx` counter during SSE parsing. (2) `buildGeminiRequest` inspects the first message: if `role=="system"` it extracts the content to `SystemInstruction` and shifts the rest into `contents`. Commit `a1fde05`
- **Lesson**: read the Gemini API shape end-to-end, not the OpenAI-compat docs. Function calling and system instructions are different enough that assuming an OpenAI mental model will bite you on both
- **Refs**: `pkg/llm/provider/gemini/client.go:buildRequest`, `client.go` SSE parser

### New overlay panel consumed Ctrl+C — user couldn't quit while help was open
- **Saw**: after opening the help overlay (`/help` or `?`), pressing Ctrl+C had no effect — the overlay stayed open and the app wouldn't quit
- **Why**: the overlay's key handler in `handleKey` returned `m, nil` for any key that didn't match Esc/Enter/q, accidentally swallowing Ctrl+C before it reached the `case tea.KeyCtrlC: return m, tea.Quit` branch below
- **Fix**: added explicit `case tea.KeyCtrlC` in the overlay's key switch before the generic fallback, plus case-insensitive `q` close check. Commit (this one)
- **Lesson**: every modal/key-grabbing layer in a TUI needs its own Ctrl+C path — don't rely on a downstream switch that the layer's early-return bypasses
- **Refs**: `internal/tui/update.go:handleKey`

### OpenAI streaming token counts require `stream_options: {include_usage: true}`
- **Saw**: the `TurnDone.InputTokens / OutputTokens` fields were always 0 when using the OpenAI provider over a streaming call
- **Why**: by default, OpenAI's streaming endpoint does not include a usage chunk. You must explicitly opt in with `"stream_options": {"include_usage": true}` in the request body; only then does a final data chunk arrive with the `usage` field populated
- **Fix**: all `openAIRequest` bodies include `StreamOptions: streamOptions{IncludeUsage: true}`. Commit `a1fde05`
- **Lesson**: OpenAI-compatible providers often require explicit opt-in for features that other providers include by default. Scan the request body options for anything labelled "include_*" or "with_*"
- **Refs**: `pkg/llm/provider/openai/client.go:openAIRequest`

---

## Testing / CI

### t.Cleanup(chdir) after t.TempDir() breaks Windows TempDir removal
- **Saw**: Windows CI failed with `testing.go:1369: TempDir RemoveAll cleanup: unlinkat … The process cannot access the file because it is being used by another process.` in `TestCommit_ProjectScope`.
- **Why**: `t.TempDir()` registers its cleanup via `t.Cleanup` (LIFO). When the test registered `t.Cleanup(func() { os.Chdir(cwd) })` *before* calling `t.TempDir()`, the chdir-back ran *last* — after the TempDir removal had already tried (and failed on Windows) to delete the directory while CWD was still inside it. On Unix this works (directory can be unlinked while in use), on Windows it doesn't.
- **Fix**: call `t.TempDir()` first, then register the chdir-back `t.Cleanup` second — so the chdir-back runs first (LIFO) and restores CWD before TempDir removal. Commit `d20e446`.
- **Lesson**: when combining `t.TempDir()` + `os.Chdir()` in a test, always call `t.TempDir()` *first*, then register the chdir-back `t.Cleanup`. The reverse ordering is a latent Windows-only failure. `defer os.Chdir(prev)` is safe (runs before `t.Cleanup` callbacks); `t.Cleanup` is not.
- **Refs**: `internal/tools/skillinstall/skillinstall_test.go:TestCommit_ProjectScope`

## Release / upgrade

### `tui.VersionString()` is a formatted banner, not a raw module version
- **Saw**: building `seek -upgrade` against the embedded version, the dev-build refusal check (`IsDev("dev") == true`) never fired — the upgrader happily proceeded to fetch releases on a local `go build`
- **Why**: `tui.VersionString()` returns the *display* form (`"dev · f68f4fd+"`), not the raw module version (`"dev"`). Pasting that into `IsDev` and `compareSemver` looked sensible but quietly compared the wrong string — only the version token before the `" · "` separator is the real version
- **Fix**: introduced `coreVersion()` in `internal/upgrade/version.go` that strips the `" · <rev>[+]"` suffix; both `IsDev` and `splitSemver` route through it. Added tests for the formatted form
- **Lesson**: any helper named `VersionString` is a user-facing display string by default; the raw value is almost always elsewhere. Treat it as opaque text and re-parse, don't assume "version == raw version"

### Atomic self-replace requires same-filesystem temp file
- **Saw**: an early design draft put the download temp file in `os.TempDir()`, intending to rename it onto the running binary
- **Why**: `os.Rename` is atomic *only* when source and destination are on the same filesystem. On Linux, `/tmp` is often a separate `tmpfs`; the cross-filesystem rename would either fail with `EXDEV` or get implemented as a non-atomic copy+unlink, opening a window where the binary doesn't exist
- **Fix**: download + extract straight into `filepath.Dir(exePath)`. See `internal/upgrade/upgrade.go:downloadAsset`
- **Lesson**: "atomic replace" always means "same directory". If you're touching a Windows running-exe, that's a separate problem (see `replace_windows.go`: rename-current-to-.old first)

### `<br>` inside `<code>` survives display but `textContent` drops it — copy-paste silently corrupts multi-line install commands
- **Saw**: clicking the "copy" button on the macOS/Linux install card and pasting into a shell produced one giant line; bash parsed `OS=$(...)$ ARCH=$(...)$ VER=$(...)$ curl ...` as a 3-var env-prefix to `curl`, with each value carrying a trailing literal `$`. The fetched URL became `…/v0.2.7$/seek_0.2.7$_darwin$_arm64$.tar.gz` → 404
- **Why**: the install snippet used `<br>` for visual line breaks. `Element.textContent` only concatenates **text nodes**; `<br>` is an element node and contributes the empty string, so all lines collapsed. The `^(\$|PS>)` strip regex had the `m` flag, but with no real `\n` it only stripped the leading prompt on the very first line
- **Fix**: walk `code.childNodes` and emit `\n` when the node is a `BR`, otherwise its `textContent`. Then the existing per-line prompt-strip works. `examples/index.html:1575`
- **Lesson**: any time you build a multi-line code block with `<br>` for layout, your copy handler must reconstruct newlines from the DOM — `textContent` / `innerText` / `outerText` all lose them in different ways. Better: use `<pre>` + real `\n` and avoid the trap entirely
- **Refs**: `examples/index.html` install section copy-to-clipboard handler

---

## Reading order for newcomers

If you're new to the project, skim entries in this order:
1. **DeepSeek API** first — the optimisation surface that justifies the whole project
2. **TUI / terminal** second — that's where most of the user-visible polish lives
3. **Go / tooling** as needed when you trip on them

---

## Go

### Function-local type can't be referenced from another function
- **Saw**: attempted to call `buildGroupedIndexString([]entryInfo{...})` where `entryInfo` was defined inside `buildMIndex()`. Go compiler rejected: "undefined: entryInfo"
- **Why**: Go type declarations inside function bodies are scoped to that function only. Unlike closures (which capture values, not types), the type itself is invisible outside its declaring function, even from another function in the same package
- **Fix**: moved `entryInfo` to package-level type. The struct is used by two functions (`buildMIndex` and `buildGroupedIndexString`), so it cannot be local to either
- **Lesson**: Go's scoping rules for types follow the same lexical block rules as variables — a type declared inside a function is as private as a variable there. If two functions share a type, it must live at package or file level
- **Refs**: `internal/memory/hook.go:entryInfo`

### RWMutex not reentrant — unexported helpers can't lock if caller holds lock
- **Saw**: added `p.mu.Lock()` to public methods like `Project.Add()`, which calls `p.writeEntries()` internally. If `writeEntries()` also called `p.mu.Lock()`, the goroutine would deadlock on the second acquisition
- **Why**: Go's `sync.RWMutex` is not reentrant (unlike Java's `synchronized`). A goroutine that already holds a `Lock()` and tries to `Lock()` again will block forever waiting for itself to release the first lock. Same applies to `RLock()` after `Lock()` — that's a write-lock upgrade attempt, which also deadlocks
- **Fix**: unexported helper methods (`writeEntries`, `appendArchive`) do NOT acquire locks — callers (the public methods) hold the lock for the entire duration of the operation. Documented in the `Project` type comment
- **Lesson**: when adding `sync.RWMutex` to an existing struct, audit every internal call path. The convention is: public methods lock, private helpers don't. Inline documentation ("caller must hold mu.Lock") prevents future deadlocks
- **Refs**: `internal/memory/project.go:Project`

### Adding a slash command changes `/m` prefix-match test expectations
- **Saw**: after adding `/memory` command, `TestFilterCommands_PrefixMatch` failed because `/m` prefix matched both `/model` and `/memory` instead of just `/model`
- **Why**: the prefix-match test hardcoded the expected match count and command name. Adding a new command whose prefix overlapped with an existing one naturally expanded the match set
- **Fix**: updated the test expectation from `["/model"]` to `["/model", "/memory"]`
- **Lesson**: tests that assert on exact command-match sets are brittle to new command additions. Either use a unique prefix (`/mo` instead of `/m`) or explicitly document that such tests need updating when commands are added
- **Refs**: `internal/tui/commands_test.go:TestFilterCommands_PrefixMatch`

### Go slice aliasing: a returned struct with a slice field shares the backing array
- **Saw**: `Project.Entries()` returned `[]Entry` where each `Entry` had a `Tags []string`. A caller doing `entries[0].Tags = append(entries[0].Tags, "x")` could silently mutate the project's internal entry map when the backing array had spare capacity, or silently appear to work when it didn't — non-deterministic behaviour
- **Why**: Go's slice header is a (ptr, len, cap) triplet. Copying an `Entry` struct copies the `Tags` header, but the header points to the same underlying array. `append` with spare capacity writes into that shared array; without spare capacity, it allocates a new one. The result depends on the exact capacity at call time
- **Fix**: added defensive `make+copy` in both `Entries()` (on read-out) and `Add()` (on store-in). Three lines each, zero runtime cost for entries without Tags
- **Lesson**: any exported method that returns a struct containing a slice, or accepts one and stores it, must defensively copy the slice to prevent non-deterministic aliasing. The bug class is invisible when callers never mutate the slice — and explodes the first time they do
- **Refs**: `internal/memory/project.go:Entries`, `project.go:Add`

### LLM thinking-mode artifact tokens leaked into generated source code
- **Saw**: a comment line ended with `// changes there. response` — the word "response" was actually a fragment of DeepSeek's thinking-mode output token `｜end▁of▁thinking｜>` that had leaked into the code. The token was invisible in the editor (rendered as whitespace/punctuation) but was present as raw UTF-8 in the file
- **Why**: the model (DeepSeek V4-Flash with thinking enabled) occasionally emits its internal thinking-mode delimiters as part of tool calls or generated content. When the thinking output is captured into a file write (via the `write` tool), the delimiter tokens get embedded as literal bytes. They look like normal characters in most editors but are semantically wrong
- **Fix**: `grep -r 'end▁of▁thinking'` or equivalent to find and remove leaked tokens. Then audit the writing tool to post-strip known artifact patterns. Alternatively, catch via CI: `grep -rn 'end▁of▁thinking' --include='*.go'` would catch any future leaks
- **Lesson**: when using thinking-mode models in an agent that writes code, set up a post-write filter for known delimiter tokens. The artifacts are rare (1 in ~hundreds of tool calls) but when they land in a source file they're invisible to human review and silent to the compiler (they look like comments). Automated grep in CI is the cheapest insurance
- **Refs**: `internal/memorycli/memorycli.go:358` (before fix)

### Time-dependent sort key breaks prefix-cache byte-stability
- **Saw**: M-index truncation path sorted entries by `Score(e, now, halfLife)` where `now` changed on every prompt. Two adjacent prompts with identical disk state could produce different M-index byte sequences, causing a full prefix-cache miss
- **Why**: `Score()` uses `exp(-(now - lastActive) / halfLife)`. Even when no entry metadata changes, the passage of time between prompts shifts scores slightly, potentially pushing a borderline entry into or out of the top-K budget window. The byte sequence changes → cache miss on the entire prior conversation
- **Fix**: replaced the time-dependent Score sort with a TIME-INDEPENDENT key: `pinned desc → recall_count desc → name asc`. This changes byte output only when entry metadata changes (add/remove/recall/GC), not on every prompt. Documented the trade in `buildMIndex`'s byte-stability doc
- **Lesson**: any sort or filter key used in a prompt-injection path must be time-independent unless you're willing to pay the cache-miss cost. Score-based ranking is better for quality but worse for caching; the right choice depends on which axis is more constrained. For M-index at 1500 token budget, the cache hit is worth more than the marginal ranking improvement
- **Refs**: `internal/memory/hook.go:buildMIndex`

### System prompt advertised non-existent `limit` parameter → resolved by accepting `limit` with max-50 enforcement
- **Saw**: every `read` call that passed `"limit": N` was rejected with `json: unknown field "limit"`, causing a wasted model round-trip and confusing the agent
- **Why**: `cmd/seek/main.go` (the system prompt template) listed `read(path, offset?, limit?)` and told the model to "Always pass limit when reading an unfamiliar file". But `limit` had been removed from the `read` tool's JSON Schema (AGENTS.md §Tool usage workflow, item 3) to enforce the 50-line maximum without exposing a bypass. Prompt and implementation were out of sync
- **Fix**: added `limit` back to the schema with `maximum: 50` + server-side rejection of values > 50 with a clear error message. Schema's `maximum: 50` tells the LLM the constraint; if the LLM ignores it, the Go code returns `"read: limit must be at most 50, got %d"`. Model gets one round-trip to retry with a valid value, which also trains it to respect the schema. Changes: `internal/tools/read/read.go` (schema, Args, validation), `internal/tools/read/read_test.go` (replaced rejection test with valid + error cases), `cmd/seek/main.go` (prompt mentions limit/max/error), `AGENTS.md`/`CLAUDE.md` (updated convention docs)
- **Lesson**: when the model systematically wants to pass a parameter, accept it in the schema (with a max) and **reject invalid values with a clear error** rather than `additionalProperties: false` (which gives a generic unknown-field error) or silent clamping (which hides the mistake). The model gets one round-trip to retry, which is acceptable. The key insight: the schema's `maximum` constraint is informational to the LLM; server-side validation is the actual enforcement
- **Refs**: `cmd/seek/main.go:74` (before/after), `internal/tools/read/read.go:23-31,68-78`, `AGENTS.md:40,59`, commit (this one)

### Adding a new permission mode requires touching 7+ packages
- **Saw**: implementing `/review` mode (behavioural twin of `/plan` for code review) needed coordinated changes across `internal/permission`, `internal/tui` (options, commands, update, statusbar, placeholder), `cmd/seek` (flags, prompt, wiring), `internal/session` (struct, New, Fork), and their tests. Missing any one caused a compile error or silent misbehaviour (e.g. mode cycle skipping Review, status bar not showing the badge, session not inheriting Review on Fork)
- **Why**: permission modes are a cross-cutting concern. The mode constant lives in `permission`, but the TUI reads it via `Options.Review` (model.go) + `SetReview` hook, renders it in `statusbar.go` and `update.go:modeLabel()`, toggles it in `update.go:cycleMode()` and `commands.go:cmdReview`, persists it in `session.go` (struct field + New/Fork), and configures it in `cmd/seek/main.go` (flag + wiring + system prompt). The `Check()` method in `permission.go` must also explicitly handle the new mode alongside `ModePlan` — they share read-only logic but `Check()` uses `mode == ModePlan || mode == ModeReview`, so forgetting either branch silently grants the wrong permissions
- **Fix**: checklist-driven approach: (1) add `ModeReview` constant, (2) update `Check()` guard, (3) add `Review()` method, (4) add `Options.Review` + `SetReview`, (5) register `/review` command, (6) update `cycleMode`, (7) update `modeLabel`, (8) add status bar badge, (9) update placeholder, (10) add `--review` flag + mutual exclusion, (11) wire `SetReview` in TUI options, (12) update `session.New` signature + all callers + Fork, (13) update `runPrint`/`runJSON`/`runRPC` signatures, (14) inject review workflow reminder in system prompt, (15) update tests for new cycle + new session.New signature. Each step produces a compile error if the next isn't done, so iterate `go build ./... && go test ./...` after each cluster
- **Lesson**: a new permission mode is a "scaffold across the stack" change. Don't start without a checklist of every touch point. `git grep` for `ModePlan` and `\.Plan` to find all locations that need a parallel path. The session.New signature change (`plan bool` → `plan, review bool`) is particularly invasive — every test file that constructs a Session must be updated, and missing any one produces a build failure that's only caught by `go test ./...` (not `go build` on the main binary)
- **Refs**: `internal/permission/permission.go:ModeReview`, `internal/tui/commands.go:cmdReview`, `internal/tui/update.go:cycleMode`, `internal/session/session.go:New`, `cmd/seek/main.go:reviewWorkflowReminder`, commit (this commit)
- **Update**: `/review` was later downgraded from a full permission mode to a simple one-shot shortcut command. See "Permission modes should be reserved for actual permission changes" below.

### Permission modes should be reserved for actual permission changes
- **Saw**: `/review` was originally implemented as `ModeReview`, a full permission mode constant with its own session field, TUI cycle state, CLI flag, system prompt suffix, and status bar badge. But its permission behaviour was 100% identical to `ModePlan` — the only difference was a code-review "persona" injected into the system prompt. This meant every new permission mode required touching 7+ packages for what was essentially a behavioural tweak.
- **Why**: permission modes (`ModeDeny`, `ModeAsk`, `ModeYolo`, `ModePlan`) control what tools can do (write, bash, read outside CWD). Mixing "persona" (how the model should behave) into the permission system created an N×M explosion: each new persona (reviewer, tester, documenter...) would need a new mode constant + session field + TUI state + flag + status badge.
- **Fix**: downgraded `/review` to a simple slash command: it sets plan mode (read-only) then auto-submits a user message with review instructions. The review "persona" lives in the user message, not the system prompt — no permission changes needed. Removed `ModeReview` constant, `Session.Review` field, `Options.Review`/`SetReview`, `--review` CLI flag, `reviewWorkflowReminder` const, and `cycleMode` 4-state cycle (back to 3-state Ask→Plan→Yolo). Future mode-like behaviours (tester, documenter) should follow this pattern: slash command that flips to an existing permission mode + injects a persona prompt.
- **Lesson**: a new permission mode is only warranted when the *permission semantics* differ from all existing modes. Personas (reviewer, tester, etc.) should be implemented as shortcut commands that reuse an existing mode + inject instructions into the user message. The `ModeLabel` mechanism in `pkg/agent` already supports per-turn mode reminders without touching the system prompt — use it.
- **Refs**: this commit (downgrade /review → shortcut)

### Paste-folding "any key to expand" UX shock → marker persists, resolved on Enter
- **Saw**: pasting >5 lines into the textarea folded the content into a placeholder `"📋 pasted N lines — press any key to expand"`. Typing ANY character (even a single letter) would instantly restore the full multi-line content, causing a jarring "explosion" of the input area
- **Why**: the restore block in `handleKey` ran unconditionally on every keypress when `m.pastedContent != ""`. The design assumed "first keypress = user is ready to edit/submit", but the implementation was too aggressive — any key (even typing to continue the sentence) triggered the expansion
- **Fix**: removed the restore block entirely. The marker now persists in the textarea on every non-Enter keypress. On Enter (submit/queue/steer), the marker is replaced with the actual pasted content via `strings.Replace(val, marker, m.pastedContent, 1)` before the text is sent. If the user edits/deletes the marker, the paste is silently discarded
- **Lesson**: when a fold/placeholder is needed for stability (large paste in fixed-height textarea), the UNFOLD trigger should be narrow (Enter = submission) rather than broad (any key). "Any key" feels like a bug to the user because no explicit action should silently inject multiline content. The trade-off: if the user wants to edit the pasted content before sending, they can't see it — they have to trust the marker. For the LLM-prompt use case (paste-first, edit-rarely), the Enter-only trigger is the right UX
- **Refs**: `internal/tui/update.go:handlePasteFolding`, `internal/tui/update.go:handleKey` (streaming + non-streaming submit paths), `internal/tui/model.go:pastedContent/pastedLineCount`

### reviewBranchEntry Esc checked after streaming → Esc cancels stream, not branch entry
- **Saw**: during an active agent stream, opening the /review picker, selecting "Type a branch name…" to enter reviewBranchEntry mode, then pressing Esc to cancel the branch-entry would instead cancel the stream. The reviewBranchEntry flag stayed true, so the next Enter would submit `/review <textarea>` (possibly empty) rather than behaving normally
- **Why**: the `case tea.KeyEsc` branch in `handleKey` checked `m.streaming` first. The reviewBranchEntry guard was AFTER the streaming guard, so Esc always hit the streaming-cancel path when a stream was active, regardless of reviewBranchEntry state
- **Fix**: moved the `m.reviewBranchEntry` check to the top of the Esc handler, before the streaming check. Now Esc in branch-entry mode always cancels the entry regardless of stream state. The streaming path is only reached when reviewBranchEntry is false
- **Lesson**: when adding a modal sub-state (reviewBranchEntry, setupKeyEntry) that uses the textarea, its Esc handler must come BEFORE the streaming Esc handler. Otherwise, Esc during an active stream can't exit the modal state. General rule: all modal-state Esc guards go first, streaming-cancel second, fallback third
- **Refs**: `internal/tui/update.go:handleKey` (`case tea.KeyEsc`), `internal/tui/commands.go:handleReviewPick`, commit (this commit)

### `s[:n]` on a multi-byte UTF-8 string produces broken runes
- **Saw**: queued Chinese text (Enter during a stream) showed garbled characters in the "↰ queued:" preview, and any Chinese text clipped by `truncateOneLine` rendered as invalid UTF-8 (� characters) wherever it appeared (tool args, permission prompts)
- **Why**: `truncateOneLine` used `len(s)` (byte count) and `s[:n]` (byte slicing) on a UTF-8 string. Chinese characters are 3 bytes in UTF-8; when `n` landed in the middle of a 3-byte sequence, the resulting string contained an incomplete/invalid UTF-8 sequence. Same root cause as the banner bug — byte-level indexing on a multi-byte string
- **Fix**: changed `truncateOneLine` to use `[]rune(s)` for length comparison and slicing — rune-level slicing never splits a multi-byte character. Commit (this commit)
- **Lesson**: any Go code that truncates, clips, or paginates user-visible text by length must use `[]rune` for the count and the slice operation. `string` indexing is byte-level; multi-byte characters (Chinese, emoji, CJK punctuation) will be silently corrupted. The function signature says "n chars" — make sure the implementation actually counts chars, not bytes
- **Refs**: `internal/tui/update.go:truncateOneLine`, `docs/pitfalls.md:300` (same root cause, different surface)

### Custom color tokens (`--dim`) must be verified against WCAG AA in both themes
- **Saw**: after a UI/UX Pro Max review, the `--dim` color token in both light mode (`#7a7570`) and dark mode (`#6a6a7a`) failed WCAG AA minimum contrast (4.5:1) for `.72rem` labels, despite looking "fine" to the developer
- **Why**: designing by eye is unreliable for low-contrast semantic colors. `--dim` is used for secondary/muted labels at small sizes (`.72rem` ≈ 11.5px), which requires the strictest contrast ratio. Light mode achieved ~4.2:1 (needs 4.5:1), dark mode ~3.8:1 (needs 4.5:1)
- **Fix**: light mode `--dim` → `#635e58` (~5.7:1), dark mode → `#8a8a98` (~5.8:1). Verified with Chrome DevTools contrast checker
- **Lesson**: any custom color token used for text (even "dim" or "muted" text) must be verified against WCAG AA (4.5:1) before shipping. The eye is not a colorimeter — especially for subtle differences around the 4.0–4.5:1 boundary. Always test both light and dark themes independently
- **Refs**: `examples/index.html:105,126`, WCAG 1.4.3

### SVG data-URI in CSS `content:` needs explicit width/height on pseudo-element
- **Saw**: replaced `content: "📋"` emoji with an SVG data URI in `::after`, but the icon rendered at 0×0 or default size
- **Why**: `content: url(...)` does not inherit the `font-size` of the element; SVGs render at their intrinsic size (if declared) or 0 if the URI is opaque to the browser. The old emoji sized itself via `font-size: .8rem`, which is meaningless for a `background-image` approach
- **Fix**: switched from `content: url(...)` to `content: ""` + `background-image` + explicit `width`/`height: 16px` + `background-size: contain`. This gives reliable sizing across all browsers
- **Lesson**: when replacing text/emoji in CSS pseudo-elements with SVGs, use the `background-image` pattern (empty content + dimensions + background-size) rather than `content: url(...)`. The former gives you explicit size control; the latter depends on the SVG's intrinsic dimensions and browser implementation
- **Refs**: `examples/index.html:834`, `docs/pitfalls.md:this-commit`

### Alt+Enter steer doesn't work on macOS terminals
- **Saw**: pressing Option+Enter during a stream queued the text instead of steering it — same behaviour as plain Enter. The `Alt` modifier was lost before it reached bubbletea
- **Why**: `msg.Alt` depends on the terminal sending `\x1b\r` (ESC prefix + Enter) as the raw byte sequence. macOS terminal emulators (Terminal.app, iTerm2, Warp) typically send Option+Enter as a bare `\r` unless "Use Option as Meta key" is enabled. Without the ESC prefix, `msg.Alt` is false and the code falls into the queue branch (update.go:502)
- **Fix**: added `/steer [text]` slash command (alias `/s`) with queue promotion: bare `/steer` promotes a queued message to steer, and `/steer <text>` is equivalent to Alt+Enter. Commit `0e5b275`
- **Lesson**: modifier-key-dependent keybindings (Alt+key) are unreliable across platforms because terminal emulators vary in how they encode the Alt/Option modifier. For any action gated by a modifier key, provide a text-based alternative (slash command) so users whose terminal doesn't forward the modifier can still trigger the action. The "queue first, then promote" workflow is a particularly Mac-friendly pattern because it doesn't require any modifier key at all
- **Refs**: `internal/tui/update.go:502` (msg.Alt check), `internal/tui/commands.go:cmdSteer`, commit `0e5b275`

### skill_commit must not silently pick a scope — the model has no signal for user intent
- **Saw**: the original `skill_commit` tool installed skill at user scope (`~/.seek/skills/`) by default. A user asking "install this skill from GitHub" could silently end up with it in `$HOME` when they wanted it in the project repo, or vice versa. The model had no way to know which was appropriate
- **Why**: the two scopes (user vs project) have very different consequences: user scope is private to the machine, project scope is shared via git with the whole team. Neither the tool name nor the source URL tells the model what the user intends — only the user knows. Hard-coding `user` meant the model never learned to ask, and the user never learned the distinction existed
- **Fix**: made `scope` a required enum parameter (`"user"` | `"project"`) on `skill_commit`. Schema validation rejects calls that omit it. The system prompt + tool description explicitly instruct the model to ask the user before calling. The approval prompt displays the resolved target path so the user sees where it's going. Tests cover missing scope, invalid scope, and both valid scopes. Commit `d86d509`
- **Lesson**: when a tool parameter represents a high-consequence user choice (private vs shared, local vs remote, permanent vs temporary), make it an explicit required enum instead of a silent default. The model cannot infer user intent for binary decisions that affect filesystem layout or data visibility. This applies generically: any "A or B" that a human would consider carefully is an `ask_user` candidate, not a defaultable parameter
- **Refs**: `internal/tools/skillinstall/skillinstall.go` (scope validation + approval prompt), `cmd/seek/main.go` (system prompt instruction), `docs/guide-skills.md` (scope docs), commit `d86d509`

### Slash menu / status bar floated mid-screen on /clear, /help close, and other "banner gone" states
- **Saw**: across three superficially-different bug reports — `/clear` + `/`, `/help` then Esc + `/`, slash menu opening on a tall fresh terminal — the same shape: input/status assembly rendered 14 rows above the terminal floor with empty space beneath. Each time, a fresh `bannerWiped` assignment plugged that specific path; each time, a different path re-introduced it
- **Why**: `view.go`'s bottom-pin math computed `cursorRow := welcomeFixedLines(14) + scrollbackLines`, encoding the assumption "the 12-row pixel banner + 2 stderr loader lines printed by `PrintPixelWelcomeBanner` before `tea.NewProgram` still sit at terminal rows 0..13". Bubbletea inline mode cannot query the real terminal cursor, so this had to be a model-side belief — and every action that scrolled those 14 rows out of view invalidated it: `tea.ClearScreen` (wipe), `/help` overlay output of ~40+ rows on a sub-60-row terminal (forces bubbletea to scroll the banner out of scrollback), any future tall full-screen view that hasn't been written yet
- **Fix (architectural — replaces the `bannerWiped` patch family)**: stop printing the banner to stdout before bubbletea starts. Render it INSIDE `View()` instead, via the already-existing `renderWelcomeHeader(opts)`, gated on `scrollbackLines == 0` (same gate as the post-`/clear` welcome path that already worked). With the banner now in `sb`, `cursorRow` simplifies to just `m.scrollbackLines` and the load-bearing 14-row assumption is gone — along with `bannerWiped`, `welcomeFixedLines`, `welcomePadding`, `PrintPixelWelcomeBanner`, `animateBanner`, and `shouldAnimate`. UX side-effect: banner is now "welcome state only" (no longer reachable from terminal scrollback after the first turn), and the startup reveal animation is dropped (can come back later as a bubbletea tickMsg). Commits: the staged refactor (this one) supersedes the earlier `bannerWiped` patches
- **Lesson**: when a layout invariant rests on "feature X is currently at terminal row N", and the model can't observe the terminal, the invariant is going to drift — every action that touches the screen becomes a maintenance burden for that one belief. Two patches sharing the same `bannerWiped` flag was the warning sign: the fix wasn't "find every site that scrolls the banner", it was "stop trusting that the banner is at a fixed row". Rendering it inside `View()` puts it in the same reference frame as every other layout element, and `cursorRow = scrollbackLines` becomes the single, stable invariant
- **Refs**: `internal/tui/view.go` (welcome-screen block + bottom-pin pad), `internal/tui/banner.go` (renderWelcomeHeader is the surviving emitter), `internal/tui/run.go` (no more pre-tea banner print)

### Inline-mode layout drift — the architectural reset to alt-screen
- **Saw**: after three rounds of bug-fix-then-pitfall (banner offset / popup-in-bottomBuf / Esc-leftover-`/`), each fix uncovered another way the model's `scrollbackLines` estimate could disagree with the terminal's real cursor row. The pattern wasn't a single bug — it was a category: inline mode means the program cannot observe where it is on the terminal, so any layout calculation depending on "we are at row N" is going to drift the moment something causes the terminal to scroll (`tea.ClearScreen`, frame overflow, async `WindowSizeMsg`). The status bar would land mid-screen, popups would shift unexpectedly, and the next fix would just plug one path
- **Why**: bubbletea's inline mode (`tea.NewProgram(m)` with no `WithAltScreen()`) renders View() at the cursor's current position and tracks frame height via internal state — it does NOT query the terminal for cursor coordinates. We bolted on `scrollbackLines` to estimate "rows above the live region" so we could pad to the floor. But that estimate is one-way (model → render), so any time the terminal scrolled because the frame overflowed or because of resize timing, the estimate became fiction. Every patch (`bannerWiped`, post-Esc `input.Reset`, popup-in-sb) addressed a symptom of the same root problem
- **Fix (root cause)**: switch to `tea.NewProgram(m, tea.WithAltScreen())`. Bubbletea owns the entire viewport, layout becomes absolute (View() output = `m.height` rows, status pinned to last row), and `scrollbackLines` no longer participates in layout math. Conversation history moves from terminal scrollback into a `Model.historyBuf []string` field that View() renders at the top of the viewport. Every `tea.Println` callsite was rewritten as `m.appendHistory(line)` — which buffers the line AND returns a `tea.Println` cmd. Bubbletea's standard renderer queues Println output while alt-screen is active (`standard_renderer.go:190` gates flush on `!altScreenActive`) and flushes the queue to the main screen on program exit. **Free exit-time dump**: the user returns to their shell with the entire conversation in terminal scrollback, no code in run.go required for it
- **Trade-offs**: in-app scroll of long history is not yet implemented (bubbletea truncates the top of the viewport buffer when View() output exceeds `m.height`). The exit dump preserves everything, so no information is lost. Cmd+F search inside the alt-screen viewport works on what's visible only; for cross-history search the user exits and uses native terminal search on the dump. PRD `run.go:13-18`'s former "NO alt-screen" stance is reversed in commit messages and inline comments
- **Lesson**: when you find yourself patching the Nth symptom of "model state vs terminal state mismatch", stop and check whether the model has any way to actually observe the terminal. If not, the model is guessing — and every guess can be wrong. The cleanest fix is usually to remove the need to guess (alt-screen takes over the terminal, so there's nothing to guess about) rather than to refine the guess. We spent three iterations refining; one iteration of "stop guessing" replaced them all
- **Refs**: `internal/tui/run.go` (alt-screen + auto-flush exit dump), `internal/tui/model.go` (`historyBuf` field + `appendHistory` method returning Println cmd), `internal/tui/view.go` (pad math without scrollbackLines), bubbletea `standard_renderer.go:190` (queuedMessageLines flush behaviour)

### Welcome banner pinned across the entire first turn → user input lands above it, screen "auto-clears" at TurnEnd
- **Saw**: two reports of the same shape. (1) After the user submits their first prompt, their `▌ you: …` line appears ABOVE the SEEK wordmark banner — the banner stays put at the bottom of the live region while their input scrolls past it. (2) When the assistant finishes replying, the terminal appears to "auto-clear" — real scrollback content (cwd marker, earlier conversation) disappears in a single frame.
- **Why**: `view.go` gated the welcome banner on `m.turns == 0`. But `m.turns` only increments at `agent.TurnEnd` — i.e. AFTER the assistant response completes. From `submit()` to `TurnEnd` the gate stays true, so the 11-row banner block (blank-line + 7-row wordmark + blank + cwd + 2 blanks) is pinned inside the live region for the entire first turn. Every `tea.Println` (user message, assistant content commit, tool result commits) lands in scrollback ABOVE the still-rendered banner. Then `TurnEnd` fires, `turns` flips 0→1, the banner disappears in one View frame — and bubbletea's inline cursor-up + EraseScreenBelow over-erases by 11 rows, wiping real scrollback content that had been written above the live region in the meantime.
- **Fix**: tighten the welcome gate to `m.turns == 0 && len(m.promptHistory) == 0`. `promptHistory` gets its first entry inside `submit()` BEFORE any streaming activity, so the banner disappears the moment the user hits Enter — the layout shrinks once, on a quiet frame, before any `tea.Println` redraw storm. `cmdNew` also resets `promptHistory`/`historyIdx`/`savedDraft` so `/clear` (documented as "fresh conversation") legitimately restores the welcome state.
- **Lesson**: when a frequently-changing layout block is gated on a counter that updates late (`turns` at TurnEnd is many seconds after submit, in network terms), the gate is wrong. The disappearance has to align with a quiet frame, not with the busiest frame of the turn. Use the earliest stable signal of state change (`promptHistory` append in submit) rather than the latest (`TurnEnd`). Inline-mode bubbletea is especially unforgiving here — large per-frame layout deltas + concurrent `tea.Println` writes is exactly the recipe for cursor-position miscounts.
- **Refs**: `internal/tui/view.go` (welcome gate), `internal/tui/commands.go:cmdNew` (promptHistory reset), `internal/tui/view_test.go:TestView_WelcomeBannerHiddenAfterFirstSubmit` (regression test)

### Plan artifact write needs context BEFORE Sink.Approved fires — added a separate ContextReceiver interface
- **Saw**: when wiring the plan artifact writer into `planBridge.Approved(steps, batch)`, the artifact needs the `problem` statement and `why_now` from propose — but `Approved`'s signature only carries `steps` and `batch`. The obvious instinct was to add `problem` / `whyNow` to `Approved`'s signature, but that would have been the THIRD breaking change to the Sink interface in one feature batch (the first was Phase C's `batch bool`, the second was the F1+F2 optional interfaces).
- **Why**: the existing Sink contract is the contract every fake / test recording sink in the codebase implements. Each signature break drags every test that constructs a Sink into the diff, even when the test has nothing to do with the new feature. Repeated signature churn is the kind of thing that calcifies into "we don't change Sink anymore, please" — and the next reasonable addition gets bolted on with a struct member instead, which is worse.
- **Fix**: added a separate OPTIONAL interface `propose.ContextReceiver { OnProposeStart(problem string, steps []string, whyNow string) }`. Tool's Execute upcasts and calls it BEFORE the picker pops; hosts that don't care don't implement it and pay nothing. Same pattern as `DuplicateChecker`, `ProgressReporter`, `ArtifactReporter` — a small family of "if you can, here's some context" interfaces that the core Sink doesn't have to know about. PRD §八 / commit (this one).
- **Lesson**: Go's structural typing makes optional-interface upcast cheap. When you're tempted to add a fourth parameter to an existing method, ask whether a sibling interface would carry the new concern without touching every caller. Three callsites of `Approved(steps, batch)` in the codebase vs. one callsite of `OnProposeStart` plus an `if _, ok := sink.(ContextReceiver); ok` upcast — the latter is the diff-shaped right answer when the new info is "context for a subset of consumers", not "data every consumer needs".
- **Refs**: `internal/tools/propose/propose.go:Sink,DuplicateChecker,ProgressReporter,ContextReceiver,ArtifactReporter` (the family), `cmd/seek/main.go:planBridge` (implements all four optional interfaces).

### `[plan: approved]` is a load-bearing wire-format prefix shared between propose and the plan reconstructor
- **Saw**: when adding the "approve with auto-approve-per-step" option to the propose picker, the obvious instinct was to emit `[plan: approved batch]\n…` so the model could see at-a-glance that batch mode was on. That broke `seek -resume`: the task list rebuilt as empty even after a session that had approved a batched plan.
- **Why**: `internal/tools/plan/reconstruct.go` scans the transcript for tool results whose `Content` has the literal prefix `[plan: approved]` (closing bracket included). `[plan: approved batch]` does NOT have that prefix — the bracket comes after the variant token instead of before it. The seeding point for plan-state reconstruction was therefore silently missed and the entire downstream `plan(start)`/`plan(complete)` history got discarded.
- **Fix**: keep the closing-bracketed marker exactly intact across all approval variants. The batch path now emits `[plan: approved] (auto-approve-per-step)\n…`, and the reconstructor's prefix scan continues to match. Added `TestExecute_ApproveBatch` that asserts on `HasPrefix(out, "[plan: approved]")` so a future variant can't silently regress this. Commit (Phase C of plan-mode optimisation).
- **Lesson**: result strings produced by tools are not just LLM-facing prose — they are also wire format if anything else parses them. The "[plan: approved]" prefix is the seeding marker for transcript replay; once you have two consumers (the model AND the reconstructor), the format is a contract, not a string. When adding variants, append AFTER the contract token, never inside it. Same lesson as schema bytes (CLAUDE.md / PRD §4.8.1): bytes that other code parses must stay byte-identical across releases unless you intend a migration.
- **Refs**: `internal/tools/propose/propose.go:approveResult` (the variant comment), `internal/tools/plan/reconstruct.go:approvedMarker`, `internal/tools/plan/reconstruct_test.go:TestReconstruct_*`, `internal/tools/propose/propose_test.go:TestExecute_ApproveBatch`

### `/help` documented `?` hotkey not implemented; overlay lacked picker pattern
- **Saw**: the help overlay said `"/help, /? or ?", "Show this help"`, but pressing `?` in an empty input did nothing. `/help` showed a full-height overlay that blocked the conversation. `/model` had a much nicer interaction: no-args → compact picker with ↑/↓ navigation.
- **Why**: `handleKey` had no case for a bare `?` key — the docstring was aspirational. And `cmdHelp` used a static overlay for everything, while `/model` used the picker pattern (modelPickerOpen + modelPickerFiltered). Two different interaction models for two commands that should be equally quick to access.
- **Fix**: (1) added `?` hotkey in `handleKey` when input is empty (intercepted before textarea); (2) refactored `cmdHelp` to open a topic picker on no-args (topics: all, commands, keys, about) and show the overlay only when a topic is selected or passed as arg; (3) added `/help ` auto-open handoff in `updateCommandMenu` mirroring `/model /effort /lang` pattern. Commit (this one).
- **Lesson**: don't document keyboard shortcuts that don't exist — the user will try them and feel gaslit. When adding a command that parallels an existing one (`/model` → picker), match the interaction pattern; a full-height overlay is a very different ask than a compact dropdown. The cost of keeping `?` in-sync across help content and handler code is the same as implementing it.
- **Refs**: `internal/tui/update_key.go` (`?` hotkey), `internal/tui/commands.go` (cmdHelp → picker + topic builders), `internal/tui/update_menu.go` (`/help ` auto-open), `internal/tui/commands_test.go` (picker + topic tests)
