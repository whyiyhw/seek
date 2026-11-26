package controlplane

// Voice frontend v2 push-to-talk inbound (feature-deepseek-app-api
// §4.3 card V2): the web page's 🎤 button records ≤120 s, encodes
// 16-bit PCM WAV client-side and POSTs it to /voice; this frontend
// transcribes it via the ASR seam and pushes the text into the plane
// as a normal Inbound. No transport of its own: the endpoint rides
// the web frontend's listener (the card's "录音 UI 载体 = web
// frontend" — voice without web has no recorder UI and refuses to
// run), and replies ride the web SSE of the page that uploaded.
//
// Semantics pinned by the card:
//
//	ConvID    voice:<device-id> — a per-BROWSER uuid, so the voice
//	          conversation (and its binding) is the device's, distinct
//	          from the page's web:<uuid> chat conv.
//	DirectAt  always true — push-to-talk has no group-chatter shape;
//	          pressing the button IS addressing seek.
//	EventID   the recording-session id (one uuid per button press).
//	          The frontend itself does not dedup — the router's EventID
//	          LRU is the authority, same as every other channel.
//
// Out of scope by design (§4.3): full-duplex streaming (ASR has no
// streaming endpoint, TTS no interruption — do not fake either). Send
// is text-only through the web page; TTS playback is the v1 cards'
// job (V1a/V1b), not this one.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// voiceQueueDepth bounds uploads waiting for the Run loop. Push-to-talk
// is human-paced; the buffer only absorbs simultaneous presses while an
// ASR call is in flight. Full buffer → 503 (the client shows a hint).
const voiceQueueDepth = 4

// voiceDeviceCap bounds the device→page routing table. Devices are
// browser-minted uuids; every private window mints a fresh one, so an
// unbounded map is a slow leak on a long-lived serve. At the cap the
// table wipes wholesale — crude but bounded, and the next upload
// re-registers (a wipe loses at most one stale reply per device).
const voiceDeviceCap = 1024

// Header names of the /voice upload. Same X-Seek- naming family as
// X-Seek-Conversation; EventSource cannot set headers but /voice is a
// fetch, so headers are fine here.
const (
	voiceDeviceHeader = "X-Seek-Device"
	voiceEventHeader  = "X-Seek-Voice-Event"
)

// VoiceASR is the plane's narrow ASR seam: one clip in, one transcript
// (+ its billing duration) out. Production is the S2 client adapter
// below; tests stub it. Keeping the seam inside controlplane means the
// full inbound path (upload → validate → transcribe → Inbound → Send)
// is testable without any network.
type VoiceASR interface {
	Transcribe(ctx context.Context, audio []byte) (text string, durationMS int64, err error)
}

// DeepSeekVoiceASR adapts pkg/deepseek's ASR client (S2, card c_5ckroy)
// onto VoiceASR. The client owns every format constraint (WAV/Ogg
// magic, ≤8 MiB, ≤120 s, transient retry) — the adapter adds nothing.
type DeepSeekVoiceASR struct{ c *deepseek.Client }

// NewDeepSeekVoiceASR builds the adapter against the default DeepSeek
// endpoint with the environment API key (deepseek.New's own fallback).
// A missing key surfaces per upload as an ASR failure — the plane keeps
// serving, the page gets an actionable error frame.
func NewDeepSeekVoiceASR() *DeepSeekVoiceASR {
	return &DeepSeekVoiceASR{c: deepseek.New()}
}

// Transcribe implements VoiceASR.
func (a *DeepSeekVoiceASR) Transcribe(ctx context.Context, audio []byte) (string, int64, error) {
	resp, err := a.c.ASR(ctx, &deepseek.ASRRequest{Audio: audio})
	if err != nil {
		return "", 0, err
	}
	return resp.Text, resp.DurationMS, nil
}

