package main

// ACP approval-lane fixtures (P0-2, backlog ①): acpApproval.ask against
// a REAL acp.Server over pipes (scripted client) and a REAL
// permission.Policy — the three states plus the always-allow grant's
// idempotence (the second same-Kind action must pass WITHOUT a second
// request reaching the client). The real-binary end-to-end lives in
// acp_approval_e2e_test.go; the transport primitives live in
// internal/acp/permission_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/permission"
)

// nopBackend satisfies acp.Backend for transport-only tests (none of
// its methods is ever called here).
type nopBackend struct{}

func (nopBackend) Initialize(p acp.InitializeParams) acp.InitializeResult {
	return acp.InitializeResult{ProtocolVersion: p.ProtocolVersion}
}
func (nopBackend) NewSession(p acp.NewSessionParams) (acp.NewSessionResult, error) {
	return acp.NewSessionResult{SessionID: "sess-1"}, nil
}
func (nopBackend) LoadSession(p acp.LoadSessionParams, send func(acp.SessionUpdate)) (acp.LoadSessionResult, error) {
	return acp.LoadSessionResult{}, nil
}
func (nopBackend) Prompt(ctx context.Context, p acp.PromptParams, send func(acp.SessionUpdate)) (acp.PromptResult, error) {
	return acp.PromptResult{StopReason: "end_turn"}, nil
}
func (nopBackend) SetConfigOption(p acp.SetConfigOptionParams) (acp.SetConfigOptionResult, error) {
	return acp.SetConfigOptionResult{}, nil
}

// acpApprovalHarness wires asker + server + policy over pipes and
// scripts the client side.
type acpApprovalHarness struct {
	srv    *acp.Server
	enc    *json.Encoder // client→server
	dec    *json.Decoder // server→client
	pol    *permission.Policy
	ask    *acpApproval
	frames chan json.RawMessage // every agent→client frame, for counting requests
}

func newACPApprovalHarness(t *testing.T, timeout time.Duration) *acpApprovalHarness {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv := acp.NewServer(c2sR, s2cW, nopBackend{})
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() { cancel(); c2sW.Close(); s2cW.Close() })

	pol, err := permission.New(t.TempDir(), permission.PrefAsk)
	if err != nil {
		t.Fatalf("permission.New: %v", err)
	}
	ask := newACPApproval(srv, pol.SetAlwaysAllow, timeout)
	pol.SetAskFn(ask.ask)
	ask.beginTurn(ctx, "sess-1") // tests run "inside" a prompt

	h := &acpApprovalHarness{
		srv: srv, enc: json.NewEncoder(c2sW), dec: json.NewDecoder(s2cR),
		pol: pol, ask: ask,
		frames: make(chan json.RawMessage, 16),
	}
	go func() {
		for {
			var raw json.RawMessage
			if h.dec.Decode(&raw) != nil {
				close(h.frames)
				return
			}
			h.frames <- raw
		}
	}()
	return h
}

// nextFrame returns the next agent→client frame (must arrive within 5s).
func (h *acpApprovalHarness) nextFrame(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	select {
	case raw, ok := <-h.frames:
		if !ok {
			t.Fatal("server→client stream closed")
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("frame not json: %v (%s)", err, raw)
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no agent→client frame within 5s")
		return nil
	}
}

// answerPermission reads one request_permission frame and answers with
// the given optionId (selected outcome, v1 schema shape).
func (h *acpApprovalHarness) answerPermission(t *testing.T, optionID string) {
	t.Helper()
	m := h.nextFrame(t)
	if string(m["method"]) != `"session/request_permission"` {
		t.Fatalf("method = %s, want session/request_permission", m["method"])
	}
	id := m["id"]
	_ = h.enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result": map[string]any{
			"outcome": map[string]any{"outcome": "selected", "optionId": optionID},
		},
	})
}

func acpBashAction() permission.Action {
	return permission.Action{Kind: permission.KindBash, Command: "echo hi"}
}

// checkAsync runs policy.Check in the background (the ask inside it
// blocks until the client answers) and returns the result channel.
func (h *acpApprovalHarness) checkAsync(a permission.Action) <-chan error {
	res := make(chan error, 1)
	go func() { res <- h.pol.Check(a) }()
	return res
}

func TestACPApproval_AllowOnceReleasesGate(t *testing.T) {
	h := newACPApprovalHarness(t, 5*time.Second)
	res := h.checkAsync(acpBashAction())
	h.answerPermission(t, acp.OptionAllowOnce)
	if err := <-res; err != nil {
		t.Fatalf("Check after allow_once: %v", err)
	}
}

func TestACPApproval_RejectDenies(t *testing.T) {
	h := newACPApprovalHarness(t, 5*time.Second)
	res := h.checkAsync(acpBashAction())
	h.answerPermission(t, acp.OptionRejectOnce)
	err := <-res
	if !errors.Is(err, permission.ErrDenied) {
		t.Fatalf("Check after reject_once = %v, want ErrDenied", err)
	}
}

