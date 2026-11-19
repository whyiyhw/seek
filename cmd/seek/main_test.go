package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/mcpconfig"
	"github.com/whyiyhw/seek/internal/opencatalog"
	"github.com/whyiyhw/seek/internal/opencodeauth"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/subagent"
	"github.com/whyiyhw/seek/internal/tools/mcptool"
	"github.com/whyiyhw/seek/pkg/llm"
	"github.com/whyiyhw/seek/pkg/mcp"
)

// detectGlamourStyle has three decision axes:
//  1. flag override ("dark"/"light" → return immediately)
//  2. SEEK_STYLE env var when flag is "auto"
//  3. terminal default (HasDarkBackground) when neither applies
//
// Axes 1 and 2 are pure logic and fully testable.
// Axis 3 depends on the real terminal — we verify it returns
// either "dark" or "light" without panicking.

func TestDetectGlamourStyle_FlagOverride(t *testing.T) {
	tests := []struct {
		name  string
		theme string
		want  string
	}{
		{"explicit dark", "dark", "dark"},
		{"explicit light", "light", "light"},
	}
	// Mixed-case values fall through to env/terminal detection,
	// so we don't assert a specific result — just verify no panic.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectGlamourStyle(tt.theme)
			if got != tt.want {
				t.Errorf("detectGlamourStyle(%q) = %q, want %q", tt.theme, got, tt.want)
			}
		})
	}
}

func TestDetectGlamourStyle_EnvOverride(t *testing.T) {
	// SEEK_STYLE must be honoured when flag is "auto".
	// We set the env, call the function, then restore.
	const envKey = "SEEK_STYLE"
	prev := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prev) })

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"env dark", "dark", "dark"},
		{"env light", "light", "light"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv(envKey, tt.value)
			got := detectGlamourStyle("auto")
			if got != tt.want {
				t.Errorf("detectGlamourStyle(\"auto\") with SEEK_STYLE=%q = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestDetectGlamourStyle_FlagBeatsEnv(t *testing.T) {
	// When flag is "dark" or "light", the env var must be ignored.
	const envKey = "SEEK_STYLE"
	prev := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prev) })
	os.Setenv(envKey, "light")

	got := detectGlamourStyle("dark")
	if got != "dark" {
		t.Errorf("detectGlamourStyle(\"dark\") with SEEK_STYLE=light = %q, want \"dark\"", got)
	}
}

func TestDetectGlamourStyle_Fallback(t *testing.T) {
	// No flag override, no env var → falls through to terminal
	// detection. We can't predict the result, but it must return
	// "dark" or "light" without error.
	const envKey = "SEEK_STYLE"
	prev := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prev) })
	os.Unsetenv(envKey)

	got := detectGlamourStyle("auto")
	if got != "dark" && got != "light" {
		t.Errorf("detectGlamourStyle(\"auto\") with no env = %q, want \"dark\" or \"light\"", got)
	}
}

// TestDetectGlamourStyle_NonTTYStdinSkipsQuery pins the CONIN$ hang
// fix: with a piped stdin (no tty), detection must short-circuit to
// "dark" WITHOUT issuing the OSC 11 query. Pre-fix, lipgloss v2's
// Windows path opened CONIN$, switched it to raw mode, and blocked in
// ReadConsole forever waiting for a reply no non-interactive console
// sends (the 2s CancelReader timeout did not fire on every console
// host) — a full `go test ./...` hung 10m at TestDetectGlamourStyle_
// Fallback exactly here. The pipe swap makes the non-tty condition
// true on every platform, so the test is a portable regression trap.
func TestDetectGlamourStyle_NonTTYStdinSkipsQuery(t *testing.T) {
	const envKey = "SEEK_STYLE"
	prevEnv := os.Getenv(envKey)
	t.Cleanup(func() { os.Setenv(envKey, prevEnv) })
	os.Unsetenv(envKey)

	prevStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = prevStdin
		r.Close()
		w.Close()
	})

	if got := detectGlamourStyle("auto"); got != "dark" {
		t.Errorf("detectGlamourStyle(\"auto\") with piped stdin = %q, want \"dark\" (query skipped)", got)
	}
}