// VoiceFrontend is the push-to-talk Frontend. Safe for concurrent use:
// Run from the serve goroutine, Send from executor goroutines, the
// mounted /voice handler from the web server's goroutines.
type VoiceFrontend struct {
	asr VoiceASR
	web *WebFrontend // transport host, set by Attach before any Run

	uploads chan voiceUpload // /voice handler → Run loop

	usage string // append-JSONL cost log, resolved in Run when empty
	now   func() time.Time

	// usageOff is set when the default ledger path could not be resolved:
	// the frontend keeps transcribing, the bookkeeping is off (already
	// warned once at Run — a per-call warn would spam the serve log).
	usageOff bool

	inboxMu sync.RWMutex
	inbox   chan<- Inbound // set by Run; the handler never touches it

	devMu   sync.Mutex
	devices map[string]ConvID // device-id → the web conv of its last upload (Send routing)
}

// voiceUpload is one accepted recording, queued for transcription.
type voiceUpload struct {
	device  string // browser-minted device uuid (the ConvID native part)
	event   string // recording-session uuid → Inbound.EventID
	webConv ConvID // the uploading page's web conv (echo + reply target)
	wav     []byte
}

// Compile-time port check.
var _ Frontend = (*VoiceFrontend)(nil)

// NewVoice builds the frontend around one ASR implementation. It is
// inert until Attach hosts it on a web frontend. Billing rows land in
// <serve>/voice-usage.jsonl (the speaker's file — one ledger, both
// directions of the voice lane).
func NewVoice(asr VoiceASR) *VoiceFrontend {
	return newVoice(asr, "")
}

// NewVoiceAt builds the frontend with an explicit usage-log path — the
// test / injection seam that avoids the host-global paths.ServeDir, the
// same "At" shape as OpenBindingStoreAt (and SpeakerConfig.UsagePath).
func NewVoiceAt(asr VoiceASR, usagePath string) *VoiceFrontend {
	return newVoice(asr, usagePath)
}

func newVoice(asr VoiceASR, usagePath string) *VoiceFrontend {
	return &VoiceFrontend{
		asr:     asr,
		uploads: make(chan voiceUpload, voiceQueueDepth),
		usage:   usagePath,
	}
}

// Attach hosts the voice frontend on web's HTTP surface: /voice joins
// web's route table inside the same auth/CSP/host-gate wrap stack.
// Pre-Run only (the §3.2 phase contract, like RPC registration) — the
// mux is built once in web's Run; after that a mount would silently
// never serve.
func (v *VoiceFrontend) Attach(web *WebFrontend) {
	v.web = web
	web.mountVoice(v.handleUpload)
}

// Name implements the Frontend port.
func (v *VoiceFrontend) Name() string { return "voice" }

// Run implements the Frontend port: park on the upload queue, turn
// each recording into one Inbound (ASR → transcript echo → plane).
// The uploads are drained SERIALLY — one ASR call at a time; a queue
// of pending presses waits (§4.3's no-streaming posture: recording is
// push-to-talk, never a live channel).
func (v *VoiceFrontend) Run(ctx context.Context, in chan<- Inbound) error {
	if v.web == nil {
		return errors.New("voice: no web frontend attached — start voice together with web (--frontend=web --frontend=voice)")
	}
	if v.asr == nil {
		return errors.New("voice: no ASR client configured")
	}
	// Cost ledger resolution happens HERE (the Run boundary), the same
	// laziness as the speaker's: paths.ServeDir only computes a path, and
	// a host with no resolvable home loses the bookkeeping, never the
	// frontend (best-effort by contract).
	if v.now == nil {
		v.now = time.Now
	}
	if v.usage == "" {
		serveDir, err := paths.ServeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "controlplane: voice: usage log disabled (resolve serve dir): %v\n", err)
			v.usageOff = true
		} else {
			v.usage = filepath.Join(serveDir, voiceUsageFile)
		}
	}
	v.inboxMu.Lock()
	v.inbox = in
	v.inboxMu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil
		case up := <-v.uploads:
			v.deliver(ctx, up, in)
		}
	}
}

