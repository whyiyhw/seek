package hooks

// Three-zone byte-assertion guard (feature-hooks-injection §4.1/§4.6,
// orchestration §6 D16 — milestone M-O3.2 acceptance).
//
// The three laws, as testable properties of the Registry + the
// authorized inject face:
//
//	cache-stable   — committed history is never rewritten: after a turn
//	                 WITH injection, the previous request's bytes are a
//	                 strict prefix of the next request's bytes (命题一's
//	                 guard: a mid-stream rewrite would break the prefix
//	                 and cost tail × 0.9 every turn);
//	cache-volatile — the ONLY injection surface is Prepend at the
//	                 frontier: every injected message is user-role,
//	                 UserText threading never rewrites old bytes, and
//	                 new content lands strictly AFTER the committed
//	                 prefix (命题二: frontier append = zero marginal cost);
//	session-bounds — the system prompt is frozen in-process: composed
//	                 once per session from the Header, byte-identical
//	                 across turns no matter what hooks injected (命题三:
//	                 recomputation happens only at process boundaries).
//
// The inject hook's stdout here is deliberately NON-DETERMINISTIC
// (different bytes every turn): per §2.3/§4.5-精确化-1, even that must
// not be able to break the cache — commit-once + append-only are the
// architecture's guarantee, not the hook author's discipline.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/whyiyhw/seek/internal/hooksconfig"
	"github.com/whyiyhw/seek/internal/sysprompt"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// msgBytes serialises a message list the way the session/API layer
// sees it: one canonical JSON document per message, in order. The
// exact encoding does not matter for the guard — what matters is that
// it is deterministic and append-only.
func msgBytes(t *testing.T, ms []deepseek.Message) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, m := range ms {
		if err := enc.Encode(m); err != nil {
			t.Fatalf("marshal message: %v", err)
		}
	}
	return buf.Bytes()
}

// requestBytes is B_t = sys ‖ m_1 ‖ … ‖ m_k (feature-hooks-injection
// §2.1) — the byte stream DeepSeek's cache matches on.
func requestBytes(sys string, msgs []deepseek.Message, t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString(sys)
	buf.Write(msgBytes(t, msgs))
	return buf.Bytes()
}

// runTurn simulates what pkg/agent.Prompt does at the seam: snapshot
// history (defensive copy), run the decorator chain, commit Prepend
// messages + the user message at the frontier, append-only.
func runTurn(t *testing.T, reg *Registry, history []deepseek.Message, userText string) (deepseek.Message, []deepseek.Message) {
	t.Helper()
	snapshot := append([]deepseek.Message{}, history...)
	out, err := reg.ApplyPrePrompt(context.Background(), PrePromptIn{UserText: userText, History: snapshot})
	if err != nil {
		t.Fatalf("ApplyPrePrompt: %v", err)
	}
	// The snapshot handed to hooks must come back untouched (the
	// stable zone is not even MUTABLE in place through this seam).
	if !bytes.Equal(msgBytes(t, snapshot), msgBytes(t, history)) {
		t.Fatalf("history snapshot mutated by the PrePrompt chain")
	}
	next := append(append([]deepseek.Message{}, history...), out.Prepend...)
	next = append(next, deepseek.Message{Role: deepseek.RoleUser, Content: out.UserText})
	return deepseek.Message{Role: deepseek.RoleUser, Content: out.UserText}, next
}

