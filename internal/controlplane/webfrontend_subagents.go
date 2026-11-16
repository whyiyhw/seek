package controlplane

// GET /subagents — the task panel's list source (W3.1,
// feature-web-frontend §3.2): fold the requesting conv's bound
// project's subagents.jsonl into the state projection, filtered by
// ?parent=<sid> (parent session id; empty = all). Strictly read-only
// — the panel never writes the index; live updates arrive later via
// the SSE topic (W3.2).
//
// Response is the bare folded array, shape-aligned with inspect-rpc
// §3.1 M12.1 subagent/list ({sub_sid, parent_sid, type, status,
// description, tokens, started_at, ...} — the Subagent JSON tags
// carry the projection's additive fields) so P3's RPC method consumes
// the same reader without a second fold.

import (
	"errors"
	"net/http"

	"github.com/whyiyhw/seek/internal/subagent"
)

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
	subs, err := subagent.ListProject(b.ProjectPath, subagent.ListFilter{
		ParentSid: r.URL.Query().Get("parent"),
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, subs)
}
