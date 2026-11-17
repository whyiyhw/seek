package deepseek_test

// Real-API smoke for the ASR client — the only test here that talks to
// api.deepseek.com. Gated exactly like pkg/agent/cache_e2e_test.go:
// skipped unless DEEPSEEK_API_KEY is set and not under -short, so CI
// and `go test ./...` stay hermetic and free.
//
// Roundtrip shape ("WAV 往返转写", card c_5ckroy / PRD
// feature-deepseek-app-api §4.2 S2): synthesize a short Chinese
// phrase with the TTS endpoint via raw HTTP (the TTS client is S1, a
// separate card), then transcribe the returned 24 kHz 16-bit WAV
// through Client.ASR and require the phrase back. Expected spend at
// list price: ≈¥0.0003 TTS (10 chars) + ≈¥0.0001 ASR (a few seconds).
//
//	printf 'DEEPSEEK_API_KEY=sk-...\n' >> .env   # .env is gitignored
//	set -a && . ./.env && set +a
//	go test ./pkg/deepseek/ -run TestASR_RealAPIRoundtrip -v
//
// Status 2026-11-16: delivered by card c_5ckroy but NOT yet run — the
// card's worktree has no .env. Main session: run the three lines above
// (expected cost well under ¥0.01) and fold the logged text/cost back
// into the card as the smoke evidence.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestASR_RealAPIRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("real-API test; skipped under -short")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set; see this file's header for how to run the ASR roundtrip smoke")
	}
	ctx := context.Background()

	// 1. TTS → WAV. The HTTP-integral TTS endpoint streams raw audio
	// bytes (no JSON envelope); format=wav yields RIFF/WAVE.
	body, err := json.Marshal(map[string]string{
		"text": "你好，今天天气怎么样", "voice_id": "stella", "format": "wav",
	})
	if err != nil {
		t.Fatal(err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		deepseek.DefaultBaseURL+"/v1/tools/tts", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	hreq.Header.Set("Authorization", "Bearer "+key)
	hreq.Header.Set("Content-Type", "application/json")
	hresp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(hreq)
	if err != nil {
		t.Fatalf("tts request: %v", err)
	}
	defer func() { _ = hresp.Body.Close() }()
	wav, err := io.ReadAll(hresp.Body)
	if err != nil {
		t.Fatalf("read tts audio: %v", err)
	}
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("tts status %d: %s", hresp.StatusCode, preview(wav, 200))
	}
	if len(wav) < 12 || !bytes.HasPrefix(wav, []byte("RIFF")) || !bytes.Equal(wav[8:12], []byte("WAVE")) {
		t.Fatalf("tts body is not RIFF/WAVE: %q", preview(wav, 16))
	}
	if len(wav) > deepseek.ASRMaxBytes {
		t.Fatalf("tts wav %d bytes exceeds the 8 MiB ASR cap — shorten the phrase", len(wav))
	}

	// 2. ASR roundtrip, priced exactly the way a caller will.
	c := deepseek.New(deepseek.WithAPIKey(key))
	out, err := c.ASR(ctx, &deepseek.ASRRequest{Audio: wav})
	if err != nil {
		t.Fatalf("ASR: %v", err)
	}
	if !strings.Contains(out.Text, "天气") {
		t.Errorf("roundtrip text %q lost the source phrase (duration_ms=%d)", out.Text, out.DurationMS)
	}
	t.Logf("roundtrip: text=%q duration_ms=%d cost=%s",
		out.Text, out.DurationMS, pricing.FormatCost(pricing.ASRCost(float64(out.DurationMS)/1000)))
}

func preview(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.TrimSpace(string(b))
}
