package autopilot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/verifier"
)

// ledgerReport is a run with every field the row must carry: mixed
// task statuses, all three verdict outcomes, judge errors on some
// tasks, non-zero git Δ and cost.
func ledgerReport() Report {
	return Report{
		Goal: "fix the build and add tests",
		Outcomes: []Outcome{
			{Task: Task{Title: "fix build"}, Status: "done", Worktree: "/wt/a", Commit: "abc1234",
				Tokens: 5100, CostUSD: 0.02, Files: 2, Insertions: 10, Deletions: 3,
				Verdict: &verifier.Verdict{Overall: verifier.Pass}},
			{Task: Task{Title: "add tests"}, Status: "done", Worktree: "/wt/b",
				Tokens: 2000, CostUSD: 0.01, Files: 1, Insertions: 5, Deletions: 0,
				Verdict: &verifier.Verdict{Overall: verifier.Doubt, JudgeError: "judge: 401 unauthorized"}},
			{Task: Task{Title: "bump deps"}, Status: "failed", Summary: "boom",
				Verdict: &verifier.Verdict{Overall: verifier.Fail, JudgeError: "judge: stream cut"}},
		},
		Done: 2, Failed: 1, TotalTokens: 7100, TotalCostUSD: 0.03,
		VerifiedPass: 1, VerifiedDoubt: 1, VerifiedFail: 1,
	}
}

// TestLine_FieldCompleteness: acceptance ① — the persisted row carries
// every required key with the right values. Checked against the raw
// JSON map (not the struct) so a missing json tag or an omitempty
// cannot hide behind Go's zero values.
func TestLine_FieldCompleteness(t *testing.T) {
	ts := time.Date(2026, 11, 24, 10, 30, 0, 0, time.UTC)
	body, err := json.Marshal(ledgerReport().Line("deepseek-chat", ts))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("row is not valid JSON: %v\n%s", err, body)
	}
	want := map[string]any{
		"ts":           "2026-11-24T10:30:00Z",
		"goal":         "fix the build and add tests",
		"verdict":      "fail", // fail beats doubt beats pass
		"pass":         float64(1),
		"doubt":        float64(1),
		"fail":         float64(1),
		"done":         float64(2),
		"failed":       float64(1),
		"tasks":        float64(3),
		"model":        "deepseek-chat",
		"tokens":       float64(7100),
		"cost_usd":     0.03,
		"files":        float64(3),
		"insertions":   float64(15),
		"deletions":    float64(3),
		"judge_errors": []any{"judge: 401 unauthorized", "judge: stream cut"},
	}
	for k, v := range want {
		got, ok := raw[k]
		if !ok {
			t.Fatalf("row missing key %q:\n%s", k, body)
		}
		gotBody, _ := json.Marshal(got)
		wantBody, _ := json.Marshal(v)
		if !bytes.Equal(gotBody, wantBody) {
			t.Errorf("key %q = %s, want %s", k, gotBody, wantBody)
		}
	}
	if len(raw) != len(want) {
		t.Fatalf("row has %d keys, want exactly %d (no extras):\n%s", len(raw), len(want), body)
	}
}

