package main

// W5 card c_n37tnr, child-half acceptance for the `seek -p --json
// --approval-stdin` relay (cmd/seek/approval_stdin.go). The plane-side
// tests (internal/controlplane) prove the pipe and the routing; these
// pin the child's own promises:
//
//	① askFn emits one approval_request jsonLine and blocks until the
//	   matching approval_decision line arrives on its reader;
//	② "a" grants the per-Kind session allowlist (SetAlwaysAllow) and
//	   allows; "y" allows once; "n" denies;
//	③ a decision for another req_id is ignored (not mismatched);
//	④ junk / non-decision lines are skipped, and stdin EOF denies
//	   (today's print-mode default — an unattended run must not hang);
//	⑤ ctx cancellation withdraws the card (approval_cancel) and denies;
//	⑥ a request still pending when the run ends is withdrawn by
//	   CancelPending, exactly once.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/permission"
)

// lineSink collects emitted jsonLines behind a mutex — the relay emits
// from the tool goroutine, so tests must not read a bare slice.
type lineSink struct {
	mu    sync.Mutex
	lines []jsonLine
}

func (s *lineSink) emit(l jsonLine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, l)
}

func (s *lineSink) snapshot() []jsonLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]jsonLine(nil), s.lines...)
}

// newRelayForTest wires a relay over a scripted decision reader.
func newRelayForTest(script string, granted *[]permission.Kind) (*approvalRelay, *lineSink) {
	sink := &lineSink{}
	r := newApprovalRelay(strings.NewReader(script))
	r.emit = sink.emit
	if granted != nil {
		r.grant = func(k permission.Kind) { *granted = append(*granted, k) }
	}
	return r, sink
}

func decisionLine(reqID, decision string) string {
	b, _ := json.Marshal(map[string]string{
		"type": "approval_decision", "req_id": reqID, "decision": decision,
	})
	return string(b) + "\n"
}

// askAsync runs ask in the background and returns its result channel —
// the relay BLOCKS by design, so every test drives it asynchronously.
func askAsync(r *approvalRelay, ctx context.Context, a permission.Action) <-chan bool {
	ch := make(chan bool, 1)
	go func() { ch <- r.askFn(ctx)(a) }()
	return ch
}

// TestApprovalRelay_AllowOnce is ①②'s "y": request line out, allow in.
func TestApprovalRelay_AllowOnce(t *testing.T) {
	var granted []permission.Kind
	r, sink := newRelayForTest("", &granted)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The id is minted inside ask, so the scripted answer must be
	// req_id-agnostic — the reader answers whatever id just went out.
	r.in = &anyIDReader{sink: sink, decision: "y"}
	res := askAsync(r, ctx, permission.Action{Kind: permission.KindBash, Command: "go test ./..."})
	if !<-res {
		t.Fatal("y must allow")
	}
	if len(granted) != 0 {
		t.Errorf("y must not grant the session allowlist, granted=%v", granted)
	}
	lines := sink.snapshot()
	if len(lines) != 1 || lines[0].Type != "approval_request" {
		t.Fatalf("emitted lines = %+v, want one approval_request", lines)
	}
	if lines[0].ReqID == "" || lines[0].Kind != "bash" || lines[0].Command != "go test ./..." {
		t.Errorf("request line = %+v, want req_id/kind/bash+command", lines[0])
	}
}

// anyIDReader answers each read with a decision line reusing the req_id
// the relay just minted (it watches the sink for the request line).
type anyIDReader struct {
	sink     *lineSink
	decision string
}

