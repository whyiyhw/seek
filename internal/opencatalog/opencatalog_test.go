package opencatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/internal/modelcap"
	"github.com/whyiyhw/seek/internal/pricing"
)

// resetOverrides clears the runtime layers these tests install, so one
// test's catalogue can't price the next one's turns.
func resetOverrides(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		pricing.SetModelRates(nil)
		budget.SetLimits(nil)
		modelcap.SetVision(nil)
	})
}

// samplePayload is a trimmed copy of the real GET /v1/models shape: one
// metered chat model with a peak multiplier, one free chat model, one
// Responses-only model (must be dropped), one non-chat entry (dropped),
// and one model with no id (dropped).
const samplePayload = `{
  "object": "list",
  "data": [
    {"id": "deepseek-v4.1-flash", "object": "model", "name": "DeepSeek V4.1 Flash 1017", "type": "chat",
     "api": {"chat-completions": {"base_url": "https://opencode.ai/inference/go/openai/v1"}},
     "cost": {"text": {"input": {"price": 0.15, "cache": {"read": 0.003}}, "output": {"price": 0.6}},
              "multipliers": [{"when": {}, "factor": 2}]},
     "limit": {"context": 1000000, "output": 384000},
     "modalities": {"input": ["text", "image/png"], "output": ["text"]}},
    {"id": "brand-new-model", "object": "model", "name": "Brand New", "type": "chat",
     "api": {"chat-completions": {}},
     "cost": {"text": {"input": {"price": 1.5, "cache": {"read": 0.2}}, "output": {"price": 6}}},
     "limit": {"context": 262144},
     "modalities": {"input": ["text"]}},
    {"id": "beam-free", "object": "model", "name": "Beam Free", "type": "chat",
     "api": {"chat-completions": {}},
     "cost": {"text": {"input": {"price": 0, "cache": {"read": 0}}, "output": {"price": 0}}},
     "limit": {"context": 1048576},
     "modalities": {"input": ["text"]}},
    {"id": "gpt-6-luna", "object": "model", "name": "GPT 6 Luna", "type": "chat",
     "api": {"responses": {}},
     "cost": {"text": {"input": {"price": 0.1}, "output": {"price": 0.4}}},
     "limit": {"context": 1050000}},
    {"id": "gpt-embed", "object": "model", "name": "Embed", "type": "embedding",
     "api": {"chat-completions": {}}},
    {"id": "", "object": "model", "name": "No id", "type": "chat",
     "api": {"chat-completions": {}}}
  ]
}`

func TestParse_FiltersAndNormalises(t *testing.T) {
	now := time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC)
	snap, err := Parse([]byte(samplePayload), now)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !snap.FetchedAt.Equal(now) {
		t.Errorf("FetchedAt = %v, want %v", snap.FetchedAt, now)
	}
	byID := map[string]Model{}
	for _, m := range snap.Models {
		byID[m.ID] = m
	}
	if len(byID) != 3 {
		t.Fatalf("models = %d (%v), want 3 chat-completions rows only", len(byID), byID)
	}
	if _, ok := byID["gpt-6-luna"]; ok {
		t.Error("responses-only model must be filtered out — the compatible adapter can't speak that wire")
	}
	if _, ok := byID["gpt-embed"]; ok {
		t.Error("non-chat model must be filtered out")
	}

	flash := byID["deepseek-v4.1-flash"]
	// The multiplier is a peak surcharge on the base price: the card
	// stores the peak figure (0.15×2, 0.003×2, 0.6×2) to err high.
	if flash.Input != 0.30 || flash.Hit != 0.006 || flash.Output != 1.20 {
		t.Errorf("flash rates = %v/%v/%v, want peak 0.30/0.006/1.20", flash.Input, flash.Hit, flash.Output)
	}
	if flash.Context != 1_000_000 {
		t.Errorf("flash context = %d, want 1000000", flash.Context)
	}
	if !strings.Contains(flash.Label, "1M ctx") || !strings.Contains(flash.Label, "vision") {
		t.Errorf("flash label = %q, want ctx + vision markers", flash.Label)
	}
	if flash.Free {
		t.Error("flash must not be marked free")
	}
	// Vision rides the endpoint's own modalities declaration — this is
	// what makes an OpenCode model seek has never heard of usable for
	// image pastes without waiting for a release.
	if !flash.Vision {
		t.Error("deepseek-v4.1-flash declares image/png input → Vision must be true")
	}
	if byID["beam-free"].Vision {
		t.Error("beam-free declares text-only modalities → Vision must be false")
	}
	if byID["brand-new-model"].Vision {
		t.Error("brand-new-model declares text-only modalities → Vision must be false")
	}

	beam := byID["beam-free"]
	if !beam.Free {
		t.Error("zero-price model must be marked free")
	}
	if !strings.Contains(beam.Label, "free") {
		t.Errorf("free label = %q, want a free prefix", beam.Label)
	}

	// Embedded ordering wins: the lane default comes first even though the
	// payload lists a brand-new model second and it sorts later.
	if snap.Models[0].ID != pricing.ModelOpenCodeV41Flash {
		t.Errorf("first row = %q, want the embedded default %q", snap.Models[0].ID, pricing.ModelOpenCodeV41Flash)
	}
}

