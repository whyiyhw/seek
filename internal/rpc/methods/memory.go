package methods

// The memory method set (W4.3): memory/list|get|projects over the
// M-layer store (internal/memory — ~/.seek/projects/<id>/memory.jsonl
// entries + manifest). All reads go through memory.ListProjects /
// Project (read-only, no side effects), so — unlike Sessions — this
// family holds no lazy root resolution: paths.Home() is consulted per
// call and reads never create directories. Tests drive it with a
// temporary SEEK_HOME (the card's fixture口径).

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/rpc"
)

// Memories is the memory/list|get|projects family. Stateless: every
// dispatch re-reads the projects root (SEEK_HOME env ladder), the same
// tolerance ListProjects already applies (corrupt/foreign subdirs are
// skipped, a missing root is an empty list, never an error).
type Memories struct{}

// NewMemories returns the family over the env-ladder projects root.
func NewMemories() *Memories { return &Memories{} }

// RegisterAll wires the three methods into r.
func (m *Memories) RegisterAll(r *rpc.Registry) {
	r.Register("memory/list", m.list)
	r.Register("memory/get", m.get)
	r.Register("memory/projects", m.projects)
}

// memory/list — one project's entries, name-sorted (byte-stable, the
// same discipline as Project.Index) with stale entries included and
// flagged: a browse view must show what GC is about to retire.
//
// Field mapping vs the inspect-rpc §3.1 sketch: entries are JSONL
// records, not files, so `kind`/`mtime` have no disk-side counterpart —
// name/tagline carry over, mtime maps to updated_at. Additive fields
// (pinned/stale) ride along; result-field additions are contract-OK.
//
// project_id 缺省: the sketch says "当前项目", a TUI-process concept
// the serve process doesn't have (§3.3 caveat — everything is from
// disk). The disk-side proxy is the project with the newest manifest
// LastSeen; with no projects at all the default resolves to -32003
// (nothing to list), while an explicit project_id keeps §3.5 codes.

type memoryListParams struct {
	ProjectID string `json:"project_id,omitempty"`
}

type memoryListItem struct {
	Name      string    `json:"name"`
	Tagline   string    `json:"tagline"`
	UpdatedAt time.Time `json:"updated_at"`
	Pinned    bool      `json:"pinned,omitempty"`
	Stale     bool      `json:"stale,omitempty"`
}

type memoryListResult struct {
	Memories []memoryListItem `json:"memories"` // never null: walkable arrays
}

