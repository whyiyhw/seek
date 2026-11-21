package methods

// The stats method set (W4.3 + v8 §4 pillar S): stats/cache and
// stats/friction. The sketch's in-process source (cache.Tracker.
// Cumulative()) doesn't exist in the serve process — §3.3's caveat: the
// asset plane reads from disk. The persisted counterpart is the session
// header's cumulative Usage (the same deepseek.Usage the tracker would
// sum in-process, saved per turn by the writing seek subprocess), so
// this family is a header-only read away the session store — cheap on
// any transcript length. stats/friction (c_2i6sm6) instead folds the
// message lines through internal/friction — a whole-file read, still
// strictly read-only.

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/friction"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Stats is the stats/* family over one sessions store, with the same
// lazy-resolution pattern as Sessions (session.NewStore creates its
// directory; wiring must stay side-effect-free, failures sticky
// as -32603).
type Stats struct {
	resolve func() (*session.Store, error)

	once  sync.Once
	store *session.Store
	err   error
}

// NewStats binds the family to an already-resolved store (tests,
// embedders with their own root).
func NewStats(store *session.Store) *Stats {
	return &Stats{resolve: func() (*session.Store, error) { return store, nil }}
}

// NewStatsFromEnv defers store resolution to the first dispatch via
// session.NewStore — the same ladder and stickiness as
// NewSessionsFromEnv / NewProjectsFromEnv.
func NewStatsFromEnv() *Stats {
	return &Stats{resolve: session.NewStore}
}

func (s *Stats) open() (*session.Store, error) {
	s.once.Do(func() { s.store, s.err = s.resolve() })
	return s.store, s.err
}

// RegisterAll wires the method into r.
func (s *Stats) RegisterAll(r *rpc.Registry) {
	r.Register("stats/cache", s.cache)
	r.Register("stats/friction", s.friction)
	r.Register("stats/trend", s.trend)
}

// statsSIDParams is the family's params convention: sid optional —
// absent means "the current session", resolved by resolveSID.
type statsSIDParams struct {
	SID string `json:"sid,omitempty"`
}

// resolveSID maps the sid slot onto a concrete session id: empty
// defaults to the newest session in the store (the disk-side proxy for
// "current"; List is newest-first), an empty store answers -32001 with
// the sid slot empty — the structured data distinguishes it from an
// unknown-but-well-formed id — and a named sid is path-checked and
// returned as-is (existence is the caller's read's job).
func (s *Stats) resolveSID(sid string) (string, error) {
	if sid != "" {
		if err := checkSID(sid); err != nil {
			return "", err
		}
		return sid, nil
	}
	store, err := s.open()
	if err != nil {
		return "", err
	}
	infos, _, err := store.List()
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", &rpc.Error{
			Code:    rpc.CodeSessionNotFound,
			Message: "no sessions in store",
			Data:    map[string]string{"sid": ""},
		}
	}
	return infos[0].ID, nil
}

// stats/cache — the cumulative prefix-cache picture for one session:
// every Usage field (what cache.Tracker.Cumulative() would have
// returned in-process) plus the derived hit_ratio. sid 缺省 = the
// sketch's "当前 session" mapped to its disk-side proxy: the most
// recently updated session in the store. An empty store has no
// current session and answers -32001 with the sid slot empty — the
// structured data distinguishes it from an unknown-but-well-formed id.

type statsCacheResult struct {
	SID      string         `json:"sid"`
	Model    string         `json:"model"`
	Usage    deepseek.Usage `json:"usage"`
	HitRatio float64        `json:"hit_ratio"`
	// CostUSD prices the cumulative Usage at the tier in effect at
	// query time — an estimate, since the turns accrued across mixed
	// tiers that the header doesn't record. Server-side by the web's
	// "client never prices" rule (webfrontend_stream.go §2.5): rate
	// cards live only in internal/pricing, the same cards as the CLI
	// footer. Additive W4.5 field; result-field additions are
	// contract-OK (the W4.3 memory.go precedent).
	CostUSD float64 `json:"cost_usd"`
}

