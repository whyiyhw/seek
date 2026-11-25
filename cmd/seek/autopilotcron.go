package main

// The cron-fired autopilot lane resolver + billing gate (v8 §9.5③1,
// card c_kdblc4). `seek autopilot run` is unattended: whatever the
// spawner decided has to travel by env, because flag.Parse stops at
// the "autopilot" positional (the SEEK_ACP_PROVIDER shape). The env
// NAMES live in internal/routines (routines.EnvAutopilot*) — the cron
// tick is the only authorised stamper of the marker; this file is the
// only reader that turns the marker into lane behaviour.
//
// The standing rule this enforces (v8 §9.1, #9336): an unattended run
// must not burn a metered (pay-per-token) key unless someone said so
// in writing. Hence:
//
//   - cron-fired + no provider pin  → opencode, the flat-rate default
//     (the pin deliberately wins over buildProvider's auto-detect,
//     which would otherwise happily pick up any DEEPSEEK_API_KEY lying
//     in the scheduler's env — "landed but billing the wrong lane").
//   - cron-fired + metered pin      → refused unless
//     SEEK_AUTOPILOT_ALLOW_METERED names the approver
//     (e.g. "approved-by:c_kdblc4"). Subscription lanes need no
//     approver.
//   - not cron-fired (human typed it) → no pin, no gate: today's
//     behaviour, unchanged.

import (
	"context"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/routines"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// autopilotSubscriptionLanes are the providers billed per request
// window rather than per token — the lanes an unattended run may use
// with no approver on record. Everything else buildProvider knows
// (deepseek / anthropic / openai / gemini / glm) is metered. GLM's
// Coding Plan is a subscription, but seek's default GLM endpoint is
// the recharge-billed /api/paas/v4 path, so the lane as seek builds
// it is treated as metered — a Coding-Plan operator who wants it
// unattended names the approver like any other metered lane.
var autopilotSubscriptionLanes = map[string]bool{
	"opencode":   true, // $10/mo flat, request-window billed
	"opendesign": true, // $120/mo allowance, request-window billed
}

// autopilotDefaultLane is the provider a cron-fired run uses when
// SEEK_AUTOPILOT_PROVIDER is empty. The OpenCode Go lane: flat-rate,
// and the lane autopilot's workers already know how to ride.
const autopilotDefaultLane = "opencode"

// autopilotCronLane is the resolved shape of the routines.EnvAutopilot*
// env contract. The zero value is "not cron-fired" — pin and gate are
// both no-ops there.
type autopilotCronLane struct {
	cron         bool
	provider     string // SEEK_AUTOPILOT_PROVIDER, trimmed
	model        string // SEEK_AUTOPILOT_MODEL, trimmed
	allowMetered string // SEEK_AUTOPILOT_ALLOW_METERED (approver name), trimmed
}

// resolveAutopilotCronLane reads the env contract. Any non-empty
// SEEK_AUTOPILOT_CRON counts as cron-fired: the tick always stamps
// "1", and a human who sets it by hand is deliberately simulating the
// cron lane (they get the billing gate too — that is the safe
// direction to err).
func resolveAutopilotCronLane(getenv func(string) string) autopilotCronLane {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	marker := strings.TrimSpace(getenv(routines.EnvAutopilotCron))
	return autopilotCronLane{
		cron:         marker != "",
		provider:     strings.TrimSpace(getenv(routines.EnvAutopilotProvider)),
		model:        strings.TrimSpace(getenv(routines.EnvAutopilotModel)),
		allowMetered: strings.TrimSpace(getenv(routines.EnvAutopilotAllowMetered)),
	}
}

// PinProvider returns the provider a cron-fired run must run on (""
// when not cron-fired — leave buildProvider's auto-detect alone).
func (l autopilotCronLane) PinProvider() string {
	if !l.cron {
		return ""
	}
	if l.provider != "" {
		return l.provider
	}
	return autopilotDefaultLane
}

// PinModel returns the pinned model id ("" = the lane's default).
func (l autopilotCronLane) PinModel() string {
	if !l.cron {
		return ""
	}
	return l.model
}

// GateErr is the billing gate: nil unless this is a cron-fired run
// whose effective lane is metered with no approver on record. The
// refusal names both escape routes — pin a subscription lane, or name
// the approver — because either one is a legitimate answer.
func (l autopilotCronLane) GateErr() error {
	if !l.cron {
		return nil
	}
	lane := l.PinProvider()
	if autopilotSubscriptionLanes[lane] {
		return nil
	}
	if l.allowMetered != "" {
		return nil
	}
	return fmt.Errorf(
		"autopilot cron: lane %q is metered (pay-per-token) and an unattended run refuses it without a named approver.\n"+
			"  - pin a subscription lane instead: %s=opencode (or opendesign) — no approval needed; or\n"+
			"  - record who approved the burn: %s=approved-by:<card-or-person>",
		lane, routines.EnvAutopilotProvider, routines.EnvAutopilotAllowMetered)
}

// LogLine is the stderr line main prints once the lane is resolved —
// the audit trail the approver convention needs (a named approver that
// never appears anywhere is not "in writing").
func (l autopilotCronLane) LogLine() string {
	if !l.cron {
		return ""
	}
	line := fmt.Sprintf("autopilot: cron-fired run pinned to lane %s", l.PinProvider())
	if m := l.PinModel(); m != "" {
		line += " model " + m
	}
	if !autopilotSubscriptionLanes[l.PinProvider()] {
		line += fmt.Sprintf(" (metered, %s=%q)", routines.EnvAutopilotAllowMetered, l.allowMetered)
	}
	return line
}

// llmChatClient adapts an llm.Provider (second-tier lane) to the
// single-shot Chat subset the autopilot decomposer and the verifier
// judge consume (DecomposeClient / verifier.JudgeClient). The
// decomposer and judge never send tools and never expect tool calls —
// one assistant text turn is the whole exchange — so the translation
// is content-only and the stream drains into one string. Token
// accounting rides TurnDone; cache metadata does not exist on
// second-tier lanes (the documented degradation) and reports as zero.
type llmChatClient struct {
	p llm.Provider
}

// Chat runs one non-interactive exchange over the provider's stream.
func (c llmChatClient) Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error) {
	if c.p == nil {
		return nil, fmt.Errorf("autopilot: no provider on the cron lane")
	}
	msgs := make([]llm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, llm.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	ch, err := c.p.ChatStream(ctx, llm.ChatRequest{
		Model:     req.Model,
		Messages:  msgs,
		MaxTokens: req.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	resp := &deepseek.ChatResponse{}
	for ev := range ch {
		switch e := ev.(type) {
		case llm.TextDelta:
			sb.WriteString(e.Delta)
		case llm.ErrorEvent:
			return nil, fmt.Errorf("provider stream: %w", e.Err)
		case llm.TurnDone:
			resp.Usage = deepseek.Usage{
				PromptTokens:     e.InputTokens,
				CompletionTokens: e.OutputTokens,
				TotalTokens:      e.InputTokens + e.OutputTokens,
			}
		}
	}
	content := sb.String()
	if content == "" && resp.Usage.TotalTokens == 0 {
		return nil, fmt.Errorf("provider stream ended without content or usage")
	}
	resp.Choices = []deepseek.Choice{{Message: deepseek.Message{
		Role:    deepseek.RoleAssistant,
		Content: content,
	}}}
	return resp, nil
}
