package methods

// stats/cache tests (W4.3 acceptance): the disk-side cumulative Usage
// picture per session, the default-sid newest-session proxy, and the
// §3.5 error rows.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestStatsCache(t *testing.T) {
	env := newAssetEnv(t)

	// Explicit sid: all Usage fields + the derived hit_ratio.
	res, err := env.reg.Dispatch(context.Background(), "stats/cache",
		json.RawMessage(`{"sid":"20261115-090000-aaaaaaaaaaaaaaaa"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(statsCacheResult)
	wantUsage := deepseek.Usage{PromptTokens: 1000, CompletionTokens: 200, PromptCacheHitTokens: 600, PromptCacheMissTokens: 400}
	if got.SID != "20261115-090000-aaaaaaaaaaaaaaaa" || got.Model != "deepseek-chat" || got.Usage != wantUsage {
		t.Errorf("result drifted: %+v", got)
	}
	if got.HitRatio != 0.6 { // 600 / (600+400)
		t.Errorf("hit_ratio = %v, want 0.6", got.HitRatio)
	}
	// The wire shape keeps every Usage field (the "Cumulative() 全字段"
	// contract) — a dropped field is a breaking result-field deletion.
	b, _ := json.Marshal(got)
	for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens"} {
		if !strings.Contains(string(b), field) {
			t.Errorf("usage field %q missing from wire: %s", field, b)
		}
	}
	// cost_usd (W4.5 additive): priced server-side by the "client never
	// prices" rule — expected recomputed with the same call the method
	// makes, so the assertion holds across tier boundaries.
	wantCost := pricing.Cost(got.Model, pricing.CurrentTier(time.Now()), wantUsage)
	if got.CostUSD != wantCost {
		t.Errorf("cost_usd = %v, want %v (pricing.Cost at current tier)", got.CostUSD, wantCost)
	}
	if !strings.Contains(string(b), "cost_usd") {
		t.Errorf("cost_usd missing from wire: %s", b)
	}

	// Default sid = newest session (the orphan, saved last).
	res, err = env.reg.Dispatch(context.Background(), "stats/cache", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(statsCacheResult); got.SID != "20261115-100000-cccccccccccccccc" {
		t.Errorf("default sid = %q, want newest session", got.SID)
	}

	// No params member at all is the default too.
	res, err = env.reg.Dispatch(context.Background(), "stats/cache", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(statsCacheResult); got.SID != "20261115-100000-cccccccccccccccc" {
		t.Errorf("absent-params default sid = %q", got.SID)
	}
}

// The §3.5 rows: bad sids are -32602; an unknown-but-well-formed sid
// is -32001 naming it; an empty store has no current session and
// answers -32001 with the empty sid slot.
func TestStatsCacheErrorCodes(t *testing.T) {
	env := newAssetEnv(t)

	cases := []struct {
		name   string
		params string
		want   int
	}{
		{"traversal sid", `{"sid":"../../etc/passwd"}`, rpc.CodeInvalidParams},
		{"dot-carrying sid", `{"sid":"x.jsonl"}`, rpc.CodeInvalidParams},
		{"scalar params", `"sid"`, rpc.CodeInvalidParams},
		{"unknown sid", fmt.Sprintf(`{"sid":%q}`, unknownSID), rpc.CodeSessionNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.reg.Dispatch(context.Background(), "stats/cache", json.RawMessage(tc.params))
			if err == nil {
				t.Fatal("want error, got success")
			}
			var e *rpc.Error
			if !errors.As(err, &e) {
				t.Fatalf("error = %T (%v), want *rpc.Error", err, err)
			}
			if e.Code != tc.want {
				t.Errorf("code = %d, want %d (%v)", e.Code, tc.want, err)
			}
		})
	}

	// The unknown-sid error names the sid in structured data.
	_, err := env.reg.Dispatch(context.Background(), "stats/cache", json.RawMessage(fmt.Sprintf(`{"sid":%q}`, unknownSID)))
	var e *rpc.Error
	if !errors.As(err, &e) {
		t.Fatal(err)
	}
	if data, ok := e.Data.(map[string]string); !ok || data["sid"] != unknownSID {
		t.Errorf("unknown sid must name it in data: %+v", e)
	}
}

// Empty store, default sid: no current session exists — -32001 with
// the empty sid slot distinguishing it from an unknown-but-named id.
func TestStatsCacheEmptyStoreDefault(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_SESSIONS_DIR", "")

	fam := NewStatsFromEnv() // fresh lazy resolve against the empty home
	reg := rpc.NewRegistry()
	fam.RegisterAll(reg)
	_, err := reg.Dispatch(context.Background(), "stats/cache", json.RawMessage(`{}`))
	var e *rpc.Error
	if !errors.As(err, &e) || e.Code != rpc.CodeSessionNotFound {
		t.Fatalf("empty store default = %v, want -32001", err)
	}
	if data, ok := e.Data.(map[string]string); !ok || data["sid"] != "" {
		t.Errorf("data = %+v, want sid slot empty", e.Data)
	}
}

// The store-resolving W4.3 families (Projects, Stats) share Sessions'
// lazy env contract: construction touches nothing, the first dispatch
// resolves, and a broken root fails STICKY as a plain error the host
// maps to -32603 (never silently resolving elsewhere). The blocker
// file makes NewStore's MkdirAll fail with ENOTDIR — deterministic on
// every OS, same trick as the session family's test.
func TestEnvResolvedFamiliesStickyBrokenRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("file, not dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	projects := NewProjectsFromEnv()
	stats := NewStatsFromEnv()
	// Construction must not have created the sessions dir.
	if _, err := os.Stat(filepath.Join(home, "sessions")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("wiring created the sessions dir: %v", err)
	}

	// project/get resolves the project (SEEK_HOME walk, healthy) before
	// it touches the sessions store — so the sticky-store probe needs a
	// resolvable project on disk, not just the params.
	writeProjectFixture(t, home, "0123456789abcdef", "/gone", fixtureTime)

	t.Setenv("SEEK_SESSIONS_DIR", filepath.Join(blocker, "sub"))
	reg := rpc.NewRegistry()
	projects.RegisterAll(reg)
	stats.RegisterAll(reg)
	for i := 0; i < 2; i++ { // sticky: same failure on every call
		for _, method := range []string{"project/list", "project/get", "stats/cache"} {
			params := json.RawMessage(`{}`)
			if method == "project/get" {
				params = json.RawMessage(`{"project_id":"0123456789abcdef"}`)
			}
			_, err := reg.Dispatch(context.Background(), method, params)
			if err == nil {
				t.Fatalf("%s call %d: broken root must error", method, i)
			}
			var e *rpc.Error
			if errors.As(err, &e) {
				t.Errorf("%s call %d: unexpected *rpc.Error %v, want a plain error the host maps to -32603", method, i, err)
			}
		}
	}
}

// A store that resolves fine but whose directory vanishes before a
// List (deleted underneath the serve process): a plain error the host
// maps to -32603, on both the project/list and stats/cache paths —
// not an app code, not a panic.
func TestStoreListFailureIsPlainError(t *testing.T) {
	env := newAssetEnv(t)
	if err := os.RemoveAll(env.store.Dir()); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"project/list", "stats/cache"} {
		_, err := env.reg.Dispatch(context.Background(), method, json.RawMessage(`{}`))
		if err == nil {
			t.Fatalf("%s over deleted store: want error", method)
		}
		var e *rpc.Error
		if errors.As(err, &e) {
			t.Errorf("%s: unexpected *rpc.Error %v, want plain error → -32603", method, err)
		}
	}
}