func (s *Stats) cache(ctx context.Context, params json.RawMessage) (any, error) {
	var p statsSIDParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	sid, err := s.resolveSID(p.SID)
	if err != nil {
		return nil, err
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	hdr, err := store.LoadHeader(sid)
	if err != nil {
		return nil, mapReadError(sid, err)
	}
	return statsCacheResult{
		SID:      hdr.ID,
		Model:    hdr.Model,
		Usage:    hdr.Usage,
		HitRatio: hdr.Usage.HitRatio(),
		CostUSD:  pricing.Cost(hdr.Model, pricing.CurrentTier(time.Now()), hdr.Usage),
	}, nil
}

// stats/friction — the v8 §4 pillar-S fold: per-tool Calls / Errors /
// Denied / Repeats plus session-level Turns / ToolCalls, derived purely
// from the session's message lines (no state beyond the transcript; the
// judgment criteria are owned by internal/friction). Same sid contract
// as stats/cache, incl. the empty-default → newest proxy. Reads via
// Load — the raw message lines, not a repaired view: Store.Repair is a
// separate resume-time action, so orphan tool_calls tails are folded
// exactly as they sit on disk.

type statsFrictionResult struct {
	SID     string           `json:"sid"`
	Summary friction.Summary `json:"summary"`
}

func (s *Stats) friction(ctx context.Context, params json.RawMessage) (any, error) {
	var p statsSIDParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	sid, err := s.resolveSID(p.SID)
	if err != nil {
		return nil, err
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	sess, err := store.Load(sid)
	if err != nil {
		return nil, mapReadError(sid, err)
	}
	return statsFrictionResult{
		SID:     sess.ID,
		Summary: friction.Fold(sess.Messages),
	}, nil
}

// stats/trend — c_6xktw4: the cross-session trend the stats pane plots.
// One point per recent session, OLDEST first so a line drawn left→right
// reads as time. Folded from the same header read stats/cache uses
// (Store.List decodes line 1 only), so the cost is O(N) header decodes
// and never touches the message bodies.
//
// Boundaries the UI must survive:
//   - empty store → {"points":[]} — an EMPTY SUCCESS, not -32001: "no
//     history yet" is a normal state for a trend line, unlike a
//     per-session read where there is no session to answer for.
//   - one point  → a single point; a polyline needs two.
// Per-file load errors are dropped silently, the session/list rule: one
// corrupt header must not hide the whole trend.

const (
	trendDefaultLimit = 20
	trendMaxLimit     = 100
)

type statsTrendParams struct {
	Limit *int `json:"limit,omitempty"` // pointer: explicit 0 is invalid, not "default"
}

type trendPoint struct {
	SID          string    `json:"sid"`
	At           time.Time `json:"at"` // session UpdatedAt — the point's x
	Model        string    `json:"model"`
	PromptTokens int       `json:"prompt_tokens"`
	CompTokens   int       `json:"completion_tokens"`
	CacheHit     int       `json:"cache_hit_tokens"`
	CacheMiss    int       `json:"cache_miss_tokens"`
	HitRatio     float64   `json:"hit_ratio"`
	CostUSD      float64   `json:"cost_usd"`
}

type statsTrendResult struct {
	Points []trendPoint `json:"points"` // never null: a walkable array for JS
}

func (s *Stats) trend(ctx context.Context, params json.RawMessage) (any, error) {
	var p statsTrendParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	limit := trendDefaultLimit
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > trendMaxLimit {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "limit %d: want 1..%d", *p.Limit, trendMaxLimit)
		}
		limit = *p.Limit
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	infos, _, err := store.List() // newest first
	if err != nil {
		return nil, err
	}
	if len(infos) > limit {
		infos = infos[:limit]
	}
	tier := pricing.CurrentTier(time.Now())
	points := make([]trendPoint, 0, len(infos))
	for i := len(infos) - 1; i >= 0; i-- { // newest-first → oldest-first
		info := infos[i]
		u := info.Usage
		points = append(points, trendPoint{
			SID:          info.ID,
			At:           info.UpdatedAt,
			Model:        info.Model,
			PromptTokens: u.PromptTokens,
			CompTokens:   u.CompletionTokens,
			CacheHit:     u.PromptCacheHitTokens,
			CacheMiss:    u.PromptCacheMissTokens,
			HitRatio:     u.HitRatio(),
			CostUSD:      pricing.Cost(info.Model, tier, u),
		})
	}
	return statsTrendResult{Points: points}, nil
}
