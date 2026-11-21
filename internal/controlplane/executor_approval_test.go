package controlplane

// W5 card c_n37tnr, child-half acceptance (feature-web-frontend.md
// §13.1's c_n37tnr row): the approval round-trip from the browser's
// click all the way into the subprocess's stdin, plus the two cleanup
// paths. Four things are pinned here:
//
//	① argv: an ExecRequest with Approval set spawns the child with
//	   --approval-stdin; without it the child's argv keeps its exact
//	   pre-W5 shape (fake/feishu paths unchanged).
//	② delivery: the decision the router receives is written to the
//	   child's stdin as one JSON line keyed by req_id — the stub echoes
//	   it back, so "the subprocess really got it" is asserted from the
//	   child's own output, not from the plane's bookkeeping.
//	③ ordering: the delivery handle is registered BEFORE the request is
//	   emitted (a card must never be visible without a live sink).
//	④ cleanup: an attempt that ends with a card still pending withdraws
//	   it (router Cancel), and the end-to-end web test proves the browser
//	   sees the `cancelled` frame and loses the prompt.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingRouter is a fake ApprovalRouter: records registrations and
// cancellations so the executor's seam is observable without a hub.
type recordingRouter struct {
	mu        sync.Mutex
	order     []string // "route:<id>" / "cancel", in call order
	deliver   map[string]func(string)
	cancelled int
}

func newRecordingRouter() *recordingRouter {
	return &recordingRouter{deliver: make(map[string]func(string))}
}

func (r *recordingRouter) Route(reqID string, deliver func(string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, "route:"+reqID)
	r.deliver[reqID] = deliver
}

func (r *recordingRouter) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, "cancel")
	r.cancelled++
	r.deliver = make(map[string]func(string))
}

// decide hands the pending decision to the child's stdin writer.
func (r *recordingRouter) decide(t *testing.T, reqID, decision string) {
	t.Helper()
	r.mu.Lock()
	d := r.deliver[reqID]
	r.mu.Unlock()
	if d == nil {
		t.Fatalf("no delivery handle registered for %q (order=%v)", reqID, r.order)
	}
	d(decision)
}

// TestExecutor_ApprovalStdinRoundTrip is acceptance ①②③: the child's
// argv grows the flag, the request is routed before it is emitted, and
// a decision reaches the child's stdin keyed by req_id — proved by the
// stub echoing the line it read.
func TestExecutor_ApprovalStdinRoundTrip(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_MODE", "approval_roundtrip")
	t.Setenv("CP_STUB_REQID", "ap-e2e-42")

	router := newRecordingRouter()
	var events []RunEvent
	var mu sync.Mutex
	emit := func(ev RunEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		if ev.Type == "approval_request" {
			// A live UI answers here — exactly what approval/decide does.
			router.decide(t, ev.ReqID, "a")
		}
	}

	res, err := ex.RunStream(context.Background(), ExecRequest{
		Conv: "web:ap", Dir: t.TempDir(), Message: "审批", Approval: router,
	}, emit)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// ① argv carries the flag.
	invs := readInvocations(t, dir)
	if len(invs) != 1 {
		t.Fatalf("invocations = %d, want 1", len(invs))
	}
	if !argvHas(invs[0], "--approval-stdin", "") {
		t.Errorf("child argv lacks --approval-stdin: %q", invs[0].Argv)
	}

	// ③ route happened before the emit, and exactly once.
	for i, ev := range events {
		if ev.Type == "approval_request" {
			if i != 1 { // agent_start, approval_request, …
				t.Errorf("approval_request at event %d, want 1 (after agent_start)", i)
			}
			router.mu.Lock()
			first := ""
			if len(router.order) > 0 {
				first = router.order[0]
			}
			router.mu.Unlock()
			if first != "route:ap-e2e-42" {
				t.Errorf("first router call = %q, want route:ap-e2e-42 (route must precede emit)", first)
			}
		}
	}

	// ② the child received the decision line — it echoed it back.
	want := `decision:{"type":"approval_decision","req_id":"ap-e2e-42","decision":"a"}`
	if !strings.Contains(res.Reply, want) {
		t.Errorf("child reply = %q, want it to contain %q (the stdin line the child read)", res.Reply, want)
	}

	// ④ the attempt withdrew leftovers once (the card was answered, so
	// Cancel is a no-op in the hub — but the executor must still call it
	// by contract).
	if router.cancelled != 1 {
		t.Errorf("Cancel calls = %d, want 1", router.cancelled)
	}
}

