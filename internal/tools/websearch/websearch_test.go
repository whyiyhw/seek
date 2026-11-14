package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// costRecorder captures RecordFlatCost calls so tests can assert that
// cost is reported exactly once per SUCCESSFUL call and never on error.
type costRecorder struct {
	calls int
	total float64
}

func (c *costRecorder) RecordFlatCost(usd float64) {
	c.calls++
	c.total += usd
}

func resultJSON(title, url, summary string) string {
	b, _ := json.Marshal(map[string]any{
		"results": []map[string]any{{
			"rerank_score": 2.0, "title": title, "url": url,
			"snippet": "short", "summary": summary, "content": "",
			"site_name": nil, "site_icon": nil, "date": nil, "authority": 2,
		}},
	})
	return string(b)
}

func mustCall(t *testing.T, tool *Tool, raw string) string {
	t.Helper()
	out, err := tool.Execute(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("Execute(%s): %v", raw, err)
	}
	return out
}

func TestExecute_RendersTitleURLSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resultJSON("Go 1.30 release notes",
			"https://go.dev/doc/go1.30", "What changed in Go 1.30.")))
	}))
	defer srv.Close()

	cost := &costRecorder{}
	tool := New(deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)), cost)
	out := mustCall(t, tool, `{"query": "go 1.30 release notes", "count": 3}`)

	for _, want := range []string{
		"Query: go 1.30 release notes",
		"Results: 1",
		"1. Go 1.30 release notes",
		"https://go.dev/doc/go1.30",
		"What changed in Go 1.30.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if cost.calls != 1 || cost.total != pricing.SearchCost(1) {
		t.Errorf("cost recorder = %d calls / %v, want 1 / %v", cost.calls, cost.total, pricing.SearchCost(1))
	}
}

func TestExecute_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer srv.Close()

	cost := &costRecorder{}
	tool := New(deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)), cost)
	out := mustCall(t, tool, `{"query": "zzz no such thing"}`)

	if !strings.Contains(out, "[web_search: no results]") {
		t.Errorf("output missing no-results marker:\n%s", out)
	}
	// An empty-but-successful search IS billable (HTTP 200).
	if cost.calls != 1 {
		t.Errorf("cost calls = %d, want 1 — empty 200 is a billable success", cost.calls)
	}
}

func TestExecute_HTTPErrorNoCost(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid query"}}`))
	}))
	defer srv.Close()

	cost := &costRecorder{}
	tool := New(deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)), cost)
	out := mustCall(t, tool, `{"query": "x"}`)

	if !strings.HasPrefix(out, "[web_search: http 400]") {
		t.Errorf("output = %q, want [web_search: http 400] prefix", out)
	}
	if !strings.Contains(out, "Invalid query") {
		t.Errorf("output should carry the API message:\n%s", out)
	}
	if cost.calls != 0 {
		t.Errorf("cost calls = %d, want 0 — failed calls are not billable", cost.calls)
	}
	if atomic.LoadInt32(&n) != 1 {
		t.Errorf("requests = %d, want 1 — 400 must fail fast without retry", n)
	}
}

func TestExecute_Retries429ThenReportsCostOnce(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": {"message": "rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resultJSON("After retry", "https://after.example", "Recovered.")))
	}))
	defer srv.Close()

	cost := &costRecorder{}
	tool := New(deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)), cost)
	out := mustCall(t, tool, `{"query": "transient"}`)

	if !strings.Contains(out, "After retry") {
		t.Errorf("output missing recovered result:\n%s", out)
	}
	if atomic.LoadInt32(&n) != 2 {
		t.Errorf("requests = %d, want 2 (retry once)", n)
	}
	if cost.calls != 1 {
		t.Errorf("cost calls = %d, want 1 — the retry succeeded once, bill once", cost.calls)
	}
}

func TestExecute_ArgValidation(t *testing.T) {
	tool := New(deepseek.New(deepseek.WithAPIKey("t")), nil) // nil cost sink must be safe

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"missing query", `{}`, "query is required"},
		{"blank query", `{"query": "   "}`, "query is required"},
		{"query too long", `{"query": "` + strings.Repeat("x", 129) + `"}`, "at most 128"},
		{"count over max", `{"query": "ok", "count": 51}`, "count must be 1..50"},
		{"count zero-ish negative", `{"query": "ok", "count": -2}`, "count must be 1..50"},
		{"unknown field", `{"query": "ok", "q": "typo"}`, "unknown field"},
	}
	for _, tc := range cases {
		_, err := tool.Execute(context.Background(), json.RawMessage(tc.raw))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}

	// count omitted → the tool applies its default of 10 and forwards
	// it explicitly (same value the server would default to).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req deepseek.SearchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Count != defaultCount {
			t.Errorf("count forwarded = %d, want default %d", req.Count, defaultCount)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	defer srv.Close()
	tool2 := New(deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)), nil)
	mustCall(t, tool2, `{"query": "defaults"}`)
}

func TestExecute_LongSummaryTruncated(t *testing.T) {
	long := strings.Repeat("字", 2000) // 2000 runes > maxSummaryChars=1200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resultJSON("T", "https://u", long)))
	}))
	defer srv.Close()

	tool := New(deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)), nil)
	out := mustCall(t, tool, `{"query": "q"}`)
	if n := strings.Count(out, "字"); n != maxSummaryChars {
		t.Errorf("summary rendered %d runes, want capped at %d", n, maxSummaryChars)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "…") {
		t.Errorf("truncated summary should end with ellipsis:\n…%s", out[len(out)-40:])
	}
}

func TestTool_ReadOnlyAndSchema(t *testing.T) {
	tool := New(nil, nil) // nil client: metadata accessors must not need it
	if !tool.ReadOnly() {
		t.Error("ReadOnly() = false, want true")
	}
	if tool.Name() != "web_search" {
		t.Errorf("Name() = %q", tool.Name())
	}
	// Schema must be deterministic and carry the documented bounds.
	var s map[string]any
	if err := json.Unmarshal(tool.Schema(), &s); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	props := s["properties"].(map[string]any)
	count := props["count"].(map[string]any)
	if count["maximum"] != float64(50) {
		t.Errorf("count.maximum = %v, want 50", count["maximum"])
	}
	if string(tool.Schema()) != string(tool.Schema()) {
		t.Error("schema not byte-stable")
	}
}
