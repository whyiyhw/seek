### Paseo has no ACP subagent channel — the private subagent UI belongs to the OMP provider

- **Saw**: wanted subagent progress in paseo's subagent cards over ACP. Cards exist, but a seek (`extends:"acp"`) provider produced none; only a plain `agent` tool card showed.
- **Why**: `Paseo.app/Contents/Resources/app.asar` shows `translateSessionUpdate` handling 11 fixed standard variants and dropping the rest (`default` branch); `subagent_update` appears zero times. Rich cards come from `subagent_lifecycle`/`subagent_progress`/`subagent_event` events on the Oh-My-OpenCode (OMP) provider's private JSON-RPC channel (`OmpSubagentCardTracker` with `parentToolCallId`) — unreachable from the ACP wire.
- **Fix**: stream child text as standard `agent_thought_chunk` frames (paseo renders them in the reasoning region), prefixed `[<sub-sid>]`. A per-turn context sink rides the sync subagent's runCtx; no `StreamingTool`/`EmitEvent` (they'd break the streaming×readonly dispatch invariant).
- **Lesson**: confirm the client's UI is fed by the protocol you speak before designing a client-side feature; extension frames a strict client drops are a dead end, and ACP v2's `subagent_update` draft won't help a v1-negotiated provider.
- **Refs**: `cmd/seek/subagent_progress.go`, `cmd/seek/main.go` (`acpBackend.Prompt`), `cmd/seek/subagent_glue.go`, `docs/guide-zed.md` §5.
