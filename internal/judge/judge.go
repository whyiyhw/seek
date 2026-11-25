// Package judge is the pure-logic kernel of `seek judge` — PRD
// docs/prd/feature-judge-engine.md §6 work item 5, front half (card
// c_42a7rf): one entry in, one verdict out, rows aggregated into a
// trailing `_meta` line.
//
// Boundaries (PRD §6 "不碰" + §7 "明确不做"):
//   - Judging is ONE pkg/deepseek.Chat call per entry. No agent loop, no
//     tool registry (the request carries no `tools` field at all), no
//     internal/permission — the judge face cannot execute anything.
//   - Zero state. This package opens no file for writing and imports no
//     path/os package: the CLI layer owns JSONL I/O, the caller owns the
//     rubric file, and `~/.seek` is not a concept here. TestNoStateImports
//     pins this structurally (AST import whitelist), not by convention.
//   - ResponseFormat is deliberately NOT set. PRD §4 D4 downgraded it to
//     an optional hardening; the 2026-11-24 baseline (§8.1-1) measured
//     100% format compliance without it. Wiring it "while we're here"
//     would change the wire bytes every batch pays for.
//
// No CLI, no budget cap, no `--isolated`, no concurrency: those are the
// later halves of item 5/6/7. Batch is deliberately sequential so "rows
// come out in input order" is a tested invariant rather than a promise
// the scheduler has to keep.
package judge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Verdict values. keep/drop/gray is the PRD §3.3 three-value contract,
// isomorphic to internal/verifier's pass/fail/doubt: gray is the design
// core — an unsure entry is neither kept nor thrown away, it is stacked
// for a human.
const (
	VerdictKeep = "keep"
	VerdictDrop = "drop"
	VerdictGray = "gray"
)

// defaultMaxTokens bounds the reply. PRD §8.1-3 measured output as 92%
// of a judge call's cost, so an uncapped reply is the one place a single
// entry can cost 4× its expected price. 4k is comfortably above a
// low-effort reply (reasons + signals) while still bounding a runaway.
const defaultMaxTokens = 4096

// Chatter is the narrow seam over *deepseek.Client. Narrowed the same
// way internal/verifier's judge does it: tests inject an httptest-backed
// client (zero real key), and this package can never grow a second
// provider path behind the kernel's back.
type Chatter interface {
	Chat(ctx context.Context, req *deepseek.ChatRequest) (*deepseek.ChatResponse, error)
}

// Judge judges entries against one caller-owned rubric. Construct via
// New; safe for concurrent use (it holds no mutable state once built —
// the embedded client is concurrent-safe, and Batch writes only to its
// own Result).
type Judge struct {
	client      Chatter
	model       string
	rubricSHA   string
	system      string
	temperature *float64
	effort      string
	tier        pricing.Tier
	maxTokens   int
}

// Option configures a Judge.
type Option func(*Judge)

// WithTemperature pins the sampling temperature. nil (the default) sends
// no temperature key at all, keeping the request byte-identical to its
// pre-knob form (prefix-cache discipline); a pointer to 0 is the real,
// different request the judge lane wants (PRD §4 D6).
func WithTemperature(t *float64) Option { return func(j *Judge) { j.temperature = t } }

// WithEffort sets reasoning_effort on the wire ("" = no override, the
// model's own default). The value is passed through verbatim — the CLI
// layer validates the token (cmd/seek's parseEffortFlag) — and it is
// echoed into `_meta` because §8.1-4 measured effort as one of the three
// knobs that explain drift between two runs.
func WithEffort(e string) Option { return func(j *Judge) { j.effort = e } }

// WithMaxTokens overrides the reply cap (default 4096). Values ≤ 0 are
// ignored: a zero cap would be a request the provider rejects, which is
// a worse failure mode than the default.
func WithMaxTokens(n int) Option {
	return func(j *Judge) {
		if n > 0 {
			j.maxTokens = n
		}
	}
}

