package main

// judge_test.go — the CLI half of card c_jdo7rp (PRD
// docs/prd/feature-judge-engine.md §3.1 command shape + §3.4 exit codes).
//
// Two layers:
//
//   - parseJudgeFlags drives the flag surface directly (no subprocess):
//     the accepted shape, the REQUIRED --rubric, the --parallel ceiling,
//     unknown flags, and the positional-mixing trap the autopilot lane
//     documented (flag.Parse stops at the first positional).
//   - the E2E tests compile the real binary and run it against an
//     OpenAI-compatible fake backend under a hermetic SEEK_HOME, which is
//     where exit codes, line integrity and the no-MCP-fork guarantee can
//     actually be observed.
//
// judgeStream itself has two direct tests (cancellation and a write
// failure) because those paths are cheap to reach in-process and
// expensive to provoke through a subprocess.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/judge"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ---- flag shape ------------------------------------------------------

// TestParseJudgeFlags_Table pins the command shape the PRD publishes:
// `seek judge --rubric x` must parse, and every ambiguity must be a
// usage error rather than a silent reinterpretation.
func TestParseJudgeFlags_Table(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		// errHint must appear on stderr when wantErr (a usage error that
		// does not say what is wrong is indistinguishable from a crash).
		errHint string
		check   func(t *testing.T, o judgeOptions)
	}{
		{
			name: "minimal shape",
			args: []string{"--rubric", "rubric.md"},
			check: func(t *testing.T, o judgeOptions) {
				if o.rubric != "rubric.md" {
					t.Fatalf("rubric = %q", o.rubric)
				}
				// "-" defaults are the PRD §3.1 stdin/stdout shape.
				if o.in != "-" || o.out != "-" {
					t.Fatalf("in/out = %q/%q, want -/-", o.in, o.out)
				}
				if o.parallel != judgeDefaultParallel {
					t.Fatalf("parallel = %d, want %d", o.parallel, judgeDefaultParallel)
				}
			},
		},
		{
			name: "full shape",
			args: []string{
				"--rubric", "r.md", "--in", "items.jsonl", "--out", "verdicts.jsonl",
				"--provider", "compatible", "--model", "m", "--parallel", "8",
			},
			check: func(t *testing.T, o judgeOptions) {
				if o.in != "items.jsonl" || o.out != "verdicts.jsonl" {
					t.Fatalf("in/out = %q/%q", o.in, o.out)
				}
				if o.parallel != 8 || o.model != "m" || o.provider != "compatible" {
					t.Fatalf("opts = %+v", o)
				}
			},
		},
		{
			name:    "missing rubric",
			args:    []string{"--in", "x.jsonl"},
			wantErr: true,
			errHint: "--rubric is required",
		},
		{
			name:    "parallel above ceiling",
			args:    []string{"--rubric", "r", "--parallel", "17"},
			wantErr: true,
			errHint: fmt.Sprintf("between 1 and %d", judgeMaxParallel),
		},
		{
			name:    "parallel zero",
			args:    []string{"--rubric", "r", "--parallel", "0"},
			wantErr: true,
			errHint: "between 1 and",
		},
		{
			// The flag this card deliberately does NOT wire (later card
			// §6-6): it must die as an unknown flag, exit 2, not silently
			// convert the run into one with no isolation.
			name:    "unknown flag isolated",
			args:    []string{"--rubric", "r", "--isolated"},
			wantErr: true,
			errHint: "not defined",
		},
		{
			// --max-cost IS wired (§6 item 7, c_qjuags): a positive USD
			// amount parses and records the cap. No cap is 0.
			name: "max cost accepted",
			args: []string{"--rubric", "r", "--max-cost", "0.05"},
			check: func(t *testing.T, o judgeOptions) {
				if o.maxCost != 0.05 {
					t.Fatalf("maxCost = %v, want 0.05", o.maxCost)
				}
			},
		},
		{
			// The autopilot-lane trap: a positional makes flag.Parse stop,
			// so `--rubric y` behind it would never be seen. Rejecting is
			// the only honest reading — judging a batch the caller did not
			// describe is worse than refusing.
			name:    "positional mixed in",
			args:    []string{"items.jsonl", "--rubric", "r"},
			wantErr: true,
			errHint: "unexpected positional",
		},
		{
			name:    "positional after flags",
			args:    []string{"--rubric", "r", "items.jsonl"},
			wantErr: true,
			errHint: "unexpected positional",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			opts, err := parseJudgeFlags(tc.args, &stderr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parse accepted %v (opts %+v)", tc.args, opts)
				}
				if !errors.Is(err, errJudgeUsage) {
					t.Errorf("error = %v, want the errJudgeUsage sentinel", err)
				}
				if tc.errHint != "" && !strings.Contains(stderr.String(), tc.errHint) {
					t.Errorf("stderr misses %q:\n%s", tc.errHint, stderr.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("parse rejected %v: %v\n%s", tc.args, err, stderr.String())
			}
			if tc.check != nil {
				tc.check(t, opts)
			}
		})
	}
}

