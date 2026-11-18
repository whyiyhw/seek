package controlplane

// V1a speaker tests (feature-deepseek-app-api §4.3): the outbound TTS
// sink exercised end-to-end with zero network — a stub VoiceTTS, a
// stub (or absent) Player, temp dirs. The degrade path (no player →
// wav file IS the delivery) is the card's named sandbox-testable
// acceptance; the sliding-window throttle is the other. Cost tallying
// asserts the §6 serve-side JSONL bookkeeping.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubTTS records synthesized texts and returns canned audio.
type stubTTS struct {
	mu    sync.Mutex
	texts []string
	audio []byte
	err   error
}

func (s *stubTTS) Synthesize(_ context.Context, text string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	if s.err != nil {
		return nil, s.err
	}
	return s.audio, nil
}

func (s *stubTTS) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.texts...)
}

// stubPlayer records played paths; err != nil simulates a broken
// player (the degrade trigger).
type stubPlayer struct {
	mu    sync.Mutex
	paths []string
	err   error
}

func (p *stubPlayer) Play(_ context.Context, path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.paths = append(p.paths, path)
	return p.err
}

func (p *stubPlayer) played() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...)
}

// fakeWave is a stand-in RIFF payload — content is opaque to the
// speaker, but a realistic magic keeps file assertions honest.
var fakeWave = []byte("RIFF\x24\x00\x00\x00WAVEfmt fake-bytes")

// newTestSpeaker builds a speaker over temp dirs with a controllable
// clock. mutate tweaks the config last (override cap / player).
func newTestSpeaker(t *testing.T, tts *stubTTS, mutate func(*SpeakerConfig)) (*Speaker, *atomic.Int64) {
	t.Helper()
	var nowMS atomic.Int64
	nowMS.Store(1_700_000_000_000)
	cfg := SpeakerConfig{
		TTS:       tts,
		Dir:       filepath.Join(t.TempDir(), "voice"),
		UsagePath: filepath.Join(t.TempDir(), "voice-usage.jsonl"),
		Now:       func() time.Time { return time.UnixMilli(nowMS.Load()) },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	sp, err := NewSpeaker(cfg)
	if err != nil {
		t.Fatalf("NewSpeaker: %v", err)
	}
	return sp, &nowMS
}

// usageLines parses the speaker's JSONL cost log.
func usageLines(t *testing.T, sp *Speaker) []voiceUsageLine {
	t.Helper()
	b, err := os.ReadFile(sp.usage)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read usage log: %v", err)
	}
	var lines []voiceUsageLine
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var v voiceUsageLine
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("usage line not JSON: %q: %v", l, err)
		}
		lines = append(lines, v)
	}
	return lines
}

// wavFiles lists the clip dir's wavs; a dir that was never created
// (no broadcast happened) counts as zero.
func wavFiles(t *testing.T, sp *Speaker) []string {
	t.Helper()
	ents, err := os.ReadDir(sp.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read clip dir: %v", err)
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".wav") {
			out = append(out, e.Name())
		}
	}
	return out
}

// ---- acceptance ①: 合成播放, sandbox via the degrade path ----