// deliver runs the V2 inbound path for one upload: transcribe, echo
// the transcript to the uploading page, push the Inbound. ASR failure
// and empty transcripts never reach the plane — the page gets a
// voice-error frame instead (the router would drop empty text anyway;
// better to tell the user than to log into the void).
func (v *VoiceFrontend) deliver(ctx context.Context, up voiceUpload, in chan<- Inbound) {
	text, durMS, err := v.asr.Transcribe(ctx, up.wav)
	if err != nil {
		// Shutdown drain cancels mid-transcription: not the user's
		// fault and nobody is left listening — skip the frame.
		if ctx.Err() != nil {
			return
		}
		fmt.Fprintf(os.Stderr, "controlplane: voice: asr failed (event %s): %v\n", up.event, err)
		v.notify(up.webConv, "voice-error", webJSON(voiceErrorEvent{Error: "语音识别失败：" + err.Error()}))
		return
	}
	fmt.Fprintf(os.Stderr, "controlplane: voice: transcribed event %s: %dms, %d chars\n", up.event, durMS, len([]rune(text)))
	// Billed BEFORE the empty-transcript early-out: a 2xx transcription
	// costs money whether or not it recognised anything (the ASR client's
	// contract — only 2xx is billable, and this one was). Same posture as
	// the speaker's logUsage: the row is bookkeeping, the frame below is
	// the user's answer.
	outcome := voiceOutcomeTranscribed
	if strings.TrimSpace(text) == "" {
		outcome = voiceOutcomeEmpty
	}
	v.logUsage(up.device, durMS, outcome)
	if strings.TrimSpace(text) == "" {
		v.notify(up.webConv, "voice-error", webJSON(voiceErrorEvent{Error: "（未识别到语音内容）"}))
		return
	}

	// Register the reply route BEFORE the Inbound: the run this message
	// triggers will Send through the table, and the registering upload
	// has always arrived before its own reply (Send-after-Inbound is
	// the router's contract, not a hope).
	v.devMu.Lock()
	if v.devices == nil {
		v.devices = make(map[string]ConvID)
	}
	if len(v.devices) >= voiceDeviceCap {
		v.devices = make(map[string]ConvID) // cap reached: wipe, see voiceDeviceCap
	}
	v.devices[up.device] = up.webConv
	v.devMu.Unlock()

	// Transcript echo: the page shows what was recognised as a `you`
	// bubble. A NAMED event — the default onmessage path stays exactly
	// the MVP contract (named events need addEventListener, §2.5).
	v.notify(up.webConv, "voice", webJSON(voiceEchoEvent{Text: text}))

	select {
	case in <- Inbound{
		Conv:     NewConvID("voice", up.device),
		Text:     text,
		Sender:   up.device,
		EventID:  up.event,
		DirectAt: true, // push-to-talk: pressing the button IS addressing seek
	}:
	case <-ctx.Done():
	}
}

// notify frames one named SSE event to the page's own web conv.
func (v *VoiceFrontend) notify(webConv ConvID, name, data string) {
	v.web.broadcast(webConv, sseFrame{name: name, data: data})
}

// logUsage appends one billed-transcription row to the voice ledger —
// the ASR half of the §6 serve-side bookkeeping the speaker's TTS rows
// live in. Pricing basis is the SERVER-reported duration_ms (the ASR
// response's own number, not the uploaded container's), passed raw to
// pricing.ASRCost which ceils to whole seconds. Best-effort: a failed
// append WARNs and never disturbs the inbound path (the money was
// already spent; losing the row loses only the tally).
func (v *VoiceFrontend) logUsage(device string, durationMS int64, outcome string) {
	if v.usage == "" || v.usageOff {
		return // resolution failed (already warned at Run) or not yet run
	}
	if err := appendVoiceUsageLine(v.usage, voiceUsageLine{
		TS:         v.now().UTC(),
		Kind:       voiceKindASR,
		Conv:       string(NewConvID("voice", device)),
		DurationMS: durationMS,
		CostUSD:    pricing.ASRCost(float64(durationMS) / 1000),
		Outcome:    outcome,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "controlplane: voice: usage log: %v\n", err)
	}
}

