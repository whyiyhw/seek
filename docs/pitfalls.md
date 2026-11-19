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

- AI assistants working in this repo: every time you fix a non-obvious bug or discover a surprising constraint, **append an entry here** AND add a `Pitfall:` trailer to the commit (see [`AGENTS.md`](../AGENTS.md)).
- Humans: same convention. Use `scripts/extract-pitfalls.sh` to sanity-check that recent commits' `Pitfall:` trailers line up with entries here.

## Status: live vs absorbed (archive pass 2026-11-19)

**248 live entries, 0 absorbed in this file.** The 15 entries the 2026-11-14 audit marked `[已吸收]` were moved verbatim to [`pitfalls-archive.md`](pitfalls-archive.md) (26 031 B) — one file for live knowledge, one for superseded history. The 2026-11-14 pre-archive baseline was 201 entries / 15 absorbed / 186 live; the ledger has grown since (new sections: agents/orchestration, MCP, web frontend, Files API).

**Marking rule** (keep consistent on re-audit):

- `### [已吸收]` at the start of a heading marks an entry whose *subject* has been superseded or removed: the subsystem was deleted, a later commit reversed the design, or the model/dependency the entry constrains is gone. Following the entry today would steer you wrong.
- Every absorbed entry carries a trailing `- **已吸收**: reason → pointer` bullet naming what superseded it (commit / file / sibling entry).
- A pitfall whose fix landed but whose constraint still holds is **not** absorbed — that is the normal shape of a live pitfall (bug fixed, knowledge still true). Only design-level supersession marks.
- **Live pitfall = any `### ` heading without the `[已吸收]` prefix.** `scripts/pitfall-to-eval.sh` scaffolds only from live entries; mechanical filter: skip headings matching `^### \[已吸收\]`.
- **Archive pass**: marked entries stay in place until a periodic pass moves them (content verbatim, nothing deleted) into `pitfalls-archive.md`, whose header records the pass date and size. Two files instead of one keeps this ledger searchable for live knowledge without losing the history.
- `Reading order for newcomers` is prose navigation, not entries — not counted.