// No player configured → every broadcast degrades to the wav file:
// bytes on disk, path logged, usage row marked wav_fallback with the
// Han×2 billable count priced. This is the card's "沙盒走降级写 wav
// 路径可测" acceptance.
func TestSpeaker_NoPlayerDegradesToWavFile(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	sp, _ := newTestSpeaker(t, tts, nil) // Player nil → degrade by construction

	if err := sp.Speak(context.Background(), conv1, "任务完成"); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if got := tts.calls(); len(got) != 1 || got[0] != "任务完成" {
		t.Fatalf("tts calls = %v, want [任务完成]", got)
	}
	files := wavFiles(t, sp)
	if len(files) != 1 {
		t.Fatalf("clip dir has %d wavs, want 1 (the delivery)", len(files))
	}
	b, err := os.ReadFile(filepath.Join(sp.dir, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(fakeWave) {
		t.Fatalf("wav content = %q, want the synthesized bytes verbatim", b)
	}

	lines := usageLines(t, sp)
	if len(lines) != 1 {
		t.Fatalf("usage lines = %d, want 1", len(lines))
	}
	// 4 Han runes × 2 = 8 billable chars (汉字算 2), priced > 0.
	if lines[0].BillableChars != 8 {
		t.Errorf("billable_chars = %d, want 8 (Han×2)", lines[0].BillableChars)
	}
	if lines[0].CostUSD <= 0 {
		t.Errorf("cost_usd = %v, want > 0", lines[0].CostUSD)
	}
	if lines[0].Outcome != speakOutcomeFallback {
		t.Errorf("outcome = %q, want %q", lines[0].Outcome, speakOutcomeFallback)
	}
	if lines[0].Conv != string(conv1) {
		t.Errorf("conv = %q, want %q", lines[0].Conv, conv1)
	}
}

// Player present and healthy → the clip is played and REMOVED (the
// file was a means, not the delivery); usage says played.
func TestSpeaker_PlayedRemovesClip(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	player := &stubPlayer{}
	sp, _ := newTestSpeaker(t, tts, func(c *SpeakerConfig) { c.Player = player.Play })

	if err := sp.Speak(context.Background(), conv1, "done"); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if got := player.played(); len(got) != 1 {
		t.Fatalf("player calls = %d, want 1", len(got))
	}
	if files := wavFiles(t, sp); len(files) != 0 {
		t.Errorf("clip dir still holds %d wavs after a played broadcast, want 0", len(files))
	}
	lines := usageLines(t, sp)
	if len(lines) != 1 || lines[0].Outcome != speakOutcomePlayed {
		t.Fatalf("usage = %+v, want one played row", lines)
	}
}

// Player present but FAILING → degrade, not an error: the wav file
// stays as the delivery, Speak returns nil.
func TestSpeaker_PlayerFailureDegrades(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	player := &stubPlayer{err: errors.New("audio device busy")}
	sp, _ := newTestSpeaker(t, tts, func(c *SpeakerConfig) { c.Player = player.Play })

	if err := sp.Speak(context.Background(), conv1, "hello"); err != nil {
		t.Fatalf("Speak after player failure = %v, want nil (degrade is a delivery)", err)
	}
	if files := wavFiles(t, sp); len(files) != 1 {
		t.Fatalf("clip dir has %d wavs, want 1 kept as the delivery", len(files))
	}
	lines := usageLines(t, sp)
	if len(lines) != 1 || lines[0].Outcome != speakOutcomeFallback {
		t.Fatalf("usage = %+v, want one wav_fallback row", lines)
	}
}

// ---- acceptance ②: 节流上限生效 ----

// Cap of 2/conv/min: the third broadcast inside the window is refused
// (ErrSpeakThrottled, no synthesis, no file, no usage row), a different
// conversation is unaffected, and the window sliding past re-allows.
func TestSpeaker_ThrottlePerConvPerMinute(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	sp, nowMS := newTestSpeaker(t, tts, func(c *SpeakerConfig) { c.MaxPerConvPerMin = 2 })

	for range 2 {
		if err := sp.Speak(context.Background(), conv1, "one"); err != nil {
			t.Fatalf("Speak #%d: %v", 1, err)
		}
	}
	err := sp.Speak(context.Background(), conv1, "third")
	if !errors.Is(err, ErrSpeakThrottled) {
		t.Fatalf("third Speak err = %v, want ErrSpeakThrottled", err)
	}
	if got := tts.calls(); len(got) != 2 {
		t.Errorf("tts calls = %d after a throttled drop, want 2", len(got))
	}
	if files := wavFiles(t, sp); len(files) != 2 {
		t.Errorf("clip dir has %d wavs, want 2 (the throttled one never synthesized)", len(files))
	}
	if lines := usageLines(t, sp); len(lines) != 2 {
		t.Errorf("usage lines = %d, want 2", len(lines))
	}

	// A different conversation has its own budget.
	conv2 := NewConvID("feishu", "c2")
	if err := sp.Speak(context.Background(), conv2, "other conv"); err != nil {
		t.Fatalf("Speak on a second conv inside the window: %v", err)
	}

	// Sliding window: 61s later conv1's entries aged out — allowed.
	nowMS.Add(61_000)
	if err := sp.Speak(context.Background(), conv1, "after the window"); err != nil {
		t.Fatalf("Speak after the window slid: %v", err)
	}
	if got := tts.calls(); len(got) != 4 {
		t.Errorf("tts calls = %d, want 4", len(got))
	}
}

// Throttle counts ATTEMPTS: a broadcast whose synthesis fails still
// consumes the window slot (the guard bounds call rate, not only
// billed successes).
func TestSpeaker_ThrottleCountsAttempts(t *testing.T) {
	tts := &stubTTS{err: errors.New("tts down")}
	sp, _ := newTestSpeaker(t, tts, func(c *SpeakerConfig) { c.MaxPerConvPerMin = 1 })

	if err := sp.Speak(context.Background(), conv1, "fails"); err == nil {
		t.Fatal("Speak with a broken TTS should return the error")
	}
	err := sp.Speak(context.Background(), conv1, "second")
	if !errors.Is(err, ErrSpeakThrottled) {
		t.Fatalf("second Speak err = %v, want ErrSpeakThrottled (the failed attempt consumed the slot)", err)
	}
}

// ---- failure / edge faces ----

// Blank text is a silent no-op: no synthesis, no file, no usage row.
func TestSpeaker_BlankTextNoop(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	sp, _ := newTestSpeaker(t, tts, nil)

	for _, text := range []string{"", "   \n\t "} {
		if err := sp.Speak(context.Background(), conv1, text); err != nil {
			t.Fatalf("Speak(%q): %v", text, err)
		}
	}
	if got := tts.calls(); len(got) != 0 {
		t.Errorf("tts calls = %v, want none", got)
	}
	if files := wavFiles(t, sp); len(files) != 0 {
		t.Errorf("clip dir has %d wavs, want 0", len(files))
	}
	if lines := usageLines(t, sp); len(lines) != 0 {
		t.Errorf("usage lines = %d, want 0", len(lines))
	}
}

// TTS failure: error returned (wrapped, conv-attributable), no wav on
// disk, no usage row (only 2xx synths are billed — the S1 contract).
func TestSpeaker_TTSFailure(t *testing.T) {
	tts := &stubTTS{err: errors.New("401 unauthorized")}
	sp, _ := newTestSpeaker(t, tts, nil)

	err := sp.Speak(context.Background(), conv1, "hello")
	if err == nil || !strings.Contains(err.Error(), "401 unauthorized") {
		t.Fatalf("Speak err = %v, want the wrapped TTS error", err)
	}
	if !strings.Contains(err.Error(), string(conv1)) {
		t.Errorf("error %q lacks the conv attribution", err)
	}
	if files := wavFiles(t, sp); len(files) != 0 {
		t.Errorf("clip dir has %d wavs after a failed synth, want 0", len(files))
	}
	if lines := usageLines(t, sp); len(lines) != 0 {
		t.Errorf("usage lines = %d after a failed synth, want 0", len(lines))
	}
}

// Missing TTS is a construction error, not a per-broadcast surprise.
func TestNewSpeaker_RequiresTTS(t *testing.T) {
	if _, err := NewSpeaker(SpeakerConfig{}); err == nil {
		t.Fatal("NewSpeaker without a TTS client should error")
	}
}

// SpeakNotification (the routines sibling adapter): joins title+body,
// keys the throttle by event so concurrent event kinds each get their
// own budget, and swallows throttle drops (the guard working is not an
// error worth stderr).
func TestSpeaker_SpeakNotification(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	sp, _ := newTestSpeaker(t, tts, func(c *SpeakerConfig) { c.MaxPerConvPerMin = 1 })

	sp.SpeakNotification(context.Background(), "cron.completed", "seek cron: job (completed)", "all good")
	// Same event again → throttled silently (no error surface on the
	// adapter; the drop is the guard).
	sp.SpeakNotification(context.Background(), "cron.completed", "seek cron: job (completed)", "again")
	// A different event kind has its own budget.
	sp.SpeakNotification(context.Background(), "trigger.failed", "seek trigger: t (failed)", "boom")

	got := tts.calls()
	if len(got) != 2 {
		t.Fatalf("tts calls = %d, want 2 (second cron.completed throttled)", len(got))
	}
	if want := "seek cron: job (completed) all good"; got[0] != want {
		t.Errorf("spoken text = %q, want %q", got[0], want)
	}
	lines := usageLines(t, sp)
	if len(lines) != 2 {
		t.Fatalf("usage lines = %d, want 2", len(lines))
	}
	if lines[0].Conv != "routines:cron.completed" || lines[1].Conv != "routines:trigger.failed" {
		t.Errorf("usage convs = %q / %q, want routines:<event> keys", lines[0].Conv, lines[1].Conv)
	}
}

// Concurrent speaks (the router calls from per-message goroutines):
// every broadcast lands a unique clip and a usage row — the -race
// detector polices the mutex discipline.
func TestSpeaker_ConcurrentSpeaks(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	player := &stubPlayer{}
	sp, _ := newTestSpeaker(t, tts, func(c *SpeakerConfig) { c.MaxPerConvPerMin = 64; c.Player = player.Play })

	const workers, per = 8, 4
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range per {
				conv := NewConvID("feishu", fmt.Sprintf("c%d", w))
				if err := sp.Speak(context.Background(), conv, fmt.Sprintf("msg %d", i)); err != nil {
					t.Errorf("Speak: %v", err)
				}
			}
		}(w)
	}
	wg.Wait()

	if got := len(tts.calls()); got != workers*per {
		t.Errorf("tts calls = %d, want %d", got, workers*per)
	}
	if got := len(player.played()); got != workers*per {
		t.Errorf("player calls = %d, want %d", got, workers*per)
	}
	if lines := usageLines(t, sp); len(lines) != workers*per {
		t.Errorf("usage lines = %d, want %d", len(lines), workers*per)
	}
}

