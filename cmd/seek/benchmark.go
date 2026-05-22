// Package main contains the seek CLI entrypoint. This file implements the
// --benchmark flag: a self-hosting benchmark that runs seek against its own
// codebase and reports cache hit ratio, FIM usage ratio, and timing metrics.
//
// Design (PRD §v1.0):
//   - "自举 benchmark — 让 seek 自己读、改、运行自己的仓库，验证缓存命中率 ≥ 60%"
//   - "FIM 快路径：模型在需要小范围补丁时调用 fim_complete 的比例 ≥ 50%（需 benchmark）"
//
// The benchmark reuses the same agent loop as runPrint/runJSON, consuming
// the event stream to record per-turn Usage (cache hit/miss) and tool
// invocation counts. After the agent finishes, it computes aggregate metrics
// and writes a structured JSON report.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/cache"
	"github.com/whyiyhw/seek/internal/session"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ---------------------------------------------------------------------------
// Benchmark task definitions
// ---------------------------------------------------------------------------

// benchmarkTask describes one predefined benchmark task.
//
// FIMMinCalls is the per-task acceptance threshold for fim_complete
// invocations. A ratio (FIM / total tool calls) is unsound: total tool
// calls includes read/bash/grep which can't use FIM, AND each FIM call
// must be paired with an edit to write its output to disk (see ch6
// §6.5), bounding any ratio metric to ~50%. Asking "did the model
// take the FIM path at least N times in a task that's supposed to
// exercise FIM" is the metric this actually measures. PRD §6 was
// updated alongside this code to record the same finding.
//
// FIMMinCalls = 0 means "this task doesn't exercise FIM" — pass is
// automatic regardless of fim_complete usage.
type benchmarkTask struct {
	Name        string
	Description string
	Prompt      string
	FIMMinCalls int
}

// benchmarkPresets is the registry of recognised --benchmark task names.
var benchmarkPresets = map[string]benchmarkTask{
	"self-hosting": {
		Name:        "self-hosting",
		Description: "Read internal/cache/cache.go, add GetHitRatio(), run go test",
		Prompt: `Read internal/cache/cache.go. Add a new exported method GetHitRatio() float64 that returns the cumulative prefix-cache hit ratio across all recorded turns (it should delegate to t.Cumulative().HitRatio()). Then run go test ./internal/cache/ to verify the code compiles and tests pass. After the tests pass, revert your change so the workspace is clean (use 'git checkout -- internal/cache/cache.go').`,
		FIMMinCalls: 0, // self-hosting is a cache-prefix test; FIM not required
	},
	"fim-patch": {
		Name:        "fim-patch",
		Description: "Read pkg/deepseek/types.go, add a String() method to Usage via FIM, run go vet",
		Prompt: `Read pkg/deepseek/types.go. The Usage struct has a HitRatio() method. Add a String() method to Usage that returns a human-readable summary like 'prompt=100 hit=80 miss=20 ratio=80.0%'. Place it directly after the HitRatio() method body. Use fim_complete to fill in the gap if the edit is small and well-defined. Then run 'go vet ./pkg/deepseek/' to verify the code compiles. Finally, revert your changes with 'git checkout -- pkg/deepseek/types.go'.`,
		FIMMinCalls: 1, // one small edit; one FIM call = FIM path traversed
	},
}

// ---------------------------------------------------------------------------
// Benchmark report types
// ---------------------------------------------------------------------------

// benchmarkThresholds encodes the acceptance criteria for a benchmark run.
//
// FIMMinCalls replaces the original FIMUsageMin ratio: a ratio against
// total tool calls is unsound because non-editable tools (read, bash,
// grep, list_dir) dilute the denominator, and FIM calls must be paired
// with edits (ch6 §6.5) which bounds any FIM/edit ratio to ~50%.
// Per-task absolute call count is the metric that actually answers
// "did the FIM path get exercised".
type benchmarkThresholds struct {
	CacheHitRatioMin float64 `json:"cache_hit_ratio_min"` // e.g. 0.60
	FIMMinCalls      int     `json:"fim_min_calls"`       // 0 = N/A
}