// buildProvider("glm", ...) wiring: label, default model, key resolution,
// and the --base-url override path. SEEK_HOME is pointed at a temp dir so
// the test never reads the developer's real ~/.seek/config.json.
func TestBuildProvider_GLM(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())

	t.Run("env key, default base url", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "k")
		provider, dsClient, label, modelDefault, err := buildProvider("glm", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if dsClient != nil {
			t.Errorf("glm must not build a deepseek client")
		}
		if provider == nil || provider.Name() != "GLM" {
			t.Errorf("provider label = %v, want GLM", provider)
		}
		if label != "GLM" {
			t.Errorf("provLabel = %q, want \"GLM\"", label)
		}
		if modelDefault != "glm-5.3" {
			t.Errorf("modelDefault = %q, want glm-5.3", modelDefault)
		}
	})

	t.Run("config-file key", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "")
		home := t.TempDir()
		t.Setenv("SEEK_HOME", home)
		cfgJSON := `{"providers": {"glm": {"api_key": "from-config"}}}`
		if err := os.WriteFile(home+"/config.json", []byte(cfgJSON), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		provider, _, label, modelDefault, err := buildProvider("glm", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if provider == nil || label != "GLM" || modelDefault != "glm-5.3" {
			t.Errorf("glm from config-file key: provider=%v label=%q model=%q", provider, label, modelDefault)
		}
	})

	t.Run("no key errors", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "")
		t.Setenv("SEEK_HOME", t.TempDir())
		_, _, _, _, err := buildProvider("glm", "", "")
		if err == nil || !strings.Contains(err.Error(), "no GLM API key") {
			t.Errorf("expected no-key error, got %v", err)
		}
	})

	t.Run("base-url override accepted", func(t *testing.T) {
		t.Setenv("GLM_API_KEY", "k")
		provider, _, _, _, err := buildProvider("glm", "http://localhost:9/v1/chat/completions", "")
		if err != nil {
			t.Fatalf("buildProvider with --base-url: %v", err)
		}
		if provider == nil {
			t.Errorf("nil provider with --base-url override")
		}
	})

	t.Run("config base_url beats --base-url on the wire", func(t *testing.T) {
		// The regression this card exists for: a Coding Plan key must be
		// able to live at /api/coding/paas/v4 via config alone. Assert on
		// the path the HTTP layer actually POSTs to — a precedence bug
		// here silently bills the recharge balance again.
		t.Setenv("GLM_API_KEY", "k")
		t.Setenv("GLM_BASE_URL", "") // isolate from the developer's shell
		home := t.TempDir()
		t.Setenv("SEEK_HOME", home)
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n")
		}))
		defer srv.Close()
		cfgJSON := `{"providers": {"glm": {"api_key": "k", "base_url": "` + srv.URL + `/from-config"}}}`
		if err := os.WriteFile(home+"/config.json", []byte(cfgJSON), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		provider, _, _, _, err := buildProvider("glm", srv.URL+"/from-flag", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		ch, err := provider.ChatStream(context.Background(), llm.ChatRequest{
			Model:    "glm-5.3",
			Messages: []llm.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("ChatStream: %v", err)
		}
		for range ch { // drain so the request fully lands
		}
		if gotPath != "/from-config" {
			t.Errorf("request path = %q, want /from-config (config base_url must beat --base-url)", gotPath)
		}
	})
}