func TestParse_Errors(t *testing.T) {
	if _, err := Parse([]byte(`{ not json`), time.Now()); err == nil {
		t.Error("malformed JSON must error — installing it would blank the menu")
	}
	if _, err := Parse([]byte(`{"data":[]}`), time.Now()); err == nil {
		t.Error("empty list must error, not install an empty catalogue")
	}
	if _, err := Parse([]byte(`{"data":[{"id":"x","type":"chat","api":{"responses":{}}}]}`), time.Now()); err == nil {
		t.Error("a list with no chat-completions rows must error (shape change, not an empty pool)")
	}
}

func TestMerge_DropsRemovedAndAppendsNew(t *testing.T) {
	// A model the embedded list knows but the endpoint no longer returns
	// must disappear (it was deprecated); a new id lands at the end.
	fetched := []Model{
		{ID: "brand-new-model", Label: "new"},
		{ID: pricing.ModelOpenCodeV41Flash, Label: "flash"},
	}
	out := merge(pricing.OpenCodeGoModels, fetched)
	if len(out) != 2 {
		t.Fatalf("merged rows = %d, want 2 (embedded ids not in the payload are dropped)", len(out))
	}
	if out[0].ID != pricing.ModelOpenCodeV41Flash {
		t.Errorf("row 0 = %q, want the embedded default first", out[0].ID)
	}
	if out[1].ID != "brand-new-model" {
		t.Errorf("row 1 = %q, want the new id appended", out[1].ID)
	}
	for _, m := range out {
		if m.ID == "kimi-k3" {
			t.Error("a removed model must not survive the merge")
		}
	}
}