// TestJudgeHelp_DocumentsExitCodes: the exit-code contract is the
// lane's published CI surface, so `seek judge help` must state it.
func TestJudgeHelp_DocumentsExitCodes(t *testing.T) {
	var out, errBuf bytes.Buffer
	if err := runJudge([]string{"help"}, strings.NewReader(""), &out, &errBuf); err != nil {
		t.Fatalf("judge help: %v\n%s", err, errBuf.String())
	}
	text := out.String()
	for _, want := range []string{"seek judge", "--rubric", "--parallel", "--max-cost", "Exit codes", "1  the file is complete", "2  usage", "3  --max-cost reached"} {
		if !strings.Contains(text, want) {
			t.Errorf("help is missing %q:\n%s", want, text)
		}
	}
}

// ---- fake backend ----------------------------------------------------

// judgeBackend is an OpenAI-compatible SSE backend that answers each
// judging call with a synthetic verdict derived from the ENTRY TEXT, so
// concurrent calls can be attributed without relying on arrival order.
//
// Marker contract (the test inputs embed them):
//
//	ITEM<id>-KEEP / -DROP / -GRAY  → the matching verdict
//	ITEM<id>-BAD                   → prose, i.e. an unusable reply
type judgeBackend struct {
	srv *httptest.Server

	mu          sync.Mutex
	requests    int
	inFlight    int
	maxInFlight int
	bodies      [][]byte
	// delay widens the window in which concurrent calls overlap, so the
	// --parallel bound is observable rather than timing-lucky.
	delay time.Duration
	// usageOverride, when set, replaces the (tiny) default usage in the
	// finish chunk. Budget tests use it to price a single call above the
	// cap so the stop is deterministic (acceptance ①/②, card c_qjuags).
	usageOverride *deepseek.Usage
}

func newJudgeBackend(t *testing.T, delay time.Duration) *judgeBackend {
	t.Helper()
	b := &judgeBackend{delay: delay}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		b.mu.Lock()
		b.requests++
		b.bodies = append(b.bodies, body)
		b.inFlight++
		if b.inFlight > b.maxInFlight {
			b.maxInFlight = b.inFlight
		}
		delay := b.delay
		b.mu.Unlock()

		defer func() {
			b.mu.Lock()
			b.inFlight--
			b.mu.Unlock()
		}()

		if delay > 0 {
			time.Sleep(delay)
		}

		content := `{"verdict":"gray","reasons":["no marker"]}`
		switch {
		case strings.Contains(string(body), "-KEEP"):
			content = `{"verdict":"keep","reasons":["matches the rubric"]}`
		case strings.Contains(string(body), "-DROP"):
			content = `{"verdict":"drop","reasons":["off topic"]}`
		case strings.Contains(string(body), "-GRAY"):
			content = `{"verdict":"gray","reasons":["cannot tell"]}`
		case strings.Contains(string(body), "-BAD"):
			// Prose with no decodable verdict — the reply failure the
			// kernel refuses to guess at.
			content = `Sure! I would keep this one, it looks interesting.`
		}
		writeSSEJSONUsage(t, w, content, b.usageOverride)
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// budgetUsage prices ONE call well above the test caps, so the budget
// gate trips on the first emitted row regardless of peak/off-peak tier.
// prompt_tokens alone is enough: priceUsage treats the un-split remainder
// as cache MISS (the expensive side) — see internal/judge.priceUsage.
var budgetUsage = &deepseek.Usage{
	PromptTokens:     1_000_000,
	CompletionTokens: 100_000,
	TotalTokens:      1_100_000,
}

// writeSSEJSON emits the minimal OpenAI-compatible stream: one content
// delta, a finish chunk carrying usage, then [DONE].
func writeSSEJSON(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	writeSSEJSONUsage(t, w, content, nil)
}

// writeSSEJSONUsage is writeSSEJSON with an optional usage block. A nil
// override keeps the tiny default (10/2/12) every non-budget test relies on.
func writeSSEJSONUsage(t *testing.T, w http.ResponseWriter, content string, u *deepseek.Usage) {
	t.Helper()
	quoted, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	pt, ct, tt := 10, 2, 12
	if u != nil {
		pt, ct, tt = u.PromptTokens, u.CompletionTokens, u.TotalTokens
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n", quoted)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", pt, ct, tt)
	io.WriteString(w, "data: [DONE]\n\n")
}

func (b *judgeBackend) stats() (requests, maxInFlight int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests, b.maxInFlight
}

func (b *judgeBackend) capturedBodies() [][]byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]byte(nil), b.bodies...)
}

// ---- e2e harness -----------------------------------------------------