// TestGLMEndpoint_Precedence pins the three-layer endpoint resolution —
// providers.glm.base_url / GLM_BASE_URL > --base-url > pay-as-you-go
// default — and that the default itself did NOT move. Keeping the
// default on paas/v4 is deliberate: the majority of keys are pay-as-you-
// go, so flipping it would break them in the opposite direction. The
// endpoint strings are contract: /api/paas/v4 bills the recharge
// balance, /api/coding/paas/v4 bills the Coding Plan.
func TestGLMEndpoint_Precedence(t *testing.T) {
	t.Setenv("GLM_BASE_URL", "")
	const (
		wantDefault = "https://open.bigmodel.cn/api/paas/v4/chat/completions"
		wantCoding  = "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions"
		flagURL     = "http://localhost:9/from-flag"
	)
	if glmBaseURL != wantDefault {
		t.Errorf("glmBaseURL = %q, want %q (default must stay pay-as-you-go)", glmBaseURL, wantDefault)
	}
	if glmCodingPlanBaseURL != wantCoding {
		t.Errorf("glmCodingPlanBaseURL = %q, want %q", glmCodingPlanBaseURL, wantCoding)
	}
	cfgWith := func(u string) config.Config {
		return config.Config{Providers: map[string]config.ProviderConfig{
			"glm": {APIKey: "k", BaseURL: u},
		}}
	}

	if got := glmEndpoint(config.Config{}, ""); got != wantDefault {
		t.Errorf("default layer = %q, want %q", got, wantDefault)
	}
	if got := glmEndpoint(config.Config{}, flagURL); got != flagURL {
		t.Errorf("flag layer = %q, want %q", got, flagURL)
	}
	if got := glmEndpoint(cfgWith(wantCoding), flagURL); got != wantCoding {
		t.Errorf("config layer must beat the flag, got %q", got)
	}
	t.Setenv("GLM_BASE_URL", "https://from-env.example/v4/chat/completions")
	if got := glmEndpoint(cfgWith(wantCoding), flagURL); got != "https://from-env.example/v4/chat/completions" {
		t.Errorf("GLM_BASE_URL must beat the config file, got %q", got)
	}
}

// TestBuildSubagentRoutes_GLMEndpoint guards the SECOND construction site
// for the GLM lane: subagent routes used to hardcode glmBaseURL, so a
// Coding Plan parent would still spawn recharge-balance-billed children.
// config base_url must reach the child's POST target too.
func TestBuildSubagentRoutes_GLMEndpoint(t *testing.T) {
	t.Setenv("GLM_API_KEY", "k")
	t.Setenv("GLM_BASE_URL", "")
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n")
	}))
	defer srv.Close()
	cfgJSON := `{"providers": {"glm": {"api_key": "k", "base_url": "` + srv.URL + `/coding"}}}`
	if err := os.WriteFile(home+"/config.json", []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	route, ok := buildSubagentRoutes(nil, "")[subagent.ProviderGLM]
	if !ok || route.provider == nil {
		t.Fatalf("glm route missing from subagent table: %+v", route)
	}
	ch, err := route.provider.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "glm-5.3",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("child ChatStream: %v", err)
	}
	for range ch {
	}
	if gotPath != "/coding" {
		t.Errorf("child GLM request path = %q, want /coding (config base_url must reach subagent routes)", gotPath)
	}
}

