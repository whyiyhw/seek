package controlplane

// Voice lane billing wiring tests (card c_yc2thl): pricing.ASRCost had
// zero production callers — the ASR client (S2) was merged, priced, and
// never billed, while its TTS twin already tallied rows into
// <serve>/voice-usage.jsonl (speaker.go). These tests pin the symmetric
// wiring: one ASR call → exactly one row in the SAME ledger, priced at
// the published per-second rate × the SERVER-reported duration, and the
// two lanes' rows summing into one cost tally.

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ledgerLines parses the voice ledger at path; a missing file (nothing
// billed yet) is zero rows, not a failure.
func ledgerLines(t *testing.T, path string) []voiceUsageLine {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read voice ledger: %v", err)
	}
	var lines []voiceUsageLine
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var v voiceUsageLine
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("ledger line not JSON: %q: %v", l, err)
		}
		lines = append(lines, v)
	}
	return lines
}

// fakeASRBackend is an httptest stand-in for POST /v1/tools/asr that
// reports a FIXED duration_ms — deliberately different from the uploaded
// clip's own header length, so a row that prices the local container
// instead of the server's number fails the assertion.
func fakeASRBackend(t *testing.T, durationMS int64, text string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/asr" {
			t.Errorf("ASR path = %q, want /v1/tools/asr", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("Authorization = %q, want Bearer t", got)
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{"request_id": "req-1", "text": text, "duration_ms": durationMS})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestVoiceLedger builds the real ASR adapter (pkg/deepseek client →
// httptest fake backend) over a temp ledger, with a controllable clock so
// rows are deterministic. The upload path still rides the real web
// handler, exactly like newTestVoice.
func newTestVoiceLedger(t *testing.T, asr VoiceASR, ledgerPath string) (*VoiceFrontend, chan Inbound, string) {
	t.Helper()
	web, _, tok := newTestWeb(t)
	v := NewVoiceAt(asr, ledgerPath)
	v.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	v.Attach(web)

	in := make(chan Inbound, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.Run(ctx, in) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("voice Run returned %v on shutdown, want nil", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("voice Run did not return within 5s of ctx cancel")
		}
	})

	srv := httptest.NewServer(web.handler())
	t.Cleanup(srv.Close)
	return v, in, srv.URL + "/voice?token=" + tok
}

// TestVoice_ASRCallBillsOneRow is the card's acceptance ②: ONE ASR call
// through the real client against a fake backend produces exactly ONE
// billing row, whose amount is the published rate × the billed seconds
// (duration_ms 3200 → 4 whole seconds, ceil internal to pricing).
func TestVoice_ASRCallBillsOneRow(t *testing.T) {
	srv := fakeASRBackend(t, 3200, "今天天气怎么样")
	ledger := filepath.Join(t.TempDir(), "voice-usage.jsonl")
	asr := &DeepSeekVoiceASR{c: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))}
	_, in, url := newTestVoiceLedger(t, asr, ledger)

	if resp := postVoice(t, url, "dev-1", "evt-1", "c1", testWAV(0.5)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	select {
	case m := <-in:
		if m.Text != "今天天气怎么样" {
			t.Errorf("Inbound.Text = %q, want the backend transcript", m.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no Inbound within 5s")
	}

	lines := ledgerLines(t, ledger)
	if len(lines) != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1 for one ASR call", len(lines))
	}
	row := lines[0]
	if row.Kind != voiceKindASR {
		t.Errorf("kind = %q, want %q", row.Kind, voiceKindASR)
	}
	if row.Conv != "voice:dev-1" {
		t.Errorf("conv = %q, want voice:dev-1 (the device's conversation)", row.Conv)
	}
	if row.DurationMS != 3200 {
		t.Errorf("duration_ms = %d, want the SERVER-reported 3200", row.DurationMS)
	}
	if row.Outcome != voiceOutcomeTranscribed {
		t.Errorf("outcome = %q, want %q", row.Outcome, voiceOutcomeTranscribed)
	}
	// 3200 ms → 4 whole billed seconds ($0.116/h, ceil internal).
	perSec := 0.116 / 3600
	if want := 4 * perSec; math.Abs(row.CostUSD-want) > 1e-15 {
		t.Errorf("cost_usd = %v, want 4 billed seconds = %v", row.CostUSD, want)
	}
	if want := pricing.ASRCost(3200.0 / 1000); math.Abs(row.CostUSD-want) > 1e-15 {
		t.Errorf("cost_usd = %v, want pricing.ASRCost(duration_ms/1000) = %v", row.CostUSD, want)
	}
	if !row.TS.Equal(time.UnixMilli(1_700_000_000_000)) {
		t.Errorf("ts = %v, want the injected clock", row.TS)
	}
}

// TestVoice_EachASRCallBillsItsOwnRow: two presses → two rows, each with
// its own server-reported duration (the ledger is append-only per call,
// never a folded counter).
func TestVoice_EachASRCallBillsItsOwnRow(t *testing.T) {
	srvN := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srvN++
		w.Header().Set("Content-Type", "application/json")
		// 1.0s then 2.5s → 1 then 3 billed seconds.
		dur := 1000
		if srvN > 1 {
			dur = 2500
		}
		_, _ = fmt.Fprintf(w, `{"request_id":"r%d","text":"好","duration_ms":%d}`, srvN, dur)
	}))
	defer srv.Close()

	ledger := filepath.Join(t.TempDir(), "voice-usage.jsonl")
	asr := &DeepSeekVoiceASR{c: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))}
	_, in, url := newTestVoiceLedger(t, asr, ledger)

	for i, event := range []string{"evt-1", "evt-2"} {
		if resp := postVoice(t, url, "dev-1", event, "c1", testWAV(0.5)); resp.StatusCode != http.StatusAccepted {
			t.Fatalf("upload %d = %d, want 202", i, resp.StatusCode)
		}
		select {
		case <-in:
		case <-time.After(5 * time.Second):
			t.Fatalf("no Inbound for upload %d within 5s", i)
		}
	}

	lines := ledgerLines(t, ledger)
	if len(lines) != 2 {
		t.Fatalf("ledger rows = %d, want 2 (one per ASR call)", len(lines))
	}
	perSec := 0.116 / 3600
	for i, wantBilled := range []float64{1, 3} {
		if lines[i].DurationMS != int64([]int{1000, 2500}[i]) {
			t.Errorf("row %d duration_ms = %d, want the server's own number", i, lines[i].DurationMS)
		}
		if want := wantBilled * perSec; math.Abs(lines[i].CostUSD-want) > 1e-15 {
			t.Errorf("row %d cost_usd = %v, want %v (%v billed s)", i, lines[i].CostUSD, want, wantBilled)
		}
	}
}

