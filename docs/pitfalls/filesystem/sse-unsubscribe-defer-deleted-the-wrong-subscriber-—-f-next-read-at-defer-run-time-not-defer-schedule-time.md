### SSE unsubscribe defer deleted the WRONG subscriber — `f.next` read at defer-run time, not defer-schedule time
- **Saw**: web MVP's `/events` handler registered subscribers as `f.subs[f.next] = ch` but its deferred cleanup ran `delete(f.subs, f.next)`; with two tabs connected, the first to disconnect could delete the other's channel and leak its own (invisible while all tabs stayed healthy)
- **Why**: a deferred closure only captures variables, not values — `f.next` was re-read when the defer RAN, by which time any later subscriber had bumped the counter
- **Fix**: capture `id := f.next` (or return the id from the subscribe helper) and delete by that captured id; W1.3's `subscribe` returns a closure doing exactly this. Commit c_nl4nqg
- **Lesson**: `defer` freezes the *closure environment*, not the snapshot — anything read inside a deferred func must be captured to a local at schedule time if concurrent writers can move it. The bug class is silent until the shared value moves between schedule and run, which is exactly when two clients race
- **Refs**: `internal/controlplane/webfrontend_conv.go:subscribe`, `internal/controlplane/webfrontend.go:handleEvents`