// buildProvider("opencode", ...) wiring, mirroring the GLM paradigm:
// label, lane default model (id confirmed against the gateway's public
// GET /v1/models on 2026-11-18), key resolution via
// OPENCODE_API_KEY → providers.opencode, and the --base-url override.
func TestBuildProvider_OpenCode(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())

	// The endpoint is contract, like glmBaseURL: /go/openai/v1 is the
	// base_url the gateway's own GET /v1/models advertises for every
	// chat model, and the /go prefix is what scopes a request to the Go
	// plan. The doc's /inference/openai/v1/... and the older
	// /inference/go/v1/... aliases also route — pinning keeps a future
	// edit from silently drifting onto one of them.
	t.Run("default endpoint is the documented Go base", func(t *testing.T) {
		const want = "https://opencode.ai/inference/go/openai/v1/chat/completions"
		if opencodeBaseURL != want {
			t.Errorf("opencodeBaseURL = %q, want %q", opencodeBaseURL, want)
		}
	})

	t.Run("env key, default label and model", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "k")
		provider, dsClient, label, modelDefault, err := buildProvider("opencode", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if dsClient != nil {
			t.Errorf("opencode must not build a deepseek client")
		}
		if provider == nil || provider.Name() != "OpenCode" {
			t.Errorf("provider label = %v, want OpenCode", provider)
		}
		if label != "OpenCode" {
			t.Errorf("provLabel = %q, want \"OpenCode\"", label)
		}
		if modelDefault != "deepseek-v4.1-flash" {
			t.Errorf("modelDefault = %q, want deepseek-v4.1-flash", modelDefault)
		}
	})

	t.Run("config-file key", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "")
		home := t.TempDir()
		t.Setenv("SEEK_HOME", home)
		cfgJSON := `{"providers": {"opencode": {"api_key": "from-config"}}}`
		if err := os.WriteFile(home+"/config.json", []byte(cfgJSON), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		provider, _, label, modelDefault, err := buildProvider("opencode", "", "")
		if err != nil {
			t.Fatalf("buildProvider: %v", err)
		}
		if provider == nil || label != "OpenCode" || modelDefault != "deepseek-v4.1-flash" {
			t.Errorf("opencode from config-file key: provider=%v label=%q model=%q", provider, label, modelDefault)
		}
	})

	t.Run("no key errors", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "")
		t.Setenv("SEEK_HOME", t.TempDir())
		_, _, _, _, err := buildProvider("opencode", "", "")
		// The message must name BOTH credential routes: a user who never
		// heard of the browser login would otherwise only see the env var.
		if err == nil || !strings.Contains(err.Error(), "seek auth opencode") ||
			!strings.Contains(err.Error(), "OPENCODE_API_KEY") {
			t.Errorf("expected a both-routes error, got %v", err)
		}
	})

	t.Run("base-url override accepted", func(t *testing.T) {
		t.Setenv("OPENCODE_API_KEY", "k")
		provider, _, _, _, err := buildProvider("opencode", "http://localhost:9/v1/chat/completions", "")
		if err != nil {
			t.Fatalf("buildProvider with --base-url: %v", err)
		}
		if provider == nil {
			t.Errorf("nil provider with --base-url override")
		}
	})
}

// opencodeEndpoint mirrors glmEndpoint's precedence: OPENCODE_BASE_URL /
// providers.opencode.base_url > --base-url > compiled default. The config
// and env layers used to be ignored on this lane (only the flag was read),
// so a proxy/mirror fronting the Go gateway applied to the parent session
// but not to subagent routes.
func TestOpenCodeEndpoint_Precedence(t *testing.T) {
	t.Setenv("OPENCODE_BASE_URL", "")
	const (
		wantDefault = "https://opencode.ai/inference/go/openai/v1/chat/completions"
		flagURL     = "http://localhost:9/from-flag"
		cfgURL      = "http://localhost:9/from-config"
	)
	if got := opencodeEndpoint(config.Config{}, ""); got != wantDefault {
		t.Errorf("default layer = %q, want %q", got, wantDefault)
	}
	if got := opencodeEndpoint(config.Config{}, flagURL); got != flagURL {
		t.Errorf("flag layer = %q, want %q", got, flagURL)
	}
	cfg := config.Config{Providers: map[string]config.ProviderConfig{
		"opencode": {APIKey: "k", BaseURL: cfgURL},
	}}
	if got := opencodeEndpoint(cfg, flagURL); got != cfgURL {
		t.Errorf("config layer must beat the flag, got %q", got)
	}
	t.Setenv("OPENCODE_BASE_URL", "https://from-env.example/v1/chat/completions")
	if got := opencodeEndpoint(cfg, flagURL); got != "https://from-env.example/v1/chat/completions" {
		t.Errorf("OPENCODE_BASE_URL must beat the config file, got %q", got)
	}
}

