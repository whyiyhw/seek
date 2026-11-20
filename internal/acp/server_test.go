package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// fakeBackend is a scriptable ACP Backend.
type fakeBackend struct {
	prompt    func(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error)
	menu      []ConfigOption                                               // returned by NewSession (nil default)
	setConfig func(p SetConfigOptionParams) (SetConfigOptionResult, error) // nil default = canned success
	load      func(p LoadSessionParams, send func(SessionUpdate)) (LoadSessionResult, error)
}

func (f fakeBackend) Initialize(p InitializeParams) InitializeResult {
	return InitializeResult{ProtocolVersion: p.ProtocolVersion, AgentCapabilities: AgentCapabilities{LoadSession: true}}
}
func (f fakeBackend) NewSession(p NewSessionParams) (NewSessionResult, error) {
	return NewSessionResult{SessionID: "sess-1", ConfigOptions: f.menu}, nil
}
func (f fakeBackend) LoadSession(p LoadSessionParams, send func(SessionUpdate)) (LoadSessionResult, error) {
	if f.load != nil {
		return f.load(p, send)
	}
	return LoadSessionResult{ConfigOptions: f.menu}, nil
}
func (f fakeBackend) Prompt(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error) {
	return f.prompt(ctx, p, send)
}
func (f fakeBackend) SetConfigOption(p SetConfigOptionParams) (SetConfigOptionResult, error) {
	if f.setConfig != nil {
		return f.setConfig(p)
	}
	return SetConfigOptionResult{ConfigOptions: f.menu}, nil
}

// startServer wires a Server to an in-process client over two pipes and
// returns an encoder (client→server) + decoder (server→client).
func startServer(t *testing.T, b Backend) (*json.Encoder, *json.Decoder) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	srv := NewServer(c2sR, s2cW, b)
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() { cancel(); c2sW.Close(); s2cW.Close() })
	return json.NewEncoder(c2sW), json.NewDecoder(s2cR)
}

func send(t *testing.T, enc *json.Encoder, id int, method string, params any) {
	t.Helper()
	pb, _ := json.Marshal(params)
	raw := json.RawMessage(pb)
	idRaw := json.RawMessage([]byte(itoa(id)))
	m := map[string]any{"jsonrpc": "2.0", "method": method, "params": raw}
	if id >= 0 {
		m["id"] = &idRaw
	}
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// readMsg decodes one server→client message into a generic map.
func readMsg(t *testing.T, dec *json.Decoder) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func TestServer_InitializeHandshake(t *testing.T) {
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 1, "initialize", InitializeParams{ProtocolVersion: 1})
	msg := readMsg(t, dec)
	var res InitializeResult
	if err := json.Unmarshal(msg["result"], &res); err != nil {
		t.Fatalf("no result: %v (%v)", err, msg)
	}
	if res.ProtocolVersion != 1 || !res.AgentCapabilities.LoadSession {
		t.Fatalf("bad initialize result: %+v", res)
	}
}

func TestServer_NewSession(t *testing.T) {
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 2, "session/new", NewSessionParams{Cwd: "/x"})
	var res NewSessionResult
	json.Unmarshal(readMsg(t, dec)["result"], &res)
	if res.SessionID != "sess-1" {
		t.Fatalf("sessionId = %q", res.SessionID)
	}
}

