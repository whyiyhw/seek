### Flat search cost is not resumed — Tracker only re-prices TOKENS
- **Saw**: (design-time, web_search landing) the Search API bills per successful call (≈$0.0007) and has no `deepseek.Usage` to `Record`. The hack — a synthetic Usage turn whose pricing.Cost equals the flat fee — would poison `Last()` and the ctx% indicator with fake tokens.
- **Why**: resume reconstructs cost from `session.Usage` via `SetBase`, and Usage carries only token counts — the persisted session has nothing to re-derive a flat dollar amount from.
- **Fix**: `Tracker.RecordFlatCost(usd)` grows a separate `extraCost` folded into `CumulativeCost()` only (status bar $ total); `Cumulative()`/`Last()`/`LastCost()` stay token-pure. Accepted: flat costs vanish across `-resume` rather than grow the session schema for a $0.0007/call line item.
- **Lesson**: cost not denominated in the persisted unit (tokens) can't survive resume free — widen the schema deliberately or accept the loss, but never launder it through a fake token turn (it corrupts every token-derived indicator).
- **Refs**: `internal/cache/cache.go:RecordFlatCost`, `internal/tools/websearch/websearch.go`, `TestTracker_RecordFlatCost`