// TestBuildSubagentRoutes_OpenCodeEndpoint guards the second construction
// site for the OpenCode lane, same shape as the GLM guard above: the route
// used to hardcode opencodeBaseURL, so a providers.opencode.base_url
// override would govern the parent session but not explore/plan children.
func TestBuildSubagentRoutes_OpenCodeEndpoint(t *testing.T) {
	t.Setenv("OPENCODE_API_KEY", "k")
	t.Setenv("OPENCODE_BASE_URL", "")
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n")
	}))
	defer srv.Close()
	cfgJSON := `{"providers": {"opencode": {"api_key": "k", "base_url": "` + srv.URL + `/mirror"}}}`
	if err := os.WriteFile(home+"/config.json", []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	route, ok := buildSubagentRoutes(nil, "")[subagent.ProviderOpenCode]
	if !ok || route.provider == nil {
		t.Fatalf("opencode route missing from subagent table: %+v", route)
	}
	ch, err := route.provider.ChatStream(context.Background(), llm.ChatRequest{
		Model:    "deepseek-v4.1-flash",
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("child ChatStream: %v", err)
	}
	for range ch {
	}
	if gotPath != "/mirror" {
		t.Errorf("child OpenCode request path = %q, want /mirror (config base_url must reach subagent routes)", gotPath)
	}
}

// TestLaneHasCredential pins the presence gate shared by the ACP dropdown
// and the subagent routing table: a browser login counts for the opencode
// lane (a lane that appears in the dropdown must not fail on the first
// prompt), but must not leak credentials to any other lane.
func TestLaneHasCredential(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "")

	if laneHasCredential(config.Config{}, "opencode") {
		t.Error("no key and no login must mean no credential")
	}
	if err := opencodeauth.Save(opencodeauth.Credentials{
		ClientID: "oac_x", AccessToken: "st_x", ExpiresAt: time.Now().Add(time.Hour), OrgID: "org_x",
	}); err != nil {
		t.Fatal(err)
	}
	if !laneHasCredential(config.Config{}, "opencode") {
		t.Error("a stored login must satisfy the opencode gate")
	}
	if laneHasCredential(config.Config{}, "glm") {
		t.Error("a stored opencode login must not authenticate other lanes")
	}

	t.Setenv("OPENCODE_API_KEY", "k")
	if !laneHasCredential(config.Config{}, "opencode") {
		t.Error("a static key must satisfy the gate too")
	}
}

// TestBuildSubagentRoutes_OpenCodeOAuth: children inherit the browser
// login, otherwise the lane's cost argument (route explore/plan there)
// vanishes the moment the user stops using a static key.
func TestBuildSubagentRoutes_OpenCodeOAuth(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	if err := opencodeauth.Save(opencodeauth.Credentials{
		ClientID: "oac_x", AccessToken: "st_x", RefreshToken: "rt_x",
		ExpiresAt: time.Now().Add(time.Hour), OrgID: "org_x", OrgName: "Personal",
	}); err != nil {
		t.Fatal(err)
	}
	route, ok := buildSubagentRoutes(nil, "")[subagent.ProviderOpenCode]
	if !ok || route.provider == nil {
		t.Fatalf("opencode route missing with OAuth creds: %+v", route)
	}
}

// TestBuildProvider_OpenCodeOAuth pins the credential fallback: with no
// static key but a stored browser login, buildProvider returns a client
// instead of the "no credentials" error; a login without a workspace is
// rejected (the gateway would 403 every request).
func TestBuildProvider_OpenCodeOAuth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("OPENCODE_API_KEY", "")
	write := func(cred opencodeauth.Credentials) {
		t.Helper()
		if err := opencodeauth.Save(cred); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	write(opencodeauth.Credentials{
		ClientID: "oac_x", AccessToken: "st_x", RefreshToken: "rt_x",
		ExpiresAt: time.Now().Add(time.Hour), OrgID: "org_x", OrgName: "Personal",
	})
	provider, dsClient, label, modelDefault, err := buildProvider("opencode", "", "")
	if err != nil {
		t.Fatalf("buildProvider with OAuth creds: %v", err)
	}
	if dsClient != nil || provider == nil || label != "OpenCode" || modelDefault != pricing.ModelOpenCodeV41Flash {
		t.Errorf("provider=%v ds=%v label=%q model=%q", provider, dsClient, label, modelDefault)
	}

	// No workspace recorded → error at build time, not a 403 later.
	write(opencodeauth.Credentials{ClientID: "oac_x", AccessToken: "st_x", ExpiresAt: time.Now().Add(time.Hour)})
	if _, _, _, _, err := buildProvider("opencode", "", ""); err == nil ||
		!strings.Contains(err.Error(), "no workspace") {
		t.Errorf("missing-workspace login = %v, want a build-time error", err)
	}

	// An explicit key still wins over the stored login (checked by the
	// existing env-key subtest; here we prove the OAuth path is skipped
	// when a key resolves, by storing a broken login and passing a key).
	t.Setenv("OPENCODE_API_KEY", "k")
	if _, _, _, _, err := buildProvider("opencode", "", ""); err != nil {
		t.Errorf("explicit key must win over a broken stored login: %v", err)
	}
}

