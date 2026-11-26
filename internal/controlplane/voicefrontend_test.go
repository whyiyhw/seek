package controlplane

// Voice frontend v2 tests (card c_f6ayyg acceptance): the push-to-talk
// inbound path driven END-TO-END through the real web handler stack
// (auth → validate → queue) with a stub ASR — upload → Inbound, the
// transcript echo, the reply delivery, and every failure mode of the
// upload surface. Real-API ASR and real-browser recording are the
// separately-run smoke (the card parks both with the main session).

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// stubVoiceASR is the deterministic ASR stand-in: records every clip
// it got, returns canned output, optionally parks until unblocked or
// the ctx dies (the cancellation-path fixture).
type stubVoiceASR struct {
	mu    sync.Mutex
	clips [][]byte
	text  string
	err   error
	block chan struct{}
}

func (s *stubVoiceASR) Transcribe(ctx context.Context, audio []byte) (string, int64, error) {
	s.mu.Lock()
	s.clips = append(s.clips, append([]byte(nil), audio...))
	s.mu.Unlock()
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}
	if s.err != nil {
		return "", 0, s.err
	}
	return s.text, int64(len(audio)), nil
}

func (s *stubVoiceASR) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clips)
}

func (s *stubVoiceASR) lastClip() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clips) == 0 {
		return nil
	}
	return s.clips[len(s.clips)-1]
}

// testWAV builds a minimal 16-bit PCM mono 16 kHz WAV of the given
// length — byte content is silence, only the container shape matters
// to the plane (the stub never decodes; the real client's checks are
// its own tests).
func testWAV(seconds float64) []byte {
	const rate = uint32(16000)
	dataLen := uint32(seconds * float64(rate) * 2)
	b := make([]byte, 44+dataLen)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], 36+dataLen)
	copy(b[8:], "WAVE")
	copy(b[12:], "fmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], dataLen)
	return b
}

// newTestVoice assembles the full v2 stack with the stub ASR: web
// frontend + attached voice frontend over httptest, voice.Run live on
// a cancellable ctx. Returns the pieces the tests poke at. The billing
// ledger is redirected into the test's temp tree — the frontend's
// default is <serve>/voice-usage.jsonl (paths.ServeDir), which a test
// must never touch (voiceusage_test.go asserts the rows).
func newTestVoice(t *testing.T, asr VoiceASR) (*WebFrontend, *VoiceFrontend, chan Inbound, string) {
	t.Helper()
	web, _, tok := newTestWeb(t)
	v := NewVoiceAt(asr, filepath.Join(t.TempDir(), "voice-usage.jsonl"))
	v.Attach(web)

	in := make(chan Inbound, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.Run(ctx, in) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("voice Run returned %v on shutdown, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("voice Run did not return within 5s of ctx cancel")
		}
	})

	srv := httptest.NewServer(web.handler())
	t.Cleanup(srv.Close)
	return web, v, in, srv.URL + "/voice?token=" + tok
}

