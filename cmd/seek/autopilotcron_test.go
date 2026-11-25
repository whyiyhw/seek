package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/internal/routines"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// --- resolver -------------------------------------------------------------

func envMap(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// Not cron-fired: no pin (buildProvider auto-detect stays in charge),
// no gate. A human typing `seek autopilot run` is untouched.
func TestResolveAutopilotCronLane_NotCronNoop(t *testing.T) {
	l := resolveAutopilotCronLane(envMap(map[string]string{
		"DEEPSEEK_API_KEY": "sk-leak",
	}))
	if l.cron {
		t.Fatal("no marker → not cron")
	}
	if got := l.PinProvider(); got != "" {
		t.Fatalf("no pin expected off-cron, got %q", got)
	}
	if got := l.PinModel(); got != "" {
		t.Fatalf("no model pin expected off-cron, got %q", got)
	}
	if err := l.GateErr(); err != nil {
		t.Fatalf("no gate off-cron: %v", err)
	}
}

// Cron-fired with no pins: defaults to the opencode subscription lane
// — never to buildProvider's auto-detect, which would pick up any
// DEEPSEEK_API_KEY sitting in the scheduler's env.
func TestResolveAutopilotCronLane_DefaultsToSubscription(t *testing.T) {
	l := resolveAutopilotCronLane(envMap(map[string]string{
		routines.EnvAutopilotCron: "1",
		"DEEPSEEK_API_KEY":        "sk-metered-lying-around",
	}))
	if !l.cron {
		t.Fatal("marker set → cron")
	}
	if got := l.PinProvider(); got != "opencode" {
		t.Fatalf("cron default lane = %q, want opencode", got)
	}
	if err := l.GateErr(); err != nil {
		t.Fatalf("subscription default must pass the gate: %v", err)
	}
}

func TestResolveAutopilotCronLane_PinHonoured(t *testing.T) {
	l := resolveAutopilotCronLane(envMap(map[string]string{
		routines.EnvAutopilotCron:     "1",
		routines.EnvAutopilotProvider: "opendesign",
		routines.EnvAutopilotModel:    "some-model",
	}))
	if got := l.PinProvider(); got != "opendesign" {
		t.Fatalf("pin = %q, want opendesign", got)
	}
	if got := l.PinModel(); got != "some-model" {
		t.Fatalf("model pin = %q", got)
	}
	if err := l.GateErr(); err != nil {
		t.Fatalf("subscription pin must pass: %v", err)
	}
}

// The billing gate: a cron-fired run pinned to a metered lane is
// refused without a named approver, and the refusal names both env
// vars so the operator knows the two legitimate ways out.
func TestResolveAutopilotCronLane_MeteredNeedsApprover(t *testing.T) {
	l := resolveAutopilotCronLane(envMap(map[string]string{
		routines.EnvAutopilotCron:     "1",
		routines.EnvAutopilotProvider: "deepseek",
	}))
	err := l.GateErr()
	if err == nil {
		t.Fatal("metered lane without approver must be refused")
	}
	msg := err.Error()
	for _, want := range []string{
		routines.EnvAutopilotProvider,
		routines.EnvAutopilotAllowMetered,
		"approved-by:",
		"metered",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal should mention %q, got: %s", want, msg)
		}
	}
}

func TestResolveAutopilotCronLane_MeteredApprovedPasses(t *testing.T) {
	l := resolveAutopilotCronLane(envMap(map[string]string{
		routines.EnvAutopilotCron:         "1",
		routines.EnvAutopilotProvider:     "glm",
		routines.EnvAutopilotAllowMetered: "approved-by:c_kdblc4",
	}))
	if err := l.GateErr(); err != nil {
		t.Fatalf("named approver must pass the gate: %v", err)
	}
	if got := l.PinProvider(); got != "glm" {
		t.Fatalf("pin = %q, want glm", got)
	}
	if !strings.Contains(l.LogLine(), "approved-by:c_kdblc4") {
		t.Fatalf("log line should carry the approver: %s", l.LogLine())
	}
}

// Values are trimmed — a trailing newline in an overlay file must not
// disable the gate or mangle the pin.
func TestResolveAutopilotCronLane_TrimsEnvValues(t *testing.T) {
	l := resolveAutopilotCronLane(func(k string) string {
		switch k {
		case routines.EnvAutopilotCron:
			return "1\n"
		case routines.EnvAutopilotProvider:
			return " deepseek "
		}
		return ""
	})
	if !l.cron {
		t.Fatal("marker with trailing newline must still count")
	}
	if got := l.PinProvider(); got != "deepseek" {
		t.Fatalf("pin = %q, want deepseek", got)
	}
	if err := l.GateErr(); err == nil {
		t.Fatal("trimmed metered pin must still be gated")
	}
}

// --- llmChatClient ---------------------------------------------------------

