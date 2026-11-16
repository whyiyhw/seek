package methods

// The stats method set (W4.3): stats/cache. The sketch's in-process
// source (cache.Tracker.Cumulative()) doesn't exist in the serve
// process — §3.3's caveat: the asset plane reads from disk. The
// persisted counterpart is the session header's cumulative Usage
// (the same deepseek.Usage the tracker would sum in-process, saved
// per turn by the writing seek subprocess), so this family is a
// header-only read away the session store — cheap on any transcript
// length.

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Stats is the stats/cache family over one sessions store, with the
// same lazy-resolution pattern as Sessions (session.NewStore creates
// its directory; wiring must stay side-effect-free, failures sticky
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
}

// stats/cache — the cumulative prefix-cache picture for one session:
// every Usage field (what cache.Tracker.Cumulative() would have
// returned in-process) plus the derived hit_ratio. sid 缺省 = the
// sketch's "当前 session" mapped to its disk-side proxy: the most
// recently updated session in the store. An empty store has no
// current session and answers -32001 with the sid slot empty — the
// structured data distinguishes it from an unknown-but-well-formed id.

type statsCacheParams struct {
	SID string `json:"sid,omitempty"`
}

type statsCacheResult struct {
	SID      string         `json:"sid"`
	Model    string         `json:"model"`
	Usage    deepseek.Usage `json:"usage"`
	HitRatio float64        `json:"hit_ratio"`
}

func (s *Stats) cache(ctx context.Context, params json.RawMessage) (any, error) {
	var p statsCacheParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	// checkSID's empty-id rule doesn't apply here: an absent sid is
	// this method's default-to-newest semantics, not a missing param.
	// The path-safety half (no / \ .) still guards every named sid.
	if p.SID != "" {
		if err := checkSID(p.SID); err != nil {
			return nil, err
		}
	}
	store, err := s.open()
	if err != nil {
		return nil, err
	}
	sid := p.SID
	if sid == "" {
		infos, _, err := store.List()
		if err != nil {
			return nil, err
		}
		if len(infos) == 0 {
			return nil, &rpc.Error{
				Code:    rpc.CodeSessionNotFound,
				Message: "no sessions in store",
				Data:    map[string]string{"sid": ""},
			}
		}
		sid = infos[0].ID // List is newest-first: the "current" proxy
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
	}, nil
}