// runJudgeE2E runs `seek judge …` in a hermetic env: SEEK_HOME and HOME
// both point at workDir, so no MCP config / skills / project-md / git
// state can leak in from this machine. extra rides after the fixed
// provider flags; stdinData feeds the process's stdin.
func runJudgeE2E(t *testing.T, backendURL, workDir string, stdinData string, extra ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	bin := buildSeekBin(t)
	args := append([]string{
		"judge",
		"--provider", "compatible",
		"--base-url", backendURL + "/v1/chat/completions",
		"--provider-name", "E2E",
		"--model", "e2e-model",
	}, extra...)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"SEEK_HOME="+workDir,
		"HOME="+workDir,
		"OPENAI_API_KEY=e2e-key",
		"SEEK_NO_WEBFETCH=1",
		"SEEK_NO_WEBSEARCH=1",
		"SEEK_AUTO_DISTILL=0",
	)
	if stdinData != "" {
		cmd.Stdin = strings.NewReader(stdinData)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run seek judge: %v\nstderr:\n%s", err, errBuf.String())
	}
	return outBuf.String(), errBuf.String(), code
}

// writeRubric lands a caller-owned rubric inside the hermetic workDir.
func writeRubric(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "rubric.md")
	if err := os.WriteFile(path, []byte("# Rubric\n\nKeep entries that cite a source. Drop the rest. Gray when unsure.\n"), 0o644); err != nil {
		t.Fatalf("write rubric: %v", err)
	}
	return path
}

// readVerdictLines parses a JSONL blob into per-line raw objects, failing
// on any line that is not valid JSON (acceptance ③: no interleaved half
// lines under --parallel>1).
func readVerdictLines(t *testing.T, out string) []map[string]json.RawMessage {
	t.Helper()
	lines := make([]map[string]json.RawMessage, 0)
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is not valid JSON (half line / interleaving):\n%s", n, line)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Fatalf("line %d: %v", n, err)
		}
		lines = append(lines, obj)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan output: %v", err)
	}
	return lines
}

func metaOf(t *testing.T, lines []map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	last := lines[len(lines)-1]
	raw, ok := last["_meta"]
	if !ok {
		t.Fatalf("last line is not _meta: %v", last)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode _meta: %v", err)
	}
	return m
}

func fieldStr(t *testing.T, obj map[string]json.RawMessage, key string) string {
	t.Helper()
	if raw, ok := obj[key]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("field %q: %v", key, err)
		}
		return s
	}
	return ""
}

func fieldInt(t *testing.T, obj map[string]json.RawMessage, key string) int {
	t.Helper()
	var n int
	if raw, ok := obj[key]; ok {
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Fatalf("field %q: %v", key, err)
		}
	}
	return n
}

func fieldFloat(t *testing.T, obj map[string]json.RawMessage, key string) float64 {
	t.Helper()
	var n float64
	if raw, ok := obj[key]; ok {
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Fatalf("field %q: %v", key, err)
		}
	}
	return n
}

// ---- acceptance ①: happy batch + partial failure ----------------------

// TestJudgeE2E_ThreeItemsExitZero: 3 input lines → 3 verdict rows + a
// trailing _meta, exit 0. Also pins the id echo (the caller joins on id,
// PRD §3.3 invariant 1) and the rubric fingerprint.
func TestJudgeE2E_ThreeItemsExitZero(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0)

	in := strings.Join([]string{
		`{"id":"a1","text":"ITEM1-KEEP with meta","meta":{"source":"hn"}}`,
		`{"id":"a2","text":"ITEM2-DROP"}`,
		`{"id":"a3","text":"ITEM3-GRAY"}`,
	}, "\n") + "\n"
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in, "--rubric", rubric)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr)
	}

	lines := readVerdictLines(t, stdout)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 3 rows + _meta\nstdout:\n%s", len(lines), stdout)
	}
	wantIDs := []string{"a1", "a2", "a3"}
	wantVerdicts := []string{"keep", "drop", "gray"}
	for i := 0; i < 3; i++ {
		if got := fieldStr(t, lines[i], "id"); got != wantIDs[i] {
			t.Errorf("row %d id = %q, want %q (order follows input)", i, got, wantIDs[i])
		}
		if got := fieldStr(t, lines[i], "verdict"); got != wantVerdicts[i] {
			t.Errorf("row %d verdict = %q, want %q", i, got, wantVerdicts[i])
		}
		if _, hasErr := lines[i]["error"]; hasErr {
			t.Errorf("row %d unexpectedly carries an error", i)
		}
	}
	// meta is opaque and must be re-emitted verbatim.
	if raw, ok := lines[0]["meta"]; !ok || !strings.Contains(string(raw), `"source":"hn"`) {
		t.Errorf("row 0 meta not echoed: %v", lines[0])
	}

	m := metaOf(t, lines)
	if got := fieldStr(t, m, "model"); got != "e2e-model" {
		t.Errorf("_meta.model = %q, want e2e-model", got)
	}
	if got := fieldStr(t, m, "rubric_sha256"); len(got) != 64 {
		t.Errorf("_meta.rubric_sha256 = %q, want a 64-hex digest", got)
	}
	if items, kept, dropped, gray := fieldInt(t, m, "items"), fieldInt(t, m, "kept"), fieldInt(t, m, "dropped"), fieldInt(t, m, "gray"); items != 3 || kept != 1 || dropped != 1 || gray != 1 {
		t.Errorf("_meta counts = items %d keep %d drop %d gray %d, want 3/1/1/1", items, kept, dropped, gray)
	}
	// 3 rows really hit the wire — a batch that silently reused one
	// answer would still look fine above.
	if n, _ := b.stats(); n != 3 {
		t.Errorf("backend saw %d requests, want 3 (one chat call per entry)", n)
	}
	if got := fieldInt(t, m, "prompt_tokens"); got != 30 {
		t.Errorf("_meta.prompt_tokens = %d, want 30 (3 calls × 10)", got)
	}
}

