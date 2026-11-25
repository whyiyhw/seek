package controlplane

// speaker.go — the v1 outbound TTS broadcast sink (feature-deepseek-
// app-api §4.3 card V1a): the serve plane's Reply exit, synthesized to
// speech and played on the serve host's own audio out ("离开电脑也能
// 收" — the same audience sentence as mobile-push, through speakers
// instead of a push channel). Flow: Reply.Text → TTS (S1 client, wav)
// → play — macOS `afplay`, Linux `paplay`|`aplay`; a host with no
// player (or a player that fails) DEGRADES to the wav file itself as
// the delivery: it stays on disk under <serve>/voice/ and its path is
// logged. Never fatal, and never delays the text reply that already
// went out through Send.
//
// Cost guard (§4.3: serve is a resident surface, "不靠价格自觉"): a
// plane-layer sliding-window throttle caps broadcasts per conversation
// per minute. Broadcasts are additionally tallied in
// <serve>/voice-usage.jsonl — one JSONL line per billed synthesis
// (billable chars via deepseek.TTSBillableChars, USD via
// pricing.TTSCost), the §6 serve-side bookkeeping note made real: the
// serve process has no session Tracker, so usage lands next to
// bindings.json under the same dir discipline.
//
// V1b (next card) adds the serial queue, the voice.enabled switch and
// long-text truncation; this card ships the sink itself. Production
// enablement is deliberately NOT wired into servecli here — no config
// surface exists until V1b owns it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// VoiceTTS is the plane's narrow TTS seam, twin of VoiceASR: text in,
// one audio clip out. Production is the S1 adapter below; tests stub
// it. Keeping the seam inside controlplane makes the full speak path
// (text → synth → wav file → play/fallback) testable with zero
// network — the acceptance bar of card V1a.
type VoiceTTS interface {
	Synthesize(ctx context.Context, text string) (audio []byte, err error)
}

// DeepSeekVoiceTTS adapts pkg/deepseek's TTS client (S1, card c_k4iejt)
// onto VoiceTTS, always requesting a self-describing WAV container —
// the player chain (afplay/paplay/aplay) and the file fallback both
// consume file-shaped audio. The client owns every constraint (≤10,000
// billing chars, voice/format validation, transient retry); the
// adapter adds nothing. A missing key surfaces per broadcast as a
// speak failure — the plane keeps serving, the text reply already went
// out. Long replies over the char cap fail here in V1a (dropped audio,
// logged); truncation is V1b's card.
type DeepSeekVoiceTTS struct{ c *deepseek.Client }

// NewDeepSeekVoiceTTS builds the adapter against the default DeepSeek
// endpoint with the environment API key (deepseek.New's own fallback).
func NewDeepSeekVoiceTTS() *DeepSeekVoiceTTS {
	return &DeepSeekVoiceTTS{c: deepseek.New()}
}

// Synthesize implements VoiceTTS.
func (t *DeepSeekVoiceTTS) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return t.c.TTS(ctx, &deepseek.TTSRequest{Text: text, Format: deepseek.TTSAudioWAV})
}

// Player plays one wav file, blocking until the audio finished (or the
// broadcast's ctx died — implementations should use it so drain-time
// cancellation kills the audio too). Returning an error degrades the
// broadcast to the wav file itself; that degrade is part of the
// contract, not an afterthought.
type Player func(ctx context.Context, wavPath string) error

// DefaultPlayer picks the host's player: macOS `afplay`, Linux
// `paplay` (PulseAudio) then `aplay` (ALSA) — the V1a card's platform
// list. A nil return means "no player on this host" and every
// broadcast degrades to the wav file (Windows / exotic platforms
// today; tests force the same posture via SpeakerConfig.Player = nil,
// which is how the card's 沙盒降级 path stays deterministic).
func DefaultPlayer() Player {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("afplay"); err == nil {
			return cmdPlayer("afplay")
		}
	case "linux":
		for _, name := range []string{"paplay", "aplay"} {
			if _, err := exec.LookPath(name); err == nil {
				return cmdPlayer(name)
			}
		}
	}
	return nil
}

// cmdPlayer builds the Player for one player command — `cmd <file>`,
// ctx-aware, CombinedOutput on failure so the stderr of a confused
// player is attributable.
func cmdPlayer(name string) Player {
	return func(ctx context.Context, wavPath string) error {
		out, err := exec.CommandContext(ctx, name, wavPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %v: %s", name, err, bytes.TrimSpace(out))
		}
		return nil
	}
}

