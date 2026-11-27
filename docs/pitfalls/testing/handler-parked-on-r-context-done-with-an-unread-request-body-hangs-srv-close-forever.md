### Handler parked on `<-r.Context().Done()` with an unread request body hangs `srv.Close()` forever
- **Saw**: a ctx-cancel test (handler blocks on `<-r.Context().Done()`, client cancels mid-flight, client call returns fine) wedged the binary until `go test -timeout` — `httptest.Server.Close` waited forever; the server-side request context never fired despite the disconnect.
- **Why**: on HTTP/1.1 the server cancels `r.Context()` on disconnect only once a background connection read is active, and `net/http` defers starting it until the body hits EOF (`registerOnHitEOF` in `serveConn`); a handler parking with the body unconsumed never starts that read, so the disconnect is invisible.
- **Fix**: drain before parking — `_, _ = io.Copy(io.Discard, r.Body)` then `<-r.Context().Done()`.
- **Lesson**: a fake parking on the request context is also a body consumer: the client's abort and the server's knowledge of it differ, and on HTTP/1.1 the second costs an explicit drain. Same `srv.Close()` hang as the SSE entry, different root cause (LIFO there, background-read deferral here).
- **Refs**: `pkg/deepseek/asr_test.go:TestASR_ContextCanceled`
