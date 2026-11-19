// Package opencatalog keeps the OpenCode Go lane's model list current.
//
// The lane's gateway publishes its whole catalogue — ids, per-model cost
// blocks, context windows, and which wire protocol each model speaks — at
// GET https://opencode.ai/inference/go/v1/models. That list churns on a
// days-long cadence (models appear and are deprecated week to week), so
// the compiled baseline in internal/pricing would fall out of date
// between releases. This package fetches the list, normalises it into the
// same shape the embedded catalogue uses, caches it on disk for 24h, and
// installs the result into internal/pricing / internal/budget as runtime
// overrides.
//
// Three rules shape the design:
//
//  1. **Static is the floor, dynamic is the ceiling.** Every failure —
//     no key, 403 Models›Read, timeout, non-JSON, zero usable rows,
//     corrupt cache — silently leaves the embedded list and its cards in
//     place. Nothing here can block startup or an inference call.
//  2. **Keyed fetch only.** The docs are explicit: without a key the list
//     holds every public model, with one it holds only what the key's
//     workspace can use. Offering models the workspace can't call would
//     turn the picker into a minefield, so an empty key means "don't
//     fetch", not "fetch anonymously".
//  3. **Never the prompt.** The catalogue feeds menus, the cost display
//     and the /compact threshold. It is not part of the system prompt, so
//     a refresh cannot disturb the prefix cache.
package opencatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/pricing"
)

// DefaultModelsURL is the lane's catalogue endpoint. It answers without a
// key (public list) and with one (workspace-scoped list) — see Fetch.
const DefaultModelsURL = "https://opencode.ai/inference/go/v1/models"

const (
	// cacheTTL is how long a fetched list is trusted. Same scale as the
	// upgrade check (internal/upgrade): long enough to keep startup
	// offline-friendly, short enough that a re-listed model shows up
	// within a day.
	cacheTTL = 24 * time.Hour
	// staleTTL is how long a stale cache still beats the compiled
	// baseline. A week-old price list is closer to the truth than a
	// release-old binary; older than that and the embedded table wins,
	// because the endpoint itself may have moved.
	staleTTL = 7 * 24 * time.Hour
	// maxLabelRunes matches the picker's row budget: the TUI pads the id
	// column to 32 and appends " (current)" inside ~100 columns.
	maxLabelRunes = 55
	// maxBodyBytes caps the response. The real list is ~60 KiB; the cap
	// exists so a hostile/broken endpoint can't balloon memory.
	maxBodyBytes = 1 << 20
)

// Model is one normalised catalogue row.
type Model struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Context int     `json:"context"`
	Input   float64 `json:"input_per_mtok"`
	Hit     float64 `json:"hit_per_mtok"`
	Output  float64 `json:"output_per_mtok"`
	Free    bool    `json:"free"`
}

// Snapshot is a full catalogue as of one fetch (or the embedded baseline).
type Snapshot struct {
	FetchedAt time.Time `json:"fetched_at"`
	Models    []Model   `json:"models"`
}

// Fresh reports whether the snapshot is young enough to use without a
// refresh.
func (s Snapshot) Fresh(now time.Time) bool {
	return !s.FetchedAt.IsZero() && now.Sub(s.FetchedAt) < cacheTTL
}

// Usable reports whether the snapshot is worth installing at all: fresh,
// or at least not older than staleTTL.
func (s Snapshot) Usable(now time.Time) bool {
	return !s.FetchedAt.IsZero() && now.Sub(s.FetchedAt) < staleTTL && len(s.Models) > 0
}

// directLaneIDs are ids that also belong to a first-tier lane, where the
// compiled card prices that lane's real per-token billing. The registry
// is keyed by wire id alone, so installing this lane's card for them
// would misprice the other lane's turns; their compiled entries are close
// enough for the Go display and stay untouched.
var directLaneIDs = map[string]bool{
	"glm-5.3":         true, // --provider=glm (Zhipu, CNY card)
	"deepseek-v4-pro": true, // direct DeepSeek API
}

