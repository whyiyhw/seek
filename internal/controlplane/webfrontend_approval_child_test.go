package controlplane

// W5 card c_n37tnr, child half — the end-to-end acceptance the card
// names: 「桩 seek 全流转（事件→卡→点→子进程真收到决定行→继续跑）、
// cancel 撤卡」. Real components throughout: WebFrontend (httptest) +
// Router + Executor over the re-exec stub seek speaking the real
// child contract (approval_roundtrip / approval_leftover modes).
//
//	① full loop: stub emits approval_request → SSE `approval` card →
//	   POST /rpc approval/decide {y|n|a} → the executor's stdin writer
//	   feeds the child → the child echoes the line it read back as a
//	   text_delta → the run continues to agent_end and the reply
//	   close-out carries it. The decision reaching the SUBPROCESS is
//	   what is asserted, not just the plane's own bookkeeping.
//	② cancel: the stub ends with the card still pending → the browser
//	   receives the `cancelled` frame and approval/list stops listing it.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWeb_ApprovalChildRoundTrip is acceptance ① at the wire level.
func TestWeb_ApprovalChildRoundTrip(t *testing.T) {
	fe, srv, tok := newStreamE2E(t, "approval_roundtrip")
	fe.RegisterApprovalMethods(fe.RPCRegistry())
	t.Setenv("CP_STUB_REQID", "ap-child-7")

	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	postMsg(t, srv, tok, "/bind "+t.TempDir())
	wantFrame(t, tap, "", "已绑定项目")

	postMsg(t, srv, tok, "审批")

	// The card: pending, the stub's payload, and the live run id.
	fr := wantFrame(t, tap, "approval", `"id":"ap-child-7"`, `"state":"pending"`, `"kind":"bash"`)
	var card approvalView
	if err := json.Unmarshal([]byte(fr.data), &card); err != nil {
		t.Fatalf("card frame: %v (%s)", err, fr.data)
	}

	// Click "allow once" — the plane writes the line into the child.
	env, _ := callApproval(t, srv, tok, "approval/decide", `{"id":"ap-child-7","decision":"y"}`)
	if e := env["error"]; e != nil {
		t.Fatalf("approval/decide errored: %s", e)
	}
	if !strings.Contains(string(env["result"]), `"applied":true`) {
		t.Errorf("decide result = %s, want applied:true", env["result"])
	}

	// The receipt frame (resolved) …
	wantFrame(t, tap, "approval", `"state":"resolved"`, `"decision":"y"`)

	// … then the child's proof it read the line, then the close-out.
	d := wantFrame(t, tap, "delta", `decision:`)
	var del webDeltaEvent
	if err := json.Unmarshal([]byte(d.data), &del); err != nil {
		t.Fatalf("delta frame: %s (%v)", d.data, err)
	}
	wantLine := `decision:{"type":"approval_decision","req_id":"ap-child-7","decision":"y"}`
	if del.Text != wantLine {
		t.Fatalf("child's echoed decision = %q, want %q", del.Text, wantLine)
	}
	wantFrame(t, tap, "", `"conv":"web:default"`)

	// Card gone from the pending snapshot.
	if n := countPending(t, srv, tok, `{}`); n != 0 {
		t.Errorf("pending approvals after the decision = %d, want 0", n)
	}
}

// TestWeb_ApprovalRunEndWithdrawsCard is acceptance ②: the run ends
// with the card unanswered → the browser gets `cancelled` and the card
// stops listing. Nothing but the producer's own run-end can withdraw
// it (the human never clicked).
func TestWeb_ApprovalRunEndWithdrawsCard(t *testing.T) {
	fe, srv, tok := newStreamE2E(t, "approval_leftover")
	fe.RegisterApprovalMethods(fe.RPCRegistry())
	t.Setenv("CP_STUB_REQID", "ap-left-web")

	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	postMsg(t, srv, tok, "/bind "+t.TempDir())
	wantFrame(t, tap, "", "已绑定项目")

	postMsg(t, srv, tok, "审批")

	wantFrame(t, tap, "approval", `"id":"ap-left-web"`, `"state":"pending"`)
	// The stub never reads stdin; the attempt ends at once, so the
	// framework's Cancel fires and the browser's card is withdrawn.
	wantFrame(t, tap, "approval", `"id":"ap-left-web"`, `"state":"cancelled"`)

	if n := countPending(t, srv, tok, `{}`); n != 0 {
		t.Errorf("pending approvals after the run ended = %d, want 0", n)
	}
	// A late decide against the withdrawn card is an idempotent no-op
	// (applied=false, state cancelled) — the browser's click raced the
	// run's death, and the client has already dropped the card.
	env, _ := callApproval(t, srv, tok, "approval/decide", `{"id":"ap-left-web","decision":"y"}`)
	res := string(env["result"])
	if !strings.Contains(res, `"applied":false`) || !strings.Contains(res, `"state":"cancelled"`) {
		t.Errorf("late decide reply = %s, want applied:false state:cancelled", env)
	}
}

// TestWeb_ApprovalFrontendImplementsSeam pins the upcast the router
// relies on: the web frontend is an ApprovalFrontend, and its router
// is freshly minted per run (ids cached across runs would deliver a
// dead child's decisions into the next run).
func TestWeb_ApprovalFrontendImplementsSeam(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	af, ok := any(fe).(ApprovalFrontend)
	if !ok {
		t.Fatal("WebFrontend must implement ApprovalFrontend")
	}
	a, b := af.ApprovalRoute(), af.ApprovalRoute()
	if a == nil || b == nil || a == b {
		t.Fatalf("ApprovalRoute must mint a fresh router per run: %v / %v", a, b)
	}
}