// Send implements the Frontend port: deliver one reply to the PAGE
// that last spoke for this device. Voice has no own transport — the
// frame rides the web conv's SSE subscribers with the MVP reply shape.
//
// Deliberately NOT web.Send: that method pops the conv's live run for
// the W1.4 close-out, and this conv is the PAGE's web conv — a voice
// reply arriving while the page's own typed run is streaming would
// close out that run's bubble with the wrong text. The bare broadcast
// (no run id) renders as a plain message, which is exactly right.
//
// Unknown device (voice inbound predates this process — restart, or a
// Send without any upload): drop, best-effort like every Send.
func (v *VoiceFrontend) Send(ctx context.Context, conv ConvID, r Reply) error {
	v.devMu.Lock()
	webConv, ok := v.devices[conv.Native()]
	v.devMu.Unlock()
	if !ok {
		return nil
	}
	data, err := json.Marshal(map[string]string{"conv": string(webConv), "text": r.Text})
	if err != nil {
		return err // unreachable for string payloads, but the type says it
	}
	v.web.broadcast(webConv, sseFrame{data: string(data)})
	return nil
}

// ---- the /voice upload endpoint (mounted on the web frontend) ----

// handleUpload is the push-to-talk transport: auth (the SAME stack as
// POST /msg — a recording drives seek runs, so it is write-surface),
// validate, enqueue, 202. Transcription happens in the Run loop, not
// here: the HTTP request must not hang on a seconds-long ASR call.
func (v *VoiceFrontend) handleUpload(w http.ResponseWriter, r *http.Request) {
	switch v.web.authKind(r) {
	case credNone:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing token (use ?token= or Authorization: Bearer)"})
		return
	case credCookie:
		if !v.web.writeGate(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cookie-authenticated writes need a same-origin Origin header or X-Seek-Token"})
			return
		}
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	device := r.Header.Get(voiceDeviceHeader)
	event := r.Header.Get(voiceEventHeader)
	// Both ids share the web conv's charset rule (uuid-shaped, opaque)
	// — anything else can never be a valid ConvID native part.
	if !validConvNative(device) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing/invalid " + voiceDeviceHeader + " (uuid)"})
		return
	}
	if !validConvNative(event) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing/invalid " + voiceEventHeader + " (uuid)"})
		return
	}
	webConv, err := v.web.convFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// One byte over the API cap so the boundary itself is detectable.
	wav, err := io.ReadAll(http.MaxBytesReader(w, r.Body, deepseek.ASRMaxBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "audio must be ≤8 MiB"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
		return
	}
	if len(wav) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audio is empty"})
		return
	}
	// Container magic only — the ASR client stays the format authority
	// (bit depth, duration, codec). This early-out just keeps garbage
	// out of the queue and the failure attributable to the client.
	if !looksLikeVoiceAudio(wav) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audio must be 16-bit PCM WAV or Ogg Opus"})
		return
	}

	select {
	case v.uploads <- voiceUpload{device: device, event: event, webConv: webConv, wav: wav}:
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "event": event})
	case <-r.Context().Done():
		// client gave up; the upload was never accepted
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "voice queue full, try again"})
	}
}

// looksLikeVoiceAudio checks the two container magics the ASR API
// accepts. Local twin of pkg/deepseek's unexported checks — four lines
// beats an exported API nobody else needs.
func looksLikeVoiceAudio(b []byte) bool {
	if bytes.HasPrefix(b, []byte("OggS")) {
		return true
	}
	return len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WAVE"))
}

// ---- SSE payload shapes (named events on the page's web conv) ----

// voiceEchoEvent is the transcript echo (named event "voice"): the
// page renders it as the user's own bubble. Named events are invisible
// to the MVP onmessage handler by SSE design, so older pages ignore
// voice entirely.
type voiceEchoEvent struct {
	Text string `json:"text"`
}

// voiceErrorEvent is the failure face (named event "voice-error"):
// ASR errors and empty transcripts — everything that does not become
// an Inbound.
type voiceErrorEvent struct {
	Error string `json:"error"`
}