// benchmarkReport is the top-level JSON output.
type benchmarkReport struct {
	Task               string              `json:"task"`
	Description        string              `json:"description"`
	Status             string              `json:"status"` // "pass" or "fail"
	Turns              int                 `json:"turns"`
	Elapsed            string              `json:"elapsed"`
	ElapsedSec         float64             `json:"elapsed_sec"`
	CacheHitRatio      float64             `json:"cache_hit_ratio"`
	CacheHitAfterTurn5 float64             `json:"cache_hit_ratio_after_turn_5"`
	FIMUsageRatio      float64             `json:"fim_usage_ratio"`
	FIMCalls           int                 `json:"fim_calls"`
	TotalToolCalls     int                 `json:"total_tool_calls"`
	PromptTokens       int                 `json:"prompt_tokens"`
	CompletionTokens   int                 `json:"completion_tokens"`
	TotalTokens        int                 `json:"total_tokens"`
	CacheHitTokens     int                 `json:"cache_hit_tokens"`
	CacheMissTokens    int                 `json:"cache_miss_tokens"`
	Thresholds         benchmarkThresholds `json:"thresholds"`
	ThresholdsMet      bool                `json:"thresholds_met"`
	Errors             []string            `json:"errors,omitempty"`
}

// ---------------------------------------------------------------------------
// Per-turn snapshot used for the after-turn-5 computation
// ---------------------------------------------------------------------------

type turnSnapshot struct {
	Turn         int
	Usage        deepseek.Usage
	FIMCalls     int
	ToolCalls    int
}

// ---------------------------------------------------------------------------
// main entry
// ---------------------------------------------------------------------------

