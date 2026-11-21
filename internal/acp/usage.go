// Context-window reporting for the ACP lane (card c_uzlczr, P0-4).
//
// Why this file exists: a client driving seek over ACP could see the
// answer stream and the tool cards, but never how much of the model's
// context window the conversation was eating — a long session was a
// gamble with no dial. seek already had the number in three places (the
// TUI status bar's formatBudget, the Web top bar's ContextMeter, the
// turn_end usage row's ctx_limit); this is the fourth surface, not a
// fourth source.
//
// WIRE SHAPE — ACP's own, no invention. `session/update` carries a
// `usage_update` variant (RFD "Session Context Size and Cost", stable in
// ACP 1.2) whose fields are exactly:
//
//	used  (required) — tokens currently in context
//	size  (required) — total context window size in tokens
//	cost  (optional) — cumulative session cost
//
// Clients derive `remaining` = size − used and the percentage
// themselves; the RFD has no percent field. seek therefore sends
// used/size and puts its extras under `_meta`, the namespace ACP
// reserves for implementations ("Implementations MUST NOT make
// assumptions about values at these keys"):
//
//	"_meta": {"seek": {"ctx": {"percent": 76, "severity": "critical",
//	                           "alert": "critical",
//	                           "notice": "ctx 76% — /compact soon"}}}
//
// `percent`/`severity` ride EVERY frame (cheap, one object) so a client
// that learns seek's extension gets the same reading the TUI shows;
// `alert`/`notice` appear only on the threshold-crossing frame (see
// CtxAlertEnvVar) — that is the "告警帧" the card asks for, expressed as
// a second usage_update rather than an invented sessionUpdate value,
// because an unknown variant makes strict clients drop the whole
// notification.
//
// Thresholds are NOT redeclared here: the caller classifies with
// internal/budget (WarnFraction 0.60 / CriticalFraction 0.75), the same
// authority the TUI and the Web meter read. Same-number discipline — if
// the fractions ever move, all three surfaces move together.
package acp

import (
	"fmt"
	"strings"
)

// CtxAlertEnvVar opts the lane's threshold-crossing usage frame in or
// out. The ACP lane consumes no command-line flags (flag.Parse stops at
// the "acp" positional — see headless.go), so env is the only channel a
// client or an embedding host can turn it with.
//
// Default: ON. The frame is additive and advisory — a client that
// ignores `_meta` sees the same used/size it already got. Set it to
// 0/false/no/off to drop the alert frame and keep only the plain
// per-turn update (useful when a client logs every notification and the
// duplicate row is noise).
const CtxAlertEnvVar = "SEEK_ACP_CTX_ALERT"

// CtxAlertFromEnv resolves CtxAlertEnvVar. getenv is injected
// (os.Getenv in cmd/seek, a stub in tests) so the parsing is
// deterministic under test — same shape as YoloFromEnv, inverted
// default: only an explicit falsy value disables.
func CtxAlertFromEnv(getenv func(string) string) bool {
	if getenv == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(getenv(CtxAlertEnvVar))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// Severity labels for the `_meta.seek.ctx.severity` field. They mirror
// budget.Severity's three states; kept as wire strings so this package
// stays free of the budget dependency (the caller classifies, this file
// only shapes the frame).
const (
	CtxSeverityOK       = "ok"
	CtxSeverityWarn     = "warn"
	CtxSeverityCritical = "critical"
)

// UsageUpdate assembles one `usage_update` session/update.
//
//	used    — this turn's context occupancy (prompt + completion tokens,
//	          the same proxy the TUI and the Web meter use: the last
//	          turn's re-sent history IS the context, a cumulative sum
//	          inflates quadratically)
//	size    — budget.Limit(model), the model's context window
//	percent — int(used/size*100), truncated like formatBudget's label
//	sev     — CtxSeverity* label, from budget.Classify
//	alert   — true on a threshold-crossing frame: adds `alert` + `notice`
//
// A size <= 0 is never sent by the caller (the RFD defines no null
// size), so no guard here.
func UsageUpdate(sessionID string, used, size, percent int, sev string, alert bool) SessionUpdate {
	ctx := map[string]any{
		"percent":  percent,
		"severity": sev,
	}
	if alert {
		ctx["alert"] = sev
		// Wording follows formatBudget: critical carries the
		// /compact nudge, warn is the bare percentage. English like
		// every other client-visible string on this lane (i18n is
		// view-layer only, and ACP clients render their own chrome).
		if sev == CtxSeverityCritical {
			ctx["notice"] = fmt.Sprintf("ctx %d%% — /compact soon", percent)
		} else {
			ctx["notice"] = fmt.Sprintf("ctx %d%%", percent)
		}
	}
	return SessionUpdate{SessionID: sessionID, Update: map[string]any{
		"sessionUpdate": "usage_update",
		"used":          used,
		"size":          size,
		"_meta":         map[string]any{"seek": map[string]any{"ctx": ctx}},
	}}
}
