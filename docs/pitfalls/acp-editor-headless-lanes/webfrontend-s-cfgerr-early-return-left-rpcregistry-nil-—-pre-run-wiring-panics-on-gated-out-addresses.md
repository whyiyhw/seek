### WebFrontend's cfgErr early-return left RPCRegistry() nil — pre-Run wiring panics on gated-out addresses
- **Saw**: W4.2 wiring (`methods.NewSessionsFromEnv().RegisterAll(fe.RPCRegistry())` in servecli's web ctor) panicked `assignment to entry in nil map` inside `Registry.Register` — only in `TestServePlane_BadWebAddrFails`: a web_addr failing the scope gate made NewWeb return before `f.rpcReg = rpc.NewRegistry()`, so RPCRegistry() returned a zero value.
- **Why**: NewWeb's failure shape is "hold cfgErr, return early" — fields initialized after the gate stay zero; W4.1's tests only exercised the happy path, so the nil was latent (its doc: "the registry constructor cannot fail").
- **Fix**: build the registry BEFORE the scope gate so RPCRegistry() is never nil; registering into a gated-out frontend is harmless — Run fails with cfgErr anyway (card c_zam44t).
- **Lesson**: an accessor exposing a construction-time wiring hook must guarantee it on EVERY path of a constructor with early returns — else the first downstream wiring panics exactly on the failure path.
- **Refs**: `internal/controlplane/webfrontend.go`, `internal/servecli/servecli.go`, `internal/rpc/methods/session.go`