// TestJudgeE2E_PartialFailureExitsOne: one unusable reply poisons ONLY
// its row; the rest and the _meta line stay complete, and the exit code
// is 1 (PRD §3.4: "some rows did not get judged").
func TestJudgeE2E_PartialFailureExitsOne(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0)

	in := strings.Join([]string{
		`{"id":"a1","text":"ITEM1-KEEP"}`,
		`{"id":"a2","text":"ITEM2-BAD"}`,
		`{"id":"a3","text":"ITEM3-DROP"}`,
	}, "\n") + "\n"
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in, "--rubric", rubric)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (partial failure)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "carried an error") {
		t.Errorf("stderr should name the partial failure:\n%s", stderr)
	}

	lines := readVerdictLines(t, stdout)
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 3 rows + _meta\nstdout:\n%s", len(lines), stdout)
	}
	if v := fieldStr(t, lines[0], "verdict"); v != "keep" {
		t.Errorf("row a1 verdict = %q, want keep (a bad sibling must not contaminate it)", v)
	}
	if e := fieldStr(t, lines[1], "error"); !strings.Contains(e, "malformed verdict") {
		t.Errorf("row a2 error = %q, want a malformed-verdict error", e)
	}
	if _, hasVerdict := lines[1]["verdict"]; hasVerdict {
		t.Errorf("error row must not carry a verdict: %v", lines[1])
	}
	if v := fieldStr(t, lines[2], "verdict"); v != "drop" {
		t.Errorf("row a3 verdict = %q, want drop", v)
	}

	m := metaOf(t, lines)
	if items, kept, dropped := fieldInt(t, m, "items"), fieldInt(t, m, "kept"), fieldInt(t, m, "dropped"); items != 3 || kept != 1 || dropped != 1 {
		t.Errorf("_meta counts = items %d keep %d drop %d, want 3/1/1 (error rows still count as items)", items, kept, dropped)
	}
}

// TestJudgeE2E_MalformedInputLineIsolated: a line that is not JSON at
// all never reaches the model and never aborts the batch — it is one
// error row, and the file still ends with _meta.
func TestJudgeE2E_MalformedInputLineIsolated(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0)

	in := strings.Join([]string{
		`not json at all`,
		`{"id":"a2","text":"ITEM2-KEEP"}`,
	}, "\n") + "\n"
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in, "--rubric", rubric)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	lines := readVerdictLines(t, stdout)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 2 rows + _meta:\n%s", len(lines), stdout)
	}
	if e := fieldStr(t, lines[0], "error"); !strings.Contains(e, "invalid JSON") {
		t.Errorf("bad line's row = %v, want an invalid-JSON error", lines[0])
	}
	if v := fieldStr(t, lines[1], "verdict"); v != "keep" {
		t.Errorf("good row verdict = %q, want keep", v)
	}
	if n, _ := b.stats(); n != 1 {
		t.Errorf("backend saw %d requests, want 1 (the bad line must not reach the model)", n)
	}
}

// ---- acceptance ②: usage errors are exit 2 ---------------------------

// TestJudgeE2E_UsageErrorsExitTwo covers the shapes the card names:
// missing / unreadable --rubric, --parallel out of range, an unknown
// flag, and a positional mixed into the flags. All must exit 2 with a
// clear stderr AND must not reach the backend.
func TestJudgeE2E_UsageErrorsExitTwo(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)

	cases := []struct {
		name    string
		args    []string
		errHint string
	}{
		{"missing rubric", []string{"--parallel", "2"}, "--rubric is required"},
		{"rubric gone", []string{"--rubric", filepath.Join(workDir, "nope.md")}, "rubric"},
		{"parallel too high", []string{"--rubric", rubric, "--parallel", "17"}, "between 1 and"},
		{"parallel zero", []string{"--rubric", rubric, "--parallel", "0"}, "between 1 and"},
		{"unknown flag", []string{"--rubric", rubric, "--isolated"}, "not defined"},
		{"positional mixed in", []string{rubric, "--rubric", rubric}, "unexpected positional"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newJudgeBackend(t, 0)
			stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, `{"id":"a1","text":"x"}`+"\n", tc.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.errHint) {
				t.Errorf("stderr misses %q:\n%s", tc.errHint, stderr)
			}
			if n, _ := b.stats(); n != 0 {
				t.Errorf("backend saw %d requests, want 0 (a usage error must die before any call)", n)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("stdout is not clean on a usage error:\n%s", stdout)
			}
		})
	}
}