func (m *Memories) list(ctx context.Context, params json.RawMessage) (any, error) {
	var p memoryListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	proj, err := resolveProject(p.ProjectID)
	if err != nil {
		return nil, err
	}
	entries := proj.Entries()
	out := make([]memoryListItem, 0, len(entries))
	for _, e := range entries {
		out = append(out, memoryListItem{
			Name:      e.Name,
			Tagline:   e.Tagline,
			UpdatedAt: e.UpdatedAt,
			Pinned:    e.Pinned,
			Stale:     e.Stale,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return memoryListResult{Memories: out}, nil
}

// memory/get — one entry in full (content is the body the §3.1 sketch
// promised; links have no counterpart in the M-layer schema and are
// omitted rather than invented).

type memoryGetParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Name      string `json:"name"`
}

type memoryGetResult struct {
	Name            string    `json:"name"`
	Tagline         string    `json:"tagline"`
	Content         string    `json:"content"`
	Tags            []string  `json:"tags"` // never null
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	LastRecalledAt  time.Time `json:"last_recalled_at"`
	RecallCount     int       `json:"recall_count"`
	Pinned          bool      `json:"pinned,omitempty"`
	Stale           bool      `json:"stale,omitempty"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	AutoSourced     bool      `json:"auto_sourced,omitempty"`
}

func (m *Memories) get(ctx context.Context, params json.RawMessage) (any, error) {
	var p memoryGetParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpc.Errorf(rpc.CodeInvalidParams, "name is required")
	}
	proj, err := resolveProject(p.ProjectID)
	if err != nil {
		return nil, err
	}
	e, ok := proj.Get(p.Name)
	if !ok {
		return nil, &rpc.Error{
			Code:    rpc.CodeMemoryNotFound,
			Message: "memory " + p.Name + " not found",
			Data:    map[string]string{"project_id": proj.ID, "name": p.Name},
		}
	}
	tags := e.Tags
	if tags == nil {
		tags = []string{}
	}
	return memoryGetResult{
		Name:            e.Name,
		Tagline:         e.Tagline,
		Content:         e.Content,
		Tags:            tags,
		CreatedAt:       e.CreatedAt,
		UpdatedAt:       e.UpdatedAt,
		LastRecalledAt:  e.LastRecalledAt,
		RecallCount:     e.RecallCount,
		Pinned:          e.Pinned,
		Stale:           e.Stale,
		SourceSessionID: e.SourceSessionID,
		AutoSourced:     e.AutoSourced,
	}, nil
}

// memory/projects — the cross-project memory entry enumeration:
// every project ListProjects tolerates in, newest LastSeen first
// (the browse order; project/list keeps the id-sorted audit order).

type memoryProjectsResult struct {
	Projects []memoryProjectItem `json:"projects"` // never null
}

type memoryProjectItem struct {
	ProjectID    string    `json:"project_id"`
	Root         string    `json:"root"`
	LastActiveAt time.Time `json:"last_active_at"`
	Memories     int       `json:"memories"`
}

func (m *Memories) projects(ctx context.Context, params json.RawMessage) (any, error) {
	if err := decodeParams(params, &memoryListParams{}); err != nil {
		return nil, err // params-lenient: only shape errors land here
	}
	projs, err := memory.ListProjects()
	if err != nil {
		return nil, err
	}
	out := make([]memoryProjectItem, 0, len(projs))
	for _, p := range projs {
		out = append(out, memoryProjectItem{
			ProjectID:    p.ID,
			Root:         p.AbsPath,
			LastActiveAt: p.Manifest.LastSeen,
			Memories:     len(p.Entries()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActiveAt.After(out[j].LastActiveAt) })
	return memoryProjectsResult{Projects: out}, nil
}

// ---------- shared project lookup (memory + project families) ----------

// checkProjectID applies the projects-dir key rule at the RPC boundary:
// ids are 16 lowercase hex chars (paths.ProjectID's shape), so anything
// else is invalid params (-32602) — a traversal attempt never reaches
// the filesystem.
func checkProjectID(id string) error {
	if id == "" {
		return nil // absent is each method's default semantics
	}
	if len(id) != 16 {
		return rpc.Errorf(rpc.CodeInvalidParams, "project_id %q: want 16 hex chars", id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return rpc.Errorf(rpc.CodeInvalidParams, "project_id %q: want 16 hex chars", id)
		}
	}
	return nil
}

// resolveProject finds the read-only project for id, or — when id is
// empty — the disk-side "current project" proxy (newest manifest
// LastSeen). Unknown or unresolvable answers -32003 with the id in
// structured data.
func resolveProject(id string) (*memory.Project, error) {
	if err := checkProjectID(id); err != nil {
		return nil, err
	}
	projs, err := memory.ListProjects()
	if err != nil {
		return nil, err
	}
	if id == "" {
		var best *memory.Project
		for _, p := range projs {
			if best == nil || p.Manifest.LastSeen.After(best.Manifest.LastSeen) {
				best = p
			}
		}
		if best == nil {
			return nil, &rpc.Error{
				Code:    rpc.CodeProjectNotFound,
				Message: "no projects on disk",
				Data:    map[string]string{"project_id": ""},
			}
		}
		return best, nil
	}
	for _, p := range projs {
		if p.ID == id {
			return p, nil
		}
	}
	return nil, &rpc.Error{
		Code:    rpc.CodeProjectNotFound,
		Message: "project " + id + " not found",
		Data:    map[string]string{"project_id": id},
	}
}