// postVoice uploads one clip through the real HTTP surface.
func postVoice(t *testing.T, url, device, event, conv string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if device != "" {
		req.Header.Set(voiceDeviceHeader, device)
	}
	if event != "" {
		req.Header.Set(voiceEventHeader, event)
	}
	if conv != "" {
		req.Header.Set(convHeader, conv)
	}
	req.Header.Set("Content-Type", "audio/wav")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestVoice_FullPath is the card's acceptance: stub ASR, the whole
// inbound path — authenticated upload → validated clip → transcript →
// transcript echo → Inbound with the pinned v2 semantics — plus the
// reply riding back over the web conv's SSE.
func TestVoice_FullPath(t *testing.T) {
	asr := &stubVoiceASR{text: "今天天气怎么样"}
	web, v, in, url := newTestVoice(t, asr)

	webConv := NewConvID("web", "c1")
	ch, unsub := web.subscribe(webConv)
	defer unsub()

	wav := testWAV(0.5)
	resp := postVoice(t, url, "dev-1", "evt-1", "c1", wav)
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /voice = %d (%s), want 202", resp.StatusCode, body)
	}
	var ack struct {
		OK    bool   `json:"ok"`
		Event string `json:"event"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil || !ack.OK || ack.Event != "evt-1" {
		t.Fatalf("202 body = %+v (%v), want ok+event echo", ack, err)
	}

	select {
	case m := <-in:
		if m.Conv != NewConvID("voice", "dev-1") {
			t.Errorf("Inbound.Conv = %q, want voice:dev-1", m.Conv)
		}
		if m.Text != "今天天气怎么样" {
			t.Errorf("Inbound.Text = %q, want the stub transcript", m.Text)
		}
		if m.EventID != "evt-1" {
			t.Errorf("Inbound.EventID = %q, want the recording-session id", m.EventID)
		}
		if !m.DirectAt {
			t.Errorf("Inbound.DirectAt = false, want true (push-to-talk is always direct)")
		}
		if m.Sender != "dev-1" {
			t.Errorf("Inbound.Sender = %q, want the device id", m.Sender)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Inbound arrived within 5s")
	}

	// The exact clip bytes flow to the ASR seam unmutated.
	if got := asr.lastClip(); string(got) != string(wav) {
		t.Errorf("ASR got %d bytes, want the uploaded %d verbatim", len(got), len(wav))
	}

	// Transcript echo on the page's web conv: a NAMED "voice" frame.
	select {
	case fr := <-ch:
		if fr.name != "voice" {
			t.Fatalf("echo frame name = %q, want voice", fr.name)
		}
		var ev voiceEchoEvent
		if err := json.Unmarshal([]byte(fr.data), &ev); err != nil || ev.Text != "今天天气怎么样" {
			t.Errorf("echo payload = %q (%v), want the transcript", fr.data, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no transcript echo frame within 5s")
	}

	// The reply path: Send to the VOICE conv lands on the page's web
	// conv as an MVP-shape default frame.
	if err := v.Send(context.Background(), NewConvID("voice", "dev-1"), Reply{Text: "晴，18 度"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case fr := <-ch:
		if fr.name != "" {
			t.Errorf("reply frame name = %q, want default (unnamed)", fr.name)
		}
		var m map[string]string
		if err := json.Unmarshal([]byte(fr.data), &m); err != nil {
			t.Fatalf("reply frame %q: %v", fr.data, err)
		}
		if m["conv"] != string(webConv) || m["text"] != "晴，18 度" {
			t.Errorf("reply frame = %q, want conv %q + reply text", fr.data, webConv)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no reply frame within 5s")
	}
}

// TestVoice_ASRFailure: a failing transcription never reaches the
// plane; the page gets the voice-error face instead.
func TestVoice_ASRFailure(t *testing.T) {
	asr := &stubVoiceASR{err: errors.New("boom (429)")}
	web, _, in, url := newTestVoice(t, asr)

	ch, unsub := web.subscribe(NewConvID("web", "c1"))
	defer unsub()

	if resp := postVoice(t, url, "dev-1", "evt-1", "c1", testWAV(0.1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	select {
	case fr := <-ch:
		if fr.name != "voice-error" {
			t.Fatalf("frame name = %q, want voice-error", fr.name)
		}
		var ev voiceErrorEvent
		if err := json.Unmarshal([]byte(fr.data), &ev); err != nil || !strings.Contains(ev.Error, "boom") {
			t.Errorf("error payload = %q (%v), want the ASR failure", fr.data, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no voice-error frame within 5s")
	}
	select {
	case m := <-in:
		t.Errorf("failed transcription must not become an Inbound, got %+v", m)
	default:
	}
}

// TestVoice_EmptyTranscript: silence is a user-facing dead end, not a
// router warn line — the same voice-error face, no Inbound.
func TestVoice_EmptyTranscript(t *testing.T) {
	asr := &stubVoiceASR{text: "   "}
	web, _, in, url := newTestVoice(t, asr)

	ch, unsub := web.subscribe(NewConvID("web", "c1"))
	defer unsub()

	if resp := postVoice(t, url, "dev-1", "evt-1", "c1", testWAV(0.1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	select {
	case fr := <-ch:
		if fr.name != "voice-error" {
			t.Fatalf("frame name = %q, want voice-error", fr.name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no voice-error frame within 5s")
	}
	select {
	case m := <-in:
		t.Errorf("empty transcript must not become an Inbound, got %+v", m)
	default:
	}
}

// TestVoice_SendUnknownDevice: a reply for a device this process never
// heard from is a best-effort drop (nil), never an error — the same
// posture as web Send with no subscribers.
func TestVoice_SendUnknownDevice(t *testing.T) {
	_, v, _, _ := newTestVoice(t, &stubVoiceASR{text: "x"})
	if err := v.Send(context.Background(), NewConvID("voice", "never-seen"), Reply{Text: "hi"}); err != nil {
		t.Errorf("Send to unknown device = %v, want nil drop", err)
	}
}

// TestVoice_UploadValidation: the endpoint's own failure modes.
func TestVoice_UploadValidation(t *testing.T) {
	_, _, _, url := newTestVoice(t, &stubVoiceASR{text: "x"})

	cases := []struct {
		name   string
		device string
		event  string
		body   []byte
		want   int
	}{
		{"missing device", "", "e", testWAV(0.1), http.StatusBadRequest},
		{"bad device charset", "has space", "e", testWAV(0.1), http.StatusBadRequest},
		{"missing event", "d", "", testWAV(0.1), http.StatusBadRequest},
		{"empty body", "d", "e", nil, http.StatusBadRequest},
		{"garbage bytes", "d", "e", []byte("definitely not audio"), http.StatusBadRequest},
		{"ogg accepted", "d", "e", append([]byte("OggS"), make([]byte, 64)...), http.StatusAccepted},
	}
	for _, tc := range cases {
		if resp := postVoice(t, url, tc.device, tc.event, "c1", tc.body); resp.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}

	// Auth: wrong token → 401 (the no-token case is its own test).
	base, _, _ := strings.Cut(url, "token=")
	if resp := postVoice(t, base+"token=wrong", "d", "e", "c1", testWAV(0.1)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token = %d, want 401", resp.StatusCode)
	}

	// Method: GET on the endpoint → 405 (token still present in URL).
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /voice = %d, want 405", resp.StatusCode)
	}
}

// TestVoice_NoTokenUnauthorized: the write-surface token gate.
func TestVoice_NoTokenUnauthorized(t *testing.T) {
	_, _, _, url := newTestVoice(t, &stubVoiceASR{text: "x"})
	if resp := postVoice(t, strings.SplitN(url, "?token=", 2)[0], "d", "e", "c1", testWAV(0.1)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", resp.StatusCode)
	}
}

// TestVoice_CookieWriteGate: a cookie alone must not upload — the same
// CSRF bar as POST /msg; with the same-origin Origin header it passes
// (and only THAT upload transcribes).
func TestVoice_CookieWriteGate(t *testing.T) {
	asr := &stubVoiceASR{text: "好"}
	_, _, in, url := newTestVoice(t, asr)
	base := strings.SplitN(url, "?token=", 2)[0]
	tok := strings.SplitN(url, "token=", 2)[1]
	origin := strings.SplitN(strings.TrimPrefix(base, "http://"), "/", 2)[0]

	post := func(origin, event string) int {
		req, _ := http.NewRequest(http.MethodPost, base, bytes.NewReader(testWAV(0.1)))
		req.AddCookie(&http.Cookie{Name: webTokenCookie, Value: tok})
		if origin != "" {
			req.Header.Set("Origin", "http://"+origin)
		}
		req.Header.Set(voiceDeviceHeader, "dev-c")
		req.Header.Set(voiceEventHeader, event)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// The bare-Host Origin a browser would send for this httptest host.
	if code := post(origin, "evt-ok"); code != http.StatusAccepted {
		t.Fatalf("cookie+same-origin upload = %d, want 202", code)
	}
	if code := post("evil.example", "evt-x1"); code != http.StatusForbidden {
		t.Errorf("cookie+cross-origin upload = %d, want 403", code)
	}
	if code := post("", "evt-x2"); code != http.StatusForbidden {
		t.Errorf("cookie without Origin = %d, want 403", code)
	}
	// Exactly the same-origin upload may transcribe — the 403'd ones
	// never reached the queue.
	select {
	case m := <-in:
		if m.EventID != "evt-ok" {
			t.Errorf("unexpected Inbound %+v — a 403'd upload must not transcribe", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("same-origin upload never transcribed")
	}
	select {
	case m := <-in:
		t.Errorf("extra Inbound %+v — exactly one upload may pass the gate", m)
	default:
	}
}

// TestVoice_BodyTooLarge: one byte over the API cap is a 413, not a
// memory sink.
func TestVoice_BodyTooLarge(t *testing.T) {
	_, _, _, url := newTestVoice(t, &stubVoiceASR{text: "x"})
	big := append(testWAV(0.1), make([]byte, deepseek.ASRMaxBytes)...)
	if resp := postVoice(t, url, "d", "e", "c1", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize upload = %d, want 413", resp.StatusCode)
	}
}

// TestVoice_QueueFull: with the Run loop not draining (here: not
// started at all), a full upload queue answers 503 instead of hanging
// the request.
func TestVoice_QueueFull(t *testing.T) {
	web, _, tok := newTestWeb(t)
	v := NewVoice(&stubVoiceASR{text: "x"})
	v.Attach(web)
	srv := httptest.NewServer(web.handler())
	defer srv.Close()
	url := srv.URL + "/voice?token=" + tok

	for i := 0; i < voiceQueueDepth; i++ {
		if resp := postVoice(t, url, "d", fmt.Sprintf("e%d", i), "c1", testWAV(0.1)); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("upload %d = %d, want 202 (queue not full yet)", i, resp.StatusCode)
		}
	}
	if resp := postVoice(t, url, "d", "overflow", "c1", testWAV(0.1)); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("upload past queue depth = %d, want 503", resp.StatusCode)
	}
}

// TestVoice_RunNeedsAttachAndASR: both config mistakes fail loud at
// Run (Serve then reports the frontend down), not as a dead endpoint.
func TestVoice_RunNeedsAttachAndASR(t *testing.T) {
	v := NewVoice(&stubVoiceASR{text: "x"})
	if err := v.Run(context.Background(), make(chan<- Inbound, 1)); err == nil || !strings.Contains(err.Error(), "web frontend") {
		t.Errorf("Run without Attach = %v, want attach error", err)
	}
	_, v2, _, _ := newTestVoice(t, &stubVoiceASR{text: "x"}) // cleanup owns its ctx
	bare := NewVoice(nil)
	bare.Attach(v2.web)
	if err := bare.Run(context.Background(), make(chan<- Inbound, 1)); err == nil || !strings.Contains(err.Error(), "ASR") {
		t.Errorf("Run without ASR = %v, want ASR error", err)
	}
}

// TestVoice_RunReturnsOnCancel: a cancellation that lands mid-ASR (the
// stub parks until ctx dies) unwinds the loop cleanly — no error, no
// voice-error frame into a shutting-down plane.
func TestVoice_RunReturnsOnCancel(t *testing.T) {
	web, _, tok := newTestWeb(t)
	asr := &stubVoiceASR{text: "x", block: make(chan struct{})}
	v := NewVoiceAt(asr, filepath.Join(t.TempDir(), "voice-usage.jsonl"))
	v.Attach(web)
	srv := httptest.NewServer(web.handler())
	defer srv.Close()

	in := make(chan Inbound, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.Run(ctx, in) }()

	if resp := postVoice(t, srv.URL+"/voice?token="+tok, "d", "e", "c1", testWAV(0.1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for asr.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if asr.calls() == 0 {
		t.Fatal("stub ASR never saw the clip")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run on cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of mid-ASR cancel")
	}
	select {
	case m := <-in:
		t.Errorf("cancelled transcription must not become an Inbound, got %+v", m)
	default:
	}
}

// TestVoice_OggDurationNotPrechecked: Ogg clips skip the WAV duration
// pre-check by design (the server owns it) — the plane's queue accepts
// them; a smoke-level assertion that only the magic gate applies.
func TestVoice_OggDurationNotPrechecked(t *testing.T) {
	_, _, in, url := newTestVoice(t, &stubVoiceASR{text: "ogg 内容"})
	ogg := append([]byte("OggS"), make([]byte, 128)...)
	if resp := postVoice(t, url, "dev-o", "evt-o", "c1", ogg); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ogg upload = %d, want 202", resp.StatusCode)
	}
	select {
	case m := <-in:
		if m.Conv != NewConvID("voice", "dev-o") {
			t.Errorf("Inbound.Conv = %q, want voice:dev-o", m.Conv)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Inbound within 5s")
	}
}
