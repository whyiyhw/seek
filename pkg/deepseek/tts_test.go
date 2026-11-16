package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ttsServer mimics the TTS API at /v1/tools/tts and counts requests so
// retry-once behaviour is observable. Script semantics match
// searchServer / asrServer: each handler runs once in order, the last
// repeats.
func ttsServer(t *testing.T, script ...http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	i := -1
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		if r.URL.Path != endpointTTS {
			t.Errorf("path = %q, want %q", r.URL.Path, endpointTTS)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("Authorization = %q, want Bearer t", got)
		}
		h := script[len(script)-1]
		i++
		if i < len(script)-1 {
			h = script[i]
		}
		h(w, r)
	})), &n
}

// readTTSRequest decodes the JSON body the client sent. Malformed JSON
// is itself the signal that the client's wire format broke.
func readTTSRequest(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read tts request body: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode tts request body %q: %v", body, err)
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	return got
}

// wavAudio is a stand-in synthesized clip: RIFF/WAVE magic plus a
// payload, so the byte-equality assertions double as format checks.
var wavAudio = append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)

func writeTTSAudio(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(wavAudio)
}

func TestTTS_ReturnsAudioVerbatim(t *testing.T) {
	srv, n := ttsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got := readTTSRequest(t, r)
		if got["text"] != "任务完成。" {
			t.Errorf("text = %q, want 任务完成。", got["text"])
		}
		if got["voice_id"] != "stella" {
			t.Errorf("voice_id = %q, want stella", got["voice_id"])
		}
		if got["format"] != "wav" {
			t.Errorf("format = %q, want wav", got["format"])
		}
		writeTTSAudio(w)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	audio, err := c.TTS(context.Background(), &TTSRequest{
		Text: "任务完成。", VoiceID: TTSVoiceStella, Format: TTSAudioWAV,
	})
	if err != nil {
		t.Fatalf("TTS: %v", err)
	}
	if string(audio) != string(wavAudio) {
		t.Errorf("audio differs: got %d bytes, want %d verbatim", len(audio), len(wavAudio))
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestTTS_DefaultsOmittedFromBody(t *testing.T) {
	srv, _ := ttsServer(t, func(w http.ResponseWriter, r *http.Request) {
		got := readTTSRequest(t, r)
		if len(got) != 1 {
			t.Errorf("request fields = %v, want only text (server defaults apply)", got)
		}
		writeTTSAudio(w)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	if _, err := c.TTS(context.Background(), &TTSRequest{Text: "hi"}); err != nil {
		t.Fatalf("TTS(defaults): %v", err)
	}
}

// TestTTS_BillableChars pins the local billing counter against the
// published rule (every rune 1, Han 2) — the number both the ≤10,000
// pre-check and pricing.TTSCost consume. Full-width punctuation and
// kana are NOT Han and count 1; the server is the billing authority,
// this is the documented rule implemented literally.
func TestTTS_BillableChars(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"", 0},
		{"hello", 5},
		{"任务完成", 8},                 // 4 Han × 2
		{"任务完成。", 9},                // 。 is punctuation, not Han
		{"第1条：done", 10},             // 第条=2+2, "1"=1, ：=1 (full-width colon is not Han), "done"=4
		{"こんにちは", 5},                // kana count 1
		{"한국어", 3},                   // hangul counts 1
		{"😀 emoji", 7},                // 😀 is a rune like any other
		{strings.Repeat("汉", 5_000), 10_000}, // the cap boundary
	}
	for _, tc := range cases {
		if got := TTSBillableChars(tc.text); got != tc.want {
			t.Errorf("TTSBillableChars(%q) = %d, want %d", tc.text, got, tc.want)
		}
	}
}

func TestTTS_4xxFailsFastWithoutRetry(t *testing.T) {
	srv, n := ttsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "text length exceeds limit"}}`))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.TTS(context.Background(), &TTSRequest{Text: "hi"})
	if err == nil {
		t.Fatal("want error on 400")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Message != "text length exceeds limit" {
		t.Errorf("err = %v, want APIError 400 with parsed message", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1 — non-429 4xx must NOT retry", got)
	}
}

func TestTTS_RetriesOnceThenSucceeds(t *testing.T) {
	var first []byte
	srv, n := ttsServer(t,
		func(w http.ResponseWriter, r *http.Request) { // attempt 1: transient 429
			first, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "too many concurrent requests"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) { // attempt 2: OK
			second, _ := io.ReadAll(r.Body)
			if string(first) != string(second) {
				t.Errorf("retry sent a different body: %q vs %q", first, second)
			}
			writeTTSAudio(w)
		},
	)
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	audio, err := c.TTS(context.Background(), &TTSRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("TTS after one retry: %v", err)
	}
	if len(audio) == 0 {
		t.Errorf("audio = %d bytes, want the synthesized clip", len(audio))
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", got)
	}
}

func TestTTS_RetriesOnceThenGivesUp(t *testing.T) {
	srv, n := ttsServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"message": "boom"}}`))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.TTS(context.Background(), &TTSRequest{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("err = %v, want annotated 'after 2 attempts'", err)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (retry once, no more)", got)
	}
}

