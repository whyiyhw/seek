### Adding a required field to a tool schema: sibling interface for the wiring, a declared cache bust for the cost
- **Saw**: `done_when`/`reviewed_by` reach Sink + tools array; widening `OnProposeStart` breaks `recordingSink`, `artifactSink`, `duplicateArtifactSink`, `planBridge`.
- **Why**: `schemaBytes` rides every request, so each edit busts cache; optional reopens the hole.
- **Fix**: `propose.CompletionReceiver{ OnProposeCompletion(doneWhen []string, reviewedBy string) }` beside `ContextReceiver`; `planBridge` keeps `pendingDoneWhen`/`pendingReviewedBy`, cleared in `Approved`; `TestCompletion_SinkWithoutInterfaceStillWorks`; bust in `v0.12.1`.
- **Lesson**: sibling interface, not a parameter; required schema fields are release-visible.
- **Refs**: `internal/tools/propose/propose.go`, `cmd/seek/main.go`, `internal/tools/propose/donewhen_test.go`, `git show v0.12.1:CHANGELOG.md`, `CompletionReceiver`, `schemaBytes`, `validFields`, `planBridge.OnProposeCompletion`, `.tagmsg`