func TestLabel_TruncatesToPickerWidth(t *testing.T) {
	long := strings.Repeat("x", 200)
	w := wireModel{Name: long}
	w.Limit.Context = 1_000_000
	got := labelFor(w, true)
	if n := len([]rune(got)); n > maxLabelRunes {
		t.Errorf("label = %d runes, want <= %d (picker row budget)", n, maxLabelRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated label = %q, want an ellipsis", got)
	}
}

func TestInstall_RatesAndLimits(t *testing.T) {
	resetOverrides(t)
	snap, err := Parse([]byte(samplePayload), time.Now())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := New()
	c.Install(snap)

	// The new id must now price through its own card, not the Flash
	// fallback — and it must be flat-rate: a subscription lane has no
	// peak clock, so off-peak must equal standard.
	std := pricing.PricingFor("brand-new-model", pricing.TierStandard)
	if std.InputMissPerMTok != 1.5 || std.OutputPerMTok != 6 {
		t.Errorf("brand-new-model card = %+v, want 1.5/6", std)
	}
	if !std.NoOffPeakDiscount {
		t.Error("installed cards must be forced flat-rate")
	}
	if off := pricing.PricingFor("brand-new-model", pricing.TierOffPeak); off != std {
		t.Errorf("off-peak %+v differs from standard %+v", off, std)
	}
	if _, ok := pricing.LookupRate("brand-new-model"); !ok {
		t.Error("LookupRate must find the installed card")
	}

	// Context windows ride along, so /compact nags at the real size.
	if got := budget.Limit("brand-new-model"); got != 262_144 {
		t.Errorf("context = %d, want 262144", got)
	}

	// Direct-lane ids keep their own card (the registry is keyed by wire
	// id, so overwriting would misprice the other lane's turns).
	if got := budget.Limit("glm-5.3"); got != 1_000_000 {
		t.Errorf("glm-5.3 context = %d, want the compiled 1M", got)
	}
}

// TestInstall_VisionGrants: the catalogue's modalities answer must reach
// internal/modelcap for every row — including ids the compiled table
// never heard of, and including direct-lane ids (a capability is not a
// price card; the skip that protects the rate table must not protect
// this one).
func TestInstall_VisionGrants(t *testing.T) {
	resetOverrides(t)
	if modelcap.Vision("kimi-k3") {
		t.Fatal("kimi-k3 must not be vision before the catalogue installs")
	}
	payload := `{"data":[
	  {"id":"kimi-k3","type":"chat","api":{"chat-completions":{}},
	   "cost":{"text":{"input":{"price":3,"cache":{"read":0.3}},"output":{"price":15}}},
	   "limit":{"context":1048576},
	   "modalities":{"input":["text","image/png"],"output":["text"]}},
	  {"id":"deepseek-v4-pro","type":"chat","api":{"chat-completions":{}},
	   "cost":{"text":{"input":{"price":1.32,"cache":{"read":0.044}},"output":{"price":3.96}}},
	   "limit":{"context":1000000},
	   "modalities":{"input":["text","image/jpeg"],"output":["text"]}}
	]}`
	snap, err := Parse([]byte(payload), time.Now())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	New().Install(snap)

	if !modelcap.Vision("kimi-k3") {
		t.Error("kimi-k3: catalogue declared image input but modelcap says no")
	}
	if !modelcap.Vision("deepseek-v4-pro") {
		t.Error("deepseek-v4-pro: vision grant must not be skipped with the rate card")
	}
	// The compiled POSITIVE rows must survive an install that doesn't
	// mention them (grants are additive — an older cache has no vision
	// key at all, so absence cannot mean "no").
	if !modelcap.Vision("deepseek-flash") {
		t.Error("a catalogue that doesn't list deepseek-flash must not demote it")
	}
}

func TestInstall_EmptySnapshotIgnored(t *testing.T) {
	resetOverrides(t)
	c := New()
	before := len(c.Rows())
	c.Install(Snapshot{})
	if got := len(c.Rows()); got != before {
		t.Errorf("rows = %d after installing an empty snapshot, want %d", got, before)
	}
}

func TestSnapshotFreshness(t *testing.T) {
	now := time.Now()
	fresh := Snapshot{FetchedAt: now.Add(-time.Hour), Models: []Model{{ID: "x"}}}
	if !fresh.Fresh(now) {
		t.Error("1h-old snapshot must be fresh")
	}
	stale := Snapshot{FetchedAt: now.Add(-(cacheTTL + time.Minute)), Models: []Model{{ID: "x"}}}
	if stale.Fresh(now) {
		t.Error("25h-old snapshot must not be fresh")
	}
	if !stale.Usable(now) {
		t.Error("a 25h-old snapshot is still usable (staleTTL is a week)")
	}
	ancient := Snapshot{FetchedAt: now.Add(-(staleTTL + time.Hour)), Models: []Model{{ID: "x"}}}
	if ancient.Usable(now) {
		t.Error("a week-plus-old snapshot must fall back to the embedded baseline")
	}
	if (Snapshot{FetchedAt: now, Models: []Model{}}).Usable(now) {
		t.Error("an empty snapshot is never usable")
	}
}

func TestCache_RoundTripCorruptAndMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	if _, ok := LoadCached(); ok {
		t.Error("missing cache must report ok=false")
	}
	snap := Snapshot{FetchedAt: time.Unix(1_700_000_000, 0).UTC(), Models: []Model{{ID: "x", Label: "X"}}}
	if err := SaveCached(snap); err != nil {
		t.Fatalf("SaveCached: %v", err)
	}
	got, ok := LoadCached()
	if !ok || len(got.Models) != 1 || got.Models[0].ID != "x" {
		t.Fatalf("round trip = %+v ok=%v", got, ok)
	}
	// No .tmp left behind (SaveCached writes <path>.tmp then renames).
	if _, err := os.Stat(filepath.Join(home, "opencode-catalog.json.tmp")); err == nil {
		t.Error("temp file left behind")
	}

	// Corrupt cache never blocks startup.
	p, _ := CachePath()
	if err := os.WriteFile(p, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadCached(); ok {
		t.Error("corrupt cache must report ok=false")
	}
}

func TestFetch_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("Authorization = %q, want Bearer k", got)
		}
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "seek") {
			t.Errorf("User-Agent = %q, want it to name seek", ua)
		}
		_, _ = w.Write([]byte(samplePayload))
	}))
	defer srv.Close()

	snap, err := Fetch(context.Background(), srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(snap.Models) != 3 {
		t.Errorf("models = %d, want 3", len(snap.Models))
	}
}