// WithTier selects the pricing window used for `_meta.cost_usd`
// (pricing.TierStandard by default, i.e. peak — the conservative
// direction for a cost audit). The CLI computes it once from the clock
// (pricing.CurrentTier); the kernel takes it as an argument so tests
// never depend on wall time.
func WithTier(t pricing.Tier) Option { return func(j *Judge) { j.tier = t } }

// New builds a Judge. model may be empty (defaults to DeepSeek's V4.1
// Flash, the judge lane's default); rubric must be non-empty — an empty
// rubric would make every verdict a guess, and the rubric is
// caller-owned by design (PRD §4 D1), so there is no built-in fallback.
func New(client Chatter, model, rubric string, opts ...Option) (*Judge, error) {
	if client == nil {
		return nil, errors.New("judge: nil client")
	}
	if strings.TrimSpace(rubric) == "" {
		return nil, errors.New("judge: empty rubric (the rubric is caller-owned, PRD §4 D1)")
	}
	if model == "" {
		model = deepseek.ModelV41Flash
	}
	j := &Judge{
		client:    client,
		model:     model,
		rubricSHA: sha256Hex(rubric),
		system:    systemPreamble + rubricHeader + rubric,
		tier:      pricing.TierStandard,
		maxTokens: defaultMaxTokens,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(j)
		}
	}
	return j, nil
}

// Model returns the wire model id the judge sends (and echoes in _meta).
func (j *Judge) Model() string { return j.model }

// Batch judges every input entry, sequentially, and returns one row per
// entry in input order (PRD §3.3 invariant 1: output order matches input,
// and the id — never the position — is what the caller joins on).
//
// Insulated per entry (invariant 5): a malformed input line, a transport
// failure, a malformed reply, or one entry's invalid verdict contaminates
// only its own row; the rest are judged and reported. A canceled ctx
// stops new calls but still emits a row for every remaining entry — the
// output never has a half line and never silently loses an input.
func (j *Judge) Batch(ctx context.Context, entries []Entry) *Result {
	res := &Result{Rows: make([]Row, 0, len(entries))}
	var usage deepseek.Usage
	var cost float64

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			// Still one row per entry: a canceled batch is a complete
			// batch of "not judged" rows, not a truncated file.
			res.Rows = append(res.Rows, Row{
				ID:    e.Item.ID,
				Meta:  e.Item.Meta,
				Error: "not judged: " + err.Error(),
			})
			continue
		}
		if e.Err != "" {
			res.Rows = append(res.Rows, Row{ID: e.Item.ID, Meta: e.Item.Meta, Error: e.Err})
			continue
		}
		row, u, billed := j.one(ctx, e.Item)
		res.Rows = append(res.Rows, row)
		if billed {
			usage.PromptTokens += u.PromptTokens
			usage.CompletionTokens += u.CompletionTokens
			usage.TotalTokens += u.TotalTokens
			usage.PromptCacheHitTokens += u.PromptCacheHitTokens
			usage.PromptCacheMissTokens += u.PromptCacheMissTokens
			cost += priceUsage(j.model, j.tier, u)
		}
	}

	res.Meta = j.meta(res.Rows, usage, cost)
	return res
}

