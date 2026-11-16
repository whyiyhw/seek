package deepseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// asrServer mimics the ASR API at /v1/tools/asr and counts requests so
// retry-once behaviour is observable. Script semantics match
// searchServer: each handler runs once in order, the last repeats.
func asrServer(t *testing.T, script ...http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	i := -1
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		if r.URL.Path != endpointASR {
			t.Errorf("path = %q, want %q", r.URL.Path, endpointASR)
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

// readASRUpload parses the multipart body and returns the sole file
// part's filename and content. r.FormFile failing is itself the signal
// that the client's multipart/boundary was malformed.
func readASRUpload(t *testing.T, r *http.Request) (string, []byte) {
	t.Helper()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		t.Errorf("multipart field \"file\": %v", err)
		return "", nil
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Errorf("read uploaded audio: %v", err)
		return hdr.Filename, nil
	}
	return hdr.Filename, b
}

func writeASRJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"request_id": "asr-req-1", "text": "上海今天天气怎么样？", "duration_ms": 3200}`))
}

// wavBytes builds a minimal canonical mono 16-bit PCM WAV whose data
// chunk holds dataLen payload bytes at the given byte rate. byteRate
// and dataLen are independent knobs so duration (dataLen/byteRate) and
// total size vary freely in tests.
func wavBytes(byteRate uint32, dataLen int) []byte {
	hdr := make([]byte, 44)
	copy(hdr[0:4], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(36+dataLen))
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:20], 16)         // fmt chunk size
	binary.LittleEndian.PutUint16(hdr[20:22], 1)          // PCM
	binary.LittleEndian.PutUint16(hdr[22:24], 1)          // mono
	binary.LittleEndian.PutUint32(hdr[24:28], byteRate/2) // sample rate
	binary.LittleEndian.PutUint32(hdr[28:32], byteRate)   // byte rate
	binary.LittleEndian.PutUint16(hdr[32:34], 2)          // block align
	binary.LittleEndian.PutUint16(hdr[34:36], 16)         // bits per sample
	copy(hdr[36:40], "data")
	binary.LittleEndian.PutUint32(hdr[40:44], uint32(dataLen))
	return append(hdr, make([]byte, dataLen)...)
}

func TestASR_DecodesResponse(t *testing.T) {
	audio := wavBytes(32000, 3200) // 0.1 s at 32 kB/s
	srv, n := asrServer(t, func(w http.ResponseWriter, r *http.Request) {
		name, got := readASRUpload(t, r)
		if name != "audio.wav" {
			t.Errorf("filename = %q, want audio.wav", name)
		}
		if !bytes.Equal(got, audio) {
			t.Errorf("uploaded audio differs: got %d bytes, want %d", len(got), len(audio))
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
			t.Errorf("Content-Type = %q, want multipart/form-data with boundary", ct)
		}
		writeASRJSON(w)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	resp, err := c.ASR(context.Background(), &ASRRequest{Audio: audio})
	if err != nil {
		t.Fatalf("ASR: %v", err)
	}
	if resp.Text != "上海今天天气怎么样？" || resp.DurationMS != 3200 || resp.RequestID != "asr-req-1" {
		t.Errorf("resp = %+v, want text/duration_ms/request_id decoded", resp)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestASR_OggUploadAccepted(t *testing.T) {
	// Magic capture pattern + arbitrary payload: the client must pass
	// Ogg through without a duration pre-check (server authority).
	audio := append([]byte("OggS"), []byte("\x00\x02OpusHead-fake-payload")...)
	srv, n := asrServer(t, func(w http.ResponseWriter, r *http.Request) {
		name, got := readASRUpload(t, r)
		if name != "audio.ogg" {
			t.Errorf("filename = %q, want audio.ogg", name)
		}
		if !bytes.Equal(got, audio) {
			t.Errorf("uploaded audio differs")
		}
		writeASRJSON(w)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	if _, err := c.ASR(context.Background(), &ASRRequest{Audio: audio}); err != nil {
		t.Fatalf("ASR(ogg): %v", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestASR_4xxFailsFastWithoutRetry(t *testing.T) {
	srv, n := asrServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "audio format not supported"}}`))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.ASR(context.Background(), &ASRRequest{Audio: wavBytes(32000, 3200)})
	if err == nil {
		t.Fatal("want error on 400")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Message != "audio format not supported" {
		t.Errorf("err = %v, want APIError 400 with parsed message", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1 — non-429 4xx must NOT retry", got)
	}
}

func TestASR_RetriesOnceThenSucceeds(t *testing.T) {
	audio := wavBytes(32000, 3200)
	var first []byte
	srv, n := asrServer(t,
		func(w http.ResponseWriter, r *http.Request) { // attempt 1: transient 429
			_, first = readASRUpload(t, r)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "too many concurrent requests"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) { // attempt 2: OK
			_, second := readASRUpload(t, r)
			if !bytes.Equal(first, second) {
				t.Errorf("retry uploaded different audio: %d vs %d bytes", len(first), len(second))
			}
			writeASRJSON(w)
		},
	)
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	resp, err := c.ASR(context.Background(), &ASRRequest{Audio: audio})
	if err != nil {
		t.Fatalf("ASR after one retry: %v", err)
	}
	if resp.Text == "" {
		t.Errorf("resp = %+v, want decoded text", resp)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", got)
	}
}

func TestASR_RetriesOnceThenGivesUp(t *testing.T) {
	srv, n := asrServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"message": "boom"}}`))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.ASR(context.Background(), &ASRRequest{Audio: wavBytes(32000, 3200)})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("err = %v, want annotated 'after 2 attempts'", err)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (retry once, no more)", got)
	}
}

// TestASR_ContextCanceled is the ctx.Done path (AGENTS.md "Testing":
// any ctx-aware function gets one). The handler parks until the
// client aborts so the cancellation lands mid-flight; the abort must
// surface as context.Canceled through the "deepseek: http:" wrap and
// retryCall must NOT retry a cancelled context (retry.go:43 returns
// unannotated on ctx.Err() != nil).
//
// The body MUST be drained before parking: on HTTP/1.1 the server
// only cancels r.Context() on client disconnect once a background
// connection read is active, and net/http defers starting it until
// the request body hits EOF (registerOnHitEOF). A handler that parks
// with the body unread never learns the client left, so httptest's
// srv.Close() — which waits for outstanding handlers — hangs forever.
func TestASR_ContextCanceled(t *testing.T) {
	srv, n := asrServer(t, func(w http.ResponseWriter, r *http.Request) {
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
	resp, err := c.ASR(ctx, &ASRRequest{Audio: wavBytes(32000, 3200)})
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled in chain", err)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil", resp)
	}
	if got := atomic.LoadInt32(n); got > 1 {
		t.Errorf("requests = %d, want ≤1 — no retry after cancellation", got)
	}
}

func TestASR_BadJSONResponse(t *testing.T) {
	srv, _ := asrServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.ASR(context.Background(), &ASRRequest{Audio: wavBytes(32000, 3200)})
	if err == nil || !strings.Contains(err.Error(), "decode asr response") {
		t.Errorf("err = %v, want decode failure", err)
	}
}

func TestASR_RequestValidation(t *testing.T) {
	srv, _ := asrServer(t, func(w http.ResponseWriter, r *http.Request) { writeASRJSON(w) })
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	cases := []struct {
		name string
		req  *ASRRequest
		want string
	}{
		{"nil request", nil, "nil asr request"},
		{"empty audio", &ASRRequest{Audio: []byte{}}, "empty"},
		{"oversize", &ASRRequest{Audio: make([]byte, ASRMaxBytes+1)}, "8 MiB"},
		{"bad magic", &ASRRequest{Audio: []byte("ID3 this is an mp3 file")}, "WAV or Ogg Opus"},
		{"wav over 120s", &ASRRequest{Audio: wavBytes(8000, 8000*121)}, "120 s"},
	}
	for _, tc := range cases {
		_, err := c.ASR(context.Background(), tc.req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}

	// Boundary values must PASS client-side and reach the server:
	// exactly 120.0 s of WAV, and exactly 8 MiB total (44-byte header
	// + 8 MiB−44 data at a byte rate keeping duration under the cap).
	if _, err := c.ASR(context.Background(), &ASRRequest{Audio: wavBytes(8000, 8000*120)}); err != nil {
		t.Errorf("exactly 120 s rejected: %v", err)
	}
	if _, err := c.ASR(context.Background(), &ASRRequest{Audio: wavBytes(192000, ASRMaxBytes-44)}); err != nil {
		t.Errorf("exactly 8 MiB rejected: %v", err)
	}
}

func TestASR_MissingAPIKey(t *testing.T) {
	c := New()
	_, err := c.ASR(context.Background(), &ASRRequest{Audio: wavBytes(32000, 3200)})
	if err == nil || !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("err = %v, want missing api key", err)
	}
}

// TestWavDurationSeconds pins the header parser against real-world WAV
// quirks: LIST/INFO metadata chunks between fmt and data, and
// streaming writers' placeholder data sizes that must clamp to the
// bytes actually present.
func TestWavDurationSeconds(t *testing.T) {
	// Canonical file: 3200 payload bytes at 32 kB/s = 0.1 s.
	if got, ok := wavDurationSeconds(wavBytes(32000, 3200)); !ok || got != 0.1 {
		t.Errorf("canonical: got (%v, %v), want (0.1, true)", got, ok)
	}

	// LIST chunk before fmt: the walk must skip it, not assume offsets.
	list := make([]byte, 12) // "LIST" + size 4 + "INFO"
	copy(list, "LIST\x04\x00\x00\x00")
	copy(list[8:], "INFO")
	fmtChunk := make([]byte, 24)
	copy(fmtChunk, "fmt \x10\x00\x00\x00")
	binary.LittleEndian.PutUint32(fmtChunk[8+8:], 16000) // byte rate at payload +8
	data := make([]byte, 8+16000)
	copy(data, "data")
	binary.LittleEndian.PutUint32(data[4:], 16000)
	riff := append([]byte("RIFF\x00\x00\x00\x00WAVE"), list...)
	riff = append(riff, fmtChunk...)
	riff = append(riff, data...)
	binary.LittleEndian.PutUint32(riff[4:8], uint32(len(riff)-8))
	if got, ok := wavDurationSeconds(riff); !ok || got != 1.0 {
		t.Errorf("LIST-interleaved: got (%v, %v), want (1.0, true)", got, ok)
	}

	// Placeholder data size 0xFFFFFFFF must clamp to available bytes:
	// 3200 present at 32 kB/s = 0.1 s, not a false over-cap rejection.
	ph := wavBytes(32000, 3200)
	binary.LittleEndian.PutUint32(ph[40:44], 0xFFFFFFFF)
	if got, ok := wavDurationSeconds(ph); !ok || got != 0.1 {
		t.Errorf("placeholder size: got (%v, %v), want (0.1, true)", got, ok)
	}

	// Bare RIFF header with no chunks at all → cannot pre-check, not a
	// rejection (the server stays the format authority).
	if _, ok := wavDurationSeconds([]byte("RIFF\x24\x00\x00\x00WAVE")); ok {
		t.Errorf("header-only file: ok = true, want false (no chunks to read)")
	}
}
