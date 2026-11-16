// tts.go — the standalone TTS API (POST /v1/tools/tts), part of the
// tool-API family opened 2026-11-13. HTTP-integral BY DESIGN: the whole
// text goes up as one JSON request and the synthesized audio comes back
// as one raw byte stream — no JSON envelope, no request_id, nothing to
// correlate. The WebSocket variant (wss://…/tts, segment streaming,
// opus) is deliberately not modelled: seek's voice frontend plays whole
// announcements (PRD feature-deepseek-app-api §4.3), and the API offers
// no interrupt/resume anyway. Billing is per BILLING CHARACTER (Han
// runes count 2) — pricing lives in internal/pricing.TTSCost, not the
// ModelPricing token table.
//
// Same key / base URL as chat; same retryCall policy as Search and ASR
// (transient 5xx / 429 / transport retry once, other 4xx fail fast). A
// stream that dies mid-body surfaces as a transport error on the read
// and is retried like any other blip.
package deepseek

import (
	"context"
	"errors"
	"fmt"
	"io"
	"unicode"
)

// endpointTTS is the standalone TTS API path (api-docs.deepseek.com
// /guides/tts), versioned under /v1 like the rest of the tool family.
const endpointTTS = "/v1/tools/tts"

// TTS constraints from the published reference: at most 10,000 billing
// characters per request (Han characters count as two) and a request
// body of at most 64 KiB; voices stella (default) and tide; output
// wav or pcm (24 kHz 16-bit mono) — opus exists on the WebSocket
// variant only. Speed control and interrupt/resume do not exist.
//
// The 64 KiB body cap is SHADOWED by the char cap: 10,000 billing
// chars marshal to ≤ ~60 KB even at worst-case \uXXXX escape
// inflation (6 bytes per 1-byte control char), so no enforceable
// request can exceed it. TTSMaxBodyBytes stays as the published
// constraint's documentation — and as the tripwire to re-derive the
// shadow math if a future field (a longer voice list, SSML tags)
// ever changes the per-char inflation.
const (
	TTSMaxBillableChars = 10_000
	TTSMaxBodyBytes     = 64 << 10
)

// Voices offered by the published reference. Empty VoiceID means "let
// the server default apply" (stella).
const (
	TTSVoiceStella = "stella"
	TTSVoiceTide   = "tide"
)

// Output container formats on the HTTP endpoint. WAV is a RIFF/WAVE
// file (self-describing); PCM is headerless raw samples — 24 kHz
// 16-bit mono, so only the requester knows the shape. Empty Format
// lets the server default apply; pass it explicitly when the bytes'
// interpretation matters to the caller.
const (
	TTSAudioWAV = "wav"
	TTSAudioPCM = "pcm"
)

// TTSRequest is the text to synthesize.
type TTSRequest struct {
	// Text is the full text to speak. Markdown filtering happens
	// server-side and is on by default; the client sends text verbatim.
	Text string `json:"text"`
	// VoiceID is one of TTSVoiceStella / TTSVoiceTide, or "" for the
	// server default.
	VoiceID string `json:"voice_id,omitempty"`
	// Format is one of TTSAudioWAV / TTSAudioPCM, or "" for the server
	// default.
	Format string `json:"format,omitempty"`
}

// TTS synthesizes one clip and returns the raw audio bytes (wav file
// or headerless pcm, per req.Format). Transient failures (5xx, 429,
// transport — including a stream that dies mid-body) retry once via
// the same retryCall policy as Chat, Search and ASR; other 4xx are
// configuration errors (oversize text, bad voice/format, auth) and
// fail fast. Like Search and ASR, only a 2xx counts as billable —
// cost accounting must key off this returning nil and price
// TTSBillableChars(req.Text) via pricing.TTSCost, not off "a request
// was attempted". The response carries no billing fields, so the
// caller-side counter below is the only local source of the number.
func (c *Client) TTS(ctx context.Context, req *TTSRequest) ([]byte, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil tts request")
	}
	if n := TTSBillableChars(req.Text); n == 0 {
		return nil, errors.New("deepseek: tts text is empty")
	} else if n > TTSMaxBillableChars {
		return nil, fmt.Errorf("deepseek: tts text must be ≤%d billing characters (Han counts 2), got %d", TTSMaxBillableChars, n)
	}
	// No 64 KiB body gate: the char cap shadows it completely — 10,000
	// billing chars marshal to at most ~60 KB (a 1-byte control char
	// escapes to six \uXXXX bytes, the worst inflation; every other
	// rune is ≤ its escape), so a body over the cap cannot be built
	// through the public fields. See TTSMaxBodyBytes.
	switch req.VoiceID {
	case "", TTSVoiceStella, TTSVoiceTide:
	default:
		return nil, fmt.Errorf("deepseek: tts voice_id must be %q or %q (or empty for the default), got %q", TTSVoiceStella, TTSVoiceTide, req.VoiceID)
	}
	switch req.Format {
	case "", TTSAudioWAV, TTSAudioPCM:
	default:
		return nil, fmt.Errorf("deepseek: tts format must be %q or %q (opus is websocket-only), got %q", TTSAudioWAV, TTSAudioPCM, req.Format)
	}

	return retryCall(ctx, func() ([]byte, error) {
		resp, err := c.do(ctx, endpointTTS, req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()

		// The 2xx body IS the audio — read it whole before the status
		// check so an error body is available to parseAPIError either
		// way (same shape as Search/ASR, just without the JSON decode
		// on success).
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("deepseek: read tts audio: %w", err)
		}
		if resp.StatusCode/100 != 2 {
			return nil, parseAPIError(resp.StatusCode, body)
		}
		if len(body) == 0 {
			return nil, errors.New("deepseek: tts returned an empty audio body")
		}
		return body, nil
	})
}

// TTSBillableChars returns text's billing character count: every rune
// counts one, Han characters count two (汉字算 2 字符 — the published
// billing rule). It powers both the client-side ≤10,000 pre-check and
// the caller's cost accounting (pricing.TTSCost takes this number).
// The server stays the billing authority; if its rune weighting ever
// diverges from this reading of the rule, the divergence shows up as
// a gap in the real-API bill reconciliation smoke, not as a rejected
// request.
func TTSBillableChars(text string) int {
	n := 0
	for _, r := range text {
		n++
		if unicode.Is(unicode.Han, r) {
			n++
		}
	}
	return n
}