// TestVoice_ASRFailureNotBilled: only a 2xx transcription is billable
// (the ASR client's contract). A failed call → the voice-error face and
// NO ledger row.
func TestVoice_ASRFailureNotBilled(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "voice-usage.jsonl")
	asr := &stubVoiceASR{err: fmt.Errorf("boom (500 after 2 attempts)")}
	_, in, url := newTestVoiceLedger(t, asr, ledger)

	if resp := postVoice(t, url, "dev-1", "evt-1", "c1", testWAV(0.1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for asr.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case m := <-in:
		t.Errorf("failed transcription must not become an Inbound, got %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
	if lines := ledgerLines(t, ledger); len(lines) != 0 {
		t.Errorf("ledger rows = %+v, want 0 after a failed ASR call", lines)
	}
}

// TestVoice_EmptyTranscriptStillBilled: a 2xx that recognised nothing is
// still a spend — the row lands with outcome empty_transcript even though
// the user gets the voice-error face and no Inbound.
func TestVoice_EmptyTranscriptStillBilled(t *testing.T) {
	srv := fakeASRBackend(t, 1500, "   ")
	ledger := filepath.Join(t.TempDir(), "voice-usage.jsonl")
	asr := &DeepSeekVoiceASR{c: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))}
	_, in, url := newTestVoiceLedger(t, asr, ledger)

	if resp := postVoice(t, url, "dev-1", "evt-1", "c1", testWAV(0.1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(ledgerLines(t, ledger)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	lines := ledgerLines(t, ledger)
	if len(lines) != 1 {
		t.Fatalf("ledger rows = %d, want 1 (the 2xx was billed)", len(lines))
	}
	if lines[0].Outcome != voiceOutcomeEmpty {
		t.Errorf("outcome = %q, want %q", lines[0].Outcome, voiceOutcomeEmpty)
	}
	if want := pricing.ASRCost(1500.0 / 1000); math.Abs(lines[0].CostUSD-want) > 1e-15 {
		t.Errorf("cost_usd = %v, want %v (1500ms → 2 whole seconds)", lines[0].CostUSD, want)
	}
	select {
	case m := <-in:
		t.Errorf("empty transcript must not become an Inbound, got %+v", m)
	default:
	}
}

// TestVoice_DefaultLedgerUnderServeDir: the production constructor
// (NewVoice — what servecli wires) resolves its ledger lazily at Run to
// <SEEK_HOME>/serve/voice-usage.jsonl, the SAME file the speaker writes
// TTS rows into. SEEK_HOME keeps the test off the real host dir.
func TestVoice_DefaultLedgerUnderServeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	web, _, tok := newTestWeb(t)
	v := NewVoice(&stubVoiceASR{text: "好"}) // no explicit path: the production shape
	v.Attach(web)

	in := make(chan Inbound, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- v.Run(ctx, in) }()
	defer func() {
		cancel()
		<-done
	}()
	srv := httptest.NewServer(web.handler())
	defer srv.Close()

	if resp := postVoice(t, srv.URL+"/voice?token="+tok, "dev-1", "evt-1", "c1", testWAV(0.1)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	select {
	case <-in:
	case <-time.After(5 * time.Second):
		t.Fatal("no Inbound within 5s")
	}

	lines := ledgerLines(t, filepath.Join(home, "serve", voiceUsageFile))
	if len(lines) != 1 {
		t.Fatalf("rows in $SEEK_HOME/serve/%s = %d, want 1", voiceUsageFile, len(lines))
	}
	if lines[0].Kind != voiceKindASR || lines[0].CostUSD <= 0 {
		t.Errorf("row = %+v, want a priced ASR row", lines[0])
	}
}

// TestAppendVoiceUsageLine_CreatesParentDir pins the pitfall's fix: the
// helper owns its parent directory. O_APPEND|O_CREATE creates the FILE
// only, so before the fix the first-ever row on a host (nothing had yet
// written <serve>/) died ENOENT into a stderr line nobody reads.
func TestAppendVoiceUsageLine_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve", "nested", voiceUsageFile) // neither dir exists
	if err := appendVoiceUsageLine(path, voiceUsageLine{
		TS:      time.UnixMilli(1_700_000_000_000),
		Kind:    voiceKindASR,
		Conv:    "voice:dev-1",
		CostUSD: pricing.ASRCost(1),
		Outcome: voiceOutcomeTranscribed,
	}); err != nil {
		t.Fatalf("append into a not-yet-existing dir tree: %v", err)
	}
	if lines := ledgerLines(t, path); len(lines) != 1 {
		t.Fatalf("rows = %d, want the one just appended", len(lines))
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("parent dir mode = %v, want 0700 (the serve dir's mode)", perm)
	}
}

// TestVoice_SharedLedgerSumsBothLanes: TTS broadcasts (speaker.go) and
// ASR transcriptions write ONE file — the §6 tally is a sum over
// cost_usd, so both kinds must coexist and parse as one stream.
func TestVoice_SharedLedgerSumsBothLanes(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "voice-usage.jsonl")

	sp, err := NewSpeaker(SpeakerConfig{
		TTS:       &stubTTS{audio: fakeWave},
		Dir:       filepath.Join(t.TempDir(), "voice"),
		UsagePath: ledger,
		Now:       func() time.Time { return time.UnixMilli(1_700_000_000_000) },
	})
	if err != nil {
		t.Fatalf("NewSpeaker: %v", err)
	}
	if err := sp.Speak(context.Background(), NewConvID("feishu", "c1"), "任务完成"); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	srv := fakeASRBackend(t, 3200, "好")
	asr := &DeepSeekVoiceASR{c: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))}
	_, in, url := newTestVoiceLedger(t, asr, ledger)
	if resp := postVoice(t, url, "dev-1", "evt-1", "c1", testWAV(0.5)); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d, want 202", resp.StatusCode)
	}
	select {
	case <-in:
	case <-time.After(5 * time.Second):
		t.Fatal("no Inbound within 5s")
	}

	lines := ledgerLines(t, ledger)
	if len(lines) != 2 {
		t.Fatalf("ledger rows = %d, want 2 (one tts + one asr)", len(lines))
	}
	kinds := map[string]int{}
	var total float64
	for _, l := range lines {
		kinds[l.Kind]++
		total += l.CostUSD
	}
	if kinds[voiceKindTTS] != 1 || kinds[voiceKindASR] != 1 {
		t.Fatalf("kinds = %v, want one tts + one asr row", kinds)
	}
	// 4 Han runes × 2 = 8 billable chars + 4 billed ASR seconds.
	want := pricing.TTSCost(8) + pricing.ASRCost(3.2)
	if math.Abs(total-want) > 1e-15 {
		t.Errorf("summed cost = %v, want %v (tts + asr)", total, want)
	}
}
