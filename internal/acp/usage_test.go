package acp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCtxAlertFromEnv pins the switch's polarity: unset means ON (the
// frame is additive and advisory), and only an explicit falsy value
// turns it off. ACP consumes no flags, so this env is a host's only
// lever.
func TestCtxAlertFromEnv(t *testing.T) {
	cases := []struct {
		name string
		val  string
		want bool
	}{
		{"unset", "", true},
		{"blank", "   ", true},
		{"one", "1", true},
		{"true", "true", true},
		{"zero", "0", false},
		{"zero padded", " 0 ", false},
		{"FALSE", "FALSE", false},
		{"no", "no", false},
		{"off", "off", false},
		{"garbage keeps the default", "maybe", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CtxAlertFromEnv(stubEnv(map[string]string{CtxAlertEnvVar: c.val})); got != c.want {
				t.Errorf("CtxAlertFromEnv(SEEK_ACP_CTX_ALERT=%q) = %v, want %v", c.val, got, c.want)
			}
		})
	}
	if !CtxAlertFromEnv(nil) {
		t.Error("CtxAlertFromEnv(nil) = false, want true (nil getenv must not disable the frame)")
	}
	if CtxAlertEnvVar != "SEEK_ACP_CTX_ALERT" {
		t.Errorf("CtxAlertEnvVar = %q, want SEEK_ACP_CTX_ALERT", CtxAlertEnvVar)
	}
}

// TestUsageUpdate_WireShape pins the ACP RFD shape (used/size at the top
// level, everything seek-specific under _meta) and the alert variant.
func TestUsageUpdate_WireShape(t *testing.T) {
	// Plain frame: no alert keys, but percent/severity always present.
	u := UsageUpdate("sess-1", 5_000, 200_000, 2, CtxSeverityOK, false)
	if u.SessionID != "sess-1" {
		t.Fatalf("sessionId = %q", u.SessionID)
	}
	m := u.Update.(map[string]any)
	if m["sessionUpdate"] != "usage_update" {
		t.Fatalf("sessionUpdate = %v, want usage_update", m["sessionUpdate"])
	}
	if m["used"] != 5_000 || m["size"] != 200_000 {
		t.Fatalf("used/size = %v/%v, want 5000/200000", m["used"], m["size"])
	}
	ctx := ctxMeta(t, m)
	if ctx["percent"] != 2 || ctx["severity"] != CtxSeverityOK {
		t.Fatalf("_meta.seek.ctx = %v, want percent=2 severity=ok", ctx)
	}
	if _, has := ctx["alert"]; has {
		t.Fatalf("plain frame must not carry an alert key: %v", ctx)
	}

	// Alert frame at the critical point: the nudge mirrors the TUI's
	// formatBudget wording, and alert repeats the severity.
	u = UsageUpdate("sess-1", 760_000, 1_000_000, 76, CtxSeverityCritical, true)
	ctx = ctxMeta(t, u.Update.(map[string]any))
	if ctx["alert"] != CtxSeverityCritical {
		t.Fatalf("alert = %v, want critical", ctx["alert"])
	}
	if notice, _ := ctx["notice"].(string); !strings.Contains(notice, "/compact") {
		t.Fatalf("critical notice = %q, want the /compact nudge", notice)
	}

	// Warn: bare percentage, no command nudge (it is not yet time to
	// compact — same call the status bar makes).
	u = UsageUpdate("sess-1", 650_000, 1_000_000, 65, CtxSeverityWarn, true)
	ctx = ctxMeta(t, u.Update.(map[string]any))
	if ctx["notice"] != "ctx 65%" {
		t.Fatalf("warn notice = %v, want \"ctx 65%%\"", ctx["notice"])
	}

	// The whole frame must survive JSON encoding with numeric used/size
	// and the _meta object intact — that is what a client parses.
	raw, err := json.Marshal(UsageUpdate("s", 1, 2, 50, CtxSeverityWarn, false))
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			Used          int    `json:"used"`
			Size          int    `json:"size"`
			Meta          struct {
				Seek struct {
					Ctx struct {
						Percent  int    `json:"percent"`
						Severity string `json:"severity"`
					} `json:"ctx"`
				} `json:"seek"`
			} `json:"_meta"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, raw)
	}
	if back.SessionID != "s" || back.Update.SessionUpdate != "usage_update" ||
		back.Update.Used != 1 || back.Update.Size != 2 || back.Update.Meta.Seek.Ctx.Percent != 50 {
		t.Fatalf("round trip = %+v (%s)", back, raw)
	}
}

// ctxMeta pulls the _meta.seek.ctx map out of an update, failing the
// test if the nesting slipped (a client-visible contract, not a detail).
func ctxMeta(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		t.Fatalf("_meta missing or not an object: %#v", m["_meta"])
	}
	seek, ok := meta["seek"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.seek missing: %#v", meta)
	}
	ctx, ok := seek["ctx"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.seek.ctx missing: %#v", seek)
	}
	return ctx
}