func TestFetch_NoKeyIsAnError(t *testing.T) {
	// A keyless list contains every public model, not the workspace's —
	// installing that would offer models the key can't call.
	if _, err := Fetch(context.Background(), "https://example.invalid/v1/models", "", nil); err == nil {
		t.Error("empty api key must error without making a request")
	}
}

func TestFetch_HTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"authentication_error"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), srv.URL, "k", srv.Client()); err == nil {
		t.Error("401 must be an error")
	}

	// Cancellation: a hung endpoint must not outlive the caller's ctx.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Fetch(ctx, slow.URL, "k", slow.Client()); err == nil {
		t.Error("cancelled context must be an error")
	}
}

func TestCatalogue_RefreshInstallsAndCaches(t *testing.T) {
	resetOverrides(t)
	t.Setenv("SEEK_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(samplePayload))
	}))
	defer srv.Close()

	c := New()
	c.URL = srv.URL
	c.APIKey = "k"
	n, err := c.Refresh(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if n != 3 {
		t.Errorf("installed %d rows, want 3", n)
	}
	if _, ok := pricing.LookupRate("brand-new-model"); !ok {
		t.Error("refresh must install rates")
	}
	if _, ok := LoadCached(); !ok {
		t.Error("refresh must write the cache")
	}

	// A failed refresh leaves the installed catalogue untouched.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer bad.Close()
	c.URL = bad.URL
	before := len(c.Rows())
	if _, err := c.Refresh(context.Background(), bad.Client()); err == nil {
		t.Error("500 must be an error")
	}
	if got := len(c.Rows()); got != before {
		t.Errorf("rows = %d after a failed refresh, want %d", got, before)
	}
}

func TestCatalogue_ConcurrentReadsDuringInstall(t *testing.T) {
	resetOverrides(t)
	snap, err := Parse([]byte(samplePayload), time.Now())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := New()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.Rows()
				_ = c.Snapshot()
				_ = pricing.PricingFor(pricing.ModelOpenCodeV41Flash, pricing.TierStandard)
				_ = budget.Limit(pricing.ModelOpenCodeV41Flash)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			c.Install(snap)
		}
	}()
	wg.Wait()
}

func TestEmbedded_MatchesCompiledTables(t *testing.T) {
	resetOverrides(t)
	snap := Embedded()
	if len(snap.Models) != len(pricing.OpenCodeGoModels) {
		t.Fatalf("embedded rows = %d, catalogue = %d", len(snap.Models), len(pricing.OpenCodeGoModels))
	}
	for _, m := range snap.Models {
		if m.Label == "" {
			t.Errorf("%s: empty label", m.ID)
		}
		if m.Context == 0 {
			t.Errorf("%s: no context window — budget.Known missed it", m.ID)
		}
		if strings.HasSuffix(m.ID, "-free") != m.Free {
			t.Errorf("%s: Free = %v, want the -free suffix rule", m.ID, m.Free)
		}
	}
}

func TestEnabled_Switch(t *testing.T) {
	for _, v := range []string{"off", "0", "false", "no", "OFF"} {
		t.Setenv("SEEK_OPENCODE_CATALOG", v)
		if Enabled() {
			t.Errorf("SEEK_OPENCODE_CATALOG=%q must disable the catalogue", v)
		}
	}
	for _, v := range []string{"", "1", "on", "true"} {
		t.Setenv("SEEK_OPENCODE_CATALOG", v)
		if !Enabled() {
			t.Errorf("SEEK_OPENCODE_CATALOG=%q must keep the catalogue enabled", v)
		}
	}
}