// Filenames survive hostile conv ids (':' and worse): sanitized, short
// — and never empty.
func TestSpeakNameSafe(t *testing.T) {
	for _, c := range []struct {
		in   ConvID
		want string
	}{
		{"feishu:oc_abc", "feishu_oc_abc"},
		{ConvID(""), "conv"},
		{"a:b/c\\d", "a_b_c_d"},
	} {
		if got := speakNameSafe(c.in); got != c.want {
			t.Errorf("speakNameSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Overlong ids truncate to a bound.
	long := speakNameSafe(ConvID("feishu:" + strings.Repeat("x", 200)))
	if len(long) > 48+8 { // capped + the ".wav"-free prefix slack
		t.Errorf("speakNameSafe length = %d, want bounded", len(long))
	}
}

// ---- router integration: the serve Reply exit speaks ----

// The V1a acceptance at plane level: a bound session's reply goes out
// via Send AND is synthesized+spoken (here through the degrade path —
// no player, wav on disk). The bind ack is a reply too, so both rows
// land. RouterConfig.Speaker nil (every pre-existing router test)
// changes nothing — covered by those suites still passing.
func TestRouter_ReplySpoken(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	sp, _ := newTestSpeaker(t, tts, nil)
	project := t.TempDir()
	r, fe, _, _ := newTestRouter(t, nil, RouterConfig{Speaker: sp})

	r.Handle(context.Background(), inbound("/bind "+project, "e1"))
	r.Handle(context.Background(), inbound("hello world", "e2"))

	sends := fe.sends()
	if len(sends) != 2 || sends[1].Text != "Hello hello world" {
		t.Fatalf("text replies = %+v — the speaker must never eat them", sends)
	}
	got := tts.calls()
	if len(got) != 2 {
		t.Fatalf("spoken texts = %q, want the bind ack + the run reply", got)
	}
	if want := "Hello hello world"; got[1] != want {
		t.Errorf("spoken run reply = %q, want %q", got[1], want)
	}
	// Degrade delivery: one wav per spoken broadcast, both billed.
	if files := wavFiles(t, sp); len(files) != 2 {
		t.Errorf("clip dir has %d wavs, want 2 (degrade path)", len(files))
	}
	if lines := usageLines(t, sp); len(lines) != 2 {
		t.Errorf("usage lines = %d, want 2", len(lines))
	}
}

// Throttle at plane level: with a one-per-minute cap the bind ack's
// speech consumes the slot, the run reply's speech is dropped — and
// the TEXT reply still arrives (audio is the secondary channel).
func TestRouter_ThrottleNeverEatsTextReply(t *testing.T) {
	tts := &stubTTS{audio: fakeWave}
	sp, _ := newTestSpeaker(t, tts, func(c *SpeakerConfig) { c.MaxPerConvPerMin = 1 })
	project := t.TempDir()
	r, fe, _, _ := newTestRouter(t, nil, RouterConfig{Speaker: sp})

	r.Handle(context.Background(), inbound("/bind "+project, "e1"))
	r.Handle(context.Background(), inbound("hello world", "e2"))

	sends := fe.sends()
	if len(sends) != 2 || sends[1].Text != "Hello hello world" {
		t.Fatalf("text replies = %+v — a throttled speak must not eat the reply", sends)
	}
	if got := tts.calls(); len(got) != 1 {
		t.Errorf("spoken texts = %q, want only the first (bind ack) — second throttled", got)
	}
}
