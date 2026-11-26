package main

// c_dxrjjl acceptance, model×lane consistency on the ACP startup path.
//
// Background (measured 2026-11-25): a paseo-driven `seek acp` child was
// spawned with the session model only (`deepseek-v4.1-flash-eco`, an
// OpenDesign Cloud gateway wire id) and no provider pin, so the lane fell
// to buildProvider's auto-detect (deepseek) and the official API answered
// `invalid_request_error: The supported API model names are deepseek-flash,
// deepseek-v4-pro, but you passed deepseek-v4.1-flash-eco`. The same id on
// amr-link.open-design.ai answers 200 — the model was never gone; the
// request was on the wrong lane.
//
// The gate under test is acpStartupLaneGate. Three shapes matter:
//   - unknown id (a compatible endpoint's own namespace) → never refused;
//   - gateway id + the gateway's own lane → allowed;
//   - gateway id + a lane that cannot serve it → refused, naming the lane.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/config"
	"github.com/whyiyhw/seek/internal/modelcap"
	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// TestACPStartupLaneGate is the table-driven core: what the gate answers
// for each (model, provider-pin, config) shape. cfg is passed as a value so
// every row is hermetic (no env, no ~/.seek read).
func TestACPStartupLaneGate(t *testing.T) {
	const eco = "deepseek-v4.1-flash-eco" // OpenDesign-exclusive wire id
	dsKey := func() config.Config {
		return config.Config{Providers: map[string]config.ProviderConfig{"deepseek": {APIKey: "k"}}}
	}
	odKey := func() config.Config {
		return config.Config{Providers: map[string]config.ProviderConfig{"opendesign": {APIKey: "sk-amr-k"}}}
	}

	cases := []struct {
		name    string
		model   string
		provPin string
		cfg     config.Config
		wantErr string // substring; "" = gate must pass
	}{
		{
			name: "no model pin passes (lane default is faithful by construction)",
			cfg:  dsKey(),
		},
		{
			// ACCEPTANCE ①: the reported failure shape — model only, no
			// provider pin, DeepSeek key present → auto-detect lands on
			// deepseek and the gate must refuse, naming the lane.
			name:    "gateway id + auto-detect deepseek refused",
			model:   eco,
			cfg:     dsKey(),
			wantErr: `model "deepseek-v4.1-flash-eco" belongs to lane "opendesign"`,
		},
		{
			name:    "gateway id + explicit deepseek refused",
			model:   eco,
			provPin: "deepseek",
			cfg:     odKey(),
			wantErr: `set SEEK_ACP_PROVIDER=opendesign`,
		},
		{
			name:    "gateway id + wrong gateway refused",
			model:   eco,
			provPin: "opencode",
			cfg:     odKey(),
			wantErr: `belongs to lane "opendesign"`,
		},
		{
			// ACCEPTANCE ② (gate half): the correct pin is a pass.
			name:    "gateway id + its own lane allowed",
			model:   eco,
			provPin: "opendesign",
			cfg:     odKey(),
		},
		{
			// The OpenDesign lane's DEFAULT model is a shared id, which
			// LaneOf resolves to the Go lane (the older claim). Serves()
			// is what keeps this legal — LaneOf alone would refuse the
			// lane's own default model.
			name:    "openDesign default (shared id) allowed",
			model:   pricing.ModelOpenDesignV41Flash,
			provPin: "opendesign",
			cfg:     odKey(),
		},
		{
			// A gateway deepseek id is refused on the official lane even
			// when it LOOKS like the same vendor family: the API's
			// accepted names are a closed set (deepseek-flash,
			// deepseek-v4-pro), which is the failure this gate exists
			// for. Same id on its own lane is a pass (next row).
			name:    "gateway fleet id + auto-detect deepseek refused",
			model:   "deepseek-v4.1-flash",
			cfg:     dsKey(),
			wantErr: `belongs to lane "opencode"`,
		},
		{
			name:    "gateway fleet id + its own gateway allowed",
			model:   "deepseek-v4.1-flash",
			provPin: "opencode",
			cfg:     odKey(),
		},
		{
			name:  "first-tier curated id passes",
			model: "deepseek-flash",
			cfg:   dsKey(),
		},
		{
			// ACCEPTANCE ③: an unknown id is the operator's namespace
			// (--provider=compatible / self-hosted mirror). Never refused.
			name:    "unknown compatible id passes",
			model:   "my-local-ollama-model",
			provPin: "compatible",
		},
		{
			name:  "unknown id passes even with no provider pin",
			model: "totally-unknown-id",
			cfg:   dsKey(),
		},
		{
			name:    "compatible endpoint takes a gateway-looking id (declared namespace)",
			model:   eco,
			provPin: "compatible",
		},
		{
			// A gateway-menu id that also matches the provider's own
			// vendor family is tolerated: seek has no per-vendor list to
			// prove the first tier can't serve it.
			name:    "vendor-family id on its own first-tier lane tolerated",
			model:   "glm-5.3-flash",
			provPin: "glm",
		},
		{
			// …but a DEEPSEEK gateway id is not tolerated: the official
			// API's accepted names are a closed set, which is the failure
			// this gate exists for.
			name:    "gateway deepseek id on the GLM lane refused",
			model:   eco,
			provPin: "glm",
			wantErr: `belongs to lane "opendesign"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEEPSEEK_API_KEY", "")
			t.Setenv("OPENDESIGN_API_KEY", "")
			t.Setenv("OPENCODE_API_KEY", "")
			t.Setenv("GLM_API_KEY", "")

			err := acpStartupLaneGate(tc.model, tc.provPin, tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("gate refused a legal pair: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("gate accepted %q on provider %q — want a refusal containing %q",
					tc.model, tc.provPin, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestACPStartupLaneGate_AutoDetectMatchesBuildProvider pins the shared
// resolution: the gate must predict the lane buildProvider would build.
// Two copies of the priority chain would let the gate and the built lane
// disagree, which is the class of bug this card closes.
func TestACPStartupLaneGate_AutoDetectMatchesBuildProvider(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("OPENCODE_API_KEY", "")
	t.Setenv("OPENDESIGN_API_KEY", "sk-amr-k")

	cfg := loadGateConfig()
	if got := resolveProviderName("", cfg); got != "opendesign" {
		t.Fatalf("resolveProviderName(auto) = %q, want opendesign (the only key present)", got)
	}
	// The gate therefore allows the OpenDesign-exclusive eco id here…
	if err := acpStartupLaneGate("deepseek-v4.1-flash-eco", "", cfg); err != nil {
		t.Fatalf("auto-detect lands on opendesign, so eco is legal: %v", err)
	}
	// …and refuses once a DeepSeek key takes over the auto-detect order.
	t.Setenv("DEEPSEEK_API_KEY", "k")
	cfg = loadGateConfig()
	if got := resolveProviderName("", cfg); got != "deepseek" {
		t.Fatalf("resolveProviderName(auto with deepseek key) = %q, want deepseek", got)
	}
	if err := acpStartupLaneGate("deepseek-v4.1-flash-eco", "", cfg); err == nil {
		t.Fatal("auto-detect now lands on deepseek — the eco id must be refused")
	}
}

// TestACPStartupLaneGate_OpenDesignLaneServesEco is ACCEPTANCE ②'s
// construction-level half: with SEEK_ACP_PROVIDER=opendesign + the eco
// model, the lane that gets built is the OpenDesign gateway and the
// request that leaves carries the eco id — httptest backend, no real key.
// This proves the gate is not merely permitting a pair the lane would fail
// to serve. Endpoint override rides providers.opendesign.base_url, the
// same resolver production uses.
func TestACPStartupLaneGate_OpenDesignLaneServesEco(t *testing.T) {
	const eco = "deepseek-v4.1-flash-eco"
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("OPENDESIGN_API_KEY", "sk-amr-e2e")
	t.Setenv("OPENDESIGN_BASE_URL", "")

	b := newFakeLane(t, glmSSE) // any single-turn SSE shape works here
	cfgJSON := `{"providers": {"opendesign": {"api_key": "sk-amr-e2e", "base_url": "` + b.srv.URL + `"}}}`
	if err := os.WriteFile(filepath.Join(os.Getenv("SEEK_HOME"), "config.json"), []byte(cfgJSON), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// The gate passes the correct pin, and — the point of this test — the
	// gate can PASS and the lane still serves: the two halves are asserted
	// independently so a gate that approved a pair the builder rejects
	// would fail here.
	if err := acpStartupLaneGate(eco, "opendesign", loadGateConfig()); err != nil {
		t.Fatalf("gate refused opendesign+eco: %v", err)
	}
	// …and the lane it builds really addresses eco on the gateway.
	proc, dsClient, label, modelDefault, err := buildProvider("opendesign", "", "")
	if err != nil {
		t.Fatalf("buildProvider(opendesign): %v", err)
	}
	if dsClient != nil {
		t.Fatal("opendesign must not build a deepseek client")
	}
	if label != "OpenDesign" {
		t.Errorf("label = %q, want OpenDesign", label)
	}
	if modelDefault != pricing.ModelOpenDesignV41Flash {
		t.Errorf("modelDefault = %q, want %q", modelDefault, pricing.ModelOpenDesignV41Flash)
	}

	ch, err := proc.ChatStream(context.Background(), llm.ChatRequest{
		Model:    eco,
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	for range ch {
	}
	models, _ := b.requests()
	if len(models) != 1 || len(models[0]) != 1 || models[0][0] != eco {
		t.Errorf("gateway saw models %v, want [[%s]]", models, eco)
	}
}

// TestACPStartupLaneGate_ExitsThroughTheRealBinary is the end-to-end
// shape: the compiled binary on the `seek acp` lane with the model pinned
// and no provider pin exits non-zero and says which lane the id belongs to.
// No backend is needed — the launch dies at the gate, before any request.
func TestACPStartupLaneGate_ExitsThroughTheRealBinary(t *testing.T) {
	workDir := t.TempDir()
	bin := buildSeekBin(t)
	cmd := exec.Command(bin, "acp")
	cmd.Dir = workDir
	cmd.Env = []string{
		"SEEK_HOME=" + workDir,
		"HOME=" + workDir,
		"PATH=" + os.Getenv("PATH"),
		"DEEPSEEK_API_KEY=e2e-key", // makes auto-detect land on deepseek
		"ANTHROPIC_API_KEY=",
		"OPENAI_API_KEY=",
		"GEMINI_API_KEY=",
		"GLM_API_KEY=",
		"OPENCODE_API_KEY=",
		"OPENDESIGN_API_KEY=",
		"SEEK_ACP_MODEL=deepseek-v4.1-flash-eco",
		"SEEK_ACP_PROVIDER=",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("binary exited 0; want a startup refusal naming the lane\noutput:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("not an ExitError: %v\noutput:\n%s", err, out)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("exit code 0, want non-zero\noutput:\n%s", out)
	}
	text := string(out)
	for _, want := range []string{`belongs to lane "opendesign"`, "SEEK_ACP_PROVIDER=opendesign"} {
		if !strings.Contains(text, want) {
			t.Errorf("stderr lacks %q:\n%s", want, text)
		}
	}
	// Nothing must have been attempted upstream: the message is seek's
	// own ("acp startup:"), not an API error body.
	if strings.Contains(text, "invalid_request_error") {
		t.Errorf("startup still reached the upstream API:\n%s", text)
	}
}

// TestLaneOfServesCompanion pins the LaneOf/Serves split the gate relies on
// — the difference between "which lane owns this id" (display) and "can
// this lane address it" (routing).
func TestLaneOfServesCompanion(t *testing.T) {
	const eco = "deepseek-v4.1-flash-eco"
	if lane, _ := modelcap.LaneOf(eco); lane != modelcap.LaneOpenDesign {
		t.Fatalf("LaneOf(%q) = %q, want opendesign", eco, lane)
	}
	if !modelcap.Serves(modelcap.LaneOpenDesign, eco) {
		t.Error("Serves(opendesign, eco) = false — the gate's pass branch would be dead")
	}
	if modelcap.Serves(modelcap.LaneOpenCode, eco) {
		t.Error("Serves(opencode, eco) = true — eco is not on the Go menu")
	}
	// The shared default resolves to the Go lane by the older-claim rule,
	// yet BOTH gateways serve it — this is why the gate needs Serves.
	if lane, _ := modelcap.LaneOf(pricing.ModelOpenDesignV41Flash); lane != modelcap.LaneOpenCode {
		t.Errorf("LaneOf(shared) = %q, want the older claim opencode", lane)
	}
	if !modelcap.Serves(modelcap.LaneOpenDesign, pricing.ModelOpenDesignV41Flash) {
		t.Error("the OpenDesign lane must serve its own default model")
	}
	if modelcap.Serves("deepseek", eco) {
		t.Error("Serves(deepseek, eco) = true — first-tier lanes have no gateway menu")
	}
}

// TestACPLoadSession_RestoresOpenDesignLane is the resume half of the same
// defect: a transcript whose recorded model is the eco id used to restore on
// the CURRENT lane (ResolveRoute had no OpenDesign entry, so applyLane fell
// through to SetModel) — which is exactly the paseo "restart the session and
// it still 400s" shape. With the lane in the routing table the load now
// points the agent at the gateway, and the restore even names the correct
// default when the recorded id is the lane's own.
func TestACPLoadSession_RestoresOpenDesignLane(t *testing.T) {
	const eco = "deepseek-v4.1-flash-eco"
	dir := t.TempDir()
	t.Setenv("SEEK_SESSIONS_DIR", filepath.Join(dir, "sessions"))

	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	pol, err := permission.New(dir, permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agent.New(agent.Config{
		Client:       deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL("http://unused")),
		Model:        deepseek.ModelV41Flash,
		SystemPrompt: "sys",
	})
	if err != nil {
		t.Fatal(err)
	}

	var restoredTo []string
	b := &acpBackend{
		ag:     ag,
		policy: pol,
		store:  store,
		sess:   session.New(deepseek.ModelV41Flash, dir, "sys", false, false),
		// The production closure is buildProvider("opendesign", …): a
		// second-tier provider and NO deepseek client. Faking exactly that
		// shape is what makes the assertion meaningful.
		laneFor: func(provider string) (llm.Provider, *deepseek.Client, string, error) {
			restoredTo = append(restoredTo, provider)
			if provider != "opendesign" {
				t.Errorf("laneFor(%q): the eco model must restore the opendesign lane", provider)
			}
			return fakeSecondTier{name: "OpenDesign"}, nil, eco, nil
		},
	}

	stored := session.New(eco, dir, "sys", false, false)
	stored.Messages = []deepseek.Message{{Role: deepseek.RoleUser, Content: "earlier"}}
	if err := store.Save(stored); err != nil {
		t.Fatal(err)
	}

	if _, err := b.LoadSession(acp.LoadSessionParams{SessionID: stored.ID}, func(acp.SessionUpdate) {}); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(restoredTo) != 1 || restoredTo[0] != "opendesign" {
		t.Fatalf("restored lanes = %v, want exactly [opendesign]", restoredTo)
	}
	if got := b.ag.Model(); got != eco {
		t.Errorf("agent model = %q, want %q", got, eco)
	}
}

// fakeSecondTier is the minimal llm.Provider the load test needs: applyLane
// only stores the interface, so nothing has to stream.
type fakeSecondTier struct{ name string }

func (f fakeSecondTier) ChatStream(context.Context, llm.ChatRequest) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	close(ch)
	return ch, nil
}
func (f fakeSecondTier) Name() string { return f.name }
