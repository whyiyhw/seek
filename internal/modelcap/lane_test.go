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

// TestLaneOf_OpenDesignCoversMenu is the OpenDesign half: an id offered
// ONLY on that lane must resolve to it, and an id the Go lane also
// carries must keep resolving to opencode — the documented tie-break
// that keeps this function's older answers byte-identical.
func TestLaneOf_OpenDesignCoversMenu(t *testing.T) {
	goRows := make(map[string]bool, len(pricing.OpenCodeGoModels))
	for _, row := range pricing.OpenCodeGoModels {
		goRows[row.ID] = true
	}
	exclusive := 0
	for _, row := range pricing.OpenDesignModels {
		lane, label := LaneOf(row.ID)
		switch {
		case sharedFirstTier[row.ID] != "":
			continue // first-tier lane wins over both pools
		case goRows[row.ID]:
			if lane != "opencode" {
				t.Errorf("%s: shared with the Go lane, resolves to %q; the documented tie-break says opencode", row.ID, lane)
			}
		default:
			exclusive++
			if lane != "opendesign" || label != "OpenDesign 网关" {
				t.Errorf("opendesign-only row %q resolves to %q/%q, want opendesign — the menu and LaneOf drifted", row.ID, lane, label)
			}
		}
	}
	if exclusive == 0 {
		t.Error("no OpenDesign-exclusive ids — openDesignIDs is doing nothing; re-check the menus")
	}
}

// TestServes pins the routing companion's contract (c_dxrjjl): the lane
// MENU, not LaneOf's ownership answer. Shared ids are the crux — LaneOf
// resolves them to the older Go claim while BOTH gateways carry them, and
// a startup gate that reasoned from LaneOf alone would refuse the
// OpenDesign lane's own default model.
func TestServes(t *testing.T) {
	cases := []struct {
		lane, model string
		want        bool
	}{
		{LaneOpenDesign, "deepseek-v4.1-flash-eco", true}, // OpenDesign-exclusive
		{LaneOpenCode, "deepseek-v4.1-flash-eco", false},
		{LaneOpenDesign, pricing.ModelOpenDesignV41Flash, true}, // shared default
		{LaneOpenCode, pricing.ModelOpenDesignV41Flash, true},   // …served by the Go lane too
		{LaneOpenDesign, "kimi-k3", false},                      // Go-only id
		{LaneOpenCode, "kimi-k3", true},
		{LaneOpenDesign, "deepseek-flash", false}, // first-tier id
		{LaneOpenCode, "deepseek-flash", false},
		{"deepseek", "deepseek-v4.1-flash-eco", false}, // no gateway menu
		{"", "deepseek-v4.1-flash-eco", false},
		{LaneOpenDesign, "", false},
	}
	for _, c := range cases {
		if got := Serves(c.lane, c.model); got != c.want {
			t.Errorf("Serves(%q, %q) = %v, want %v", c.lane, c.model, got, c.want)
		}
	}
}

// TestServes_MenuCompleteness is the "no second table" guard from the
// routing side: every row either menu carries must be served by its own
// lane, and every exclusive row of one menu must be refused by the other.
func TestServes_MenuCompleteness(t *testing.T) {
	for _, row := range pricing.OpenDesignModels {
		if !Serves(LaneOpenDesign, row.ID) {
			t.Errorf("OpenDesign menu row %q is not served by its own lane", row.ID)
		}
	}
	for _, row := range pricing.OpenCodeGoModels {
		if !Serves(LaneOpenCode, row.ID) {
			t.Errorf("Go menu row %q is not served by its own lane", row.ID)
		}
	}
}