// TestTTS_MidBodyDisconnectRetried is the "TTS 流中途断开视为失败（重试
// 一次）" case from the PRD test plan: the server dies partway through
// the audio stream. A declared Content-Length longer than what gets
// written makes io.ReadAll fail with unexpected EOF — a transport
// error, so retryCall's second attempt must run and succeed.
func TestTTS_MidBodyDisconnectRetried(t *testing.T) {
	srv, n := ttsServer(t,
		func(w http.ResponseWriter, r *http.Request) { // attempt 1: stream cut mid-body
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(wavAudio[:len(wavAudio)/2])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler) // kill the connection without finishing the body
		},
		func(w http.ResponseWriter, r *http.Request) { // attempt 2: complete stream
			writeTTSAudio(w)
		},
	)
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	audio, err := c.TTS(context.Background(), &TTSRequest{Text: "hi"})
	if err != nil {
		t.Fatalf("TTS after mid-body retry: %v", err)
	}
	if string(audio) != string(wavAudio) {
		t.Errorf("audio differs after retry: got %d bytes, want the whole clip", len(audio))
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (mid-body cut retried once)", got)
	}
}

// TestTTS_ContextCanceled is the ctx.Done path (AGENTS.md "Testing":
// any ctx-aware function gets one). The handler parks until the client
// aborts so the cancellation lands mid-flight; the abort must surface
// as context.Canceled through the "deepseek: http:" wrap and retryCall
// must NOT retry a cancelled context (retry.go returns unannotated on
// ctx.Err() != nil). Same body-drain-before-park requirement as the
// ASR twin: an unread request body keeps the server from ever noticing
// the disconnect, and srv.Close() then hangs.
func TestTTS_ContextCanceled(t *testing.T) {
	srv, n := ttsServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	audio, err := c.TTS(ctx, &TTSRequest{Text: "hi"})
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled in chain", err)
	}
	if audio != nil {
		t.Errorf("audio = %d bytes, want nil", len(audio))
	}
	if got := atomic.LoadInt32(n); got > 1 {
		t.Errorf("requests = %d, want ≤1 — no retry after cancellation", got)
	}
}

// TestTTS_EmptyAudioBody: a 200 with no bytes at all is a broken
// response, not a valid silent clip — PCM silence would still be
// thousands of bytes, and zero bytes can never be played.
func TestTTS_EmptyAudioBody(t *testing.T) {
	srv, n := ttsServer(t, func(w http.ResponseWriter, r *http.Request) {})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.TTS(context.Background(), &TTSRequest{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "empty audio body") {
		t.Errorf("err = %v, want empty-audio error", err)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 — empty body is a decode-class failure, retried once then surfaced", got)
	}
}

func TestTTS_RequestValidation(t *testing.T) {
	srv, _ := ttsServer(t, func(w http.ResponseWriter, r *http.Request) { writeTTSAudio(w) })
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	cases := []struct {
		name string
		req  *TTSRequest
		want string
	}{
		{"nil request", nil, "nil tts request"},
		{"empty text", &TTSRequest{}, "text is empty"},
		{"over 10k billable (Han ×2)", &TTSRequest{Text: strings.Repeat("汉", 5_001)}, "billing characters"},
		{"unknown voice", &TTSRequest{Text: "hi", VoiceID: "alexa"}, "voice_id"},
		{"opus is websocket-only", &TTSRequest{Text: "hi", Format: "opus"}, "websocket-only"},
		{"unknown format", &TTSRequest{Text: "hi", Format: "mp3"}, "format"},
	}
	for _, tc := range cases {
		_, err := c.TTS(context.Background(), tc.req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}

	// Boundary: exactly 10,000 billing characters must PASS client-side
	// and reach the server (5,000 Han chars = 10,000 billed).
	if _, err := c.TTS(context.Background(), &TTSRequest{Text: strings.Repeat("汉", 5_000)}); err != nil {
		t.Errorf("exactly 10,000 billing chars rejected: %v", err)
	}
	// The 64 KiB body cap cannot be exceeded through valid fields
	// (shadowed by the char cap — see TTSMaxBodyBytes); the worst
	// escape-inflated body stays under it, so no local gate exists.
	if buf, _ := json.Marshal(&TTSRequest{Text: strings.Repeat("\x01", 10_000)}); len(buf) >= TTSMaxBodyBytes {
		t.Errorf("max-inflation body = %d bytes, expected < %d (shadow math broken?)", len(buf), TTSMaxBodyBytes)
	}
}

func TestTTS_MissingAPIKey(t *testing.T) {
	c := New()
	_, err := c.TTS(context.Background(), &TTSRequest{Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("err = %v, want missing api key", err)
	}
}
