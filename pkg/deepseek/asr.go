// asr.go — the standalone ASR API (POST /v1/tools/asr), part of the
// tool-API family opened 2026-11-13. Non-streaming BY DESIGN: the
// complete clip goes up in one multipart request and the transcription
// comes back whole — the voice-frontend "voice bar" shape (record
// ≤120s, then send), never live streaming (the API offers none).
// Audio is billed per second of clip duration rounded up — pricing
// lives in internal/pricing.ASRCost, not the ModelPricing token table.
//
// Same key / base URL as chat; same retryCall policy as Search
// (transient 5xx / 429 / transport retry once, other 4xx fail fast).
package deepseek

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// endpointASR is the standalone ASR API path (api-docs.deepseek.com
// /guides/asr), versioned under /v1 like the rest of the tool family.
const endpointASR = "/v1/tools/asr"

// ASR constraints from the published reference: 16-bit PCM WAV or Ogg
// Opus containers, at most 8 MiB and 120 seconds of audio, 21
// languages auto-detected server-side (there is no language parameter).
const (
	ASRMaxBytes   = 8 << 20
	ASRMaxSeconds = 120.0
)

// ASRRequest is the audio clip to transcribe. Container format is
// detected from the payload's magic bytes; the server remains the
// authority on everything beyond container level (16-bit PCM depth,
// sample rates, Opus vs other Ogg codecs).
type ASRRequest struct {
	// Audio is the complete audio file: 16-bit PCM WAV or Ogg Opus.
	Audio []byte
}

// ASRResponse is the 200 body.
type ASRResponse struct {
	RequestID string `json:"request_id"`
	// Text is the full transcription.
	Text string `json:"text"`
	// DurationMS is the recognised clip length in milliseconds — the
	// billing basis. Pass DurationMS/1000 into pricing.ASRCost (which
	// rounds up to whole seconds, server-style).
	DurationMS int64 `json:"duration_ms"`
}

// ASR transcribes one audio clip. Transient failures (5xx, 429,
// transport) retry once via the same retryCall policy as Chat and
// Search; other 4xx are configuration errors (bad format, oversize
// audio, auth) and fail fast. Like Search, only a 2xx counts as
// billable — cost accounting must key off this returning nil and price
// resp.DurationMS, not off "a request was attempted".
func (c *Client) ASR(ctx context.Context, req *ASRRequest) (*ASRResponse, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil asr request")
	}
	if len(req.Audio) == 0 {
		return nil, errors.New("deepseek: asr audio is empty")
	}
	if len(req.Audio) > ASRMaxBytes {
		return nil, fmt.Errorf("deepseek: asr audio must be ≤%d MiB, got %d bytes", ASRMaxBytes>>20, len(req.Audio))
	}

	// Client-side checks are early-outs for doomed uploads, not a
	// format authority: reject only what we can judge with certainty,
	// and let the server decide everything else. A false local
	// rejection is worse than a wasted round trip.
	filename := "audio.wav"
	switch {
	case looksLikeWAV(req.Audio):
		if dur, ok := wavDurationSeconds(req.Audio); ok && dur > ASRMaxSeconds {
			return nil, fmt.Errorf("deepseek: asr audio must be ≤%.0f s, wav header says %.1f s", ASRMaxSeconds, dur)
		}
	case looksLikeOgg(req.Audio):
		// No duration pre-check: Ogg duration needs the last page's
		// granule position, a heuristic parse this client won't own.
		// The server enforces the 120 s cap authoritatively.
		filename = "audio.ogg"
	default:
		return nil, errors.New("deepseek: asr audio must be 16-bit PCM WAV or Ogg Opus (unrecognised magic bytes)")
	}

	return retryCall(ctx, func() (*ASRResponse, error) {
		resp, err := c.doASR(ctx, req.Audio, filename)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("deepseek: read asr response: %w", err)
		}
		if resp.StatusCode/100 != 2 {
			return nil, parseAPIError(resp.StatusCode, body)
		}

		out := &ASRResponse{}
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("deepseek: decode asr response: %w", err)
		}
		return out, nil
	})
}

// doASR POSTs the audio as a one-field multipart form (field "file",
// per the published reference). The package's only non-JSON transport,
// so it lives here rather than in client.go's do — the multipart body
// is rebuilt per attempt, which keeps retryCall simple and the retries
// byte-identical in content (only the boundary differs, which servers
// do not see as content).
func (c *Client) doASR(ctx context.Context, audio []byte, filename string) (*http.Response, error) {
	if c.apiKey == "" {
		return nil, errors.New("deepseek: missing api key (set DEEPSEEK_API_KEY)")
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("deepseek: build asr multipart: %w", err)
	}
	if _, err := fw.Write(audio); err != nil {
		return nil, fmt.Errorf("deepseek: build asr multipart: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("deepseek: build asr multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpointASR, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepseek: http: %w", err)
	}
	return resp, nil
}

// looksLikeWAV checks the RIFF/WAVE container magic.
func looksLikeWAV(b []byte) bool {
	return len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WAVE"))
}

// looksLikeOgg checks the Ogg capture pattern. Opus-vs-Vorbis depth
// stays server-side; this only rules out the wrong-file-altogether case.
func looksLikeOgg(b []byte) bool {
	return bytes.HasPrefix(b, []byte("OggS"))
}

// wavDurationSeconds returns the playback duration implied by a
// RIFF/WAVE header, or ok=false when the header doesn't yield one
// (zero byte rate, missing data chunk, or sizes running past the
// buffer — streaming writers emit placeholder sizes). Callers treat
// ok=false as "cannot pre-check", never as rejection.
//
// Chunks are WALKED rather than read at fixed offsets: LIST/INFO
// metadata chunks between fmt and data are common in encoder output,
// and every chunk is word-aligned (odd sizes carry one pad byte).
func wavDurationSeconds(b []byte) (seconds float64, ok bool) {
	// Caller has verified the 12-byte RIFF/WAVE magic.
	var byteRate, dataLen uint64
	off := 12
	for off+8 <= len(b) && (byteRate == 0 || dataLen == 0) {
		id := string(b[off : off+4])
		size := uint64(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		switch id {
		case "fmt ":
			if body+12 <= len(b) { // byte rate sits at fmt payload +8
				byteRate = uint64(binary.LittleEndian.Uint32(b[body+8 : body+12]))
			}
		case "data":
			if avail := uint64(len(b) - body); size > avail {
				size = avail // clamp placeholder (e.g. 0xFFFFFFFF) sizes
			}
			dataLen = size
		}
		next := uint64(body) + size + size%2 // chunks are word-aligned
		if next > uint64(len(b)) {
			break // header claims chunks past the buffer — nothing more to read
		}
		off = int(next)
	}
	if byteRate == 0 || dataLen == 0 {
		return 0, false
	}
	return float64(dataLen) / float64(byteRate), true
}