func (a *anyIDReader) Read(p []byte) (int, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ls := a.sink.snapshot(); len(ls) > 0 && ls[len(ls)-1].ReqID != "" {
			return copy(p, decisionLine(ls[len(ls)-1].ReqID, a.decision)), nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return 0, nil // timeout: EOF, which the relay reads as deny
}

// TestApprovalRelay_AlwaysGrants is ①②'s "a": the per-Kind grant fires
// with the ACTION's kind (set by SetAlwaysAllow in production).
func TestApprovalRelay_AlwaysGrants(t *testing.T) {
	var granted []permission.Kind
	r, sink := newRelayForTest("", &granted)
	r.in = &anyIDReader{sink: sink, decision: "a"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := askAsync(r, ctx, permission.Action{Kind: permission.KindEdit, Path: "/tmp/x.go"})
	if !<-res {
		t.Fatal("a must allow")
	}
	if len(granted) != 1 || granted[0] != permission.KindEdit {
		t.Fatalf("granted = %v, want exactly [edit]", granted)
	}
}

// TestApprovalRelay_Deny: "n" denies, and a nil grant degrades "a" to
// allow-once (the TUI's nil-AlwaysAllowKind posture).
func TestApprovalRelay_Deny(t *testing.T) {
	r, sink := newRelayForTest("", nil)
	r.in = &denyReader{sink: sink}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if <-askAsync(r, ctx, permission.Action{Kind: permission.KindBash}) {
		t.Fatal("n must deny")
	}
}

type denyReader struct{ sink *lineSink }

func (d *denyReader) Read(p []byte) (int, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ls := d.sink.snapshot(); len(ls) > 0 && ls[len(ls)-1].ReqID != "" {
			return copy(p, decisionLine(ls[len(ls)-1].ReqID, "n")), nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return 0, nil
}

// TestApprovalRelay_IgnoresStaleAndJunk is ③④: a decision for someone
// else's req_id and a non-decision line are both skipped; the matching
// line still lands.
func TestApprovalRelay_IgnoresStaleAndJunk(t *testing.T) {
	script := "not json\n" +
		`{"type":"agent_start"}` + "\n" +
		decisionLine("someone-else", "y") +
		decisionLine("ap-1", "y")
	var granted []permission.Kind
	r, sink := newRelayForTest(script, &granted)
	r.in = &sequencedReader{script: script, sink: sink}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !<-askAsync(r, ctx, permission.Action{Kind: permission.KindBash}) {
		t.Fatal("the matching y must still answer after junk + a stale id")
	}
}

// sequencedReader replays the fixed script, with the final line's
// req_id rewritten to the minted one so ordering (junk first, match
// last) is preserved.
type sequencedReader struct {
	script string
	sink   *lineSink
	sent   bool
}

func (s *sequencedReader) Read(p []byte) (int, error) {
	if s.sent {
		return 0, nil // EOF: the relay's reader goroutine exits
	}
	s.sent = true
	deadline := time.Now().Add(5 * time.Second)
	id := ""
	for time.Now().Before(deadline) && id == "" {
		if ls := s.sink.snapshot(); len(ls) > 0 {
			id = ls[len(ls)-1].ReqID
		}
		time.Sleep(2 * time.Millisecond)
	}
	out := strings.ReplaceAll(s.script, `"ap-1"`, `"`+id+`"`)
	return copy(p, out), nil
}

// TestApprovalRelay_EOFDenies is ④: no decider attached (stdin closed)
// is a denial and a withdrawn card — never a hung run.
func TestApprovalRelay_EOFDenies(t *testing.T) {
	var granted []permission.Kind
	r, sink := newRelayForTest("", &granted) // empty reader: immediate EOF
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := askAsync(r, ctx, permission.Action{Kind: permission.KindBash})

	select {
	case ok := <-done:
		if ok {
			t.Fatal("EOF must deny")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("askFn hung on a closed stdin (must deny, not block forever)")
	}
	lines := sink.snapshot()
	if len(lines) != 2 {
		t.Fatalf("lines = %+v, want request + cancel", lines)
	}
	if lines[0].Type != "approval_request" || lines[1].Type != "approval_cancel" {
		t.Errorf("line types = %s,%s, want approval_request,approval_cancel", lines[0].Type, lines[1].Type)
	}
	if lines[1].ReqID != lines[0].ReqID {
		t.Errorf("cancel req_id %q != request %q", lines[1].ReqID, lines[0].ReqID)
	}
}

// TestApprovalRelay_ContextCancelWithdraws is ⑤: a cancelled session
// denies and withdraws the card instead of leaving it up.
func TestApprovalRelay_ContextCancelWithdraws(t *testing.T) {
	r, sink := newRelayForTest("", nil) // never answers
	ctx, cancel := context.WithCancel(context.Background())
	done := askAsync(r, ctx, permission.Action{Kind: permission.KindWrite, Path: "/tmp/y"})
	// Give ask a moment to emit, then cancel.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(sink.snapshot()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled session must deny")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("askFn did not observe ctx cancellation")
	}
	lines := sink.snapshot()
	if len(lines) != 2 || lines[1].Type != "approval_cancel" {
		t.Fatalf("lines = %+v, want request then cancel", lines)
	}
}

// TestApprovalRelay_CancelPending is ⑥: the run-end sweep withdraws
// every still-open id exactly once, and a second sweep is a no-op.
func TestApprovalRelay_CancelPending(t *testing.T) {
	r, sink := newRelayForTest("", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = askAsync(r, ctx, permission.Action{Kind: permission.KindBash})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(sink.snapshot()) == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	r.CancelPending()
	r.CancelPending() // idempotent
	lines := sink.snapshot()
	cancels := 0
	for _, l := range lines {
		if l.Type == "approval_cancel" {
			cancels++
		}
	}
	if cancels != 1 {
		t.Errorf("cancel lines = %d, want exactly 1 (idempotent sweep): %+v", cancels, lines)
	}
}

// TestApprovalSummary pins the card's one-liner for the kinds the
// permission gate actually asks about.
func TestApprovalSummary(t *testing.T) {
	cases := []struct {
		a    permission.Action
		want string
	}{
		{permission.Action{Kind: permission.KindBash, Command: "ls"}, "run a shell command"},
		{permission.Action{Kind: permission.KindWrite, Path: "/tmp/a"}, "write /tmp/a"},
		{permission.Action{Kind: permission.KindEdit, Path: "/tmp/b"}, "edit /tmp/b"},
		{permission.Action{Kind: permission.KindMemoryRemember, Display: permission.Display{MemoryName: "n"}}, "save memory n"},
		{permission.Action{Kind: permission.KindSkillInstall, Display: permission.Display{SkillName: "s"}}, "install skill s"},
	}
	for _, c := range cases {
		if got := approvalSummary(c.a); got != c.want {
			t.Errorf("approvalSummary(%s) = %q, want %q", c.a.Kind, got, c.want)
		}
	}
}

// TestApprovalRelay_RequestLineShape pins the emitted wire shape: the
// keys the plane's RunEvent decodes (req_id/kind/summary/command/diff),
// nothing else, so the SSE card fields stay populated.
func TestApprovalRelay_RequestLineShape(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	var encMu sync.Mutex
	r := newApprovalRelay(strings.NewReader(""))
	r.emit = func(l jsonLine) {
		encMu.Lock()
		defer encMu.Unlock()
		_ = enc.Encode(l)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	<-askAsync(r, ctx, permission.Action{
		Kind: permission.KindEdit, Path: "/tmp/x.go",
		Display: permission.Display{Diff: "-a\n+b"},
	})

	var first map[string]any
	if err := json.Unmarshal(bytes.SplitN(buf.Bytes(), []byte("\n"), 2)[0], &first); err != nil {
		t.Fatalf("request line not JSON: %v (%s)", err, buf.String())
	}
	for _, k := range []string{"type", "req_id", "kind", "summary", "diff"} {
		if _, ok := first[k]; !ok {
			t.Errorf("request line lacks %q: %s", k, buf.String())
		}
	}
	if first["type"] != "approval_request" || first["kind"] != "edit" || first["diff"] != "-a\n+b" {
		t.Errorf("request line fields = %v", first)
	}
}