// defaultSpeakPerMin is the per-conversation broadcast cap per sliding
// minute. Three: a spoken reply is ~10s of audio; more than three a
// minute is a runaway loop, not a conversation (§4.3 "每会话每分钟播
// 报上限"). Caller-tunable via SpeakerConfig.MaxPerConvPerMin.
const defaultSpeakPerMin = 3

// speakWindow is the throttle's sliding window length.
const speakWindow = time.Minute

// speakConvCap bounds the throttle's per-conv map. Conversations are
// user-scale, but a flood of fresh conv ids would otherwise grow the
// map forever; at the cap it wipes wholesale (the voiceDeviceCap
// posture — crude, bounded, self-re-registering).
const speakConvCap = 4096

// Speak outcomes recorded in the usage log.
const (
	speakOutcomePlayed   = "played"
	speakOutcomeFallback = "wav_fallback"
)

// ErrSpeakThrottled marks a broadcast dropped by the per-conversation
// per-minute cap. Callers log it like any best-effort failure, but it
// is the guard WORKING, not a malfunction.
var ErrSpeakThrottled = errors.New("controlplane: speak throttled (per-conv per-minute cap)")

// SpeakerConfig tunes a Speaker. TTS is required; the rest default.
type SpeakerConfig struct {
	// TTS synthesizes text to wav bytes. Required — NewSpeaker errors
	// on nil rather than failing per broadcast.
	TTS VoiceTTS
	// Player plays the synthesized wav. nil (the default) forces the
	// write-wav-file degrade path for EVERY broadcast. Production
	// passes DefaultPlayer().
	Player Player
	// Dir is where wav clips land — synthesis always round-trips
	// through a file because afplay/paplay/aplay consume paths. Empty
	// → <serve>/voice via paths.ServeDir. Files of successful plays
	// are removed; degraded ones stay as the delivery.
	Dir string
	// MaxPerConvPerMin caps broadcasts per conversation per sliding
	// minute — the plane-layer cost guard. <=0 → defaultSpeakPerMin.
	MaxPerConvPerMin int
	// UsagePath is the append-JSONL cost log (one line per billed
	// synthesis). Empty → <serve>/voice-usage.jsonl. Usage logging is
	// best-effort: a failure WARNs and never fails the broadcast.
	UsagePath string
	// Now overrides the clock (throttle windowing) in tests. Nil →
	// time.Now.
	Now func() time.Time
}

// Speaker is the plane's outbound-voice sink: Speak one Reply, one
// routine notification — anything textual — as synthesized audio on
// the serve host. Safe for concurrent use: the router calls Speak
// from per-message handler goroutines.
type Speaker struct {
	tts    VoiceTTS
	player Player
	dir    string
	maxPer int
	usage  string
	now    func() time.Time

	mu     sync.Mutex // guards recent + the usage append below
	recent map[ConvID][]time.Time

	seq atomic.Uint64 // wav filename uniqueness across concurrent speaks
}

// NewSpeaker resolves SpeakerConfig's defaults (dir, cap, usage path
// against paths.ServeDir) and returns the sink, or an error when the
// config is unusable (nil TTS, unresolvable default dir). Tests
// inject Dir/UsagePath/Now and stay off the host-global serve dir.
func NewSpeaker(cfg SpeakerConfig) (*Speaker, error) {
	if cfg.TTS == nil {
		return nil, errors.New("controlplane: speaker: no TTS client")
	}
	dir := cfg.Dir
	usage := cfg.UsagePath
	if dir == "" || usage == "" {
		serveDir, err := paths.ServeDir()
		if err != nil {
			return nil, fmt.Errorf("controlplane: speaker: resolve serve dir: %w", err)
		}
		if dir == "" {
			dir = filepath.Join(serveDir, "voice")
		}
		if usage == "" {
			usage = filepath.Join(serveDir, voiceUsageFile)
		}
	}
	maxPer := cfg.MaxPerConvPerMin
	if maxPer <= 0 {
		maxPer = defaultSpeakPerMin
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Speaker{
		tts:    cfg.TTS,
		player: cfg.Player,
		dir:    dir,
		maxPer: maxPer,
		usage:  usage,
		now:    now,
		recent: make(map[ConvID][]time.Time),
	}, nil
}

// Speak synthesizes and plays one broadcast for conv. Best-effort
// primitive: the text answer has already gone out through Send by the
// time the router calls this, so every failure here loses only the
// AUDIO, never the reply. Blank text is a silent no-op. Throttled
// broadcasts return ErrSpeakThrottled; synthesis failures return the
// TTS error; a missing/failing player is NOT an error — the wav file
// stays on disk as the delivery and the outcome is logged.
func (s *Speaker) Speak(ctx context.Context, conv ConvID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil // nothing to say is not a failure
	}
	if !s.allow(conv) {
		return ErrSpeakThrottled
	}
	audio, err := s.tts.Synthesize(ctx, text)
	if err != nil {
		return fmt.Errorf("speak %s: tts: %w", conv, err)
	}
	path, err := s.writeClip(conv, audio)
	if err != nil {
		return fmt.Errorf("speak %s: %w", conv, err)
	}
	outcome := speakOutcomeFallback
	if s.player != nil && s.player(ctx, path) == nil {
		outcome = speakOutcomePlayed
		_ = os.Remove(path) // played: the file was a means, not the delivery
	} else {
		// No player, or the player failed (a ctx kill included) — the
		// wav file IS the delivery: keep it, say where it is.
		fmt.Fprintf(os.Stderr, "controlplane: speaker: %s: no player or playback failed — wav written: %s\n", conv, path)
	}
	s.logUsage(conv, text, outcome)
	if outcome == speakOutcomePlayed {
		fmt.Fprintf(os.Stderr, "controlplane: speaker: %s: played %d billable chars\n", conv, deepseek.TTSBillableChars(text))
	}
	return nil
}

