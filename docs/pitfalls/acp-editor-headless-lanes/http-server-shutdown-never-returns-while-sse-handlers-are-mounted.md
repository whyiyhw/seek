### http.Server.Shutdown never returns while SSE handlers are mounted
- **Saw**: 2026-11-13 web frontend first draft (c_qucxtu): `srv.Shutdown(ctx)` at plane shutdown never returned — the process hung with live /events connections until the deadline.
- **Why**: Shutdown closes listeners, then *idle* connections, then waits indefinitely for the rest to go idle. An SSE handler keeps its connection active forever (stream loop until client disconnect); request contexts are cancelled by the CLIENT going away, never by Shutdown.
- **Fix**: webfrontend's Run force-closes with `srv.Close()` once the plane ctx is done (in-flight POST bodies are tiny; the Serve drain owns the message queue, not HTTP responses). Client disconnects stay covered by `r.Context().Done()` in the stream loop.
- **Lesson**: for any streaming endpoint, "graceful" Shutdown needs per-connection cancellation tracking (register each request's cancel and fire them before Shutdown). Plain Shutdown + timeout is a hang with extra steps — don't go back to Close() without that.
- **Refs**: `internal/controlplane/webfrontend.go` (Run), net/http Server.Shutdown docs