// TestACPMenu_OpenCodeCatalogue guards the ACP half of the Go lane's
// menu: the session/new dropdown must offer every row of
// pricing.OpenCodeGoModels (the same slice the TUI /model picker renders)
// and keep currentValue pointing at the startup model. A curated list
// that drifts from the catalogue would let one surface offer a model the
// other can't select.
func TestACPMenu_OpenCodeCatalogue(t *testing.T) {
	for _, env := range []string{
		"DEEPSEEK_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GLM_API_KEY",
	} {
		t.Setenv(env, "")
	}
	t.Setenv("OPENCODE_API_KEY", "k")
	t.Setenv("SEEK_HOME", t.TempDir())

	menu := acpModelMenu(pricing.ModelOpenCodeV41Flash, nil)
	if len(menu) != 1 {
		t.Fatalf("menu = %+v, want exactly the model select", menu)
	}
	opts := menu[0].Options
	if len(opts) != len(pricing.OpenCodeGoModels) {
		t.Fatalf("dropdown rows = %d, catalogue = %d", len(opts), len(pricing.OpenCodeGoModels))
	}
	for i, m := range pricing.OpenCodeGoModels {
		wantValue := "opencode:" + m.ID
		if opts[i].Value != wantValue {
			t.Errorf("row %d value = %q, want %q", i, opts[i].Value, wantValue)
		}
		if wantName := m.ID + " (OpenCode Go)"; opts[i].Name != wantName {
			t.Errorf("row %d name = %q, want %q", i, opts[i].Name, wantName)
		}
	}
	if want := "opencode:" + pricing.ModelOpenCodeV41Flash; menu[0].CurrentValue != want {
		t.Errorf("currentValue = %q, want %q", menu[0].CurrentValue, want)
	}

	// Non-nil live rows replace the compiled list entirely: the refreshed
	// catalogue is the vendor's truth, so an id it dropped must leave the
	// dropdown even though the binary still ships it.
	live := []pricing.OpenCodeGoModel{
		{ID: "brand-new-model", Label: "1M ctx · Brand New"},
		{ID: pricing.ModelOpenCodeV41Flash, Label: "1M ctx · vision · lane default"},
	}
	menu = acpModelMenu("brand-new-model", live)
	if len(menu) != 1 {
		t.Fatalf("live menu = %+v, want exactly the model select", menu)
	}
	if got := len(menu[0].Options); got != len(live) {
		t.Fatalf("live rows = %d, want %d", got, len(live))
	}
	if menu[0].Options[0].Value != "opencode:brand-new-model" {
		t.Errorf("row 0 = %q, want the refreshed id", menu[0].Options[0].Value)
	}
	if menu[0].CurrentValue != "opencode:brand-new-model" {
		t.Errorf("currentValue = %q, want the refreshed startup model", menu[0].CurrentValue)
	}
}

// TestInitOpenCatalog_OffKeepsBaseline pins the failure posture: with the
// switch off (or no cache, no key) the compiled rows are what the menus
// see, and nothing reaches the network.
func TestInitOpenCatalog_OffKeepsBaseline(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("SEEK_OPENCODE_CATALOG", "off")
	oldCat := openCatalog
	t.Cleanup(func() { openCatalog = oldCat })

	initOpenCatalog()
	if openCatalog == nil {
		t.Fatal("initOpenCatalog must seed a catalogue even when disabled")
	}
	if openCatalog.APIKey != "" || openCatalog.Bearer != nil {
		t.Errorf("catalogue credentials set with the switch off: key=%q bearer=%v",
			openCatalog.APIKey, openCatalog.Bearer != nil)
	}
	rows := openCatalogRows()
	if len(rows) != len(pricing.OpenCodeGoModels) {
		t.Errorf("rows = %d, want the compiled baseline %d", len(rows), len(pricing.OpenCodeGoModels))
	}
}

