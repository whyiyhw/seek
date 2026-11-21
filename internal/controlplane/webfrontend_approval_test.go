package controlplane

// W5 card c_n37tnr acceptance (feature-web-frontend §3.4 + §13.1):
// the remote approval card flow pinned at the hub level —
//
//	① full flow: fake approval event → SSE card frame → /rpc decide
//	   → decide callback fires once → receipt frame → card gone from
//	   approval/list;
//	② idempotency: repeated decides never re-fire the callback
//	   (applied=true then false, same recorded decision), unknown id
//	   and non-y/n/a decisions are -32602;
//	③ empty state: no approvals → empty list, zero approval frames on
//	   the conv stream, page ships the badge hidden + hidden-when-empty
//	   stack (static).
//
// Plus the stream translation (SendStream's approval_request /
// approval_cancel handling) and conv isolation. The child-side wiring
// (askFn → jsonLine event → stdin decision read-back) is deliberately
// out of scope — the follow-up card; the contract it will speak is
// documented in webfrontend_approval.go's header.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newApprovalWeb builds a web frontend with the approval methods
// registered (the servecli shape) and its httptest server.
func newApprovalWeb(t *testing.T) (*WebFrontend, *httptest.Server, string) {
	t.Helper()
	fe, _, tok := newTestWeb(t)
	fe.RegisterApprovalMethods(fe.RPCRegistry())
	srv := httptest.NewServer(fe.handler())
	t.Cleanup(srv.Close)
	return fe, srv, tok
}

// callApproval posts one /rpc call and decodes the envelope.
func callApproval(t *testing.T, srv *httptest.Server, tok, method, params string) (map[string]json.RawMessage, *http.Response) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/rpc?token="+tok, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	if err != nil {
		t.Fatalf("POST /rpc %s: %v", method, err)
	}
	defer resp.Body.Close()
	var env map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode /rpc %s reply: %v", method, err)
	}
	return env, resp
}

// TestWeb_ApprovalFullFlow is acceptance ①: fake event → card →
// decision → receipt → card disappears, end to end over the real HTTP
// + SSE surfaces.
func TestWeb_ApprovalFullFlow(t *testing.T) {
	fe, srv, tok := newApprovalWeb(t)
	conv := webConvID()
	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()

	// Fake producer: one bash approval with a command summary.
	got := make(chan string, 1)
	id := fe.RequestApproval(conv, ApprovalPayload{
		Kind: "bash", Summary: "在项目里跑测试",
		Command: "go test ./internal/controlplane/",
	}, func(d string) { got <- d })

	// Card frame: named `approval` event, pending, payload intact.
	fr := wantFrame(t, tap, "approval",
		`"id":"`+id+`"`, `"state":"pending"`, `"kind":"bash"`,
		`"summary":"在项目里跑测试"`, `"command":"go test ./internal/controlplane/"`)
	var card approvalView
	if err := json.Unmarshal([]byte(fr.data), &card); err != nil {
		t.Fatalf("card frame not an approvalView: %v (%s)", err, fr.data)
	}
	if card.Conv != string(conv) {
		t.Errorf("card conv = %q, want %q", card.Conv, conv)
	}

	// Decision via the new RPC method.
	env, _ := callApproval(t, srv, tok, "approval/decide",
		`{"id":"`+id+`","decision":"y"}`)
	if e := env["error"]; e != nil {
		t.Fatalf("approval/decide errored: %s", e)
	}
	var res struct {
		ID       string `json:"id"`
		State    string `json:"state"`
		Decision string `json:"decision"`
		Applied  bool   `json:"applied"`
	}
	if err := json.Unmarshal(env["result"], &res); err != nil {
		t.Fatalf("decode decide result: %v", err)
	}
	if !res.Applied || res.ID != id || res.State != "resolved" || res.Decision != "y" {
		t.Errorf("decide result = %+v, want applied resolved y for %s", res, id)
	}
	select {
	case d := <-got:
		if d != "y" {
			t.Errorf("decide callback got %q, want y", d)
		}
	default:
		t.Fatal("decide callback did not fire")
	}

	// Receipt frame: resolved with the decision — every tab drops the card.
	wantFrame(t, tap, "approval", `"state":"resolved"`, `"decision":"y"`, `"id":"`+id+`"`)

	// Card gone: the pending snapshot no longer lists it.
	env, _ = callApproval(t, srv, tok, "approval/list", `{}`)
	if s := string(env["result"]); !strings.Contains(s, `"approvals":[]`) && !strings.Contains(s, `"approvals":null`) {
		t.Errorf("approval/list after resolve = %s, want empty", s)
	}
}