// Install pushes the snapshot's rates and context windows into the
// pricing / budget override layers. Every card is installed flat-rate:
// the Go plan is a request-window subscription with no DeepSeek peak
// clock, and a remote cost block must never be halved by one.
func (s Snapshot) Install() {
	rates := make(map[string]pricing.ModelPricing, len(s.Models))
	limits := make(map[string]int, len(s.Models))
	for _, m := range s.Models {
		if m.ID == "" || directLaneIDs[m.ID] {
			continue
		}
		rates[m.ID] = pricing.ModelPricing{
			InputMissPerMTok: m.Input,
			InputHitPerMTok:  m.Hit,
			OutputPerMTok:    m.Output,
		}
		if m.Context > 0 {
			limits[m.ID] = m.Context
		}
	}
	pricing.SetModelRates(rates)
	budget.SetLimits(limits)
}

// Rows renders the snapshot as picker rows, in the order the catalogue
// carries them (embedded order first, new ids appended — see merge).
func (s Snapshot) Rows() []pricing.OpenCodeGoModel {
	out := make([]pricing.OpenCodeGoModel, 0, len(s.Models))
	for _, m := range s.Models {
		out = append(out, pricing.OpenCodeGoModel{ID: m.ID, Label: m.Label})
	}
	return out
}

// Catalogue is the process-wide, concurrency-safe view of the lane's
// model list. Construct with New, then Install snapshots from the cache
// and from refreshes.
type Catalogue struct {
	// URL overrides DefaultModelsURL (tests point this at an httptest
	// server). Empty means the default.
	URL string
	// APIKey is the static credential (OPENCODE_API_KEY / config).
	APIKey string
	// Bearer, when set, resolves the credential per refresh: a rotating
	// OAuth token plus the workspace id. Takes precedence over APIKey.
	Bearer func(ctx context.Context) (token, orgID string, err error)

	mu   sync.RWMutex
	snap Snapshot
}

// New returns a catalogue seeded with the embedded baseline
// (pricing.OpenCodeGoModels + their compiled cards and windows).
func New() *Catalogue {
	return &Catalogue{snap: Embedded()}
}

// Embedded builds the compiled baseline as a Snapshot: ids and labels
// from pricing.OpenCodeGoModels, rates from the pricing table, windows
// from budget, free rows detected by a $0 card.
func Embedded() Snapshot {
	models := make([]Model, 0, len(pricing.OpenCodeGoModels))
	for _, row := range pricing.OpenCodeGoModels {
		m := Model{ID: row.ID, Label: row.Label, Free: strings.HasSuffix(row.ID, "-free")}
		if w, ok := budget.Known(row.ID); ok {
			m.Context = w
		}
		if card, ok := pricing.LookupRate(row.ID); ok {
			m.Input, m.Hit, m.Output = card.InputMissPerMTok, card.InputHitPerMTok, card.OutputPerMTok
		}
		models = append(models, m)
	}
	return Snapshot{Models: models}
}

// Install replaces the current snapshot and applies it. A snapshot with
// no models is ignored (an empty list would blank the menu).
func (c *Catalogue) Install(s Snapshot) {
	if len(s.Models) == 0 {
		return
	}
	c.mu.Lock()
	c.snap = s
	c.mu.Unlock()
	s.Install()
}

// Rows returns the live picker rows.
func (c *Catalogue) Rows() []pricing.OpenCodeGoModel {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snap.Rows()
}

// Snapshot returns a copy of the current catalogue.
func (c *Catalogue) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := Snapshot{FetchedAt: c.snap.FetchedAt, Models: make([]Model, len(c.snap.Models))}
	copy(out.Models, c.snap.Models)
	return out
}

// Refresh fetches the catalogue, installs it and caches it. It returns
// the number of rows installed. Any error leaves the current catalogue
// untouched.
func (c *Catalogue) Refresh(ctx context.Context, client *http.Client) (int, error) {
	var snap Snapshot
	var err error
	if c.Bearer != nil {
		token, org, aerr := c.Bearer(ctx)
		if aerr != nil {
			return 0, aerr
		}
		snap, err = FetchBearer(ctx, c.url(), token, org, client)
	} else {
		snap, err = Fetch(ctx, c.url(), c.APIKey, client)
	}
	if err != nil {
		return 0, err
	}
	c.Install(snap)
	// Cache write is best-effort: a read-only HOME must not fail the
	// refresh that already updated memory.
	_ = SaveCached(snap)
	return len(snap.Models), nil
}