// TestInitOpenCatalog_UsesFreshCache: a warm start installs the cached
// snapshot before any network work, so the menu is complete offline.
func TestInitOpenCatalog_UsesFreshCache(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("SEEK_OPENCODE_CATALOG", "")
	oldCat := openCatalog
	t.Cleanup(func() { openCatalog = oldCat })

	cached := opencatalog.Snapshot{
		FetchedAt: time.Now().Add(-time.Hour),
		Models: []opencatalog.Model{
			{ID: "cached-model", Label: "cached", Context: 1_000_000},
		},
	}
	if err := opencatalog.SaveCached(cached); err != nil {
		t.Fatalf("SaveCached: %v", err)
	}
	initOpenCatalog()
	rows := openCatalogRows()
	if len(rows) != 1 || rows[0].ID != "cached-model" {
		t.Fatalf("rows = %+v, want the cached snapshot", rows)
	}
}

// cronAutoTickEnabled has two independent off-switches: --no-cron-tick
// (explicit opt-out) and the ACP headless lane (always off — its processes
// are short-lived spawns where a detached tick goroutine is a half-fired
// cron job risk, PRD feature-paseo-provider.md §2.3). The default case
// (interactive TUI, no flag) MUST stay on: startup auto-tick is what makes
// routines fire "when seek IS open" without an OS scheduler.
func TestCronAutoTickEnabled(t *testing.T) {
	tests := []struct {
		name       string
		noCronTick bool
		acpLane    bool
		want       bool
	}{
		{"interactive default: tick fires", false, false, true},
		{"--no-cron-tick opts out", true, false, false},
		{"acp headless lane skips", false, true, false},
		{"acp lane with flag (flag inexpressible there): still off", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cronAutoTickEnabled(tt.noCronTick, tt.acpLane); got != tt.want {
				t.Errorf("cronAutoTickEnabled(noCronTick=%v, acpLane=%v) = %v, want %v",
					tt.noCronTick, tt.acpLane, got, tt.want)
			}
		})
	}
}