func TestServer_Prompt_StreamsUpdatesThenResult(t *testing.T) {
	b := fakeBackend{prompt: func(ctx context.Context, p PromptParams, sendU func(SessionUpdate)) (PromptResult, error) {
		sendU(SessionUpdate{SessionID: p.SessionID, Update: map[string]any{"kind": "chunk", "text": p.PromptText()}})
		sendU(SessionUpdate{SessionID: p.SessionID, Update: map[string]any{"kind": "chunk", "text": "more"}})
		return PromptResult{StopReason: "end_turn"}, nil
	}}
	enc, dec := startServer(t, b)
	send(t, enc, 3, "session/prompt", PromptParams{SessionID: "s", Prompt: []ContentBlock{{Type: "text", Text: "hi there"}}})

	// Expect: two session/update notifications, then the response (id=3).
	updates := 0
	for {
		msg := readMsg(t, dec)
		if _, isResult := msg["result"]; isResult {
			var r PromptResult
			json.Unmarshal(msg["result"], &r)
			if r.StopReason != "end_turn" {
				t.Fatalf("stop reason = %q", r.StopReason)
			}
			break
		}
		var method string
		json.Unmarshal(msg["method"], &method)
		if method != "session/update" {
			t.Fatalf("unexpected message: %v", msg)
		}
		updates++
		if updates > 5 {
			t.Fatal("too many updates, never saw result")
		}
	}
	if updates != 2 {
		t.Fatalf("got %d updates, want 2", updates)
	}
}

func TestServer_PromptText(t *testing.T) {
	p := PromptParams{Prompt: []ContentBlock{{Type: "text", Text: "a"}, {Type: "image"}, {Type: "text", Text: "b"}}}
	if got := p.PromptText(); got != "a\nb" {
		t.Fatalf("PromptText = %q, want a\\nb", got)
	}
}

func TestServer_Cancel(t *testing.T) {
	started := make(chan struct{})
	b := fakeBackend{prompt: func(ctx context.Context, p PromptParams, sendU func(SessionUpdate)) (PromptResult, error) {
		close(started)
		<-ctx.Done() // block until cancelled
		return PromptResult{}, ctx.Err()
	}}
	enc, dec := startServer(t, b)
	send(t, enc, 4, "session/prompt", PromptParams{SessionID: "s"})
	<-started
	send(t, enc, -1, "session/cancel", map[string]string{"sessionId": "s"}) // notification (no id)

	var res PromptResult
	json.Unmarshal(readMsg(t, dec)["result"], &res)
	if res.StopReason != "cancelled" {
		t.Fatalf("stop reason = %q, want cancelled", res.StopReason)
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 5, "frobnicate", map[string]any{})
	msg := readMsg(t, dec)
	if _, hasErr := msg["error"]; !hasErr {
		t.Fatalf("unknown method should return an error, got %v", msg)
	}
}

func TestServer_CleanShutdownOnEOF(t *testing.T) {
	c2sR, c2sW := io.Pipe()
	s2cR, _ := io.Pipe()
	srv := NewServer(c2sR, io.Discard, fakeBackend{})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background()) }()
	c2sW.Close() // client disconnects → EOF
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EOF should be clean shutdown, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return on EOF")
	}
	s2cR.Close()
}

func TestPromptParams_ImageBlocks(t *testing.T) {
	p := PromptParams{Prompt: []ContentBlock{
		{Type: "text", Text: "look at this"},
		{Type: "image", Data: "Zm9v", MimeType: "image/png"},
		{Type: "image", Data: "", MimeType: "image/png"}, // no data → skipped
		{Type: "image", Data: "YmFy", MimeType: "image/jpeg"},
	}}
	imgs := p.ImageBlocks()
	if len(imgs) != 2 {
		t.Fatalf("expected 2 image blocks (non-empty data), got %d", len(imgs))
	}
	if imgs[0].MimeType != "image/png" || imgs[1].MimeType != "image/jpeg" {
		t.Fatalf("image blocks out of order/wrong: %+v", imgs)
	}
	// PromptText must still ignore image blocks (text-only).
	if got := p.PromptText(); got != "look at this" {
		t.Fatalf("PromptText should only return text blocks, got %q", got)
	}
}

func testMenu() []ConfigOption {
	return []ConfigOption{{
		ID:           "model",
		Name:         "Model",
		Category:     "model",
		Type:         "select",
		CurrentValue: "deepseek:deepseek-flash",
		Options: []ConfigSelectOption{
			{Value: "deepseek:deepseek-flash", Name: "deepseek-flash (V4.1-Flash)"},
			{Value: "glm:glm-5.3", Name: "glm-5.3 (GLM)"},
		},
	}}
}

