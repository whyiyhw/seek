package methods

// stats/trend tests (card c_6xktw4 acceptance ①): the cross-session
// aggregation behind the web stats pane's line chart — one point per
// recent session folded from the header only, oldest first, with the
// two boundaries the UI has to survive called out explicitly: an empty
// store is an empty SUCCESS (not -32001 — "no history yet" is a normal
// trend state) and a one-point result is a well-formed single point.
// All sandbox: a temp SEEK_HOME sessions dir, nothing touches ~/.seek.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// trendEnv builds a store with n sessions, saved oldest→newest so the
// recency order is deterministic, each carrying a distinct usage.
func trendEnv(t *testing.T, n int) (*rpc.Registry, []string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")

	store, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	var sids []string
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("2026111%d-090000-%016d", i%10, i)
		sess := &session.Session{
			SchemaVersion: 3,
			ID:            sid,
			Model:         "deepseek-chat",
			CWD:           "/tmp/proj",
			Usage: deepseek.Usage{
				PromptTokens:          1000 * (i + 1),
				CompletionTokens:      100 * (i + 1),
				TotalTokens:           1100 * (i + 1),
				PromptCacheHitTokens:  100 * i,
				PromptCacheMissTokens: 1000,
			},
			Messages: []deepseek.Message{{Role: deepseek.RoleUser, Content: "hi"}},
		}
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
		sids = append(sids, sid)
		time.Sleep(time.Millisecond) // distinct UpdatedAt: Save stamps it
	}
	reg := rpc.NewRegistry()
	NewStats(store).RegisterAll(reg)
	return reg, sids
}

// TestStatsTrendAggregates pins the fold: one point per session, OLDEST
// first (a chart drawn left→right must read as time), every usage field
// surfaced, and cost priced server-side at the current tier.
func TestStatsTrendAggregates(t *testing.T) {
	reg, sids := trendEnv(t, 3)

	res, err := reg.Dispatch(context.Background(), "stats/trend", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := res.(statsTrendResult)
	if !ok {
		t.Fatalf("result = %T, want statsTrendResult", res)
	}
	if len(got.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(got.Points))
	}
	for i, p := range got.Points {
		if p.SID != sids[i] {
			t.Errorf("point %d sid = %q, want %q (oldest first)", i, p.SID, sids[i])
		}
		want := deepseek.Usage{
			PromptTokens: 1000 * (i + 1), CompletionTokens: 100 * (i + 1),
			TotalTokens:          1100 * (i + 1),
			PromptCacheHitTokens: 100 * i, PromptCacheMissTokens: 1000,
		}
		if p.PromptTokens != want.PromptTokens || p.CompTokens != want.CompletionTokens ||
			p.CacheHit != want.PromptCacheHitTokens || p.CacheMiss != want.PromptCacheMissTokens {
			t.Errorf("point %d usage drifted: %+v", i, p)
		}
		if p.HitRatio != want.HitRatio() {
			t.Errorf("point %d hit_ratio = %v, want %v", i, p.HitRatio, want.HitRatio())
		}
		if wantCost := pricing.Cost("deepseek-chat", pricing.CurrentTier(time.Now()), want); p.CostUSD != wantCost {
			t.Errorf("point %d cost_usd = %v, want %v", i, p.CostUSD, wantCost)
		}
		if p.At.IsZero() {
			t.Errorf("point %d has no timestamp", i)
		}
	}
	// Oldest first also means at monotonic x.
	for i := 1; i < len(got.Points); i++ {
		if got.Points[i].At.Before(got.Points[i-1].At) {
			t.Errorf("points not chronological at %d: %v < %v", i, got.Points[i].At, got.Points[i-1].At)
		}
	}
	// Wire shape: every field the chart reads survives marshalling, and
	// points is an array (never null — the page walks it).
	b, _ := json.Marshal(got)
	for _, field := range []string{"sid", "at", "model", "prompt_tokens", "completion_tokens",
		"cache_hit_tokens", "cache_miss_tokens", "hit_ratio", "cost_usd"} {
		if !strings.Contains(string(b), `"`+field+`"`) {
			t.Errorf("field %q missing from wire: %s", field, b)
		}
	}
	if !strings.HasPrefix(string(b), `{"points":[`) {
		t.Errorf("points is not a JSON array: %s", b)
	}
}