// ---- acceptance ①/②/③: --max-cost budget gate (card c_qjuags) -------

// TestJudgeE2E_MaxCostStopsEarlyExitsThree: with the backend pricing each
// call above the cap, the lane stops dispatching new entries, finishes the
// ones already in flight, writes a COMPLETE file (every judged row + _meta),
// and exits 3 — never 1. The _meta counters must reconcile with the rows.
func TestJudgeE2E_MaxCostStopsEarlyExitsThree(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0)
	b.usageOverride = budgetUsage

	var in strings.Builder
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&in, `{"id":"a%d","text":"ITEM%d-KEEP"}`+"\n", i, i)
	}
	// 0.30 is below one call's price (~$0.42 peak / ~$0.21 off-peak), so
	// the FIRST emitted row trips the gate; --parallel 1 leaves at most
	// one extra entry in flight. Eight inputs means "stopped early" is
	// unambiguous.
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in.String(),
		"--rubric", rubric, "--parallel", "1", "--max-cost", "0.30")
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (budget exhausted)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "--max-cost") {
		t.Errorf("stderr should name the budget stop:\n%s", stderr)
	}

	lines := readVerdictLines(t, stdout) // every line must be whole JSON
	n, _ := b.stats()
	if n >= 8 {
		t.Fatalf("backend saw %d requests for 8 entries — the gate never stopped dispatch", n)
	}
	if n < 1 {
		t.Fatalf("backend saw 0 requests — the budget gate tripped before any judgment")
	}
	// Every dispatched entry has its row on disk, plus one _meta line.
	if len(lines) != n+1 {
		t.Fatalf("got %d lines, want %d rows + _meta (backend saw %d calls)", len(lines), n, n)
	}
	m := metaOf(t, lines)
	if got := fieldInt(t, m, "items"); got != n {
		t.Errorf("_meta.items = %d, want %d (one row per dispatched entry)", got, n)
	}
	// Reconciliation: items == kept+dropped+gray+errors (all rows judged OK
	// here, so errors must be 0 and kept must equal the row count).
	items := fieldInt(t, m, "items")
	kept := fieldInt(t, m, "kept")
	dropped := fieldInt(t, m, "dropped")
	gray := fieldInt(t, m, "gray")
	if items != kept+dropped+gray {
		t.Errorf("_meta counts do not reconcile: items %d != kept %d + dropped %d + gray %d", items, kept, dropped, gray)
	}
	// _meta.cost_usd must reflect what was actually spent AND be at/above
	// the cap — that is precisely WHY the gate stopped.
	if cost := fieldFloat(t, m, "cost_usd"); cost < 0.30 {
		t.Errorf("_meta.cost_usd = %v, want >= 0.30 (the stop threshold)", cost)
	}
}

// TestJudgeE2E_MaxCostDrainsInFlight: with --parallel 4 and a delayed
// backend, all four entries are in flight when the first result trips the
// cap. Every in-flight entry must still finish AND get its row on disk —
// "stop dispatching" never means "drop work already started" (card
// acceptance ①: 在飞条等收尾). Nothing beyond the in-flight four is begun.
func TestJudgeE2E_MaxCostDrainsInFlight(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 60*time.Millisecond) // all 4 overlap before the first reply
	b.usageOverride = budgetUsage

	var in strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&in, `{"id":"a%d","text":"ITEM%d-KEEP"}`+"\n", i, i)
	}
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in.String(),
		"--rubric", rubric, "--parallel", "4", "--max-cost", "0.30")
	if code != 3 {
		t.Fatalf("exit = %d, want 3\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	lines := readVerdictLines(t, stdout)
	n, maxInFlight := b.stats()
	if maxInFlight < 2 {
		t.Fatalf("max in flight = %d — the test did not exercise real overlap", maxInFlight)
	}
	// In-flight entries drain; the rest are never dispatched. The exact
	// count depends on how many workers grabbed a queued job before the
	// emitter folded the first over-cap result and closed the gate — all
	// of those are legitimately "in flight at the cap" and MUST be fully
	// drained. What the contract pins: dispatch stopped WELL short of the
	// 12 inputs, and every call that did start has its row on disk.
	if n < 1 || n >= 12 {
		t.Fatalf("backend saw %d requests for 12 inputs — the gate did not stop early", n)
	}
	if len(lines) != n+1 {
		t.Fatalf("got %d lines, want %d drained rows + _meta:\n%s", len(lines), n, stdout)
	}
	for i := 0; i < n; i++ {
		if got := fieldStr(t, lines[i], "id"); got != fmt.Sprintf("a%d", i+1) {
			t.Errorf("row %d id = %q, want a%d (input order)", i, got, i+1)
		}
	}
	m := metaOf(t, lines)
	if got := fieldInt(t, m, "items"); got != n {
		t.Errorf("_meta.items = %d, want %d drained rows", got, n)
	}
}

// TestJudgeE2E_MaxCostNotReachedIsNormal: a generous cap that is never hit
// must behave exactly like the no-cap c_jdo7rp lane — exit 0, every row,
// and a small real cost in _meta.
func TestJudgeE2E_MaxCostNotReachedIsNormal(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0) // tiny default usage (~$0.0000x/call)

	in := `{"id":"a1","text":"ITEM1-KEEP"}` + "\n" + `{"id":"a2","text":"ITEM2-DROP"}` + "\n"
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in,
		"--rubric", rubric, "--max-cost", "1.00")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (cap never reached)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	lines := readVerdictLines(t, stdout)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 2 rows + _meta:\n%s", len(lines), stdout)
	}
	if n, _ := b.stats(); n != 2 {
		t.Errorf("backend saw %d requests, want 2 (all entries judged)", n)
	}
	if cost := fieldFloat(t, metaOf(t, lines), "cost_usd"); cost <= 0 || cost >= 1.00 {
		t.Errorf("_meta.cost_usd = %v, want a small positive cost below the cap", cost)
	}
}