// runBenchmark executes a benchmark task and writes the JSON report.
//
// It follows the same pattern as runPrint/runJSON: build an agent, call
// Prompt, consume the event stream to collect metrics, then produce a report.
//
// The function is designed to be called from run() when --benchmark is set.
// It owns its own context (with a generous timeout) so a stuck benchmark
// doesn't hang forever.
func runBenchmark(ctx context.Context, taskName, outputPath string,
	ag *agent.Agent, tracker *cache.Tracker,
	model string, activeSession *session.Session, store *session.Store,
) error {
	task, ok := benchmarkPresets[taskName]
	if !ok {
		names := make([]string, 0, len(benchmarkPresets))
		for n := range benchmarkPresets {
			names = append(names, n)
		}
		return fmt.Errorf("unknown benchmark task %q (known: %s)", taskName, strings.Join(names, ", "))
	}

	start := time.Now()
	var (
		turns        int
		toolCalls    int
		fimCalls     int
		snapshots    []turnSnapshot
		runErrors    []string
		usageAtTurn5 deepseek.Usage // cumulative usage after turn 5
		savedAt5     bool
	)

	for ev := range ag.Prompt(ctx, task.Prompt) {
		switch e := ev.(type) {

		case agent.ToolExecStart:
			if e.Name == "fim_complete" {
				fimCalls++
			}
			toolCalls++

		case agent.TurnEnd:
			tracker.Record(e.Usage)
			turns++
			// Record a snapshot for after-turn-5 computation.
			cum := tracker.Cumulative()
			snapshots = append(snapshots, turnSnapshot{
				Turn:      turns,
				Usage:     cum,
				FIMCalls:  fimCalls,
				ToolCalls: toolCalls,
			})
			if turns >= 5 && !savedAt5 {
				usageAtTurn5 = cum
				savedAt5 = true
			}
			// Persist session progress so interrupt doesn't lose everything.
			if activeSession != nil && store != nil {
				activeSession.Messages = ag.Messages()
				activeSession.Turns = turns
				activeSession.ToolCalls = toolCalls
				activeSession.Usage = cum
				activeSession.Model = model
				if err := store.Save(activeSession); err != nil {
					fmt.Fprintf(os.Stderr, "benchmark: warning: failed to save session: %v\n", err)
				}
			}

		case agent.AgentEnd:
			// Final cumulative stats already captured via last TurnEnd.

		case agent.ErrorEvent:
			runErrors = append(runErrors, e.Err.Error())
		}
	}

	elapsed := time.Since(start)
	finalUsage := tracker.Cumulative()

	// --- Compute cache hit ratio after turn 5 ---
	var cacheHitAfterTurn5 float64
	if savedAt5 {
		deltaHit := finalUsage.PromptCacheHitTokens - usageAtTurn5.PromptCacheHitTokens
		deltaMiss := finalUsage.PromptCacheMissTokens - usageAtTurn5.PromptCacheMissTokens
		total := deltaHit + deltaMiss
		if total > 0 {
			cacheHitAfterTurn5 = float64(deltaHit) / float64(total)
		}
	}

	// --- Compute FIM usage ratio ---
	// FIM usage ratio = FIM calls / total tool calls.
	// If totalToolCalls is 0, ratio is 0.
	var fimRatio float64
	if toolCalls > 0 {
		fimRatio = float64(fimCalls) / float64(toolCalls)
	}

	// --- Thresholds ---
	// FIM threshold is per-task absolute call count; the per-run ratio
	// is still reported for visibility but no longer used in the
	// pass/fail verdict (see benchmarkTask.FIMMinCalls doc comment).
	thresholds := benchmarkThresholds{
		CacheHitRatioMin: 0.60,
		FIMMinCalls:      task.FIMMinCalls,
	}
	cachePass := finalUsage.HitRatio() >= thresholds.CacheHitRatioMin
	fimPass := fimCalls >= thresholds.FIMMinCalls
	thresholdsMet := cachePass && fimPass

	// --- Build report ---
	report := benchmarkReport{
		Task:               task.Name,
		Description:        task.Description,
		Status:             "pass",
		Turns:              turns,
		Elapsed:            elapsed.Round(time.Millisecond).String(),
		ElapsedSec:         elapsed.Seconds(),
		CacheHitRatio:      finalUsage.HitRatio(),
		CacheHitAfterTurn5: cacheHitAfterTurn5,
		FIMUsageRatio:      fimRatio,
		FIMCalls:           fimCalls,
		TotalToolCalls:     toolCalls,
		PromptTokens:       finalUsage.PromptTokens,
		CompletionTokens:   finalUsage.CompletionTokens,
		TotalTokens:        finalUsage.TotalTokens,
		CacheHitTokens:     finalUsage.PromptCacheHitTokens,
		CacheMissTokens:    finalUsage.PromptCacheMissTokens,
		Thresholds:         thresholds,
		ThresholdsMet:      thresholdsMet,
		Errors:             runErrors,
	}
	if !thresholdsMet || len(runErrors) > 0 {
		report.Status = "fail"
	}

	// --- Always print human-readable summary to stderr ---
	fmt.Fprintf(os.Stderr, "\n--- benchmark: %s ---\n", task.Name)
	fmt.Fprintf(os.Stderr, "status:          %s\n", report.Status)
	fmt.Fprintf(os.Stderr, "turns:           %d\n", turns)
	fmt.Fprintf(os.Stderr, "elapsed:         %s\n", report.Elapsed)
	fmt.Fprintf(os.Stderr, "prompt tok:      %d (hit %d / miss %d, ratio %.1f%%)\n",
		finalUsage.PromptTokens, finalUsage.PromptCacheHitTokens, finalUsage.PromptCacheMissTokens,
		finalUsage.HitRatio()*100)
	if savedAt5 {
		fmt.Fprintf(os.Stderr, "cache ratio (turn 6+): %.1f%% (threshold: ≥%.0f%%)\n",
			cacheHitAfterTurn5*100, thresholds.CacheHitRatioMin*100)
	}
	fmt.Fprintf(os.Stderr, "FIM calls:       %d / %d tool calls (ratio %.1f%%, threshold: ≥%d call%s)\n",
		fimCalls, toolCalls, fimRatio*100,
		thresholds.FIMMinCalls, plural(thresholds.FIMMinCalls))
	fmt.Fprintf(os.Stderr, "thresholds met:  %v\n", thresholdsMet)

	// --- Write JSON report ---
	enc := json.NewEncoder(os.Stdout)
	if outputPath != "" {
		f, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("benchmark: create output: %w", err)
		}
		defer f.Close()
		enc = json.NewEncoder(f)
	}
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("benchmark: encode report: %w", err)
	}

	if !thresholdsMet {
		return fmt.Errorf("benchmark: thresholds not met (cache: %.1f%% ≥ %.0f%% = %v, FIM: %d ≥ %d = %v)",
			finalUsage.HitRatio()*100, thresholds.CacheHitRatioMin*100, cachePass,
			fimCalls, thresholds.FIMMinCalls, fimPass)
	}
	return nil
}

// plural returns "" for 1, "s" otherwise — small helper for the human-
// readable summary line so we don't print "1 calls".
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
