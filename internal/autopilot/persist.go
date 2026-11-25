package autopilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/whyiyhw/seek/internal/verifier"
)

// v8 §9.5③3 wiring (card c_wjo6wy): the R2 verdict finally has an
// accumulatable落点. Until now a completed run lived only in
// Report.Verified{Pass,Doubt,Fail} in-memory counts + fmt.Println +
// a best-effort webhook — "就算跑过也没有可积累的痕迹". This file
// persists one JSON line per completed run to
// SEEK_HOME/autopilot/reports.jsonl so the §9.1-style doubt rate can
// be recomputed from the ledger once the butler-side reader exists
// (out of scope here: seek holds no butler credentials and writes
// nothing remote — LOCAL ledger only).
//
// Why a Report-level row and not per-task verdict dumps: §9.1's doubt
// rate aggregates over judged units; the Report's pass/doubt/fail
// counts ARE that aggregation surface (they are what §9.5③ measured
// as "structurally zero"). JudgeError rides along as the doubt
// attribution signal — a doubt with a judge error is an infra story, a
// doubt without one is an evidence-gap story, and the two need
// different follow-ups.

const (
	// capGoalRunes bounds the persisted goal. The row must stay small
	// (see AppendReportLine); a goal is prose and 256 runes is far past
	// the decomposer's input shape while keeping the worst-case row
	// comfortably bounded.
	capGoalRunes = 256
	// capJudgeErrRunes bounds each persisted judge error — same budget
	// logic, aligned with the eval lane's judge_reason cap spirit.
	capJudgeErrRunes = 200
	// capJudgeErrs bounds how many judge errors ride on one row. Rows
	// are per-run; a run is capped by Caps.MaxTasks (default 8), but
	// caps are caller-configurable, so the row caps itself.
	capJudgeErrs = 8
)

// ReportLine is the persistent JSONL row for one completed run —
// the "at least" field set of card c_wjo6wy: 时间戳/goal/verdict/
// pass-doubt-fail 计数/model/token 与成本/git Δ/JudgeError, plus the
// done/failed/task counts that make the row self-describing. Every
// field is always present (no omitempty): a ledger row that silently
// drops keys forces its reader to distinguish "absent" from "zero",
// and §9.1-style recomputation must not care.
type ReportLine struct {
	TS      string  `json:"ts"`      // RFC3339 UTC, run end
	Goal    string  `json:"goal"`    // capped at capGoalRunes
	Verdict string  `json:"verdict"` // run rollup: pass | doubt | fail | none
	Pass    int     `json:"pass"`    // VerifiedPass
	Doubt   int     `json:"doubt"`   // VerifiedDoubt
	Fail    int     `json:"fail"`    // VerifiedFail
	Done    int     `json:"done"`
	Failed  int     `json:"failed"`
	Tasks   int     `json:"tasks"` // len(Outcomes)
	Model   string  `json:"model"`
	Tokens  int     `json:"tokens"`   // TotalTokens (prompt+completion, cache hits included)
	CostUSD float64 `json:"cost_usd"` // TotalCostUSD via internal/pricing
	// git Δ summed over the run's local commits (the morning-review
	// footprint, v8 柱 R R1).
	Files      int `json:"files"`
	Insertions int `json:"insertions"`
	Deletions  int `json:"deletions"`
	// JudgeErrors collects each judged task's Verdict.JudgeError (run
	// order), each capped at capJudgeErrRunes; empty slice — never nil
	// and never omitted — when every judge call succeeded or no
	// verification ran.
	JudgeErrors []string `json:"judge_errors"`
}

// OverallVerdict folds the run's verdict counts into one value with
// the same precedence the verifier uses per task (composeOverall):
// fail beats doubt beats pass. "none" means nothing was judged — no
// verifier wired or no tasks — which is a different fact from any of
// the three verdicts and must not fold into "pass".
func (r Report) OverallVerdict() string {
	switch {
	case r.VerifiedFail > 0:
		return verifier.Fail
	case r.VerifiedDoubt > 0:
		return verifier.Doubt
	case r.VerifiedPass > 0:
		return verifier.Pass
	default:
		return "none"
	}
}

// Line projects a Report into its persistent row. model is the run's
// lane (Report itself doesn't carry it — the caller wired the fleet
// and knows it); ts is the run end (injected so tests can pin it).
func (r Report) Line(model string, ts time.Time) ReportLine {
	ln := ReportLine{
		TS:      ts.UTC().Format(time.RFC3339),
		Goal:    capRunes(r.Goal, capGoalRunes),
		Verdict: r.OverallVerdict(),
		Pass:    r.VerifiedPass,
		Doubt:   r.VerifiedDoubt,
		Fail:    r.VerifiedFail,
		Done:    r.Done,
		Failed:  r.Failed,
		Tasks:   len(r.Outcomes),
		Model:   model,
		Tokens:  r.TotalTokens,
		CostUSD: r.TotalCostUSD,
	}
	for _, o := range r.Outcomes {
		ln.Files += o.Files
		ln.Insertions += o.Insertions
		ln.Deletions += o.Deletions
		if o.Verdict != nil && o.Verdict.JudgeError != "" && len(ln.JudgeErrors) < capJudgeErrs {
			// `continue`-shaped by construction: the cap bounds HOW MANY
			// judge errors ride on one row, never how much git Δ gets
			// folded. An early `break` here would silently truncate
			// files/insertions/deletions for every outcome past the 9th
			// judge error (unreachable at the default MaxTasks=8, but
			// MaxTasks is caller-configurable — see
			// TestLine_JudgeErrCapDoesNotStopDeltaFold).
			ln.JudgeErrors = append(ln.JudgeErrors, capRunes(o.Verdict.JudgeError, capJudgeErrRunes))
		}
	}
	if ln.JudgeErrors == nil {
		ln.JudgeErrors = []string{} // always-present key, empty not absent
	}
	return ln
}

// appendLock serialises in-process appends to the ledger (same shape
// as subagent's indexLock). Cross-process concurrency falls to the
// O_APPEND single-write discipline below.
var appendLock sync.Mutex

// AppendReportLine appends one row to path as a single JSONL line.
//
// Line integrity (the c_wjo6wy acceptance ③ posture): the marshalled
// row + "\n" go out in ONE Write on an O_APPEND descriptor — the
// kernel serialises concurrent write()s on the same regular file
// (the exact discipline of subagents.jsonl / the cron run log /
// sessmeta audit), so neither in-process nor cross-process appenders
// can interleave. Field caps keep realistic rows under ~1 KiB. The
// one hole is a hard power-loss mid-flush, which can tear the
// TRAILING line only — same exposure as every other JSONL ledger
// under SEEK_HOME; readers skip a torn last line.
//
// Failure is returned, never fatal by design: callers log it to
// stderr and the already-printed Report stands (a lost ledger row
// must not fail an otherwise-completed unattended run).
func AppendReportLine(path string, ln ReportLine) error {
	body, err := json.Marshal(ln)
	if err != nil {
		return fmt.Errorf("autopilot: marshal report line: %w", err)
	}
	body = append(body, '\n')

	appendLock.Lock()
	defer appendLock.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("autopilot: mkdir report ledger parent: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("autopilot: open report ledger: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return fmt.Errorf("autopilot: write report ledger: %w", err)
	}
	return nil
}

// capRunes truncates s to at most n runes (rune-safe, no ellipsis —
// the ledger keeps raw prefixes, display truncation is Body()'s job).
func capRunes(s string, n int) string {
	if len(s) <= n {
		return s // fast path: bytes <= runes always
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