// TestParseJudgeFlags_MaxCostInvalid: the USD cap must be a positive,
// finite number. Zero / negative / non-numeric / NaN / Inf are usage
// errors (exit 2), never a silent "no cap" or an unbounded run.
func TestParseJudgeFlags_MaxCostInvalid(t *testing.T) {
	for _, raw := range []string{"0", "-1", "-0.5", "abc", "NaN", "Inf", "+Inf", "1.2.3"} {
		t.Run(raw, func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := parseJudgeFlags([]string{"--rubric", "r", "--max-cost", raw}, &stderr)
			if !errors.Is(err, errJudgeUsage) {
				t.Fatalf("parse accepted --max-cost %q (err = %v)", raw, err)
			}
			if !strings.Contains(stderr.String(), "positive USD amount") {
				t.Errorf("stderr misses the cap explanation:\n%s", stderr.String())
			}
		})
	}
}

// TestJudgeE2E_MaxCostInvalidExitsTwo: the invalid-value table reaches the
// real binary as exit 2 and never touches the backend.
func TestJudgeE2E_MaxCostInvalidExitsTwo(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	for _, raw := range []string{"0", "-1", "abc", "NaN"} {
		t.Run(raw, func(t *testing.T) {
			b := newJudgeBackend(t, 0)
			stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir,
				`{"id":"a1","text":"ITEM1-KEEP"}`+"\n",
				"--rubric", rubric, "--max-cost", raw)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 for --max-cost %q\nstdout:\n%s\nstderr:\n%s", code, raw, stdout, stderr)
			}
			if !strings.Contains(stderr, "positive USD amount") {
				t.Errorf("stderr misses the cap explanation:\n%s", stderr)
			}
			if n, _ := b.stats(); n != 0 {
				t.Errorf("backend saw %d requests — a usage error must die before any call", n)
			}
		})
	}
}

// ---- acceptance ③: bounded concurrency, whole lines ------------------

// TestJudgeE2E_ParallelBoundedWholeLines: with --parallel 4 and a slow
// backend, at most 4 calls are ever in flight (the cap is a constant,
// not a suggestion) and >1 really overlaps; every output line is valid
// JSON (no interleaved half lines), the row order still follows the
// input, and _meta is last.
func TestJudgeE2E_ParallelBoundedWholeLines(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 40*time.Millisecond)

	var in strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&in, `{"id":"a%d","text":"ITEM%d-KEEP"}`+"\n", i, i)
	}
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in.String(), "--rubric", rubric, "--parallel", "4")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr)
	}

	lines := readVerdictLines(t, stdout) // fails the test on any bad line
	if len(lines) != 11 {
		t.Fatalf("got %d lines, want 10 rows + _meta\nstdout:\n%s", len(lines), stdout)
	}
	for i := 0; i < 10; i++ {
		want := fmt.Sprintf("a%d", i+1)
		if got := fieldStr(t, lines[i], "id"); got != want {
			t.Fatalf("row %d id = %q, want %q (input order is preserved by the emitter)", i, got, want)
		}
	}
	metaOf(t, lines) // asserts the last line is _meta

	n, maxInFlight := b.stats()
	if n != 10 {
		t.Errorf("backend saw %d requests, want 10", n)
	}
	if maxInFlight > judgeMaxParallel {
		t.Errorf("max in flight = %d, the ceiling is %d", maxInFlight, judgeMaxParallel)
	}
	if maxInFlight > 4 {
		t.Errorf("max in flight = %d, --parallel 4 asked for at most 4", maxInFlight)
	}
	if maxInFlight < 2 {
		t.Errorf("max in flight = %d — the run was serial, so the bound was never exercised", maxInFlight)
	}
}