// TestWeb_ApprovalDecideIdempotent is acceptance ②: repeated decides
// (double click, second tab racing the receipt) never re-fire the
// callback and answer the recorded outcome with applied=false.
func TestWeb_ApprovalDecideIdempotent(t *testing.T) {
	fe, srv, tok := newApprovalWeb(t)
	conv := webConvID()
	fires := 0
	id := fe.RequestApproval(conv, ApprovalPayload{Kind: "edit", Summary: "写文件"},
		func(string) { fires++ })

	decide := func(dec string) (bool, string) {
		env, _ := callApproval(t, srv, tok, "approval/decide",
			`{"id":"`+id+`","decision":"`+dec+`"}`)
		if e := env["error"]; e != nil {
			t.Fatalf("decide %s errored: %s", dec, e)
		}
		var res struct {
			Applied  bool   `json:"applied"`
			Decision string `json:"decision"`
		}
		json.Unmarshal(env["result"], &res)
		return res.Applied, res.Decision
	}
	if a, d := decide("n"); !a || d != "n" {
		t.Errorf("first decide = applied:%v decision:%s, want applied true n", a, d)
	}
	for i := 0; i < 2; i++ { // the replay path — same decision, no re-fire
		if a, d := decide("n"); a || d != "n" {
			t.Errorf("replay decide #%d = applied:%v decision:%s, want applied false n", i+1, a, d)
		}
	}
	// A DIFFERENT decision on a resolved card still reads back the
	// recorded one — the first decision is final.
	if a, d := decide("y"); a || d != "n" {
		t.Errorf("conflicting replay = applied:%v decision:%s, want applied false n", a, d)
	}
	if fires != 1 {
		t.Errorf("decide callback fired %d times, want exactly 1", fires)
	}

	// Producer retry with the same id never resurrects the card.
	if again := fe.RequestApproval(conv, ApprovalPayload{ID: id, Kind: "edit", Summary: "重试"}, nil); again != id {
		t.Errorf("retry id = %q, want the existing %q", again, id)
	}
	env, _ := callApproval(t, srv, tok, "approval/list", `{}`)
	if s := string(env["result"]); strings.Contains(s, `"pending"`) {
		t.Errorf("retry resurrected the card: %s", s)
	}
}

// TestWeb_ApprovalDecideValidation pins the -32602 tier: unknown id,
// decisions outside {y,n,a}, missing id.
func TestWeb_ApprovalDecideValidation(t *testing.T) {
	_, srv, tok := newApprovalWeb(t)
	for _, tc := range []struct{ name, params string }{
		{"unknown id", `{"id":"nope","decision":"y"}`},
		{"bad decision", `{"id":"x","decision":"maybe"}`},
		{"missing id", `{"decision":"y"}`},
	} {
		env, _ := callApproval(t, srv, tok, "approval/decide", tc.params)
		var e struct {
			Code int `json:"code"`
		}
		if env["error"] == nil {
			t.Fatalf("%s: no error envelope", tc.name)
		}
		json.Unmarshal(env["error"], &e)
		if e.Code != -32602 {
			t.Errorf("%s: code = %d, want -32602", tc.name, e.Code)
		}
	}
}

// TestWeb_ApprovalEmptyState is acceptance ③: with no approvals the
// list is empty, the conv stream carries zero approval frames, and
// the page ships the zero-footprint shape (badge hidden by default,
// stack hidden when empty).
func TestWeb_ApprovalEmptyState(t *testing.T) {
	fe, srv, tok := newApprovalWeb(t)
	env, _ := callApproval(t, srv, tok, "approval/list", `{}`)
	if s := string(env["result"]); strings.Contains(s, "pending") {
		t.Errorf("empty-state approval/list = %s", s)
	}

	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	fe.Send(nil, webConvID(), Reply{Text: "hi"})
	wantFrame(t, tap, "", `"text":"hi"`) // the reply lands…

	// …and no approval frame follows: drain what's buffered, then a
	// short quiet window proves none arrive unsolicited (heartbeat
	// comments don't parse as frames, so a clean read IS the proof).
	for {
		select {
		case fr, ok := <-tap:
			if !ok {
				t.Fatal("stream closed")
			}
			if fr.name == "approval" {
				t.Fatalf("unsolicited approval frame in empty state: %s", fr.data)
			}
			continue
		default:
		}
		break
	}

	page := string(webPage)
	for _, want := range []string{
		`id="ap-badge" hidden`,         // badge ships hidden
		`#approvals:empty { display: none; }`, // stack hides when empty
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing empty-state shape %q", want)
		}
	}
}

