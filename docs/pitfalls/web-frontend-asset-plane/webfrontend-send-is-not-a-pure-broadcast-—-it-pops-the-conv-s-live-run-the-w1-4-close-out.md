### WebFrontend.Send is not a pure broadcast — it pops the conv's live run (the W1.4 close-out)
- **Saw**: wiring voice V2 replies through `web.Send(ctx, pageConv, r)` looked like the obvious reuse; it would have CLOSED OUT the page's own streaming run bubble with the voice reply's text whenever a typed run was live on the same page conv.
- **Why**: `Send` calls `popRun` — a reply is the W1.4 terminal event of a streamed run, so Send carries run/session ids and the client replaces the streaming bubble. A secondary frontend delivering into a web conv has no run there; popping steals the page's run id.
- **Fix**: `VoiceFrontend.Send` broadcasts the MVP `{"conv","text"}` frame directly via `web.broadcast` (no run id → the client's plain-message path); commit with card `c_f6ayyg`.
- **Lesson**: any NON-web frontend (voice today, future push/TTS channels) delivering through a web conv must `broadcast`, never `web.Send` — Send's close-out semantics belong exclusively to the frontend that owns the conv's runs.
- **Refs**: `internal/controlplane/webfrontend.go` `Send` / `webfrontend_stream.go` `popRun`; `internal/controlplane/voicefrontend.go` `Send`.
