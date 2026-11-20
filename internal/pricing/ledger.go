package pricing

// The verified-on ledger: one row per priced card in this package,
// recording WHEN it was last confirmed against its source, WHERE, and
// in WHICH currency the source lists it natively.
//
// Why a structured ledger (c_ldu44u): docs/pitfalls.md carries six
// same-family entries — retired ids silently answering at the wrong
// card (4.4×), alias sunsets orphaning literal references, phantom
// cross-currency "rate changes", tools-family prices leaving the
// pricing page, and the peak/off-peak window inversion — all sharing
// one root cause: verification dates lived as prose in comments, so
// the only drift signal was human memory. The ledger replaces the
// scattered "checked YYYY-MM-DD" comment stamps (they are retired
// from the comments — this file is the single source for dates) and
// gives scripts/price-audit.sh something mechanical to patrol:
// stale rows (>90 days) and orphan references (code naming a model id
// the ledger does not cover).
//
// The Currency field is the SOURCE PAGE's native listing currency
// (USD for DeepSeek's English page, CNY for bigmodel.cn), not the
// card's unit — the cards are all USD. It exists so the next
// verification diffs like-for-like currency instead of manufacturing
// a phantom rate change (pitfalls: cross-currency diffs).
//
// PARSE CONTRACT: scripts/price-audit.sh reads this file as text,
// one entry per line matching
//
//	"id": {VerifiedOn: "YYYY-MM-DD", SourceURL: "https://…", Currency: "USD|CNY"},
//
// Flexible whitespace between fields (gofmt alignment is tolerated),
// but one entry per line is REQUIRED — ledger_test.go re-parses the
// source with the same expression as the script, so a reformat that
// would blind the patrol fails the Go suite first.
//
// Free-pool rows ($0 cards) and tool rows (tool:search etc.) are
// verified like any other: $0 is also a price that can change.
var verifiedOn = map[string]verification{
	"beam-free":               {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"deepseek-flash":          {VerifiedOn: "2026-11-20", SourceURL: "https://api-docs.deepseek.com/quick_start/pricing", Currency: "USD"},
	"deepseek-v4-pro":         {VerifiedOn: "2026-11-20", SourceURL: "https://api-docs.deepseek.com/quick_start/pricing", Currency: "USD"},
	"deepseek-v4.1-flash":     {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"glm-5.2":                 {VerifiedOn: "2026-11-18", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"glm-5.3":                 {VerifiedOn: "2026-11-20", SourceURL: "https://docs.bigmodel.cn/cn/guide/start/pricing", Currency: "CNY"},
	"glm-5.3-flash":           {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"hy3":                     {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"hy4":                     {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"hy4-flash-free":          {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"hy4-preview":             {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"iceman-contributor-free": {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"kimi-k2.7-code":          {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"kimi-k3":                 {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"kimi-k3.1-pro":           {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"longcat-2.0":             {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"longcat-2.5-free":        {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"minimax-m3.1-flash":      {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"mimo-v2.6-flash":         {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"mimo-v2.6-pro":           {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"step-5-preview":          {VerifiedOn: "2026-11-20", SourceURL: "https://opencode.ai/inference/go/v1/models", Currency: "USD"},
	"tool:asr":                {VerifiedOn: "2026-11-20", SourceURL: "https://api-docs.deepseek.com/quick_start/pricing", Currency: "USD"},
	"tool:parse":              {VerifiedOn: "2026-11-20", SourceURL: "https://api-docs.deepseek.com/quick_start/pricing", Currency: "USD"},
	"tool:search":             {VerifiedOn: "2026-11-20", SourceURL: "https://api-docs.deepseek.com/quick_start/pricing", Currency: "USD"},
	"tool:tts":                {VerifiedOn: "2026-11-20", SourceURL: "https://api-docs.deepseek.com/quick_start/pricing", Currency: "USD"},
}

// verification is one ledger row. All fields are required; the parity
// test enforces one row per priced card.
type verification struct {
	// VerifiedOn is the date (YYYY-MM-DD, UTC) the card was last
	// confirmed against SourceURL — by a human reading the page or a
	// patrol run capturing evidence. Bump it ONLY on actual
	// re-verification, never mechanically.
	VerifiedOn string
	// SourceURL is where the price is publicly listed. For the
	// tools family this may be the tool's own guide page rather than
	// quick_start/pricing — record wherever the figure is actually
	// readable (pitfalls: tools-family prices left the pricing page).
	SourceURL string
	// Currency is what SourceURL lists natively: "USD" or "CNY".
	// The card's own unit stays USD regardless.
	Currency string
}
