package methods

// The subagent method set (inspect-rpc §3.1 M12.1 list/get, closed out
// on the web /rpc plane per §6 #12 and feature-web-frontend §3.3):
// subagent/list + subagent/get over the same disk projection the web
// task panel already reads — subagent.ListProject folds
// ~/.seek/projects/<id>/subagents.jsonl with the one applyEvent core
// (whole-file fold and the SSE topic's incremental follow share it), so
// the method, GET /subagents and the topic push are ONE projection.
//
// subagent/tree (the sketch's third row) is deliberately absent: v5
// caps nesting at depth 1 (no subagent may spawn a subagent), so the
// tree is the flat list with parent_sid on every row. Shipping a
// schema for a shape the data cannot take would be a promise the fold
// cannot keep; the row's parent_sid is the tree edge today.
//
// Project resolution (the §3.3 caveat — the serve process has no TUI
// state): project_id is optional, and absent means the disk-side
// "current project" proxy every other family uses (newest manifest
// LastSeen). subagent/get with no project_id SCANS the projects
// newest-first for the sub_sid: a sub_sid is a globally unique id
// (timestamp + 64-bit random suffix), so the first folded match is the
// only match, and a detail click must not require the caller to know
// which project's index holds the row.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/rpc"
	"github.com/whyiyhw/seek/internal/subagent"
)

// Subagents is the subagent/list|get family. Stateless: every dispatch
// re-walks the projects root (SEEK_HOME ladder) and re-folds the
// matching index, so rows appended by a live seek child in another
// process are visible to the next call — the same freshness contract
// as GET /subagents and the topic follow.
type Subagents struct{}

// NewSubagents returns the family over the env-ladder projects root.
func NewSubagents() *Subagents { return &Subagents{} }

// RegisterAll wires the two methods into r.
func (s *Subagents) RegisterAll(r *rpc.Registry) {
	r.Register("subagent/list", s.list)
	r.Register("subagent/get", s.get)
}

// subagentListParams follows the §3.1 sketch (parent_sid?, status?) and
// adds project_id — an optional addition, contract-OK per §3.2.
type subagentListParams struct {
	ParentSid string `json:"parent_sid,omitempty"`
	Status    string `json:"status,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

// subagentListResult wraps the rows: /rpc families answer objects with
// a never-null array (the sessions/memories/projects precedent), while
// GET /subagents keeps its bare-array shape for the panel's existing
// consumers.
type subagentListResult struct {
	ProjectID string              `json:"project_id"`
	Subagents []subagent.Subagent `json:"subagents"` // never null
}

func (s *Subagents) list(ctx context.Context, params json.RawMessage) (any, error) {
	var p subagentListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	status, err := parseSubagentStatus(p.Status)
	if err != nil {
		return nil, err
	}
	proj, err := resolveProject(p.ProjectID)
	if err != nil {
		return nil, err
	}
	rows, err := subagent.ListProject(proj.AbsPath, subagent.ListFilter{
		ParentSid: p.ParentSid,
		Status:    status,
	})
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []subagent.Subagent{}
	}
	return subagentListResult{ProjectID: proj.ID, Subagents: rows}, nil
}

// parseSubagentStatus validates the status filter against the fold's
// own vocabulary plus "all" (no filter). Rejecting an unknown value
// beats silently returning everything: a panel typo must not read as
// "here are all your rows".
func parseSubagentStatus(s string) (subagent.Status, error) {
	switch s {
	case "", "all":
		return "", nil
	case string(subagent.StatusActive),
		string(subagent.StatusCompleted),
		string(subagent.StatusFailed),
		string(subagent.StatusKilled),
		string(subagent.StatusOrphaned),
		string(subagent.StatusPromoted):
		return subagent.Status(s), nil
	}
	return "", rpc.Errorf(rpc.CodeInvalidParams,
		"status %q: want active|completed|failed|killed|orphaned|promoted|all", s)
}

// subagentGetParams: sub_sid is required; project_id narrows the scan
// when the caller already knows the project (the panel usually does).
type subagentGetParams struct {
	SubSid    string `json:"sub_sid"`
	ProjectID string `json:"project_id,omitempty"`
}

// subagentGetResult is the §3.1 sketch's row PLUS last_assistant — the
// same information GET /subagents?sub= carries (feature-web-frontend
// §13.1 c_4doebx "详情展开与 /subagents?sub= 同信息量"), so a detail
// view has exactly one fetch on either transport. The summary 单源
// discipline holds: the index carries no summary, the child's own
// transcript is the single source, and an empty tail is an answer (the
// child is still running or wrote no transcript), never an error.
type subagentGetResult struct {
	Sub           subagent.Subagent `json:"sub"`
	LastAssistant string            `json:"last_assistant"`
	ProjectID     string            `json:"project_id"`
	ProjectRoot   string            `json:"project_root"`
}

func (s *Subagents) get(ctx context.Context, params json.RawMessage) (any, error) {
	var p subagentGetParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if err := checkSubSid(p.SubSid); err != nil {
		return nil, err
	}
	projects, err := subagentProjects(p.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, proj := range projects {
		rows, err := subagent.ListProject(proj.AbsPath, subagent.ListFilter{})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.SubSid != p.SubSid {
				continue
			}
			last, err := subagent.LastAssistant(proj.AbsPath, row.ParentSid, row.SubSid)
			if err != nil {
				return nil, err
			}
			return subagentGetResult{
				Sub:           row,
				LastAssistant: last,
				ProjectID:     proj.ID,
				ProjectRoot:   proj.AbsPath,
			}, nil
		}
	}
	return nil, &rpc.Error{
		Code:    rpc.CodeSubagentNotFound,
		Message: "subagent " + p.SubSid + " not found",
		Data:    map[string]string{"sub_sid": p.SubSid},
	}
}

// checkSubSid applies the id rule at the RPC boundary: the id is a
// directory name under the project's session tree, so path separators
// and dots are invalid params (-32602), never a filesystem probe.
func checkSubSid(subSid string) error {
	if subSid == "" {
		return rpc.Errorf(rpc.CodeInvalidParams, "sub_sid is required")
	}
	if strings.ContainsAny(subSid, "/\\.") {
		return rpc.Errorf(rpc.CodeInvalidParams, "invalid sub_sid %q", subSid)
	}
	return nil
}

// subagentProjects returns the candidate projects for a lookup,
// newest-manifest first (the order a detail click should try: the
// project the user was just working in is the likely holder). An
// explicit project_id resolves to that one project only (-32003 when
// unknown); an absent id means every project, and no projects at all
// is an empty slice — the caller's loop then answers
// CodeSubagentNotFound, which is the truthful answer ("this id is in
// no index"), unlike a -32003 that would blame a project nobody named.
func subagentProjects(projectID string) ([]*memory.Project, error) {
	if projectID != "" {
		proj, err := resolveProject(projectID)
		if err != nil {
			return nil, err
		}
		return []*memory.Project{proj}, nil
	}
	projs, err := memory.ListProjects()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(projs, func(i, j int) bool {
		return projs[i].Manifest.LastSeen.After(projs[j].Manifest.LastSeen)
	})
	return projs, nil
}
