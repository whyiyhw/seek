package modelcap

// Lane determination tests (W5 card c_oyp2qf): the acceptance sample is
// the model FAMILY that straddles two lanes — DeepSeek V4.1 Flash is
// "deepseek-flash" on the direct API lane and "deepseek-v4.1-flash" on
// the OpenCode Go gateway, two wire ids for one model family. The web
// ContextMeter displays exactly these labels, so the mapping here IS the
// backend determination the display must agree with.

import (
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
)

func TestLaneOf(t *testing.T) {
	cases := []struct {
		model    string
		wantLane string
		wantLbl  string
	}{
		// The two-lane sample pair (acceptance ③): same family, two ids.
		{"deepseek-flash", "deepseek", "DeepSeek 直连"},
		{"deepseek-v4.1-flash", "opencode", "OpenCode 网关"},
		// Go pool membership, not prefix, decides the pool rows: kimi/longcat
		// have no vendor prefix of their own yet are Go-gateway ids.
		{"kimi-k3", "opencode", "OpenCode 网关"},
		{"beam-free", "opencode", "OpenCode 网关"},
		// Shared first-tier ids win over pool membership (the pricing
		// tie-break, directLaneIDs).
		{"glm-5.3", "glm", "GLM"},
		{"deepseek-v4-pro", "deepseek", "DeepSeek 直连"},
		// Curated second-tier defaults by prefix.
		{"claude-sonnet-4-20250514", "anthropic", "Anthropic"},
		{"gpt-4o", "openai", "OpenAI"},
		{"gemini-2.0-flash", "gemini", "Gemini"},
		{"glm-5.3-flash", "opencode", "OpenCode 网关"}, // pool row, not the shared id
		// Unknowns and degenerate input: no lane, no label — the caller hides.
		{"", "", ""},
		{"mystery-model", "", ""},
		{"deepseek-", "", ""}, // prefix alone (no tail) must not match
	}
	for _, c := range cases {
		lane, label := LaneOf(c.model)
		if lane != c.wantLane || label != c.wantLbl {
			t.Errorf("LaneOf(%q) = %q, %q; want %q, %q", c.model, lane, label, c.wantLane, c.wantLbl)
		}
	}
}

// TestLaneOf_PoolCoversMenu pins the "no second table" rule from the other
// direction: every id in the Go lane's menu resolves to the opencode lane
// except the shared first-tier ids — if a future pool row stops resolving,
// the table here (or pricing's) has drifted.
func TestLaneOf_PoolCoversMenu(t *testing.T) {
	for _, row := range pricing.OpenCodeGoModels {
		lane, label := LaneOf(row.ID)
		if sharedFirstTier[row.ID] != "" {
			continue // shared ids resolve to their first-tier lane
		}
		if lane != "opencode" || label != "OpenCode 网关" {
			t.Errorf("pool row %q resolves to %q/%q, want opencode — the menu and LaneOf drifted", row.ID, lane, label)
		}
	}
}
