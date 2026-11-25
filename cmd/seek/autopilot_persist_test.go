package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/autopilot"
	"github.com/whyiyhw/seek/internal/verifier"
)

// TestPersistAutopilotReport_LandsUnderSeekHome: the runAutopilot
// wiring writes exactly one valid JSONL row per call to
// SEEK_HOME/autopilot/reports.jsonl (v8 §9.5③3, acceptance ① at the
// cmd layer — the unit-level row shape is pinned in
// internal/autopilot/persist_test.go).
func TestPersistAutopilotReport_LandsUnderSeekHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)

	rep := autopilot.Report{
		Goal: "wire the verdict ledger",
		Outcomes: []autopilot.Outcome{
			{Task: autopilot.Task{Title: "t1"}, Status: "done", Tokens: 100, CostUSD: 0.001,
				Files: 2, Insertions: 7, Deletions: 1,
				Verdict: &verifier.Verdict{Overall: verifier.Doubt, JudgeError: "judge: 401"}},
		},
		Done: 1, TotalTokens: 100, TotalCostUSD: 0.001, VerifiedDoubt: 1,
	}
	if err := persistAutopilotReport(rep, "deepseek-chat"); err != nil {
		t.Fatal(err)
	}
	if err := persistAutopilotReport(rep, "deepseek-chat"); err != nil {
		t.Fatal(err) // second run must append, not fail on the existing file
	}

	body, err := os.ReadFile(filepath.Join(home, "autopilot", "reports.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 ledger lines, got %d:\n%s", len(lines), body)
	}
	var row autopilot.ReportLine
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatalf("ledger line not valid JSON: %v", err)
	}
	if row.Model != "deepseek-chat" || row.Goal != "wire the verdict ledger" || row.Verdict != "doubt" {
		t.Errorf("row mismatch: %+v", row)
	}
	if _, err := time.Parse(time.RFC3339, row.TS); err != nil {
		t.Errorf("ts not RFC3339: %v", err)
	}
}

// TestPersistAutopilotReport_FailureIsError: acceptance ② — an
// unwritable SEEK_HOME yields a returned error (for stderr logging in
// runAutopilot), never a panic.
func TestPersistAutopilotReport_FailureIsError(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEEK_HOME", blocker) // Home() resolves; MkdirAll cannot

	if err := persistAutopilotReport(autopilot.Report{Goal: "g"}, "m"); err == nil {
		t.Fatal("persisting under a file SEEK_HOME must return an error")
	}
}