// stubProvider streams a scripted event sequence per call.
type stubProvider struct {
	events []llm.Event
	calls  int
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.Event, error) {
	s.calls++
	ch := make(chan llm.Event, len(s.events))
	for _, e := range s.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestLlmChatClient_AssemblesTextAndUsage(t *testing.T) {
	p := &stubProvider{events: []llm.Event{
		llm.TextDelta{Delta: `[{"title":"t","prompt":"do it","done_when":["it is done"]}]`},
		llm.TextDelta{Delta: ""},
		llm.TurnDone{FinishReason: "stop", InputTokens: 11, OutputTokens: 7},
	}}
	c := llmChatClient{p: p}
	resp, err := c.Chat(context.Background(), &deepseek.ChatRequest{
		Model: "m",
		Messages: []deepseek.Message{
			{Role: deepseek.RoleSystem, Content: "sys"},
			{Role: deepseek.RoleUser, Content: "goal"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != `[{"title":"t","prompt":"do it","done_when":["it is done"]}]` {
		t.Fatalf("content not assembled: %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 18 {
		t.Fatalf("usage not carried: %+v", resp.Usage)
	}
	// The request translation is content-only, role-preserving.
	if p.calls != 1 {
		t.Fatalf("one call expected, got %d", p.calls)
	}
}

func TestLlmChatClient_StreamErrorPropagates(t *testing.T) {
	p := &stubProvider{events: []llm.Event{
		llm.TextDelta{Delta: "par"},
		llm.ErrorEvent{Err: errors.New("boom")},
	}}
	c := llmChatClient{p: p}
	if _, err := c.Chat(context.Background(), &deepseek.ChatRequest{Model: "m"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stream error must propagate, got %v", err)
	}
}

func TestLlmChatClient_EmptyStreamIsError(t *testing.T) {
	p := &stubProvider{events: []llm.Event{}}
	c := llmChatClient{p: p}
	if _, err := c.Chat(context.Background(), &deepseek.ChatRequest{Model: "m"}); err == nil {
		t.Fatal("a stream with neither content nor usage must be an error, not a silent empty reply")
	}
}

func TestLlmChatClient_NilProviderIsError(t *testing.T) {
	c := llmChatClient{}
	if _, err := c.Chat(context.Background(), &deepseek.ChatRequest{Model: "m"}); err == nil {
		t.Fatal("nil provider must error")
	}
}

// --- acceptance ③ linkage: pref=Yolo ⇒ autopilot worker bash runs ---
//
// The pref card (autopilot lane self-elevating to Yolo) is NOT merged,
// so this pins the LINKAGE the card's acceptance ③ names at the
// mechanism layer: the moment the autopilot lane runs under Yolo (by
// whatever lands it there), the worker's bash passes Check with no
// human to ask — and under Deny it is refused. This is exactly the
// ParentPolicy.Spawn inheritance the subagent manager wires
// (manager.go: m.opts.ParentPolicy.Spawn(cwd, tmpl.Restriction)); the
// general-purpose template carries no Restriction override, so the
// child's pref IS the parent lane's pref, monotonic-only.

// yoloParentStub stands in for the subagent manager's ParentPolicy
// spawn path: same constructor, same Spawn call, zero cmd/seek
// machinery.
func TestAutopilotWorkerBash_PrefLinkage(t *testing.T) {
	mkAction := func() permission.Action {
		return permission.Action{Kind: permission.KindBash, Command: "go test ./..."}
	}

	// Yolo parent (what the pref card will put the autopilot lane in):
	// worker bash passes with NO askFn installed — unattended means
	// there is nobody to ask, and Check must not need them.
	parent, err := permission.New(t.TempDir(), permission.PrefYolo)
	if err != nil {
		t.Fatal(err)
	}
	child, err := parent.Spawn(t.TempDir(), permission.Restriction{})
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Check(mkAction()); err != nil {
		t.Fatalf("worker bash under Yolo parent must pass: %v", err)
	}

	// Deny parent (today's autopilot lane until the pref card lands):
	// worker bash is refused — the documented gap this test keeps
	// visible rather than papering over.
	parentDeny, err := permission.New(t.TempDir(), permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	childDeny, err := parentDeny.Spawn(t.TempDir(), permission.Restriction{})
	if err != nil {
		t.Fatal(err)
	}
	if err := childDeny.Check(mkAction()); err == nil {
		t.Fatal("worker bash under Deny parent must be refused (pref card gap still open)")
	}

	// The monotonicity guard the pref card's design leans on: a
	// Restriction can never lift the child above the parent, so the
	// lane's Yolo is the ceiling workers inherit, never a bypass.
	deny := permission.PrefDeny
	if _, err := parentDeny.Spawn(t.TempDir(), permission.Restriction{Pref: &deny}); err == nil {
		// deny==parent is legal (tighten-or-equal); the ILLEGAL one is next.
		_ = err
	}
	yolo := permission.PrefYolo
	if _, err := parentDeny.Spawn(t.TempDir(), permission.Restriction{Pref: &yolo}); err == nil {
		t.Fatal("Spawn must refuse to loosen Deny→Yolo")
	}
}