func (c *Catalogue) url() string {
	if c.URL != "" {
		return c.URL
	}
	return DefaultModelsURL
}

// --- network ---

// wireList is the OpenAI-style list envelope.
type wireList struct {
	Data []wireModel `json:"data"`
}

type wireModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// API is keyed by protocol ("chat-completions", "responses",
	// "messages", "generate-content"). Only chat-completions speaks the
	// compatible adapter's wire, so it is the filter.
	API  map[string]json.RawMessage `json:"api"`
	Cost struct {
		Text struct {
			Input struct {
				Price float64 `json:"price"`
				Cache struct {
					Read float64 `json:"read"`
				} `json:"cache"`
			} `json:"input"`
			Output struct {
				Price float64 `json:"price"`
			} `json:"output"`
		} `json:"text"`
		// Multipliers are time-of-day surcharges (DeepSeek peak in the
		// pool's case, 2× on weekdays). The price fields are the base;
		// the displayed card uses the peak figure so the estimate errs
		// high, matching the embedded cards.
		Multipliers []struct {
			Factor float64 `json:"factor"`
		} `json:"multipliers"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
	Modalities struct {
		Input []string `json:"input"`
	} `json:"modalities"`
}

// DefaultClient is the catalogue's HTTP client: a short timeout so a slow
// or hanging endpoint can't pin the background refresh goroutine. The
// caller's context is the outer bound; this is the inner one.
func DefaultClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

// Fetch performs one catalogue request with a static API key. An empty
// key is a hard error: the docs say an unkeyed list contains every public
// model, and offering models the workspace can't call is worse than an
// empty menu (the caller keeps the embedded baseline).
func Fetch(ctx context.Context, url, apiKey string, client *http.Client) (Snapshot, error) {
	return FetchBearer(ctx, url, apiKey, "", client)
}

// FetchBearer is Fetch with an explicit bearer + the workspace header the
// OpenCode gateway requires when the credential is an OAuth token
// ("Workspace selection is required" 403 otherwise). orgID may be empty
// for static API keys, which are workspace-bound already.
func FetchBearer(ctx context.Context, url, bearer, orgID string, client *http.Client) (Snapshot, error) {
	if bearer == "" {
		return Snapshot{}, fmt.Errorf("opencatalog: no credential — the unkeyed list is not workspace-scoped")
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Snapshot{}, fmt.Errorf("opencatalog: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if orgID != "" {
		req.Header.Set("x-org-id", orgID)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := client.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("opencatalog: http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Snapshot{}, fmt.Errorf("opencatalog: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Snapshot{}, fmt.Errorf("opencatalog: read body: %w", err)
	}
	return Parse(body, time.Now())
}

// userAgent mirrors the openai client's seekUserAgent. Duplicated rather
// than shared because pkg/ must not import internal/ and the UA is a
// one-liner; if the format changes, both copies change together.
func userAgent() string {
	v := "dev"
	if info, ok := debug.ReadBuildInfo(); ok {
		if mv := info.Main.Version; mv != "" && mv != "(devel)" && !strings.HasPrefix(mv, "v0.0.0-") {
			v = mv
		}
	}
	return "seek/" + v
}

// Parse normalises a models payload into a Snapshot. Rows that aren't
// chat models, don't speak chat-completions, or have no id are dropped.
// An empty result is an error — it usually means the response shape
// changed, and installing it would blank the menu.
func Parse(body []byte, now time.Time) (Snapshot, error) {
	var list wireList
	if err := json.Unmarshal(body, &list); err != nil {
		return Snapshot{}, fmt.Errorf("opencatalog: decode: %w", err)
	}
	snap := Snapshot{FetchedAt: now.UTC()}
	for _, w := range list.Data {
		if w.ID == "" || w.Type != "chat" {
			continue
		}
		if _, ok := w.API["chat-completions"]; !ok {
			continue
		}
		factor := 1.0
		for _, mu := range w.Cost.Multipliers {
			if mu.Factor > factor {
				factor = mu.Factor
			}
		}
		free := w.Cost.Text.Input.Price == 0 && w.Cost.Text.Output.Price == 0
		snap.Models = append(snap.Models, Model{
			ID:      w.ID,
			Label:   labelFor(w, free),
			Context: w.Limit.Context,
			Input:   w.Cost.Text.Input.Price * factor,
			Hit:     w.Cost.Text.Input.Cache.Read * factor,
			Output:  w.Cost.Text.Output.Price * factor,
			Free:    free,
		})
	}
	if len(snap.Models) == 0 {
		return Snapshot{}, fmt.Errorf("opencatalog: no chat-completions models in the response")
	}
	snap.Models = merge(pricing.OpenCodeGoModels, snap.Models)
	return snap, nil
}

// merge keeps the embedded ordering (so the lane default stays first and
// muscle memory survives a refresh) and appends ids the compile-time list
// has never heard of, in the order the endpoint returned them. Ids the
// endpoint no longer lists are dropped — a removed model must leave the
// menu with it.
func merge(embedded []pricing.OpenCodeGoModel, fetched []Model) []Model {
	byID := make(map[string]Model, len(fetched))
	for _, m := range fetched {
		byID[m.ID] = m
	}
	out := make([]Model, 0, len(fetched))
	seen := make(map[string]bool, len(fetched))
	for _, row := range embedded {
		if m, ok := byID[row.ID]; ok {
			out = append(out, m)
			seen[row.ID] = true
		}
	}
	for _, m := range fetched {
		if !seen[m.ID] {
			out = append(out, m)
			seen[m.ID] = true
		}
	}
	return out
}

// labelFor builds a picker label from the endpoint's own metadata:
// "free · 1M ctx · vision · DeepSeek V4.1 Flash 1017", truncated to the
// picker's width budget.
func labelFor(w wireModel, free bool) string {
	var parts []string
	if free {
		parts = append(parts, "free")
	}
	if c := contextLabel(w.Limit.Context); c != "" {
		parts = append(parts, c)
	}
	for _, mod := range w.Modalities.Input {
		if strings.HasPrefix(mod, "image/") || strings.HasPrefix(mod, "video/") {
			parts = append(parts, "vision")
			break
		}
	}
	if w.Name != "" {
		parts = append(parts, w.Name)
	}
	return truncateRunes(strings.Join(parts, " · "), maxLabelRunes)
}

// contextLabel renders a window the way the picker rows do: "1M ctx" /
// "256K ctx"; empty for an unknown window.
func contextLabel(n int) string {
	switch {
	case n >= 1_000_000:
		return "1M ctx"
	case n >= 1_000:
		return fmt.Sprintf("%dK ctx", n/1_000)
	}
	return ""
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

// --- disk cache ---

// CachePath is $SEEK_HOME/opencode-catalog.json — same flat layout as
// upgrade-check.json.
func CachePath() (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "opencode-catalog.json"), nil
}

// LoadCached returns the persisted snapshot. Errors (missing, unreadable,
// malformed) report ok=false — a corrupt cache must never block startup,
// only force a refresh.
func LoadCached() (Snapshot, bool) {
	p, err := CachePath()
	if err != nil {
		return Snapshot{}, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Snapshot{}, false
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil || len(snap.Models) == 0 {
		return Snapshot{}, false
	}
	return snap, true
}

// SaveCached persists the snapshot atomically (tmp + rename), so a crash
// mid-write leaves the previous cache intact.
func SaveCached(s Snapshot) error {
	p, err := CachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Enabled reports whether the dynamic catalogue is switched on.
// $SEEK_OPENCODE_CATALOG=off (also 0/false/no) disables the fetch and the
// cache; the embedded baseline is used instead.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SEEK_OPENCODE_CATALOG"))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}