// SpeakNotification adapts the Speaker onto the routines sibling
// shape — (ctx, event, title, body), the WebhookDispatcher precedent
// (feature-deepseek-app-api §4.3 V1a): Go func types are structural,
// so `speaker.SpeakNotification` satisfies routines.SpeakerDispatcher
// with no import in either direction. The event ("cron.completed",
// "trigger.failed") becomes the throttle key, so a fleet of jobs
// finishing in one minute still respects the per-minute cap. Wiring
// this into `seek cron tick` is V1b's (voice.enabled owns
// enablement); this adapter exists so that wiring is one assignment.
func (s *Speaker) SpeakNotification(ctx context.Context, event, title, body string) {
	text := strings.TrimSpace(title + " " + body)
	if err := s.Speak(ctx, NewConvID("routines", event), text); err != nil && !errors.Is(err, ErrSpeakThrottled) {
		fmt.Fprintf(os.Stderr, "controlplane: speaker: notification %s: %v\n", event, err)
	}
}

// allow applies the sliding-window cap and, when granted, records the
// attempt. ATTEMPTS count, not only billed successes: the guard bounds
// broadcast rate — and thus worst-case spend — whatever the API does
// with each call.
func (s *Speaker) allow(conv ConvID) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.recent[conv]
	fresh := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < speakWindow {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= s.maxPer {
		s.recent[conv] = fresh
		return false
	}
	if len(s.recent) >= speakConvCap {
		s.recent = make(map[ConvID][]time.Time) // cap reached: wipe, see speakConvCap
	}
	s.recent[conv] = append(fresh, now)
	return true
}

// writeClip persists one synthesized clip under dir with a unique,
// conv-tagged filename (0600). Synthesis always lands in a file: the
// players consume paths and the degrade path needs the file anyway.
func (s *Speaker) writeClip(conv ConvID, audio []byte) (string, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", s.dir, err)
	}
	name := fmt.Sprintf("speak-%s-%d-%06d.wav", speakNameSafe(conv), s.now().Unix(), s.seq.Add(1))
	path := filepath.Join(s.dir, name)
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// speakNameSafe reduces a ConvID to filename-safe bytes — the ':' of
// "<frontend>:<native>" is not portable in filenames — capped short.
func speakNameSafe(c ConvID) string {
	var b strings.Builder
	for _, r := range string(c) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 48 {
			break
		}
	}
	if b.Len() == 0 {
		return "conv"
	}
	return b.String()
}

// voiceUsageLine is one row of voice-usage.jsonl — the §6 serve-side
// bookkeeping: the serve process has no session Tracker, so TTS spend
// is tallied locally instead of forced into session cost. The row shape
// (and the ASR sibling that shares the file) lives in voiceusage.go.

// logUsage appends one billed-broadcast row. Best-effort by contract:
// an open/write failure WARNs to stderr and never fails the broadcast
// that already happened.
func (s *Speaker) logUsage(conv ConvID, text, outcome string) {
	chars := deepseek.TTSBillableChars(text)
	row := voiceUsageLine{
		TS:            s.now().UTC(),
		Kind:          voiceKindTTS,
		Conv:          string(conv),
		BillableChars: chars,
		CostUSD:       pricing.TTSCost(chars),
		Outcome:       outcome,
	}
	// The mutex serialises this speaker's own appends (concurrent speaks
	// land rows in mint order); the cross-writer ordering with the voice
	// frontend is O_APPEND's business, not ours.
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := appendVoiceUsageLine(s.usage, row); err != nil {
		fmt.Fprintf(os.Stderr, "controlplane: speaker: usage log: %v\n", err)
	}
}
