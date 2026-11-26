// parallel.go — the per-entry seam and the streaming fold the CLI needs
// so bounded concurrency can live in the CLI (cmd/seek/judge.go, card
// c_jdo7rp) without duplicating the `_meta` contract here.
//
// Batch stays the whole-slice API (it is what the table tests drive, and
// what "in input order, sequentially" means); JudgeEntry is the
// one-entry API a worker pool fans out over, and Folder folds each
// OUTCOME at the emission point — so the CLI writes rows in input order
// and never has to retain them to build `_meta`. Batch is now itself a
// loop over those two, which is what keeps the sequential and concurrent
// paths from drifting apart.
package judge

import (
	"context"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// Outcome is one entry's judged result: the row, plus what the provider
// billed for it. Billed is false when no usage exists at all (the call
// itself failed or the entry never reached the wire); it is true for a
// reply that was unusable but still paid for.
type Outcome struct {
	Row    Row
	Usage  deepseek.Usage
	Billed bool
}

// JudgeEntry judges ONE entry and never returns a Go error: a malformed
// line, a cancelled ctx, a failed call, or an unusable reply all become
// that row's own error text (PRD §3.3 invariant 5).
func (j *Judge) JudgeEntry(ctx context.Context, e Entry) Outcome {
	if err := ctx.Err(); err != nil {
		// Still one row per entry: a cancelled batch is a complete
		// batch of "not judged" rows, not a truncated file.
		return Outcome{Row: Row{ID: e.Item.ID, Meta: e.Item.Meta, Error: "not judged: " + err.Error()}}
	}
	if e.Err != "" {
		return Outcome{Row: Row{ID: e.Item.ID, Meta: e.Item.Meta, Error: e.Err}}
	}
	row, u, billed := j.one(ctx, e.Item)
	return Outcome{Row: row, Usage: u, Billed: billed}
}

// Folder accumulates the batch-wide numbers behind the trailing `_meta`
// row. The CLI folds one Outcome per emitted row, in input order; Batch
// folds every row it collects. Neither retains rows to count them.
//
// Deliberately NOT mutex-guarded: the fold happens at one point (the
// ordered emitter in the CLI), so the counts' ordering is trivially the
// output's ordering. A caller that folds from several goroutines owns
// its own lock.
type Folder struct {
	judge *Judge

	usage   deepseek.Usage
	cost    float64
	items   int
	kept    int
	dropped int
	gray    int
	errors  int
}

// NewFolder returns a fold bound to this Judge's model / tier / knobs.
func (j *Judge) NewFolder() *Folder { return &Folder{judge: j} }

// Add folds one outcome: row counts, the error count, and the usage/cost
// of a call that was actually billed.
func (f *Folder) Add(o Outcome) {
	f.items++
	if o.Row.Error != "" {
		f.errors++
	}
	switch o.Row.Verdict {
	case VerdictKeep:
		f.kept++
	case VerdictDrop:
		f.dropped++
	case VerdictGray:
		f.gray++
	}
	if o.Billed {
		f.usage.PromptTokens += o.Usage.PromptTokens
		f.usage.CompletionTokens += o.Usage.CompletionTokens
		f.usage.TotalTokens += o.Usage.TotalTokens
		f.usage.PromptCacheHitTokens += o.Usage.PromptCacheHitTokens
		f.usage.PromptCacheMissTokens += o.Usage.PromptCacheMissTokens
		f.cost += f.judge.Cost(o)
	}
}

// Errors reports how many folded rows carried an error — the CLI's
// exit-code 1 trigger (PRD §3.4: partial failure is file-visible).
func (f *Folder) Errors() int { return f.errors }

// Cost returns the USD accumulated so far from folded outcomes — exactly
// the figure the trailing _meta will report. The CLI's --max-cost gate
// reads it to decide when to stop dispatching new entries; sharing this
// one number keeps "the cost we stopped on" and "the cost we printed"
// from drifting apart (PRD §6 item 7, card c_qjuags).
func (f *Folder) Cost() float64 { return f.cost }

// Meta renders the `_meta` line's contents from what was folded: items
// counts OUTPUT rows, so items == kept+dropped+gray+errors always holds
// and a caller can audit a file without knowing the input.
func (f *Folder) Meta() Meta {
	m := Meta{
		Model:          f.judge.model,
		RubricSHA256:   f.judge.rubricSHA,
		Items:          f.items,
		Kept:           f.kept,
		Dropped:        f.dropped,
		Gray:           f.gray,
		PromptTokens:   f.usage.PromptTokens,
		CacheHitTokens: f.usage.PromptCacheHitTokens,
		OutputTokens:   f.usage.CompletionTokens,
		CostUSD:        f.cost,
		Effort:         f.judge.effort,
	}
	if f.judge.temperature != nil {
		v := *f.judge.temperature
		m.Temperature = &v
	}
	return m
}