func TestServer_NewSessionCarriesConfigOptions(t *testing.T) {
	enc, dec := startServer(t, fakeBackend{menu: testMenu()})
	send(t, enc, 6, "session/new", NewSessionParams{Cwd: "/x"})
	msg := readMsg(t, dec)
	var res NewSessionResult
	if err := json.Unmarshal(msg["result"], &res); err != nil {
		t.Fatalf("no result: %v (%v)", err, msg)
	}
	if len(res.ConfigOptions) != 1 {
		t.Fatalf("configOptions = %+v, want 1 entry", res.ConfigOptions)
	}
	opt := res.ConfigOptions[0]
	if opt.ID != "model" || opt.Type != "select" || opt.CurrentValue != "deepseek:deepseek-flash" || len(opt.Options) != 2 {
		t.Fatalf("bad model option: %+v", opt)
	}
	// The wire object must flatten the select variant (currentValue/
	// options inlined next to id/name/type), not nest it — that shape is
	// what the client SDK schema (and paseo) deserialises.
	var raw struct {
		ConfigOptions []map[string]json.RawMessage `json:"configOptions"`
	}
	if err := json.Unmarshal(msg["result"], &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.ConfigOptions) != 1 {
		t.Fatalf("wire configOptions = %s", msg["result"])
	}
	for _, key := range []string{"id", "name", "type", "currentValue", "options"} {
		if _, ok := raw.ConfigOptions[0][key]; !ok {
			t.Fatalf("wire configOption missing %q: %s", key, msg["result"])
		}
	}
}

func TestServer_SetConfigOption(t *testing.T) {
	var got SetConfigOptionParams
	menu := testMenu()
	b := fakeBackend{menu: menu, setConfig: func(p SetConfigOptionParams) (SetConfigOptionResult, error) {
		got = p
		menu[0].CurrentValue = p.Value // the real backend updates in place
		return SetConfigOptionResult{ConfigOptions: menu}, nil
	}}
	enc, dec := startServer(t, b)
	send(t, enc, 7, "session/set_config_option", SetConfigOptionParams{
		SessionID: "sess-1", ConfigID: "model", Value: "glm:glm-5.3",
	})
	msg := readMsg(t, dec)
	var res SetConfigOptionResult
	if err := json.Unmarshal(msg["result"], &res); err != nil {
		t.Fatalf("no result: %v (%v)", err, msg)
	}
	if got.ConfigID != "model" || got.Value != "glm:glm-5.3" {
		t.Fatalf("backend saw %+v", got)
	}
	if len(res.ConfigOptions) != 1 || res.ConfigOptions[0].CurrentValue != "glm:glm-5.3" {
		t.Fatalf("response must echo the FULL option set with the new current: %+v", res)
	}
}

func TestServer_SetConfigOption_BackendError(t *testing.T) {
	b := fakeBackend{setConfig: func(p SetConfigOptionParams) (SetConfigOptionResult, error) {
		return SetConfigOptionResult{}, io.ErrClosedPipe // any backend failure → -32000
	}}
	enc, dec := startServer(t, b)
	send(t, enc, 8, "session/set_config_option", SetConfigOptionParams{SessionID: "s", ConfigID: "nope", Value: "x"})
	msg := readMsg(t, dec)
	var e rpcError
	if err := json.Unmarshal(msg["error"], &e); err != nil {
		t.Fatalf("expected error, got %v", msg)
	}
	if e.Code != -32000 {
		t.Fatalf("code = %d, want -32000", e.Code)
	}
}

