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
	prompt func(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error)
}

func (f fakeBackend) Initialize(p InitializeParams) InitializeResult {
	return InitializeResult{ProtocolVersion: p.ProtocolVersion, AgentCapabilities: AgentCapabilities{LoadSession: true}}
}
func (f fakeBackend) NewSession(p NewSessionParams) (NewSessionResult, error) {
	return NewSessionResult{SessionID: "sess-1"}, nil
}
func (f fakeBackend) Prompt(ctx context.Context, p PromptParams, send func(SessionUpdate)) (PromptResult, error) {
	return f.prompt(ctx, p, send)
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
