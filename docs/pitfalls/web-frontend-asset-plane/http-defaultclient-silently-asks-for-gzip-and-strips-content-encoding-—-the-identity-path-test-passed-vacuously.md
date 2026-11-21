### http.DefaultClient silently asks for gzip and strips Content-Encoding — the "identity path" test passed vacuously
- **Saw**: `TestWeb_GzipPage`'s not-asked-for branch (no Accept-Encoding → expect identity) was green, but the server's identity path had never executed.
- **Why**: Go's `http.Transport` adds `Accept-Encoding: gzip` to any request that carries none, then transparently gunzips the answer and DELETES `Content-Encoding` — the test read a decoded body through a header the transport scrubbed. An explicit Accept-Encoding suppresses this; an absent one does not.
- **Fix**: dedicated `rawClient` (`&http.Transport{DisableCompression: true}`) in `webfrontend_static_test.go`, plus a `resp.Uncompressed` tripwire; commit with card `c_ph2zax` (rework round 1).
- **Lesson**: any test asserting "server does X when the client doesn't ask" over HTTP must disable the transport's compression negotiation — `DisableCompression` for both directions of the surprise, and check `resp.Uncompressed` as the regression wire.
- **Refs**: `internal/controlplane/webfrontend_static_test.go` `rawClient` / `getRaw`; `net/http` Transport docs.
