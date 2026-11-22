package main

// Card c_uzlczr (ACP P0-4): context-window reporting on the ACP lane.
// These tests pin the two things the frame must get right and one it must
// not: the numbers come from budget (same authority as the TUI/Web
// meters), the alert fires once per crossing rather than every turn, and
// nothing is sent when the window is unusable.

import (
	"testing"

	"github.com/whyiyhw/seek/internal/acp"
	"github.com/whyiyhw/seek/internal/budget"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// ctxOf pulls _meta.seek.ctx out of a usage_update (fails the test if the
// nesting slipped — it is client-visible contract).
func ctxOf(t *testing.T, u acp.SessionUpdate) map[string]any {
	t.Helper()
	m, ok := u.Update.(map[string]any)
	if !ok {
		t.Fatalf("update is not a map: %#v", u.Update)
	}
	if m["sessionUpdate"] != "usage_update" {
		t.Fatalf("sessionUpdate = %v, want usage_update", m["sessionUpdate"])
	}
	meta, _ := m["_meta"].(map[string]any)
	seek, _ := meta["seek"].(map[string]any)
	ctx, ok := seek["ctx"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.seek.ctx missing: %#v", m)
	}
	return ctx
}

// TestACPCtxMeter_LowUsage: a small turn on a 1M window gives one plain
// frame — used/size from budget, percent 0, severity ok, and NO alert.
func TestACPCtxMeter_LowUsage(t *testing.T) {
	m := &acpCtxMeter{alerts: true}
	ups := m.update("s1", "glm-5.3", deepseek.Usage{PromptTokens: 5_000, CompletionTokens: 12})
	if len(ups) != 1 {
		t.Fatalf("frames = %d, want 1 (low usage must not alert)", len(ups))
	}
	upd := ups[0].Update.(map[string]any)
	if upd["used"] != 5_012 {
		t.Fatalf("used = %v, want prompt+completion 5012", upd["used"])
	}
	if want := budget.Limit("glm-5.3"); upd["size"] != want {
		t.Fatalf("size = %v, want budget.Limit = %d", upd["size"], want)
	}
	ctx := ctxOf(t, ups[0])
	if ctx["percent"] != 0 || ctx["severity"] != acp.CtxSeverityOK {
		t.Fatalf("ctx = %v, want percent=0 severity=ok", ctx)
	}
	if _, has := ctx["alert"]; has {
		t.Fatalf("low usage must not carry an alert: %v", ctx)
	}
}

// TestACPCtxMeter_NearFullAlertsOncePerCrossing walks the meter up
// through the thresholds: the first critical turn adds the alert frame
// (with the /compact nudge, formatBudget's wording), the next critical
// turn is plain again, and dropping below the line then re-crossing it
// alerts a second time.
func TestACPCtxMeter_NearFullAlertsOncePerCrossing(t *testing.T) {
	m := &acpCtxMeter{alerts: true}
	turn := func(prompt, completion int) []acp.SessionUpdate {
		return m.update("s1", "glm-5.3", deepseek.Usage{PromptTokens: prompt, CompletionTokens: completion})
	}

	// 70% → warn, first crossing: two frames, the second flagged.
	ups := turn(700_000, 0)
	if len(ups) != 2 {
		t.Fatalf("frames = %d, want 2 at the first warn crossing", len(ups))
	}
	ctx := ctxOf(t, ups[1])
	if ctx["alert"] != acp.CtxSeverityWarn || ctx["severity"] != acp.CtxSeverityWarn {
		t.Fatalf("alert ctx = %v, want warn/warn", ctx)
	}
	if ctx["notice"] != "ctx 70%" {
		t.Fatalf("warn notice = %v, want \"ctx 70%%\" (no nudge yet)", ctx["notice"])
	}

	// Still in warn: plain frame only — the meter moved, the alarm did
	// not.
	if ups := turn(720_000, 0); len(ups) != 1 {
		t.Fatalf("frames = %d, want 1 while staying inside warn", len(ups))
	}

	// 80% → critical crossing: alert again, this time with the nudge.
	ups = turn(800_000, 12)
	if len(ups) != 2 {
		t.Fatalf("frames = %d, want 2 at the first critical crossing", len(ups))
	}
	ctx = ctxOf(t, ups[1])
	if ctx["alert"] != acp.CtxSeverityCritical {
		t.Fatalf("alert = %v, want critical", ctx["alert"])
	}
	if notice, _ := ctx["notice"].(string); notice != "ctx 80% — /compact soon" {
		t.Fatalf("critical notice = %q, want formatBudget's wording", notice)
	}

	// Back down (a /compact would do this): no alert, and the memory
	// resets so the next crossing is a crossing again.
	if ups := turn(10_000, 0); len(ups) != 1 {
		t.Fatalf("frames = %d after compacting back down, want 1", len(ups))
	}
	ups = turn(900_000, 0)
	if len(ups) != 2 {
		t.Fatalf("frames = %d, want the alert to fire again after re-crossing", len(ups))
	}

	// reset() (session/load) forgets the level without touching alerts.
	m.reset()
	ups = turn(950_000, 0)
	if len(ups) != 2 {
		t.Fatalf("frames = %d after reset, want the resumed session to warn", len(ups))
	}
}

// TestACPCtxMeter_AlertsDisabled: with SEEK_ACP_CTX_ALERT off the plain
// frame still goes out every turn (the meter keeps working), only the
// alert marker is gone.
func TestACPCtxMeter_AlertsDisabled(t *testing.T) {
	m := &acpCtxMeter{alerts: false}
	ups := m.update("s1", "glm-5.3", deepseek.Usage{PromptTokens: 900_000})
	if len(ups) != 1 {
		t.Fatalf("frames = %d, want 1 with alerts disabled", len(ups))
	}
	ctx := ctxOf(t, ups[0])
	if ctx["severity"] != acp.CtxSeverityCritical {
		t.Fatalf("severity = %v, want the level still reported", ctx["severity"])
	}
	if _, has := ctx["alert"]; has {
		t.Fatalf("alerts are off — no alert key: %v", ctx)
	}
}

// TestACPCtxMeter_UnknownModelFallsBackToBudgetDefault: an id the table
// has never heard of still gets a real window (budget.Default), so the
// client sees a meter rather than silence. Zero window is the only
// silent case the code has, and budget.Limit cannot return one — the
// guard is defensive.
func TestACPCtxMeter_UnknownModelFallsBackToBudgetDefault(t *testing.T) {
	m := &acpCtxMeter{}
	ups := m.update("s1", "some-unlisted-model", deepseek.Usage{PromptTokens: 1_000})
	if len(ups) != 1 {
		t.Fatalf("frames = %d, want 1", len(ups))
	}
	if upd := ups[0].Update.(map[string]any); upd["size"] != budget.Default {
		t.Fatalf("size = %v, want budget.Default %d", upd["size"], budget.Default)
	}
}
