// row.go — the output contract (PRD §3.3): one JSON line per input
// entry, same id, terminal state; and `_meta` as the LAST line.
//
// Row marshals to exactly one of two shapes, never both:
//
//	{"id":"a1","verdict":"keep","reasons":[...],"signals":{...},"meta":{...}}
//	{"id":"a3","error":"model returned malformed verdict"}
//
// `meta` appears only when the caller supplied it (omitempty on a Raw
// field drops both null and an empty Raw), and an error row never
// carries a verdict — so a consumer can branch on the presence of
// `error` without ever having to disambiguate a zero verdict.
package judge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Row is one output line.
//
// `id` is never omitted: it is part of the frozen row shape, and an
// unattributable input line (invalid JSON — nothing to echo) renders
// `"id":""` rather than dropping the key. A caller joining on id simply
// never matches ""; a consumer parsing the file never has to special-case
// a missing key.
type Row struct {
	ID      string                     `json:"id"`
	Verdict string                     `json:"verdict,omitempty"`
	Reasons []string                   `json:"reasons,omitempty"`
	Signals map[string]json.RawMessage `json:"signals,omitempty"`
	Meta    json.RawMessage            `json:"meta,omitempty"`
	Error   string                     `json:"error,omitempty"`
}

// Meta is the trailing aggregate line (PRD §3.3 invariant 4).
//
// Counts are of OUTPUT rows, so `items` reconciles mechanically against
// the file the caller already has: items = kept+dropped+gray+errors.
// temperature/effort/model ride along because §8.1-4 measured all three
// as drift causes — a verdict file that omits them cannot explain why
// two runs of the "same" rubric disagreed.
type Meta struct {
	Model          string  `json:"model"`
	RubricSHA256   string  `json:"rubric_sha256"`
	Items          int     `json:"items"`
	Kept           int     `json:"kept"`
	Dropped        int     `json:"dropped"`
	Gray           int     `json:"gray"`
	PromptTokens   int     `json:"prompt_tokens"`
	CacheHitTokens int     `json:"cache_hit_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	// Temperature and Effort are deliberately NOT omitempty: `_meta` must
	// always answer "what knobs produced this file" (§8.1-4). A null
	// temperature means the request carried no temperature key at all
	// (model default) — distinct from 0, which is a real determinism pin.
	// An empty effort means no reasoning_effort override.
	Temperature *float64 `json:"temperature"`
	Effort      string   `json:"effort"`
}

// Result is one batch's complete output: rows in input order, then the
// meta aggregate.
type Result struct {
	Rows []Row
	Meta Meta
}

// Errors returns the number of error rows — the CLI's exit-code 1
// trigger (PRD §3.4: partial failure is file-visible, not an exception).
func (r *Result) Errors() int {
	n := 0
	for _, row := range r.Rows {
		if row.Error != "" {
			n++
		}
	}
	return n
}

// JSONL renders the whole result: one compact JSON object per row, in
// input order, then `{"_meta":{...}}` as the final line. Every line is
// terminated by '\n' (including the last), so the output is valid JSONL
// even when the batch was empty (an empty batch still emits the _meta
// line — a caller reading cost/audit fields must not have to special-case
// "no items").
func (r *Result) JSONL() ([]byte, error) {
	var b strings.Builder
	for i, row := range r.Rows {
		line, err := marshalCompact(row)
		if err != nil {
			return nil, fmt.Errorf("judge: marshal row %d: %w", i, err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	line, err := marshalCompact(struct {
		Meta Meta `json:"_meta"`
	}{Meta: r.Meta})
	if err != nil {
		return nil, fmt.Errorf("judge: marshal _meta: %w", err)
	}
	b.Write(line)
	b.WriteByte('\n')
	return []byte(b.String()), nil
}

func marshalCompact(v any) ([]byte, error) {
	// encoding/json escapes <, >, & into \u003c etc. by default; SetEscapeHTML(false)
	// keeps a judge output line readable (reasons often quote code or URLs) and
	// byte-stable for diffing. It is still valid JSON either way.
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}
