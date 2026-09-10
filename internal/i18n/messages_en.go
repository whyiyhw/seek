package i18n

// messagesEn is the source-language catalogue. Every key used anywhere
// in the codebase MUST have an entry here; TestCatalogueParity fails
// the build-side check if a translation catalogue drifts from this key
// set. English text lives here (not inline at call sites) so the
// English rendering itself is diffable against the translations.
//
// Keys are namespaced "<surface>.<element>": view.* for render-layer
// prose, cmd.* for slash-command feedback, lang.* for /lang itself.
var messagesEn = map[string]string{
	// view.go — pre-sizing hint, provider banner, streaming label,
	// queue hint.
	"view.starting":        "starting…",
	"view.provider_banner": "⚠ Provider: %s — FIM / cache stats / Reasoner disabled",
	"view.thinking":        "thinking…",
	"view.thinking_s":      "thinking… %ds",
	"view.thinking_ms":     "thinking… %dm%ds",
	"view.queue.steering":  "↪ steering: ",
	"view.queue.queued":    "↰ queued: ",

	// view.go — popup menu footer hints.
	"menu.hint.simple": "  ↑/↓ navigate · Enter accept · Esc cancel",
	"menu.hint.toggle": "  ↑/↓ navigate · Space toggle · Enter confirm · Esc cancel",

	// commands.go — dispatch fallback for an unmatched /command.
	"cmd.unknown": "unknown command %s — try /help",

	// /lang feedback.
	"lang.status":         "Language: %s (view text). Switch with /lang <en|zh>; SEEK_LANG overrides per-launch.",
	"lang.switched":       "Language switched to %s. Already-rendered lines keep their original language.",
	"lang.unsupported":    "Unsupported language %q — supported: en, zh.",
	"lang.persist_failed": "warning: could not persist language to ~/.seek/config.json: %v",
}
