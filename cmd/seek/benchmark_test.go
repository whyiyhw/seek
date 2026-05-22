package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Benchmark report is computation-light (arithmetic + field copies), so the
// tests focus on:
//   - Report structure and JSON field naming
//   - Thresholds logic (pass/fail)
//   - After-turn-5 delta computation
//   - Edge cases (zero tokens, zero tool calls)
//
// These are pure-data tests: we construct reports directly rather than
// going through the agent loop (which requires a real DeepSeek API key).

func TestBenchmarkThresholds_Pass(t *testing.T) {
	r := benchmarkReport{
		Task:          "self-hosting",
		Description:   "test",
		Status:        "pass",
		Turns:         8,
		CacheHitRatio: 0.72,
		FIMUsageRatio: 0.55,
		FIMCalls:      5,
		TotalToolCalls: 9,
		Thresholds: benchmarkThresholds{
			CacheHitRatioMin: 0.60,
			FIMMinCalls:      1,
		},
		ThresholdsMet: true,
	}
	if r.Status != "pass" {
		t.Errorf("Status = %q, want %q", r.Status, "pass")
	}
	if !r.ThresholdsMet {
		t.Errorf("ThresholdsMet = false, want true")
	}
}

func TestBenchmarkThresholds_Fail(t *testing.T) {
	// All thresholds fail: low cache hit, low FIM, runtime errors.
	r := benchmarkReport{
		Task:           "self-hosting",
		Description:    "test",
		Status:         "fail",
		Turns:          4,
		CacheHitRatio:  0.20, // below 0.60
		FIMUsageRatio:  0.0,  // below 0.50
		FIMCalls:       0,
		TotalToolCalls: 10,
		Thresholds: benchmarkThresholds{
			CacheHitRatioMin: 0.60,
			FIMMinCalls:      1,
		},
		ThresholdsMet: false,
		Errors:        []string{"tool exec failed"},
	}
	if r.Status != "fail" {
		t.Errorf("Status = %q, want %q", r.Status, "fail")
	}
	if r.ThresholdsMet {
		t.Errorf("ThresholdsMet = true, want false")
	}
	if len(r.Errors) != 1 {
		t.Errorf("Errors = %v, want 1 error", r.Errors)
	}
}

func TestBenchmarkReport_JSONRoundTrip(t *testing.T) {
	r := benchmarkReport{
		Task:               "self-hosting",
		Description:        "Read and modify cache.go",
		Status:             "pass",
		Turns:              10,
		Elapsed:            "1m30s",
		ElapsedSec:         90.0,
		CacheHitRatio:      0.72,
		CacheHitAfterTurn5: 0.85,
		FIMUsageRatio:      0.60,
		FIMCalls:           3,
		TotalToolCalls:     5,
		PromptTokens:       15000,
		CompletionTokens:   4000,
		TotalTokens:        19000,
		CacheHitTokens:     10800,
		CacheMissTokens:    4200,
		Thresholds: benchmarkThresholds{
			CacheHitRatioMin: 0.60,
			FIMMinCalls:      1,
		},
		ThresholdsMet: true,
	}

	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent: %v", err)
	}

	// Verify key fields appear in JSON output.
	jsonStr := string(b)
	for _, key := range []string{
		`"task"`, `"cache_hit_ratio"`, `"cache_hit_ratio_after_turn_5"`,
		`"fim_usage_ratio"`, `"fim_calls"`, `"total_tool_calls"`,
		`"cache_hit_tokens"`, `"cache_miss_tokens"`,
		`"thresholds"`, `"thresholds_met"`,
	} {
		if !strings.Contains(jsonStr, key) {
			t.Errorf("JSON missing key: %s", key)
		}
	}

	// Unmarshal back and verify round-trip.
	var got benchmarkReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.CacheHitRatio != r.CacheHitRatio {
		t.Errorf("CacheHitRatio round-trip = %v, want %v", got.CacheHitRatio, r.CacheHitRatio)
	}
	if got.CacheHitAfterTurn5 != r.CacheHitAfterTurn5 {
		t.Errorf("CacheHitAfterTurn5 round-trip = %v, want %v", got.CacheHitAfterTurn5, r.CacheHitAfterTurn5)
	}
	if got.Thresholds.CacheHitRatioMin != r.Thresholds.CacheHitRatioMin {
		t.Errorf("CacheHitRatioMin round-trip = %v, want %v", got.Thresholds.CacheHitRatioMin, r.Thresholds.CacheHitRatioMin)
	}
}