// mcpSectionFromBridges is the MCP-D3 wiring boundary: bridges from
// LoadServers plus the parsed mcp.json go in, the sysprompt MCPSection
// bytes come out. The copy bytes themselves are locked by
// internal/tools/mcptool's own suite; what's proven here is the three
// seams that suite cannot see — (1) an mcp.json "hint" reaches the
// section and beats the built-in semble copy, (2) an explicit
// "mcp_prompt": false yields an empty section (tools register before
// this call, so only the guidance disappears — the A2 eval arm), and
// (3) server blocks are byte-stable regardless of bridge order while
// each server's tools keep bridge registration order.
func TestMCPSectionFromBridges(t *testing.T) {
	bridges := func() []*mcptool.Bridge {
		return []*mcptool.Bridge{
			mcptool.New(nil, "semble", mcp.ToolDef{
				Name:        "search",
				Description: "Search code in a repo using hybrid BM25 + semantic retrieval.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"},"query":{"type":"string"}},"required":["repo","query"]}`),
			}, "semble__search"),
			mcptool.New(nil, "semble", mcp.ToolDef{
				Name:        "find_related",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"},"line":{"type":"integer"}},"required":["file","line"]}`),
			}, "semble__find_related"),
			mcptool.New(nil, "zeta", mcp.ToolDef{Name: "search"}, "zeta__search"),
		}
	}
	noPromptConfig := func() mcpconfig.Config {
		servers := map[string]mcpconfig.ServerEntry{"semble": {}, "zeta": {}}
		return mcpconfig.Config{MCPServers: servers}
	}

	t.Run("no hint: built-in semble copy, tools in registration order", func(t *testing.T) {
		got := mcpSectionFromBridges(bridges(), noPromptConfig())
		if !strings.Contains(got, "- semble (semantic code search, local & indexed):") {
			t.Errorf("built-in semble copy missing:\n%s", got)
		}
		if !strings.Contains(got, "  Tools: semble__search(repo, query), semble__find_related(file, line).") {
			t.Errorf("tools line wrong or not in registration order:\n%s", got)
		}
		// zeta is unknown to seek and has no hint → tier-3 listing, and
		// dictionary order puts semble before zeta.
		if !strings.Contains(got, "- zeta (external MCP server):") {
			t.Errorf("tier-3 listing missing for zeta:\n%s", got)
		}
		if si, zi := strings.Index(got, "- semble"), strings.Index(got, "- zeta"); !(si >= 0 && si < zi) {
			t.Errorf("servers not in dictionary order (semble@%d zeta@%d):\n%s", si, zi, got)
		}
	})

	t.Run("hint from mcp.json overrides built-in copy", func(t *testing.T) {
		cfg := noPromptConfig()
		entry := cfg.MCPServers["semble"]
		entry.Hint = "Prefer semble for intent queries\nnot for exact symbols"
		cfg.MCPServers["semble"] = entry

		got := mcpSectionFromBridges(bridges(), cfg)
		if !strings.Contains(got, "- semble:\n") {
			t.Errorf("hinted server block missing:\n%s", got)
		}
		if !strings.Contains(got, "  Prefer semble for intent queries\n  not for exact symbols\n") {
			t.Errorf("hint not rendered verbatim:\n%s", got)
		}
		if strings.Contains(got, "semantic code search") {
			t.Errorf("built-in copy leaked through a tier-1 hint:\n%s", got)
		}
	})

	t.Run("mcp_prompt false yields empty section", func(t *testing.T) {
		cfg := noPromptConfig()
		off := false
		cfg.MCPPrompt = &off
		if got := mcpSectionFromBridges(bridges(), cfg); got != "" {
			t.Errorf("mcp_prompt:false must suppress the section, got:\n%s", got)
		}
		// Explicit true is the same as absent: the section is assembled.
		on := true
		cfg.MCPPrompt = &on
		if got := mcpSectionFromBridges(bridges(), cfg); got == "" {
			t.Error("mcp_prompt:true must keep the section")
		}
	})

	t.Run("bridge order does not change bytes", func(t *testing.T) {
		in := bridges()
		want := mcpSectionFromBridges(in, noPromptConfig())
		// Shuffle across servers only: within one server, registration
		// order is part of the contract (asserted above).
		shuffled := []*mcptool.Bridge{in[2], in[0], in[1]}
		if got := mcpSectionFromBridges(shuffled, noPromptConfig()); got != want {
			t.Errorf("bridge order leaked into the section:\n--- want ---\n%s\n--- got ---\n%s", want, got)
		}
	})

	t.Run("empty bridges yield empty section", func(t *testing.T) {
		if got := mcpSectionFromBridges(nil, noPromptConfig()); got != "" {
			t.Errorf("mcpSectionFromBridges(nil) = %q, want empty", got)
		}
		if got := mcpSectionFromBridges([]*mcptool.Bridge{}, noPromptConfig()); got != "" {
			t.Errorf("mcpSectionFromBridges(empty) = %q, want empty", got)
		}
	})
}

// TestHelpRequested pins the pre-flag.Parse scan that routes -h to
// stdout/exit 0: anywhere before the "--" terminator counts, past it
// nothing does (there it is a positional, and flag.Parse owns it).
func TestHelpRequested(t *testing.T) {
	for _, args := range [][]string{
		{"-h"},
		{"--help"},
		{"help"},
		{"--frontend=web", "-h"},
		{"-h", "-yolo"},
	} {
		if !helpRequested(args) {
			t.Errorf("helpRequested(%v) = false, want true", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{"-p", "hello"},
		{"-hx"},
		{"-help"}, // flag.Parse's own special case, not this scan's
		{"--", "-h"},
		{"--frontend=web"},
	} {
		if helpRequested(args) {
			t.Errorf("helpRequested(%v) = true, want false", args)
		}
	}
}

// TestPrintUsage_ListsCommands guards the discovery surface `seek -h`:
// a flags-only listing (flag.PrintDefaults alone) is what made the verb
// surface — serve included — look like the whole CLI. Commands must
// print, before the flag half.
func TestPrintUsage_ListsCommands(t *testing.T) {
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	for _, want := range []string{
		"Commands:",
		"serve",
		"--frontend=web",
		"autopilot",
		"Flags:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("usage missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "Commands:") > strings.Index(out, "Flags:") {
		t.Error("the commands block must come before the flag listing")
	}
}