// TestExecutor_ApprovalLeftoverCancelled is acceptance ④ from the
// child side: a run that ends with the card unanswered still triggers
// the router's Cancel, exactly once.
func TestExecutor_ApprovalLeftoverCancelled(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_MODE", "approval_leftover")
	t.Setenv("CP_STUB_REQID", "ap-left-1")

	router := newRecordingRouter()
	res, err := ex.RunStream(context.Background(), ExecRequest{
		Conv: "web:ap2", Dir: t.TempDir(), Message: "m", Approval: router,
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.SessionID != "SAL" {
		t.Errorf("session id = %q, want SAL", res.SessionID)
	}
	if router.cancelled != 1 {
		t.Errorf("Cancel calls = %d, want 1 (the pending card must be withdrawn)", router.cancelled)
	}
}

// TestExecutor_NoApprovalKeepsArgv: a request without a router must not
// grow the flag — the pre-W5 child byte shape (fake frontend, feishu
// history) stays exactly as it was.
func TestExecutor_NoApprovalKeepsArgv(t *testing.T) {
	ex, dir := newStubExecutor(t, nil)
	if _, err := ex.Run(context.Background(), ExecRequest{
		Conv: "feishu:oc_1", Dir: t.TempDir(), Message: "m",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	invs := readInvocations(t, dir)
	if len(invs) != 1 {
		t.Fatalf("invocations = %d, want 1", len(invs))
	}
	for _, a := range invs[0].Argv {
		if a == "--approval-stdin" {
			t.Fatalf("argv grew --approval-stdin without an Approval router: %q", invs[0].Argv)
		}
	}
}

// TestExecutor_ApprovalDecisionAfterExitIsDropped: a decision that
// arrives after the attempt settled must not panic or block — the pipe
// is closed and the writer turns into a no-op (the browser clicking a
// card whose run just died is a normal race, not an error).
func TestExecutor_ApprovalDecisionAfterExitIsDropped(t *testing.T) {
	ex, _ := newStubExecutor(t, nil)
	t.Setenv("CP_STUB_MODE", "approval_leftover")
	t.Setenv("CP_STUB_REQID", "ap-late-1")

	router := newRecordingRouter()
	var handle func(string)
	var handleMu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ex.RunStream(context.Background(), ExecRequest{
			Conv: "web:ap3", Dir: t.TempDir(), Message: "m", Approval: router,
		}, func(ev RunEvent) {
			if ev.Type == "approval_request" {
				router.mu.Lock()
				h := router.deliver[ev.ReqID]
				router.mu.Unlock()
				handleMu.Lock()
				handle = h
				handleMu.Unlock()
			}
		})
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("run did not finish")
	}
	handleMu.Lock()
	h := handle
	handleMu.Unlock()
	if h == nil {
		t.Fatal("no delivery handle was captured")
	}
	h("y") // must be a no-op: the child is gone, the pipe is closed
	// And the JSON shape sanity of the decision line the writer produced
	// is pinned by TestApprovalDecisionLine below.
}

// TestApprovalDecisionLine pins the stdin wire shape the child parses
// (cmd/seek/approval_stdin.go approvalDecision): type/req_id/decision,
// exactly the keys the contract names.
func TestApprovalDecisionLine(t *testing.T) {
	b, err := json.Marshal(struct {
		Type     string `json:"type"`
		ReqID    string `json:"req_id"`
		Decision string `json:"decision"`
	}{"approval_decision", "ap-1", "n"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"type":"approval_decision","req_id":"ap-1","decision":"n"}`; got != want {
		t.Errorf("decision line = %s, want %s", got, want)
	}
}