// TestLine_EmptyJudgeErrorsAlwaysPresent: the judge_errors key must be
// present (empty array) when every judge call succeeded or no
// verification ran — "字段齐全" includes empty-but-present.
func TestLine_EmptyJudgeErrorsAlwaysPresent(t *testing.T) {
	body, err := json.Marshal(Report{Goal: "g"}.Line("m", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"judge_errors":[]`) {
		t.Fatalf("judge_errors must be present as an empty array, got:\n%s", body)
	}
}

// TestOverallVerdict_Fold: run-level rollup uses the verifier's
// precedence (fail > doubt > pass) and "none" for zero judged tasks.
func TestOverallVerdict_Fold(t *testing.T) {
	cases := []struct {
		p, d, f int
		want    string
	}{
		{0, 0, 0, "none"},
		{3, 0, 0, "pass"},
		{3, 1, 0, "doubt"},
		{3, 1, 1, "fail"},
		{0, 0, 1, "fail"},
	}
	for _, c := range cases {
		r := Report{VerifiedPass: c.p, VerifiedDoubt: c.d, VerifiedFail: c.f}
		if got := r.OverallVerdict(); got != c.want {
			t.Errorf("fold(%d,%d,%d) = %q, want %q", c.p, c.d, c.f, got, c.want)
		}
	}
}

// TestAppendReportLine_RoundTrip: acceptance ① — appended lines exist,
// are individually valid JSON, and appending never rewrites prior rows.
func TestAppendReportLine_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autopilot", "reports.jsonl")
	ts := time.Date(2026, 11, 24, 10, 30, 0, 0, time.UTC)
	first := ledgerReport().Line("deepseek-chat", ts)
	second := Report{Goal: "second run", Outcomes: []Outcome{{Status: "done"}}, Done: 1, VerifiedPass: 1}.Line("deepseek-reasoner", ts.Add(time.Hour))

	if err := AppendReportLine(path, first); err != nil {
		t.Fatal(err)
	}
	if err := AppendReportLine(path, second); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatalf("ledger must end with a newline, got tail %q", body[len(body)-40:])
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), body)
	}
	var r1, r2 ReportLine
	if err := json.Unmarshal([]byte(lines[0]), &r1); err != nil {
		t.Fatalf("line 1 not valid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &r2); err != nil {
		t.Fatalf("line 2 not valid JSON: %v", err)
	}
	if r1.Goal != "fix the build and add tests" || r1.Tokens != 7100 {
		t.Errorf("line 1 round-trip mismatch: %+v", r1)
	}
	if r2.Goal != "second run" || r2.Verdict != "pass" || r2.JudgeErrors == nil {
		t.Errorf("line 2 round-trip mismatch: %+v", r2)
	}
}

// TestAppendReportLine_UnwritableNoPanic: acceptance ② — a failing
// write path returns an error instead of panicking. Two shapes: a
// ledger path that is a DIRECTORY (open fails) and a parent that is a
// FILE (MkdirAll fails).
func TestAppendReportLine_UnwritableNoPanic(t *testing.T) {
	dir := t.TempDir()

	asDir := filepath.Join(dir, "reports.jsonl")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AppendReportLine(asDir, Report{Goal: "g"}.Line("m", time.Now())); err == nil {
		t.Fatal("appending onto a directory must fail")
	}

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendReportLine(filepath.Join(blocker, "reports.jsonl"), Report{Goal: "g"}.Line("m", time.Now())); err == nil {
		t.Fatal("appending under a file parent must fail")
	}
}

// TestAppendReportLine_Concurrent: acceptance ③ — concurrent appends
// produce exactly N×M complete lines, every line valid JSON, no torn
// halves. The guarantee is the O_APPEND single-Write discipline plus
// the in-process appendLock (cross-process interleave falls to the
// kernel's write serialisation — see AppendReportLine's comment).
func TestAppendReportLine_Concurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autopilot", "reports.jsonl")
	const workers, each = 8, 16

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				ln := Report{Goal: fmt.Sprintf("worker %d run %d", w, i)}.Line("m", time.Now())
				if err := AppendReportLine(path, ln); err != nil {
					t.Errorf("append w%d i%d: %v", w, i, err)
				}
			}
		}(w)
	}
	wg.Wait()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != workers*each {
		t.Fatalf("want %d lines, got %d", workers*each, len(lines))
	}
	seen := make(map[string]bool, len(lines))
	for i, ln := range lines {
		var row ReportLine
		if err := json.Unmarshal([]byte(ln), &row); err != nil {
			t.Fatalf("torn or invalid line %d: %v\n%q", i, err, ln)
		}
		if row.Goal == "" || seen[row.Goal] {
			t.Fatalf("line %d goal %q missing or duplicated", i, row.Goal)
		}
		seen[row.Goal] = true
	}
}

// TestLine_CapsBoundRowSize: the field caps exist to keep rows
// PIPE_BUF-shaped (well under 4 KiB). Feed pathological inputs — a
// huge goal and many long judge errors — and pin the marshalled bound.
func TestLine_CapsBoundRowSize(t *testing.T) {
	rep := Report{Goal: strings.Repeat("目标很长的目标", 10_000)} // 60k runes
	for i := 0; i < 32; i++ {
		rep.Outcomes = append(rep.Outcomes, Outcome{
			Status: "done",
			Verdict: &verifier.Verdict{Overall: verifier.Doubt,
				JudgeError: strings.Repeat("boom ", 10_000)},
		})
	}
	body, err := json.Marshal(rep.Line("m", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 3800 {
		t.Fatalf("capped row must stay under 3800 bytes, got %d", len(body))
	}
	// Rune-safety: the capped goal must not end mid-rune.
	var row ReportLine
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatal(err)
	}
	for _, r := range row.Goal {
		if r == 0xFFFD {
			t.Fatal("capped goal contains a replacement rune — truncation split a rune")
		}
	}
	if len(row.JudgeErrors) != capJudgeErrs {
		t.Fatalf("judge errors must cap at %d, got %d", capJudgeErrs, len(row.JudgeErrors))
	}
}

// TestLine_JudgeErrCapDoesNotStopDeltaFold pins a real bug the first
// review pass found: JudgeError cap and git-Δ fold share one loop, so
// an early `break` at the cap also stopped folding files/insertions/
// deletions for every remaining outcome — the row then under-reports
// its own footprint while `tasks` stays truthful. Unreachable at the
// default Caps.MaxTasks=8 (the 9th judge error is out of range), so
// the cap and the fold must be exercised independently.
func TestLine_JudgeErrCapDoesNotStopDeltaFold(t *testing.T) {
	const n = capJudgeErrs + 2 // every outcome has a judge error AND Δ
	rep := Report{Goal: "g"}
	for i := 0; i < n; i++ {
		rep.Outcomes = append(rep.Outcomes, Outcome{
			Status: "done", Files: 1, Insertions: 2, Deletions: 1,
			Verdict: &verifier.Verdict{Overall: verifier.Doubt, JudgeError: fmt.Sprintf("judge err %d", i)},
		})
	}
	ln := rep.Line("m", time.Now())
	if len(ln.JudgeErrors) != capJudgeErrs {
		t.Errorf("judge errors must cap at %d, got %d", capJudgeErrs, len(ln.JudgeErrors))
	}
	if ln.Files != n || ln.Insertions != 2*n || ln.Deletions != n {
		t.Fatalf("git Δ must fold over ALL %d outcomes (cap bounds judge errors, not the fold): got files=%d insertions=%d deletions=%d",
			n, ln.Files, ln.Insertions, ln.Deletions)
	}
}
