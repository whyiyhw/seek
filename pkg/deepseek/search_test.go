package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// searchServer mimics the standalone Search API at /v1/tools/search and
// counts requests so retry-once behaviour is observable. Each handler in
// script runs once, in order; a single handler repeats forever.
func searchServer(t *testing.T, script ...http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	i := -1
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		if r.URL.Path != endpointSearch {
			t.Errorf("path = %q, want %q", r.URL.Path, endpointSearch)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("Authorization = %q, want Bearer t", got)
		}
		h := script[len(script)-1]
		i++
		if i < len(script)-1 {
			h = script[i]
		}
		h(w, r)
	})), &n
}

func writeSearchJSON(w http.ResponseWriter, results string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"results": [` + results + `]}`))
}

func TestSearch_DecodesRankedResults(t *testing.T) {
	date := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	srv, n := searchServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req SearchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Query != "deepseek search api" {
			t.Errorf("query = %q", req.Query)
		}
		if req.Count != 5 {
			t.Errorf("count = %d, want 5", req.Count)
		}
		site := "api-docs.deepseek.com"
		writeSearchJSON(w, `{
			"rerank_score": 2.5, "title": "Search | DeepSeek API Docs",
			"url": "https://api-docs.deepseek.com/api/search",
			"snippet": "short UI snippet", "summary": "long model digest", "content": "",
			"site_name": "`+site+`", "site_icon": null, "date": "`+date.Format(time.RFC3339)+`",
			"authority": 4
		}, {
			"rerank_score": 1.1, "title": "Bare result", "url": "https://example.com/x",
			"snippet": "s", "summary": "", "content": "",
			"site_name": null, "site_icon": null, "date": null, "authority": -1
		}`)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	resp, err := c.Search(context.Background(), &SearchRequest{Query: "deepseek search api", Count: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	first := resp.Results[0]
	if first.Title != "Search | DeepSeek API Docs" || first.URL != "https://api-docs.deepseek.com/api/search" {
		t.Errorf("first result = %+v", first)
	}
	if first.SiteName == nil || *first.SiteName != "api-docs.deepseek.com" {
		t.Errorf("site_name = %v", first.SiteName)
	}
	if first.Date == nil || !first.Date.Equal(date) {
		t.Errorf("date = %v, want %v", first.Date, date)
	}
	if first.Summary != "long model digest" || first.Snippet != "short UI snippet" {
		t.Errorf("summary/snippet = %q/%q", first.Summary, first.Snippet)
	}
	if resp.Results[1].SiteName != nil || resp.Results[1].Date != nil {
		t.Errorf("nullable fields should decode to nil, got %+v", resp.Results[1])
	}
	if atomic.LoadInt32(n) != 1 {
		t.Errorf("requests = %d, want 1", *n)
	}
}

func TestSearch_EmptyResultsIsValid(t *testing.T) {
	srv, _ := searchServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeSearchJSON(w, "")
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	resp, err := c.Search(context.Background(), &SearchRequest{Query: "no hits for this"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("results = %d, want 0", len(resp.Results))
	}
}

func TestSearch_4xxFailsFastWithoutRetry(t *testing.T) {
	srv, n := searchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "query too long"}}`))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.Search(context.Background(), &SearchRequest{Query: "x"})
	if err == nil {
		t.Fatal("want error on 400")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Message != "query too long" {
		t.Errorf("err = %v, want APIError 400 with parsed message", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1 — non-429 4xx must NOT retry", got)
	}
}

func TestSearch_RetriesOnceThenSucceeds(t *testing.T) {
	srv, n := searchServer(t,
		func(w http.ResponseWriter, r *http.Request) { // attempt 1: transient 429
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "rate limited"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) { // attempt 2: OK
			writeSearchJSON(w, `{"title": "T", "url": "https://a", "snippet": "s", "summary": "m", "content": "", "site_name": null, "site_icon": null, "date": null, "authority": 0, "rerank_score": 1}`)
		},
	)
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	resp, err := c.Search(context.Background(), &SearchRequest{Query: "blip"})
	if err != nil {
		t.Fatalf("Search after one retry: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "T" {
		t.Errorf("results = %+v", resp.Results)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", got)
	}
}

func TestSearch_RetriesOnceThenGivesUp(t *testing.T) {
	srv, n := searchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"message": "boom"}}`))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.Search(context.Background(), &SearchRequest{Query: "down"})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("err = %v, want annotated 'after 2 attempts'", err)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (retry once, no more)", got)
	}
}

func TestSearch_RequestValidation(t *testing.T) {
	c := New(WithAPIKey("t"))
	cases := []struct {
		name string
		req  *SearchRequest
		want string
	}{
		{"nil request", nil, "nil search request"},
		{"empty query", &SearchRequest{Query: ""}, "1..128"},
		{"query too long", &SearchRequest{Query: strings.Repeat("x", 129)}, "1..128"},
		{"count over max", &SearchRequest{Query: "ok", Count: 51}, "1..50"},
		{"count negative", &SearchRequest{Query: "ok", Count: -1}, "1..50"},
	}
	for _, tc := range cases {
		_, err := c.Search(context.Background(), tc.req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
	// Boundary values must PASS validation client-side (129 is the
	// first bad length; 128 and count 50/0 are legal).
	srv, _ := searchServer(t, func(w http.ResponseWriter, r *http.Request) { writeSearchJSON(w, "") })
	defer srv.Close()
	c = New(WithAPIKey("t"), WithBaseURL(srv.URL))
	if _, err := c.Search(context.Background(), &SearchRequest{Query: strings.Repeat("x", 128), Count: SearchCountMax}); err != nil {
		t.Errorf("boundary query/count rejected: %v", err)
	}
	if _, err := c.Search(context.Background(), &SearchRequest{Query: "x", Count: 0}); err != nil {
		t.Errorf("count=0 (server default) rejected: %v", err)
	}
}

func TestSearch_MissingAPIKey(t *testing.T) {
	c := New()
	_, err := c.Search(context.Background(), &SearchRequest{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("err = %v, want missing api key", err)
	}
}