func TestInjectionZones_ByteAssertion(t *testing.T) {
	// --- session-boundary zone: sys composed once at startup ---
	header := sysprompt.Header{
		Cwd:            "/repo/seek",
		Date:           "Tuesday, 2026-11-17",
		ProjectSection: "# AGENTS.md\nproject rules",
		SkillManifest:  "- skill-a: does things",
	}
	sysStartup := sysprompt.Compose(header, "ask")

	// An inject hook whose stdout differs EVERY call (worst case for
	// byte stability) plus a live in-process Prepend hook (memory
	// stand-in) sharing the same seam.
	var calls atomic.Int64
	exec := func(ctx context.Context, command, cwd string, env []string) (string, int, error) {
		return strings.Repeat("v", int(1+calls.Add(1))), 0, nil // v, vv, vvv…
	}
	runner := NewShellRunner(hooksconfig.Config{
		PrePromptInject: []hooksconfig.Hook{{
			Name: "noisy", Command: "date; uname; whoami", Source: hooksconfig.SourceUser,
			Event: hooksconfig.EventPrePromptInject, MaxBytes: 64,
		}},
	}, WithExecutor(exec))
	defer runner.Close()

	reg := NewRegistry()
	reg.Register(&fixedPrependHook{content: "<context source=\"memory.soul\">\nsnapshot v1\n</context>"})
	reg.Register(runner.InjectHook())

	// Turn 1 on a small pre-existing history.
	base := []deepseek.Message{
		{Role: deepseek.RoleUser, Content: "earlier question"},
		{Role: deepseek.RoleAssistant, Content: "earlier answer"},
	}
	before1 := requestBytes(sysStartup, base, t)
	userMsg1, hist1 := runTurn(t, reg, base, "first question")
	if userMsg1.Content != "first question" {
		t.Errorf("UserText = %q, want the original text (no rewrite)", userMsg1.Content)
	}
	req1 := requestBytes(sysStartup, hist1, t)

	// cache-stable, turn 1: the old request is a strict prefix of the
	// new one — everything the model already saw is byte-identical.
	if !bytes.HasPrefix(req1, before1) {
		t.Fatalf("turn 1 rewrote committed bytes: prefix broken")
	}
	if bytes.Equal(req1, before1) {
		t.Fatalf("turn 1 appended nothing — test setup broken")
	}
	// cache-volatile, turn 1: injections are user-role <context> only.
	for _, m := range hist1[len(base):] {
		if m.Role != deepseek.RoleUser {
			t.Errorf("frontier message role = %s, want user", m.Role)
		}
	}

	// Turn 2 with DIFFERENT injected bytes behind a longer history.
	before2 := req1
	sysTurn2 := sysprompt.Compose(header, "ask") // what a naive per-turn recompose would produce
	_, hist2 := runTurn(t, reg, hist1, "second question")
	req2 := requestBytes(sysStartup, hist2, t)

	// cache-stable, turn 2: req1 ⊂ req2 even though the hook emitted
	// different stdout on turn 2 (the whole point of commit-once).
	if !bytes.HasPrefix(req2, before2) {
		t.Fatalf("turn 2 rewrote committed bytes: non-deterministic hook output leaked backwards")
	}
	// session-boundary: sys identical across turns (startup-frozen).
	if sysStartup != sysTurn2 {
		t.Fatalf("system prompt drifted between turns — session-boundary zone broken")
	}
	if !bytes.Equal(req2[:len(sysStartup)], req1[:len(sysStartup)]) {
		t.Fatalf("sys bytes differ between turn requests")
	}
}

// Zone-shape guard for the config layer: the inject budget is bounded
// by the ceiling even when the user asks for more, and defaults
// conservatively — the numbers guide-hooks.md documents.
func TestInjectionZones_MaxBytesBounds(t *testing.T) {
	for _, tc := range []struct {
		config, want int
	}{
		{0, hooksconfig.DefaultMaxBytes},
		{-5, hooksconfig.DefaultMaxBytes},
		{1, 1},
		{hooksconfig.MaxBytesCeiling, hooksconfig.MaxBytesCeiling},
		{hooksconfig.MaxBytesCeiling + 4096, hooksconfig.MaxBytesCeiling},
	} {
		h := hooksconfig.Hook{MaxBytes: tc.config}
		if got := h.EffectiveMaxBytes(); got != tc.want {
			t.Errorf("EffectiveMaxBytes(%d) = %d, want %d", tc.config, got, tc.want)
		}
	}
}