// TestJudgeE2E_ParallelOneIsSerial pins the low end of the range: one
// worker at a time, still one row per entry, still exit 0.
func TestJudgeE2E_ParallelOneIsSerial(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 20*time.Millisecond)

	in := `{"id":"a1","text":"ITEM1-KEEP"}` + "\n" + `{"id":"a2","text":"ITEM2-DROP"}` + "\n"
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in, "--rubric", rubric, "--parallel", "1")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr)
	}
	readVerdictLines(t, stdout)
	if _, maxInFlight := b.stats(); maxInFlight != 1 {
		t.Errorf("max in flight = %d, want exactly 1 under --parallel 1", maxInFlight)
	}
}

// ---- acceptance ④: the judge lane forks nothing ----------------------

// TestJudgeE2E_SpawnsNoMCPServer is the c_xujwvq precedent applied to
// this lane: with an mcp.json that WOULD fork a child, `seek judge` must
// never load MCP at all. The evidence is the child's pid file staying
// absent (a fork writes it), plus no "mcp: loaded" line.
func TestJudgeE2E_SpawnsNoMCPServer(t *testing.T) {
	workDir := t.TempDir()
	pidFile := filepath.Join(workDir, "mcp-child.pid")
	writeMCPConfig(t, workDir, stubServerRoster(pidFile, nil))
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0)

	in := `{"id":"a1","text":"ITEM1-KEEP"}` + "\n"
	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, in, "--rubric", rubric)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("the MCP server spawned — the judge lane must not load MCP")
	}
	if strings.Contains(stderr, "mcp: loaded") {
		t.Errorf("an MCP load happened in the judge lane:\n%s", stderr)
	}
	// And the request carried no tool definitions at all: the judge face
	// cannot execute anything (PRD §5 L1 / §6 "不碰").
	for i, body := range b.capturedBodies() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if _, present := raw["tools"]; present {
			t.Errorf("request %d carries a tools field:\n%s", i, body)
		}
	}
}

// ---- file I/O shape --------------------------------------------------

// TestJudgeE2E_FileInFileOut pins the two file forms: --in/--out as
// paths, and the default "-" (stdin/stdout) shape. Both must produce
// the same row shape for the same input.
func TestJudgeE2E_FileInFileOut(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0)

	inPath := filepath.Join(workDir, "items.jsonl")
	payload := `{"id":"a1","text":"ITEM1-KEEP"}` + "\n" + `{"id":"a2","text":"ITEM2-DROP"}` + "\n"
	if err := os.WriteFile(inPath, []byte(payload), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	outPath := filepath.Join(workDir, "verdicts.jsonl")

	_, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, "", "--rubric", rubric, "--in", inPath, "--out", outPath)
	if code != 0 {
		t.Fatalf("file mode exit = %d, want 0\nstderr:\n%s", code, stderr)
	}
	fileOut, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read verdicts: %v", err)
	}
	fileLines := readVerdictLines(t, string(fileOut))
	if len(fileLines) != 3 {
		t.Fatalf("file mode produced %d lines, want 2 rows + _meta:\n%s", len(fileLines), fileOut)
	}
	if got := fieldStr(t, fileLines[0], "id"); got != "a1" {
		t.Errorf("first row id = %q, want a1", got)
	}

	// Same input through stdin/stdout must agree on the audit shape.
	b2 := newJudgeBackend(t, 0)
	stdout, stderr, code := runJudgeE2E(t, b2.srv.URL, workDir, payload, "--rubric", rubric)
	if code != 0 {
		t.Fatalf("stdin mode exit = %d, want 0\nstderr:\n%s", code, stderr)
	}
	stdinLines := readVerdictLines(t, stdout)
	if len(stdinLines) != 3 {
		t.Fatalf("stdin mode produced %d lines, want 3:\n%s", len(stdinLines), stdout)
	}
	if a, b := fieldStr(t, metaOf(t, fileLines), "rubric_sha256"), fieldStr(t, metaOf(t, stdinLines), "rubric_sha256"); a != b {
		t.Errorf("rubric_sha256 differs across I/O forms: %q vs %q", a, b)
	}
}

// TestJudgeE2E_EmptyInputStillEmitsMeta: an empty batch is still a
// valid file — one _meta line, exit 0. A caller reading cost/audit
// fields must not have to special-case "no items".
func TestJudgeE2E_EmptyInputStillEmitsMeta(t *testing.T) {
	workDir := t.TempDir()
	rubric := writeRubric(t, workDir)
	b := newJudgeBackend(t, 0)

	stdout, stderr, code := runJudgeE2E(t, b.srv.URL, workDir, "", "--rubric", rubric)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, stderr)
	}
	lines := readVerdictLines(t, stdout)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want only _meta:\n%s", len(lines), stdout)
	}
	m := metaOf(t, lines)
	if items := fieldInt(t, m, "items"); items != 0 {
		t.Errorf("_meta.items = %d, want 0", items)
	}
	if n, _ := b.stats(); n != 0 {
		t.Errorf("backend saw %d requests for an empty batch, want 0", n)
	}
}

