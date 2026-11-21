package methods

// The hooks method set (W4.6): hooks/list + hooks/audit/tail, the
// inspect-rpc §3.1 sketch's last M12.0 rows absorbed by feature-web-
// frontend §3.3. Everything reads what the disk already holds — the
// merged hooks.toml view and the append-only audit JSONL — which is
// why, unlike the CLI's `seek hooks list`, this family runs NO trust
// gate and NO bash -n static check: the Gate belongs to the seek
// subprocess that would execute the hooks, and a read-only plane must
// not prompt or spawn shells. A syntax-skipped hook therefore lists
// as configured here and shows up in the audit tail as a static-skip
// entry with its reason — the two views compose instead of duplicating
// the check.
//
// hooks/audit/tail is one-shot only: follow=true answers -32602
// pointing at the delivered follow surface — the web /events SSE
// `hooks` topic (W5 c_xz2bey; inspect-rpc §6 #6/#7 thereby closed).
// Rejecting beats silently ignoring — a follow client would sit
// waiting for events a snapshot reply never brings. POST /rpc stays
// one-shot by design; the follow lives on the GET /events?topic=hooks
// stream, whose frames carry the audit line verbatim (same rows this
// method returns).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
)

// Hooks is the hooks/list + hooks/audit/tail family. Only the audit
// project filter needs the sessions store (session_id → header CWD →
// project bucket); it resolves lazily on first use with the same
// stickiness rationale as Sessions — wiring stays side-effect-free,
// and a plane that never filters by project never creates a sessions
// dir.
type Hooks struct {
	resolve func() (*session.Store, error)

	once  sync.Once
	store *session.Store
	err   error
}

// NewHooks binds the family to an already-resolved store (tests,
// embedders with their own root).
func NewHooks(store *session.Store) *Hooks {
	return &Hooks{resolve: func() (*session.Store, error) { return store, nil }}
}

// NewHooksFromEnv defers store resolution to the first project-filtered
// audit call via session.NewStore — the same ladder and stickiness as
// NewSessionsFromEnv.
func NewHooksFromEnv() *Hooks {
	return &Hooks{resolve: session.NewStore}
}

func (h *Hooks) open() (*session.Store, error) {
	h.once.Do(func() { h.store, h.err = h.resolve() })
	return h.store, h.err
}

// RegisterAll wires the two methods into r.
func (h *Hooks) RegisterAll(r *rpc.Registry) {
	r.Register("hooks/list", h.list)
	r.Register("hooks/audit/tail", h.auditTail)
}

// hooks/list — the merged configured view (project file first, user
// file second, dispatch order preserved; event-group order across
// groups, same as `seek hooks list`). Field names follow the §3.1
// sketch (name/event/command/source); tool and timeout_ms ride along
// additively — result-field additions are contract-OK.
//
// project_id 缺省: the newest-manifest proxy like memory/list — but a
// machine with no projects on disk still gets the USER plane listed
// rather than -32003. Hooks exist at two levels and the user file
// stands on its own; an explicit unknown project_id keeps §3.5 codes.

type hooksListParams struct {
	ProjectID string `json:"project_id,omitempty"`
}

type hooksListItem struct {
	Name      string `json:"name"`
	Event     string `json:"event"`
	Command   string `json:"command"`
	Source    string `json:"source"` // "project" | "user" (hooksconfig.Source wire form)
	Tool      string `json:"tool"`   // "*" or the matched tool name
	TimeoutMs int    `json:"timeout_ms"`
}

type hooksListResult struct {
	Hooks []hooksListItem `json:"hooks"` // never null: walkable arrays
}

func (h *Hooks) list(ctx context.Context, params json.RawMessage) (any, error) {
	var p hooksListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	// The project side of the merge: explicit id must resolve (-32003
	// otherwise); the default resolves to the newest project but may
	// legitimately find none — then only the user file contributes.
	projPath := ""
	if p.ProjectID != "" {
		proj, err := resolveProject(p.ProjectID)
		if err != nil {
			return nil, err
		}
		projPath = paths.ProjectHooksToml(proj.AbsPath)
	} else if proj, err := resolveProject(""); err == nil {
		projPath = paths.ProjectHooksToml(proj.AbsPath)
	} else {
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeProjectNotFound {
			return nil, err // a real read failure, not "no projects yet"
		}
	}
	projCfg := hooksconfig.Config{}
	if projPath != "" {
		var err error
		if projCfg, err = hooksconfig.Load(projPath, hooksconfig.SourceProject); err != nil {
			return nil, fmt.Errorf("project hooks.toml: %w", err)
		}
	}
	userPath, err := paths.UserHooksToml()
	if err != nil {
		return nil, err
	}
	userCfg, err := hooksconfig.Load(userPath, hooksconfig.SourceUser)
	if err != nil {
		return nil, fmt.Errorf("user hooks.toml: %w", err)
	}
	rows := hooksconfig.Summarize(hooksconfig.Merge(projCfg, userCfg))
	out := make([]hooksListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, hooksListItem{
			Name:      r.Name,
			Event:     r.Event,
			Command:   r.Command,
			Source:    string(r.Source),
			Tool:      r.Tool,
			TimeoutMs: r.TimeoutMs,
		})
	}
	return hooksListResult{Hooks: out}, nil
}