func TestServer_SetConfigOption_BooleanPayloadRejected(t *testing.T) {
	// seek only serves select-type options; the boolean wire variant
	// (type:"boolean", value:true) must fail decoding loudly (-32602)
	// rather than half-apply.
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 9, "session/set_config_option", map[string]any{
		"sessionId": "sess-1", "configId": "model", "type": "boolean", "value": true,
	})
	msg := readMsg(t, dec)
	var e rpcError
	if err := json.Unmarshal(msg["error"], &e); err != nil {
		t.Fatalf("expected error, got %v", msg)
	}
	if e.Code != -32602 {
		t.Fatalf("code = %d, want -32602", e.Code)
	}
}

func TestServer_SetConfigOption_RejectedWhilePromptInFlight(t *testing.T) {
	// A model switch during a turn would race the turn's lane reads;
	// dispatch must reject it while the prompt's cancel is registered.
	started := make(chan struct{})
	b := fakeBackend{
		menu: testMenu(),
		prompt: func(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error) {
			close(started)
			<-ctx.Done()
			return PromptResult{}, ctx.Err()
		},
		setConfig: func(p SetConfigOptionParams) (SetConfigOptionResult, error) {
			t.Error("backend.SetConfigOption must not be called while a prompt is in flight")
			return SetConfigOptionResult{}, nil
		},
	}
	enc, dec := startServer(t, b)
	send(t, enc, 10, "session/prompt", PromptParams{SessionID: "sess-1"})
	<-started
	send(t, enc, 11, "session/set_config_option", SetConfigOptionParams{SessionID: "sess-1", ConfigID: "model", Value: "glm:glm-5.3"})

	// The switch rejection lands while the turn is still running...
	msg := readMsg(t, dec)
	var e rpcError
	if err := json.Unmarshal(msg["error"], &e); err != nil {
		t.Fatalf("expected error, got %v", msg)
	}
	if e.Code != -32000 {
		t.Fatalf("code = %d, want -32000 (in-flight rejection)", e.Code)
	}
	// ...then the cancelled turn's result.
	send(t, enc, -1, "session/cancel", map[string]string{"sessionId": "sess-1"})
	var res PromptResult
	json.Unmarshal(readMsg(t, dec)["result"], &res)
	if res.StopReason != "cancelled" {
		t.Fatalf("stop reason = %q, want cancelled", res.StopReason)
	}
}

// TestServer_LoadSession_ReplaysThenReplies pins the wire ordering the ACP
// schema asks for: session/load streams the whole conversation back as
// session/update notifications and only then answers the request. Clients
// (paseo) assert this order — they register the session id before the
// replay arrives, so a result-first implementation loses the history.
func TestServer_LoadSession_ReplaysThenReplies(t *testing.T) {
	var got LoadSessionParams
	b := fakeBackend{load: func(p LoadSessionParams, send func(SessionUpdate)) (LoadSessionResult, error) {
		got = p
		send(SessionUpdate{SessionID: p.SessionID, Update: map[string]any{"sessionUpdate": "user_message_chunk"}})
		send(SessionUpdate{SessionID: p.SessionID, Update: map[string]any{"sessionUpdate": "agent_message_chunk"}})
		return LoadSessionResult{ConfigOptions: testMenu()}, nil
	}}
	enc, dec := startServer(t, b)
	send(t, enc, 20, "session/load", LoadSessionParams{
		SessionID: "20260101-000000-abcdef0123456789",
		Cwd:       "/proj",
	})

	var updates int
	for {
		msg := readMsg(t, dec)
		if _, isResult := msg["result"]; isResult {
			var res LoadSessionResult
			if err := json.Unmarshal(msg["result"], &res); err != nil {
				t.Fatalf("result: %v", err)
			}
			if len(res.ConfigOptions) != 1 || res.ConfigOptions[0].ID != "model" {
				t.Fatalf("load result must carry the resumed session's menu: %+v", res)
			}
			break
		}
		var method string
		_ = json.Unmarshal(msg["method"], &method)
		if method != "session/update" {
			t.Fatalf("unexpected message before the result: %v", msg)
		}
		updates++
	}
	if updates != 2 {
		t.Fatalf("replay updates = %d, want 2 (all of them before the result)", updates)
	}
	if got.SessionID != "20260101-000000-abcdef0123456789" || got.Cwd != "/proj" {
		t.Fatalf("backend saw %+v", got)
	}
}