// one performs the single Chat call for one entry. billed reports
// whether the provider returned a response whose Usage must be counted —
// true even when the REPLY was unusable (the tokens were still paid
// for), false when the call itself failed (no usage exists).
func (j *Judge) one(ctx context.Context, it Item) (row Row, u deepseek.Usage, billed bool) {
	req := &deepseek.ChatRequest{
		Model: j.model,
		Messages: []deepseek.Message{
			// system carries the rubric (identical bytes for every
			// entry of a batch → the prefix cache hits, PRD §10); user
			// carries the entry as DATA.
			{Role: deepseek.RoleSystem, Content: j.system},
			{Role: deepseek.RoleUser, Content: entryHeader + it.Text},
		},
		Temperature: j.temperature,
		MaxTokens:   j.maxTokens,
	}
	// Mirror pkg/agent's request building so the judge lane and the
	// measured baseline (§8.1) are the same request shape: V4.1-Flash
	// opts into thinking explicitly, and an explicit effort both pins
	// reasoning_effort and enables thinking.
	if deepseek.ShouldEnableThinking(j.model) {
		req.Thinking = &deepseek.ThinkingMode{Type: "enabled"}
	}
	if j.effort != "" {
		req.ReasoningEffort = j.effort
		req.Thinking = &deepseek.ThinkingMode{Type: "enabled"}
	}

	resp, err := j.client.Chat(ctx, req)
	if err != nil {
		return Row{ID: it.ID, Meta: it.Meta, Error: "judge call: " + err.Error()}, deepseek.Usage{}, false
	}
	if resp == nil {
		return Row{ID: it.ID, Meta: it.Meta, Error: "judge call: empty response"}, deepseek.Usage{}, false
	}
	if len(resp.Choices) == 0 {
		return Row{ID: it.ID, Meta: it.Meta, Error: "model returned no choices"}, resp.Usage, true
	}
	reply, perr := parseReply(resp.Choices[0].Message.Content)
	if perr != nil {
		return Row{ID: it.ID, Meta: it.Meta, Error: perr.Error()}, resp.Usage, true
	}
	return Row{
		ID:      it.ID,
		Verdict: reply.Verdict,
		Reasons: reply.Reasons,
		Signals: reply.Signals,
		Meta:    it.Meta,
	}, resp.Usage, true
}

// meta folds the run into the PRD §3.3 `_meta` contract. items counts
// OUTPUT rows (verdict and error rows alike), so the reconciliation
// identity items == kept+dropped+gray+(error rows) always holds and a
// caller can audit a file without knowing the input.
func (j *Judge) meta(rows []Row, u deepseek.Usage, cost float64) Meta {
	m := Meta{
		Model:          j.model,
		RubricSHA256:   j.rubricSHA,
		Items:          len(rows),
		PromptTokens:   u.PromptTokens,
		CacheHitTokens: u.PromptCacheHitTokens,
		OutputTokens:   u.CompletionTokens,
		CostUSD:        cost,
		Effort:         j.effort,
	}
	if j.temperature != nil {
		v := *j.temperature
		m.Temperature = &v
	}
	for _, r := range rows {
		switch r.Verdict {
		case VerdictKeep:
			m.Kept++
		case VerdictDrop:
			m.Dropped++
		case VerdictGray:
			m.Gray++
		}
	}
	return m
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// priceUsage prices one call's usage block.
//
// pricing.Cost keys off PromptCacheHitTokens + PromptCacheMissTokens,
// which are DeepSeek-specific fields a response MAY omit (a relay or a
// future wire shape can report prompt_tokens alone). If the cache split
// does not add up to prompt_tokens, the remainder is treated as cache
// MISS — the expensive side. Over-reporting cost is the safe direction
// for an audit field: under-reporting would let a --max-cost cap
// ("later half of PRD §6 item 7") sail past its budget.
//
// Prior art for WHY this guard exists: pricing.Cost structurally prices
// input only through those two buckets, so a usage block that carries
// prompt_tokens alone prices input at $0 (docs/pitfalls/
// llm-provider-quirks-m6/pricing-cost-bills-input-only-via-cache-hit-miss-buckets-*.md).
func priceUsage(model string, tier pricing.Tier, u deepseek.Usage) float64 {
	hit, miss := u.PromptCacheHitTokens, u.PromptCacheMissTokens
	if missing := u.PromptTokens - hit - miss; missing > 0 {
		miss += missing
		u.PromptCacheMissTokens = miss
	}
	return pricing.Cost(model, tier, u)
}
