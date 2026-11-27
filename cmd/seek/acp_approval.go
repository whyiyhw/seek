package main

// ACP·P0-2 (card c_7xsfsa, feature-acp.md backlog ①): the ACP approval
// lane — the THIRD surface wired to permission.Policy's askFn, after
// the TUI's askuser and print mode's --approval-stdin (approval_stdin.go;
// the web hub drives that same stdin channel, so this file completes
// the trio web-hub / print / ACP). Instead of a JSONL card on stdout it
// surfaces the request over the protocol itself —
// session/request_permission (agent→client JSON-RPC) — and maps the
// client's answer:
//
//	allow_once    → allow this action
//	allow_always  → allow + policy.SetAlwaysAllow(kind) — exactly the
//	                "[a] always: <kind>" / --approval-stdin "a" semantics
//	reject_once   → deny (the usual ErrDenied tool result)
//	cancelled / timeout / disconnect / client error / no active turn
//	             → deny — the unanswered default is DENY, mirroring
//	                --approval-stdin's EOF posture. A headless driver
//                (paseo) that doesn't implement the method answers
//                -32601, which lands here as "client error" → deny:
//                the pre-P0-2 bash-denied behaviour, never a hang.
//
// SEEK_ACP_YOLO=1 keeps its priority: that lane runs PrefYolo, so
// prefGate never consults askFn and this adapter is never even
// installed (runACP skips SetAskFn) — no request can be sent.
//
// Per-turn scoping: askFn's signature has no context, but a permission
// request must die with ITS turn (session/cancel must not leave the
// agent blocked on a prompt for a turn that is already gone). The
// backend's Prompt calls beginTurn(ctx, sessionId) / endTurn around the
// event loop, and ask() resolves the live scope under a mutex. An ask
// outside any turn denies — there is no client conversation to ask in.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/permission"
)

// acpApproval is the ACP-lane askFn. srv/grant/timeout are wired once
// in runACP before Serve starts; turn is swapped per prompt.
type acpApproval struct {
	srv     *acp.Server
	grant   func(permission.Kind) // policy.SetAlwaysAllow
	timeout time.Duration

	mu   sync.Mutex
	turn *acpTurnScope
	seq  atomic.Int64 // permission toolCallId suffix
}

// acpTurnScope is the live prompt's context + session id.
type acpTurnScope struct {
	ctx       context.Context
	sessionID string
}

func newACPApproval(srv *acp.Server, grant func(permission.Kind), timeout time.Duration) *acpApproval {
	return &acpApproval{srv: srv, grant: grant, timeout: timeout}
}

// beginTurn installs the scope ask() consults; endTurn drops it. Called
// from acpBackend.Prompt (tool execution happens within that goroutine
// tree, so the scope is always installed before any ask can fire; the
// mutex covers a pathological second concurrent prompt, where last
// begin wins and the first turn's asks still resolve against a live
// scope — acceptable for the one-process-one-session MVP).
func (ap *acpApproval) beginTurn(ctx context.Context, sessionID string) {
	ap.mu.Lock()
	ap.turn = &acpTurnScope{ctx: ctx, sessionID: sessionID}
	ap.mu.Unlock()
}

func (ap *acpApproval) endTurn() {
	ap.mu.Lock()
	ap.turn = nil
	ap.mu.Unlock()
}

// ask adapts permission.Policy's askFn to one session/request_permission
// round-trip. Every path that is not an explicit allow returns false.
func (ap *acpApproval) ask(a permission.Action) bool {
	ap.mu.Lock()
	t := ap.turn
	ap.mu.Unlock()
	if t == nil || t.ctx.Err() != nil {
		fmt.Fprintf(os.Stderr, "acp: permission %s denied: no active turn to ask in\n", a.Kind)
		return false
	}

	id := fmt.Sprintf("perm-%d", ap.seq.Add(1))
	params := acp.RequestPermissionParams{
		SessionID: t.sessionID,
		ToolCall: acp.PermissionToolCall{
			ToolCallID: id,
			Kind:       acpPermToolKind(a.Kind),
			Title:      approvalSummary(a),
			Content:    acpPermToolContent(a),
		},
		Options: acp.PermissionOptions(),
	}
	raw, err := ap.srv.Call(t.ctx, acp.MethodRequestPermission, params, ap.timeout)
	if err != nil {
		// Timeout / disconnect / cancellation / client JSON-RPC error
		// (a client that doesn't implement the method answers -32601):
		// all of them are "no answer" → deny. Log the ending so an
		// unattended lane's operator can see WHY nothing ran.
		fmt.Fprintf(os.Stderr, "acp: permission %s denied: %v\n", a.Kind, err)
		return false
	}
	switch acp.ParsePermissionOutcome(raw) {
	case acp.DecisionAllowOnce:
		return true
	case acp.DecisionAllowAlways:
		// "[a] always: <kind>" — same grant call the TUI and
		// --approval-stdin make; Check short-circuits this Kind until
		// the next SetPref/SetWorkflow resets the allowlist.
		if ap.grant != nil {
			ap.grant(a.Kind)
		}
		return true
	default: // reject / cancelled / unknown echo
		return false
	}
}

// acpPermToolKind maps permission.Kind onto the schema's ToolKind
// vocabulary ("execute"|"edit"|"read"|"other") — a UI hint for how the
// client renders the prompt.
func acpPermToolKind(k permission.Kind) string {
	switch k {
	case permission.KindBash:
		return "execute"
	case permission.KindWrite, permission.KindEdit:
		return "edit"
	case permission.KindRead:
		return "read"
	default:
		return "other"
	}
}

// acpPermToolContent carries the machine-readable detail (command / path /
// diff) as the toolCall's text content — the analogue of the JSONL
// card's command/diff fields.
func acpPermToolContent(a permission.Action) []acp.ToolCallContentWrap {
	wrap := func(text string) acp.ToolCallContentWrap {
		return acp.ToolCallContentWrap{Type: "content", Content: acp.ContentBlock{Type: "text", Text: text}}
	}
	switch a.Kind {
	case permission.KindBash:
		return []acp.ToolCallContentWrap{wrap(a.Command)}
	case permission.KindWrite, permission.KindEdit:
		if a.Display.Diff != "" {
			return []acp.ToolCallContentWrap{wrap(a.Path), wrap(a.Display.Diff)}
		}
		return []acp.ToolCallContentWrap{wrap(a.Path)}
	default:
		return nil
	}
}

// acpPermissionTimeoutDefault is how long one permission request waits
// for an answer before denying. Generous on purpose — an interactive
// user (Zed) may sit on the prompt for minutes — but bounded, because
// a client that ignores the request while holding stdio open would
// otherwise hang the turn forever (the exact failure mode P0-2 exists
// to close). SEEK_ACP_PERMISSION_TIMEOUT (seconds) overrides.
const acpPermissionTimeoutDefault = 10 * time.Minute

// acpPermissionTimeoutEnvVar overrides acpPermissionTimeoutDefault, in
// seconds (e.g. paseo unattended lanes may want 30, tests want 1).
const acpPermissionTimeoutEnvVar = "SEEK_ACP_PERMISSION_TIMEOUT"

// acpPermissionTimeoutFromEnv resolves the override. getenv is injected
// (os.Getenv in runACP, a stub in tests) — same testability posture as
// acp.YoloFromEnv. Unparseable / non-positive values keep the default.
func acpPermissionTimeoutFromEnv(getenv func(string) string) time.Duration {
	v := strings.TrimSpace(getenv(acpPermissionTimeoutEnvVar))
	if v == "" {
		return acpPermissionTimeoutDefault
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return acpPermissionTimeoutDefault
}
