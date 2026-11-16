package controlplane

// GET /subagents — the task panel's data source (W3.1 list + W3.3
// detail, feature-web-frontend §3.2): fold the requesting conv's
// bound project's subagents.jsonl into the state projection.
//
//	?parent=<sid>   list mode: rows filtered by parent session id
//	                (empty = all). Response is the bare folded array,
//	                shape-aligned with inspect-rpc §3.1 M12.1
//	                subagent/list so P3's RPC method consumes the same
//	                reader without a second fold.
//	?sub=<sub-sid>  detail mode: ONE row plus the sub transcript's
//	                last assistant entry (subagent.LastAssistant —
//	                the summary 单源 discipline, orchestration D5:
//	                the index carries no summary, the child's own
//	                transcript is the single source). 404 when the
//	                sub_sid is not in the fold. `sub` wins over a
//	                simultaneous `parent` (sub_sid is unique in the
//	                index).
//
// Strictly read-only — the panel never writes the index; live list
// updates arrive via the SSE topic (W3.2), while the detail is
// pull-per-click (a terminal push can refresh it, see the page).

import (
	"errors"
	"net/http"

	"github.com/whyiyhw/seek/internal/subagent"
)

// subagentDetail is the ?sub= response: the folded row plus the
// transcript tail. LastAssistant "" = nothing on disk yet (child
// running, or a terminal run whose Runner wrote no transcript).
type subagentDetail struct {
	Sub           subagent.Subagent `json:"sub"`
	LastAssistant string            `json:"last_assistant"`
}

func (f *WebFrontend) handleSubagents(w http.ResponseWriter, r *http.Request) {
	if !f.auth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	conv, err := f.convFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// The project comes from the conv's binding (same lookup as
	// /state): the panel watches the project this conversation is
	// driving. An unbound conv has no project to read — unlike /state
	// there is no meaningful "unbound" projection, so it answers 400
	// with the /bind hint rather than an empty 200 that would mask
	// the distinction.
	b, err := f.store.Get(conv)
	if err != nil {
		if errors.Is(err, ErrBindingNotFound) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "conversation not bound to a project (send /bind <path> first)"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if sub := r.URL.Query().Get("sub"); sub != "" {
		f.handleSubagentDetail(w, r, b.ProjectPath, sub)
		return
	}
	subs, err := subagent.ListProject(b.ProjectPath, subagent.ListFilter{
		ParentSid: r.URL.Query().Get("parent"),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, subs)
}

// handleSubagentDetail serves the ?sub= mode: fold the whole project
// (the fold is the only authority on parent_sid — the detail needs it
// to address the transcript), find the row, read its transcript tail.
// A missing row is 404, not 400: the id is well-formed, the resource
// is absent — same shape session/messages gives an unknown sid.
func (f *WebFrontend) handleSubagentDetail(w http.ResponseWriter, r *http.Request, project, subSid string) {
	subs, err := subagent.ListProject(project, subagent.ListFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, s := range subs {
		if s.SubSid != subSid {
			continue
		}
		last, err := subagent.LastAssistant(project, s.ParentSid, s.SubSid)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, subagentDetail{Sub: s, LastAssistant: last})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "subagent " + subSid + " not found"})
}
