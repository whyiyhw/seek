package agent

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// This file holds the ONE test in the suite that talks to the real
// DeepSeek API. Everything else uses httptest fakes.
//
// # Why a real-API test is necessary here
//
// seek's cost story rests on prefix-cache hits: DeepSeek bills cached
// prompt tokens ~10× cheaper, and the cache key is an exact byte
// sequence over the entire prior message history. seek maintains that
// byte stability by CONVENTION — "never modify old messages before
// sending", token trimming happens at write-time not send-time, and
// sysprompt.Compose is guarded as a pure function by
// TestCompose_IsDeterministic.
//
// Conventions are only as strong as the next contributor who has not
// read CLAUDE.md. Before this test, every cache assertion in the suite
// ran against a FAKE backend that echoed whatever cache numbers the test
// itself had written into the fixture — they proved the wire format
// parses, never that a real request hits. A regression that silently
// mutated the prefix (re-serialising an old tool result, inserting a
// per-turn timestamp, reordering tools) would have turned every request
// into a cache miss, roughly 10×'d inference cost, and kept CI green.
//
// So this test asserts the OBSERVABLE rather than the mechanism: after
// the first request in a multi-step conversation, the provider must
// report cache hits. If it stops doing so, prefix stability broke,
// whatever the proximate cause.
//
// The design is borrowed from deepseek-harness's
// packages/core/agent-loop/tests/request-cache.e2e.ts, which makes the
// same assertion for the same reason.
//
// # Running it
//
//	printf 'DEEPSEEK_API_KEY=sk-...\n' >> .env   # .env is gitignored
//	set -a && . ./.env && set +a
//	go test ./pkg/agent/ -run TestPrefixCache_RealAPI -v
//
// Without the key it skips, so CI and `go test ./...` stay hermetic and
// free. It also skips under -short.

// cacheProbeTool forces a multi-request turn: the model must call it and
// then answer from its result, so request #2 necessarily replays
// request #1's prefix plus the appended assistant/tool messages. A
// single-request conversation would prove nothing about prefix reuse.
type cacheProbeTool struct{}

func (cacheProbeTool) Name() string { return "lookup_build_id" }
func (cacheProbeTool) Description() string {
	return "Look up the current build id. Takes no arguments."
}
func (cacheProbeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (cacheProbeTool) ReadOnly() bool { return true }
func (cacheProbeTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "build id = azure-falcon-42", nil
}

// systemPromptForCache is deliberately long. DeepSeek's cache operates on
// 64-token blocks; a two-word system prompt could leave the shared prefix
// below one block and produce a legitimate zero-hit result that would
// look like a regression.
const systemPromptForCache = "You are a terse assistant used in an automated prefix-cache test. " +
	"Follow instructions literally. When the user asks for the build id, call the " +
	"lookup_build_id tool and wait for its result before answering. Never invent a " +
	"value the tool has not returned. After the tool returns, reply with one short " +
	"sentence repeating the returned value verbatim. Do not add explanations, do not " +
	"use markdown, do not ask follow-up questions. If asked anything else, answer in " +
	"one short sentence. Stay under twenty words in every reply. Do not apologise. " +
	"Do not restate the question. Do not summarise your own behaviour."

func TestPrefixCache_RealAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("real-API test; skipped under -short")
	}
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set; see this file's header for how to run the real-API cache test")
	}

	ag, err := New(Config{
		Client:       deepseek.New(deepseek.WithAPIKey(key)),
		Model:        deepseek.ModelV4Flash,
		SystemPrompt: systemPromptForCache,
		Tools:        tools.New().Add(cacheProbeTool{}),
		MaxTurns:     6,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	type turnUsage struct {
		index int
		usage deepseek.Usage
	}
	var turns []turnUsage

	for ev := range ag.Prompt(context.Background(), "What is the build id?") {
		switch e := ev.(type) {
		case TurnEnd:
			turns = append(turns, turnUsage{index: e.Index, usage: e.Usage})
		case ErrorEvent:
			t.Fatalf("agent error: %v", e.Err)
		}
	}

	if len(turns) < 2 {
		t.Fatalf("got %d turns, want ≥2 — the model did not call the tool, so no prefix was replayed "+
			"and the test proves nothing. Turn usage: %+v", len(turns), turns)
	}

	// Turn 0 is deliberately NOT asserted on. DeepSeek's cache is
	// server-side and outlives the process, so a first request may hit
	// from an earlier run of this same test — a zero there is normal on a
	// cold cache and a non-zero is normal on a warm one. Neither tells us
	// anything about seek.
	//
	// Turns 1..N are the real signal: their prefix is turn 0's request
	// plus appended messages, so a miss means seek mutated bytes it had
	// already sent.
	for _, tu := range turns[1:] {
		if tu.usage.PromptCacheHitTokens == 0 {
			t.Errorf("turn %d: PromptCacheHitTokens = 0, want > 0.\n"+
				"The request prefix changed between turns — something mutated already-sent "+
				"messages, or the system prompt / tool schemas are not byte-stable across "+
				"requests. Full usage: %+v", tu.index, tu.usage)
		}
	}

	// Sanity: the accounting must be self-consistent, otherwise a "hit"
	// number could be meaningless. DeepSeek reports hit and miss as
	// disjoint counts summing to prompt_tokens.
	for _, tu := range turns {
		sum := tu.usage.PromptCacheHitTokens + tu.usage.PromptCacheMissTokens
		if tu.usage.PromptTokens != 0 && sum != tu.usage.PromptTokens {
			t.Errorf("turn %d: cache hit(%d) + miss(%d) = %d, want prompt_tokens = %d",
				tu.index, tu.usage.PromptCacheHitTokens, tu.usage.PromptCacheMissTokens,
				sum, tu.usage.PromptTokens)
		}
	}

	for _, tu := range turns {
		t.Logf("turn %d: prompt=%d hit=%d miss=%d completion=%d",
			tu.index, tu.usage.PromptTokens, tu.usage.PromptCacheHitTokens,
			tu.usage.PromptCacheMissTokens, tu.usage.CompletionTokens)
	}
}
