### dropMalformed must list every Config event slice — Validate sees all, drop saw five
- **Saw**: wiring `pre_prompt_inject` through Gate (M-O3.2): a malformed hook (empty `name`/`command`) on any P3 observer event (`subagent_start`/`subagent_stop`/`stop`/`pre_compact`/`notification`) SURVIVED — `Validate` walks `Config.All()` and errors, but `dropMalformed` only rebuilt the five legacy slices, so a nameless hook could reach dispatch.
- **Why**: `dropMalformed` predates the P3 events and nothing ties them together; `All()` grows with every new event, the drop list doesn't.
- **Fix**: `dropMalformed` rebuilds every slice including `PrePromptInject`; `TestGate_MalformedHooksDropped` locks dropping a malformed `stop` + `pre_prompt_inject` hook (card c_m2whz2).
- **Lesson**: when validation fans over `All()` but repair enumerates fields by hand, a new event silently desynchronises them — every new `Config` event field must touch BOTH, locked by a Gate test with a malformed hook on the new event.
- **Refs**: `internal/hooksconfig/load.go:dropMalformed`, `internal/hooksconfig/hooksconfig.go:Validate`, `internal/hooksconfig/hooksconfig_test.go:TestGate_MalformedHooksDropped`