// TestStatsTrendWindow: limit keeps the NEWEST N sessions, still
// oldest-first inside the window (the chart's x order must not flip
// when the window shrinks).
func TestStatsTrendWindow(t *testing.T) {
	reg, sids := trendEnv(t, 5)

	res, err := reg.Dispatch(context.Background(), "stats/trend", json.RawMessage(`{"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(statsTrendResult)
	if len(got.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(got.Points))
	}
	if got.Points[0].SID != sids[3] || got.Points[1].SID != sids[4] {
		t.Errorf("window = %s %s, want the two newest in chronological order (%s %s)",
			got.Points[0].SID, got.Points[1].SID, sids[3], sids[4])
	}
	// Over-large limit is the whole store, not an error.
	res, err = reg.Dispatch(context.Background(), "stats/trend", json.RawMessage(`{"limit":100}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.(statsTrendResult).Points); n != 5 {
		t.Errorf("limit 100 over 5 sessions = %d points, want 5", n)
	}
}

// TestStatsTrendBoundaries: the two shapes the UI special-cases — an
// empty store (empty success, the hint path) and a single session (one
// point; a polyline needs two).
func TestStatsTrendBoundaries(t *testing.T) {
	// Empty store: NOT -32001. stats/cache answers "no current session"
	// there, but a trend with no history is an empty line, not an error.
	reg, _ := trendEnv(t, 0)
	res, err := reg.Dispatch(context.Background(), "stats/trend", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("empty store must succeed, got %v", err)
	}
	got := res.(statsTrendResult)
	if got.Points == nil || len(got.Points) != 0 {
		t.Errorf("empty store = %+v, want an empty non-nil slice", got.Points)
	}
	if b, _ := json.Marshal(got); string(b) != `{"points":[]}` {
		t.Errorf("empty store wire = %s, want {\"points\":[]}", b)
	}

	// One session: exactly one point, no error, no padding.
	reg1, sids := trendEnv(t, 1)
	res, err = reg1.Dispatch(context.Background(), "stats/trend", nil) // absent params = default window
	if err != nil {
		t.Fatal(err)
	}
	one := res.(statsTrendResult)
	if len(one.Points) != 1 || one.Points[0].SID != sids[0] {
		t.Fatalf("single session = %+v, want one point for %s", one.Points, sids[0])
	}
}

// TestStatsTrendErrorCodes: a bad limit is -32602 (the family's params
// rule); malformed params likewise; a removed store dir is a plain
// error the host maps to -32603.
func TestStatsTrendErrorCodes(t *testing.T) {
	reg, _ := trendEnv(t, 1)

	for _, tc := range []struct {
		name   string
		params string
	}{
		{"limit zero", `{"limit":0}`},
		{"limit negative", `{"limit":-1}`},
		{"limit over max", fmt.Sprintf(`{"limit":%d}`, trendMaxLimit+1)},
		{"scalar params", `"nope"`},
		{"array params", `[1]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Dispatch(context.Background(), "stats/trend", json.RawMessage(tc.params))
			var e *rpc.Error
			if !errors.As(err, &e) || e.Code != rpc.CodeInvalidParams {
				t.Fatalf("params %s = %v, want -32602", tc.params, err)
			}
		})
	}
	// The exact max is a legal boundary.
	if _, err := reg.Dispatch(context.Background(), "stats/trend",
		json.RawMessage(fmt.Sprintf(`{"limit":%d}`, trendMaxLimit))); err != nil {
		t.Errorf("limit %d must be accepted: %v", trendMaxLimit, err)
	}

	// A store that vanishes underneath: plain error (host → -32603).
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")
	st, err := session.NewStoreAt(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	reg2 := rpc.NewRegistry()
	NewStats(st).RegisterAll(reg2)
	if err := os.RemoveAll(st.Dir()); err != nil {
		t.Fatal(err)
	}
	_, err = reg2.Dispatch(context.Background(), "stats/trend", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("deleted store must error")
	}
	var e *rpc.Error
	if errors.As(err, &e) {
		t.Errorf("deleted store = %v, want a plain error (→ -32603)", err)
	}
}

// TestSessionListCostColumn pins the sidebar's per-session cost column
// (c_6xktw4): session/list carries server-priced cost_usd, computed
// with the same pricing.Cost call stats/cache makes — the page formats
// it, never prices it (§2.5).
func TestSessionListCostColumn(t *testing.T) {
	reg, _ := newFixtureRegistry(t)
	res, err := reg.Dispatch(context.Background(), "session/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := res.(sessionListResult)
	tier := pricing.CurrentTier(time.Now())
	var seen bool
	for _, s := range got.Sessions {
		if s.SID != mainSID {
			if s.CostUSD != 0 {
				t.Errorf("session %s cost = %v, want 0 (no usage on the header)", s.SID, s.CostUSD)
			}
			continue
		}
		seen = true
		wantU := deepseek.Usage{PromptTokens: 100, CompletionTokens: 40, PromptCacheHitTokens: 60}
		if want := pricing.Cost(s.Model, tier, wantU); s.CostUSD != want {
			t.Errorf("main cost_usd = %v, want %v (pricing.Cost at current tier)", s.CostUSD, want)
		}
		if s.CostUSD <= 0 {
			t.Errorf("main cost_usd = %v, want > 0 for a session with usage", s.CostUSD)
		}
	}
	if !seen {
		t.Fatal("fixture main session missing from the list")
	}
	// Wire shape: the field rides every row (the page reads s.cost_usd).
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"cost_usd"`) {
		t.Errorf("cost_usd missing from session/list wire: %s", b)
	}
}