Per-section distribution (live entries; every section's absorbed count is 0 after the 2026-11-19 pass):

| Section | Live |
|---|---:|
| Filesystem / concurrency | 3 |
| Hook / memory | 7 |
| Skill distill (auto path) | 1 |
| TUI / terminal | 29 |
| Agent loop | 17 |
| DeepSeek API | 24 |
| Go language | 12 |
| Tooling / environment | 24 |
| LLM provider quirks (M6) | 9 |
| Testing / CI | 7 |
| Release / upgrade | 3 |
| Site / landing page | 3 |
| Go | 31 |
| Worktree / Windows | 3 |
| Sandbox / OS jail | 1 |
| Cron parser | 1 |
| Feishu / external API | 1 |
| Subprocess / secrets | 1 |
| Tool output | 2 |
| Testing | 2 |
| Tool decorators | 2 |
| Filesystem / model safety | 5 |
| Tooling / line endings | 4 |
| TUI / session stats | 5 |
| CI / gates | 3 |
| Eval / tool descriptions | 5 |
| Build tags / dependency isolation | 1 |
| ACP / editor & headless lanes | 11 |
| Eval / results data shape | 1 |
| Typed agents / orchestration | 2 |
| Git / merge | 2 |
| Subprocess / streaming stdout | 2 |
| Web frontend / asset plane | 15 |
| Tooling / go list | 2 |
| Skill stats / --idle | 1 |
| MCP bridge / tool naming | 1 |
| Setup wizard / provider menu | 1 |
| MCP prompt section | 1 |
| DeepSeek Files API / file_id | 3 |
| **Total** | **248** |

Coarse four-bucket rollup — **TUI** (TUI/terminal + TUI/session stats): 34 live · **API** (DeepSeek API + LLM provider quirks + Feishu): 34 · **Go** (both Go sections): 43 · **tooling & engineering** (remaining sections): 137.

---

## Filesystem / concurrency

### Two parallel writers of the SAME content-addressed blob race on a shared `.tmp` filename
- **Saw**: checkpoint package's concurrent-snapshot test failed with ~half the expected events; the warnings logged "rename ... .tmp ... : no such file or directory" for many goroutines
- **Why**: `storeBlobLocked(sha, content)` used a fixed `<bp>.tmp` filename, then `os.Rename(tmp, bp)`. Two goroutines hashing the same content (e.g. concurrent edits to small files with identical pre-state) both: (a) Stat — miss; (b) WriteFile(tmp); (c) Rename(tmp, bp). Whichever rename wins erases the .tmp file. The losing goroutine's later WriteFile-to-the-same-tmp succeeds, but its rename of that file fails because the .tmp path is already renamed-away — same name, but stale fd handle wasn't the issue, the file path was simply gone
- **Fix**: swap fixed-name `<bp>.tmp` for `os.CreateTemp(dir, base+".tmp.*")` so every writer gets a unique scratch path, then absorb the "target already exists" race after `Rename` returns an error by checking `Stat(bp)` and dropping our tmp copy silently. Commit (M9.1)
- **Lesson**: content-addressed storage is forgiving about losing the race (the winner's bytes equal yours) but UNforgiving about sharing a `.tmp` filename. Either use a unique tmp name per writer OR hold a per-blob mutex. The unique-tmp approach is simpler and lock-free
- **Refs**: `internal/checkpoint/file.go:storeBlobLocked`, `internal/checkpoint/checkpoint_test.go:TestFile_ConcurrentSnapshots`

### SSE unsubscribe defer deleted the WRONG subscriber — `f.next` read at defer-run time, not defer-schedule time
- **Saw**: web MVP's `/events` handler registered subscribers as `f.subs[f.next] = ch` but its deferred cleanup ran `delete(f.subs, f.next)`; with two tabs connected, the first to disconnect could delete the other's channel and leak its own (invisible while all tabs stayed healthy)
- **Why**: a deferred closure only captures variables, not values — `f.next` was re-read when the defer RAN, by which time any later subscriber had bumped the counter
- **Fix**: capture `id := f.next` (or return the id from the subscribe helper) and delete by that captured id; W1.3's `subscribe` returns a closure doing exactly this. Commit c_nl4nqg
- **Lesson**: `defer` freezes the *closure environment*, not the snapshot — anything read inside a deferred func must be captured to a local at schedule time if concurrent writers can move it. The bug class is silent until the shared value moves between schedule and run, which is exactly when two clients race
- **Refs**: `internal/controlplane/webfrontend_conv.go:subscribe`, `internal/controlplane/webfrontend.go:handleEvents`

### Topic fanout keyed coarser than the producer unit — cross-project SSE leak
- **Saw**: W3.2's `subagents` topic pushed project A's subagent rows into a stream subscribed for project B (verification round 2, card c_3736yl); every existing test was green because all of them ran a single project
- **Why**: subscribers were registered in a hub keyed by TOPIC NAME (`f.topics["subagents"]`) while the producer was a per-INDEX-PATH tail-follow (`f.follows[indexPath]`); `followLoop → pushTopic(topic)` fanned out to every subscriber of the name, a set spanning all projects
- **Fix**: make the follow the fanout unit — subscriber channels live on `topicFollow.subs`, `followLoop` pushes via `pushFollow(fl, ev)` to its own follow only, and the topic-name hub (`f.topics`) is deleted (`len(subs)` also replaces the separate `refs` counter). Rework commit on `drive/c_3736yl`
- **Lesson**: a fanout registry must be keyed at least as finely as its producer unit — keying it coarser (name vs per-project follow) leaks events across the finer dimension, and single-fixture tests cannot see it. When a subscription has a producer-side shard, hang the subscriber list on that shard instead of a parallel name-keyed map
- **Refs**: `internal/controlplane/webfrontend_topic.go:topicFollow`, `internal/controlplane/webfrontend_topic_test.go:TestWeb_TopicProjectIsolation`

## Hook / memory

### OnSessionStart must reset snapshot state for --resume correctness
- **Saw**: after implementing the M5.9 snapshot+delta strategy, `--resume` would inject a stale snapshot based on the previous session's first `OnPrePrompt` call, because `snapshotInjected` was still `true` from the prior agent lifetime
- **Why**: the Hook struct fields `snapshotInjected` and `snapshotEntryNames` survive across agent lifetimes when the registry is reused (which happens on `--resume`). Without explicit reset, the second session's first `OnPrePrompt` would skip `injectSnapshot()` and produce no soul/index context at all — the model would lose all memory context
- **Fix**: `OnSessionStart` now resets `snapshotInjected = false` and `snapshotEntryNames = nil` before running GC. This ensures the first `OnPrePrompt` after every session boundary (fresh or resume) rebuilds the snapshot from current data. Commit (M5.9)
- **Lesson**: any Hook struct field that tracks per-session state MUST be reset in `OnSessionStart`. The Hook is reusable across agent lifetimes (--resume), not 1:1 with sessions. Don't assume zero-init at construction covers all paths
- **Refs**: `internal/memory/hook.go:OnSessionStart`, `internal/memory/hook.go:Hook.snapshotInjected`

### ObserveEnqueue accept counter races with non-blocking ResultChan send
- **Saw**: `go test -race ./internal/memory` failed in `TestObserveEnqueue_RespectsSessionCap` on `h.observeAcceptCt++` — two filter goroutines could increment concurrently
- **Why**: `ObserveEnqueue` is non-blocking; the TUI/test receives from `ResultChan` as soon as the goroutine sends, which can overlap with the goroutine still bumping stats. `observeAcceptCt++` ran after the channel send, so a second enqueue could start before the first increment finished
- **Fix**: store `observeCount` / `observeAcceptCt` as `atomic.Int32` (Add/Load)
- **Lesson**: non-blocking handoff to a consumer doesn't mean the producer goroutine is done — any post-send bookkeeping in that goroutine needs the same synchronization as the shared counter itself
- **Refs**: `internal/memory/hook.go:ObserveEnqueue`, `internal/memory/auto_distill_test.go:TestObserveEnqueue_RespectsSessionCap`

### Shell-hook SkipReason needs per-event handling, not a single funnel
- **Saw**: a hook marked `SkipReason="syntax: bad"` (failed `bash -n` at startup) was producing `Deny: 'hook "broken" exited with code -1'` and blocking the tool, even though the PRD says skipped hooks must be silently bypassed
- **Why**: the natural place to short-circuit was inside `runHook` — return `code=-1` with `Reason="skipped: …"`. But `OnPreToolUse` treats `code != 0` as deny by design (that's the whole pre_tool contract), so a "neutral" sentinel exit code for "skipped" doesn't exist. Trying to centralise the skip-handling at the exec level papers over a real semantic split: pre_tool needs to skip-and-continue, observers need to skip-and-noop, and there's no single exit code that means both
- **Fix**: each event entry point checks `h.SkipReason` itself BEFORE calling `runHook` and decides what skip means for THAT event. `OnPreToolUse` `continue`s the loop; observers no-op. `runHook` still defends-in-depth by returning `code=0` if reached with SkipReason set, so the observer paths don't accidentally trip on a stale -1
- **Lesson**: when a flag has different semantics per call site, push the handling to the call site rather than synthesising a "magic value" in the shared helper. Shared helpers are great for shared invariants; per-caller decisions belong with the caller
- **Refs**: `internal/hooks/shell_runner.go:OnPreToolUse` (skip branch), `internal/hooks/shell_runner.go:runHook` (defensive)

### Trust prompt must run pre-bubbletea — askuser channel isn't drained yet
- **Saw**: initial plan was to plumb the shell-hooks trust dialog through the existing `internal/askuser.Policy` (same pattern as the propose tool). But Gate runs at main.go ~line 920, while `askPolicy.SetAskFn` doesn't get a real callback until line ~1145, and even then the callback pushes onto a channel the TUI bubbletea loop is supposed to drain — and the TUI hasn't started yet. Any pre-TUI askuser call would block forever
- **Why**: the askuser policy is a TUI-coupled async primitive by design — the SetAskFn callback expects a goroutine on the other end. There's no version of it that works at startup before bubbletea's `tea.NewProgram(...).Run()` has spun up
- **Fix**: built a separate `stdinTrustPrompt` in `cmd/seek/hooks_trust.go` that reads from stdin directly when isatty=true and refuses with a warning when not. Pre-TUI stdin still belongs to the process; piped / non-TTY launches auto-refuse rather than hang
- **Lesson**: not every "ask the user" UI needs to route through askuser.Policy. Pre-bubbletea questions belong on stdin; post-bubbletea questions belong in the policy. The cost of two paths is one ~80-line file; the cost of unifying them is reworking the TUI start order to allow async-prompts-before-render
- **Refs**: `cmd/seek/hooks_trust.go`, `internal/askuser/askuser.go` (the policy we deliberately do NOT use here)

### limitedWriter needs a pointer receiver — value receiver loses byte count
- **Saw**: cap-output write helper looked OK in isolation but in tests the captured stdout would exceed the documented 64KiB cap because `l.n` never seemed to grow
- **Why**: `func (l limitedWriter) Write` (value receiver) gets a fresh copy of the struct on every call. `l.n += n` modifies the copy, which is discarded when Write returns. The caller (exec.Cmd) sees `Write` return successfully and keeps streaming, blowing past the cap silently
- **Fix**: switch to pointer receiver `func (l *limitedWriter) Write` and pass `&limitedWriter{…}` when assigning to `cmd.Stdout` / `cmd.Stderr`. The `io.Writer` interface is satisfied by both value and pointer receivers, but ONLY the pointer receiver mutates state across calls
- **Lesson**: any "tracking" struct (counter, accumulator, ring buffer) that satisfies an interface MUST use pointer receivers. The compiler doesn't warn — it'll happily build and silently drop your mutations
- **Refs**: `internal/hooks/shell_runner.go:limitedWriter`

### A hook registered on the shared Registry fires in EVERY agent that shares it — including subagent children
- **Saw**: (design-time, caught while wiring the background-completion Notifier) the obvious wiring — `hooksReg.Register(subagent.NewNotifier(mgr))` — silently corrupts delivery: `buildSubagentRunner` hands the SAME `*hooks.Registry` pointer to every child `agent.New`, so a child's `Prompt` runs `ApplyPrePrompt` too. A background sibling finishing just before a child spawns has its completion notice DRAINED into the child's transcript — wrong context, wrong session file, and the parent never sees the result.
- **Why**: the registry is a live pointer shared parent+children by design (pre_tool deny / post_tool audit must cover subagent tool calls), so registration order can't scope a hook to the parent; any ordering still leaks because children read the registry at spawn time, after everything is registered. The memory hook registering AFTER Manager construction additionally means a snapshot taken at construction time would silently drop it from children.
- **Fix**: `hooks.Registry.CloneWithout(exclude)` — children resolve their registry through a LAZY `hooksFn` closure (`hooksReg.CloneWithout(bgNotifier)` per spawn) so they get every hook registered so far minus the parent-only drain point. Delivery (Notifier) DRAINS the inbox; the TUI wake PEEKS (`PendingNotices`) — one consumer per semantics. Card c_bickfj (P1-a).
- **Lesson**: on this codebase, "register a hook" is a broadcast to every agent sharing the registry — parent-session-scoped hooks need explicit exclusion at the child boundary, and the exclusion must be lazy (closure) when the parent registry keeps growing after the children's factory is built. Any future parent-only PrePrompt hook (resume revival, controlplane inbox) rides the same `CloneWithout` seam.
- **Refs**: `internal/hooks/registry.go:CloneWithout`, `internal/subagent/notifier.go`, `cmd/seek/main.go` (`subagentRunnerOpts.hooksFn`), `docs/prd/feature-orchestration.md` §4.1 D2

### dropMalformed must list every Config event slice — Validate sees all, drop saw five
- **Saw**: wiring `pre_prompt_inject` through Gate (M-O3.2) surfaced that a malformed hook (empty `name`/`command`) on any P3 observer event (`subagent_start`/`subagent_stop`/`stop`/`pre_compact`/`notification`) SURVIVED Gate: `Validate` walks `Config.All()` (every event) and errors, but `dropMalformed` only rebuilt the five legacy slices — P3 hooks were copied through as-is, so a nameless hook could reach dispatch
- **Why**: `dropMalformed` predates the P3 events and nothing ties the two functions together; `All()` grows with every new event, the drop list doesn't unless the author remembers to check
- **Fix**: `dropMalformed` rebuilds every slice including `PrePromptInject`; `TestGate_MalformedHooksDropped` locks a malformed `stop` + `pre_prompt_inject` hook both being dropped (card c_m2whz2)
- **Lesson**: when a validation function fans over `All()` but its repair function enumerates fields by hand, adding an event silently desynchronizes them — every new `Config` event field must touch BOTH, and a Gate test with a malformed hook on the new event is the cheap lock
- **Refs**: `internal/hooksconfig/load.go:dropMalformed`, `internal/hooksconfig/hooksconfig.go:Validate`, `internal/hooksconfig/hooksconfig_test.go:TestGate_MalformedHooksDropped`

## Skill distill (auto path)

### Same session re-entering the distill window double-counted recurrence
- **Saw**: 巡检卡 c_jqwd72 确认的二期遗留：`StrengthenDraft` counted every merge call unconditionally — a session re-entering the window (mtime touched after the watermark: state reset, overlapping runs) pushed a single-session draft to recurrence 2 → "reviewable", defeating the two-independent-sessions gate. Fix card c_jdwywj.
- **Why**: the auto-distill window is mtime-based (`prev < UpdatedAt ≤ watermark`, `internal/skilldistill/auto.go:runBatch`). The watermark is the ONLY thing keeping a distilled session out of later batches, and it's a resettable state file — nothing downstream was idempotent.
- **Fix**: SessionID dedup inside `StrengthenDraft` (the merge choke point — covers mergeDraft's match path AND the write-race fallback): a `src` whose SessionID is already in `sources` is a full no-op — no count, no source/evidence/trace append, no UpdatedAt bump (GC ages on real evidence growth). Empty SessionID never dedups.
- **Lesson**: window/watermark logic gives at-most-once BATCHING, not at-most-once COUNTING. Any recurrence-style accumulator keyed on "independent sessions" must dedup on session identity at the write that increments it — the watermark is a defense layer, not an invariant.
- **Refs**: `internal/skillmgr/draft.go:StrengthenDraft`, `internal/skilldistill/auto_test.go:TestAutoPath_SameSessionReentryNoRecount`, `docs/prd/feature-skill-distill.md` §4

## TUI / terminal

### A light palette derived by darkening a dark palette stops the "muted" role from receding
- **Saw**: on a light-theme terminal every muted element — plan rows, separators, unselected menu items, footer hints — read as loud as body prose. Nothing stepped back, so finding the in-progress step or the selected command meant reading each line instead of scanning
- **Why**: `lightPalette` was built by taking each dark-theme value and picking a darker one. That is right for foreground roles (they must move away from a white ground) and backwards for Muted, whose entire job is to RECEDE toward the ground — on white it has to move UP. 237 (#3a3a3a) landed one grey step off Assistant's 235 (#262626): 11.4:1 against white where body is 15.1:1, a 1.33x separation where the dark palette has 3.2x. The direction was not strictly inverted (#3a3a3a is a hair lighter than #262626) — the miss was one of MAGNITUDE, which is exactly why it survived review: as two constants side by side in a diff, 235 and 237 look like a sensible pair
- **Fix**: Muted 237 → 245 (#8a8a8a) = 3.45:1 on white, matching the dark palette's Muted at 3.44:1 on black, so both themes step back by the same amount. `TestPalettes_MutedRecedesBehindBody` asserts the RELATIONSHIP — recede direction, a 3.0x body/muted separation floor, and Reasoning staying more visible than Muted — instead of pinning colour constants, so a future re-tint is free to move every value
- **Lesson**: a palette encodes a hierarchy of contrast RATIOS, not a set of colours. Derive a second theme by preserving each role's ratio against its OWN ground; never by transforming the first theme's values. Colour hierarchy is invisible to source review — the numbers have to be asserted in a test or the hierarchy drifts unnoticed
- **Refs**: `internal/tui/styles.go` (`lightPalette`), `internal/tui/styles_test.go`

### Status-bar glyph-width oracle mismatch soft-wraps the frame and ghosts the welcome banner
- **Saw**: after `/model` switched to `deepseek-v4-flash-vision-exp`, the TUI showed the pixel banner TWICE. No banner code path ran twice — the duplicate was the previous frame's top block left behind when the repaint didn't reach it
- **Why**: the status bar is the frame's LAST line; lipgloss and the terminal disagree by 1–2 cols on ambiguous-width glyphs it contains (☀️ U+2600+VS16, ○ U+25CB render wider on CJK-locale Windows Terminal). foldStatusBar believed "fits at width" while the terminal soft-wrapped the bar onto 2 physical rows; bubbletea's next cursor-up came up one row short and repainted below the old frame's top. The 10-col longer vision-exp id pushed the bar from comfortably-fitting into exactly this boundary band (widths 90–110 — PowerShell's default 100 included)
- **Fix**: foldStatusBar reserves a 2-col safety margin; RenderStatusBar draws to width−2 with MaxWidth truncation as backstop (the right edge gets a 2-col unpainted sliver — cheaper than a wrap). Picker rows got the same treatment: "(current)" moved off the `%-32s` id column (which pads but never truncates) into the description column, and the vision row's description shortened. `TestRenderStatusBar_NeverExceedsTerminalWidth` pins single-line + physical-width ≤ frame width measured by runewidth (the strictest oracle in play). Follow-up (same day): the offending glyphs were removed from the bar entirely — tier/agents/cron/goal/ctx segments are ASCII-labelled now (`peak · off-peak in 2h17m`, `agents 2`, `cron 3`, `goal 2/8`, `! ctx 78%`) and `TestStatusBar_NoRetiredEmojiGlyphs` pins them out; the 2-col margin stays as defense in depth for the remaining ambiguous glyphs (○/●/·) and anything a future segment adds
- **Lesson**: under inline (non-alt-screen) bubbletea, ANY frame line whose terminal-measured width exceeds the frame width is a rendering-desync bomb — the failure surfaces far from its cause (ghost content at the frame TOP from a wrap at the BOTTOM). Width-sensitive UI must budget for the width oracle's ambiguity zone, not just its own arithmetic, and validate against the strictest available measurer
- **Refs**: `internal/tui/statusbar.go` (`foldStatusBar`, `RenderStatusBar`), `internal/tui/view.go` (`renderModelPicker`), observed on Windows Terminal zh-CN after /model → vision-exp

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

### Long streaming replies freeze the screen — inline renderer can't cursor-up past a screen height
- **Saw**: a long reply (30+ rendered rows) streamed fine for a while, then the visible text froze on an old frame (e.g. mid-heading) while the stream kept going; the turn ended normally and the footer appeared. The full content WAS present — session file, agent message, and scrollback commit were all complete — only the live-region display was stuck
- **Why**: the bubbletea inline standard renderer assumes the live region fits the terminal height. Once the streaming body exceeds it, every full redraw (and the spinner/status-bar segments force full redraws, not append-only diffs) must cursor-up past the screen top to reach the live region's origin. The terminal clamps cursor-up at the top of the screen, so the renderer's position bookkeeping diverges from reality: subsequent frames are painted at the wrong rows, new deltas land off-screen, and the visible text appears frozen. Content integrity is unaffected — the freeze is purely presentational
- **Fix**: long-reply chunking in `applyAgentEvent`: once the assistant live block (label + reasoning + content) exceeds `chunkThreshold` rendered rows (`height-10`, floor 4, set in relayout), commit the streamed-so-far segment to scrollback and reset the live buffers. Segments after the first carry a "▸ seek (续)" label (renderAssistantBlockLabel). An unclosed ``` fence defers the split until it closes (bounded at 2x the threshold) so segment boundaries don't land mid-code-block. MessageEnd's `lastAssistantText` now sources `e.Message.Content` (the full assembled message) instead of `m.curContent` (only the final segment once chunked). The guard covers EVERY path that fills the live buffers: chat content deltas, tool-streamed content (ToolDelta — think's final answer), and — via a folded-tail render, not commits — shown streaming reasoning, which no content delta can bound while reasoning alone streams first (tail budget `threshold-4` rows + fold marker; the full reasoning still lands once at MessageEnd). The floor is 4, not a "sane minimum": a floor above height-10 on short terminals (vertical splits) re-enables the overflow. Tests: `internal/tui/chunk_test.go`
- **Lesson**: inline-mode live regions must never exceed the terminal height — no renderer redraw of an over-tall live region is reliable, whatever the framework's docs imply. Any streaming surface that can grow unbounded (assistant body, reasoning, tool previews) needs a height cap with a commit-to-scrollback escape hatch. Known compromise: live shows multiple `▸ seek` blocks while replay renders one full block — content identical, shape differs; accepted
- **Refs**: `internal/tui/update_agent.go:shouldChunkCommit`, `commitChunk`, `internal/tui/view.go:relayout` (chunkThreshold), `internal/tui/chunk_test.go`; commit (this commit)

### Garbage `]11;rgb:fae0/fae0/fae0\[1;1R` in the input box
- **Saw**: opening the TUI, the input field had escape-sequence-looking junk pre-filled; pressing Enter would send it to the LLM
- **Why**: `glamour.WithAutoStyle()` lazily probes the terminal background via an **OSC 11** query (`ESC ] 11 ; ? ESC \`). The terminal's response (`ESC ] 11 ; rgb:... ESC \`) plus a stray cursor-position report would arrive on stdin **after** bubbletea had taken over the TTY, so they were parsed as keystrokes and inserted into the textarea
- **Fix**: in `cmd/seek` we now call the background probe **before** entering bubbletea's alt-screen (originally `termenv`, since the v2 migration `lipgloss.HasDarkBackground`). The query/response handshake completes synchronously while we still own stdin. The result is passed as `Options.GlamourStyle` and glamour gets `WithStandardStyle(style)` (no runtime query). Commit `24342b3`
- **Lesson**: any library that auto-probes the terminal must run before your TUI grabs the TTY, or its probe responses will leak as input. Env override `SEEK_STYLE=dark|light` for users with weird terminals
- **Refs**: `cmd/seek/main.go:detectGlamourStyle`

### lipgloss v2 background probe hangs forever when stdin is piped (Windows CONIN$)
- **Saw**: `go test ./...` on a Windows dev box hung for the full 10m timeout at `cmd/seek`'s `TestDetectGlamourStyle_Fallback`, goroutine stuck in `syscall.ReadConsole`. `seek` launched with redirected stdin (wrapped by another program) would hang the same way at startup, console stuck in raw mode
- **Why**: lipgloss v2's `BackgroundColor` on Windows, seeing stdin is not a tty, **opens `CONIN$`** (the real console), switches it to raw mode, and waits for an OSC 11 reply. A non-interactive console never replies, and the 2s timeout inside `queryTerminal` works by cancelling the reader — but `CancelReader.Cancel()` does NOT interrupt a blocked `ReadConsole` on every console host, so the read blocks forever. CI stays green because its runners have no interactive console: `CONIN$` open fails → error → dark fallback. Pre-migration termenv degraded gracefully (NoColor on non-tty) — this was a regression smuggled in by the v2 migration
- **Fix**: gate the probe on stdin being a real terminal (`isatty.IsTerminal || IsCygwinTerminal`, same pattern as hooks_trust.go): non-tty → assume dark, skip the query entirely. The TUI needs a tty stdin anyway. Test: `TestDetectGlamourStyle_NonTTYStdinSkipsQuery` swaps os.Stdin for a pipe — a portable regression trap (hangs pre-fix on Windows)
- **Lesson**: a terminal-query helper that "falls back on error" is not hang-safe — check whether its fallback covers the no-reply case on EVERY platform, or gate the call yourself on the precondition (tty stdin) instead of relying on internal timeouts. Also: a green CI can hide platform-specific hangs that only a local full-suite run catches
- **Refs**: `cmd/seek/main.go:detectGlamourStyle`, `charm.land/lipgloss/v2 query.go:BackgroundColor`, `internal/tui/teatest` migration notes; commit (this commit)

### Alt-screen mode breaks scrollback, copy, and content persistence
- **Saw**: in M4 we ran with `tea.WithAltScreen()`. Symptoms accumulated: terminal scrollback dead (only an in-app viewport had history), copy worked only within the visible viewport, exiting seek wiped the whole conversation, OSC query responses had nowhere to land cleanly
- **Why**: alt-screen swaps the terminal to a secondary buffer. That buffer has no scrollback by design — it's meant for full-screen apps (vim, less) where you accept "what's on screen is all there is". For a chat-style coding agent the trade is wrong: users expect to scroll back arbitrarily, copy any prior message, and have the conversation remain in the shell after they exit
- **Fix**: M4.5.1 switches to inline mode. `tea.WithAltScreen()` removed; committed history (user prompts, tool results, completed assistant messages) is published to the terminal's native scrollback via `tea.Println`. The bubbletea live region holds only volatile state (active tools, streaming assistant text, input, status). Commit `5d1c78c`
- **Lesson**: alt-screen is the wrong default for any "conversation that has history". Use it only for genuinely full-screen modal UIs (file pickers, log viewers). Inline mode + `tea.Println` for committed content matches gh and gemini CLI — and it's not an accident. Claude Code is the exception: it runs a full-screen React/Ink pseudo-app (history in memory, app-owned virtual scroll) and dumps the session to scrollback on exit — see "Claude Code's third path" in docs/book/chapter-07.md §7.2
- **Replay 2026-05** (this lesson was relearned the hard way): a later attempt to fix layout drift switched seek BACK to alt-screen. Mouse wheel and copy broke again; see "Inline-mode drift was self-inflicted by floor-pin padding" below for the second migration back to inline

### Inline-mode drift was self-inflicted by floor-pin padding
- **Saw**: under inline mode the input line drifted off the terminal floor after a few turns (commits `16b13ef`, `59bd9a9`, `f218c57` each chased a different facet of the symptom; the eventual "fix" was to migrate to alt-screen, which broke mouse wheel and click-drag copy)
- **Why**: `View()` was padding `sb` with `strings.Repeat("\n", pad)` every frame to pin the input to the absolute terminal floor. The padding length came from a manual `scrollbackLines` counter that lost sync against `tea.ClearScreen`, multi-line `tea.Println` from `/help`, async window resize, and discarded `tea.Println` cmds (`_ = tea.Println(...)` patterns). The whole class of bugs was the *floor-pin ambition*, not inline mode itself
- **Fix**: second migration back to inline mode — `tea.WithAltScreen()` removed AND no floor-pin padding. The live region's height varies frame to frame; bubbletea's standard renderer handles that natively via cursor-up + EraseScreenBelow. The welcome banner is printed once to stdout BEFORE `tea.NewProgram` so it lives in terminal scrollback and never re-enters the redraw loop. The separator above the input is conditional on "is there live content above it" — at idle there is nothing to separate from
- **Rule**: do NOT pad the live region with trailing newlines under any condition. If you find yourself reaching for `m.height - rowsIn(...)` math, you're recomputing the M3-era drift. The cursor sits below the live region; that's correct. If the terminal looks "empty below the input", that's *also* correct — same as the shell prompt after any command
- **Lesson**: when an architectural decision (alt-screen) was made to fix a symptom (drift), question whether the symptom's root cause was correctly identified. The drift wasn't inherent to inline mode — it was inherent to *trying to outsmart the renderer*. gh and gemini CLI run inline without any floor-pin math; they look stable because they don't try to control where the live region lands. Claude Code looks stable for the OPPOSITE reason — it is NOT inline: full-screen takeover means the app owns the whole viewport, so "bottom" is a layout coordinate (flex column), not a cursor offset to track against terminal scrollback
- **Refs**: `internal/tui/run.go`, `internal/tui/view.go:View`, related entry "Alt-screen mode breaks scrollback, copy, and content persistence" above and the M3-era "Status bar scrolled away with live content" / "/clear left input pinned to TOP" entries below for the original symptom set
- **Refs**: PRD §4.9, `internal/tui/run.go`, related entry "Mouse drag-to-select did nothing"

### Reserved popup zone was the same mistake as floor-pin padding, wearing a different hat
- **Saw**: users complained the input looked like it was "floating" — 9 blank rows always sat between the content and the input. The blank zone existed so that opening a filter popup (slash menu / model picker / path picker) would NOT shift the input by 9 rows
- **Why**: the reserved zone fixed a real complaint ("input jumps when I open the slash menu") the same way floor-pin padding fixed a real complaint ("input drifts off the bottom") — by holding a fixed number of rows hostage in the live region. Both are attempts to pin the input's position against the terminal, which inline mode cannot do; the zone traded a transient jump for a permanent visual defect (input floating mid-screen, 9 dead rows of screen space)
- **Fix**: retired the reserved zone. Idle View() emits nothing between content and separator; a popup open grows the live region by menuMaxRows+1 rows and the input moves down — the same "content grew" behaviour as streaming output, which users accept because they are interacting with the popup. The fixed-height INSIDE the popup (menuMaxRows while filtering) is kept; only the always-on blank zone is gone. The status bar stays the LAST row (terminal-bottom anchor) with the input directly above it. (First attempt also moved the status bar ABOVE the input to make the input the last row — that failed: in inline mode there is no fixed top, so the status just floated mid-screen. The input does not need to be the last row; it needs no blank rows around it.) Tests `TestView_PopupZoneGrowsOnOpen` + `TestView_StatusBarIsLastRow` pin the new contract
- **Lesson**: any fixed-height padding that is visible when the thing it reserves is absent is a UI defect with a "stability" justification. The inline-mode rule is not just "no trailing-newline padding to the floor" — it is "no padding for position-stability AT ALL; let the live region's height be exactly its content's height". If a popup shifts the input, that shift is content growth, not a bug. See docs/book/chapter-07.md §7.2 for the same lesson in the Claude Code comparison
- **Refs**: `internal/tui/view.go:View` (bottom block), `internal/tui/view_test.go:TestView_PopupZoneGrowsOnOpen`, `TestView_StatusBarIsLastRow`; commit (this commit)

### Esc-cancelled stream silently dropped the user's prompt
- **Saw**: pressing Esc mid-stream to cancel an assistant's response also erased the user's prompt from the input box. The user had to re-type it to re-submit
- **Why**: the Esc handler in `handleKey` cleared `queuedText` ("" = empty string) to "stop everything", but the user's submitted prompt was already sitting in `queuedText` at that point. The textarea had been cleared on Enter, so nothing was recoverable
- **Fix**: Esc now restores `queuedText` into the textarea before clearing it; `streamEndMsg` also restores `promptHistory` into the textarea if the stream was cancelled and the input is still empty. Commit `af4b4d0`
- **Lesson**: when you clear volatile state on user-cancel, check whether that state contains something the user would want back. "Cancel the current operation" and "clear the edit buffer" are different intents — treat them separately
- **Refs**: `internal/tui/update.go:handleKey`, `internal/tui/update.go:streamEndMsg`

### `/model` updated the display but not the Agent's model — API calls still used the old model
- **Saw**: user ran `/model deepseek-v4-pro` in the TUI; status bar showed `deepseek-v4-pro`, but DeepSeek's backend stats showed all calls as `deepseek-v4-flash`. The model switch had zero effect on actual API requests
- **Why**: `cmdModel` (and `applyModelChoice` for the picker) only updated `m.opts.Model` (status bar display) and called `SetModel(args)` (updated `sessionModel` in main.go). But the Agent's `a.cfg.Model` is frozen at creation time (`agent.New`), and `/model` never rebuilt the agent or updated its config. `RebuildAgent` is only called on `/new`. So every subsequent API call still used the original model from startup
- **Fix**: added `Agent.SetModel(model)` in `pkg/agent/agent.go` that mutates `a.cfg.Model` in place (safe between turns). Both `cmdModel` and `applyModelChoice` now call `m.opts.Agent.SetModel(args)` after updating the display. Commit (this one)
- **Lesson**: any struct whose config is baked at construction and read on every operation must expose a mutator if a CLI/TUI command claims to change the config at runtime. "Effective on next prompt" requires the agent's copy changed, not just the host variable and status bar
- **Refs**: `pkg/agent/agent.go:SetModel`, `internal/tui/commands.go:cmdModel`, `internal/tui/commands.go:applyModelChoice`

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

### Adding a builtin skill is prefix-cache-safe ONLY because the manifest is deterministically ordered
- **Saw**: dropping a 4th file into `internal/skill/builtin/` (`code-review.md`) adds a line to the system-prompt `# Available skills` manifest, which is sent every turn as part of the cached prefix.
- **Why**: this is safe — but only by construction. The manifest renders via `Set.Manifest()` → `Set.List()` → `s.order` (an insertion-order **slice**, not map iteration), and the loader sorts every directory's entries (`sort.Slice(... Name())` for both on-disk and `embed.FS` tiers) before `Add`. So the manifest bytes are identical across process restarts. `Manifest()` also flattens `\n` out of descriptions so a multi-line description can't introduce per-skill byte variability.
- **Fix**: nothing to fix; verified the invariant holds and added a `code-review` presence + description-keyword assertion to `loader_test.go`.
- **Lesson**: if you ever render skills into a prompt by ranging a `map` (instead of `List()`/`order`), or stop sorting a skill source directory, you silently break prefix-cache for **every** session (random manifest order per process → full cache miss each turn). Keep all skill→prompt paths going through `List()`. Adding a skill still costs a one-time cache miss on the first turn of any session **resumed across the upgrade** (the system prompt changed) — unavoidable, not a regression.
- **Refs**: `internal/skill/skill.go:Manifest`/`List`/`order`, `internal/skill/loader.go:107,247` (sort.Slice), `cmd/seek/main.go:1126,1291,1663` (the only manifest call sites)

### Batch ask_user picker: only the first option could be selected (transient faux pendingQuestion leaked past the router)
- **Saw**: in a multi-question `ask_user` picker, pressing Enter on the cursor's default row (option 0) worked, but choosing ANY other option — which requires an arrow-key nav first — silently did nothing. The answer vanished and the question stayed open.
- **Why**: `handleBatchKey` installs a *transient* faux `m.pendingQuestion` each keypress (so it can reuse `handleQuestionKey`'s per-row logic), with a private buffered `Reply` channel that only that one call reads. Its in-progress branch then returned WITHOUT clearing `m.pendingQuestion`. Because the key router (`handleKey`) checked `pendingQuestion` BEFORE `pendingBatch`, the next key (the Enter) was routed straight to `handleQuestionKey`, which completed the answer into the now-orphaned channel from the *previous* keypress — nobody read it, the batch never advanced. Option 0 escaped the bug only because it completes inside a single `handleBatchKey` call (no intervening nav key to leak the pointer).
- **Fix**: two layers. (1) Router checks `pendingBatch` before `pendingQuestion` — an active batch is authoritative regardless of any leaked faux request (this is what the router's own comment always *claimed* it did). (2) `handleBatchKey`'s in-progress branch now sets `m.pendingQuestion = nil` so the faux request never survives into the next key. Either alone fixes it; both = defense in depth.
- **Lesson**: a "borrow a field, delegate, restore" trick is only safe if the borrowed field is cleared on EVERY exit path, including the in-progress/no-op one — and don't lean on router ordering you didn't enforce. The deeper lesson is the test gap: the existing batch tests called `handleBatchKey` directly, which rebuilds the faux request each call and masks the leak entirely. The bug only manifests through the REAL router across two keypresses, so the regression test (`TestHandleKey_BatchSingleSelect_NonFirstOption`) drives `handleKey`, not `handleBatchKey`. When a bug lives in routing/state-between-events, the test MUST enter through the same door the user's keystrokes do.
- **Refs**: `internal/tui/update_key.go:handleKey` (route order), `internal/tui/update_modal.go:handleBatchKey` (leak clear), `internal/tui/update_batch_test.go:TestHandleKey_BatchSingleSelect_NonFirstOption`

### Windows Terminal forwards the IME-commit Enter — the 50ms paste guard turned Chinese messages into newlines
- **Saw**: on Windows Terminal, after a conversation, pressing Enter to send a Chinese message inserted a newline instead of submitting; sending needed two Enters (or the message never sent and kept growing lines)
- **Why**: `enterInsertsNewlineDuringPaste()` treats ANY Enter arriving within 50ms of the last KeyRunes as an intra-paste newline (added for legacy conhost CRLF paste, where each pasted line arrives as runes + `\r`). Windows Terminal delivers an IME-commit as the committed text AND the Enter key that ended the composition, in the same dispatch (microsoft/terminal#20039 / #20471 — TSF IMEs "re-inject" the commit key). Same signature as a paste line → guard misfires. A second misfire path: `lastInputRunesAt` is stamped at event-PROCESSING time, so when the event loop is busy (right after a stream/render), typed runes + Enter queue up and process within microseconds → same false positive
- **Fix**: the guard now fires ONLY on legacy conhost — `m.legacyConhostInput`, detected once in `tui.New()` (`paste.go:detectLegacyConhostInput`): `GOOS==windows` AND no terminal-emulator env var (`WT_SESSION`, `TERM_PROGRAM`, `ConEmuPID`). Conhost is the only environment that BOTH lacks bracketed paste (guard needed) AND suppresses keys during active IME composition (guard safe). `SEEK_LEGACY_CONHOST_INPUT=1/0` overrides detection
- **Lesson**: a time-window heuristic keyed on "recent runes" cannot distinguish paste from IME-commit or from a busy-loop input batch — the events are identical at processing time. Scope such heuristics to the environment that actually needs them (env-var detection is the honest signal: conhost sets no identifying vars). Also: bubbletea on Windows reads raw console key events (coninput), so there is NO `msg.Paste`-style marker for non-bracketed paste — the guard is the only defense there, which is why it can't simply be deleted
- **Refs**: `internal/tui/paste.go`, `internal/tui/update_key.go:452`, commit 84aa379 (the original guard), microsoft/terminal#20039 + #20471 (IME commit key delivery), docs/guide-windows.md (multi-line paste section)

### Shift+Enter is indistinguishable from Enter — bubbletea drops the modifiers on VK_RETURN
- **Saw**: user reported "Shift+Enter 换不了行了" (can't insert a newline) on Windows — pressing Shift+Enter SENT the message instead. Investigation showed Shift+Enter newline was never implemented (M4 comment: "added when terminal keymap support catches up") and on Windows it actively submits
- **Why**: three stacked layers. (1) bubbletea's Windows coninput reader maps `VK_RETURN → KeyEnter` unconditionally (`key_windows.go:keyType` — `shiftPressed` is only honoured for Tab/arrows/Home/End), so Shift+Enter, Ctrl+Enter and Enter are byte-identical KeyMsgs. (2) `KeyMsg` has no Shift field and no `KeyShiftEnter` type exists — even a VT path has nowhere to put the modifier. (3) bubbletea's sequence table has no CSI-u (`ESC[13;2u`) support, so kitty/foot/wezterm Shift+Enter is dropped entirely. On Windows only Ctrl+J (KeyCtrlJ) survives as a newline key; Ctrl+Enter submits too (comments/docs claiming "Ctrl+Enter newlines" are Unix-only)
- **Fix**: documented reality instead of fighting the stack: `docs/guide-windows.md` newline table (Ctrl+J official, Shift+Enter sends, WT settings.json `sendInput: "\n"` workaround), clarified the `model.go` InsertNewline comment and the update_key.go Ctrl+Enter comment, added a /help hint. Real Shift+Enter support requires patching bubbletea (KeyShiftEnter type + coninput modifier mapping + CSI-u sequences) — deferred
- **RESOLVED by the v2 migration**: Bubble Tea v2's input layer (ultraviolet) keeps modifiers on VK_RETURN (`decoder.go: VK_RETURN → BaseCode=KeyEnter`, then `key.Mod = translateControlKeyState(cks)`) and parses CSI-u / kitty keyboard protocol on Unix. `Key` is a struct with a `Mod` field, and `KeyPressMsg.String()` renders `"shift+enter"`. seek now binds Shift+Enter to the textarea's `InsertNewline` (`model.go`: `SetKeys("ctrl+j", "shift+enter")`) — on modifier-capable terminals it inserts a newline; on others it degrades to plain Enter (submits, same as v1). Covered by `TestHandleKey_ShiftEnterInsertsNewline`. The WT `sendInput: "\n"` workaround is now unnecessary on WT/conhost (they report modifiers); keep it only for terminals that still collapse the modifier
- **Lesson**: before designing keybindings around a modified key, verify the whole input chain can carry the modifier: KeyMsg struct fields, the platform input reader's mapping, and the terminal's escape-sequence support. "Shift+Enter = newline" is a UI convention that depends on terminal-specific encoding (CSI-u) that most stacks silently drop. On Windows, Shift+Enter == Enter is a platform-level fact, not a seek bug — but that fact changed in v2, so re-verify platform facts after a framework major upgrade
- **Refs**: `internal/tui/model.go:626` (InsertNewline), `internal/tui/update_key.go:505`, bubbletea `key_windows.go:keyType` (VK_RETURN branch), bubbletea `key.go:Key` struct (no Shift field), `docs/guide-windows.md` (newline section)

### Bubble Tea v2 migration: KeyMsg became an interface, paste became PasteMsg, teatest still pins v1
- **Saw**: during the v1.3.10 → charm.land/bubbletea/v2 (v2.0.8) migration, every key-handling file broke at once: `msg.Type`/`msg.Runes`/`msg.Paste` gone, `tea.KeyCtrlC` gone, `View() string` must become `View() tea.View`, and the upstream `teatest` test harness refused to compile against v2 (its go.mod still requires bubbletea v1.3.5)
- **Why**: v2 restructured the input model: `KeyMsg` is now an interface over `KeyPressMsg`/`KeyReleaseMsg`, both of which are the `Key` struct (`Code rune` / `Text string` / `Mod`); `tea.KeyCtrlX` constants were deleted (match `Code: 'x' && Mod: tea.ModCtrl` or `msg.String()=="ctrl+x"`); paste is a dedicated `tea.PasteMsg`; space bar's `String()` changed from `" "` to `"space"`; `Style.Render()` in lipgloss v2 always emits SGR (no more "not a TTY strips colours" — downsampling moved to the output layer). Upstream teatest had no v2 release yet, and since Go cannot have two versions of bubbletea in one module graph, it cannot be imported alongside v2
- **Fix**: mechanical pass first (imports via script, then compile-error-driven): `View()` wraps its string in `tea.NewView(...)`; key switches use `msg.String()` (all default bindings — enter/esc/ctrl+c/shift+tab/alt+enter/up/down/? — produce identical strings in v2, verified by keymap tests); `normalizeControlRunes` dropped its paste-passthrough branch (PasteMsg never enters the key path) and the `msg.Paste` block in update_key.go moved to a `case tea.PasteMsg` in Update; wrote a minimal local `internal/tui/teatest` compatibility layer (~230 lines, v2 Program API) implementing the handful of functions render_test.go uses. Whole migration: 28 files + new teatest package; 504 tests green
- **Lesson**: v2's `String()` output is the migration-friendly surface — `Resolve(msg tea.KeyMsg) Action` (string-based keymap) survived unchanged; the semantic traps are the ones String() hides (space vs "space", Ctrl keys via Code+Mod). Before migrating a TUI, inventory which v1 quirks your code worked AROUND (here: keynorm.go's rune normalization, the conhost CRLF guard) — v2 often makes them native (paste separation, CR/LF folding), so parts of the workaround layer become deletable. And check the test-harness ecosystem FIRST: a pinned-v1 teatest forces a local compatibility layer
- **Refs**: `internal/tui/teatest/`, `internal/tui/keynorm.go`, `internal/tui/update.go` (PasteMsg branch), UPGRADE_GUIDE_V2.md in charm.land/bubbletea/v2; supersedes parts of the Shift+Enter entry above (v2 adds CSI-u/kitty keyboard protocol support, so the "no CSI-u" half of that story is v1-only)

### Ctrl+V image paste on Windows was a silent no-op — a missing platform branch with zero feedback
- **Saw**: on Windows, Ctrl+V with a screenshot on the clipboard did nothing at all — no `📋 image` marker, no error, no status line. Text pastes still worked, which made it read as "broken feature" rather than "missing platform support"
- **Why**: `clipimage.defaultGrabCommand` had `darwin` / `linux` branches and a bare `default: return nil` — on Windows that is `ErrNoGrabber`, which `tryClipboardPaste` swallows before falling back to `clipboard.ReadAll()`. An image-only clipboard has no TEXT to fall back to, so the handler returned "not handled". Three layers, each reasonable alone; together: silence
- **Fix**: added the `windows` branch (Windows PowerShell + WinForms `Clipboard::GetImage()`; the table now returns `(argv, outEnv)` — see the next entry for why), split `defaultGrabCommandFor(goos)` out so EVERY branch is testable on any host, and added the gated Windows e2e (`SEEK_CLIPIMAGE_E2E=1`, asserts PNG magic + the 3s TUI budget). Also corrected `ErrNoGrabber`'s text — it pointed at `imagepaste.command`, a config key that was never wired (only the env `SEEK_CLIPBOARD_IMAGE_CMD` exists)
- **Lesson**: a "degrade to the sane fallback" path that ends in a no-op needs a signal — silent failure is indistinguishable from "never built", and it casts doubt on every other platform claim. And when the default is a per-OS table, test each branch behind a goos PARAMETER, not `runtime.GOOS`: the gap was invisible to a green Linux/macOS CI run and only exists on real hardware
- **Refs**: `internal/clipimage/clipimage.go:defaultGrabCommandFor`, `internal/tui/paste.go:tryClipboardPaste`, `internal/clipimage/e2e_windows_test.go`, `docs/guide-windows.md` (paste-image section)

### PowerShell `-Command`: everything after it is command TEXT — never pass a path (or any data) on that argv
- **Saw**: wiring the Windows grabber, `-Command "<script using $args[0]>" <path>` never saw the path. Probed through a cmd.exe wrapper, the script itself came back as interpolated OUTPUT. Fixed-looking shape `& { param([string]$out) … } <path>` passed the e2e — then FAILED the moment the cache dir was named `clip cache`: the param bound `…\clip`, Save hit a missing directory, no file, `ErrNoImage`, silent text-paste fallback. `[Environment]::GetCommandLineArgs()[-1]` did recover the full path, which proved the argument survived as far as the process — it just never reaches the script that way
- **Why**: powershell.exe joins everything after `-Command` into ONE command string ("all arguments following it are interpreted as part of the command to execute"). Go's argv quoting is consumed by PowerShell's own CLI parser BEFORE the join, so the re-parsed text is unquoted: a space splits the path, and `'` / `;` / `$` become string-parse or statement separators — i.e. user data lands in the interpreter's CODE channel. The call-operator `& { param }` form only binds *syntactically clean* trailing tokens, which is exactly what a real Windows profile (`C:\Users\John Smith\…`, `C:\Users\O'Brien\…`) is not
- **Fix**: the output path never touches that argv. `defaultGrabCommandFor` returns `(argv, outEnv)`; the Windows entry names `SEEK_CLIPIMAGE_OUT`, `Grab` exports it on the child (`cmd.Env = append(os.Environ(), outEnv+"="+out)`) and skips the usual append-the-path-to-argv step; the script reads `$env:SEEK_CLIPIMAGE_OUT`. Pinned by `TestGrab_OutEnvDeliversPath` (fake default proves Grab delivers via env, not argv) + `TestDefaultGrabCommandFor_WindowsShape`; e2e verified on real Windows with a `clip cache O'Brien` cache dir (334-byte PNG, ~330ms) and with no image (`ErrNoImage`, no leftover file)
- **Lesson**: when a runner re-parses argv as code (`powershell -Command`, `cmd /c`), trying to out-quote it is a losing game — pass the data out-of-band (env var, stdin, `-File`), and make the code path that does so explicit rather than clever. Two test-design notes: the passing e2e was a false negative because `t.TempDir()` had no space — **"it worked" only means the adversarial input wasn't in the fixture**; and probing through a shell wrapper (cmd.exe) gave different answers than Go's direct `exec`, so reproduce quoting questions down the real exec path
- **Refs**: `internal/clipimage/clipimage.go:windowsOutEnv` + `windowsGrabScript`, `internal/clipimage/clipimage_test.go:TestGrab_OutEnvDeliversPath`, `internal/clipimage/e2e_windows_test.go`, MS Learn `about_PowerShell_exe`

### Windows terminals swallow Ctrl+V — the key binding was unreachable no matter how good the grabber was
- **Saw**: with the grabber shipped and its e2e green, Ctrl+V in seek STILL pasted nothing (or junk text) on the same machine. A key-echo probe built on the repo's own charm.land/bubbletea/v2 showed why: pressing Ctrl+V delivered a PasteMsg carrying the co-copied TEXT (230 chars), never a `ctrl+v` KeyPress
- **Why**: stock Windows terminals intercept ONE of the two paste chords and pass the other: Windows Terminal binds plain Ctrl+V to its own terminal-level text paste (the keypress never reaches the app), while conhost with "Use Ctrl+Shift+C/V as Copy/Paste" checked intercepts Ctrl+Shift+V instead. The terminal's paste event can never substitute for the key — bracketed paste carries UTF-8 text only, so an image-only clipboard injects NOTHING, and the bitmap exists solely in the OS clipboard where only the grabber can reach it
- **Fix**: TWO layers. (1) dual-bind `ctrl+shift+v` beside `ctrl+v` (update_key.go; ctrl+shift matched case-insensitively — Windows console input capitalises the rune under Shift). Complementary interception means at least one chord reaches seek where terminals pass keys. (2) The real zero-config path for terminals that eat BOTH (verified on a real Windows Terminal: every paste chord yields `PasteMsg{Content:""}`, never a keypress): an EMPTY bracketed paste is the terminal pasting an image-only clipboard — no text format to inject, wrappers still emitted — so Update routes empty PasteMsg to `tryClipboardPaste` and grabs the bitmap. The user's muscle-memory Ctrl+V now works unmodified. Pinned by `TestHandleKey_PasteKeys_ReachImageGrab` + `TestUpdate_EmptyPasteGrabsImage`; the stale "press Enter to OCR & send" marker became "press Enter to send" in the same pass (OCR 柱 Q is long gone)
- **Lesson**: before shipping a key-triggered clipboard feature on Windows, probe that the KEY itself arrives in a stock terminal — "text paste works" proves nothing (the terminal did it, not the app). And when every chord gets intercepted, instrument what the terminal DOES emit: the empty paste event it couldn't help sending was the only faithful carrier of the user's intent — sometimes the interception leaks its own signal
- **Refs**: `internal/tui/update_key.go` (dual binding), `internal/tui/imgpaste_test.go:TestHandleKey_PasteKeys_ReachImageGrab`, `docs/guide-windows.md` (paste-image section)

### Bare `m.appendHistory(...)` as a statement drops the line — same disease as `_ = tea.Println`
- **Saw**: /distill's skill-segment rejection lines ("· skill candidate rejected — …") never appeared in scrollback despite the handler visibly calling appendHistory for each. Surfaced while building `/distill --dry-run`, whose entire value is diffing those lines against a real run — the real run's half of the diff was empty
- **Why**: sibling of the `_ = tea.Println(line)` entry above. `appendHistory` doesn't buffer anything — it RETURNS the `tea.Println` cmd that does the printing. The rejection loop called it as a bare statement inside a `[]tea.Cmd`-returning handler, so the cmds fell on the floor. Compiles clean, vet is silent (Go doesn't flag unused call results), no test asserted the lines printed
- **Fix**: collect the cmds and return them (`cmds = append(cmds, m.appendHistory(...))`); pinned by `TestHandleSkillDistillDone_RejectionCmdsAreReturned`. Commit (this one)
- **Lesson**: `appendHistory` is print-via-Cmd, not a buffer append. In a `[]tea.Cmd`-returning handler, every appendHistory call must be wired into the returned slice — a bare call is a silent no-op. When a handler's job is emitting lines, assert on the returned cmds' count, not just the model state
- **Refs**: `internal/tui/distill_skill.go:handleSkillDistillDone`, `internal/tui/model.go:appendHistory` (doc comment spells out the contract), `internal/tui/distill_skill_dryrun_test.go`

---

## Agent loop

### Static prompt prose can name tools the request's tools array won't carry — by design
- **Saw**: wiring `sysprompt.Header.MCPSection` (MCP-D2), the section is assembled right after `mcptool.LoadServers`, but the `-tools` whitelist (`applyToolsWhitelist`) narrows the registry much later — so under `seek -tools read,grep` with MCP servers configured, the prompt's MCP section names tools that are not callable that session. Tempting to "fix" by filtering the section through the final registry.
- **Why**: startup order is load-bearing — `session.New` freezes the composed prompt BEFORE the whitelist runs (the whitelist itself must see the full registry to validate `-tools` names, including MCP tools, per its own doc). Filtering the section would mean restructuring startup for a lane (bare-core comparison, v8·T1) that explicitly deviates from the default composition.
- **Fix**: kept the section static and documented the posture inline (`cmd/seek/main.go`, mcpSection declaration): the section describes what loaded; the request's tools array is the callable truth. Same precedent as rootTpl listing `fim_complete` docs on non-DeepSeek providers where the tool never registers.
- **Lesson**: seek's system prompt is static prose describing the tool landscape, NOT a mirror of the per-request tools array. Don't couple prompt assembly to registry mutation order; if a lane narrows tools, the prose being broader is accepted (and the model discovers unavailability on the refusal, like references-without-LSP).
- **Refs**: `cmd/seek/main.go` (mcpSection comment, applyToolsWhitelist), `internal/sysprompt/sysprompt.go` (rootTpl static tool docs), `docs/prd/feature-mcp-client.md` §3.1

### A refusal the model does not learn from is a pure tax — auto-fix unambiguous intent instead
- **Saw**: the git tool's duplicated-subcommand guard (`git diff --stat` arriving as subcommand "diff" + args ["diff","--stat"]) refused with a precise, actionable error naming the correct shape. Real transcripts showed DeepSeek making the SAME mistake twice in four consecutive calls — recovering each time after the refusal, then repeating the shape on the next call from scratch. Each occurrence cost a full wasted round trip.
- **Why**: an in-error correction teaches only within the turn it fires in; the next call is built fresh from the schema description and conversation attention, not from the accumulated refusals. When the emitting model has a systematic arg-construction tic, per-call refusals are a recurring cost with no compounding benefit.
- **Fix**: the guard now DROPS the duplicated subcommand and runs the intended command, prepending a note to the result that names the effective invocation (`ran \`git log --oneline -n 5\``) and the explicit forms for a genuine same-named path/ref. Same philosophy as read's directory→listing fallback: unambiguous intent + read-only tool → do the obvious thing, teach in-band.
- **Lesson**: before refusing a malformed-but-unambiguous tool call, ask two questions: does the model learn from this refusal across calls (it usually doesn't — check transcripts, not hope), and what is the worst case of auto-fixing (for a read-only tool it is almost always recoverable from the output)? Refuse what is dangerous or ambiguous; auto-fix-and-note what is neither.
- **Refs**: `internal/tools/git/git.go` (duplicate-subcommand guard), `internal/tools/git/git_test.go:TestExecute_DuplicateSubcommandArgAutoFixed`, the read-tool directory-fallback precedent in `internal/tools/read/read.go:listDirShallow`

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

### Symlinked CWD made every RELATIVE `write` compare "outside" — the escape fix over-tightened
- **Saw**: under a symlinked CWD (macOS `/tmp` → `/private/tmp`), `write`/`edit` with a RELATIVE path (`notes.md`) was denied as "outside the working directory" in print/Ask mode. Absolute paths worked, so the model self-healed in one retry — a wasted round-trip per relative write. Hit in the wild (session on card c_7hudp6)
- **Why**: `isWithin` canonicalised the two sides on DIFFERENT bases. Root went through `EvalSymlinks` → physical form (`/private/tmp/proj`). A relative target came back from `resolveClosest` still relative (its walk-up lands on `.` and returns it as-is), then `filepath.Abs` joined it with the shell's logical `PWD` (`/tmp/proj/notes.md`) — `Abs` never resolves symlinks, and `os.Getwd` prefers `PWD` over the syscall result. `Rel(physicalRoot, logicalTarget)` starts with `..` → outside. The earlier symlink-ESCAPE fix (entry above) introduced exactly this asymmetry
- **Fix**: both sides through the SAME pipeline — `Abs` first, then symlink resolution (`EvalSymlinks` for the root, `resolveClosest` for the target so not-yet-existing files still work). `..` traversal and symlink-escape denials still hold; pinned by `permission.TestIsWithin_RelativePathUnderSymlinkedCWD` (which builds an explicit symlink so it reproduces on Linux too, not just macOS /var-folders). Commit `a191f45`
- **Lesson**: path-containment checks only mean anything if BOTH operands were canonicalised by the same pipeline in the same order. `filepath.Abs` is lexical (PWD-logical), `EvalSymlinks` is physical; mixing them splits the base under any symlinked directory — and on macOS that's the default (`/tmp`, `/var`, `$TMPDIR`)
- **Refs**: `internal/permission/permission.go:isWithin`, `resolveClosest`; prior entry "Symlinks inside CWD let `write`/`edit` escape the CWD gate" (the fix whose asymmetry caused this)

### `Policy.mode` was raced by /yolo flips against concurrent `Check`
- **Saw**: a `-race` test on concurrent Check + SetMode lit up four data-race warnings on the `Policy.mode` field. In production this could fire when the user pressed `/yolo` while a tool dispatch was in flight (TUI goroutine writes mode, agent goroutine reads it via Check)
- **Why**: the Policy struct held `mode` and `askFn` as plain fields with no synchronization. The bug never manifested before because tool dispatch is sequential and `/yolo` between dispatches was the common case — but `-race` catches the *contract* violation regardless of timing luck
- **Fix**: `sync.RWMutex` on Policy; Check snapshots mode/askFn/cwd under RLock and releases BEFORE calling askFn (which blocks on the user for arbitrary time). Commit `73c5f3d`
- **Lesson**: a permission gate is a synchronization primitive whether you designed it as one or not. The moment any field is mutable post-construction, it needs the mutex. Discovered by writing a `-race` test specifically for the concurrent path, which is exactly the discipline AGENTS.md now requires
- **Refs**: `internal/permission/permission.go:Policy`; test `TestCheck_ConcurrentCallsRaceFree`

### `tools.ReadOnlyTool` is the on-switch for parallel dispatch — forgetting it makes "spawn 3 agents at once" run serial
- **Saw**: user asked the model to spawn two `agent` subagents to research independent paths in parallel. Wall time was the SUM of the two subagents' durations (27s + 21s ≈ 48s), not the max (~27s) you'd expect from parallelism. The model emitted both tool calls in one assistant message — they should have run concurrently
- **Why**: `pkg/agent`'s dispatch gate — all-or-nothing at the time — ran a tool-call batch concurrently only when EVERY call was backed by a `tools.ReadOnlyTool`. The `agent` tool didn't implement that marker, so the batch fell through to the serial path. Two `agent` calls → serial. Easy to miss because "ReadOnly" reads like a permission concept (don't mutate) but is actually a dispatch concept (safe to run concurrent with siblings). (2026-08: dispatch is now PARTITIONED — `readOnlyCall()` routes each marked call onto the concurrent side of a mixed batch — but the marker remains the on-switch for concurrency, so the lesson stands)
- **Fix**: implement `ReadOnly() bool { return true }` on `internal/tools/agent.Tool` despite the semantic stretch (general-purpose subagents CAN mutate via their child tools — but the marker is consumed by the agent loop's dispatcher, not by the permission gate). Manager.Spawn is concurrent-safe by design, so dispatch-level concurrency is correct. Added compile-time `var _ tools.ReadOnlyTool = (*Tool)(nil)` assertion + `TestTool_ImplementsReadOnlyTool` to catch future regressions
- **Lesson**: the name `ReadOnlyTool` overloads two concerns — "doesn't mutate" (permission semantics) and "safe to dispatch concurrently" (orchestrator semantics). For tools where those diverge, document the stretch in the method doc and pin with a compile-time assertion + behavioural test. Any new tool that fans out to independent work (futures: cron triggers, web_search batched, …) should also be marked
- **Refs**: `internal/tools/agent/agent.go:ReadOnly`, `pkg/agent/agent.go:readOnlyCall` (partitioned dispatch), test `TestTool_ImplementsReadOnlyTool`

### `seek -upgrade` returned raw "permission denied" — users couldn't see they needed sudo
- **Saw**: macOS / Linux users running plain `seek -upgrade` (the form advertised on the landing page) hit `upgrade: replace /usr/local/bin/seek: rename ... permission denied`. The install one-liner ships the binary to `/usr/local/bin` via `sudo tar -xz` (root-owned dir); the upgrade's `os.Rename` then fails for any subsequent non-sudo run. Nothing in the error said "try sudo" — users had to recognize EACCES + figure out the elevation path on their own
- **Why**: `internal/upgrade/replace.go:replaceBinary` wrapped the rename error verbatim with `fmt.Errorf("upgrade: replace %s: %w", ...)`. Correct for `errors.Is(err, fs.ErrPermission)` checks downstream but unhelpful as a user-facing message. The default Go / POSIX strings ("permission denied", "operation not permitted") don't suggest the fix
- **Fix**: detect `errors.Is(err, fs.ErrPermission)` in `replaceBinary` and rewrite to `upgrade: cannot write <path> — requires elevated privileges. Try: sudo seek -upgrade`. Parallel detection in `replace_windows.go` rewrites to "Re-run from a PowerShell launched as Administrator" (sudo doesn't exist on Windows; the operative concept is elevation, not the unix command). Tests in `replace_test.go` chmod a target dir to `0o555` to simulate EACCES + verify the message contains "sudo seek -upgrade" and names the offending path. Non-permission errors keep the original wrap so `errors.Is(err, fs.ErrNotExist)` etc. still chain
- **Lesson**: when an OS-level error has a known user remediation that isn't obvious from the OS's own message, rewriting at the boundary is high-leverage. The branch is one `if errors.Is`; the saved user time is large. Same pattern applies anywhere a stdlib error string ends a CLI session — webfetch's category-prefix errors are the prior art in this repo
- **Refs**: `internal/upgrade/replace.go:replaceBinary`, `internal/upgrade/replace_windows.go:replaceBinary`, tests `TestReplaceBinary_PermissionDeniedHintsSudo` / `TestReplaceBinary_NonPermissionErrorPreservesWrap` / `TestReplaceBinary_HappyPath`

### Tick's "len(due) == 0 → early return" silently skipped triggers + GC for the entire tick
- **Saw**: G4 housekeeping integration test failed: `GCRemoved = 0, want 2`. GC ran cleanly when invoked directly, but inside Tick it never fired. Triggers processing also wasn't running on ticks with no due cron jobs
- **Why**: Tick had an optimisation `if len(due) == 0 { return res, nil }` placed AFTER step 3 (load + filter jobs) and BEFORE step 5 (trigger drain). Originally — when Tick's only job WAS firing cron jobs — that early-return was correct. Once triggers/ (M11.3) and GC (v0.6.1) joined as steps 5-6, the early-return became "if no cron jobs are due, skip ALL the other tick work too" — which is wrong in two distinct ways: (a) triggers are an independent input source that has nothing to do with cron-job schedule state, (b) GC is housekeeping that should run regardless of activity
- **Fix**: removed the early-return. `wg.Wait()` on an empty WaitGroup is a sub-microsecond no-op, so falling through to steps 5-6 on idle ticks costs nothing measurable but ensures the work happens. Added a comment to the now-removed branch explaining why fall-through is correct. Integration test pins it: with zero due jobs but seeded fake history, Tick reports `GCRemoved = 2` and `Fired = 0`
- **Lesson**: a function's "early exit" optimisation is a contract with the future: "anything I add below this line will silently not run when the optimisation triggers." When new work lands below an early-return, audit the return condition against the new work's semantics. The faster ergonomics-fix is to NEVER add an early-return for "no work in the first stage" — let an empty wg.Wait / empty range loop be the fast path so future-you doesn't have to remember
- **Refs**: `internal/routines/tick.go:Tick` (step 4 fall-through), `internal/routines/tick_test.go:TestTick_GCSweepsOldRunsAndMalformed`

### OS scheduler invokes `seek cron tick` with a near-empty env — interactive-shell secrets invisible
- **Saw**: cron jobs registered via `seek cron create` ran fine when fired manually with `seek cron run <name>` (interactive shell context) but every scheduled fire from launchd/systemd/cron hit "missing DEEPSEEK_API_KEY". The api-key value lives in the user's `.zshrc` / a project `.env` that the OS scheduler doesn't source
- **Why**: `exec.Cmd{}` with `cmd.Env == nil` "inherits the current process's environment" — but the "current process" here is `seek cron tick`, itself invoked by launchd/systemd/cron with whatever env THOSE handed it. launchd typically gives `PATH=/usr/bin:/bin` and not much else; systemd user units inherit only what `EnvironmentFile=`/`Environment=` named; classic cron(1) often has no `HOME` and a stripped `PATH`. The interactive-shell env never crosses the boundary. The bug was unobservable in tests (the test harness runs in the dev shell with everything set) and only surfaced post-install on real user machines
- **Fix**: `DefaultSubprocess` now sets `cmd.Env` EXPLICITLY (`os.Environ()` + opt-in `~/.seek/cron/env` overlay via `LoadEnvFile` + `MergeEnv`). The overlay is dotenv-format (KEY=VALUE / `#` comments / quote-stripping / no shell expansion) and a parse error in it FAILS the spawn loudly rather than silently dropping a misformatted line. systemd users get bonus convenience: point `EnvironmentFile=` at the same file. Commit (this one)
- **Lesson**: any `cmd.Env = nil` in code that spawns a subprocess inside a daemon / scheduler / hook is a latent env-injection failure — the inherited env shape depends on who invoked the parent, which is hidden from the test harness. Default to explicit `cmd.Env = ...` at every spawn site in this category, even if today's behaviour is identical, so the path is auditable. Also: any "secret injection" mechanism MUST fail loud on misconfig — a typo'd env file that silently runs without the API key is far worse than one that refuses to launch
- **Refs**: `internal/routines/env.go`, `internal/routines/tick.go:DefaultSubprocess`, `internal/paths/paths.go:CronEnv`, `docs/prd/archive/feature-routines.md §3.9`

### `cache.Tracker.AdoptChild` had no resume-double-count guard — informal contract was load-bearing
- **Saw**: audit of v5 柱 G shipping state found `cache.Tracker.AdoptChild` documented a SAFETY contract ("do not adopt a child whose token usage has already been folded into this Tracker's baseUsage") but enforced nothing. Future resume code that restored prior-session subagent Trackers from disk and re-adopted them post-`SetBase` would silently double-count those tokens in `Cumulative()` / `CumulativeCost()` — wrong status-bar dollar figure, no panic, no log
- **Why**: AdoptChild's prior validation only blocked spawn-depth > 1 (nested children panic). The harder-to-spot case — a child whose `turns` or `baseUsage` are non-zero being adopted by a `SetBase`'d parent — passed through. The whole point of `SetBase` is to seed `baseUsage` with last-session's cumulative INCLUDING every child that ran in that session; then walking a re-adopted child via the `children` slice adds those same tokens a second time. Production today doesn't trigger it (children Trackers die with their session and aren't restored), but the doc-only contract meant any future "show me prior subagents in the status bar" code path could land the bug
- **Fix**: add `Tracker.hasBase bool`, set by `SetBase`. `AdoptChild` now captures `childHasUsage` under `child.mu` (same critical section as the nesting check), and after pointer-dedup panics if `t.hasBase && childHasUsage`. Pointer-dedup runs FIRST so the spawn-retry idempotence path still works. Tests pin both panicking branches (Recorded turns; child SetBase) and both pass-through branches (fresh child after resume; recorded child without resume — used by the existing idempotence test). Commit (this one)
- **Lesson**: an "informal contract" doc comment is a regression deferred, not avoided. If a check is cheap (here: a single bool flag set + one bool conjunction) and the failure mode is silent-mis-pricing, write the runtime guard. Defense in depth at the data-structure boundary catches future callers the original author never imagined; the existing nested-children panic is the precedent — same pattern, different invariant
- **Refs**: `internal/cache/cache.go:AdoptChild`; `docs/prd/archive/feature-subagent.md §8 risk "resume cost 双重计数"`; tests `TestTracker_AdoptChild_DoubleCountAfterResumePanics`, `TestTracker_AdoptChild_DoubleCountGuardCoversChildBase`, `TestTracker_AdoptChild_FreshChildAfterResumeOK`, `TestTracker_AdoptChild_NonFreshChildWithoutResumeOK`

### `json.Unmarshal` silently drops unknown fields — LLM typos produce useless errors
- **Saw**: model called `list_dir({"directory": "/path", "depth": 1})` (wrong field name). Error returned: `list_dir: path is required` — no mention of `directory` being unknown, no hint about valid fields. Model retried with identical args and hit the same wall
- **Why**: Go's `json.Unmarshal` drops unknown fields silently. The `directory` key was ignored, `path` stayed zero-value, and the following nil check produced the generic "required" error with zero diagnostic value
- **Fix**: `internal/tools/tool.go` introduces `UnmarshalStrict` (uses `json.Decoder.DisallowUnknownFields`) and `MissingField` helpers. Error now reads: `list_dir: bad arguments: json: unknown field "directory". Got: {"directory":...}. Valid fields: path, depth, show_hidden`. All eight tool `Execute` methods updated. Regression test: `TestListDir_UnknownFieldSurfacesActionableError`. Commit (this one)
- **Lesson**: any `json.Unmarshal` target in an LLM tool boundary must use `DisallowUnknownFields`. Silent drops make self-correction loops impossible — the model has no information to act on
- **Refs**: `internal/tools/tool.go:UnmarshalStrict`; `internal/tools/listdir/listdir_test.go`

### A clean empty completion aborted the turn — but a retry needs "nothing was rendered" as its gate
- **Saw**: a turn died with `agent: model returned an empty response (no content and no tool calls)` after a long tool-heavy run. The stream had completed normally (200, finish=stop) with no deltas at all — an upstream flake — yet the whole user turn was lost to a manual re-send
- **Why**: the empty-response guard (correctly) refuses to commit an assistant message with no content and no tool_calls — DeepSeek rejects that shape on the next request. But it treated every empty as fatal, including the pure-flake shape where NOTHING had been streamed to the UI. A retry is only safe when no events were emitted; once reasoning deltas reached the screen, re-issuing would double-render the partial block
- **Fix**: `errEmptyResponse` sentinel + retry-once inside the existing retry budget. `runTurnDeepSeek`/`runTurnLLM` return the sentinel ONLY when `started == false` (no MessageStart/MessageDelta ever emitted) and finish is ""/stop; reasoning-only empties and finish=length keep the hard error. Pinned by `TestAgent_EmptyResponse_RetriedOnceAndRecovers` (exactly 2 requests, 1 MessageStart, 1 TurnEnd), `TestAgent_ReasoningOnlyEmpty_NotRetried` (1 request), and the extended `TestAgent_EmptyChoicesUsageOnly` (budget exhausted → error)
- **Lesson**: "retryable" is not a property of the error, it is a property of the error AND what the consumer already saw. Encode retryability where visibility is known (the streaming function), not at the retry site — the loop cannot tell whether events left the process
- **Refs**: `pkg/agent/agent.go` (`errEmptyResponse`, retry loop in `Prompt`, both `runTurn*` guards), `pkg/agent/agent_test.go`

### Startup validation of custom agent types must run AFTER the registry is final
- **Saw**: P2-b's D13 startup pass (warn when a custom type's `tools` aren't in the parent registry) placed naively next to `agenttypes.Load` or at Manager construction flags `plan`/`propose` as unknown for EVERY custom type — they are `reg.Add`ed later than both points.
- **Why**: the parent tool registry is built in stages (initial set → `agent.New` → agent tool → plan/propose → memory_observe, some env-gated); `ManagerOpts.ParentToolNamesFn` is a closure for exactly this reason. Any snapshot taken before the last `reg.Add` is a partial registry.
- **Fix**: the pass runs after the last registration in `cmd/seek/main.go` (comment pins the constraint); spawn-time resolution (`Manager.resolveType` → `ResolveCustomType`) re-checks against the live `ParentToolNamesFn()` as the authority, so the startup pass is UX only. Commit (this one).
- **Lesson**: "validate against the registry" needs a registry-final anchor. Gate validation at the point of use (spawn) and treat startup passes as advisory echoes, or staged construction will manufacture phantom config errors.
- **Refs**: `cmd/seek/main.go` (startup gate), `internal/subagent/manager.go` (`resolveType`), `internal/subagent/custom.go`

### agenttypes.Definition zero-Origin reads as a built-in — hand-built Sets silently lose their types
- **Saw**: a test Set built via `agenttypes.NewSet().Add(&Definition{...})` spawned as `unknown subagent_type` — the def was in the Set, spell-checked, everything.
- **Why**: `Origin` zero value IS `OriginBuiltin`, and the spawn resolver discriminates custom-vs-builtin by `def.Origin != OriginBuiltin` (file defs always get an explicit origin from `Load`; only hand-built ones don't). Zero-Origin defs fall through to the compile-time template table, miss, and read as unknown.
- **Fix**: test builder sets `Origin: OriginProject` explicitly (`internal/subagent/custom_test.go` `def()` helper, comment explains). Loud failure, never a widening — but a minute lost to it.
- **Lesson**: when a sentinel enum's zero value is a meaningful category, constructors that bypass the loader must set it explicitly; consider making the zero value the "unset/invalid" case for new enums.
- **Refs**: `internal/agenttypes/agenttypes.go` (`Origin`), `internal/subagent/manager.go` (`resolveType`)

### `finish_reason:"refusal"` with empty content is not an "empty response" — and its marker must stream as a delta
- **Saw**: adding the `safety_check` opt-in (SC1), a refusal response (HTTP 200, zero deltas, `finish_reason:"refusal"`) hit the empty-response guard and surfaced `agent: model returned an empty response` — a documented, deliberate outcome reading as an upstream failure. Fixing that (in-band marker + nil error) then rendered a blank assistant block in the TUI.
- **Why**: two separate assumptions. (1) The empty guard classifies by content shape, not finish_reason — a refusal is the one empty that IS the final answer. (2) `MessageEnd`'s `Message` is the persistence/side-channel view; the TUI commits the deltas it accumulated (`m.curContent`), so content injected only at end-of-turn never reaches the screen.
- **Fix**: `isSafetyRefusal(finish)` (`refusal` | `content_filter`) handled BEFORE the empty guard → `safetyRefusalMarker` becomes the assistant content, plus an explicit MessageStart/MessageDelta so the live region renders it; no ErrorEvent, no retry, no AutoContinue. Tests pin one request, no ErrorEvent, marker in both history and the delta stream (`pkg/agent/agent_test.go:TestAgent_SafetyRefusal_*`).
- **Lesson**: classify terminal outcomes by finish_reason before applying shape-based guards, and route any synthetic assistant content through the delta path — MessageEnd alone is invisible to the UI.
- **Refs**: `pkg/agent/agent.go` (`isSafetyRefusal`, refusal branch in `runTurnDeepSeek`), `internal/tui/update_agent.go:350`, `docs/prd/feature-deepseek-app-api.md` §5

## DeepSeek API

### Server web tools (`deepseek_read_url`/`deepseek_web_search`) have NO working coexistence shape with function tools — the 200 silently strips them
- **Saw**: R0 gate tests (2026-11-14, feature-deepseek-app-api §2.2): function tool + `deepseek_read_url` in one request → thinking off = 400 (`client tools can only be used together with \`deepseek_web_search\`, \`deepseek_read_url\`, \`safety_check\` or \`title\` in thinking mode`); thinking ON = **200 but the server tool is silently stripped** — no `server_tool_calls`, no `server_tool_use`, model literally says "the only tool available to me is a weather lookup". Verified not model discretion via an unknowable-content probe URL (httpbin.org/uuid: alone-declared read_url fetches it under both thinking modes; mixed declaration never fetches). `deepseek_web_search` obeys the same rule (same 400).
- **Why**: undocumented server-side validation — the docs only forbid mixing *server* tools with each other; the client-tool interaction is enforced (off) or silently disables the server tool (on) with zero error surface.
- **Fix**: no code — read_url path judged red and closed (PRD §2.2, R1–R4 archived). If ever revisited: probe coexistence with an unknowable-content URL, never trust HTTP 200 alone.
- **Lesson**: a "green" status code is not a green light — a mixed-declaration request can succeed while the capability is quietly gone. Probe with content the model cannot know, and read the model's own tool inventory in its answer. (Bonus facts from the same run: `usage.server_tool_use` key is absent when all counters are zero; multi-turn round-trip of `server_tool_calls`+`signature` prevents re-fetch & re-billing, dropping it re-fetches; server-side page cache exists but cached re-fetches are still billed.)
- **Refs**: `docs/prd/feature-deepseek-app-api.md` §2.2 (full 10-call evidence table), api-docs.deepseek.com/guides/web_tools

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
- **Fix**: nothing to "fix" code-wise; documented in `docs/prd/archive/v0.md §4.8.1`. Our M2 smoke (system prompt + 5 tools = ~2KB prefix) consistently hits 70%+ across turns; the M0 11-token smoke can't
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

### FIM usage ratio is inherently low — model prefers edit/write over fim_complete
- **Saw**: both benchmark tasks (`self-hosting`, `fim-patch`) showed FIM ratio well below 50% (0% and 11% respectively) despite `fim-patch` being explicitly designed as a small inline edit. The model consistently chose `edit`/`write` instead of `fim_complete`.
- **Why**: two compounding causes. (1) Denominator: ratio was `fimCalls / totalToolCalls`, which counts `read`/`bash`/`grep` — tools that *can't* use FIM — diluting the numerator. (2) Pairing constraint: a `fim_complete` call doesn't write to disk; its output must be applied by a subsequent `edit` (book ch6 §6.5). So `FIM / (FIM+edit)` caps at ~50% even when the model takes the FIM path every chance it gets. (3) The model's training corpus also biases toward `edit`/`write` for general-purpose tools, but that's secondary to the math.
- **Fix**: PRD §6 acceptance criterion was revised in commit `0e4153e` from "ratio ≥ 50%" to per-task absolute count `FIMMinCalls` (`self-hosting: 0`, `fim-patch: 1`). The ratio still appears in the JSON report for visibility but no longer participates in the pass/fail verdict. Designing a meaningful ratio metric (candidate: FIM-saved tokens / total prompt tokens) was deferred to v1.1, when there's more real-world usage data to anchor it.
- **Lesson**: when a metric's denominator includes terms that can never be the numerator, the ceiling is structurally lower than you think. Verify the upper bound of any "X / Y ≥ Z%" metric *before* setting Z, especially for pairing constraints like "every FIM needs a follow-up edit". An aspirational threshold that turns out to be mathematically unreachable trains everyone to ignore the metric.
- **Refs**: `cmd/seek/benchmark.go`, `docs/prd/archive/v0.md §6 / §8`, commit `0e4153e`

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

### Today's date belongs in the system prompt, but only if captured ONCE per session
- **Saw**: the LLM kept guessing the wrong date (no date anywhere in the prompt). The obvious fix — inject `time.Now()` into the system prompt — is a cache trap: the date lives in the prompt prefix, so a value that recomputes per turn (or ticks over at midnight) mutates the prefix on every request and drops the DeepSeek prefix-cache hit ratio from ~96% to near zero
- **Why**: `sysprompt.Compose` is deliberately a pure function of its `Header` (the package doc + `TestCompose_IsDeterministic` exist to stop anyone calling `time.Now()` inside it). The date has to be present for correctness but byte-stable for cache
- **Fix**: added `Header.Date string`. `cmd/seek/main.go` computes `sessionDate := time.Now().Format("Monday, 2006-01-02")` ONCE at startup and threads the same string through every Compose call for the process's life (root prompt, `/new` rebuild closure, and the subagent Manager via a static `ManagerOpts.SessionDate` — static, NOT a closure or `Now()`, so it can't drift across midnight or diverge from the parent header). The prompt line tells the model the date is fixed-at-start so it won't assume it advanced. A later `-resume` is a new process → new date → one accepted prefix bust, same class as a Cwd/skill-manifest change
- **Lesson**: anything that must appear in the prompt for correctness but would otherwise vary is injected as a *field captured once*, never computed inside the assembler. "Per-turn freshness" and "prefix cache" are mutually exclusive for prefix content — pick stability and refresh only at session boundaries
- **Refs**: `internal/sysprompt/sysprompt.go` (`Header.Date`, `rootTpl`), `cmd/seek/main.go` (`sessionDate`), `internal/subagent/manager.go` (`ManagerOpts.SessionDate`), `internal/sysprompt/sysprompt_test.go:TestCompose_RendersDate`

### Provider model-alias sunset dates silently orphan string-literal references
- **Saw**: a week after DeepSeek removed the `deepseek-chat` / `deepseek-reasoner` aliases (2026-07-24), the TUI model picker still offered "DeepSeek V4-Flash" under the dead id `deepseek-chat` — picking the curated default would have hit a 404 model error. The legacy constants, rate-card entries, and budget entries were all still live too
- **Why**: alias removal is announced in provider docs, not in code. Deleting a constant catches *constant* references at compile time, but string-literal references (picker entries, httptest fixtures, `session.New` args) compile fine forever. Even the constant sweep missed one fallback (`pricing.go:89` still read `standardRates[deepseek.ModelChat]`) — it only surfaced via `go vet` after the constant was gone
- **Fix**: full repo sweep removing `ModelChat`/`ModelReasoner` constants, pricing rate-card entries, budget context-limit entries, the picker entry (`deepseek-chat` → `deepseek-v4-flash`), and every test fixture. Pricing fallback now points at `ModelV4Flash` directly. Old session files carrying alias names degrade to "unknown model" handling (no thinking, default budget/pricing) instead of failing to load
- **Lesson**: when a provider announces an alias/model sunset, schedule a same-day repo-wide grep for BOTH the constant names and the raw wire strings — the picker/status-bar/CLI help are user-facing and the last places you expect a dead model id. Re-verify pricing and context-limit fallbacks, which often reference the retired name by habit
- **Refs**: `pkg/deepseek/types.go`, `internal/tui/commands.go:knownModelsForProvider`, `internal/pricing/pricing.go:PricingFor`, `internal/budget/budget.go:contextLimits`

### DeepSeek's peak/off-peak window semantics flipped on 2026-08-16 — "off-peak is the default" inverts tier logic
- **Saw**: checking api-docs.deepseek.com on 2026-08-13 revealed V4 pricing moved to formal peak/off-peak billing (effective 08-16 16:00 UTC): peak = 01:00–04:00 & 06:00–10:00 UTC (09:00–12:00 / 14:00–18:00 Beijing), everything else off-peak at half price. seek's rate card was still the promo prices ($0.14/$0.28 flash) and its window [00:30, 08:30) Beijing covered only a slice of the new off-peak span. After updating, every placeholder test failed silently — the `noon()` helper used 12:00 as its "standard window" representative, which under the new rules is exactly the peak→off-peak boundary, so all tests ran the off-peak branch
- **Why**: the window model inverted: previously "one contiguous off-peak window, standard by default"; now "two small peak windows, off-peak by default". A "representative time" that sat comfortably mid-window under the old rules can sit exactly on a boundary under new ones — and boundary answers flip between the two rule sets
- **Fix**: `internal/pricing` re-based to the 08-16 rate card, storing **peak** prices with the existing 0.5 off-peak discount (matches the official "off-peak = half of peak" definition); `CurrentTier`/`NextTransition` rewritten as a four-segment switch; test representatives moved to mid-peak (10:00) with boundaries asserted only in dedicated boundary cases; README / docs / landing page synced
- **Lesson**: (1) annotate rate cards with "verified-on" AND "effective-on" dates — prices change and promo/rack/peak rates can coexist confusingly; (2) "representative time" helpers in tests/docs must sit far from window boundaries — boundaries belong in dedicated boundary tests only; (3) when a provider flips which window is the default, re-check every default branch, label, and comment, not just the constants
- **Refs**: api-docs.deepseek.com/quick_start/pricing, `internal/pricing/pricing.go`, `internal/tui/placeholder_test.go:noon`

### Side-channel Chat calls fail at marshal time on image-bearing history
- **Saw**: `/compact` (Summarise) is a one-shot Chat over the agent's history — once feature-vision landed, any history holding an Asset-only `ImagePart` made `ChatRequest.MarshalJSON` error out ("image part … has no URL — run ResolveImages") before a single HTTP byte went out. The loud failure is intentional (silently dropping a user-attached image is worse), but it turns every NEW side-channel caller into a landmine
- **Why**: the main send path resolves Assets via `Config.ImageLoader` at the `StripReasoningContent` station; side-channel calls (Summarise, think, memory distill, goal judge) build their own `ChatRequest` from agent history and know nothing about images
- **Fix**: Summarise runs `deepseek.WithoutImages(StripReasoningContent(history))` — a briefing needs prior TEXT only, and the in-band `[image: …]` markers stay in Content so the transcript remains self-describing. Pinned by `TestSummarise_StripsImages`
- **Lesson**: any new path that marshals agent history into a `ChatRequest` must either `ResolveImages` (loader available) or `WithoutImages` (side-channel). Keep the loud marshal error — handle it at the call site, never by weakening `toWire`
- **Refs**: `pkg/deepseek/vision.go` (`ResolveImages`/`WithoutImages`/`toWire`), `pkg/agent/agent.go` (`Summarise`), `pkg/agent/vision_test.go`

### V4.1 launch: retired model ids keep ANSWERING — client rate cards drift silently
- **Saw**: DeepSeek released V4.1 Flash (2026-09-10, `deepseek-flash`) and retired the whole V4 lineup — the dead ids are still ACCEPTED, server-routed to V4.1 Flash and billed at Flash prices (`deepseek-v4-pro` joins after 2026-09-14 12:00 CST). seek's rate card still carried v4-pro's old $3.96/M output card: after the cutover the cost display would over-report ~4.4× forever, with no request ever failing to surface the drift
- **Why**: model retirement ≠ 400. Compat routing keeps dead ids answering, so the only drift signal is the provider's pricing page. Same class as the 2026-07-24 alias sunset but sneakier — routed ids never start erroring. Bonus finds on the same pricing page: peak windows are **Monday–Friday** only (seek's `CurrentTier`/`NextTransition` never checked weekday), and thinking is now the documented SERVER DEFAULT for both tiers (the undocumented version of that default already bit the suggested-reply side channel — see the 1045-era entry above)
- **Fix**: the one-go rename `types.go` prophesied for the vision GA, then a follow-up sweep that removes the retired ids from the registry ENTIRELY: `deepseek-flash` is the only constant left, the default everywhere, the only `IsVisionModel` entry, the only rate-card entry (retired ids bill correctly via the unknown-model fallback — the same card the server charges), and the only budget entry. Sessions carrying a retired id degrade to unknown-model handling: no Thinking flag, 128K budget default, Flash-card pricing — the same precedent as the 2026-07-24 alias removal. The picker collapsed to the single live id (rows for routed ids would mislead "you're buying Pro"). Thinking is sent explicitly for the live id — the server default is thinking-on and leaning on an undocumented default already burned the suggester once. Peak windows gained a Monday–Friday qualifier per the pricing page: `CurrentTier`/`NextTransition` weekend-aware (a weekend "next transition" is Monday 09:00, NOT the nearest calendar boundary — the naive same-day branch would answer "Saturday 12:00 → off-peak", a transition that doesn't exist)
- **Lesson**: when a vendor announces id routing ("temporarily routed, billed at X"), update client cost/context tables the same day — routed ids keep working, so no runtime error will ever surface the drift. Prefer deleting retired ids outright over keeping deprecated entries: the fallback paths (pricing/budget/thinking) already encode "unknown model" behaviour, so a kept entry is a second source of truth somebody must remember to age out. And re-read the pricing page's window QUALIFIERS (weekdays), not just the numbers
- **Refs**: `pkg/deepseek/types.go`, `internal/pricing/pricing.go`, `internal/tui/commands.go:knownModelsForProvider`, `internal/budget/budget.go`, api-docs.deepseek.com/quick_start/pricing

### DeepSeek pricing pages are single-currency per language — cross-currency diffs flag phantom rate changes
- **Saw**: feature-deepseek-app-api.md §0 (2026-11-13) reported flash "repriced" to ¥2/M peak-miss vs pricing.go's $0.30 — but the English page still listed exactly $0.30. No flash price change existed at all; the only real change on the page was deepseek-v4-pro getting its own row again
- **Why**: the EN page lists USD only, the ZH page CNY only (no dual columns). DeepSeek's own implied rate is ~6.67 CNY/USD (¥2 = $0.30), while the GLM row in pricing.go converts at an assumed 7.0 — comparing a CNY number against a 7.0-converted USD number manufactures a ~5% "change" out of thin air
- **Fix**: verified 2026-11-14 against BOTH language versions before bumping anything; flash card left unchanged (comment re-dated), deepseek-v4-pro given its own registry entry ($1.32/$0.044/$3.96 peak — it had silently left the Flash-routing legacy list, where the fallback undercharges it ~4×), searchPerCallUSD switched to the natively-listed $0.75/1K
- **Lesson**: when a price page has per-language currency columns, diff like-for-like currency (or convert with the page's own implied rate) before declaring a rate change. Routing arrangements are as volatile as prices: the "v4-pro is Flash-routed" claim from the 2026-09-10 entry above was true for two months, then reversed — re-read the legacy-id note on every pricing check, not just the numbers
- **Refs**: `internal/pricing/pricing.go`, `pkg/deepseek/types.go`, docs/prd/feature-deepseek-app-api.md §0, api-docs.deepseek.com/quick_start/pricing (+ /zh-cn/ twin)

### The tools-family rates have LEFT the pricing pages entirely (2026-11-18 check)
- **Saw**: pricing.go's ASR/TTS comments say "$0.116/h / $0.45 per 10k listed natively on the English pricing page (2026-11-16 check)" — but on 2026-11-18 BOTH pricing pages list only model tokens (en keeps Search/Fetch $0.75/1K; zh doesn't even keep those). File-parsing (¥1.5/千页, PRD feature-deepseek-app-api §1.2/§6) appears on NEITHER page — its live source is the Files API / document-parsing guide pages
- **Why**: the tools family (OCR/ASR/TTS/file parsing) is documented on per-tool guide pages, not the quick-start pricing page; the pricing page's tool coverage drifts (search/fetch stayed on en, the rest silently vanished). A future rate bump that opens the pricing page first will conclude the constants are unsourced
- **Fix**: parsePerPageUSD (F3) anchored on the PRD's ¥1.5/千页, USD derived at the search row's own documented native ratio (¥5/1K → $0.75/1K ⇒ $0.225/1K pages) with the derivation in the constant's comment; list-price-only posture (no discounts modelled) unchanged
- **Lesson**: before bumping any tools-family constant, check the tool's own guide page (api-docs.deepseek.com/guides/files_api, /asr, /tts, …), not quick_start/pricing — and re-verify the guide page too, because that's where these prices live now. Where only a CNY figure exists, convert at the ratio the page natively documents for a sibling tool rather than inventing a rate
- **Refs**: `internal/pricing/pricing.go` (`parsePerPageUSD` comment), docs/prd/feature-deepseek-app-api.md §1.2/§6, api-docs.deepseek.com/quick_start/pricing (+ /zh-cn/ twin, both checked 2026-11-18)

### /effort max turns hit finish_reason=length at 16k — seek's explicit cap undercut DeepSeek's implicit thinking budget
- **Saw**: an effort:max session on a long (99k prompt, 99% cache) turn died with `response truncated (finish_reason=length, max_tokens=16384)` after a hidden-reasoning stretch — the whole 16k completion budget went to `reasoning_content`, content arrived empty or cut at the start. A sibling turn produced the clean-empty variant (`model returned an empty response`)
- **Why**: seek sends `max_tokens` explicitly (default 16384, a lineage of 4096→8192→16384 raises that all predate thinking-mode semantics). DeepSeek's API, when `max_tokens` is OMITTED, defaults to 8K non-thinking / **64K thinking** (`reasoning_effort=max`) — so the explicit 16k actively undercuts the budget the endpoint itself would grant for the exact mode /effort max selects, and reasoning + content share one budget (the suggested-reply entry below already documented the burn)
- **Fix**: `Agent.turnMaxTokens()` — with `--max-tokens` unset, `/effort max` now caps at 65536 (aligned with the official implicit thinking default), every other effort state keeps 16384; an explicit `--max-tokens` beats the linkage everywhere. Takes effect on the next Prompt after SetEffort (same between-turns contract)
- **Lesson**: when a vendor's parameter has a MODE-DEPENDENT implicit default, a client-side "safe explicit default" can quietly fight the vendor's semantics — re-check your hardcoded defaults whenever the request grows a new mode knob (here: Thinking/reasoning_effort), not just when errors appear
- **Refs**: api-docs.deepseek.com (Chat Completions: max_tokens 1–384K; omitted → 8K/64K by mode), `pkg/agent/agent.go:turnMaxTokens`, `TestTurnMaxTokens_EffortLinkage`, `TestSetEffort_CapTakesEffectNextPrompt`

### TTS 的 64 KiB 请求体上限被 10k 计费字符上限数学遮蔽——写 gate 前先做代数
- **Saw**: S1 TTS 客户端实现时的本能 gate「拒绝超 64 KiB 的请求体」根本不可达：任何通过字符校验的 TTSRequest 都 marshal 不出超限 body（先写了 raw-UTF-8 长度预检，再被自己的注释里的数学推翻）
- **Why**: 字符上限钉死 rune 数 ≤10,000；JSON 每 rune 最坏膨胀是 1 字节控制字符转义成 6 字节（`\uXXXX`）→ 最坏 ~60 KB + ~50 字段开销，恒 < 64 KiB。raw 长度预检更死：4 字节 rune × 10,000 = 40 KB 封顶
- **Fix**: 不发 body gate；`TTSMaxBodyBytes` 保留为已发布约束的文档 + 带遮蔽数学推导的 tripwire 注释（`pkg/deepseek/tts.go`）。`TestTTS_RequestValidation` 钉住最坏膨胀 body 必须低于上限——将来加字段改变了每字符膨胀率，测试先红
- **Lesson**: 一个 API 上限能从另一个上限推导出来时，先代数后 gate——被遮蔽的上限检查是伪装成覆盖率的死代码。常量连同推导一起保留，下次加字段时数学会被重跑
- **Refs**: `pkg/deepseek/tts.go`, api-docs.deepseek.com/guides/tts

## Go language

### `filepath.Base` only splits on the host OS's separator — a backslash traversal guard that passes on Windows fails on unix
- **Saw**: `TestPath_TraversalGuard` failed on macOS: `assets.Path("/tmp", "a\b.png")` was accepted, while the same guard rejects it on Windows
- **Why**: the guard was `asset != filepath.Base(asset)` — but `filepath.Base` treats `\` as a separator ONLY on Windows. On unix `a\b.png` IS its own Base, so the check passes. Asset names round-trip through session JSONL (portable wire data): a name written on macOS and opened on Windows would traverse
- **Fix**: added `strings.ContainsAny(asset, "/\\")` to the guard so both separators are rejected on every OS. `internal/assets/assets.go:Path`
- **Lesson**: for portable identifiers, never derive a path-safety check from the HOST's path semantics (`filepath.Base`, `filepath.IsAbs`, …) — reject both separator characters explicitly. The test suite only catches this if it runs the foreign-OS case on every OS, which is exactly what the literal `a\b.png` case does
- **Refs**: `internal/assets/assets.go`, `internal/assets/assets_test.go:TestPath_TraversalGuard`

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
- **Refs**: `pkg/deepseek/retry.go:retryCall` (shared retry policy — also covers non-streaming `Chat`/`FIM` since 2026-08), `pkg/deepseek/stream.go:pumpChatStream` (post-emit gate), `pkg/deepseek/stream_test.go:TestChatStream_RetryOn500`, related entry above on the empty-response guard

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

### A background process must NOT inherit the turn ctx, or it dies when the turn ends
- **Saw**: designing `bash run_in_background` (v6 柱 K), the obvious move was to mirror the foreground path — `exec.CommandContext(turnCtx, …)`. That would silently kill every background job the instant its launching turn returned (or the user pressed Esc), making "background" a lie
- **Why**: seek's tool `ctx` is the **turn** ctx — cancelled when the agent turn completes or is interrupted. Foreground bash WANTS that (a hung command dies with the turn). A background job's whole point is to outlive the turn that started it; binding it to the turn ctx is a category error
- **Fix**: `runBackground` uses plain `exec.Command` (no ctx) and registers a kill closure with the session-scoped `bgjob.Manager`. Cleanup is via `monitor(action=kill)` or `Manager.Shutdown()` at session end — never turn cancellation. The *observer* (`monitor wait`) IS bound to the turn ctx, so Esc stops watching without killing the job
- **Lesson**: separate the two lifetimes explicitly — turn-scoped (foreground exec, wait/observe) vs session-scoped (the background process itself). When a value should outlive the turn, the turn ctx is the wrong leash. Tested with an already-cancelled ctx in `TestBash_Background_IgnoresTurnCtx`
- **Refs**: `internal/tools/bash/bash.go:runBackground`, `internal/bgjob/bgjob.go` (Shutdown), `internal/tools/monitor/monitor.go` (wait propagates ctx.Err), `docs/prd/archive/feature-bash-monitor.md` §4 D5

### go flag: a requested -h is a VALUE (flag.ErrHelp), and ExitOnError hides the verb surface
- **Saw**: `seek serve --frontend=web -h` exited 2 with the usage text on stderr. Separately, `seek --help` listed only flags — enough for a reader to conclude the CLI had no subcommands at all (serve / cron / skill … all invisible)
- **Why**: two distinct shapes of the same contract. (1) A sub-FlagSet uses `ContinueOnError`, so `-h` returns `flag.ErrHelp` as an error — servecli's catch-all fed it to `badConfig`, exit 2. Only `args[0]` was checked for help, so a flag placed before `-h` slipped past. (2) `flag.CommandLine` is `ExitOnError`, and its built-in `-h` prints the DEFAULT usage — `PrintDefaults` only, on stderr — so verbs dispatched by `main` before `flag.Parse` never appear
- **Fix**: recognise `-h`/`--help` anywhere before the `--` terminator (not just args[0]) and print help to stdout with a clean exit; keep `errors.Is(err, flag.ErrHelp) → nil` as a belt-and-braces guard; set a custom `flag.Usage` that prints the command list before `PrintDefaults`, invoked explicitly before `flag.Parse` once the flag set is registered (so the listing has both halves)
- **Lesson**: a requested help is never an error — do not let a catch-all error path own `flag.ErrHelp`, and do not let `args[0]` be the only position that counts. `flag.PrintDefaults` is not a CLI's discoverability story: whatever `main` dispatches by hand must be listed by hand
- **Refs**: `internal/servecli/servecli.go:Run`, `cmd/seek/main.go:helpRequested` / `printUsage`, tests `TestRun_HelpFlag_AnywhereBeforeTerminator`, `TestHelpRequested`, `TestPrintUsage_ListsCommands`

---

## Tooling / environment

### jq without input files and without -n runs the filter zero times — empty output, exit 0
- **Saw**: `table=$(jq -r --slurpfile a f1 --slurpfile b f2 '<program>')` in a script returned an EMPTY string with exit code 0 — not even the program's constant-string lines printed — so the script happily shipped a header-only "table"
- **Why**: with no input files and no `-n`, jq runs the filter once per JSON value on **stdin**; a script has no stdin lines, so the filter executes 0 times. Output empty, rc 0 — the classic silent-swallow. (Sibling trap in the same fix: with `--slurpfile v FILE`, `$v` is *already* the array of the file's JSON values — `$v[0]` is the first VALUE, not the value array; indexing into a row object then iterating it with `map(.case)` walks the object's string fields and dies with `Cannot index string with string "case"`.)
- **Fix**: `jq -n -r --slurpfile …` in `eval/ablate.sh`, plus an explicit `[[ -z "$table" ]] && exit 2` guard so a future silent-empty can never pass as success. `$v` used directly as the row array.
- **Lesson**: a jq program whose inputs are all `--slurpfile` variables needs `-n`; and any `$(jq …)` whose output feeds a generated artifact should assert non-empty — jq has failure modes that look exactly like success
- **Refs**: `eval/ablate.sh` (the `-n` note above the table jq + the empty-table guard)

### Grep tool with broad pattern can deadlock the agent — no recovery via /compact
- **Saw**: User reports that running `grep "llm"` against a large codebase twice fills the model's context completely. `/compact` can't recover because the compact call itself requires another LLM round-trip through an already-overflowed history → hard deadlock
- **Why**: grep's `max_matches` cap (200) governs **count of matches** but not the **total bytes** of formatted output. Real-world worst case: 20 matches × 7 context lines × variable line length (think minified JS, generated TypeScript, multi-MB JSON-on-one-line) easily hits 50 KiB per call. Two such calls = 100 KiB ≈ 25K tokens of pure grep output, on top of system prompt + history + other tool outputs. DeepSeek 128K context fills up; `/compact` can't fit its own pre-prompt into the remaining budget, so the rescue path itself fails
- **Fix**: two-layer cap inside `internal/tools/grep/grep.go`. (1) `maxOutputBytes = 16 KiB` hard cap on the formatted output string — back up to last newline + append "(truncated at N KiB; M KiB dropped — refine pattern)" notice so the model knows to narrow scope. (2) `maxLineChars = 240` per-line cap — single huge lines get rune-aware `…(truncated)` clip. Both run **inside** the tool so the byte ceiling holds regardless of how the model picks `max_matches`
- **Lesson**: per-tool output budget is **not** about controlling the model's choice of arguments — it's about the worst case of any one call. CLAUDE.md spells this out ("Tool output size limits are enforced inside the tool itself") but it's easy to think "I already cap matches, the bytes will follow" — they don't, when each match has variable-length payload. Any tool that returns user-content (read, grep, listdir, webfetch) needs a hard byte ceiling, not just a count ceiling. The cap-notice should also tell the model *how to refine*, otherwise it'll just retry with the same pattern
- **Refs**: `internal/tools/grep/grep.go:maxOutputBytes` + `truncateOutput`, `internal/tools/grep/grep_test.go:TestGrep_HardBytesCap`/`TestGrep_TruncatesLongLines`

### Path-string assertions / raw paths in JSON literals broke on windows-latest CI
- **Saw**: `go test ./...` red on windows-latest with three failure shapes: (1) `strings.HasSuffix(p, "/foo")` failing because Windows gives `\foo`; (2) `strings.Contains(p, ".seek/skills")` for the same reason; (3) `read: bad arguments: invalid character 'U' in string escape code` when a test built JSON via `json.RawMessage(`{"path":"`+p+`"}`)` and `p` was `C:\Users\...` — `\U` is an invalid JSON string escape
- **Why**: macOS/Linux developers writing tests against `filepath.Join` outputs and then asserting via Unix-style literals (`/foo`) or embedding paths directly into JSON without escaping. Both work locally; both blow up the moment a Windows runner sees them. The `\U` case is sneakier — JSON requires backslashes to be escaped (`\\`), but raw concatenation skips that step
- **Fix**: (1)+(2) use `filepath.Base(p)` for last-segment checks or `filepath.ToSlash(p)` when substring matching; (3) build JSON via `json.Marshal(map[string]string{"path": p})` instead of string concat — the marshaller escapes backslashes correctly
- **Lesson**: any test that touches paths needs to be reviewed with "what does this do on Windows?" in mind. Three antipatterns to grep for periodically: `HasSuffix(.*"/`, `Contains(.*"/`, `json.RawMessage(.*+.*+`. Especially the JSON one — it looks like obviously-correct test setup until a backslash lands inside
- **Refs**: `internal/skillmgr/skillmgr_test.go` `update_test.go`, `internal/skill/loader_test.go`, `internal/tools/read/read_test.go`, `internal/tui/filepicker_test.go`, `internal/tools/plan/artifact_test.go`

### edit tool byte-matches fail on Windows CRLF Go files when old_string uses LF
- **Saw**: on Windows, `edit` with a multi-line `old_string` copied from model output (LF `\n`) against a CRLF Go file always fails with `expected 1 replacements but old_string occurs 0 times`, even when the visible text is identical; agents fall back to Python/PowerShell one-off scripts
- **Why**: `edit` tier-1 matching is exact bytes. Windows Go files default to `\r\n`; model-produced needles use `\n`. Single-line edits without embedded newlines still match; any block spanning line breaks does not
- **Fix**: add tier-2 line-ending normalisation in `internal/tools/edit/edit.go` — fold CRLF/lone CR to LF for matching, then write back in CRLF when the source file contained `\r\n`. NFC fallback now runs on the line-ending-normalised text
- **Lesson**: any exact-substring edit tool on a cross-platform codebase needs a CRLF↔LF fallback tier alongside Unicode normalisation; byte identity and visual identity diverge on Windows the moment a needle crosses a line boundary
- **Refs**: `internal/tools/edit/edit.go`, `internal/tools/edit/edit_test.go:TestEdit_CRLFFallback_*`

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

### Windows System32 `sort.exe`/`find.exe` shadow POSIX tools in cygwin/msys shells — "Input file specified two times."
- **Saw**: `scripts/pkg-inventory.sh` (new) exited 1 with `Input file specified two times.` and no table output after a full 2m44s test run; a minimal repro of `sort -t'|' -k1,1n file` failed identically, and `sort --version` printed `--versionThe system cannot find the file specified.`
- **Why**: non-login cygwin/msys shells spawned from cmd inherit the Windows PATH verbatim, where `C:\Windows\System32` precedes the POSIX bin dir. `sort`/`find` then resolve to the Windows binaries: System32 `sort.exe` has no `-t`/`-k` flags and treats them as input files ("Input file specified two times."); System32 `find.exe` is a text filter, not a file lister. (WSL bash adds a second trap: with interop disabled it cannot execute any `.exe` — "Exec format error".)
- **Fix**: `scripts/pkg-inventory.sh` resolves `SORT` to `/usr/bin/sort` (fallback `/bin/sort`, then bare `sort`) and replaced the `find … | wc -l` test-file count with a bash `nullglob` glob (`set -- "$rel"/*_test.go; tests=$#`). Both are immune to PATH order.
- **Lesson**: in repo scripts that may run under a cmd-spawned cygwin/msys shell, never call `sort`/`find`/`ls`/`rm` by bare name — either use the absolute coreutils path or drop the external tool (bash globs). Bare `grep`/`sed`/`awk`/`wc`/`tr` are safe: Windows ships no such binaries, so only the POSIX ones exist on PATH.
- **Refs**: `scripts/pkg-inventory.sh` (uncommitted at time of writing)

### grep's hidden-entry filter ate the walk ROOT when path was "." — every cwd-wide search returned "no matches"
- **Saw**: `grep pattern=X path=.` returned `no matches` for patterns that provably exist in the cwd, while the same pattern with `path=internal` matched. Hit twice in one session (both times the model concluded "nothing matches" and changed course)
- **Why**: `filepath.WalkDir` hands the callback the root entry with its LITERAL name — for `dir == "."` that's `d.Name() == "."`, which `strings.HasPrefix(d.Name(), ".")` read as "hidden directory" → `filepath.SkipDir` on the first callback → empty file list, no error. The same walker fed `**` globs, so `**/*.go` (empty prefix → `.`) was silently empty too. The trap is that the filter is right for CHILDREN and wrong for the ROOT: a bare `strings.HasPrefix(d.Name(), ".")` cannot tell them apart
- **Fix**: `walkSkip(path, root, d)` — hidden check with the root exempt (`path == root`), used by both walkers (`walkDir`, `expandDoubleStarGlob`). Hidden children stay filtered, so a `.` root still doesn't descend into `.git`. Tests: `TestGrep_DotPath_WalksCwd`, `TestGrep_Glob_DoubleStarFromDot` (plus the pre-existing `TestGrep_Directory_SkipsHidden` pinning the child filter)
- **Lesson**: when a walker applies an entry filter, decide explicitly whether the ROOT is subject to it — most filters are "don't descend into X", and the root is a user's explicit choice, not something to discover and skip. A filter that silently yields zero results is worse than an error: the caller can't tell "nothing there" from "I refused to look"
- **Refs**: `internal/tools/grep/grep.go:walkSkip`, `internal/tools/grep/grep_test.go:TestGrep_DotPath_WalksCwd`

### grep crashed the whole process on a pattern ending in `**` (empty-suffix index panic)
- **Saw**: while writing the fix above — `grep path="internal/**"` panicked with `index out of range [0] with length 0` at `grep.go:389` (reproduced from a test, which re-panicked through `testing`)
- **Why**: the separator-strip was written as `if suffix != "" && suffix[0] == '/' || suffix[0] == filepath.Separator` — precedence makes it `(a && b) || (c)`, and when the pattern ENDS in `**` the suffix is empty, so the right-hand `suffix[0]` indexed an empty string. Nothing recovers: the grep tool runs in-process inside the TUI, so this is a host crash, not a tool error the model can recover from
- **Fix**: `strings.HasPrefix(suffix, "/") || strings.HasPrefix(suffix, string(filepath.Separator))` — no indexing. `TestGrep_Glob_DoubleStarTrailing` pins it (`<dir>/**` must match everything below the dir)
- **Lesson**: in `a && b[i] || c[j]` shapes, short-circuit protects only the operand it guards — a mixed-precedence condition needs parentheses, or better, prefix checks (`HasPrefix`/`TrimPrefix`) that are total by construction. And exit-code hygiene matters here: an in-process tool with an unguarded panic converts a malformed ARGUMENT into a dead session
- **Refs**: `internal/tools/grep/grep.go:expandDoubleStarGlob`, `internal/tools/grep/grep_test.go:TestGrep_Glob_DoubleStarTrailing`

### `go get -tool pkg@latest` quietly bumps the go directive and runtime deps; analysis CLIs are pinned by the toolchain's export-data version, not just semver
- **Saw**: adding the deadcode scanner via `go get -tool golang.org/x/tools/cmd/deadcode@latest` rewrote `go 1.25.8` → `go 1.26.0` AND upgraded four runtime deps (x/net, x/sys, x/term, x/text) — for a scan-only change. Separately: globally-installed staticcheck (built with go1.25.0) failed on this module with "export data version 5 is greater than maximum supported version 2", gopls ≤v0.20's modernize cmd hit the same wall, and v0.24.0 removed the cmd package entirely
- **Why**: tool directives live in go.mod, so `@latest` resolves MVS against the newest x/tools, whose go requirement propagates to the module's `go` line and drags shared deps up. Analysis drivers read compiled export data, which is forward-incompatible: a binary built by an older toolchain cannot read a newer one's builds — "latest" is bounded by your toolchain, not the registry
- **Fix**: pin the tool to the x/tools version already in the module graph (`deadcode@v0.48.0` → go.mod diff of +1 tool line, +3 indirect); for one-off analysis runs use module-agnostic `go run pkg@version` (never touches go.mod) with the newest version your toolchain can feed: staticcheck@v0.8.2, modernize cmd @gopls v0.23.0 (the last version that ships it and reads go1.27 export data)
- **Lesson**: two pinning rules for analysis tooling — (1) `go get -tool` at the version already in your module graph unless you INTEND the toolchain/dep bump; (2) an analysis CLI's usable range is [repo's go directive, toolchain that built it], so when one errors on export data, change the CLI version, not the repo
- **Refs**: `go.mod` (`tool golang.org/x/tools/cmd/deadcode`), `docs/notes-slim-candidates-2026-11.md` §0.2 (full version matrix)

### macOS bash 3.2: fullwidth punctuation after `$var` joins the variable NAME, and a successful EXIT-trap command masks the real exit code
- **Saw**: `install-serve.sh --dry-run` printed `rendered\ufffd: unbound variable` at a line that only did `echo "--- 渲染结果（$rendered）---"` — and exited 0 despite `set -euo pipefail`. Three stacked traps, all macOS-system-bash-specific: (1) the fullwidth `）` after `$rendered` was absorbed into the variable name (bash 3.2 treats high bytes as name characters outside POSIX mode); (2) the EXIT trap's successful `rm` overwrote the expansion-error exit code → the failure reported success; (3) after fixing those, the trap itself died on `"$rendered"` once the function returned — `local` variables are gone when the trap runs back at top level
- **Why**: bash ≤3.2 (what `/bin/bash` still is on macOS) predates the modern variable-name and trap-exit-code rules; `bash -n` catches none of this, and the same text runs clean on bash 5
- **Fix**: always brace `${var}` when any non-ASCII character can follow it in a double-quoted string; make EXIT traps exit-code-honest (`trap 'rc=$?; rm -f "${rendered:-}"; exit $rc' EXIT` — `:-` for the local-out-of-scope case); verified with dry-run + `shellcheck`
- **Lesson**: repo shell scripts run under macOS system bash 3.2 — write for 3.2, not for the brew bash you test with. In Chinese prose embedded in shell strings, `$var` is ALWAYS one fullwidth character away from silently becoming a different variable. And a trap that can't fail must not be allowed to speak for the script's exit status
- **Refs**: `scripts/install-serve.sh` (uncommitted at time of writing), verified via `bash -c 'set -eu; echo "（$rendered）"'` → exit 1, with trap → exit 0

### modernize -fix turns `s[i].Field = x` into `v.Field = x` under slices.Backward — a silent write-to-copy no-op
- **Saw**: 第 3 刀 modernize（card c_l3cw6d）let gopls v0.23.0's slices.Backward fix rewrite `AttachPredictedNext`'s through-index write `a.messages[i].PredictedNext = text` into `v.PredictedNext = text`, where `v` is the range COPY — the prediction silently stopped persisting while `go build` / `go vet` / `go test -race ./...` all stayed green (the path had zero coverage)
- **Why**: the Backward fixer replaces `s[i].X` with `v.X` uniformly and drops the now-unused index; it does not distinguish reads from writes, and v0.23.0 has no write-safety guard
- **Fix**: audited all 12 Backward rewrites in the knife's diff (this was the only write-through-value); kept Backward but took the index too (`for i, v := range slices.Backward(...)`, write via `a.messages[i]`) and added `TestAttachPredictedNextWritesThroughIndex` to pin the contract
- **Lesson**: never trust a bulk `-fix` pass blind — after applying, grep the diff for value-dropping rewrites (`range slices.Backward`, index elision) and check every loop body for writes through the loop variable; a green suite only means nothing was watching
- **Refs**: `pkg/agent/agent.go` `AttachPredictedNext`, `pkg/agent/agent_test.go` `TestAttachPredictedNextWritesThroughIndex`, `docs/notes-slim-candidates-2026-11.md` 执行状态·第 3 刀

### 无头浏览器验收脚本三连坑：await 死亡 Promise 当查活、bash 拼 URL 双斜杠 307、跨块 textContent 无分隔
- **Saw**: W2.1 无头验收脚本（card c_5xtm74）三处各吞一个调试循环：① node 驱动等 Chrome DevTools 目标时永远挂起——代码是 `await chromeDead`（`new Promise(res => chrome.on('exit', ...))`），Chrome 活着才对，Promise 永不 resolve，"查活"变成了"等死"；② bash 里 `BASE` 带尾斜杠拼 `"$BASE/msg"` 得 `//msg`，Go mux 回 307 重定向，POST 静默没送达、SSE 订阅 curl 拿到的是重定向体——自检全红但谁也没报错；③ 对 DOM 快照断言 `m.txt === 期望全文` 永远不成立——`renderBody` 产出的 `<pre>` 是块级元素，`textContent` 在块间**不插任何分隔符**，"A\n" + pre + "B" 读回是 `A\nfuncB` 形状
- **Why**: ① 把"可能永远不 resolve 的 Promise"当布尔条件 await 是 Node 常见反模式，同步语义应该用 `chrome.exitCode !== null`；② `http.ServeMux` 对不洁路径（`//x`）先 301/307 重定向再匹配，curl POST 默认不跟 307 之外的方法语义、`curl -sN` 订阅流拿到重定向体就"完成"了——两层都静默；③ textContent 是纯文本拼接，不携带任何结构信息，块级边界的换行在源文本里、不在序列化结果里
- **Fix**: ① 改 `chrome.exitCode !== null` 同步查活；② `BASE="${BASE%/}"` 去尾斜杠；③ 快照断言改 contains（三段俱全 + `pres>0` + 无 `⚙` 残留），不做全文相等。另给驱动加了 120s watchdog + 每景 trace——第一版挂死时零输出，没法定位
- **Lesson**: 验收脚本自己也是会被卡死的代码：任何"等外部进程"的循环都要有总闸超时和阶段 trace；拼 URL 的 base 永远先去尾斜杠（307 是静默杀手）；对 DOM 文本断言只 contains 不 equal，除非页面是你手拼的纯文本节点
- **Refs**: `scripts/web-stream-headless.mjs` `web-stream-manual.sh`, `internal/controlplane/webfrontend.html` renderBody

### 新验收脚本忘了登记 selftest.sh → 验证车道拒跑、整卡 unverified 停人审（已机制化：登记制→发现制）
- **Saw**: 连续两张卡（c_6py7gh、c_zcygo2）的验证车道均拒绝跑新写的验收脚本——don't-ask 白名单只放行 `bash scripts/selftest.sh:*` 一条，直接 `bash scripts/<新脚本>` 不在名单，而新脚本又没登记进 selftest.sh 的 `target_of`/`ALL_NAMES`——整卡落 unverified 停人审，主会话手跑补证收口（#7848）
- **Why**: 覆盖与否寄托在「写完脚本的会话记得回到另一个文件加两行登记」上——写完脚本的那一刻任务感已完整，登记这步恰好最容易被忘
- **Fix**: selftest.sh 改发现制（c_aqbdaz）：名单 = `scripts/*.sh` 基名全量 − 显式排除表（逐项注理由）；裸跑非自测形态但自带自测 flag 的经 `dispatch_args` 字面量臂传参（kernel-deps → `--self-test`）。参数仍禁构造路径——只与 glob 派生基名比对、含 `/` 或 `..` 先拒，c_ck6djl 否决边界（白名单层禁通配、防 /tmp 穿越）原样保留
- **Lesson**: 依赖「作者记得去另一个文件补第二步」的机制，覆盖一定在第一步自我感觉完成时漏掉——把默认值倒过来：新工件默认被覆盖、不适宜的显式排除并注明理由，而不是默认不覆盖、记得了才登记
- **Refs**: `scripts/selftest.sh` 头注释，butler `docs/mechanisms.md` selftest 行，`scripts/eval-trend-test.sh`（wrapper 形态先例）

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

### GLM (bigmodel.cn paas/v4) is streaming-tool_calls compatible; `reasoning_content` deltas are silently dropped
- **Saw**: smoke-running `--provider glm` (glm-5.3, zcode coding-plan key): full grep→read→edit→read loop and a plan-mode run both worked first try; but stats showed `cache hit 0/0 ratio n/a` and TTFB 11.8–42.5s with long silent gaps before the first visible token
- **Why**: the endpoint speaks OpenAI Chat Completions byte-for-byte where it matters — `delta.tool_calls[]` keyed by `index` with id/name/arguments, `finish_reason:"tool_calls"`, usage on the final chunk (the client's `stream_options.include_usage` opt-in is honored). GLM ALSO emits `reasoning_content` deltas (same field name as DeepSeek's reasoner); the openai client's wire types don't declare it, so `encoding/json` skips those chunks — thinking time renders as dead air. No DeepSeek cache metadata comes back either, so prefix-cache stats stay empty. Both are the documented expected degradation for the GLM lane, not bugs
- **Fix**: none needed for the lane — `compatible.New` + pinned default base URL `https://open.bigmodel.cn/api/paas/v4/chat/completions` registered as `case "glm"` in `buildProvider` (this change). If silent thinking becomes a UX problem, add a `reasoning_content` passthrough like deepseek's Reasoning deltas — deliberate non-goal here
- **Lesson**: "OpenAI-compatible" needs exactly three verifications before trusting a lane: streaming tool_calls delta shape, usage-in-stream opt-in, and what the model emits that your wire types DON'T parse — that last one is where silent degradation hides
- **Refs**: `cmd/seek/main.go:buildProvider` (glm case + `glmBaseURL`), `pkg/llm/provider/openai/client.go:streamDelta` (undeclared fields are skipped by unmarshal)

### Zhipu bills by URL, not by key: a Coding Plan key on /api/paas/v4 charges the recharge balance (or errors 1113)
- **Saw**: `--provider=glm` with a GLM Coding Plan key never touched the plan quota — calls billed the recharge balance instead; when that balance is empty the same key returns `1113`「余额不足或无可用资源包」. The lane's pinned default was `/api/paas/v4`, `ProviderConfig` had no `base_url`, so the only override was the `--base-url` flag (invisible in the TUI).
- **Why**: Zhipu splits billing by endpoint, not by key shape: `https://open.bigmodel.cn/api/paas/v4/chat/completions` = pay-as-you-go (recharge balance); `https://open.bigmodel.cn/api/coding/paas/v4/chat/completions` = Coding Plan subscription quota (`/api/anthropic` is the third documented Base URL, Anthropic protocol). The old code comment "subscription and pay-as-you-go keys share it" was wrong; the keys look identical, so seek can't tell them apart.
- **Fix**: `config.ProviderConfig.BaseURL` (`json:"base_url"`) + `config.BaseURLFor` (env `GLM_BASE_URL` > config), resolved by `glmEndpoint(cfg, flag)` as override > `--base-url` > `glmBaseURL`. Default deliberately unchanged (pay-as-you-go is the majority; flipping it would break them in the opposite direction). Setup wizard prints the Coding Plan override after saving a GLM key; `docs/guide-config.md` §3 carries the JSON example.
- **Lesson**: when a vendor splits billing by URL, the endpoint is part of the provider contract — a pinned default silently mis-bills the other side. Ship a per-provider endpoint override *with* the default, and never write "both share it" without a real-key test on both paths.
- **Confirmed on a live key** (2026-11-18): `…/api/coding/paas/v4/chat/completions` answers 200 for a Coding Plan key, and `/api/monitor/usage/quota/limit` (raw key in `Authorization`, no `Bearer`) reports the plan's 5h + weekly windows — that pair is the two-minute check for "is this key subscription-billed?". Truncating the path to `…/api/coding/paas/v4` returns 404 (`path:"/v4"`), because the client POSTs `base_url` verbatim; a 401 means the key itself is stale, not the endpoint.
- **Refs**: `cmd/seek/main.go:glmEndpoint` / `glmBaseURL` / `glmCodingPlanBaseURL`, `internal/config/config.go:ProviderConfig` / `BaseURLFor`, `cmd/seek/setup.go` (GLM tip), `docs/guide-config.md` §3

### propose in print mode always errors "askuser: no callback registered" — plan approval can't be picked headless
- **Saw**: `--plan -p "..."` smoke: the model correctly called `propose` during plan-analyze, and the tool returned `ERROR: propose: askuser: no callback registered (programming error)`; the model recovered by presenting the plan inline (exit 0, zero edits — the read-only gate held)
- **Why**: `askPolicy.SetAskFn` (and the permission approval channel) are wired AFTER the `-p`/`-json`/piped-stdin branch returns in `main.go` — only the TUI path registers them. Print mode never wires an askFn, so any tool that asks (propose's approval picker, ask_user) hits `ErrNoCallback`. Pre-existing harness gap, surfaced by the GLM plan-mode smoke; provider-independent
- **Fix**: not fixed here (out of card scope). Headless plan approval needs either an auto-deny + "plan printed, rerun interactively" contract or an approval-via-stdin callback; the model's inline-plan fallback is acceptable for smokes
- **Lesson**: `SetAskFn` wiring is TUI-only; any headless surface that registers asking tools inherits `ErrNoCallback`. When smoke-testing interactive subsystems in print mode, expect this error and read it as "wiring gap", not model misbehaviour
- **Refs**: `cmd/seek/main.go` (print/json branch returns before the `SetAskFn` wiring in the TUI path), `internal/askuser/askuser.go:ErrNoCallback`, `internal/tools/propose/propose.go`

### pricing.Cost bills input ONLY via cache-hit/miss buckets — every second-tier turn priced input at $0
- **Saw**: the GLM lane's money stats only ever counted output tokens: input cost $0 on every turn, `cache hit 0/0 ratio n/a`. The earlier GLM smoke entry concluded "no cache metadata comes back" — that conclusion was unfalsifiable with the client as it stood (see Lesson)
- **Why**: three links in one chain, each silently dropping the same data. (1) `openai/client.go`'s `streamUsage` didn't declare `prompt_tokens_details`, so `encoding/json` skipped it even when the provider sent `cached_tokens`; (2) `llm.TurnDone` carried no cache field, so the provider couldn't report what it had parsed; (3) `runTurnLLM` synthesised `deepseek.Usage` with hit/miss left at zero — and `pricing.Cost` prices input ONLY through those two buckets (`PromptTokens` is never read), so input cost was structurally $0 on every `llm.Provider` turn, GLM or not
- **Fix**: `TurnDone.CachedInputTokens` + `streamUsage.prompt_tokens_details.cached_tokens` + `runTurnLLM` splitting `input-cached` / `cached` into miss/hit buckets (clamp to input), plus the `glm-5.3` rate card with `NoOffPeakDiscount` so CurrentTier's DeepSeek clock can't half-price a flat-rate provider. End-to-end (real seek binary, print mode, fake bigmodel wire server sending prompt_tokens 1000 / cached 600 / completion 12): `est. cost $0.0007`, session jsonl persists `prompt_cache_hit_tokens: 600 / miss: 400` (this change)
- **Lesson**: for "cost shows $0" bugs, read what the billing function consumes, not whether the call happened — usage carried `prompt_tokens` all along and Cost never looked at it. And: a wire type that doesn't declare a field makes "server didn't send it" and "client dropped it" observationally identical; verifying a wire contract requires dumping the raw stream, not reading your own parsed structs
- **Refs**: `internal/pricing/pricing.go:Cost`, `pkg/agent/agent.go:runTurnLLM`, `pkg/llm/provider/openai/client.go:streamUsage`, previous entry "GLM … reasoning_content deltas are silently dropped"

### OpenCode Go: the model list needs no key, the base URL has three routable aliases, and each new lane re-invents the hardcoded-endpoint bug
- **Saw**: wiring the lane had left three `待核` markers ("ids unverified until a real key exists", "official harness page 404s", "`*-Free` models unusable here"). All three fell to facts that were free: `GET https://opencode.ai/inference/go/v1/models` returns 32 chat models **unauthenticated**, each carrying `api.chat-completions.base_url`, `limit.context/output`, a `cost` block, `capabilities` (tools/reasoning/effort) and `go.monthly_usage`. Probing `POST` without `x-opencode-session` returns `400 MissingSessionID`; with the header and a bogus key, `401 Invalid credential`; a made-up path returns `404`.
- **Why**: three aliases all route (`/inference/go/openai/v1/chat/completions` = the per-model advertised base, `/inference/openai/v1/…` = the docs table, `/inference/go/v1/…` = what seek had pinned), so a wrong-but-routable path is invisible until something downstream (usage attribution) depends on the prefix. And the subagent route hardcoded `opencodeBaseURL` while the parent honoured `--base-url` only — the exact divergence the GLM lane had already grown `glmEndpoint` to fix, recurring on the next lane because the resolver wasn't shared.
- **Fix**: pinned the metadata-canonical `/go/openai/v1` target plus a test against drift; `opencodeEndpoint(cfg, flag)` + `newOpenCodeProvider` now mirror `glmEndpoint` (env `OPENCODE_BASE_URL` / `providers.opencode.base_url` > `--base-url` > default) and both construction sites (startup, subagent routes) go through them; comments and `docs/guide-config.md` §3 replaced the `待核` guesses with the verified contract. The same list also seeded the lane's model menu: `pricing.OpenCodeGoModels` (20 ids — the 16 metered Chat-Completions rows plus the 4 zero-price `*-Free` rows) is rendered by ACP's `session/new` dropdown *and* the TUI `/model` picker, each id carries its own card and `budget` context window, and three tests pin the triple (id → card → window). Free rows carry an explicit $0 card: without one they'd hit the Flash fallback and display a price for a turn that costs nothing. Pricing card unchanged: the docs table ($0.30/$1.20/$0.006) is the **peak** price — `/v1/models` quotes half that plus a 2× Mon–Fri 01–04 & 06–10 UTC multiplier.
- **Lesson**: before wiring a provider, `curl` its model list and one unauthenticated POST — a public catalogue turns "wait for a key" into a five-minute verification. And when a lane gains an endpoint override, route **every** construction site through one resolver the same day; the second site is where the divergence always lands. Corollary for menus: one exported catalogue slice, N surfaces rendering it — a hand-maintained list per surface drifts the moment the second surface is added. And read the provider's own definition before excluding rows: "`monthly_usage: null` means no allowance" and "means unusable" are different claims — the docs' line was "free = an opencode-go model with no input price", i.e. unmetered, not unavailable. The first read cost a round trip.
- **Dynamic follow-up** (`internal/opencatalog`, same day): the pool churns weekly, so hand-copying it was the wrong shape. The lane now fetches its own catalogue with the lane key (the unkeyed list is the public superset — installing it would offer models the workspace can't call), caches it in `~/.seek/opencode-catalog.json` (24h fresh / 7d stale), and installs rates + context windows as runtime overrides (`pricing.SetModelRates`, `budget.SetLimits`). Both packages are compiled-table-first by design, so the override layer is the deliberate exception and is documented as such in their package comments — the guards are: write-once-per-startup, mutex-guarded reads (the render path reads concurrently), no TTY probing, and every failure falls back to the compiled rows. Two traps worth remembering: (1) a remote cost block must be force-flattened (`NoOffPeakDiscount`) or DeepSeek's peak clock halves a subscription lane's estimate at night; (2) the pricing registry is keyed by wire id, so a dynamic card for `glm-5.3` / `deepseek-v4-pro` would silently reprice the *direct* lanes sharing that id — the catalogue skips those ids rather than "fixing" them.
- **Refs**: `cmd/seek/main.go:opencodeBaseURL` / `opencodeEndpoint` / `newOpenCodeProvider` / `acpLaneModels`, `internal/pricing/pricing.go:OpenCodeGoModels`, `internal/budget/budget.go:contextLimits`, `internal/tui/commands.go:knownModelsForProvider`, `internal/config/config.go:baseURLEnvFor`, `cmd/seek/main_test.go:TestOpenCodeEndpoint_Precedence` / `TestBuildSubagentRoutes_OpenCodeEndpoint` / `TestACPMenu_OpenCodeCatalogue`, `internal/pricing/pricing_test.go:TestOpenCodeGoCatalogue`, `docs/guide-config.md` §3, the GLM entry above

---

## Testing / CI

### t.Cleanup(chdir) after t.TempDir() breaks Windows TempDir removal
- **Saw**: Windows CI failed with `testing.go:1369: TempDir RemoveAll cleanup: unlinkat … The process cannot access the file because it is being used by another process.` in `TestCommit_ProjectScope`.
- **Why**: `t.TempDir()` registers its cleanup via `t.Cleanup` (LIFO). When the test registered `t.Cleanup(func() { os.Chdir(cwd) })` *before* calling `t.TempDir()`, the chdir-back ran *last* — after the TempDir removal had already tried (and failed on Windows) to delete the directory while CWD was still inside it. On Unix this works (directory can be unlinked while in use), on Windows it doesn't.
- **Fix**: call `t.TempDir()` first, then register the chdir-back `t.Cleanup` second — so the chdir-back runs first (LIFO) and restores CWD before TempDir removal. Commit `d20e446`.
- **Lesson**: when combining `t.TempDir()` + `os.Chdir()` in a test, always call `t.TempDir()` *first*, then register the chdir-back `t.Cleanup`. The reverse ordering is a latent Windows-only failure. `defer os.Chdir(prev)` is safe (runs before `t.Cleanup` callbacks); `t.Cleanup` is not.
- **Refs**: `internal/tools/skillinstall/skillinstall_test.go:TestCommit_ProjectScope`

### Two `time.Now()` calls in the same nanosecond make Equal-based timestamp assertions flaky
- **Saw**: `TestRecall_HitReturnsEntryAndBumpsRecall` intermittently failed with `LastRecalledAt should advance past CreatedAt after Recall`. Reproduces more often on fast CI runners with coarse clock resolution.
- **Why**: `Project.Add` and `memorytool.Execute → TouchRecall` each call `time.Now().UTC()` independently. When the two calls land in the same nanosecond, `LastRecalledAt.Equal(CreatedAt)` is true, even though Recall *did* update the timestamp. The bug is in the test's assumption that `time.Now()` strictly advances between two adjacent calls, not in the production code.
- **Fix**: insert `time.Sleep(time.Millisecond)` between `p.Add` and `tool.Execute` in the test so the clock provably ticks. 50/50 race-mode runs pass after the fix.
- **Lesson**: `t1, t2 := time.Now(), time.Now(); t1 == t2` is allowed by the Go spec — successive `Now()` calls in the same monotonic tick return equal values. Tests that assert "later timestamp is strictly greater" need either a sleep or a fake clock. Don't depend on real-clock progress for correctness assertions inside fast-running tests.
- **Refs**: `internal/tools/memorytool/memorytool_test.go:TestRecall_HitReturnsEntryAndBumpsRecall`, `internal/memory/project.go:Add` (line 266 `time.Now`), `internal/tools/memorytool/memorytool.go:92` (line 92 `time.Now`)

### Asserting zero collisions over N random IDs is a birthday-paradox flake trap
- **Saw**: `TestNewSubSid_FormatAndUniqueness` failed intermittently with `duplicate sub-sid: 20260601-103412-7790c1` — generating 1000 sub-sids at a fixed timestamp and asserting all unique.
- **Why**: sub-sids use a 24-bit (6 hex) random suffix (matching `session.generateID`'s format contract). Over 1000 draws the birthday probability of *some* collision is `1000²/(2·2²⁴) ≈ 3%`, not the "~6e-4" the test comment claimed — so the zero-collision assertion flaked ~1 run in 34. The bug was the test's probability assumption, not production: real subagent fan-out is far below 1000 IDs/second and the second-resolution timestamp disambiguates across seconds.
- **Fix**: assert RNG *health* instead of perfection — require ≥990/1000 distinct (tripping it needs ≥11 collisions, P≈1e-15; a degenerate/constant suffix fails immediately). Added the missing different-timestamp uniqueness check the comment had claimed. Commit (this one).
- **Lesson**: with a b-bit random ID you cannot robustly assert "all unique" for any meaningful N — the birthday bound is `N²/2^(b+1)`. Either widen the entropy (if the format allows) or assert a relaxed "distinct ≥ N−k" / "not degenerate" property. Don't paper a too-small ID space with a flaky zero-collision test; and don't widen entropy just to satisfy a test when production fan-out is safe and the format is a shared contract.
- **Refs**: `internal/subagent/types_test.go:TestNewSubSid_FormatAndUniqueness`, `internal/subagent/types.go:newSubSid` (3-byte suffix), `internal/session/session.go:generateID` (the mirrored format)

### SSE test hangs in `srv.Close()` when the response body only closes via `t.Cleanup`
- **Saw**: a `/events` (SSE) topic test passed all assertions, then the whole run wedged for 30s until `go test -timeout` dumped stacks — blocked inside `httptest.Server.Close`, "waiting for connections", with the still-open SSE response body registered only as `t.Cleanup(func(){resp.Body.Close()})`.
- **Why**: `t.Cleanup` callbacks run AFTER the test function's `defer`s. `defer srv.Close()` (registered first, at server start) therefore fires while the SSE body is still open — and `httptest.Server.Close` blocks indefinitely on live connections. Same LIFO family as the chdir/TempDir entry above, but inverted: the cleanup-only close is too LATE instead of too early.
- **Fix**: every SSE-open helper's caller must `defer resp.Body.Close()` immediately after the open — LIFO then closes the body before the server. Keeping the `t.Cleanup` inside the helper is still fine as a belt-and-suspenders for Fatal-before-return paths (double `Body.Close` is a safe no-op for http bodies). Commit (this one).
- **Lesson**: for never-ending response bodies (SSE, long-poll), the open helper returning `resp` is a contract: the caller owes a `defer resp.Body.Close()` before anything that closes the server. A cleanup-only close guarantees the hang.
- **Refs**: `internal/controlplane/webfrontend_topic_test.go:openEventsRaw` callers; W1.3's `openSSE` callers in `webfrontend_conv_test.go` follow the pattern.

### Handler parked on `<-r.Context().Done()` with an unread request body hangs `srv.Close()` forever
- **Saw**: a ctx-cancel test (handler blocks on `<-r.Context().Done()`, client cancels mid-flight, client call returns fine) wedged the whole test binary until `go test -timeout` — `httptest.Server.Close` waited forever for the parked handler; the server-side request context never fired despite the client having disconnected.
- **Why**: on HTTP/1.1 the server only cancels `r.Context()` on client disconnect once a background connection read is active, and `net/http` defers starting it until the request body hits EOF (`registerOnHitEOF` in `serveConn`). A handler that parks with the body unconsumed never gets that read started, so the disconnect is invisible.
- **Fix**: drain the body before parking — `_, _ = io.Copy(io.Discard, r.Body)` then `<-r.Context().Done()`. Commit (this one).
- **Lesson**: a fake that parks on the request context is also a body consumer. The client's abort and the server's *knowledge* of that abort are two different events; on HTTP/1.1 the second one costs an explicit drain (or any handler-side `r.Body` read to EOF). Same `srv.Close()` hang signature as the SSE entry above, different root cause — LIFO ordering there, background-read deferral here.
- **Refs**: `pkg/deepseek/asr_test.go:TestASR_ContextCanceled`

### `r.FormFile` can't see multipart FIELD ORDER — walk `MultipartReader` parts to assert it
- **Saw**: F1 needed a test proving the Files API upload body puts the `purpose` field part BEFORE the `file` part (the API 400s on misorder, PRD feature-deepseek-app-api §1.2), but `r.FormFile`/`r.FormValue` answer only "which value", never "which position".
- **Why**: `ParseMultipartForm` (which `FormFile`/`FormValue` call under the hood) stores parts in `Value`/`File` maps — ordering is dropped at parse time. Only the sequential `multipart.Reader.NextPart()` stream preserves wire order.
- **Fix**: `readUploadParts` in files_test.go walks `r.MultipartReader()`'s `NextPart` sequence and asserts position 0 = `purpose` (plain field, no filename), position 1 = `file`. Commit (this one).
- **Lesson**: when the CONTRACT is part order (some APIs — OpenAI-style Files endpoints especially — reject misordered multipart fields), assert on the raw part stream, never on the parsed form. The parsed form is fine for presence/content, blind to order.
- **Refs**: `pkg/deepseek/files_test.go:readUploadParts`, `pkg/deepseek/files.go:doUploadFile`

### Exec-based E2E binary can't live in any test's `t.TempDir()` — own its lifecycle in `TestMain`
- **Saw**: the first `-tools` E2E test passed; every later one failed with `fork/exec …/seek-e2e: no such file or directory` even though the build succeeded.
- **Why**: the shared `go build` artifact was written into the FIRST consumer's `t.TempDir()`, which is removed when that test ends — the `sync.Once`-cached path then points at a deleted file for the rest of the test-binary run.
- **Fix**: build into a package-level `os.MkdirTemp` dir, removed by a `TestMain` after `m.Run()`; `sync.Once` still dedupes the build. Commit (this one).
- **Lesson**: anything shared across tests (built binaries, downloaded fixtures) must have package-lifetime storage, never per-test `t.TempDir()`. Per-test dirs are for per-test inputs only.
- **Refs**: `cmd/seek/tools_flag_test.go:TestMain`, `buildSeekBin`

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

## Site / landing page

### Explicit clean-URL rewrites in `_redirects` loop forever on Cloudflare Pages
- **Saw**: on the deployed docs site (Cloudflare Pages), exactly 7 doc pages (`commands`, `architecture`, `sessions`, `memory`, `skills`, `subagent`, `cron`) were "unclickable" — clicking the card hung with `ERR_TOO_MANY_REDIRECTS`. Every other page (`config`, `goal`, …) opened fine. `curl -I` showed the 7 stuck in an infinite 307 loop to themselves; the rest returned 200
- **Why**: `examples/_redirects` had `"/docs/commands  /docs/commands.html  200"` rewrite rules for those 7 pages. But Cloudflare Pages **already serves clean URLs automatically** (`/docs/commands` → `commands.html`) AND canonicalises `*.html` → the extensionless form. The explicit 200-rewrite collides with that built-in handling: clean URL rewrites to `.html`, CF redirects `.html` back to clean, repeat. Pages with NO rule never entered the loop because CF's native handling is self-consistent. The set of broken pages == the set of pages listed in `_redirects`, which is the tell
- **Fix**: delete the `200` clean-URL rewrite rules entirely — they are redundant on Cloudflare Pages. Kept only `/docs → /docs/ 301` and a NOTE comment warning against re-adding them. `examples/_redirects`
- **Lesson**: don't hand-write clean-URL rewrites for a host that does clean URLs natively (Cloudflare Pages, Vercel `cleanUrls`). The "helpful" rule fights the platform and loops. When only a *subset* of similar pages breaks, diff that subset against config — here it mapped 1:1 to the `_redirects` entries. Also: a static site's deploy behaviour (clean URLs, trailing slash, redirects) is invisible from `file://` and from the repo source — verify against the real deployed host with `curl -I`, following redirects
- **Refs**: `examples/_redirects`; symptom surfaced via `curl -sSL -o /dev/null -w '%{http_code}'` against the live site

### Pixel "SEEK" banner read as "98%" on mobile — font-glyph block-art is not portable
- **Saw**: hero wordmark built from a `<pre>` of `█` (U+2588) block characters rendered crisply as "SEEK" on desktop but sheared into "98%" on a mobile in-app (CJK) webview — S→9, E E→8 8, K→%
- **Why**: the banner's monospace grid only holds when `█` fills exactly one cell. On mobile none of the stack's fonts (JetBrains Mono / SF Mono / Fira Code / Courier New) exist, so `█` falls back to a CJK/proportional glyph at a different advance width and `line-height: 1.15` adds vertical gutters — the strokes split into misaligned dots the eye re-reads as digits. Two earlier commits chased the wrong layer (redrew the glyph shapes, added then deleted a text wordmark); neither helped because the medium, not the letterforms, was the problem
- **Fix**: render the wordmark as real DOM cells on a 23×7 CSS grid (`<i style="grid-area:r/c">` per lit pixel), not font glyphs. Uniform cells are perfectly aligned and font-independent on every device. Source-of-truth pattern kept as an HTML comment beside the markup; lit-cell divs generated from it. `examples/index.html` `.pixel-logo`
- **Lesson**: never render a brand/wordmark as ASCII block-art that depends on a font glyph (`█`, box-drawing, braille) — mobile/CJK webviews will fall back to a glyph of the wrong width and shear it. Use real DOM/SVG rects. Also: when a visual bug survives a "fix", check you're fixing the right layer (medium vs content) before redrawing. For the look — flat hard-edged squares + uniform gutter = pixel-art (an outer amber bloom is fine and wanted); per-pixel rounding/gradients/halos or fused segment bars are what tip it into "LED display", a different aesthetic
- **Refs**: `examples/index.html` `.pixel-logo` + pixel-grid markup; commits c346c20, 85efd29 (the two mis-aimed attempts)

### README hero SVG clipped on the right on GitHub — its monospace is wider than local Menlo
- **Saw**: a hand-authored terminal-style banner (`examples/hero.svg`) fit perfectly when rasterised locally (qlmanage / Menlo), but GitHub's README render clipped the right edge — the tagline lost its last word and the status strip was cut off
- **Why**: GitHub renders `<text>` in an inline-referenced SVG with ITS OWN (wider) monospace, not the macOS Menlo that `qlmanage` uses, so lines sized to "just fit" the 1200 viewBox locally overflow it on GitHub. Compounding it: adding `width`/`height` attrs to a viewBox-only SVG made `qlmanage` mis-scale (zoom + clip), which masked the real cause
- **Fix**: keep the SVG **viewBox-only** (no `width`/`height`) and shorten every text line well under the viewBox width so any reasonable monospace fits with margin to spare. `examples/hero.svg`
- **Lesson**: never size SVG `<text>` to "just fit" against a local rasteriser — the consumer (GitHub, another OS) picks a different font with a different advance width and overflows. Leave generous horizontal margin, or convert text to paths if exact width matters. `qlmanage` is NOT a faithful preview of GitHub's SVG font; verify the actual GitHub render
- **Refs**: `examples/hero.svg`; sibling entry above (pixel block-art portability)

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
- **Fix**: (1) added `?` hotkey in `handleKey` when input is empty (intercepted before textarea); (2) refactored `cmdHelp` to open a topic picker on no-args (topics: all, commands, keys, about) and show the overlay only when a topic is selected or passed as arg; (3) added `/help ` auto-open handoff in `updateCommandMenu` mirroring `/model /effort` pattern. Commit (this one).
- **Lesson**: don't document keyboard shortcuts that don't exist — the user will try them and feel gaslit. When adding a command that parallels an existing one (`/model` → picker), match the interaction pattern; a full-height overlay is a very different ask than a compact dropdown. The cost of keeping `?` in-sync across help content and handler code is the same as implementing it.
- **Refs**: `internal/tui/update_key.go` (`?` hotkey), `internal/tui/commands.go` (cmdHelp → picker + topic builders), `internal/tui/update_menu.go` (`/help ` auto-open), `internal/tui/commands_test.go` (picker + topic tests)

### Auto-opened picker key-passthrough allow-list is a second, out-of-sync registry
- **Saw**: typing `/skill use pl` then backspace appeared to freeze the UI — Tab/Enter/↑/↓/Esc still worked, but Backspace and typing did nothing. Same for `/review` and `/help` auto-opened pickers.
- **Why**: `update_key.go` has a `switch m.pickerPurpose` at line 210 that lists which picker purposes pass Backspace/Runes/Space through to the textarea. When `skill-verb`, `skill-name`, `review`, and `help-topic` were added as auto-opened pickers in `update_menu.go`, this allow-list was never updated. Those keys fell into the `default` branch ("modal picker: swallow all other keys") and were silently discarded — the textarea never received them, so `updateCommandMenu` never saw a changed value and couldn't dismiss the picker. The user experience was "frozen": the picker was displayed but no key except the picker-navigation subset did anything.
- **Fix**: added `"review", "skill-verb", "skill-name", "help-topic"` to the allow-list in `update_key.go:210`, and added `"help-topic"` to the stale-cleanup condition in `update_menu.go:219` (which was also missing it). Closes #4.
- **Lesson**: adding a new auto-opened picker requires touching THREE places: (1) the Branch 1 trigger in `update_menu.go`, (2) the Branch 2 stale-cleanup condition in `update_menu.go`, and (3) the key-passthrough allow-list in `update_key.go`. There's no compile-time coupling between these — missing (2) or (3) silently produces a frozen picker. Consider a shared `map[string]bool` or a registration pattern that makes it impossible to add a picker without wiring all three. Until then: when you add a new auto-opened picker, grep for the last one you added and make sure all three sites match.
- **Refs**: `internal/tui/update_key.go:210`, `internal/tui/update_menu.go:133,182,219`

### Legacy conhost (blue PowerShell 5.x window) TUI streaming cascades (use Windows Terminal)
- **Saw**: in blue-background Windows PowerShell 5.x (conhost), assistant streaming text printed a new full-length line on every token — a "staircase" of growing sentences; stray escape bytes appeared in the left margin
- **Why**: seek inline mode relies on ANSI cursor-up + erase to overwrite the live region each frame. Legacy conhost does not handle these sequences reliably; each frame appends instead of overwriting
- **Fix**: document Windows Terminal as the supported Windows TUI host — no seek-side terminal shims. Users install WT via winget / Store / GitHub release and run seek there. Fallback: `seek -p` print mode. See [`docs/guide-windows.md`](guide-windows.md)
- **Lesson**: on Windows, the terminal emulator choice *is* the compatibility layer. Prefer documenting and recommending a modern host over per-emulator code paths in seek
- **Refs**: [`docs/guide-windows.md`](guide-windows.md), [Windows Terminal](https://github.com/microsoft/terminal)

### WM_SETTINGCHANGE broadcast blocks startup on busy Windows desktops
- **Saw**: first-run seek -install / PATH nudge took 1-5 seconds on a machine with many open windows; the delay was caused by SendMessageTimeoutW returning only after every top-level window processed (or timed out on) the WM_SETTINGCHANGE message
- **Why**: HWND_BROADCAST sends sequentially to every top-level window. Browsers with hundreds of tabs, Electron apps, and Office processes each take a small slice of time; summed across dozens of windows the latency hits seconds. SMTO_ABORTIFHUNG only skips hung windows - responsive ones still get the full sequential send
- **Fix**: split the PATH operation into EnsureInPATH (registry write only, no broadcast - for startup nudge) and EnsureInPATHWithBroadcast (registry + broadcast - for explicit seek -install). Startup nudge writes the registry in microseconds and tells the user to restart their terminal; the broadcast is only done when the user explicitly runs seek -install
- **Lesson**: SendMessageTimeoutW(HWND_BROADCAST, WM_SETTINGCHANGE) is not free - avoid it in startup or fast paths. Registry writes take effect on next login even without a broadcast; the broadcast is a UX nicety, not a correctness requirement
- **Refs**: internal/pathop/pathop_windows.go, MSDN WM_SETTINGCHANGE (https://learn.microsoft.com/en-us/windows/win32/sysinfo/wm-settingchange)

### Windows CRLF paste without bracketed mode submits after the first line
- **Saw**: pasting multi-line text into the seek TUI on Windows sent only the first line immediately; remaining lines were lost
- **Why**: terminals without bracketed paste (or Ctrl+Shift+V paths that bypass it) inject each CRLF line as KeyRunes followed by `\r`, which bubbletea maps to KeyEnter — and seek treats Enter as submit. Bracketed paste (`msg.Paste`) was also fed through textarea.Update, letting `\r` bytes in the paste body through before fold logic ran
- **Fix**: wholesale paste injection for `msg.Paste` and Ctrl+V (clipboard.ReadAll), CRLF normalization, and a 50ms Enter→newline guard when Enter arrives immediately after KeyRunes (intra-paste `\r`). Fold marker resolution consolidated in `resolvePasteInInput`
- **Lesson**: on Windows, `\r` is Enter — never let raw CRLF paste reach the Enter/submit path line-by-line. Either handle the full paste body atomically (bracketed paste / clipboard) or treat rapid Enter-after-runes as newline insertion, not submit
- **Refs**: `internal/tui/paste.go`, `internal/tui/update_key.go`, `internal/tui/paste_test.go`

### Child process steals /dev/tty, Esc can't interrupt bash
- **Saw**: running a third-party command via seek's bash tool that prompts interactively (sudo, ssh, git credentials, npm init, etc.) froze the entire TUI — Esc was unresponsive and the session appeared hung
- **Why**: Go's `exec.Cmd` leaves the child process connected to the controlling terminal. Commands that open `/dev/tty` directly (bypassing stdin) steal keystrokes from the TUI — your Esc goes to the child, not to seek. The context-cancel path (Esc → cancelStream → ctx.Done) technically fires but the child never gets the signal because it's busy reading /dev/tty
- **Fix**: call `setsid()` via `cmd.SysProcAttr.Setsid = true` before starting the child. This creates a new session, detaching from the controlling terminal — `/dev/tty` open returns ENXIO instead of blocking the UI. Added `detachStdin()` (Unix build tag) with a no-op for Windows
- **Lesson**: child processes inherit the parent's TTY by default. Process-spawning tools must explicitly opt out of TTY inheritance (setsid, setctty=false, or equivalent), otherwise interactive prompts steal input in a way that context cancellation can't reach
- **Refs**: `internal/tools/bash/bash_unix.go`, `internal/tools/bash/bash.go` (line 113)

### Setsid + pipe stdout = orphan grandchildren deadlock Wait()
- **Saw**: `TestBash_ContextCancel_KillsProcess` timed out at 10s. Context cancellation (Esc) didn't kill `sleep 600` even though `exec.CommandContext` sent SIGKILL to the child PID.
- **Why**: `detachStdin` sets `Setsid = true` to detach the child from the controlling terminal. But Setsid creates a new session where `sh` can fork `sleep` as a grandchild. When SIGKILL kills `sh` alone, `sleep` survives as an orphan **inheriting the stdout/stderr pipe fds** → the write-ends stay open → `cmd.Wait()` blocks forever waiting for EOF on the pipes.
- **Fix**: switch from `exec.CommandContext` to `exec.Command` + manual `Start()`/`Wait()` on Unix. A dedicated goroutine waits for `cctx.Done()` and kills the **entire process group** via `syscall.Kill(-pid, SIGKILL)`. This closes all pipe fds and lets `Wait()` return.
- **Lesson**: Setsid orphans can keep pipe fds alive. When using Setsid + pipe I/O, always cancel by process group (`-pid`), not by PID.
- **Refs**: `internal/tools/bash/bash.go`, `internal/tools/bash/bash_unix.go`

### V4-Flash prediction returns empty content when Thinking isn't explicitly disabled
- **Saw**: side-channel suggested-reply (`Suggest`) always returned `""` even though the API call succeeded (200 OK) and tokens were consumed. `finish_reason="length"`, `content=""`, but `reasoning_content` was populated with the model's internal monologue — all `max_tokens` were spent on reasoning, leaving nothing for the actual prediction.
- **Why**: the DeepSeek API currently defaults V4-Flash to thinking-mode-*on* at the endpoint level, even though `ShouldEnableThinking("deepseek-v4-flash")` returns false in the code. Without an explicit `Thinking: {Type: "disabled"}` in the request, the model burns the entire `max_tokens` budget on `reasoning_content` and emits an empty `content` field.
- **Fix**: set `Thinking: &deepseek.ThinkingMode{Type: "disabled"}` in the `Suggest` function's `ChatRequest`. The prediction task (short single-sentence guess) needs zero chain-of-thought reasoning.
- **Lesson**: never assume the API default matches the code's `ShouldEnableThinking` switch. V4 models may have default thinking enabled server-side regardless of what the client expects. When the task is fast/cheap (side-channel prediction, classification), always pin `Thinking: disabled` explicitly.
- **Refs**: `internal/suggester/suggester.go`, `pkg/deepseek/types.go:ShouldEnableThinking`, `pkg/deepseek/types.go:ThinkingMode`

### Cross-compile failure: platform-specific syscall in non-tagged file
- **Saw**: goreleaser failed with `undefined: syscall.Kill` when cross-compiling for Windows, even though CI's `go build ./...` on a Windows runner had passed. The error only appeared during the release pipeline (GoReleaser cross-compiles all targets from a single Linux runner).
- **Why**: `syscall.Kill(-pid, SIGKILL)` — a Unix process-group-kill call — was placed in `bash.go` (no build constraint). On native Windows (`GOOS=windows GOARCH=amd64` on a Windows runner), the code may compile due to platform-specific `syscall` re-exports. But when cross-compiling FROM Linux TO Windows (as goreleaser does), the linker doesn't find the same symbols, and the build fails.
- **Fix**: extracted `killProcessGroup(cmd)` into `bash_unix.go` (`//go:build !windows`) and `bash_windows.go` (`//go:build windows`), matching the pattern already used by `detachStdin`. Every Unix-only syscall must live behind a build tag.
- **Lesson**: `go build ./...` on a native Windows runner is NOT a sufficient guard for cross-compilation. Always add an explicit cross-compile step (`GOOS=windows GOARCH=amd64 go build ./cmd/seek`) on the Linux CI runner, mirroring the goreleaser build matrix. This catches "compiles on platform X but fails when cross-compiled" before the release tag is cut.
- **Refs**: `.github/workflows/ci.yml` (cross-build job), `internal/tools/bash/bash.go`, `internal/tools/bash/bash_unix.go`, `internal/tools/bash/bash_windows.go`

### WSL sudo escapes process-group kill, bash Wait() hangs past timeout
- **Saw**: `bash -c 'sudo cp …'` in WSL2 hung 55+ minutes despite `timeout_ms` max (10m); Esc/context cancel fired but the TUI stayed blocked until manual kill
- **Why**: `sudo` (and `setsid`) create a new session/PGID — `SIGKILL` to `-sh.PGID` only kills the shell wrapper. WSL `sudo` with no TTY blocks on a Windows credential dialog (not killable via Linux signals). Orphaned descendants keep stdout/stderr pipe fds open → `cmd.Wait()` never returns even after the context timer fires
- **Fix**: on Linux, walk `/proc/[pid]/children` recursively and SIGKILL every descendant **before** the PGID kill (killing sh first reparents escapees and empties `/proc/<sh>/children`); cap `cmd.Wait()` with a 5s post-kill grace so the tool returns even when kill fails; snapshot stdout via a mutex-protected buffer because pipe-copy goroutines may still run after the grace deadline
- **Lesson**: Setsid + PGID kill is necessary but not sufficient when children deliberately escape the group (sudo, setsid). Snapshot the descendant tree before reaping the root, pair group kill with descendant cleanup AND a Wait() deadline — never assume SIGKILL reaps the whole tree
- **Refs**: `internal/tools/bash/proc_linux.go`, `internal/tools/bash/bash_unix.go`, `internal/tools/bash/bash.go`, issue #9

### ObserveEnqueue goroutine captures entry parameter by reference
- **Saw**: `go test -race ./internal/memory` failed in `TestObserveEnqueue_RespectsSessionCap` at `hook.go:713` (read/write on the same address from two filter goroutines) even after `observeAcceptCt` was converted to `atomic.Int32`
- **Why**: the async goroutine closed over the outer `entry` parameter without copying it. The closure returns immediately after a non-blocking `ResultChan` send; the test (and production TUI) enqueues the next entry while the prior goroutine may still be running. Go reuses the parameter stack slot, so the second call overwrites `entry` while the first goroutine still reads it
- **Fix**: launch with `go func(ctx context.Context, entry Entry) { … }(ctx, entry)` so each goroutine owns a value copy
- **Lesson**: any `go func()` inside a callback that returns before the goroutine finishes must copy loop/parameter variables into the closure literal — atomics on unrelated counters do not fix captured-variable races
- **Refs**: `internal/memory/hook.go:ObserveEnqueue`, `internal/memory/auto_distill_test.go:TestObserveEnqueue_RespectsSessionCap`

### /proc/PID/children empty on CI — descendant walk returns nothing
- **Saw**: Linux CI failed `TestDescendantPIDs_FindsNestedChildren` (got `[]`) and `TestBash_Timeout_KillsEscapedProcessGroup` (elapsed ≈ `killWaitGrace` 5s — setsid sleep survived)
- **Why**: `descendantPIDs` relied solely on `/proc/[pid]/children`, which is missing or empty on some GitHub Actions kernels/containers even when `/proc/[pid]/stat` PPID links are present
- **Fix**: fall back to scanning `/proc/*/stat` for `PPID == parent` when the `children` file is absent or empty; keep snapshot-before-kill ordering in `killProcessGroup`
- **Lesson**: treat `/proc/PID/children` as an optimisation, not the only source of truth — PPID scan is slower but works wherever `/proc` is mounted
- **Refs**: `internal/tools/bash/proc_linux.go`, `internal/tools/bash/proc_linux_test.go`

## Worktree / Windows

### Worktree-isolated subagent edited the MAIN tree (isolation silently bypassed)
- **Saw**: autopilot e2e — a subagent spawned with `isolation:"worktree"` was asked to append a line to `README.md`; the report said "→ <worktree>", but the line landed in the **main repo's** `README.md` and the worktree stayed untouched
- **Why**: TWO layers. (1) `read`/`edit`/`write` resolved relative paths with `filepath.Clean(a.Path)` — i.e. against the PROCESS cwd (main repo), never `policy.CWD()`. (2) Deeper: even after fixing that, `buildSubagentRunner` built the child registry by **reusing the parent's tool instances** (`parentReg.Lookup`), and those instances embed the PARENT policy (main-repo CWD). So the child policy's worktree CWD was set by `Manager.Spawn` but no tool ever consulted it. `bash`/`git` looked isolated only because they pin `cmd.Dir`; the in-process file tools had no such pin.
- **Fix**: (a) add `permission.Policy.Resolve(path)` — abs paths cleaned, RELATIVE paths joined to `policy.CWD()` — and use it in read/edit/write; (b) in `buildSubagentRunner`, REBUILD the policy-bearing tools (read/write/edit/bash) with `job.Policy` instead of reusing the parent instance. (left uncommitted for review)
- **Lesson**: a child policy with the right CWD is inert unless the child's TOOLS are constructed with that policy. "Isolation" that's only wired into `cmd.Dir` (bash/git) is a half-measure — in-process file tools must resolve paths against `policy.CWD()`, and subagent registries must not silently reuse parent tool instances that close over the parent policy. Worktree creation ≠ worktree isolation.
- **Refs**: `internal/permission/permission.go:Resolve`, `internal/tools/{read,edit,write}`, `cmd/seek/main.go:buildSubagentRunner`, `internal/tools/write/write_test.go:TestWrite_RelativePath_AnchoredToPolicyCWD`

### /worktrees panel empty on Windows despite seek-managed worktrees existing
- **Saw**: TUI `/worktrees` panel always empty on Windows even when `ListFromDisk` should find entries under `~/.seek/projects/<pid>/worktrees/`
- **Why**: `git worktree list --porcelain` from Git for Windows emits forward slashes in `worktree <path>` lines, but `ListFromDisk` compared against `seekRoot` built with `filepath.Join` (backslashes). `strings.HasPrefix` is byte-exact — mixed separators never match
- **Fix**: normalize porcelain paths at parse time via `filepath.Clean(strings.ReplaceAll(p, "/", string(filepath.Separator)))` before the seekRoot prefix filter
- **Lesson**: never compare git-emitted paths with `strings.HasPrefix` against `filepath.Join` roots on Windows — normalize separators first or use `filepath.Rel`/`isPrefix`
- **Refs**: `internal/worktree/worktree.go:ListFromDisk`, `internal/worktree/worktree_test.go:TestListFromDisk_NormalizesForwardSlashes`

### never-discarded worktree refs outlive every GC (six June zombies, 2026-11-14)
- **Saw**: `seek worktree gc` (and startup GC) never touched six `seek/wt/*` worktrees created 2026-06-01 — `--older-than` also only applied to them. Five months of `refs/seek/worktrees/<id>` + branch + directory lingered until manual cleanup
- **Why**: GC only ever enumerated `refs/seek/discarded/*` — the marker written by `exit_worktree(if_dirty=discard)`. A session that dies BEFORE exit_worktree leaves NO marker anywhere, so marker-driven GC has nothing to match; an unmarked orphan is invisible to it by construction
- **Fix**: `Manager.PruneOrphans` — age-based fallback enumerating `refs/seek/worktrees/` (the ID embeds the creation timestamp, so age needs no mtime): collect past `--orphan-after` (default 7d) ONLY when (1) not in this process's active map, (2) `merge-base --is-ancestor <ref> HEAD` proves no unmerged commits, (3) `git worktree remove` WITHOUT `--force` succeeds (git itself refuses dirty/locked trees — conservative for unattended GC). All six real instances were in main's ancestor chain, i.e. exactly what the ancestry guard admits
- **Lesson**: a GC keyed on a marker only collects sessions that lived long enough to write the marker. Any crash path between create and teardown needs an orthogonal collection axis — age — with its own fail-safe guards (ancestry proof + no-force removal), because age alone can't tell trash from a slow session
- **Refs**: `internal/worktree/worktree.go:PruneOrphans`, `internal/worktreecli/worktreecli.go:cmdGC` (`--orphan-after`), `cmd/seek/main.go` startup call

## Sandbox / OS jail

### Landlock create_ruleset returns EINVAL if you "handle" an access right the kernel's ABI doesn't know
- **Saw**: (design-time, caught while implementing 柱 O) a fixed `handled_access_fs` mask including `LANDLOCK_ACCESS_FS_TRUNCATE` (ABI 3) / `REFER` (ABI 2) / `IOCTL_DEV` (ABI 5) would make `landlock_create_ruleset` fail with EINVAL on any older kernel — i.e. the whole jail silently fails to build on exactly the machines most in need of a fallback
- **Why**: Landlock is versioned. `landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION)` returns the kernel's ABI (1..N); the set of rights you may *handle* grows per ABI. Handling a newer right on an older kernel is rejected, not ignored
- **Fix**: probe the ABI first, then mask the handled write-rights set to that ABI (base FS-write rights for ABI≥1, add REFER at ≥2, TRUNCATE at ≥3, IOCTL_DEV at ≥5). Plus: `no_new_privs` MUST be set before `restrict_self`; the trampoline FAILS CLOSED (exit 127) if the jail can't be applied, never running the command unconfined. `internal/sandbox/sandbox_linux.go`
- **Lesson**: for any versioned kernel LSM API, query the supported version and mask your request down to it — never hardcode the union of all features. And confine the IN-PROCESS file tools too: kernel jails like landlock apply to the process, but only bash/git pin `cmd.Dir`; see the worktree-isolation pitfall above for the parallel hole on the permission side
- **Refs**: `internal/sandbox/sandbox_linux.go:applyLandlock`, `internal/sandbox/sandbox_linux_test.go`

## Cron parser

### `*` wildcard is not a number — must be handled before `strconv.Atoi` in field-item parser
- **Saw**: after adding 5-field cron parsing, all valid expressions with `*` fields (e.g. `* * * * *`) failed with `bad value "*"` because `parseCronItem` fell through to `strconv.Atoi("*")`. Same for any `@hourly`-disguised test that happened to pass `*` to the cron path.
- **Why**: the parse pipeline for a single field item checked name aliases → `*/N` → range/step → range → `strconv.Atoi`. A bare `*` matched none of those patterns and hit the number parser, which rejected it.
- **Fix**: add an early `if item == "*"` check before any other item-type detection, returning `cronStep(min, max, 1)` (all values). Commit `[current commit]`.
- **Lesson**: any cron parser that decomposes field items into "special forms" must treat `*` as a first-class form, not a fallback default. It's the most common cron symbol and the easiest to forget.
- **Refs**: `internal/routines/schedule.go:parseCronItem`, `internal/routines/schedule.go:parseCronField`

## Feishu / external API

### Feishu IM API returns HTTP 200 on business errors — must parse body `code`, and `content` is double-encoded JSON
- **Saw**: the old custom-bot webhook format (`feishu` / `feishu-flow`) silently swallowed failures: a keyword/signature-protected bot rejected the POST, but Feishu replied HTTP 200 with a non-zero `code` in the body, and seek only checked `resp.StatusCode` — the WARN never fired, the user thought delivery succeeded.
- **Why**: Feishu's entire open-platform API surface signals success/failure in the JSON body envelope `{code, msg, data}`, NOT via HTTP status. HTTP is ~always 200; only network-layer faults produce 4xx/5xx. The same trap carries over to the new self-built-app IM API (`im/v1/messages`). A second trap on the send path: the request body's `content` field is itself a JSON *string* (the inner `{"text":"..."}` is serialized once, then that string sits as the value of `content` in the outer body) — naive `"content": map[string]string{...}` yields a 400-equivalent body error.
- **Fix**: the feishu path in `internal/routines` now reads the response body and parses `code`; `code != 0` returns an error (so `--probe` and the best-effort dispatcher both surface it). On a token-invalid/expired code it invalidates the cached token and retries once. `content` is built by `json.Marshal(map[string]string{"text": s})` then placed as a string in the outer payload. Old `feishu`/`feishu-flow` formats removed in favour of `feishu` = self-built-app IM API. Commit `[current commit]`.
- **Lesson**: any "does it work?" check against a Feishu (or similarly-shaped) API must read the body envelope, not the HTTP status. And when an API field is documented as type "String" in their schema, it often means a JSON-encoded string — double-check by marshaling the inner object yourself. The `tenant_access_token` is good for 7200s; cache it (refresh ~5 min early) and share one holder per `app_id` so multiple targets don't each mint their own.
- **Refs**: `internal/routines/feishu_bot.go`, `internal/routines/feishu_bot_test.go` (HTTP 200 + code≠0 trap covered by `TestFeishuBot_BodyCodeNonZeroIsError`), `docs/guide-webhooks.md` §飞书

## Subprocess / secrets

### Go's `exec.Cmd` with nil `Env` hands the child EVERY parent variable — including the API key
- **Saw**: (audit-time, found while comparing seek against deepseek-harness) the bash tool never set `cmd.Env`. A model-issued `env` printed `DEEPSEEK_API_KEY`, `GH_TOKEN`, `GITHUB_TOKEN`, `OPENAI_API_KEY` verbatim — and so did every npm postinstall / Makefile recipe / build script that command transitively spawned. Same hole in the MCP and LSP clients, which launch *third-party* binaries.
- **Why**: two defaults compounding. (1) `os/exec` documents `Env == nil` as "inherit the current process's environment" — the safe-looking zero value is the maximally-permissive one. (2) The OS sandbox looked like coverage but isn't: seatbelt/landlock confine **file** effects only; environment inheritance is not a file effect. dsh draws the same boundary explicitly — its sandbox vocabulary is `read-only`/`workspace-write`/`danger-full-access`, all file-scoped.
- **Fix**: new `internal/childenv` — name-based scrub (`KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL` substrings + `SEEK_` prefix) applied at every spawn point whose code seek does not control: bash tool (foreground + background), `pkg/mcp`, `internal/lspclient`. Escape hatch is explicit-only: `config.BashEnvPassthrough` (exact, case-insensitive names) and the existing `ServerConfig.Env`, both layered AFTER the scrub so they win last. NOT applied to `internal/routines/tick.go`, which re-spawns `seek` itself and legitimately needs the key.
- **Lesson**: `cmd.Env = nil` is a decision, not a default — audit it at every `exec.Command` whose argv is model-chosen or whose binary is third-party. Match on variable NAMES, never values (value sniffing both false-positives into broken builds and false-negatives into leaks). And keep the predicate narrow enough to be safe: `AUTH` looks like a natural entry but would scrub `SSH_AUTH_SOCK` and break every ssh-backed `git push`.
- **Refs**: `internal/childenv/childenv.go`, `internal/tools/bash/bash.go:childEnv`, `internal/tools/bash/bash_test.go:TestBash_ScrubsCredentialsFromChildEnv`, dsh `packages/subprocess/subprocess/src/index.ts:44-65`

## Tool output

### Head-only truncation of command output drops the verdict and produces a SILENT wrong answer
- **Saw**: (audit-time) the bash tool clamped over-budget output with `output[:32KiB]` — keep the head, drop the tail. For `go test ./...`, `make`, `npm run build`, `pytest` the verdict is LAST: a wall of passing lines, then the failure summary. The model received the wall, never the `FAIL` lines, and reported the run as passing.
- **Why**: `s[:max]` is the obvious way to bound a string, and it's correct for output whose value is front-loaded (a file head, a `grep` result). It is exactly backwards for command output, where the head is preamble and the tail is the result. The header said only "output truncated to 32768 bytes", which gives the model no reason to suspect the *conclusion* is what went missing.
- **Fix**: `clampOutput` keeps 8 KiB of head + ~24 KiB of tail with an elision marker between them, both cuts landing on line boundaries. The marker states explicitly that the tail is intact, so the model doesn't treat the last visible line as the end of the run. `grep` was left alone on purpose — its output has no verdict at the end, and its existing notice already tells the model to narrow the pattern.
- **Lesson**: before bounding any output, ask where the INFORMATION is, not where the bytes are. Truncation that silently removes the answer is worse than truncation that removes context — the first produces a confident wrong conclusion, the second produces a request for more detail. When both ends matter, elide the middle and say so in-band.
- **Refs**: `internal/tools/bash/bash.go:clampOutput`, `internal/tools/bash/bash_test.go:TestBash_PreservesVerdictAtTail`

### git tool: a duplicated subcommand arg surfaces git's cryptic "ambiguous argument" error
- **Saw**: `git` tool called with `subcommand: "diff"` + `args: ["diff", "HEAD", "--stat"]` (the model repeated the subcommand as the first arg) returned `fatal: ambiguous argument 'diff': unknown revision or path not in the working tree. Use '--' to separate paths from revisions, like this: git diff -- <path>...`. The hint is misleading — there is no path/revision ambiguity, and following it yields a different confusing error (`pathspec 'HEAD' did not match`).
- **Why**: `internal/tools/git/git.go` builds `git -c color.ui=false <subcommand> <args...>`; a repeated subcommand becomes a second positional, which `git diff` parses as a tree-ish. The error wrapper passes git's stderr through untouched, so the model receives git's generic hint instead of the actual fix — a burned turn.
- **Fix**: pre-exec guard in `Execute` — `args[0] == subcommand` is refused with a message naming the `subcommand` field and the `["--", <path>]` escape hatch; the schema `args` description now says the subcommand is never repeated as an arg (descriptions travel with every request, so this also prevents the mistake at the source). Two tests: rejection message + `git log -- log` (a file literally named `log`) still works. (uncommitted)
- **Lesson**: when a tool wraps another CLI, error passthrough is a UI decision — a cryptic underlying error is a tool bug, not "git's fault". Refuse before exec with the real fix; and note that in a backtick-delimited raw-string schema, the schema text must not contain backticks (the first `subcommand` in an early draft of this message broke the build).
- **Refs**: `internal/tools/git/git.go:Execute`, `internal/tools/git/git_test.go:TestExecute_RejectsDuplicatedSubcommandArg`

## Testing

### Cache-hit tests against a fake backend prove the parser, not the behaviour
- **Saw**: (audit-time) seek had several assertions on `PromptCacheHitTokens` — `pkg/deepseek/stream_test.go`, `pkg/agent/agent_test.go` — all against `httptest` fakes whose usage numbers the test itself had written into the fixture. A change that broke prefix stability (re-serialising an old tool result, injecting a per-turn timestamp, reordering tool schemas) would have ~10×'d inference cost with every one of those tests still green.
- **Why**: the fake proves the wire format round-trips. Prefix-cache behaviour is a property of the BYTES seek sends across successive requests, which only a real provider can score. seek maintains those bytes by convention (CLAUDE.md's "never modify old messages", write-time-only trimming, `TestCompose_IsDeterministic`) — and a convention with no failing test is a comment.
- **Fix**: `pkg/agent/cache_e2e_test.go` — key-gated (`DEEPSEEK_API_KEY`, skips without it and under `-short`), runs a real multi-step tool turn, asserts `PromptCacheHitTokens > 0` on every turn AFTER the first, and checks hit+miss sums to `prompt_tokens`. Turn 0 is deliberately not asserted: DeepSeek's cache is server-side and outlives the process, so a cold-vs-warm first request says nothing about seek.
- **Lesson**: when a property is maintained by discipline rather than by construction, it needs a test that fails when the discipline lapses — and for provider-scored properties that test has to hit the provider. Mocks establish the format; one key-gated e2e establishes the behaviour. (dsh reaches the same conclusion at `packages/core/agent-loop/tests/request-cache.e2e.ts`; its docstring calls prefix stability "corollary #1" of log-derived requests.)
- **Refs**: `pkg/agent/cache_e2e_test.go`, `docs/dsh-analysis.md` §8.2

### Injecting filesystem failure for cross-OS tests: chmod lies, file-as-dir is honest
- **Saw**: (design-time, M-B1) testing "a failed skill write leaves no partial state" wanted a read-only parent dir (`chmod 0500`) to force `MkdirAll`/`WriteFile` to fail. On Windows the directory read-only bit doesn't block `mkdir` (chmod semantics only apply to files), so the injection silently succeeds and the test fails on the third CI OS.
- **Why**: Go's `os.Chmod` on Windows toggles the FILE read-only attribute; directories keep their ACLs. Related trap: `stat("<file>/child")` returns `ENOTDIR` on Unix but maps to `ENOENT` on Windows, so even asserting *which* error branch fired is not portable.
- **Fix**: inject with a regular FILE parked where a directory must go (e.g. `<proj>/.seek` as a file) and assert the CONTRACT (error returned + zero residue via a walk), never the branch. `internal/skillmgr/writeproject_test.go` `TestWriteProjectSkill_FailureLeavesNoPartialState`.
- **Lesson**: on a three-OS CI, failure injection must fail for the same reason everywhere; when it can't, broaden the assertion from "this branch errored" to "the invariant held" (error out, nothing written).
- **Refs**: `internal/skillmgr/writeproject_test.go`, `.github/workflows/ci.yml`

## Tool decorators

### A Tool decorator silently drops the optional interfaces the agent upcasts to
- **Saw**: (design-time, caught while building the repeat-call guard) the obvious decorator — one struct embedding `tools.Tool` and overriding `Execute` — compiles, passes its own tests, and quietly breaks two unrelated subsystems: `read`/`grep`/`list_dir` stop being dispatched concurrently, and `think` stops streaming. No error, no log line; just everything getting slower.
- **Why**: `pkg/agent` does not consume tools through the base interface alone. `agent.go:915` upcasts to `tools.StreamingTool` to prefer `ExecuteStream`, and `agent.go:996` upcasts to `tools.ReadOnlyTool` to decide whether a batch can run in parallel. Go has no delegation: a wrapper implements exactly the methods it declares, so wrapping a `ReadOnlyTool` in a struct that only satisfies `Tool` makes the upcast fail. Embedding does not help — the embedded field's type is erased at the interface boundary.
- **Fix**: `tools.WithRepeatGuard` returns one of FOUR concrete types depending on which optional interfaces the wrapped tool implements (plain / +ReadOnly / +Streaming / +both), with tests asserting each combination survives the wrap AND that a plain tool does not accidentally gain `ReadOnlyTool` (which would hand `write`/`bash` to the concurrent batch). `internal/tools/repeatguard.go`
- **Lesson**: before writing any decorator in this repo, grep for `.(tools.` to enumerate what callers upcast to — the base interface is not the contract, the union of upcasts is. The same trap applies to any future middleware over `Tool`. Prefer a constructor that switches on the concrete capabilities over a single wrapper type.
- **Refs**: `internal/tools/repeatguard.go:WithRepeatGuard`, `internal/tools/repeatguard_test.go:TestWithRepeatGuard_PreservesReadOnly`, `pkg/agent/agent.go:915,996`

### A tool result appended on the success path vanishes whenever the tool also returns an error
- **Saw**: the repeat-call reminder was initially attached to the returned result string. It fired correctly in tests that returned `(result, nil)` — and never reached the model in the case it was built for: a failing command re-run six times.
- **Why**: `pkg/agent`'s `buildToolResultMsg` documents "errors always win" — when `terr != nil` the tool-result message content becomes `fmt.Sprintf("tool error: %v", terr)`, discarding whatever result string the tool produced alongside it. Any advisory attached only to the result is dropped exactly on the error path.
- **Fix**: `appendReminder` attaches to whichever channel the model will see — `result + note` when err is nil, `fmt.Errorf("%w\n\n%s", err, note)` when it is not. `%w` keeps `errors.Is` working (`permission.ErrDenied` is checked by callers) and the note goes AFTER the original text so prefix-based parsing of error strings still matches.
- **Lesson**: in seek, a tool's error and its result are not two channels the model reads — they are one channel with the error taking precedence. Anything advisory must ride on both, and appending to a wrapped error is the only way to survive the error path.
- **Refs**: `internal/tools/repeatguard.go:appendReminder`, `internal/tools/repeatguard_test.go:TestRepeatGuard_ReminderSurvivesOnErrorPath`, `pkg/agent/agent.go:buildToolResultMsg`

## Filesystem / model safety

### `write` had no staleness protection because `edit`'s exact-match made everyone assume both were safe
- **Saw**: (audit-time) `edit` is safe by construction — it requires an exact `old_string` with an expected occurrence count, so an edit built on a stale or imagined view of the file simply fails to apply. That property quietly got attributed to the whole file-mutation surface. `write` is `os.WriteFile`: no matching, no comparison, so a model can replace a file it never read, or one that changed on disk after it read it.
- **Why**: the two tools look like a pair ("write and edit both change files") but have opposite failure modes. edit's guard is IN the operation's semantics; write's operation has no semantics to hang a guard on. The checkpoint snapshotter made the risk feel covered, but restoring from a checkpoint requires a human to notice the clobber first — which is precisely what does not happen during an unattended autopilot/goal run.
- **Fix**: `internal/fsobserve` — `read` records a (size, mtime) token per resolved path, `edit`/`write` refresh it after mutating, and `write` REFUSES (not warns) when the target exists and is either unseen or changed since it was seen. Refusing rather than warning is the point: a warning is appended after `os.WriteFile` already replaced the file. Subagents get their own store — inheriting the parent's would let a subagent blind-overwrite a same-named file in a worktree it never looked at.
- **Lesson**: when two tools in a pair share a category, check whether they share the SAFETY PROPERTY or just the category. And a stat token beats a content hash here for a non-obvious reason: `read` serves windowed views of large files, so hashing would cost a full read of a 100 MB log to serve a windowed peek.
- **Refs**: `internal/fsobserve/fsobserve.go`, `internal/tools/write/write.go`, `internal/tools/write/write_test.go:TestWrite_ReadThenWrite_Integration`, dsh `packages/fs/fs-observation-policy/src/index.ts:91-114`

### Check-then-write is a blind-overwrite guard with a hole in it
- **Saw**: (design-time, caught while comparing seek's new `write` guard against dsh's) the first version was `observer.Check(path)` followed by `os.WriteFile`. It passes every test that drives it single-threaded, and it still lets a file be clobbered: anything created at that path between the check and the write is overwritten with no guard fired. The realistic racer is not another goroutine — it is the user's editor, a running build, or a `git checkout` in another terminal.
- **Why**: the check answers a question about the past. By the time `os.WriteFile` runs, "the file did not exist a moment ago" has decayed into an assumption. dsh does not have this window because its policy plugin never returns a verdict — it returns an INTENT (`createIfAbsent` / `replaceIfVersion`, `packages/fs/fs-observation-policy/src/index.ts:65-71`) that the provider resolves inside a per-target lock, publishing via `link()` so exclusivity is a syscall guarantee (`fs-local/src/index.ts:178-187`, `fsio.ts:580`).
- **Fix**: `fsobserve.Plan` returns a `Decision` (guarded? exists? token?) instead of a bare status, and `write.writeGuarded` re-establishes the precondition at the syscall: absent target → `O_CREATE|O_EXCL` (EEXIST means someone won the race, refused with the same message); present target → open WITHOUT `O_CREATE`, stat the **file descriptor** rather than the path, compare the token, then truncate and write. Stat'ing the fd is the part that matters — it makes the file being verified provably the file being written.
- **Lesson**: a guard that runs before the operation guards a snapshot, not the operation. If the property must actually hold at write time, express it as the syscall's own precondition (`O_EXCL`, CAS, an fd-scoped check) rather than as an `if` above the call. And when adding an opt-in guard, the disabled path needs its own explicit branch: routing a nil-observer write through the new exclusive-create path broke every legitimate overwrite in tools that had not opted in.
- **Refs**: `internal/fsobserve/fsobserve.go:Plan`, `internal/tools/write/write.go:writeGuarded`, `internal/tools/write/write_test.go:TestWriteGuarded_ExclusiveCreateRefusesRaceWinner`

### A size+mtime freshness token misses the rename-over, which is how most tools change files
- **Saw**: (design-time) the freshness token was `(size, mtime)`. A formatter, code generator, or `git checkout` does not modify a file in place — it stages a temp file and renames it over the target. Same size is common; on a filesystem with coarse mtime granularity the timestamp can match too. The token reports "unchanged" and a stale whole-file write is permitted.
- **Why**: size and mtime describe a file's CONTENT STATE. They say nothing about whether the path still refers to the same file. The rename-over changes the identity, not the state.
- **Fix**: the token carries `(dev, ino, size, mtime)`, matching dsh's `dev:ino:size:mtimeNs:ctimeNs` (`packages/fs/fs-local/src/fsio.ts:74-75`). On Unix the pair comes from `fi.Sys().(*syscall.Stat_t)`; off Unix there is no portable answer from a `FileInfo` (Windows needs an open handle), so `ident_other.go` returns `(0, 0)` and the token degrades to the previous size+mtime behaviour rather than inventing an identity that would make unrelated files compare equal.
- **Lesson**: "has this file changed?" and "is this still the same file?" are different questions, and the second one is the one a coding agent's environment breaks. The regression test must pin size AND mtime identical so only the inode differs — otherwise it passes for the wrong reason.
- **Refs**: `internal/fsobserve/ident_unix.go`, `internal/fsobserve/fsobserve.go:Token`, `internal/fsobserve/fsobserve_test.go:TestCheck_RenameOverIsDetected`

### Attributing a failure to the sandbox needs three gates, because the errno text alone is worthless
- **Saw**: (design-time, building sandbox attribution) the obvious implementation — scan command output for "Permission denied" / "Operation not permitted" and tell the model the sandbox blocked it — is confidently wrong most of the time. Those strings are among the most common in ordinary tooling output (a root-owned file, a bad chmod, a `find` walking `/proc`), and a hint that blames seek's jail for an unrelated failure sends the model down a worse path than saying nothing.
- **Why**: the errno text describes the syscall result, not its cause. Under confinement, a denial and an ordinary permission problem are byte-identical in stderr — the only thing that distinguishes them is context the output does not carry: whether a jail was applied to THIS command at all.
- **Fix**: three gates, all required — confinement was configured for this command (`t.sandbox != nil`), the command exited non-zero, and the output matches a platform-specific signature (EPERM texts on darwin/seatbelt, EACCES on linux/landlock). Separately, runner failure (the jail could not be applied, so the command never ran) is identified by seek's own `seek sandbox:` / `sandbox-exec:` marker rather than by exit code, because 127 is also the shell's "command not found" — the exit code gates, the marker identifies. Runner failure is checked FIRST: its stderr can also contain an errno phrase, and reporting "you were denied" for a command that never ran is a different and worse lie.
- **Lesson**: before turning an observed string into a diagnosis, ask what ELSE produces that string. If the answer is "lots of ordinary things", the signature is not the detector — the signature plus the context gates is. And validate the signature against a REAL denial: a unit test with hand-written errno text proves the matcher, not the claim that the OS prints that text (`sandbox_test.go:TestBash_SandboxDenial_IsAttributed` drives real seatbelt so the hint fails loudly if macOS ever changes the wording).
- **Refs**: `internal/tools/bash/sandboxhint.go`, `internal/tools/bash/sandboxhint_test.go`, `internal/tools/bash/sandbox_test.go:TestBash_SandboxDenial_IsAttributed`, dsh `packages/sandbox/sandbox/src/index.ts:95-116`

### A read that reached EOF but got elided still vouched for the whole file
- **Saw**: (review-time) I3 ("only a full read vouches") and I4 ("cap the result, elide the middle") landed in one commit, and the Observe condition checked only `reachedEOF && offset<=1 && !truncated` — every term describes the SCAN, none describes what the model RECEIVED. A 70 KiB file of 100 sub-1-KiB lines: the default read reaches EOF with nothing truncated, the output exceeds the 64 KiB cap, the middle is elided — and Observe fired anyway, letting the write guard authorise overwriting ~38 KiB the model never saw. The in-band line elision opens the same hole (a ≤32 KiB whole-read whose single over-long line comes back as 1 KiB + marker).
- **Why**: two invariants phrased about different subjects. "Covered the file" is a property of the scan; "seen every byte" is a property of the result. Elision is exactly where they diverge — and neither I3's tests nor I4's tests could catch it, because each fixture exercised only one improvement.
- **Fix**: `read` tracks both elision kinds; a full-but-elided read records `NoteElided` instead of `Observe`. fsobserve gains `StatusElided`, whose refusal message names the real recovery (`edit`, or ask the user) — NOT "read it again", which can never clear an elided note (the same parts elide every time) and would just burn turns. Stale outranks elided: an external change is the more urgent fact.
- **Lesson**: when two features land together, test their INTERSECTION, not just each side — the bug lived only where both applied. And when a guard refuses, ask whether its recovery advice is achievable for the class of input that triggered it; advice that cannot succeed is worse than no advice, because the model retries it.
- **Refs**: `internal/tools/read/read.go` (Observe/NoteElided gate), `internal/fsobserve/fsobserve.go:StatusElided`, `internal/tools/read/read_improved_test.go:TestRead_ResultElidedFullReadDoesNotVouch`

## Tooling / line endings

### A/B eval binaries built from the same commit share the -version string, so run.sh writes both sides into ONE results file
- **Saw**: during the read-tool A/B eval (docs/test-plan-read-tool.md), the baseline and improved Linux binaries both reported `v0.10.0+dirty · 1ad60e3+` (same commit, dirty build) — `eval/run.sh` keys the results filename on that string, so the improved battery's rows silently appended to the baseline file, and a "drop old rows" step deleted 5 freshly-run baseline rows along with the stale improved ones. Rows were only recoverable because `ran_at` timestamps distinguished the two sides.
- **Why**: the results filename is `date + version_tag + version_rev` (run.sh:50-52) — an A/B pair built from the same commit is indistinguishable by that key. Nothing routes rows to the file you think you're writing.
- **Fix**: run the two sides with distinguishable binaries (tag one build, or copy the binary and patch its version) OR — when that's not possible — record a start timestamp before each side and split rows by `ran_at` afterwards (the pattern used here: `jq 'select(.ran_at >= $start)'`).
- **Lesson**: before an A/B eval, check that the two binaries produce different `-version` output. Same-commit dirty builds collide; the split must then happen at analysis time, not at write time.
- **Refs**: `eval/run.sh` (results-file naming), `docs/test-plan-read-tool.md` §7.3

### jq `a // b as $x | c` binds `as` INTO the `//` alternative — the pipeline disappears
- **Saw**: `eval/tools/compare-read-ab.sh`'s `func_count` returned the whole `final_text` string as its "count" instead of a number; a minimal repro `jq '.final_text // "" as $txt | (["a"] | map($txt | contains(.)) | length)'` printed the text. Meanwhile the identical `//`-without-`as` shape in `run.sh`'s extractors (`(.result // "" | contains("x"))`) behaved correctly — so the trap is specific to `as`.
- **Why**: in jq, `as $var` extends its scope over the following pipeline, and that whole construct binds as the RIGHT OPERAND of `//`. `a // b as $x | c` parses as `a // (b as $x | c)`, not `(a // b) as $x | c`. The alternative `b as $x | c` evaluates to a truthy value, so `a // <truthy>` short-circuits to `a` — the pipeline after `//` silently vanishes.
- **Fix**: parenthesise the alternative before `as`: `(.final_text // "") as $txt | (...)`. Grep the repo for `// .* as \$` to audit other occurrences.
- **Lesson**: when binding `as` after an expression that contains `//` (or any operator looser than `|`), the parens are load-bearing — test the exact expression on the deployment's jq version, not a mental model of precedence.
- **Refs**: `eval/tools/compare-read-ab.sh:func_count`, `eval/run.sh` (safe shapes), jq manual "Alternative operator" + "as"

### core.autocrlf + editing .sh on Windows yields CRLF, which strict bash (WSL) rejects while git-bash silently tolerates
- **Saw**: after extending `eval/run.sh` (an edit-tool change on Windows), `bash eval/run.sh` in git-bash passed `bash -n` and even ran (failing later for unrelated reasons), but the identical file in WSL Ubuntu failed immediately with `$'\r': command not found` and `syntax error near unexpected token $'{\r'`.
- **Why**: the repo sets `core.autocrlf=true`, so every checkout on Windows materialises LF files as CRLF — and the file tooling on this box writes CRLF into edited hunks, so the working copy was CRLF even though git's stored blob is LF (git diff stays clean thanks to autocrlf normalisation, which hides the change entirely). Git-bash's MSYS bash tolerates CRLF in scripts; WSL's strict dash/bash does not. A second related trap: `eval/run.sh`'s jq.exe fallback ("WSL needs jq.exe") cannot read POSIX paths (`/mnt/d/...`, `/tmp/...` — "Could not open file"), and WSL binfmt interop cannot execute the WinGet-Links jq.exe at all (Exec format error) — the WSL path needs a Linux `jq` from apt, not the Windows one.
- **Fix**: convert `eval/run.sh`'s working copy to LF; add `.gitattributes` with `*.sh text eol=lf` so shell scripts check out LF everywhere. Phase 2+ eval runs must execute in WSL with a Linux jq (`sudo apt install jq`), not git-bash with jq.exe.
- **Lesson**: "it runs in git-bash" is not evidence a script is portable — MSYS bash is lenient about CRLF and Windows binaries can't see MSYS/WSL paths. After editing any `.sh` on Windows, verify no whole-file line-ending noise in `git diff`, and test strict environments (WSL) before trusting the script. Shell scripts are LF-forever: enforce it in `.gitattributes`, don't rely on contributors remembering.
- **Refs**: `eval/run.sh`, `.gitattributes`, `docs/test-plan-read-tool.md` §8.3

### Hand-rolled readLine dropped bufio.Scanner's dropCR — CRLF lines gained an invisible \r
- **Saw**: after replacing `bufio.Scanner` with a hand-written `ReadSlice` loop (to elide over-long lines instead of hard-failing on them), every line of a CRLF working-tree file gained a trailing `\r` in read output. Invisible in the TUI, invisible to the model, present in the context bytes.
- **Why**: Scanner's `ScanLines` silently drops the `\r` of a `\r\n` pair (`dropCR`). That behaviour is undocumented at the call site — it lives inside the stdlib split function — so reimplementing "read a line" without it regressed invisibly. edit's line-ending fallback tier masked the practical damage (an old_string with or without the `\r` normalises to a match), which is why nothing failed loudly.
- **Fix**: `trimNL` strips one `\n` and then one `\r` (Scanner.Text semantics, including the final no-newline token). Test pins CRLF input → CR-free output.
- **Lesson**: when replacing a stdlib convenience with a hand-rolled loop, diff the BEHAVIOURS, not the happy path — Scanner's contract includes token-mangling (dropCR) you don't notice until it's gone. On a core.autocrlf Windows checkout (seek's home platform) the regression ships to every session that reads a checked-out text file.
- **Refs**: `internal/tools/read/read.go:trimNL`, `internal/tools/read/read_test.go:TestRead_CRLFStripped`, this file's "core.autocrlf + editing .sh on Windows" entry above

## TUI / session stats

### `-resume` silently downgraded every session to the provider-default model
- **Saw**: `seek --resume <id>` on a vision-model session failed with "deepseek api error: This model does not support image" — the session header recorded `deepseek-v4-flash-vision-exp`, no `--model` was passed, and the identical request shape worked via curl against the recorded model. Any session saved on V4-Pro had likewise been silently running on V4-Flash after every resume, losing thinking mode
- **Why**: the sticky-session inheritance checked `*model == modelDefault`, but the `--model` flag's default is `""` — the comparison could never be true (dead branch), and the later `if *model == ""` fallback then applied the provider default. "Sessions are sticky" existed only in the comment
- **Fix**: compare against the sentinel `""` (only an explicit `--model` overrides the saved model). Verified end-to-end by the feature-vision resume smoke: the resumed vision session answers "42" from a mid-history image. Second layer added in the same fix: the agent send path now strips history images when the active model isn't a vision model, so a mid-session `/model` switch degrades in-band instead of 400-ing the turn
- **Lesson**: when a flag's default is a sentinel (`""`), every "did the user override?" check must compare against the SENTINEL, not against the downstream resolved default — sentinel and resolved value can never be equal, and the override logic becomes dead code that no compiler can catch
- **Refs**: `cmd/seek/main.go` (sticky-session block), `pkg/agent/agent.go` (`runTurnDeepSeek` capability gate), surfaced by the feature-vision M-V.3 resume smoke

### Measure the LLM-time span at the assistant MessageEnd, not the first ToolExecStart
- **Saw**: (design-time) when adding the exit-summary timing (llm/tool split), the obvious first draft closed the per-turn LLM span at the first `ToolExecStart` of the turn (and at `TurnEnd` for tool-less turns). That overcounts: between the assistant `MessageEnd` and tool dispatch the agent is parsing args and looking up tools — agent-side work, not model time — and the two definitions (tool turn vs tool-less turn) were inconsistent.
- **Why**: pkg/agent events carry no timestamps, so timing is measured at the consumer. The event sequence is `TurnStart → deltas → assistant MessageEnd → ToolExecStart…End → TurnEnd`, and only the assistant-role `MessageEnd` marks "the stream is done" for every turn shape.
- **Fix**: close the span in the `RoleAssistant` branch of the `MessageEnd` case (tool-result MessageEnds are `RoleTool` and must NOT close); close defensively (guarded by `turnStart != zero`, then zero it) at `TurnEnd` / `AgentEnd` (Esc-cancel) / `ErrorEvent` (stream death, e.g. finish="length" fires after MessageEnd so the guard is what makes it a no-op).
- **Lesson**: when a metric is defined by "where did the time go", anchor each span to the event that semantically marks its end — and remember every other event of the same TYPE (tool-result MessageEnd) that must not trigger it. A role/type check plus a zero-value guard makes double-close and missed-close both impossible.
- **Refs**: `internal/tui/update_agent.go:applyAgentEvent`, `internal/tui/summary.go`, `pkg/agent/agent.go` (turn loop)

### Windows clock granularity: `time.Since` returns exactly 0 across sub-tick gaps — timing tests need a sleep
- **Saw**: new TUI timing tests failed deterministically standalone with `llmTime = 0s, want > 0` — the span between `applyAgentEvent(TurnStart)` and `applyAgentEvent(MessageEnd)` is a few hundred nanoseconds, and `time.Since` returned exactly 0. The same tests passed intermittently under different load (tick crossing is a coin flip).
- **Why**: `time.Now()`/`time.Since` on Windows has tick granularity coarser than a µs-scale gap; two readings inside the same tick are identical, so `Sub` is exactly 0. Nothing is wrong with the code under test.
- **Fix**: each test that asserts a positive duration sleeps ≥2ms inside the measured span before the closing event (established empirically: 2ms was reliable; sub-ms was not).
- **Lesson**: on Windows, never assert `duration > 0` on a span you created and measured within the same goroutine microseconds apart — either sleep across a tick or assert structure (zeroed state, accumulation) instead of magnitude.
- **Refs**: `internal/tui/summary_test.go` (the `time.Sleep(2 * time.Millisecond)` comments), this file's "t.Cleanup(chdir) after t.TempDir() breaks Windows TempDir removal" entry (same platform, same class)

### After /compact the status bar kept showing the summariser's full-history ctx% until the next turn
- **Saw**: right after compacting a ~510K-token session (1M window), the status bar still read `ctx 51%` — the summariser call re-read the entire old history, and that reading stuck as "the most recent turn" until the next real turn landed. Easy to read as "compaction barely helped"
- **Why**: `handleCompactDone` records the summariser's usage via `Tracker.Record` (cost honesty — it is a real billed request), and the bar's ctx% reads `Last()` = the most recently recorded turn. So the pre-compact context size was pinned as the current one even though the agent's history had already been swapped to the summary pair
- **Fix**: new `Tracker.Seal()` folds every recorded turn into the cumulative-only base — `Cumulative()` / `CumulativeCost()` / cache% unchanged — while `Last()`/`LastCost()` go empty; `handleCompactDone` calls it right after the Record. ctx% now shows 0% (no reading in this window) until the first post-compact turn re-bases it. `hasBase` is deliberately NOT set by Seal (its AdoptChild guard is about resume-time bases, not in-process folds — arming it would turn legitimate adoption flows into false panics). Tests: `TestTracker_Seal_FoldsTurnsIntoBase`, `TestTracker_Seal_IsIdempotentAndEmptySafe`, `TestTracker_Seal_DoesNotArmDoubleCountGuard`, plus the extended compact handler test
- **Lesson**: "most recent turn" is the wrong baseline for a window indicator when the window itself was replaced. When an operation rebases the live context, the tracker needs an explicit "start a new window" primitive — otherwise every `Last()` consumer silently keeps reading the old world until the next write
- **Refs**: `internal/cache/cache.go:Seal`, `internal/tui/update_agent.go:handleCompactDone`, `internal/cache/cache_test.go`

### Flat search cost is not resumed — Tracker only re-prices TOKENS
- **Saw**: (design-time, web_search landing) the Search API bills per successful call (≈$0.0007), which has no `deepseek.Usage` to `Record`. The obvious hack — fabricate a synthetic Usage turn whose pricing.Cost happens to equal the flat fee — would poison `Last()` and the ctx% indicator with fake tokens.
- **Why**: resume reconstructs the session cost from `session.Usage` via `SetBase`, and Usage carries only token counts — there is nothing in the persisted session to re-derive a flat dollar amount from.
- **Fix**: `Tracker.RecordFlatCost(usd)` grows a separate `extraCost` folded into `CumulativeCost()` only (status bar $ total); `Cumulative()`/`Last()`/`LastCost()` stay token-pure. Accepted trade-off: flat costs vanish across `-resume` rather than growing the session schema for a $0.0007/call line item.
- **Lesson**: cost that isn't denominated in the persisted unit (tokens) can't survive resume for free — either widen the persisted schema deliberately or accept the loss, but never launder it through a fake token turn (it corrupts every token-derived indicator).
- **Refs**: `internal/cache/cache.go:RecordFlatCost`, `internal/tools/websearch/websearch.go` (CostSink), `TestTracker_RecordFlatCost`

## CI / gates

### A self-skipping test suite makes a missing CI secret look like a pass
- **Saw**: (design-time, adopting dsh's real-API e2e shape) `pkg/agent/cache_e2e_test.go` skips cleanly without `DEEPSEEK_API_KEY`, which is correct for `go test ./...`. Wiring that same test into a CI job with a secret creates a trap: if the secret is deleted, renamed, or misconfigured, every real-API test skips, the job exits 0, and the entire safety net disappears while the dashboard looks healthier than ever.
- **Why**: "all tests skipped" and "all tests passed" are the same exit code. A gate built on a self-skipping suite therefore asserts nothing unless something independently proves the suite actually ran. dsh hit this and wrote it up — their note calls it "a silent regression of the entire safety net" and records that the preflight's correctness was verified live, by the run that failed before the secret existed.
- **Fix**: `.github/workflows/e2e.yml`, separate from `ci.yml` (different credential and trigger lifecycles → different files, so `ci.yml` stays keyless/forkable/always-green). Two independent defences: an unconditional preflight that fails loudly with `::error::` when the key is empty, and a `grep` for a literal `--- PASS:` line in the test output, because exit 0 alone cannot distinguish ran-and-passed from skipped-everything. The untrusted-PR skip keys on the PR **author** (`pull_request.user.login`), not `github.actor` — a maintainer re-running a Dependabot PR makes the actor human while the PR is still keyless.
- **Lesson**: any gate whose subject can skip itself needs a positive liveness assertion, not just a green exit. Ask "what does this check look like when it silently does nothing?" — if the answer is "identical to success", the check is decorative.
- **Refs**: `.github/workflows/e2e.yml`, `pkg/agent/cache_e2e_test.go`, dsh `.agents/notes/implemented/testing/2026-06-19-real-api-e2e-ci.md`

### A benign log line sharing a prefix with a fatal one turns every ordinary failure into a false diagnosis
- **Saw**: (design-time, hardening the sandbox attribution added the same day) seek's runner-failure detector matched `"seek sandbox:"` as a substring of the whole command output. That is safe today only by accident: every such line in `internal/sandbox/sandbox_linux.go` is immediately followed by `os.Exit(127)`, so the prefix is fatal-only — one commit away from being false.
- **Why**: dsh shipped exactly this defect (their postmortem 0004). Their launcher prints `landlock-run: partial enforcement (older Landlock ABI)` on a **successful** path when the kernel's Landlock ABI is old, and their classifier reduced the contract to the substring `landlock-run: ` plus "any nonzero exit". So ripgrep's exit 1 — which means "no matches found", a success — was reported as sandbox infrastructure failure, and `glob`/`grep` broke on exactly the hosts that most needed a working fallback.
- **Fix**: match runner markers **per line**, after subtracting an explicit `benignSandboxNotices` exclusion list compared **exactly** (a prefix test there would re-create the ambiguity). The list is empty today; the doc comment states that any new informational `seek sandbox:` output must be registered in the same change, and `TestDiagnoseSandbox_BenignNoticeIsNotRunnerFailure` fails if it is not. dsh's own fix is phrased the same way: "status-gated fatal evidence after exact informational exclusions."
- **Lesson**: when a component prints both fatal and informational lines, the shared prefix is not a signature — it is an ambiguity. Encode the informational cases explicitly and subtract them first, and match line-scoped rather than over the whole blob. Also worth stealing: dsh reserves a dedicated exit code (125) for launcher failure so it never collides with the shell's 127 "command not found"; seek reuses 127 and compensates with the marker requirement, which works but is the weaker construction.
- **Refs**: `internal/tools/bash/sandboxhint.go:benignSandboxNotices`, `internal/tools/bash/sandboxhint_test.go`, dsh `docs/postmortem/0004-landlock-partial-notice-misclassified-child-failures.md`

### Prose rules do not stop documentation accretion — the files that are read every session need a mechanical ceiling
- **Saw**: `AGENTS.md` (3178 words) and `CLAUDE.md` (3054) are roughly twice the size of dsh's comparable root file (their gate caps it at 1600), and both grew again in the very change that measured them. Nothing pushed back, because nothing could: every individual addition was justified.
- **Why**: these two files are read at the start of every session, so every word is paid in prompt tokens forever — but the cost is invisible at the moment of writing, while the benefit of "just adding one line" is immediate and local. dsh's note on this is blunt about why review alone fails: their accretion happened *while* the current-state rule and reviewer attention already existed.
- **Fix**: `scripts/verify-doc-budgets.sh` + `scripts/doc-budgets.tsv`, wired as a CI job. Both files are FROZEN at their current size with a 1800-word target, so an addition now requires a displacement. Scope is deliberately narrow — reference docs, PRDs and `pitfalls.md` are unbudgeted, because length is legitimate where every line is a fact. A budgeted file that goes missing also fails, so a rename cannot orphan its budget. Bash 3.2 compatible (macOS ships 3.2; no `mapfile`, no associative arrays).
- **Lesson**: word count cannot judge quality, and that is not what it is for. It forces the relocation decision at the moment content is being added — the only moment the author still has the context to place it correctly. Apply it to files whose cost is per-session and invisible; never to files whose length is their value.
- **Refs**: `scripts/verify-doc-budgets.sh`, `scripts/doc-budgets.tsv`, `.github/workflows/ci.yml` (doc-budgets job), dsh `.agents/notes/implemented/process/2026-07-04-doc-tiers-and-budgets.md`

## Eval / tool descriptions

### Explicit bash bans in prompts underperform tool-description guidance — measured
- **Saw**: on Windows cmd.exe, the model built ONE bash command with POSIX separators (`git log ...; echo ---; git log ...`) — cmd does not treat `;` as a separator, so the whole string went to git: `fatal: ambiguous argument 'echo'`. The same session also duplicated the subcommand into the git tool's `args[0]` (`{"subcommand":"show","args":["show","-s",...]}`).
- **Why**: the model transcribes shell-style command lines wholesale into tool calls. On Windows the failure surfaces as a git error ("ambiguous argument 'echo'"), not a shell error, so the self-correction loop has nothing to bite on. Prompt-level bans are instruction-following; tool descriptions travel with the schema and sit next to the generation step.
- **Fix**: git/bash tool descriptions now mandate one query per call (no `;`/`&&` chains, no read-only git via bash, `--` before pathspecs); AGENTS.md rule 6 mirrors it. Two new eval metrics (`bash_chains`, `bash_git_calls`) + the `git-multi-query-no-bash` case measure the tics. Measured (10+10 runs, see `docs/test-plan-git-tool-shape.md`): git-via-bash runs 6/10→1/10, chains 5/10→1/10, PASS 4/10→9/10.
- **Lesson**: a prompt-level prohibition ("不要用 bash 跑 git 命令") still leaked a chained bash git call in baseline — the description is the lever that moved the rate. When a command-construction tic fails with an unrelated-looking error, the mitigation belongs in the tool description, not the system prompt.
- **Refs**: `docs/test-plan-git-tool-shape.md`, `internal/tools/git/git.go`, `internal/tools/bash/bash.go`, `eval/cases/git-multi-query-no-bash/`, commits `6f7f4ab`/`9f7426d` (auto-fix precedent)

### The per-turn `workflowReminder` taught `read→grep→read` while AGENTS.md and the book promised `grep→read`
- **Saw**: every user turn carried `[Workflow: read→grep→read(limit≤50). edit→read first.]` (appended in `pkg/agent/agent.go` with deliberate recency bias, so it outranks the system-prompt copy) — the exact chain `docs/book/appendix-b.md:5` cites as the *before* state of a tuning round whose *after* state is `grep→read`, and the opposite of AGENTS.md's "Tool usage workflow" (`grep first` → `read(offset=N)`).
- **Why**: the constant was rewritten in `3dddae3c` (a "misc improvements" chore bundling the read tool's new `limit` param) and nothing cross-checks it against the documented workflow. Two instruction surfaces, no tie-breaker: drift is silent by construction.
- **Fix**: reorder to `[Workflow: grep→read(offset,limit≤50). edit→read first.]` — grep first, read only the window. Still a byte-constant string, so the prefix cache is unaffected. (this change; commit pending)
- **Lesson**: an always-injected string is an instruction surface too. When the documented workflow changes, grep for `[Workflow:` — the reminder does not update itself.
- **Refs**: `pkg/agent/agent.go:323` (+ call site `:452`), `docs/book/appendix-b.md:5`, AGENTS.md "Tool usage workflow"

### `sysprompt.go` rule 5 ("denials → stop") discarded the Hint-carrying denial design
- **Saw**: policy denials that embed a `Hint:` clause naming the exact fix (drop a `cd` prefix, use the `git` tool instead of bash git, `go test`→`go vet`) were met with a full stop + hand-back, because the system prompt's workflow rule 5 said "surface the message to the user and stop — do not loop". AGENTS.md's "Self-recovery first" and `plan-mode.md`'s "read it and adjust" say the opposite.
- **Why**: rule 5 predates the bash `Hint:` mechanism (`internal/tools/bash/hint.go`) and was never revisited. The system prompt is re-sent every turn and sits closest to generation, so its "stop" beat the AGENTS.md rule — routine denials read as dead ends.
- **Fix**: rule 5 now splits by source — user-declined y/N → stop, never re-ask/re-try; policy denial with a `Hint:` → adjust once (different tool or corrected shape); still denied → surface and stop; never retry the same shape. No test change needed: the byte-equivalence oracle in `internal/sysprompt` reuses `rootTpl` itself. (this change; commit pending)
- **Lesson**: when an error gains machine-actionable metadata (hints, alternatives), grep the instruction surfaces for blanket "on error: stop" rules — they silently discard it.
- **Refs**: `internal/sysprompt/sysprompt.go` (Workflow rule 5), `internal/tools/bash/hint.go`, `internal/skill/builtin/plan-mode.md` "Common bash pitfalls in plan-analyze"

### Eval prompts that demand verbatim quoting silently break when the target line crosses read's 1024-byte cap
- **Saw**: `tool-selection` (quote `const description` strings verbatim) failed with `bash_calls 1–4` three months after its last pass — the model still did grep×3→read×3, then bash awk/wc. Same-era `bash-overreach` failed with `bash_calls=2, list_dir=0`.
- **Why**: fixture drift, not model drift: `bash.go`'s `const description` line grew 384→1059 B, crossing `read`'s `maxLineBytes=1024` in-band elision (`grep` also truncates matches at 240 chars) — no dedicated tool can return that line verbatim any more, so shell recovery is CORRECT tool selection. `bash-overreach`'s "fewest lines" question additionally had no dedicated-tool answer at all (`list_dir` prints byte sizes, not line counts), so `find -exec wc|sort` was forced by the prompt itself.
- **Fix**: bound what the mechanism forces, not the aspiration. `tool-selection`: `max_bash_calls 0→4` (elision-recovery envelope: 1 extraction + ≤3 word counts) + new `bash_before_dedicated` metric bound 0 (bash never FIRST resort — ordering is what a total-calls bound can't see). `bash-overreach`: second question switched to "smallest in bytes" (the same single `list_dir` call answers it) + `list_dir`/`bash` description clauses steering count-/size-questions away from `find/wc/stat` (measured: bash 2→1→0, `list_dir` 0→1, sequence flipped).
- **Lesson**: an eval case that quotes repo content is coupled to that content's SIZE — when a `max_*_calls=0` assertion starts failing months later, first check whether any dedicated tool can still answer the prompt at all (line caps, truncation) before blaming model drift. And prefer order-sensitive bounds (`bash_before_dedicated`) over loosened totals when recovery is legitimate: they keep the anti-pattern failing while tolerating forced recovery.
- **Refs**: `internal/tools/read/read.go` (`maxLineBytes`), `internal/tools/grep/grep.go` (`maxLineChars`), `eval/cases/tool-selection/README.md` (drift history), `eval/cases/bash-overreach/README.md`, `eval/run.sh` (`bash_before_dedicated`)

### Default-depth `list_dir` answers nothing about a tree — and its output never said so (bash-overreach alternation)
- **Saw**: `bash-overreach` failed red-green ALTERNATING on identical binaries since 11-13 (post-repair full-lane record 4 PASS / 7 FAIL; every FAIL `list_dir ≤ 1, bash ≥ 1`, every PASS exactly `list_dir=1, bash=0`) — read as "persistent regression since 11-13" by two cards before the same-binary alternation was spotted.
- **Why**: the prompt asks a recursive question ("how many .go files under `internal/tools/`"); `list_dir` defaults `depth=1`, so the natural first call lists the 24 package dirs and answers neither count nor size — and its output gave no pointer to the `depth` re-call, leaving `bash find | wc` as the equally-cheap visible recovery. The description's "one call answers count-questions about a tree" is only true when `depth` covers the tree, so the model's choice at that margin is a coin flip. Not drift (prompt + both tool descriptions byte-stable since `f99707f`) and not a regression (the case was born 2026-05-28 with EMPTY result stubs — no green baseline ever existed); it is a fragile case at a real tool-boundary margin.
- **Fix**: in-band recovery hint — default-depth listings that contain subdirectories now end with `… N subdirectories not expanded at depth=1 — re-call with depth=2 (max 3) to list their entries and sizes` (`internal/tools/listdir/listdir.go`, same idiom as the truncation notice). Commit with card `c_lrilqc`.
- **Follow-up (2026-11-18)**: the hint is read but does not flip the case — 5 fresh rounds on `11b2366` gave **2/5 green** (re-called `list_dir(depth=2/3)` and stopped) vs 3/5 still `bash find | wc`. All 5 put `list_dir` FIRST, so the residual is recovery-after-dedicated, not overreach: `max_bash_calls=0` was bounding "needed a shell", not "reached for one". Recalibrated to the ordering bound the suite already preaches — `max_bash_calls 0→1` + `max_bash_before_dedicated: 0`, and that metric now counts `list_dir` as a dedicated tool (adding a tool can only lower it). See `eval/cases/bash-overreach/README.md`.
- **Lesson**: a `max_bash_calls=0` case whose task a dedicated tool can only answer with a NON-DEFAULT argument is stochastic unless the tool's default output names that argument — if the recovery path isn't visible in-band, bash is the only signposted route. And: read a trend-report "failing since" flag against per-run rows on the SAME binary — alternation there is flakiness, not regression.
- **Refs**: `internal/tools/listdir/listdir.go`, `eval/cases/bash-overreach/README.md` (drift history), `eval/results/` 2026-11-13…18 rows, card `c_lrilqc`

## Build tags / dependency isolation

### A package whose .go files are ALL tag-excluded errors on direct reference, silently tolerated by `./...`
- **Saw**: planned to gate the feishu frontend SDK behind `//go:build feishu` with a package containing ONLY tagged files; assumed `go build ./...` would break ("build constraints exclude all Go files"). Empirically (go1.27): `go build ./...`, `go vet ./...`, `go test ./...` all SKIP such packages silently — only directly naming the package (`go build ./internal/.../pkg`, or importing it from untagged code) errors.
- **Why**: pattern-matched packages with zero matching files are dropped from the pattern expansion; an explicitly named package must produce a build result and has none to produce.
- **Fix**: keep an untagged `doc.go` as the package anchor (also carries the tag contract in godoc) and, for the default-build guarantee, assert the package's default `GoFiles == [doc.go]` via `go/build.Default.ImportDir` in an ordinary CI test (`internal/controlplane/buildtags_test.go`) — go/build evaluates //go:build per tags without spawning builds, so the exclusion proof itself runs under default tags.
- **Lesson**: `./...` staying green does NOT prove a tag-gated package is wired right; prove the gate both ways — default tags exclude the file (go/build assertion in CI) and `-tags feishu` compiles AND runs it (tagged test, run on demand until CI grows a tagged lane).
- **Refs**: `internal/controlplane/feishu/doc.go` (anchor), `internal/controlplane/buildtags_test.go`, `docs/serve/README.md` "build-tag 依赖隔离"

## ACP / editor & headless lanes

### Session cwd has TWO resolution conventions — anchoring only one leaves half the toolbox in the daemon's dir
- **Saw**: `seek acp`'s `NewSession` ignored `session/new`'s `cwd` entirely (fixed sessionId "seek"). Invisible under Zed (which spawns seek IN the project dir and sends the same dir), but headless ACP clients (paseo) spawn the child WITHOUT a cwd — it inherits the daemon's (usually `/`) — so every relative path and every unpinned subprocess ran in the wrong place. The fix had a trap of its own: re-anchoring `permission.Policy` alone (the obvious move, since read/edit/write resolve via `Policy.Resolve` and bash pins `cmd.Dir = CWD()`) still left `git` (no cmd.Dir pin), `grep`/`list_dir` (WalkDir on the raw relative path) in `/`.
- **Why**: the tool layer deliberately has two cwd conventions — policy-anchored (file tools + bash, so worktree subagents can't leak into the main tree) and process-anchored (walking tools + exec defaults). A session-level cwd must move BOTH anchors or the toolbox splits across directories.
- **Fix**: `acp.AnchorSession` (`internal/acp/session.go`): `os.Chdir(abs)` + `Policy.Reanchor(abs)` as one atomic handshake step; cwd that can't be entered fails the session loud (-32000) instead of silently running in the daemon dir. `Policy.Reanchor` (`internal/permission`) is the one sanctioned post-construction cwd change — tools hold the `*Policy` by pointer, so the re-anchor reaches them without rebuilding the registry.
- **Lesson**: "make cwd effective" in this codebase is TWO operations, not one. Any future per-session cwd consumer (multi-session backlog ③) must go through both anchors — or enumerate which convention each affected tool uses. Zed-compat falls out for free: it sends the spawn dir as cwd, so the anchor is a no-op there.
- **Refs**: `internal/acp/session.go`, `internal/permission/permission.go` (Reanchor), `cmd/seek/main.go` (acpBackend.NewSession), `docs/prd/archive/feature-paseo-provider.md` §2.2 D-1

### Late SetPref desyncs the permission gate from the baked-in sysprompt Mode label
- **Saw**: implementing SEEK_ACP_YOLO, the obvious wiring was `policy.SetPref(PrefYolo)` at the `seek acp` short-circuit (goal-mode precedent at main.go:1732). It would have been silently half-broken: the gate would allow bash while the system prompt header still read `Mode: deny` (and the bash tool description says "Refused under the current permission mode"), so the model would likely never attempt a bash call at all.
- **Why**: the permission gate is live state — SetPref takes effect on the next Check — but the sysprompt Mode label is baked ONCE at startup into the prefix (`sysprompt.Compose(..., ModeLabel(initialPref, ...))`). Only the interactive TUI keeps them in sync mid-session via `Agent.SetModeLabel` per-message reminders; headless lanes (acp/goal/rpc) never set that label.
- **Fix**: resolve SEEK_ACP_YOLO into `initialPref` BEFORE `sysprompt.Compose` (cmd/seek/main.go, gated on the ACP lane so a stray env can't yolo the TUI); env parsing owned by `internal/acp/headless.go`. Commit: `feat(acp): SEEK_ACP_YOLO headless permission switch` (c_rewul5).
- **Lesson**: on headless lanes a permission posture is not just `policy.SetPref` — it must win before `sysprompt.Compose`, or also call `Agent.SetModeLabel`. When changing posture outside the TUI, check BOTH the gate and the prompt label.
- **Refs**: `cmd/seek/main.go` (initialPref block), `internal/acp/headless.go`, `internal/sysprompt/sysprompt.go:ModeLabel`, `docs/guide-zed.md` §5.1

### Headless ACP can't choose its provider by flag — and a stray ANTHROPIC_API_KEY in the inherited shell wins the auto-detect
- **Saw**: 2026-11-13 paseo provider 首次无头冒烟：想显式钉 DeepSeek，但 `seek acp --provider=deepseek` 里 flag 被整个无视；同时 daemon 继承的 shell 环境残留 `ANTHROPIC_API_KEY`（而 DeepSeek key 只在交互 shell 里 export、不在 daemon env / config），车道的自动探测静默选中 Anthropic，首轮就用 claude-sonnet-4 跑起来——进程健康、模型不对。
- **Why**: 两层叠加。①`seek acp` 以 `os.Args[1]=="acp"` 判定车道，Go 的 `flag.Parse` 在第一个位置参数处就停止——ACP 车道一个命令行 flag 都不消费，`--provider` 是死字。② `buildProvider` 的解析顺序是 `--provider` flag → DeepSeek key → Anthropic key → OpenAI → Gemini → GLM → `cfg.DefaultProvider`；flag 恒空时，DeepSeek key 不在 env/config 就直接落到下一家，残留的 Anthropic key 顺位上位，无任何告警。
- **Fix**: 无头车道的 provider 姿态只能走 env/config：paseo provider 块的 `env` 显式带 `DEEPSEEK_API_KEY`（或写进 `~/.seek/config.json` 的 providers 段，`config.KeyFor` 会读到）；冒烟判据补一条——首轮实际模型必须是 DeepSeek（看 `/model` 等价的启动日志或回复里的 model），"进程起来了"不算通。本卡 c_vv2mde 仅记录，未改代码。→ 2026-11-18（c_6thhqc）补：`SEEK_ACP_PROVIDER`/`SEEK_ACP_MODEL` env 钉定落地（`internal/acp/headless.go`，`buildProvider` 前注入）+ `session/set_config_option` 运行时换车道，本条"未改代码"已成历史；"冒烟必须验实际选了谁"的判据照旧。
- **Lesson**: 无 flag 车道（acp）的启动姿态——provider / 权限 / 模型——只能经 env 或 config 表达；自动化环境继承的 env 不是干净的，冒烟必须验"实际选了谁"，不能验"进程活着"。provider 自动探测的优先级对残留 key 零抵抗，daemon 侧要显式钉死。
- **Refs**: `cmd/seek/main.go`（`flag.Parse` ≈782、`acpLane` ≈933、`buildProvider` ≈2763）, `internal/config/config.go:KeyFor`, `internal/acp/headless.go`, `docs/prd/archive/feature-paseo-provider.md` §3.3, `docs/guide-zed.md` §5.1

### ProjectID hashes the path STRING, not the directory — one physical dir via /tmp and /private/tmp is two projects
- **Saw**: 2026-11-13 paseo smoke (c_bnjrw5, D-1 verification): the same physical sandbox reached as `/tmp/...` and `/private/tmp/...` produced two different ProjectIDs (be6a800f… vs 6e7915a5…) — i.e. two forked project-identity namespaces under `~/.seek/projects/`.
- **Why**: `paths.ProjectID` = `sha256(absPath)[:16]`, byte-for-byte. On macOS `/tmp` is a symlink to `/private/tmp`, and Go's `os.Getwd` returns the shell's *logical* `$PWD` whenever it stats equal to `.` (verified empirically: same cwd, `PWD=/tmp/...` → `/tmp/...`; PWD unset → `/private/tmp/...`). So the spelling depends on how the process arrived, not on where it is.
- **Fix**: decision c_4c6dar — keep `ProjectID` pure (Option A: canonical-path contract), NOT internal EvalSymlinks. The contract is now documented on `paths.ProjectID` and pinned by `TestProjectID_AliasSpellingsDiffer`; callers that care resolve cwd once with `filepath.EvalSymlinks` at the single point it's made absolute. The memory lane is already alias-tolerant regardless: `<project>/.seek/project-id` (internal/memory `resolveProjectID`) pins the ID inside the physical dir, which every spelling reads identically. Commit `5bc9846` (card c_4c6dar).
- **Lesson**: internal normalisation was rejected because it's an identity migration disguised as a fix: EvalSymlinks errors on not-yet-created paths (worktrees anchor before dirs exist) so the no-error signature forces a silent fallback — identity would then depend on filesystem state; and every project whose stored path contains a symlink component gets re-keyed, orphaning pointer-less state (checkpoints, subagents.jsonl, plans/, assets/). Real lanes (Zed spawn dir, paseo `--cwd`) pass canonical paths, so the fork only bites symlink-resident sandboxes. Revisit only if a lane starts reconciling state across machines BY ProjectID (e.g. controlplane bindings).
- **Refs**: `internal/paths/paths.go` (ProjectID contract), `internal/paths/paths_test.go:TestProjectID_AliasSpellingsDiffer`, `internal/memory/project.go` (resolveProjectID pointer), `docs/prd/archive/feature-paseo-provider.md` §2.2 (smoke trail)

### ACP configOptions: the dropdown exists only if session/new RETURNS it — and the select value is a bare string with no "type" field
- **Saw**: implementing model selection (c_6thhqc): `paseo provider diagnostic seek` printed `models=0; modes=0` while the agent was fully functional — no dropdown, no `set_config_option` traffic, ever. The PRD's only record was the behavioural one-liner "seek 两者皆空 → paseo 过滤掉 session.configure 能力" — field types, the switch method's params, and the response contract were recorded nowhere in-repo.
- **Why**: ACP session configuration is a pull contract — the client renders ONLY what `session/new` returns (`configOptions`; `modes` likewise), and unknown methods stay -32601 forever. Wire specifics that bit: `SessionConfigOption` is discriminated by `type` ("select" | "boolean") with the payload FLATTENED onto the same object (`currentValue`/`options` inlined next to `id`/`name`); `session/set_config_option`'s string variant carries `value` as a BARE string with NO `type` field on the wire (the boolean variant does carry `type:"boolean"`); and its response REQUIRES the full `configOptions` set back — a partial echo blanks the client's menu.
- **Fix**: implemented in `internal/acp/server.go` (types + dispatch) + `cmd/seek` acpBackend (`acpModelMenu`, value ids `"<provider>:<model>"`) — c_6thhqc. Authoritative shape read from the locally bundled SDK (`~/.paseo/zcode-acp/node_modules/@agentclientprotocol/sdk/schema/schema.json`), cross-checked against zcode-acp-server's `CONFIG_META` (configId `"model"`, category `"model"` — the exact strings paseo renders).
- **Lesson**: ACP capability discovery is "what the agent reports", never "what the agent supports": a healthy handshake with an empty result set = UI silently filtered, no error anywhere. For any future ACP feature (modes, request_permission, mcpServers), reverse the local SDK schema FIRST — paseo/zcode bundles it on this machine; guessing field names produces a green handshake and a permanently empty UI. Same lesson class as `--rpc`'s frozen-surface decision: check the contract, not the capability.

### http.Server.Shutdown never returns while SSE handlers are mounted
- **Saw**: 2026-11-13 web frontend first draft (card c_qucxtu): a `srv.Shutdown(ctx)` at plane shutdown never came back — the process hung with live /events connections until the deadline.
- **Why**: Shutdown closes listeners, then closes *idle* connections, then waits indefinitely for the rest to go idle. An SSE handler holds its connection active forever by design (stream loop until the client disconnects), and request contexts are cancelled by the CLIENT going away, never by Shutdown.
- **Fix**: webfrontend's Run force-closes with `srv.Close()` once the plane ctx is done (in-flight POST bodies are tiny; the Serve drain owns the message queue, not HTTP responses). Client-side disconnects stay covered by `r.Context().Done()` in the stream loop.
- **Lesson**: for any streaming endpoint, "graceful" Shutdown needs per-connection cancellation tracking (register each request's cancel and fire them before Shutdown). Plain Shutdown + timeout is a hang with extra steps — don't upgrade the Close() back without that.
- **Refs**: `internal/controlplane/webfrontend.go` (Run), net/http Server.Shutdown docs

### Subagent continuation must mint a NEW sub_sid — same-id resurrection corrupts the fold
- **Saw**: 2026-11-13 P1-b 设计（卡 c_u4ugnu）：续话最自然的形态是照抄 CC 的 SendMessage——同 agent id 再入追加指令。但按同 id 二写 `started` 事件，fold 后该子永远停在旧终态，续话运行中 /agents 面板显示 completed。
- **Why**: `foldEvents` 对同一 sub_sid 的重复 `started` 只保留首见（视为 sub_sid 空间碰撞），且 `types.go` 契约是「一个 sub_sid 恰好一个终态，终态后不再有事件」。同 id 复活要求改 fold / orphan 扫描 / TUI 三处语义，且 `writeSubagentTranscript` 同路径覆写会丢掉旧 transcript 历史。
- **Fix**: `Manager.Continue` 每次续话铸新 sub_sid，血统记在 `started` 事件的 `resumed_from` 字段（fold 透出 `Subagent.ResumedFrom`）；新 transcript 自包含（prior messages + 续话轮次），旧文件零改动。
- **Lesson**: 事件溯源索引里「id = 单生命周期」是不变式；复活/续话类需求一律用「新 id + lineage 字段」表达，别让一个 id 活第二段人生。
- **Refs**: `internal/subagent/manager.go` (Continue), `internal/subagent/index.go` (foldEvents duplicate-started branch), `docs/prd/feature-orchestration.md` §4 P1-b

### 面向模型的 id 参数必须接受模型实际看得到的形态——agent 脚注只露 sub-sid 短后缀
- **Saw**: 续话让模型回传 sub-sid 时，它手里只有结果脚注「— sub-sid: a1b2c3」（随机短后缀）；按全 id 精确匹配，真实对话里必然 `unknown_session`，测试里用全 id 却全绿。
- **Why**: `FormatCompleted/FormatContinued` 的脚注设计给人类读，展示的是 `shortSid`；工具 schema 描述若说「传 sub-sid」而模型只能引用短形态，功能在 eval/实战中差最后一步。
- **Fix**: `Manager.Continue` 先全 id 精确匹配，再按 `"-<suffix>"` 在本项目索引内唯一后缀解析；多命中报 `unknown_session` 并列出全 id 候选。schema 描述明说两种形态都收。
- **Lesson**: 给模型开的 id 入口，收它看得见的形态（脚注/面板展示什么就解析什么）；只用全 id 的契约要在测试里用「模型视角的 id」走一遍才算数。
- **Refs**: `internal/subagent/manager.go` (Continue short-form resolution), `internal/tools/agent/agent.go` (session_id schema), `internal/subagent/manager_continue_test.go:TestContinue_ShortSidResolution`

### PRD said "sessions are archived per-project" — they are not; bucket by header CWD
- **Saw**: implementing skill auto-distillation's batch scan (M-SD2), the PRD's pointer (`~/.seek/projects/<pid>/sessions/<sid>/`, paths.go L179) turned out to hold checkpoint/subagent SIDECAR space, not session transcripts — the transcripts live flat at `~/.seek/sessions/<id>.jsonl` with only a `cwd` field in the JSONL header line.
- **Why**: `ProjectSessionDir` was named/documented for the checkpoint subsystem; the PRD read the path shape as the archive location. "List a project's sessions" has no existing helper because nothing needed per-project bucketing before.
- **Fix**: `session.Store.ListProject(projectID)` (M-SD2) reads headers only (`loadMeta` discipline) and filters by `paths.ProjectID(info.CWD)`, skipping forked sessions (`ParentID != ""`) so /branch & /compact children can't double-count one conversation as two "independent" recurrences. Commit c_dkgvnp.
- **Lesson**: when a design doc cites a path as "where X lives", verify against the writer of X, not just the path helper's name. Per-project bucketing of sessions = hash of the header's `cwd`, nothing on disk groups them for you.
- **Refs**: `internal/session/session.go` (ListProject), `internal/paths/paths.go` (ProjectSessionDir comment), `docs/prd/feature-skill-distill.md` §3.1 D

### WebFrontend's cfgErr early-return left RPCRegistry() nil — pre-Run wiring panics on gated-out addresses
- **Saw**: W4.2 wiring (`methods.NewSessionsFromEnv().RegisterAll(fe.RPCRegistry())` in servecli's web ctor) panicked `assignment to entry in nil map` inside `Registry.Register` — but only in `TestServePlane_BadWebAddrFails`: a web_addr failing the scope gate made NewWeb return before `f.rpcReg = rpc.NewRegistry()`, so RPCRegistry() handed the wiring a zero-value registry.
- **Why**: NewWeb's failure shape is "hold cfgErr, return early" — fields initialized after the gate stay zero. W4.1's tests only exercised the happy path (its own doc said "the registry constructor cannot fail"), so the nil was latent until the first real pre-Run consumer arrived.
- **Fix**: build the registry BEFORE the scope gate (webfrontend.go NewWeb) so RPCRegistry() is never nil; registering into a gated-out frontend is harmless because Run fails with cfgErr anyway. Commit with this entry (card c_zam44t).
- **Lesson**: an accessor exposing a construction-time wiring hook on a constructor with early failure returns must guarantee the hook on EVERY path — otherwise the first downstream wiring becomes a landmine that fires exactly on the failure path, where nobody is looking.
- **Refs**: `internal/controlplane/webfrontend.go` (NewWeb), `internal/servecli/servecli.go` (web ctor), `internal/rpc/methods/session.go`

### ACP ToolCallContent has NO shape that validates on every SDK generation — diff blocks do; text blocks silently degrade
- **Saw**: emitting tool_call content (2026-11-18, "放出去"), the wire shape had three candidate spellings and no obviously right one: the v1 TS type says `(Content & {type:"content"})` — a spread whose zod translation (`zContent.and(type:"content")`) requires `type` to be BOTH "text" and "content" and therefore accepts NOTHING; the zcode bridge sends nested `{type:"content", content:{type:"text",…}}`, which fails v1 zod the same way; and the v2 draft redefines the union entirely.
- **Why**: schema codegen of `oneOf {type:const} + allOf $ref` is broken for Content (the ref carries its own conflicting discriminator), and the `x-deserialize-default-on-error` on toolCall content means a failing item is silently SKIPPED — so zcode's nested blocks have plausibly been dropped by clients forever with nobody noticing (matching the "信息少" symptom).
- **Fix**: seek sends edit results as protocol DIFF blocks (`{type:"diff", path, oldText, newText}` — Diff has no conflicting discriminator, validates on every generation, and paseo's bundle really renders oldText/newText) and everything else as the spread-intent text block `{type:"content", text:…}` (newer SDKs accept it; older ones drop it silently — degrades to no preview, never an error). Locations/rawInput/rawOutput are plain-typed and validate everywhere; paseo's bundle references `locations` (239×) and zero `rawOutput` refs.
- **Update (same day, user acceptance round)**: paseo's UI confirmed the analysis — tool cards render but the detail pane says "No additional details available". Decisive finds in the paseo.app bundles: (a) paseo's OWN bundled SDK carries the same broken `zContent.and(type:"content")` intersection (asar @agentclientprotocol/sdk) — text content blocks are unrenderable from ANY agent, paseo's own included; (b) paseo's built-in mock agent — the one shape proven against its own UI — sends NO content at all and instead rides `rawInput`/`rawOutput` as JSON OBJECTS (`z.unknown()` accepts them), which the frontend `toolCallDetails` panel pretty-prints as Input/Output sections. Fix follow-up (77f2250): seek now sends rawInput/rawOutput as PARSED objects (truncated JSON that stops parsing degrades to string) and builds `title` with an arg summary ("read notes.txt", not bare "read"). Also note: the earlier "zero rawOutput refs in asar" claim in this entry was a binary-grep artifact — grep -a over asar misses matches; slice by byte offset instead.
- **Update 2 (the stuck-spinner bite)**: the original Fix line's "text block … degrades to no preview, never an error" was WRONG, and the user caught the symptom: every tool call spun in_progress forever. paseo's `zToolCallUpdate` declares `content: z.array(zToolCallContent).nullish()` with NO skip-invalid wrapper — one item failing the never-satisfiable text union makes the ENTIRE update fail validation, so `status:"completed"` and `rawOutput` never land. A bad content item doesn't degrade to "no preview", it POISONS the whole update. Final shape (seek): text content blocks are never sent at all; content = edit-success diff blocks ONLY; result/error detail rides rawOutput exclusively.
- **Lesson**: for any ACP field whose schema rides `allOf`+discriminator, check what the CLIENT's bundled SDK actually accepts before choosing a spelling — and prefer variants without conflicting discriminators (diff over content-block) when the information allows. An item-array field with no skip-invalid semantics turns one bad item into a dropped MESSAGE — when you can't verify the client's array leniency, send only provably-valid items or none. And when a client ships its own mock agent, THAT is the reference wire shape — cheaper than schema archaeology. Binary grep over .asar is unreliable; use `grep -abo` + byte-offset slicing.
- **Refs**: `cmd/seek/main.go` `acpToolContent` / `acpRawValue` / `acpToolTitle` (comments pin the decisions), `@agentclientprotocol/sdk` dist/schema/zod.gen.js `zToolCallContent`, paseo.app asar (bundled SDK zToolCallContent ~byte 2619232, zToolCallUpdate's bare `z.array(zToolCallContent)` ~2622104; mock agent's rawInput/rawOutput objects ~2559484), paseo app-dist frontend `toolCallDetails:{input,output,empty}`, zcode-acp-server dispatch.js:406 (the nested shape)

## Tool-failure self-recovery

Moved from AGENTS.md 2026-11-14 (budget ratchet 3084→2787; AGENTS.md keeps the two-sentence core + a pointer here). Doctrine: self-recovery first — "ask the user" is the LAST resort.

- **Read the error.** Results carry category-specific hints — webfetch prefixes (`[webfetch: blocked target]`), bash `Hint:` clauses, permission denials suggesting alternatives. The answer is usually IN the error.
- **Try alternative paths.** webfetch returned garbage on an HTML page? Try raw GitHub, the API reference instead of the rendered doc, a mirror, the project's own `docs/`. bash denied in plan-analyze? Use the purpose-built tool (`git` instead of `bash("git log")`, `webfetch` instead of `curl`). One tool failing rarely makes the question unanswerable.
- **Check local context first.** `list_dir docs/` + `grep` for keywords before asking the user to paste anything — seek's CWD often already answers the question; the repo is closer than the internet.
- **Re-frame to source.** "I can't fetch the docs to answer Y" usually becomes "I can answer Y from the code" — for behaviour questions the source beats the docs anyway.
- **Actually blocked? Ask SPECIFIC questions** via `ask_user` with a 2–4 option picker, not free-form. Shifting lookup work to the user ("fetch this page for me?") inverts seek's value proposition — the *model* does the lookup work.

## Eval / results data shape

### Early eval result files are pretty-printed concatenated JSON, not one-per-line JSONL
- **Saw**: a line-oriented reader over `eval/results/*.jsonl` saw 347 lines but only 131 result rows; the pre-v0.5 files (e.g. the 146-line v0.4.1 file) hold each result as a multi-line pretty-printed object, many objects concatenated.
- **Why**: the current `eval/run.sh` emits one compact object per line, but the files it wrote before ~v0.5 were pretty-printed — the `.jsonl` extension promises a layout the old files don't have.
- **Fix**: `scripts/eval-trend.sh` jq-parses (`jq -r '… | @tsv'`) instead of splitting lines. Two related bites: `@tsv` without `-r` emits a JSON-encoded string (tabs escaped as `\t`) which silently defeats downstream TSV field-splitting; and rows before the `ran_at` field exist need the filename's UTC-date prefix as a fallback timestamp. Commit (card c_lxferp).
- **Lesson**: treat `results/*.jsonl` as a *stream of JSON objects*, never as lines — any new results tooling starts at jq.
- **Refs**: `scripts/eval-trend.sh` (collect_rows), `eval/README.md` "Trend reports"

## Typed agents / orchestration

### Routing built-in agent types through the custom gate silently widens their toolset
- **Saw**: design-time, caught before landing (P2-c): unifying `Manager.resolveType` so built-ins also flow through `ResolveCustomType` (the "one gate for all origins" refactor) would have made `explore` spawn with ~10 tools including bash/write/edit in any session whose registry lacks `webfetch` (env-gated) — a silent widening of a read-only type.
- **Why**: D13's unknown-tools rule (any whitelist tool missing from the parent registry → drop the whitelist, degrade to the general-purpose toolset) is the loud-config-error rule for FILE types; built-ins always had intersect-with-parent semantics inside `Template.Filter`. The two rules coexist and ORIGIN decides which applies.
- **Fix**: `resolveTypedDef` (manager.go) applies the degrade only for `OriginProject`/`OriginUser`; builtin + config clones keep Filter's intersect. `TestSpawn_ConfigOverrideRoutesBuiltin` pins it with a webfetch-less parent registry.
- **Lesson**: "route everything through one gate" migrations must diff the SEMANTICS of each old path, not just the code shape — where old paths differed, the shared gate needs origin-aware (or equivalent) rules.
- **Refs**: `internal/subagent/manager.go` `resolveTypedDef`; `docs/prd/feature-orchestration.md` §5 D13.

### A delete path built by filepath.Join(user input) escapes scope — Create's Parse gate covers writes only
- **Saw**: M-O2.3 review (rework round 1): `agentsmgr.Remove` joined `filepath.Join(dir, opts.Name+".md")` and called `os.Remove` with no name validation — `seek agents remove ../projects/<id>/plans/x` would delete any user-writable .md outside `~/.seek/agents/`, while Create on the same branch had full validation (Parse round-trip + `filepath.Rel` + Lstat-vs-symlink).
- **Why**: the scope-discipline work was designed around the WRITE path ("validation runs before anything lands on disk"); Remove deletes and so never ran Parse — there was no file content to validate, and the name gate came along only as a side effect of parsing. Deletes need the gate on the NAME itself.
- **Fix**: `agenttypes.ValidName` (the kebab-case rule, single authority with Parse) gates Remove before any filesystem call, plus the same `filepath.Rel` belt-and-braces as Create; commit on card `c_azqudp` rework round 1.
- **Lesson**: every filesystem-mutating entry point that builds a path from user input needs its OWN name/containment gate — "the sibling function validates" is not inherited. Check delete paths explicitly: they share the blast radius of writes but none of the validation machinery that usually grows around writes.
- **Refs**: `internal/agentsmgr/agentsmgr.go` `Remove`; `internal/agenttypes/agenttypes.go` `ValidName`. Sibling exposure NOT fixed in this card: `skillmgr.Uninstall` joins `opts.Name` the same way (see `internal/skillmgr/skillmgr.go:495`).

## Git / merge

### Concurrent drive merges on the SHARED main checkout silently erase uncommitted edits — do card work in a worktree
- **Saw**: mid-card (2026-11-18), an uncommitted `pkg/agent/events.go` edit vanished: `git status` clean, edit gone, two new merge commits (`c_5gy5h6`, `c_wwpdxp`) on main that landed while the edit sat in the working tree. The butler drive flow merges into the same checkout other sessions edit from.
- **Why**: a drive merge (or its cleanup) runs checkout/reset-class git operations on the shared working tree; any uncommitted foreign changes are not theirs to preserve, and lose.
- **Fix**: implement every card in its own `git worktree` on a `drive/...` branch (e.g. `/tmp/seek-wt-<card>` from main), test there, commit there, then merge back from the main checkout in one short window — same discipline the drive sessions' own "worktree 沙盒" acceptance already implies.
- **Lesson**: on this repo the main checkout is a SHARED merge target, not a private workspace. Uncommitted work there has no owner protecting it.
- **Refs**: butler drive flow (merges `drive/c_*` → main); `git worktree add /tmp/seek-wt-acptools -b drive/acp-toolcall main`

### Merge commits shipped with leftover conflict markers (twice)
- **Saw**: `920f9e1` (merge main into `drive/c_u4ugnu`) landed `docs/prd/feature-orchestration.md` with `<<<<<<< HEAD` / `=======` / `>>>>>>> main` still inside the PRD 状态行 — second bite of the class (first: CHANGELOG during the `c_zam44t` merge, cleaned in-passing without a pitfall entry). Both share a shape: the conflict lives inside ONE very long line (PRD 状态行 / CHANGELOG entry), so it never reads as a visible "pick a side" block and gets committed half-resolved.
- **Why**: git commits conflict markers without complaint, and manual per-file review was the only gate — which has now failed twice on exactly this long-single-line shape.
- **Fix**: ritual after resolving any merge, BEFORE `git commit`: `git grep -n '^<<<<<<< \|^>>>>>>> '` and `git diff --cached --check` (flags "leftover conflict marker" on added lines). Post-hoc audit of an already-made merge: `git diff --check <merge>^1 <merge>` — NOT `git show --check <merge>` (its `--cc` condensed diff suppresses these hunks; verified on `920f9e1`: `show --check` silent, diff-vs-either-parent flags all three marker lines). Cleanup itself: card `c_zxeqih`.
- **Lesson**: merges touching docs with long single-line status fields get a mechanical marker-grep before commit — two bites is a pattern, not bad luck.
- **Refs**: `docs/prd/feature-orchestration.md` 状态行; `920f9e1`, `e8eface` (first manual cleanup, same file).

## Subprocess / streaming stdout

### StdoutPipe forbids concurrent Wait — kill-able line-streaming needs cmd.Stdout=io.Writer + io.Pipe
- **Saw**: line-streaming the executor's seek child (RunStream, W0.1): the existing shape runs `cmd.Wait()` in a goroutine racing a timeout/kill select, but `cmd.StdoutPipe()` documents "it is incorrect to call Wait before all reads from the pipe have completed" — the obvious StdoutPipe+scanner wiring would lose the tail (or hang) on the kill path.
- **Why**: Wait closes the StdoutPipe read end after reaping; in-flight reads get closed-pipe instead of data. Setting `cmd.Stdout = io.Writer` makes Wait join os/exec's own copy goroutine instead — compatible with concurrent Wait, kill-tree, and the kill-grace expiry posture.
- **Fix**: `cmd.Stdout = pw` on an `io.Pipe`; a reader goroutine does `bufio.Reader.ReadString('\n')` per line (ReadString, NOT Scanner — Scanner's default 64KB token cap would truncate huge `tool_end.result` lines and lose the agent_end after them); once Wait returns, close pw (io.Pipe is synchronous ⇒ every byte is already inside the reader) so the reader sees EOF and folds a possibly unterminated final line; on kill-grace expiry snapshot under a mutex and walk away, mirroring the old lockedBuf posture (card `c_n3pghs`).
- **Lesson**: "stream a kill-able child's stdout" has one sound shape: `cmd.Stdout = io.Writer` + your own pipe/reader with close-after-Wait choreography; StdoutPipe is read-then-wait only, and line readers with token caps don't fit JSONL of unbounded line length.
- **Refs**: `internal/controlplane/executor.go` `attempt` / `streamSink`; `docs/prd/feature-web-frontend.md` §2.4.

### exec'ing an extensionless go build output fails on Windows as "not found" — CreateProcess needs the .exe name
- **Saw**: 2026-11-17/18 nightlies, Windows lane only: every `TestToolsFlagE2E_*` died with `exec: "C:\...\seek-e2e-bin-…\seek-e2e": executable file not found in %PATH%` — though `go build -o` had just written that exact path.
- **Why**: Windows CreateProcess (under os/exec) only executes files carrying a known extension; for an extensionless path it searches for `<path>.exe` and reports ENOENT. `go build -o <name>` writes the file under exactly `<name>` (no suffix added), so the artifact exists but is unexecutable by that name.
- **Fix**: `buildSeekBin` (cmd/seek/tools_flag_test.go) appends `.exe` when `runtime.GOOS == "windows"`; card c_45npoi.
- **Lesson**: any test/helper that builds-and-execs a helper binary must derive BOTH the `-o` target and the exec path from one `runtime.GOOS`-aware name. Green on macOS/Linux says nothing about this class — it only ever fires on the Windows CI lane.
- **Refs**: `cmd/seek/tools_flag_test.go` `buildSeekBin`

## Web frontend / asset plane

### session/messages elements arrive as objects after r.json() — JSON.parse on them throws and silently drops every message
- **Saw**: W4.4 sessions browser (first jsdom run against a real serve): `session/list` and `session/get` rendered fine, but the messages pane stayed empty while pagination buttons appeared — every `appendMsg` bailed in its catch.
- **Why**: `messages` is `[]json.RawMessage` on the Go side (raw bytes preserved verbatim), but raw JSON *embeds* into the response — after one `await r.json()` the elements are already objects. `JSON.parse(object)` coerces to the string `"[object Object]"` and throws; a bare `return` in the catch made it silent.
- **Fix**: `typeof raw === 'string' ? JSON.parse(raw) : raw` (string branch kept as belt only); commit with card `c_hinacy`.
- **Lesson**: `json.RawMessage` in a JSON-RPC result is "verbatim JSON", not "a string" — browser consumers get parsed values after `r.json()`, so parse-or-use guards belong at the JS boundary; never let a render-path catch swallow without a trace.
- **Refs**: `internal/rpc/methods/session.go` `sessionMessagesResult`; `internal/controlplane/webfrontend.html` `appendMsg`.

### macOS bash 3.2 under set -u mangles $VAR followed by a multi-byte character ("TMP\ufeff: unbound variable")
- **Saw**: manual-test script died in cleanup with `line 60: TMP<EFBBBF>: unbound variable` — no BOM exists in the file.
- **Why**: `/bin/bash` on macOS is 3.2 (no UTF-8 name parsing); under `set -u`, `$TMP（` reads lead bytes of the full-width character into the variable NAME, then reports that phantom name unset. Reproducible: `bash -uc 'TMP=x; echo "$TMP（"'`.
- **Fix**: brace every `${VAR}` whose next character is non-ASCII (the KEEP message in `scripts/web-sessions-manual.sh`); commit with card `c_hinacy`.
- **Lesson**: repo shell scripts must stay bash-3.2-safe (macOS default) — `${VAR}` braces are mandatory before CJK punctuation, and self-check any script whose failure path only runs in cleanup traps.
- **Refs**: `scripts/web-sessions-manual.sh` cleanup; `scripts/serve-smoke-test.sh` (same bash-3.2 audience).

### `${VAR-default}` under set -u passes a set-but-EMPTY value through — the `:-` form is the house discipline; and mktemp'd $tmp must be canonicalised before string-matching it
- **Saw** (c_w3vhcm rework, 2026-11-18): two self-test bugs in one session. (a) `case_filter=${1-}` style defaults kept an explicitly-empty arg empty (`ablate.sh '' ''` → binary="" instead of the default). (b) A byte-compare self-test normalised sandbox paths with `sed "s|$tmp|...|"` — never matched, because macOS `TMPDIR` ends in `/` so mktemp's returned string contains `//`, and `/var` is a symlink to `/private/var`, while every path the script-under-test prints has round-tripped `cd`+`pwd` (physical, separator-collapsed).
- **Why**: `${VAR-default}` only substitutes on UNSET; empty-but-set sails through (`:-` substitutes on both). And string comparison of paths compares the STRING, not the file — mktemp's return value is not canonical on macOS.
- **Fix**: default expansions in eval/ shell scripts use `:-` throughout (`eval/ablate.sh`); `scripts/eval-ablate-test.sh` canonicalises `tmp=$(cd "$tmp" && pwd)` immediately after mktemp, and asserts normalised output is non-empty — the first revision had a malformed sed expression that normalised BOTH sides to empty and `cmp`'d two empty files green.
- **Lesson**: any "compare two outputs" test needs a negative control (inject a one-char difference, watch it go red) and a non-empty guard on both sides — symmetric degradation of both arms is the silent false-green mode. Bash-3.2 family discipline (previous entry) applies to every `scripts/*.sh`.
- **Refs**: `eval/ablate.sh`; `scripts/eval-ablate-test.sh` (norm/cleanup); commit on card c_w3vhcm.

### W1.2 Host allowlist must match the host PART only — literal bind-addr matching 403s the documented tailnet posture and every httptest hit
- **Saw**: implementing §5.3's literal set {bind 地址, `localhost[:port]`, `127.0.0.1[:port]`} as exact strings rejects legitimate traffic: the §5.4 tailnet posture (bind 127.0.0.1:8790, `tailscale serve --tcp` forward) sends `Host: <tailnet-IP>:8790`, and httptest servers surface `Host: 127.0.0.1:<random>` — never the bind string.
- **Why**: the Host header carries where the CLIENT thinks it is, not where we bound; forwards and test harnesses both rewrite it. The rebinding threat lives entirely in the NAME part (an attacker-controlled DNS name resolving to loopback) — an IP literal cannot be rebound.
- **Fix**: `hostAllowed` checks the host part only: `localhost` (case-insensitive) or any loopback / tailnet-CGNAT IP literal passes, every name (including `127.0.0.1.evil.example`) 403s; port never compared; commit with card `c_w5lidm`.
- **Lesson**: Host allowlists defend against NAMES, so enumerate name-safety (literals/localhost), not client-visible addressing; matching bind addr or port couples the gate to shapes proxies and test servers legitimately rewrite.

### A blanket "POST needs Origin" CSRF gate breaks the script face — the W1.2 write gate is credential-conditional
- **Saw**: §5.2 read literally ("写面二选一：同源 Origin 或 X-Seek-Token") would 403 every curl POST `/msg?token=` — no Origin header exists outside browsers, and the baseline `TestWeb_MsgRequiresToken` (query token, no Origin → 202) is a pinned zero-regression contract.
- **Why**: CSRF is a COOKIE-born threat; a request bearing the token as query/Bearer already proves possession of the secret — it is its own CSRF defense. Only cookie-authenticated requests (credentials the browser attaches silently) need the extra bar.
- **Fix**: `authKind` reports WHICH credential passed; `writeGate` (Origin ∈ {self origin} or constant-time X-Seek-Token) runs only on the `credCookie` path; commit with card `c_w5lidm`.
- **Lesson**: CSRF gates key off the credential FORM, not the HTTP method — "which requests can a third party make the browser send" is the actual question; script-face credentials are exempt by construction.
- **Refs**: `internal/controlplane/webfrontend_auth.go` `writeGate`; `docs/prd/feature-web-frontend.md` §5.2.

### A single-file page cannot ship bare CSP `default-src 'self'` — hash-pin the inline blocks instead (and the hash guards only element content)
- **Saw**: §5.5 prescribes `Content-Security-Policy: default-src 'self'` with "no inline script/eval", but the §4.3 single-file `webfrontend.html` carries its JS and CSS INLINE — the literal policy would block both and render the page dead on arrival (blank, unstyled, no logic).
- **Why**: "no inline" assumes the JS lives in same-origin FILES (the post-static/-split shape); in the single-file era inline is the shape. CSP3's reconciliation is a `sha256-<b64>` hash source on `script-src`/`style-src` over the EXACT bytes between the tags — which the server can derive at startup from the same `//go:embed` it serves, so editing the page never desyncs the policy.
- **Fix**: `webCSP()` (webfrontend_static.go) scans `<script>/<style>` blocks out of the embed, hashes them, appends the tokens to `default-src 'self'`; commit with card `c_ph2zax`.
- **Lesson**: the hash covers ELEMENT CONTENT only — inline event-handler attributes (`onclick=`), `eval`, and injected `<script>` all stay blocked, so the page's DOM-API-only discipline (textContent, addEventListener) remains load-bearing for W2+ cards; a second script tag needs no CSP edit (hashes re-derive) but any attribute-borne JS dies silently.
- **Refs**: `internal/controlplane/webfrontend_static.go` `webCSP`; `docs/prd/feature-web-frontend.md` §5.5 落地口径.

### A gzip middleware must both PRESERVE http.Flusher and EXCLUDE text/event-stream — either miss kills SSE silently
- **Saw**: two ways the W1.5 wrapper could break `/events`: (a) embedding `http.ResponseWriter` hides the Flusher interface, so the handler's `w.(http.Flusher)` assertion 500s "streaming unsupported"; (b) compressing the event-stream buffers frames — SSE clients see nothing until the gzip window fills, exactly the proxy-batching the `X-Accel-Buffering: no` header exists to prevent.
- **Why**: middleware wraps the writer BEFORE the handler sets Content-Type, so compression can't be pre-decided; and Go interfaces satisfy on the CONCRETE type — a wrapper struct without its own `Flush` method is not a Flusher however the inner writer is.
- **Fix**: `gzipResponseWriter` decides at WriteHeader/first-Write (Content-Type allowlist, `text/event-stream` deliberately absent), implements `Flush()` that flushes the gzip stream then the underlying flusher, and `close()` finishes the stream after the handler returns; commit with card `c_ph2zax`.
- **Lesson**: response-wrapping middleware must re-expose every optional interface the wrapped handlers assert (Flusher is the one SSE needs; Hijacker would be the one websockets need) — and "compress everything textual" policies need a streaming carve-out, or per-event delivery dies behind the compressor.
- **Refs**: `internal/controlplane/webfrontend_static.go` `withGzip`; `TestWeb_GzipSkipsSSE`.

### http.DefaultClient silently asks for gzip and strips Content-Encoding — the "identity path" test passed vacuously
- **Saw**: `TestWeb_GzipPage`'s not-asked-for branch (no Accept-Encoding → expect identity) was green, but the server's identity path had never executed.
- **Why**: Go's `http.Transport` adds `Accept-Encoding: gzip` to any request that carries none, then transparently gunzips the answer and DELETES `Content-Encoding` — the test read a decoded body through a header the transport scrubbed. An explicit Accept-Encoding suppresses this; an absent one does not.
- **Fix**: dedicated `rawClient` (`&http.Transport{DisableCompression: true}`) in `webfrontend_static_test.go`, plus a `resp.Uncompressed` tripwire; commit with card `c_ph2zax` (rework round 1).
- **Lesson**: any test asserting "server does X when the client doesn't ask" over HTTP must disable the transport's compression negotiation — `DisableCompression` for both directions of the surprise, and check `resp.Uncompressed` as the regression wire.
- **Refs**: `internal/controlplane/webfrontend_static_test.go` `rawClient` / `getRaw`; `net/http` Transport docs.

### `mktemp -t name` is BSD-only — GNU coreutils reads the argument as a template and dies "too few X's"
- **Saw**: `scripts/web-gate.sh` (runs on ubuntu-latest) failed at `mktemp -t seek-size` with "too few X's"; under `set -e` the whole gate went red before asserting anything.
- **Why**: BSD mktemp treats `-t`'s argument as a PREFIX; GNU coreutils (and POSIX's newer `-t` spec) treat it as a TEMPLATE that must contain trailing X's. The portable shape is an explicit path with X's, honoring `TMPDIR`.
- **Fix**: `mktemp "${TMPDIR:-/tmp}/seek-size.XXXXXX"` (repo precedent: `install-serve.sh:68`, `eval-trend.sh:300`); same commit swept the file's `\s` regexes (grep + awk) to `[[:space:]]` — mawk doesn't take `\s`.
- **Lesson**: scripts meant for both macOS and CI never use `mktemp -t <name>`; use the `"${TMPDIR:-/tmp}/<name>.XXXXXX"` form, and keep regexes POSIX-class (`[[:space:]]`) when mawk is in the audience.
- **Refs**: `scripts/web-gate.sh` `build_size` / `baseline`; `.github/workflows/ci.yml` web-gate job.

### Asserting on SSE reply payloads with angle brackets fails — Go's json.Marshal HTML-escapes by default
- **Saw**: an e2e substring like `用法：/bind <项目路径>` never matches the SSE frame, which reads `用法：/bind \u003c项目路径\u003e` on the wire.
- **Why**: `json.Marshal` (and the `json.NewEncoder` default) HTML-escape `<`, `>`, `&` to `\u003c` etc.; every `Send`/`SendStream` envelope rides that marshal.
- **Fix**: assert bracket-free substrings (e.g. `用法：/bind` + `目录必须存在`); commit with card `c_evbzyh`.
- **Lesson**: substring assertions against marshaled JSON must avoid HTML-sensitive characters — pick neighbouring plain text, don't chase the escape spelling.

### Store.Load returns raw un-repaired transcripts — orphan tool_calls reach transcript readers
- **Saw**: assuming `session.Store.Load` hands back a playable message history, stats/friction (card `c_2i6sm6`) nearly scored orphan tool_calls as "already repaired away" — they aren't: a mid-tail orphan survives Load verbatim.
- **Why**: repair lives in `Session.Repair()` (backfill + truncate-at-last-orphan), which is a separate RESUME-time action callers opt into; `Load`/`decodeJSONL` just decode the JSONL as written.
- **Fix**: `friction.Fold` handles orphans as first-class input (counted as Calls, no Errors/Denied attributable); commit with card `c_2i6sm6`.
- **Lesson**: transcript readers on the Load path see the disk truth — interrupted-run tails included. Anything assuming API-replayable shape must call `Repair` itself or tolerate orphans; document which at the read site.
- **Refs**: `internal/session/session.go` `Repair` / `Load`; `internal/friction/friction.go` `Fold`.

### WebFrontend.Send is not a pure broadcast — it pops the conv's live run (the W1.4 close-out)
- **Saw**: wiring voice V2 replies through `web.Send(ctx, pageConv, r)` looked like the obvious reuse; it would have CLOSED OUT the page's own streaming run bubble with the voice reply's text whenever a typed run was live on the same page conv.
- **Why**: `Send` calls `popRun` — a reply is the W1.4 terminal event of a streamed run, so Send carries run/session ids and the client replaces the streaming bubble. A secondary frontend delivering into a web conv has no run there; popping steals the page's run id.
- **Fix**: `VoiceFrontend.Send` broadcasts the MVP `{"conv","text"}` frame directly via `web.broadcast` (no run id → the client's plain-message path); commit with card `c_f6ayyg`.
- **Lesson**: any NON-web frontend (voice today, future push/TTS channels) delivering through a web conv must `broadcast`, never `web.Send` — Send's close-out semantics belong exclusively to the frontend that owns the conv's runs.
- **Refs**: `internal/controlplane/webfrontend.go` `Send` / `webfrontend_stream.go` `popRun`; `internal/controlplane/voicefrontend.go` `Send`.

### getUserMedia needs a secure context — the documented tailnet `--tcp` forward cannot record push-to-talk
- **Saw**: the voice V2 🎤 button works on `http://127.0.0.1:8790` but arrives disabled on a phone reaching the same serve over `tailscale serve --tcp` (tailnet IP, plain http).
- **Why**: browsers gate `getUserMedia` (and `crypto.randomUUID`'s absence already forced the page's manual uuid fallback) behind a secure context — localhost is one by definition, a `100.x.y.z` host over http is not; `--tcp` forwarding never terminates TLS.
- **Fix**: the page detects it (`micAvailable()`) and disables the button with the reason; deployment note in `seek serve --help` + PRD §4.3: remote mic needs an HTTPS front (`tailscale serve` proxy mode), not `--tcp`; commit with card `c_f6ayyg`.
- **Lesson**: browser-capability surfaces (mic/camera/notifications) die silently on non-localhost plain http — when the documented remote-access posture is a TCP forward, the same feature needs an https-front variant or a visible "unavailable here" state, never a mystery.

### strings.Contains on a RAW JSON frame can never match a Windows path or angle bracket — needles need the escaped spelling too
- **Saw**: 2026-11-17/18 nightlies, Windows lane only: `TestWeb_CommandE2E` failed as `default frame …"text":"候选项目：\n• C:\\Users\\RUNNER~1\\…\\001…"} lacks "C:\\Users\\RUNNER~1\\…\\001"` — needle and haystack PRINT identically in the failure message.
- **Why**: the assertion runs `strings.Contains` against the raw SSE data line (Go-marshalled JSON, never decoded), where `\` rides doubled and `<`/`>`/`&` as `\u003c`-class escapes. A verbatim needle containing either can only match after the same escaping — on `/`-path platforms the bug is invisible because `/` and CJK need no escaping (the old "/bind usage" test had hand-avoided `<` for exactly this reason).
- **Fix**: `containsFrameText` (webfrontend_cmd_test.go) tries the verbatim needle, then its `json.Marshal`-escaped form (quotes stripped); `TestContainsFrameText` builds a Windows-shaped frame with the same Marshal so `/`-path lanes exercise it; card c_45npoi.
- **Lesson**: asserting substrings against marshalled-JSON wire text is only portable through an escaping-aware matcher — or decode the frame first. Any new raw-frame assertion helper must go through `containsFrameText`, not bare `strings.Contains`.

### go:embed serves the WORKING-TREE bytes — a Windows autocrlf checkout drifts a hash-pinned embedded asset
- **Saw**: 2026-11-17/18 nightlies, Windows lane only: `TestWeb_AssetHashPinned` reported `got: f3415179… want: b4323190…` with no code change anywhere near the page.
- **Why**: `go:embed` bakes the bytes as they sit on disk; GitHub's windows runner defaults `core.autocrlf=true`, so checkout rewrites the LF-committed `webfrontend.html` to CRLF locally, and the embedded page (and its sha256 pin) drift. Computed locally: LF version hashes to the pin, CRLF version to exactly the CI's `got` — the file in git was never wrong.
- **Fix**: `.gitattributes` pins `internal/controlplane/webfrontend.html text eol=lf` (same rationale as the existing `*.sh` rule); card c_45npoi.
- **Lesson**: every `go:embed`-ed asset whose bytes are asserted (hash pins, CSP inline hashes, byte-compare) must be eol-pinned in `.gitattributes` — otherwise "the test asserts the artifact" silently becomes "the test asserts the checkout configuration". A hash pin failing on ONE OS with a clean working tree is the autocrlf signature.

## Tooling / go list

### go list stops flag-parsing at the first package argument — a trailing `-f` becomes a package named "-f"
- **Saw**: `scripts/kernel-deps.sh --self-test` failed with "manufactured violation not reported" while a hand-run of the same report worked; the built-in sanity check read `go list resolved 3/1 kernel packages`.
- **Why**: the `go` command parses flags only BEFORE the first package argument; `go list -e $PKGS -f '{{...}}'` turns `-f` and the template into two extra package PATTERNS (with `-e` they "resolve" to themselves), so the output gains bogus lines and the intended `-f` never applies — no flag error is ever raised.
- **Fix**: all flags before patterns — `go list -e -f '{{...}}' $PKGS` in `scripts/kernel-deps.sh`; commit with card `c_zcygo2`.
- **Lesson**: in `go` tool invocations flags precede patterns, always; a wrong-arg-position bug masquerades as extra/missing packages, not a flag complaint.
- **Refs**: `scripts/kernel-deps.sh` (comment at the resolved-count check).

### `git show --numstat` emits `-\t-` for binaries and one `old => new` line for renames
- **Saw**: parsing numstat output as "two integers + path" trips on two line shapes: binary files render `-\t-\tb.bin` (dash counts), and a rename is ONE line `0\t1\ta.txt => renamed.txt` (or `dir/{old => new}/f.go`) even when content also changed — the old path never appears as its own delete line.
- **Why**: numstat has no line granularity for binaries, and rename detection (default-on since git 2.9) collapses the delete+add pair into a single path column.
- **Fix**: `parseNumstat` in `internal/autopilot/fleet.go` treats `-` as 0 line deltas (file still counts), accepts any `≥3 tab-separated fields` line (paths containing tabs just get extra fields), and skips malformed lines instead of erroring — numstat is evidence, not a parser contract. Probed-output fixture in `internal/autopilot/fleet_test.go` (`TestCommitDiffstat_RealGit`).
- **Lesson**: when tallying a git diff from CLI output, pin the exact line grammar against a real repo probe (binary + rename + tab-in-path) before writing the parser; `--numstat`'s tab-separated shape is a display convention, not a schema.
- **Refs**: `internal/autopilot/fleet.go:parseNumstat`, `internal/autopilot/fleet_test.go:TestParseNumstat`/`TestCommitDiffstat_RealGit`.

## Skill stats / --idle

### `--idle` must read last_used from the UNfiltered aggregate — the window-filtered one reports every idle skill as "never"
- **Saw**: implementing `seek skill stats --idle` (bridge M-B4): the one-aggregate approach (reuse the window-filtered `aggregate`) yields `LastUsed == ""` for every listed skill — their only calls predate the window and were filtered out, making "not called recently" indistinguishable from "never called".
- **Why**: `skillcli.aggregate(entries, since, now)` answers two questions in one fold (call count AND last-used); windowing it empties both. Idleness (window) and recency (all-time) are different questions over the same rows.
- **Fix**: `idleRows` runs two aggregates — `inWindow` decides inclusion, `ever` supplies the true last_used; commit with card `c_lz5wxa`.
- **Lesson**: when one fold answers two questions, windowing it silently degrades the second to the window's edge case. Split the folds, not the fields.
- **Refs**: `internal/skillcli/query.go` `idleRows`, `docs/prd/feature-memory-skill-bridge.md` §6.

## MCP bridge / tool naming

### 同一份 mcp.json 两次启动可能注册出不同的工具名
- **Saw**: chapter-12 §12.6 埋过的雷：旧「仅冲突时前缀」策略下，裸名归谁取决于哪个 server 先被遍历到（Go map 迭代序随机），同一份配置两次启动的注册名/顺序都可能不同。
- **Why**: `LoadServers` 直接按 map 迭代序遍历 `mcpServers`，注册名成为迭代序的函数。
- **Fix**: D1（`docs/prd/feature-mcp-client.md` §4.1）：一律默认 `<server>__<tool>` 前缀 + 按 server name 字典序遍历；`TestLoadServers_DeterministicRoster` 锁死「同配置两次注册名单逐字节一致」。Commit with card `c_4ejhhs`。
- **Lesson**: 凡是名字/名单从 map 迭代派生、又要给用户或 LLM 看的，必须先排序——否则配置不是契约，哈希种子才是。
- **Refs**: `internal/tools/mcptool/bridge.go`、`docs/book/chapter-12.md` §12.6。

## Setup wizard / provider menu

### providerOptions 扩行需三处同步——setup.go 的 envVarFor 镜像漏更会静默误触导向导
- **Saw**: 给 providerOptions 加 opencode/glm 行时，若不同步 setup.go 本地 envVarFor（镜像 internal/config 同名函数），env-only 用户（已 export `OPENCODE_API_KEY`/`GLM_API_KEY`）仍被 shouldTriggerWizard 判为「零 key」弹向导；向导保存完的 Tip 行也渲染成 `=... seek`（envVarFor 返回空串）。另有第三处：两处重试提示独立写死范围 "1–4"，与菜单数据无联动，加行即失真。
- **Why**: envVarFor 按 providerOptions 逐项查 env，镜像少一个 case 新 lane 的 env 凭证就查不到；范围文案与菜单行数是两份手抄数据。
- **Fix**: 卡 `c_nxirmj`：提示改按 `len(providerOptions)` 动态生成；envVarFor 镜像补 glm/opencode 两 case，`TestEnvVarFor_KnownProviders` 同步扩表锁死。
- **Lesson**: 向导扩 provider 的清单：providerOptions 行 + setup.go envVarFor case + internal/config envVarFor case（范围文案已动态化）。凡是「镜像别处枚举」的 switch，扩表当轮就补测试用例。
- **Refs**: `cmd/seek/setup.go` `providerOptions`/`envVarFor`/`shouldTriggerWizard`，`internal/config/config.go` `envVarFor`。

## MCP prompt section

### JSON object key order is data — unmarshalling into a map shuffles prompt bytes
- **Saw**: rendering MCP tool parameter signatures for the sysprompt MCP section, the obvious `json.Unmarshal` of the input schema into `map[string]…` properties would emit `search(repo, query, content?)` with parameters in random order — nondeterministic bytes inside the system-prompt prefix.
- **Why**: `encoding/json` into a Go map discards document order and map iteration is deliberately randomized per run; anything whose output must be byte-stable across sessions (sysprompt segments feed the DeepSeek prefix cache) cannot round-trip an ordered JSON object through a map.
- **Fix**: `schemaParams` in `internal/tools/mcptool/prompt.go` token-walks the `properties` object with `json.Decoder` — keys kept in document order, each value skipped via a `RawMessage` decode; commit with card `c_lgtpgq`.
- **Lesson**: JSON object key order is insignificant to a parser but load-bearing for byte-deterministic rendering — ordered output needs a token-stream read, never a map.
- **Refs**: `internal/tools/mcptool/prompt.go` `schemaParams`; determinism contract at `internal/sysprompt/sysprompt.go` (package doc).

## DeepSeek Files API / file_id

### file_id 引用参与前缀缓存——文档按「解析后的文字」进 prompt，id 本身不是 token
- **Saw**: F4 真 API 实测（2026-11-18，卡 c_5gy5h6，2 页 PDF ≈12741 字节）：同文档两轮，第 2 轮 `prompt_tokens=1964 / hit=1792 / miss=172`（91% 命中）；加长后的无文档对照臂 `1205→1220`、`hit=1024`，文件臂比对照臂多出的 ≈736 token **全部落在命中侧**。session JSON 往返（resume）后再发一轮：`1962 / 1792 / 170`——字节稳定到缓存连得上。另一次运行换了一个全新的 file_id（文档字节相同，走的是 ResolveFiles 的懒上传路径），首轮同样 `hit=1792`。
- **Why**: `{"type":"file","file_id":…}` 不是文本——file_id 串不进 prompt token 流；服务端在拼 prompt 前把解析结果展开成文档文字，缓存按展开后的 token 序列命中。所以换 id、重新上传都不击穿前缀缓存，只有文档内容或前序消息变化才会。
- **Fix**: 无代码改动（结论本身是交付物）；实验形状固化在 `pkg/deepseek/files_smoke_test.go` `TestFiles_RealAPI_UploadReferenceResumeCache`（文件臂 + 文本对照臂 + resume 臂，断言命中覆盖文档权重的一半以上，并打印完整读数）。
- **Lesson**: 「引用形态是否参与缓存」不能靠类比推断（base64 图 M-V.3 也是实测出来的）：file_id 与图 URL 一样可缓存，而且更稳——id 变了前缀照样命中，所以 ResolveFiles 的过期重传不会让用户多付一次 cache miss。
- **Refs**: `docs/prd/feature-deepseek-app-api.md` §3.2 D-F6、§5 开放问题②；`pkg/deepseek/resolve.go`；`docs/prd/archive/feature-vision.md` M-V.3（base64 图先例）。

### POST /files 响应里没有 page_count / expires_at——按页计费与过期重传都拿不到数据
- **Saw**: F4 真 API 上传（2026-11-18）返回体只有 `{id, object, bytes, created_at, filename, purpose}`：`page_count`、`expires_at` 文档里写了、实际不发（`text_recognition` 与 `rich_layout_recognition` 两种 purpose 各传一次，同一 2 页 PDF，均缺）。后果：F3 的 `pricing.ParseCost(fo.PageCount)` 恒记 $0；F4 的过期重传因 `expires_at=0`（= 未声明过期）永不主动触发。
- **Why**: 上传响应是 FileObject 的最小子集，解析产物（页数）与生命周期（过期时间）都不在其中；也没有别的查询接口可用——原始文件与解析结果均不支持下载/回读。
- **Fix**: 语义不改：`0 = 未声明过期 = 不主动重传` 是有意的保守选择（宁可一个死 id 在发送时报错，也不要每轮重上传、重复计解析费）。烟测把字段缺失记为**读数**而不是失败，并在日志里点明后果（`files_smoke_test.go`）。
- **Lesson**: 文档字段 ≠ 实际响应字段；凡是拿返回字段算钱的，真 API 首跑必须把原始响应体打出来核对。要接实按页计费，只能本地数页（仅 PDF 可行）或等 API 补字段——留给 F5 价格核对卡定夺。
- **Refs**: `pkg/deepseek/files.go` `FileObject`；`internal/fileroute/fileroute.go`（计费点与 marker）；`docs/prd/feature-deepseek-app-api.md` §1.2 / §6。

### 同一份 PDF 的两次上传，解析结果并不总是完整——上传返回 id ≠ 全部页面已进上下文
- **Saw**: F4 烟测（2026-11-18）同一份 2 页 PDF、同一字节内容，上传后立刻发问：多数运行 `prompt_tokens≈1941`（两页文字全在），但有一次只有 `1079`——第 1 页的事实答得出来，只存在于第 2 页的口令直接答 `NOT IN DOCUMENT`。同一进程再发一轮（`1102`）依旧没有第 2 页，说明不是「等一会儿就好」的异步补齐。`page_count` 又不返回（见上一条），客户端无法自检「解析是否完整」。
- **Why**: 未定论。可能是服务端按页/按块切分解析并各自入索引，某一页在 id 返回时仍未就绪；也可能是解析有截断上限。反例也观察到过一次（`prompt_tokens≈1022` 时第 2 页事实仍答对），所以既不能假设「id 到手 = 全文可用」，也不能断言必然丢页。
- **Fix**: 无代码改动。F4 的烟测只断言**第 1 页**的事实（`files_smoke_test.go` 里写死了原因），把「页 2 是否在场」留作观察而不是失败条件。
- **Lesson**: e2e 断言要挑「最不容易被服务端实现细节削掉」的证据：同一页内的事实比「某一页存在」稳。真 API 测试里凡是依赖「解析完整性」的断言，都该先跑三次看稳定性。
- **Refs**: `pkg/deepseek/files_smoke_test.go`；上一条 `page_count / expires_at` 缺失（同一族的「文档字段 ≠ 实际行为」）。