// TestServer_LoadSession_ErrorIsNotMethodNotFound: a failed load is a
// server error (-32000), NOT -32601. The distinction is what lets a client
// tell "this agent can't load sessions" (fall back to new) from "this
// session id is bad" (surface the error).
func TestServer_LoadSession_ErrorIsNotMethodNotFound(t *testing.T) {
	b := fakeBackend{load: func(LoadSessionParams, func(SessionUpdate)) (LoadSessionResult, error) {
		return LoadSessionResult{}, io.ErrUnexpectedEOF
	}}
	enc, dec := startServer(t, b)
	send(t, enc, 21, "session/load", LoadSessionParams{SessionID: "nope"})
	var e rpcError
	if err := json.Unmarshal(readMsg(t, dec)["error"], &e); err != nil {
		t.Fatalf("expected an error: %v", err)
	}
	if e.Code != -32000 {
		t.Fatalf("code = %d, want -32000 (handled method, failed load)", e.Code)
	}
}

// TestServer_LoadSession_BadParams: a malformed payload fails decoding
// loudly (-32602) instead of loading something arbitrary.
func TestServer_LoadSession_BadParams(t *testing.T) {
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 22, "session/load", map[string]any{"sessionId": 42})
	var e rpcError
	if err := json.Unmarshal(readMsg(t, dec)["error"], &e); err != nil {
		t.Fatalf("expected an error: %v", err)
	}
	if e.Code != -32602 {
		t.Fatalf("code = %d, want -32602", e.Code)
	}
}

// TestServer_LoadSessionReplayIsSilentWhenEmpty: a session with no
// messages still answers (and emits nothing) rather than hanging or
// sending a stray notification.
func TestServer_LoadSessionReplayIsSilentWhenEmpty(t *testing.T) {
	enc, dec := startServer(t, fakeBackend{})
	send(t, enc, 23, "session/load", LoadSessionParams{SessionID: "empty"})
	msg := readMsg(t, dec)
	if _, isResult := msg["result"]; !isResult {
		t.Fatalf("first message must be the result for an empty replay, got %v", msg)
	}
}

// TestServer_LoadSession_RejectedWhilePromptInFlight: a load swaps the
// agent's whole history (Reset), which must not happen under a running
// turn — same in-flight rule and race argument as set_config_option.
func TestServer_LoadSession_RejectedWhilePromptInFlight(t *testing.T) {
	started := make(chan struct{})
	b := fakeBackend{
		prompt: func(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error) {
			close(started)
			<-ctx.Done()
			return PromptResult{}, ctx.Err()
		},
		load: func(LoadSessionParams, func(SessionUpdate)) (LoadSessionResult, error) {
			t.Error("backend.LoadSession must not run while a prompt is in flight")
			return LoadSessionResult{}, nil
		},
	}
	enc, dec := startServer(t, b)
	send(t, enc, 30, "session/prompt", PromptParams{SessionID: "sess-1"})
	<-started
	send(t, enc, 31, "session/load", LoadSessionParams{SessionID: "sess-1"})
	var e rpcError
	if err := json.Unmarshal(readMsg(t, dec)["error"], &e); err != nil {
		t.Fatalf("expected a rejection: %v", err)
	}
	if e.Code != -32000 {
		t.Fatalf("code = %d, want -32000", e.Code)
	}
	send(t, enc, -1, "session/cancel", map[string]string{"sessionId": "sess-1"})
	var res PromptResult
	json.Unmarshal(readMsg(t, dec)["result"], &res)
	if res.StopReason != "cancelled" {
		t.Fatalf("stop reason = %q", res.StopReason)
	}
}