// TestWeb_ApprovalStreamTranslation pins SendStream's handling of the
// additive run events: approval_request files a card carrying the live
// run id (idempotent by req_id — a producer retry files nothing new),
// approval_cancel withdraws it, and a decide on a cancelled card reads
// back cancelled with applied=false.
func TestWeb_ApprovalStreamTranslation(t *testing.T) {
	fe, srv, tok := newApprovalWeb(t)
	tap, resp := openSSEFrames(t, srv, tok)
	defer resp.Body.Close()
	conv := webConvID()

	// A live run mints the run id (W1.4); the approval card must stamp it.
	fe.SendStream(nil, conv, RunEvent{Type: "text_delta", Delta: "x"})
	dfr := wantFrame(t, tap, "delta", `"text":"x"`)
	var d struct{ Run string }
	if err := json.Unmarshal([]byte(dfr.data), &d); err != nil || d.Run == "" {
		t.Fatalf("delta frame carries no run id: %s (%v)", dfr.data, err)
	}

	fe.SendStream(nil, conv, RunEvent{Type: "approval_request", ReqID: "req-1",
		Kind: "bash", Summary: "装依赖", Command: "npm install"})
	wantFrame(t, tap, "approval", `"id":"req-1"`, `"state":"pending"`,
		`"run":"`+d.Run+`"`, `"command":"npm install"`)

	// Producer retry with the same req_id: one pending card, no dup.
	fe.SendStream(nil, conv, RunEvent{Type: "approval_request", ReqID: "req-1", Kind: "bash"})
	if n := countPending(t, srv, tok, `{}`); n != 1 {
		t.Errorf("after retry, pending = %d, want 1", n)
	}

	// Withdrawal: the cancelled receipt drops every tab's card; a late
	// decide reads cancelled, applied=false (no decision exists).
	fe.SendStream(nil, conv, RunEvent{Type: "approval_cancel", ReqID: "req-1"})
	wantFrame(t, tap, "approval", `"id":"req-1"`, `"state":"cancelled"`)
	env, _ := callApproval(t, srv, tok, "approval/decide", `{"id":"req-1","decision":"y"}`)
	var res struct {
		State   string `json:"state"`
		Applied bool   `json:"applied"`
	}
	json.Unmarshal(env["result"], &res)
	if res.State != "cancelled" || res.Applied {
		t.Errorf("decide after cancel = %+v, want cancelled applied=false", res)
	}
}

// TestWeb_ApprovalConvIsolation: cards are conv-scoped like every
// other stream surface — another conv's list never sees them.
func TestWeb_ApprovalConvIsolation(t *testing.T) {
	fe, srv, tok := newApprovalWeb(t)
	fe.RequestApproval(webConvID(), ApprovalPayload{Kind: "bash", Summary: "s"}, nil)
	if n := countPending(t, srv, tok, `{"conv":"another-tab"}`); n != 0 {
		t.Errorf("other conv sees %d pending cards, want 0", n)
	}
	if n := countPending(t, srv, tok, `{}`); n != 1 { // absent conv → the legacy default
		t.Errorf("own conv sees %d pending cards, want 1", n)
	}
}

// TestWeb_ApprovalParseRunEvent pins the additive jsonLine contract's
// decoding into RunEvent (the shape the follow-up child and the
// sandbox stubs both speak).
func TestWeb_ApprovalParseRunEvent(t *testing.T) {
	ev, ok := parseRunEvent(`{"type":"approval_request","req_id":"r1","kind":"bash","summary":"装依赖","command":"npm install","diff":"-a\n+b"}`)
	if !ok {
		t.Fatal("approval_request line did not parse")
	}
	if ev.Type != "approval_request" || ev.ReqID != "r1" || ev.Kind != "bash" ||
		ev.Summary != "装依赖" || ev.Command != "npm install" || ev.Diff != "-a\n+b" {
		t.Errorf("decoded fields = %+v", ev)
	}
}

// countPending reads approval/list and counts the pending rows.
func countPending(t *testing.T, srv *httptest.Server, tok, params string) int {
	t.Helper()
	env, _ := callApproval(t, srv, tok, "approval/list", params)
	var res struct {
		Approvals []approvalView `json:"approvals"`
	}
	if err := json.Unmarshal(env["result"], &res); err != nil {
		t.Fatalf("decode approval/list result %s: %v", env["result"], err)
	}
	return len(res.Approvals)
}
