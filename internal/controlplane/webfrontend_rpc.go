package controlplane

// POST /rpc — the asset plane's JSON-RPC 2.0 endpoint (W4.1,
// feature-web-frontend §2.5/§3.3 absorbing inspect-rpc §3.2/§3.5).
// Single call per POST: batch bodies are -32600 (batching is not an
// asset-plane need). JSON-RPC-level failures — parse, envelope,
// unknown method, params, app errors — answer HTTP 200 with the error
// envelope, the JSON-RPC convention: the call failed, not the
// transport. Transport failures (auth, method, size) keep the web
// frontend's status-code + JSON-body convention shared with /msg.
//
// The registry is built once in NewWeb and only Dispatched by handlers
// — the §3.2 phase contract. W4.1 ships it empty (every method answers
// -32601); the session/memory/project method sets (W4.2/W4.3) register
// into it before Run.

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/whyiyhw/seek/internal/rpc"
)

// rpcBodyLimit caps one /rpc call body. Asset-plane params are ids and
// cursors — 1 MiB is generous 16× the /msg cap; the stdio transport's
// 4 MiB lines serve a different regime (whole prompts).
const rpcBodyLimit = 1 << 20

// handleRPC serves POST /rpc behind the same token gate as every
// stateful route (asset plane and chat share one origin/auth, §3.3).
func (f *WebFrontend) handleRPC(w http.ResponseWriter, r *http.Request) {
	if !f.auth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, rpcBodyLimit+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
		return
	}
	if len(body) > rpcBodyLimit {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body exceeds 1 MiB"})
		return
	}

	// Two-step parse: JSON validity first (-32700), then envelope shape
	// (-32600 — a valid-JSON array or scalar is the wrong shape, not a
	// parse error). id is null in both: it was never recoverable.
	var raw json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeRPC(w, rpc.NewError(json.RawMessage("null"), rpc.Errorf(rpc.CodeParseError, "parse error: %v", err)))
		return
	}
	var req rpc.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		writeRPC(w, rpc.NewError(json.RawMessage("null"), rpc.Errorf(rpc.CodeInvalidRequest, "invalid request: %v", err)))
		return
	}
	if err := req.Validate(); err != nil {
		writeRPC(w, rpc.NewError(req.ID, err)) // id recovered: echo it
		return
	}
	if req.IsNotification() {
		w.WriteHeader(http.StatusNoContent) // notifications get no reply (spec §4)
		return
	}

	result, err := f.rpcReg.Dispatch(r.Context(), req.Method, req.Params)
	if err != nil {
		writeRPC(w, rpc.NewError(req.ID, err))
		return
	}
	resp, err := rpc.NewResult(req.ID, result)
	if err != nil {
		writeRPC(w, rpc.NewError(req.ID, rpc.Errorf(rpc.CodeInternalError, "marshal result: %v", err)))
		return
	}
	writeRPC(w, resp)
}

// writeRPC emits one JSON-RPC response envelope with HTTP 200.
func writeRPC(w http.ResponseWriter, resp *rpc.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
