package controlplane

// Read-only serve-side settings + about payload (W5 card c_ni7mzw,
// feature-web-frontend §13.1 "设置面板").
//
// Scope decision, written down on the card: the panel is READ-ONLY.
// Every knob it shows is a startup-time read of ~/.seek/config.json
// (`serve` / `default_provider` / `auto_compact`) that the serve
// process consumed once — changing one means editing the file and
// restarting, so an in-page write face would need a config writer, a
// restart path and a validation story: a subsystem, not a slice of
// this card, and its own "读为主" wording already said read (the card's
// ≤8KB quota was for the token/theming rewrite alone; the delivered
// card measured ~31KB on the CI toolchain line — html +14.7KB source,
// ~290 lines of Go — and that overrun is recorded in
// feature-web-frontend §13.3 rather than glossed here). What the panel
// adds over reading the file is the EFFECTIVE view: defaults resolved
// (max concurrency 4, web_addr 127.0.0.1:8790), the build identity, and
// which token this page is riding.
//
// The method hangs off the frontend (not internal/rpc/methods) for the
// same reason approval/* does: the token half of the payload is
// frontend-native state, and the settings snapshot is handed in by
// servecli at construction (controlplane never reads config.json — no
// new import edge, no second config-resolution implementation).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/whyiyhw/seek/internal/rpc"
)

// ServeSettings is the read-only snapshot servecli hands the frontend
// at construction. Pointer-nil in WebConfig = "settings unavailable"
// (a bare frontend built by a test or an embedder), which the page
// renders as a hint rather than an error.
//
// Empty strings are meaningful: DefaultProvider empty = the child seek
// resolves the provider from the keys present (cmd/seek's auto ladder);
// Model empty = no config knob exists today, the subprocess uses its
// lane default. The page spells both out instead of inventing values.
type ServeSettings struct {
	Version         string   `json:"version"`
	DefaultProvider string   `json:"default_provider"`
	Model           string   `json:"model"`
	MaxConcurrency  int      `json:"max_concurrency"`
	Yolo            bool     `json:"yolo"`
	WebAddr         string   `json:"web_addr"`
	Projects        []string `json:"projects"`
	AutoCompact     string   `json:"auto_compact"` // human form; "" = off
}

// tokenTailLen is how much of the token the about block shows. Enough
// to tell two tokens apart at a glance, far too little to use one.
const tokenTailLen = 6

// tokenTail is the short display form of the live token ("…a1b2c3").
// It rides ensureToken, so a caller before Run (tests, a probe) gets
// the same sticky value Run would log — and never a second token.
func (f *WebFrontend) tokenTail() string {
	tok, err := f.ensureToken()
	if err != nil || tok == "" {
		return ""
	}
	if len(tok) <= tokenTailLen {
		return tok
	}
	return tok[len(tok)-tokenTailLen:]
}

// SetSettings fills the panel snapshot. servecli calls it right after
// buildFrontends, pre-Run (the §3.2 phase contract: the handler table
// is built inside Run, and no request can race a field written before
// it). Keep it a plain setter rather than a constructor argument so
// buildFrontends' signature — and every test that drives it — stays
// untouched.
func (f *WebFrontend) SetSettings(s ServeSettings) { f.cfg.Settings = &s }

// RegisterSettingsMethods hangs the read-only settings method on reg
// (servecli calls it pre-Run — the §3.2 phase contract, same as the
// asset families and approval/*).
//
//	settings/get {} → {version, token_tail, default_provider, model,
//	                   max_concurrency, yolo, web_addr, projects,
//	                   auto_compact}
func (f *WebFrontend) RegisterSettingsMethods(reg *rpc.Registry) {
	reg.Register("settings/get", func(_ context.Context, params json.RawMessage) (any, error) {
		// No params today; a body that is not an object is still a
		// client bug worth naming rather than ignoring.
		if len(strings.TrimSpace(string(params))) > 0 && string(params) != "null" {
			var probe map[string]any
			if err := json.Unmarshal(params, &probe); err != nil {
				return nil, rpc.Errorf(rpc.CodeInvalidParams, "invalid params: %v", err)
			}
		}
		if f.cfg.Settings == nil {
			return nil, rpc.Errorf(rpc.CodeInternalError, "serve settings unavailable on this frontend")
		}
		s := *f.cfg.Settings
		return struct {
			ServeSettings
			TokenTail string `json:"token_tail"`
		}{ServeSettings: s, TokenTail: f.tokenTail()}, nil
	})
}
