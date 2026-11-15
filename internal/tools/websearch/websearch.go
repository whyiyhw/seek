// websearch.go — the `web_search` Tool: one call to DeepSeek's
// standalone Search API (api.deepseek.com/v1/tools/search, opened
// 2026-10-17), rendered as a title / URL / summary list.
//
// Why this needs no SSRF gate (unlike webfetch): the search runs
// server-side on DeepSeek's engine and the only HTTP seek performs is a
// fixed POST to the chat client's own base URL — the model controls the
// QUERY, never a URL to fetch. Result URLs are data, not dial targets;
// if the model wants a page it must go through webfetch, which runs its
// full SSRF validation on that hop.
//
// Cost: per SUCCESSFUL call only (¥5/1K ≈ $0.0007), reported through the
// CostSink so the status bar's cumulative grows without fabricating a
// token turn. Failed searches are free and report nothing.
package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/internal/tools"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

const toolName = "web_search"

const description = "Search the web via DeepSeek's Search API and return a ranked list of results (title, URL, summary). Use when you need current information or documentation you don't have a URL for; queries support site:, -site: and quoted-phrase operators. For the full content of a result, follow up with webfetch on its URL. Billed per successful call (~$0.0007)."

// schemaBytes is package-level so the wire bytes stay identical across
// turns (CLAUDE.md "Tool JSON schemas are package-level []byte constants").
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "minLength": 1,
      "maxLength": 128,
      "description": "The search query, 1-128 characters. Supports operators such as site:, -site: and quoted phrases."
    },
    "count": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "Number of results to return, 1-50. Default 10."
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`)

const (
	// defaultCount matches the API's own default.
	defaultCount = 10
	// maxSummaryChars bounds one result's summary in the tool output.
	// The API documents 600-800 chars typical; the cap only bites if a
	// result ever comes back fatter, keeping worst-case output at
	// 50 × 1.2 KB — well under webfetch's 64 KiB envelope.
	maxSummaryChars = 1200
)

// CostSink receives the per-call flat cost. *cache.Tracker satisfies it
// (RecordFlatCost); nil disables reporting. Narrow local interface so
// tests don't need the cache package.
type CostSink interface {
	RecordFlatCost(usd float64)
}

// Tool is the web_search implementation.
type Tool struct {
	client *deepseek.Client
	cost   CostSink
}

// New constructs a Tool. client carries the DeepSeek API key / base URL
// (same key as chat — config.KeyFor deepseek); cost may be nil.
func New(client *deepseek.Client, cost CostSink) *Tool {
	return &Tool{client: client, cost: cost}
}

// Name / Description / Schema satisfy tools.Tool.
func (*Tool) Name() string            { return toolName }
func (*Tool) Description() string     { return description }
func (*Tool) Schema() json.RawMessage { return schemaBytes }

// ReadOnly is true — web_search performs no local writes and its single
// HTTP call goes to the fixed DeepSeek endpoint, so it dispatches with
// the other read-only tools and stays available in plan-analyze.
func (*Tool) ReadOnly() bool { return true }

// Compile-time: Tool must stay a Tool AND a ReadOnlyTool.
var _ tools.ReadOnlyTool = (*Tool)(nil)

type args struct {
	Query string `json:"query"`
	Count int    `json:"count,omitempty"`
}

// Execute validates the query, runs one Search API call, reports the
// per-call cost on success, and renders the ranked results.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a args
	if err := tools.UnmarshalStrict(toolName, raw, &a, "query", "count"); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", tools.MissingField(toolName, "query", raw, "query", "count")
	}
	count := a.Count
	if count == 0 {
		count = defaultCount
	}
	if count < 1 || count > deepseek.SearchCountMax {
		return "", fmt.Errorf("%s: count must be 1..%d, got %d", toolName, deepseek.SearchCountMax, a.Count)
	}
	if n := len([]rune(a.Query)); n > deepseek.SearchQueryMaxChars {
		return "", fmt.Errorf("%s: query must be at most %d characters, got %d — shorten the query",
			toolName, deepseek.SearchQueryMaxChars, n)
	}

	resp, err := t.client.Search(ctx, &deepseek.SearchRequest{Query: a.Query, Count: count})
	if err != nil {
		return formatErrorResult(a.Query, err), nil
	}

	// Billable = successful calls only (the API charges per HTTP 200).
	if t.cost != nil {
		t.cost.RecordFlatCost(pricing.SearchCost(1))
	}
	return formatResults(a.Query, resp), nil
}

// formatResults renders the ranked list the model reads. Summary is the
// API's model-oriented digest; snippet is the UI-oriented fallback when
// a result carries no summary.
func formatResults(query string, resp *deepseek.SearchResponse) string {
	if resp == nil || len(resp.Results) == 0 {
		return fmt.Sprintf("[web_search: no results] Query: %s\nNo results — try broader terms, drop operators, or rephrase.", query)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Query: %s\nResults: %d\n\n", query, len(resp.Results))
	for i, r := range resp.Results {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, metaLine(r))
		if r.URL != "" {
			fmt.Fprintf(&sb, "   %s\n", r.URL)
		}
		digest := r.Summary
		if strings.TrimSpace(digest) == "" {
			digest = r.Snippet
		}
		if n := len([]rune(digest)); n > maxSummaryChars {
			digest = string([]rune(digest)[:maxSummaryChars]) + "…"
		}
		if digest != "" {
			fmt.Fprintf(&sb, "   %s\n", digest)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// metaLine renders "title (site, date)" with whatever metadata the
// result actually carries.
func metaLine(r deepseek.SearchResult) string {
	var parts []string
	if r.SiteName != nil && *r.SiteName != "" {
		parts = append(parts, *r.SiteName)
	}
	if r.Date != nil {
		parts = append(parts, r.Date.Format("2006-01-02"))
	}
	title := r.Title
	if strings.TrimSpace(title) == "" {
		title = "(untitled)"
	}
	if len(parts) == 0 {
		return title
	}
	return fmt.Sprintf("%s (%s)", title, strings.Join(parts, ", "))
}

// formatErrorResult wraps an API / transport error into the standard
// "[web_search: <category>] …" model-facing string, mirroring webfetch.
func formatErrorResult(query string, err error) string {
	var category string
	var apiErr *deepseek.APIError
	switch {
	case errors.As(err, &apiErr):
		switch {
		case apiErr.StatusCode == 401 || apiErr.StatusCode == 403:
			category = "auth error"
		case apiErr.StatusCode == 429:
			category = "rate limited"
		default:
			category = fmt.Sprintf("http %d", apiErr.StatusCode)
		}
	case errors.Is(err, context.DeadlineExceeded):
		category = "timeout"
	default:
		category = "search error"
	}
	return fmt.Sprintf("[web_search: %s] Query: %s\n%v", category, query, err)
}
