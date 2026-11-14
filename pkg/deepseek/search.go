// search.go — the standalone Search API (POST /v1/tools/search), opened
// 2026-10-17 with DeepSeek's tool-API family. Unlike chat, Search bills
// PER SUCCESSFUL REQUEST (¥5 / 1,000 calls), never per token — pricing
// lives in internal/pricing.SearchCost, not the ModelPricing table.
//
// The search itself runs entirely server-side on DeepSeek's in-house
// engine; the only HTTP this client performs is to api.deepseek.com
// with the same key / base URL as chat. That is why the websearch tool
// needs no SSRF gate: there is no model-controlled URL to fetch.
package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// endpointSearch is the standalone Search API path (api-docs.deepseek.com
// /api/search). Versioned under /v1 unlike chat's /chat/completions.
const endpointSearch = "/v1/tools/search"

// Search API constraints from the published reference:
// query 1..128 Unicode chars (not all whitespace); count 1..50.
const (
	SearchQueryMaxChars = 128
	SearchCountMax      = 50
)

// SearchRequest is the Search API body. OriQuestion / Localization /
// NeedContent exist in the API but are deliberately not modelled: seek
// never has a richer original question than the query itself, geo
// localisation is not a seek concern, and need_content=true would balloon
// every result by a full page of content the model can webfetch on demand.
type SearchRequest struct {
	// Query supports operators such as site:, -site: and quoted phrases.
	Query string `json:"query"`
	// Count is the number of results to return, 1..50. Zero means "let
	// the server default apply" (10).
	Count int `json:"count,omitempty"`
}

// SearchResult is one ranked hit. Snippet is UI-oriented (~100-200
// chars); Summary is the model-oriented digest (~600-800 chars).
type SearchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content"` // always "" while need_content is false
	SiteName    *string `json:"site_name"`
	SiteIcon    *string `json:"site_icon"`
	// Date is the publication time in UTC (RFC 3339) or nil when the
	// engine doesn't know it.
	Date      *time.Time `json:"date"`
	Authority int        `json:"authority"` // 0..4, -1 = unknown
}

// SearchResponse is the 200 body. Results is empty (not null) when the
// query matches nothing — decode treats both identically.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// Search runs one standalone Search API call. Transient failures (5xx,
// 429, transport) retry once via the same retryCall policy as Chat; other
// 4xx are configuration errors (bad query length, auth) and fail fast.
// Only a 2xx counts as billable — the caller's cost accounting must key
// off this returning nil, not off "a request was attempted".
func (c *Client) Search(ctx context.Context, req *SearchRequest) (*SearchResponse, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil search request")
	}
	q := []rune(req.Query)
	if len(q) == 0 || len(q) > SearchQueryMaxChars {
		return nil, fmt.Errorf("deepseek: search query must be 1..%d chars, got %d", SearchQueryMaxChars, len(q))
	}
	if req.Count < 0 || req.Count > SearchCountMax {
		return nil, fmt.Errorf("deepseek: search count must be 0 (default) or 1..%d, got %d", SearchCountMax, req.Count)
	}

	return retryCall(ctx, func() (*SearchResponse, error) {
		resp, err := c.do(ctx, endpointSearch, req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("deepseek: read search response: %w", err)
		}
		if resp.StatusCode/100 != 2 {
			return nil, parseAPIError(resp.StatusCode, body)
		}

		out := &SearchResponse{}
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("deepseek: decode search response: %w", err)
		}
		return out, nil
	})
}
