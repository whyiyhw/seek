package main

// Startup gate for the OpenCode Go catalogue (card c_nfeloo).
//
// The bug shape these tests pin: the catalogue used to be seeded only
// when the process LAUNCHED on the OpenCode lane, yet Snapshot.Install
// is what pushes the gateway's `modalities.input` into modelcap's
// vision/files grants. Launch on deepseek, switch the session onto
// `opencode:hy4-flash-free` (what an ACP host does through
// session/set_config_option), paste a screenshot → modelcap answered
// deny-by-default and the image became a "当前模型不支持图片输入" note.
//
// Every case below launches on a NON-OpenCode lane on purpose (the
// config carries a DeepSeek key and no provider pin, so buildProvider
// auto-detects deepseek — exactly group A of the card's repro). The only
// axis that may move is "does this MACHINE have an OpenCode credential".

import (
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/modelcap"
	"github.com/whyiyhw/seek/internal/opencatalog"
	"github.com/whyiyhw/seek/internal/opencodeauth"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ids that exist ONLY in the live catalogue: the compiled pricing /
// budget / modelcap tables have never heard of them, so a true answer
// from modelcap can only come from the installed grants.
const (
	grantVisionID = "hy4-flash-free" // catalogue: vision, no files
	grantFilesID  = "longcat-2.5"    // catalogue: vision + application/pdf
)

// resetCatalogueLayers clears the runtime override layers the install
// writes, so one case's grants cannot leak into the next one.
func resetCatalogueLayers(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		pricing.SetModelRates(nil)
		budget.SetLimits(nil)
		budget.SetOutputLimits(nil)
		modelcap.SetVision(nil)
		modelcap.SetFiles(nil)
	})
}

// seedCatalogueOnlyCache writes a fresh on-disk snapshot carrying the
// catalogue-only ids, so a gate that DOES install has something to
// install without touching the network.
func seedCatalogueOnlyCache(t *testing.T) {
	t.Helper()
	snap := opencatalog.Snapshot{
		FetchedAt: time.Now(),
		Models: []opencatalog.Model{
			{ID: grantVisionID, Label: "free · 1M ctx · vision", Vision: true, Context: 1_048_576},
			{ID: grantFilesID, Label: "1M ctx · vision · files", Vision: true, Files: true, Context: 1_000_000},
		},
	}
	if err := opencatalog.SaveCached(snap); err != nil {
		t.Fatalf("SaveCached: %v", err)
	}
}

// isolateHome points SEEK_HOME/HOME at a temp dir and clears every
// ambient credential so a developer's real ~/.seek can never decide the
// outcome.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("HOME", home)
	for _, env := range []string{
		"OPENCODE_API_KEY", "DEEPSEEK_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GLM_API_KEY", "OPENDESIGN_API_KEY", "SEEK_OPENCODE_CATALOG",
	} {
		t.Setenv(env, "")
	}
	oldCat := openCatalog
	oldRefresh := openCatalogRefresh
	openCatalogRefresh = func(*opencatalog.Catalogue) {}
	t.Cleanup(func() {
		openCatalog = oldCat
		openCatalogRefresh = oldRefresh
	})
	resetCatalogueLayers(t)
	// A DeepSeek key in the config makes the launch lane deepseek under
	// buildProvider's auto-detect — the "non-opencode startup lane" half
	// of the bug, held constant across every case.
	if err := config.Save(config.Config{
		Providers: map[string]config.ProviderConfig{"deepseek": {APIKey: "ds-launch-key"}},
	}); err != nil {
		t.Fatalf("config.Save: %v", err)
	}
	return home
}

