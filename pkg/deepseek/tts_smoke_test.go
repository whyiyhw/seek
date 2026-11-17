package deepseek_test

// Real-API smoke for the TTS client — the only test here that talks to
// api.deepseek.com. Gated exactly like the ASR roundtrip smoke: skipped
// unless DEEPSEEK_API_KEY is set and not under -short, so CI and
// `go test ./...` stay hermetic and free.
//
// Card shape ("stella 合成 100 字", card c_k4iejt / PRD
// feature-deepseek-app-api §4.2 S1): synthesize a 100-Chinese-character
// announcement with the stella voice through Client.TTS, verify the
// returned WAV, and log the locally-counted billable characters against
// pricing.TTSCost — the §7 test plan's "真 API 账单对数" evidence: the
// console bill for this call should read 200 billing characters.
// Expected spend at list price: 100 Han ×2 = 200 chars ≈ $0.009 (¥0.06).
//
//	printf 'DEEPSEEK_API_KEY=sk-...\n' >> .env   # .env is gitignored
//	set -a && . ./.env && set +a
//	go test ./pkg/deepseek/ -run TestTTS_RealAPIStella100 -v
//
// Status 2026-11-16: delivered by card c_k4iejt but NOT yet run — the
// card's worktree has no .env. Main session: run the three lines above
// (expected cost ≈¥0.06 at list price) and fold the logged
// chars/cost/bytes plus the console bill's char count back into the
// card as the smoke evidence. If the console bills Han as 1 (or counts
// full-width punctuation as 2), TTSBillableChars' reading of the rule
// needs a docs/pitfalls.md entry and a fix.

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// smokeText is exactly 100 Han runes (ten groups of ten, no punctuation
// — punctuation would bill 1 and break the 200-char evidence) → 200
// billing characters.
const smokeText = "任务已经全部完成现在可以安全地合并新代码所有自动化测试都通过文档检查没有发现问题没有任何遗留的问题了顺利地交付了本次功能感谢每一位同事的付出大家都可以好好地休息期待下个版本继续加油我们继续再创好的佳绩"

func TestTTS_RealAPIStella100(t *testing.T) {
	if testing.Short() {
		t.Skip("real-API test; skipped under -short")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set; see this file's header for how to run the TTS smoke")
	}

	// Billing guard BEFORE spending money: the text must count exactly
	// 200, else the console-bill comparison below is meaningless.
	chars := deepseek.TTSBillableChars(smokeText)
	if chars != 200 {
		t.Fatalf("smoke text counts %d billable chars, want exactly 200 (100 Han ×2) — edit smokeText", chars)
	}

	c := deepseek.New(deepseek.WithAPIKey(key))
	wav, err := c.TTS(context.Background(), &deepseek.TTSRequest{
		Text: smokeText, VoiceID: deepseek.TTSVoiceStella, Format: deepseek.TTSAudioWAV,
	})
	if err != nil {
		t.Fatalf("TTS: %v", err)
	}
	// WAV contract: RIFF/WAVE magic. The documented output shape is
	// 24 kHz 16-bit mono = 48,000 body bytes/second, so ≥3 s of speech
	// needs ≥144,000 bytes; the 8 MiB ASR cap bounds the top end (the
	// same clip could feed an ASR roundtrip if ever needed).
	if len(wav) < 44 || !bytes.HasPrefix(wav, []byte("RIFF")) || !bytes.Equal(wav[8:12], []byte("WAVE")) {
		t.Fatalf("body is not RIFF/WAVE: %d bytes, head %q", len(wav), preview(wav, 12))
	}
	if len(wav) < 48_000*3 {
		t.Errorf("wav is only %d bytes (<3 s at the documented 24 kHz 16-bit mono rate) — likely not real speech", len(wav))
	}
	if len(wav) > deepseek.ASRMaxBytes {
		t.Errorf("wav %d bytes exceeds the 8 MiB ASR cap", len(wav))
	}
	// The billing evidence: locally counted chars × list rate.
	t.Logf("tts: 100 Han chars → %d billable → cost %s (list) / %s (50%% off), wav %d bytes",
		chars,
		pricing.FormatCost(pricing.TTSCost(chars)),
		pricing.FormatCost(pricing.TTSCost(chars)/2),
		len(wav))
}