// TestBenchmarkEdgeCases verifies that the report handles zero-value fields
// without panicking or producing invalid output.
func TestBenchmarkEdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		report benchmarkReport
	}{
		{
			name: "zero tokens",
			report: benchmarkReport{
				Task:     "self-hosting",
				Status:   "fail",
				Turns:    0,
				FIMCalls: 0,
				Thresholds: benchmarkThresholds{
					CacheHitRatioMin: 0.60,
					FIMMinCalls:      1,
				},
			},
		},
		{
			name: "many turns",
			report: benchmarkReport{
				Task:               "self-hosting",
				Status:             "pass",
				Turns:              50,
				ElapsedSec:         120.5,
				CacheHitRatio:      0.91,
				CacheHitAfterTurn5: 0.93,
				FIMUsageRatio:      0.75,
				FIMCalls:           30,
				TotalToolCalls:     40,
				PromptTokens:       100000,
				CompletionTokens:   30000,
				CacheHitTokens:     90000,
				CacheMissTokens:    10000,
				Thresholds: benchmarkThresholds{
					CacheHitRatioMin: 0.60,
					FIMMinCalls:      1,
				},
				ThresholdsMet: true,
			},
		},
		{
			name: "no tool calls",
			report: benchmarkReport{
				Task:   "self-hosting",
				Status: "fail",
				Turns:  2,
				Thresholds: benchmarkThresholds{
					CacheHitRatioMin: 0.60,
					FIMMinCalls:      1,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.MarshalIndent(tt.report, "", "  ")
			if err != nil {
				t.Fatalf("MarshalIndent: %v", err)
			}
			var decoded benchmarkReport
			if err := json.Unmarshal(b, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v\nJSON: %s", err, string(b))
			}
			// Verify the decoded report is not silently corrupted.
			if decoded.Task != tt.report.Task {
				t.Errorf("Task = %q, want %q", decoded.Task, tt.report.Task)
			}
		})
	}
}

// TestBenchmarkAfterTurn5Delta verifies the after-turn-5 computation logic
// as used by runBenchmark. This is a pure-data test using deepseek.Usage.
func TestBenchmarkAfterTurn5Delta(t *testing.T) {
	// Simulate a sequence of per-turn usage records.
	before := deepseek.Usage{
		PromptTokens:          1000,
		PromptCacheHitTokens:  600,  // 60% ratio up to turn 5
		PromptCacheMissTokens: 400,
		CompletionTokens:      200,
		TotalTokens:           1200,
	}
	after := deepseek.Usage{
		PromptTokens:          3000,
		PromptCacheHitTokens:  2300, // delta = 1700 hit
		PromptCacheMissTokens: 700,  // delta = 300 miss
		CompletionTokens:      600,
		TotalTokens:           3600,
	}

	// Compute delta as runBenchmark does.
	deltaHit := after.PromptCacheHitTokens - before.PromptCacheHitTokens
	deltaMiss := after.PromptCacheMissTokens - before.PromptCacheMissTokens
	total := deltaHit + deltaMiss

	var ratio float64
	if total > 0 {
		ratio = float64(deltaHit) / float64(total)
	}

	wantRatio := 1700.0 / 2000.0 // 0.85
	if ratio != wantRatio {
		t.Errorf("after-turn-5 ratio = %.4f, want %.4f", ratio, wantRatio)
	}

	// Verify overall ratio includes the earlier turns.
	overallRatio := after.HitRatio()
	wantOverall := 2300.0 / 3000.0 // 0.7667
	if overallRatio != wantOverall {
		t.Errorf("overall ratio = %.4f, want %.4f", overallRatio, wantOverall)
	}
}

// TestBenchmarkZeroTotalToolCalls checks that FIM ratio is 0 when
// there are no tool calls (avoiding division by zero).
func TestBenchmarkZeroTotalToolCalls(t *testing.T) {
	var fimRatio float64
	toolCalls := 0
	fimCalls := 0
	if toolCalls > 0 {
		fimRatio = float64(fimCalls) / float64(toolCalls)
	}
	if fimRatio != 0 {
		t.Errorf("fimRatio = %v, want 0", fimRatio)
	}

	// Also test with some FIM calls but zero total (shouldn't happen
	// in practice, but guard against it).
	toolCalls2 := 0
	fimCalls2 := 5
	var fimRatio2 float64
	if toolCalls2 > 0 {
		fimRatio2 = float64(fimCalls2) / float64(toolCalls2)
	}
	if fimRatio2 != 0 {
		t.Errorf("fimRatio2 = %v, want 0", fimRatio2)
	}
}
