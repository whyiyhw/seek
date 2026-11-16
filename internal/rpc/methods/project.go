package methods

// The project method set (W4.3): project/list|get — the known-project
// enumeration over ~/.seek/projects/ (via memory.ListProjects, the
// tolerant read-only walk) enriched with per-project session counts
// bucketed from the flat session store the same way ListProject
// buckets (paths.ProjectID(header.CWD), /branch forks skipped so
// overlapping transcripts don't count twice).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/projectmd"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/session"
)

// Projects is the project/list|get family over one sessions store.
// The projects root itself resolves per call through the env ladder
// (same as Memories); only the sessions store needs the lazy resolve
// pattern — session.NewStore creates its directory, and wiring must
// stay side-effect-free (the Sessions rationale, verbatim).
type Projects struct {
	resolve func() (*session.Store, error)

	once  sync.Once
	store *session.Store
	err   error
}

// NewProjects binds the family to an already-resolved store (tests,
// embedders with their own root).
func NewProjects(store *session.Store) *Projects {
	return &Projects{resolve: func() (*session.Store, error) { return store, nil }}
}

// NewProjectsFromEnv defers store resolution to the first dispatch via
// session.NewStore (the SEEK_SESSIONS_DIR / SEEK_HOME / ~/.seek ladder).
// A resolution failure is sticky and surfaces as -32603 on every call.
func NewProjectsFromEnv() *Projects {
	return &Projects{resolve: session.NewStore}
}

func (p *Projects) open() (*session.Store, error) {
	p.once.Do(func() { p.store, p.err = p.resolve() })
	return p.store, p.err
}

// RegisterAll wires the two methods into r.
func (p *Projects) RegisterAll(r *rpc.Registry) {
	r.Register("project/list", p.list)
	r.Register("project/get", p.get)
}

// project/list — every known project, id-sorted (ListProjects' own
// deterministic order, asserted by test; memory/projects is the
// newest-first browse view, this is the audit-stable enumeration).
// Per-file session load errors are dropped, matching Store.List's
// tolerance.

type projectListItem struct {
	ProjectID    string    `json:"project_id"`
	Root         string    `json:"root"`
	LastActiveAt time.Time `json:"last_active_at"`
	Sessions     int       `json:"sessions"`
	Memories     int       `json:"memories"`
}

type projectListResult struct {
	Projects []projectListItem `json:"projects"` // never null
}

func (p *Projects) list(ctx context.Context, params json.RawMessage) (any, error) {
	if err := decodeParams(params, &struct{}{}); err != nil {
		return nil, err // params-lenient: only shape errors land here
	}
	projs, err := memory.ListProjects()
	if err != nil {
		return nil, err
	}
	counts, err := p.sessionCounts()
	if err != nil {
		return nil, err
	}
	out := make([]projectListItem, 0, len(projs))
	for _, proj := range projs {
		out = append(out, projectListItem{
			ProjectID:    proj.ID,
			Root:         proj.AbsPath,
			LastActiveAt: proj.Manifest.LastSeen,
			Sessions:     counts[proj.ID],
			Memories:     len(proj.Entries()),
		})
	}
	return projectListResult{Projects: out}, nil
}

// project/get — one project's metadata. agents_md_path / hooks_toml_
// path appear only when the file exists on disk (the sketch's `?`
// optionality): a path that isn't there would send the panel hunting
// for files the project never had.

type projectGetParams struct {
	ProjectID string `json:"project_id"`
}

type projectGetResult struct {
	ProjectID     string    `json:"project_id"`
	Root          string    `json:"root"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	Sessions      int       `json:"sessions"`
	Memories      int       `json:"memories"`
	AgentsMDPath  string    `json:"agents_md_path,omitempty"`
	HooksTOMLPath string    `json:"hooks_toml_path,omitempty"`
}

func (p *Projects) get(ctx context.Context, params json.RawMessage) (any, error) {
	var pr projectGetParams
	if err := decodeParams(params, &pr); err != nil {
		return nil, err
	}
	if pr.ProjectID == "" {
		return nil, rpc.Errorf(rpc.CodeInvalidParams, "project_id is required")
	}
	proj, err := resolveProject(pr.ProjectID)
	if err != nil {
		return nil, err
	}
	counts, err := p.sessionCounts()
	if err != nil {
		return nil, err
	}
	res := projectGetResult{
		ProjectID: proj.ID,
		Root:      proj.AbsPath,
		FirstSeen: proj.Manifest.FirstSeen,
		LastSeen:  proj.Manifest.LastSeen,
		Sessions:  counts[proj.ID],
		Memories:  len(proj.Entries()),
	}
	// Root is the manifest's last-seen location — a moved or deleted
	// project still gets/get/lists fine, the optional paths just don't
	// appear. Stat failures other than existence are ignored: an
	// unreadable root must not fail the metadata read.
	if proj.AbsPath != "" {
		if _, err := os.Stat(filepath.Join(proj.AbsPath, projectmd.Filename)); err == nil {
			res.AgentsMDPath = filepath.Join(proj.AbsPath, projectmd.Filename)
		}
		hooks := paths.ProjectHooksToml(proj.AbsPath)
		if _, err := os.Stat(hooks); err == nil {
			res.HooksTOMLPath = hooks
		}
	}
	return res, nil
}

// sessionCounts buckets the store's non-fork sessions by project id in
// one List() pass — the same bucketing rule as Store.ListProject
// (header CWD hashed with paths.ProjectID, ParentID forks skipped),
// amortised across all projects instead of one List per project.
func (p *Projects) sessionCounts() (map[string]int, error) {
	store, err := p.open()
	if err != nil {
		return nil, err
	}
	infos, _, err := store.List()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, info := range infos {
		if info.ParentID != "" {
			continue
		}
		counts[paths.ProjectID(info.CWD)]++
	}
	return counts, nil
}