// ---- judgeStream paths reachable only in-process ---------------------

// chatterFunc adapts a function to judge.Chatter.
type chatterFunc func(context.Context, *deepseek.ChatRequest) (*deepseek.ChatResponse, error)

func (f chatterFunc) Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	return f(ctx, req)
}

// TestJudgeStream_CancelledContextEmitsARowForEveryEntry keeps the "no
// input is silently lost" half of the kernel contract intact on the CLI
// path: a pre-cancelled ctx still produces one row per entry and a
// _meta, it just never calls the model.
func TestJudgeStream_CancelledContextEmitsARowForEveryEntry(t *testing.T) {
	var calls atomic.Int32
	chat := chatterFunc(func(context.Context, *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	})
	j, err := judge.New(chat, "m", "rubric")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	in := strings.NewReader(`{"id":"a1","text":"x"}` + "\n" + `{"id":"a2","text":"y"}` + "\n")
	if _, _, err := judgeStream(ctx, j, in, &out, 2, 0); err != nil {
		t.Fatalf("judgeStream: %v", err)
	}
	lines := readVerdictLines(t, out.String())
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 2 rows + _meta:\n%s", len(lines), out.String())
	}
	for i, want := range []string{"a1", "a2"} {
		if got := fieldStr(t, lines[i], "id"); got != want {
			t.Errorf("row %d id = %q, want %q", i, got, want)
		}
		if e := fieldStr(t, lines[i], "error"); !strings.Contains(e, "not judged") {
			t.Errorf("row %d error = %q, want a 'not judged' row", i, e)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the model was called %d time(s) on a cancelled ctx", n)
	}
}

// errWriter fails every write after the Nth one, so the emitter's error
// path can be exercised without a full disk.
type errWriter struct {
	mu      sync.Mutex
	allowed int
	writes  int
}

func (w *errWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if w.writes > w.allowed {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}

// TestJudgeStream_WriteFailureIsReported: a failing sink must surface as
// an error from judgeStream (so the CLI exits non-zero) rather than
// silently dropping rows.
func TestJudgeStream_WriteFailureIsReported(t *testing.T) {
	chat := chatterFunc(func(context.Context, *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
		return &deepseek.ChatResponse{Choices: []deepseek.Choice{{Message: deepseek.Message{
			Content: `{"verdict":"keep","reasons":["r"]}`,
		}}}}, nil
	})
	j, err := judge.New(chat, "m", "rubric")
	if err != nil {
		t.Fatal(err)
	}

	in := strings.NewReader(`{"id":"a1","text":"x"}` + "\n")
	var sink errWriter // zero allowed writes
	if _, _, err := judgeStream(context.Background(), j, in, &sink, 1, 0); err == nil {
		t.Fatal("judgeStream reported success despite every write failing")
	} else if !strings.Contains(err.Error(), "write output") {
		t.Errorf("error = %v, want a write-output context", err)
	}
}

// TestJudgeStream_FidelityWithResultJSONL: the streamed file and the
// buffered Result.JSONL() must be byte-identical for the same rows —
// that is the promise behind exporting judge.MarshalRow / MarshalMeta.
// A drift here would mean two output shapes sharing one PRD contract.
func TestJudgeStream_FidelityWithResultJSONL(t *testing.T) {
	entries := judge.ParseEntries([]byte(strings.Join([]string{
		`{"id":"a1","text":"ITEM1-KEEP","meta":{"k":"v"},"}`,
		`{"id":"a2","text":"ITEM2-DROP"}`,
	}, "\n")))
	// (the first line is intentionally broken — its row must match too)
	chat := chatterFunc(func(_ context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
		content := `{"verdict":"keep","reasons":["r"]}`
		if strings.Contains(req.Messages[len(req.Messages)-1].Content, "-DROP") {
			content = `{"verdict":"drop","reasons":["r2"]}`
		}
		return &deepseek.ChatResponse{
			Choices: []deepseek.Choice{{Message: deepseek.Message{Content: content}}},
			Usage:   deepseek.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		}, nil
	})
	j, err := judge.New(chat, "m", "rubric")
	if err != nil {
		t.Fatal(err)
	}

	var streamed bytes.Buffer
	if _, _, err := judgeStream(context.Background(), j, strings.NewReader(strings.Join([]string{
		`{"id":"a1","text":"ITEM1-KEEP","meta":{"k":"v"},"}`,
		`{"id":"a2","text":"ITEM2-DROP"}`,
	}, "\n")), &streamed, 2, 0); err != nil {
		t.Fatalf("judgeStream: %v", err)
	}

	buffered, err := j.Batch(context.Background(), entries).JSONL()
	if err != nil {
		t.Fatalf("JSONL: %v", err)
	}
	if streamed.String() != string(buffered) {
		t.Errorf("streamed output differs from Result.JSONL():\nstreamed:\n%s\nbuffered:\n%s", streamed.String(), buffered)
	}
}