func TestACPApproval_TimeoutDefaultsToDeny(t *testing.T) {
	h := newACPApprovalHarness(t, 100*time.Millisecond)
	// No answer scripted: the frame arrives, silence follows, the short
	// deadline fires.
	res := h.checkAsync(acpBashAction())
	m := h.nextFrame(t)
	if string(m["method"]) != `"session/request_permission"` {
		t.Fatalf("method = %s", m["method"])
	}
	select {
	case err := <-res:
		if !errors.Is(err, permission.ErrDenied) {
			t.Fatalf("Check after timeout = %v, want ErrDenied", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check did not return after the ask timed out")
	}
}

// TestACPApproval_AlwaysGrantsAndIsIdempotent pins the "always" mapping
// (P0-2 ①): allow_always releases the action AND installs the per-Kind
// session grant, so the next bash action passes WITHOUT a second
// request frame reaching the client — the exact --approval-stdin "a"
// semantics.
func TestACPApproval_AlwaysGrantsAndIsIdempotent(t *testing.T) {
	h := newACPApprovalHarness(t, 5*time.Second)
	res := h.checkAsync(acpBashAction())
	h.answerPermission(t, acp.OptionAllowAlways)
	if err := <-res; err != nil {
		t.Fatalf("Check after allow_always: %v", err)
	}
	// Second same-Kind action: granted locally, no new request frame.
	if err := h.pol.Check(permission.Action{Kind: permission.KindBash, Command: "echo again"}); err != nil {
		t.Fatalf("second bash after allow_always: %v", err)
	}
	select {
	case m := <-h.frames:
		t.Fatalf("unexpected second frame after allow_always: %v", m)
	default:
	}
}

// TestACPApproval_GrantScopesToKind: an out-of-cwd write is dangerous →
// asks → allow_always on write must NOT pre-grant bash (the grant is
// per-Kind, matching the TUI's and --approval-stdin's "[a] always:
// <kind>").
func TestACPApproval_GrantScopesToKind(t *testing.T) {
	h := newACPApprovalHarness(t, 5*time.Second)
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	res := h.checkAsync(permission.Action{Kind: permission.KindWrite, Path: outside})
	h.answerPermission(t, acp.OptionAllowAlways)
	if err := <-res; err != nil {
		t.Fatalf("out-of-cwd write after allow_always(write): %v", err)
	}
	// bash was never granted: it must ask again.
	res2 := h.checkAsync(acpBashAction())
	h.answerPermission(t, acp.OptionAllowOnce)
	if err := <-res2; err != nil {
		t.Fatalf("bash after allow_always(write): %v", err)
	}
}

func TestACPApproval_NoTurnDenies(t *testing.T) {
	h := newACPApprovalHarness(t, 5*time.Second)
	h.ask.endTurn()
	err := h.pol.Check(acpBashAction())
	if !errors.Is(err, permission.ErrDenied) {
		t.Fatalf("Check outside a turn = %v, want ErrDenied", err)
	}
	// And nothing was sent to the client.
	select {
	case m := <-h.frames:
		t.Fatalf("frame sent outside a turn: %v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestACPApproval_TimeoutFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", acpPermissionTimeoutDefault},
		{"30", 30 * time.Second},
		{"  45 ", 45 * time.Second},
		{"junk", acpPermissionTimeoutDefault},
		{"0", acpPermissionTimeoutDefault},
		{"-5", acpPermissionTimeoutDefault},
	}
	for _, c := range cases {
		got := acpPermissionTimeoutFromEnv(func(string) string { return c.env })
		if got != c.want {
			t.Errorf("timeoutFromEnv(%q) = %v, want %v", c.env, got, c.want)
		}
	}
}

// TestACPApproval_KindMapping pins the Action→wire mapping the client
// renders (execute/edit/read + command/path content).
func TestACPApproval_KindMapping(t *testing.T) {
	if k := acpPermToolKind(permission.KindBash); k != "execute" {
		t.Errorf("bash kind = %q", k)
	}
	if k := acpPermToolKind(permission.KindEdit); k != "edit" {
		t.Errorf("edit kind = %q", k)
	}
	if k := acpPermToolKind(permission.KindRead); k != "read" {
		t.Errorf("read kind = %q", k)
	}
	if k := acpPermToolKind(permission.KindMemoryRemember); k != "other" {
		t.Errorf("memory kind = %q", k)
	}
	c := acpPermToolContent(permission.Action{Kind: permission.KindBash, Command: "git status"})
	if len(c) != 1 || c[0].Content.Text != "git status" {
		t.Errorf("bash content = %+v", c)
	}
}