// hooks/audit/tail — the read-only slice of the global audit JSONL
// (~/.seek/hooks-audit.jsonl, one AuditEntry per line, wire format
// verbatim per v3 §3.6). Params beyond the sketch's from_ts /
// project_id / follow: limit is an addition (optional params are
// contract-OK) because a one-shot browse must not return an unbounded
// file — the family's own messages-limit discipline. Entries come
// back oldest→newest of the returned (tail) window.
//
// from_ts is an absolute RFC3339 cutoff (entries at or after it
// stay); entries whose own ts fails to parse are KEPT — same
// fail-open rule as `seek hooks audit --since`, so a hand-edited line
// never hides its neighbours. project_id attributes each entry via
// its session's header CWD (the same bucketing as project/list, fork
// sessions included — a /branch run's hook executions did happen in
// that project); entries whose session is gone or unattributable do
// not match, they don't fail the call.

const (
	hooksAuditDefaultLimit = 100
	hooksAuditMaxLimit     = 1000
)

type hooksAuditParams struct {
	FromTS    string `json:"from_ts,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	Follow    *bool  `json:"follow,omitempty"` // pointer: explicit false is fine, true is -32602
	Limit     *int   `json:"limit,omitempty"`  // pointer: explicit 0 is invalid, not "default"
}

type hooksAuditResult struct {
	Entries []hooksconfig.AuditEntry `json:"entries"` // never null
}

func (h *Hooks) auditTail(ctx context.Context, params json.RawMessage) (any, error) {
	var p hooksAuditParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Follow != nil && *p.Follow {
		return nil, rpc.Errorf(rpc.CodeInvalidParams,
			"follow=true is not implemented on this host: /rpc is one-shot — follow the SSE hooks topic instead (GET /events?topic=hooks; inspect-rpc §6 #6 closed by W5 c_xz2bey)")
	}
	var cutoff time.Time
	if p.FromTS != "" {
		t, err := time.Parse(time.RFC3339, p.FromTS)
		if err != nil {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "from_ts %q: want RFC3339", p.FromTS)
		}
		cutoff = t
	}
	limit := hooksAuditDefaultLimit
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > hooksAuditMaxLimit {
			return nil, rpc.Errorf(rpc.CodeInvalidParams, "limit %d: want 1..%d", *p.Limit, hooksAuditMaxLimit)
		}
		limit = *p.Limit
	}
	if err := checkProjectID(p.ProjectID); err != nil {
		return nil, err
	}
	path, err := paths.HooksAuditLog()
	if err != nil {
		return nil, err
	}
	entries, err := hooksconfig.ReadAuditLog(path) // ENOENT → nil, nil: no audit data yet
	if err != nil {
		return nil, err
	}
	out := make([]hooksconfig.AuditEntry, 0, len(entries))
	for _, e := range entries {
		if !cutoff.IsZero() {
			if t, err := time.Parse(time.RFC3339, e.TS); err == nil && t.Before(cutoff) {
				continue
			}
		}
		out = append(out, e)
	}
	if p.ProjectID != "" {
		if out, err = h.filterByProject(out, p.ProjectID); err != nil {
			return nil, err
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return hooksAuditResult{Entries: out}, nil
}

// filterByProject keeps the entries attributable to projectID,
// caching the per-session verdict so a run of entries from one
// session costs one header read (line 1 only), not one per entry.
func (h *Hooks) filterByProject(entries []hooksconfig.AuditEntry, projectID string) ([]hooksconfig.AuditEntry, error) {
	store, err := h.open()
	if err != nil {
		return nil, err
	}
	verdict := make(map[string]bool, 8)
	keep := make([]hooksconfig.AuditEntry, 0, len(entries))
	for _, e := range entries {
		if _, seen := verdict[e.SessionID]; !seen {
			verdict[e.SessionID] = sessionInProject(store, e.SessionID, projectID)
		}
		if verdict[e.SessionID] {
			keep = append(keep, e)
		}
	}
	return keep, nil
}

// sessionInProject reports whether session sid ran inside projectID.
// Any failure — empty id, traversal-shaped id (the store's own path
// rule rejects it before touching the filesystem), missing or
// unreadable header — reads as unattributable, never as an error: a
// browse view must not die because one audit line names a session
// whose transcript is gone.
func sessionInProject(store *session.Store, sid, projectID string) bool {
	if sid == "" {
		return false
	}
	hdr, err := store.LoadHeader(sid)
	if err != nil {
		return false
	}
	return paths.ProjectID(hdr.CWD) == projectID
}