// TestInitCatalogueAtStartup_GrantGateOnCredential is acceptance ①: the
// startup gate reads the machine's OpenCode credential, not the launch
// lane. Credential present ⇒ catalogue installed ⇒ modelcap grants
// cover catalogue-only ids (and the rows the ACP dropdown / TUI picker
// read stop being nil). Credential absent ⇒ exactly the old behaviour:
// no catalogue, no grants, no refresh, no panic.
func TestInitCatalogueAtStartup_GrantGateOnCredential(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string                    // providers.opencode.api_key in config.json
		envKey  string                    // OPENCODE_API_KEY
		auth    *opencodeauth.Credentials // stored browser login
		catalog string                    // SEEK_OPENCODE_CATALOG
		// wantGrants: the catalogue's capability grants reach modelcap.
		// wantCatalogue: a Catalogue object exists (the compiled baseline
		// is seeded). They differ under SEEK_OPENCODE_CATALOG=off, where
		// the baseline is still seeded but neither the cache nor the
		// network is read — the pre-existing "off keeps baseline" posture.
		wantGrants    bool
		wantCatalogue bool
	}{
		{
			name:          "config key, non-opencode launch lane",
			apiKey:        "oc-config-key",
			wantGrants:    true,
			wantCatalogue: true,
		},
		{
			name:          "env key, non-opencode launch lane",
			envKey:        "oc-env-key",
			wantGrants:    true,
			wantCatalogue: true,
		},
		{
			name:          "stored browser login, non-opencode launch lane",
			auth:          &opencodeauth.Credentials{AccessToken: "st-test", RefreshToken: "rt-test", OrgID: "org-test"},
			wantGrants:    true,
			wantCatalogue: true,
		},
		{
			name: "no credential anywhere keeps the old posture",
		},
		{
			name: "expired-only credential file is not a credential",
			// AccessToken/RefreshToken empty ⇒ Credentials.Valid() false;
			// the gate must treat it exactly like "no login".
			auth: &opencodeauth.Credentials{OrgID: "org-without-token"},
		},
		{
			name:          "switch off seeds the baseline but installs nothing",
			apiKey:        "oc-config-key",
			catalog:       "off",
			wantCatalogue: true, // the SWITCH: no cache read, no grants
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateHome(t)
			t.Setenv("OPENCODE_API_KEY", tc.envKey)
			t.Setenv("SEEK_OPENCODE_CATALOG", tc.catalog)

			cfg := config.Config{Providers: map[string]config.ProviderConfig{
				"deepseek": {APIKey: "ds-launch-key"},
			}}
			if tc.apiKey != "" {
				config.SetKey(&cfg, "opencode", tc.apiKey)
			}
			if err := config.Save(cfg); err != nil {
				t.Fatalf("config.Save: %v", err)
			}
			if tc.auth != nil {
				if err := opencodeauth.Save(*tc.auth); err != nil {
					t.Fatalf("opencodeauth.Save: %v", err)
				}
			}
			seedCatalogueOnlyCache(t)

			// Pre-conditions the card's evidence asserts: neither
			// catalogue-only id is in any compiled table, so a true
			// answer can only come from the catalogue install.
			if modelcap.Vision(grantVisionID) || modelcap.Vision(grantFilesID) {
				t.Fatalf("%s / %s must be unknown to the static table before the gate runs",
					grantVisionID, grantFilesID)
			}

			refreshCalls := 0
			openCatalogRefresh = func(*opencatalog.Catalogue) { refreshCalls++ }

			loaded, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			initCatalogueAtStartup(loaded) // must never panic or block

			if !tc.wantGrants {
				if !tc.wantCatalogue && openCatalog != nil {
					t.Fatalf("catalogue seeded without a usable OpenCode credential: %+v", openCatalog)
				}
				if refreshCalls != 0 {
					t.Errorf("background refresh kicked %d time(s) with no credential", refreshCalls)
				}
				if modelcap.Vision(grantVisionID) || modelcap.Vision(grantFilesID) {
					t.Error("grants installed without a credential — pre-fix posture must be preserved")
				}
				if modelcap.Files(grantFilesID) {
					t.Error("file grant installed without a credential")
				}
				// Without a catalogue the readers get nil and fall back;
				// with an "off" catalogue they get the compiled baseline.
				wantRows := 0
				if tc.wantCatalogue {
					wantRows = len(pricing.OpenCodeGoModels)
				}
				if rows := openCatalogRows(); !tc.wantCatalogue && rows != nil || tc.wantCatalogue && len(rows) != wantRows {
					t.Errorf("openCatalogRows = %+v (len %d), want the compiled baseline", rows, len(rows))
				}
				return
			}

			if openCatalog == nil {
				t.Fatal("credential present but no catalogue seeded")
			}
			if refreshCalls != 1 {
				t.Errorf("background refresh calls = %d, want 1", refreshCalls)
			}
			if !modelcap.Vision(grantVisionID) {
				t.Errorf("modelcap.Vision(%q) = false; the catalogue's vision grant did not reach modelcap", grantVisionID)
			}
			if !modelcap.Vision(grantFilesID) {
				t.Errorf("modelcap.Vision(%q) = false", grantFilesID)
			}
			if !modelcap.Files(grantFilesID) {
				t.Errorf("modelcap.Files(%q) = false; the catalogue's document grant did not reach modelcap", grantFilesID)
			}
			// Same-source symptom: the ACP dropdown / TUI picker read
			// openCatalogRows, which used to be nil on this launch lane.
			rows := openCatalogRows()
			if len(rows) == 0 {
				t.Fatal("openCatalogRows is empty — the dropdown falls back to the compiled list")
			}
			var sawVisionID bool
			for _, r := range rows {
				if r.ID == grantVisionID {
					sawVisionID = true
				}
			}
			if !sawVisionID {
				t.Errorf("rows = %+v, want the catalogue-only id %q among them", rows, grantVisionID)
			}
			// The menu itself must now advertise the id the card reports
			// missing from the dropdown.
			menu := acpModelMenu(deepseek.ModelV41Flash, rows)
			if len(menu) != 1 {
				t.Fatalf("model menu = %+v, want exactly the model select", menu)
			}
			var offered bool
			for _, o := range menu[0].Options {
				if o.Value == "opencode:"+grantVisionID {
					offered = true
				}
			}
			if !offered {
				t.Errorf("ACP dropdown does not offer opencode:%s", grantVisionID)
			}
		})
	}
}
