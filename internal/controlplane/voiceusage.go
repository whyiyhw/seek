package controlplane

// voiceusage.go — the §6 serve-side voice bookkeeping, shared by BOTH
// directions of the voice lane: the outbound TTS sink (speaker.go, card
// V1a) and the inbound ASR frontend (voicefrontend.go, card V2). One
// append-only JSONL file under <serve>/voice-usage.jsonl — one line per
// BILLED API call, whichever lane spent the money. The serve process has
// no session Tracker, so usage lands next to bindings.json under the same
// dir discipline instead of being forced into session cost.
//
// The two lanes price differently (TTS: billable chars, han×2, via
// pricing.TTSCost; ASR: per-second-of-audio rounded up, via
// pricing.ASRCost) but they must be SUMMABLE: one row shape, one
// cost_usd, a `kind` discriminator, and each lane's own billing basis
// field (billable_chars / duration_ms) so the number can be audited
// back to the wire. Only 2xx responses are billed — both clients'
// contracts say so — so a row's existence means money was spent,
// whatever the caller then did with the reply.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// voiceUsageFile is the ledger's filename under <serve>/ (the speaker's
// default UsagePath and the voice frontend's default — one file, two
// writers).
const voiceUsageFile = "voice-usage.jsonl"

// Ledger lane discriminators (voiceUsageLine.Kind).
const (
	voiceKindTTS = "tts"
	voiceKindASR = "asr"
)

// ASR row outcomes: a transcription that produced text, and one the API
// billed but recognised as nothing (still a 2xx spend — the frontend
// then answers the page with voice-error, but the money left the account).
const (
	voiceOutcomeTranscribed = "transcribed"
	voiceOutcomeEmpty       = "empty_transcript"
)

// voiceUsageLine is one row of voice-usage.jsonl. TTS rows carry
// billable_chars + an outcome (played / wav_fallback); ASR rows carry the
// server-reported duration_ms the price was derived from. cost_usd is the
// single aggregation key: sum it per conv / per day, never re-derive it
// from the basis fields (pricing owns the rates).
//
// Rows written before card c_yc2thl (the ASR wiring) have no `kind`
// field — every one of them is a TTS row (that lane was the file's only
// writer), so a reader folding the file should treat an absent kind as
// voiceKindTTS rather than "unknown".
type voiceUsageLine struct {
	TS            time.Time `json:"ts"`
	Kind          string    `json:"kind"`                     // voiceKindTTS | voiceKindASR
	Conv          string    `json:"conv"`                     // the conversation the spend belongs to
	BillableChars int       `json:"billable_chars,omitempty"` // tts: 计费字符 (汉字×2)
	DurationMS    int64     `json:"duration_ms,omitempty"`    // asr: 服务端回报的音频时长
	CostUSD       float64   `json:"cost_usd"`
	Outcome       string    `json:"outcome"`
}

// appendVoiceUsageLine appends one row to the ledger at path. Best-effort
// by contract (the §6 bookkeeping note): an open/write failure returns the
// error for the caller to WARN — it must never fail the call that already
// happened. The serve dir is created 0o700 like the binding store's (the
// sibling env file holds credentials), so the first append works on a
// fresh serve.
func appendVoiceUsageLine(path string, line voiceUsageLine) error {
	b, err := json.Marshal(line)
	if err != nil {
		return err // unreachable for these field types
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}
