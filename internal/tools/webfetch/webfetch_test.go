package webfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestTool builds a Tool that skips IP validation (since httptest
// servers bind to 127.0.0.1, which is loopback and would otherwise be
// blocked). URL-level checks still run, exercising the rest of the
// SSRF gate.
func newTestTool() *Tool {
	t := New(ValidationOptions{
		AllowHTTP:    true, // httptest serves over http://
		AllowedPorts: nil,  // accept default (80/443/8080/8443)
	})
	t.skipIPValidation = true
	return t
}

// makeRequestRaw builds the JSON args the Tool's Execute consumes.
func makeRequestRaw(t *testing.T, url string, maxBytes int) json.RawMessage {
	t.Helper()
	a := struct {
		URL      string `json:"url"`
		MaxBytes int    `json:"max_bytes,omitempty"`
	}{URL: url, MaxBytes: maxBytes}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return raw
}

// extractServerHost returns the host:port of an httptest server, with
// the schema port (80/443/8080/8443) substituted where needed so the
// port allowlist passes. httptest binds to an ephemeral high port; we
// override AllowedPorts in the Tool for these tests instead.
func toolForServer(t *testing.T, srv *httptest.Server) *Tool {
	t.Helper()
	// httptest binds to an ephemeral high port; inject that port into
	// the allowlist so URL validation passes for tests.
	tool := New(ValidationOptions{
		AllowHTTP:    true,
		AllowedPorts: []int{serverPort(srv), 80, 443, 8080, 8443},
	})
	tool.client = tool.buildClient()
	tool.skipIPValidation = true
	return tool
}

func serverPort(srv *httptest.Server) int {
	addr := srv.Listener.Addr().String()
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return 0
	}
	var p int
	for _, c := range addr[idx+1:] {
		if c < '0' || c > '9' {
			break
		}
		p = p*10 + int(c-'0')
	}
	return p
}

// --- Happy paths ----------------------------------------------------

func TestExecute_HappyTextHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><h1>Hello</h1><p>World</p></body></html>`))
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Status: 200 OK") {
		t.Errorf("result missing 200 status:\n%s", out)
	}
	if !strings.Contains(out, "Content-Type: text/html") {
		t.Errorf("result missing Content-Type:\n%s", out)
	}
	// HTML body should have been simplified — H1/P contents survive,
	// tags do not.
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "World") {
		t.Errorf("simplified body missing content:\n%s", out)
	}
	if strings.Contains(out, "<h1>") || strings.Contains(out, "<body>") {
		t.Errorf("simplified body still contains raw HTML tags:\n%s", out)
	}
}

func TestExecute_HappyApplicationJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world","n":42}`))
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	// JSON is returned verbatim (no HTML simplification).
	if !strings.Contains(out, `{"hello":"world","n":42}`) {
		t.Errorf("JSON body not returned verbatim:\n%s", out)
	}
}

func TestExecute_TextPlainPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("line 1\nline 2\nline 3"))
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line 1\nline 2\nline 3") {
		t.Errorf("text/plain not verbatim:\n%s", out)
	}
}

// --- Redirects ------------------------------------------------------

func TestExecute_FollowsBenignRedirect(t *testing.T) {
	var hop2URL string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("final content"))
	}))
	defer srv2.Close()
	hop2URL = srv2.URL

	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, hop2URL, http.StatusFound)
	}))
	defer srv1.Close()

	// Build tool with BOTH server ports in the allowlist.
	tool := New(ValidationOptions{
		AllowHTTP:    true,
		AllowedPorts: []int{serverPort(srv1), serverPort(srv2), 80, 443, 8080, 8443},
	})
	tool.skipIPValidation = true
	tool.client = tool.buildClient()

	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv1.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "final content") {
		t.Errorf("redirect-follow did not reach final body:\n%s", out)
	}
	if !strings.Contains(out, "Final URL:") {
		t.Errorf("multi-hop redirect should surface Final URL line:\n%s", out)
	}
}

func TestExecute_RejectsRedirectToBlockedURL(t *testing.T) {
	// The hop-2 destination is file:/// — must be refused by
	// CheckRedirect's re-validation.
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer srv1.Close()

	tool := toolForServer(t, srv1)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv1.URL, 0))
	if err != nil {
		t.Fatal(err) // Tool returns error as result string, not as err
	}
	if !strings.Contains(out, "[webfetch:") {
		t.Errorf("expected error-category result, got:\n%s", out)
	}
	if !strings.Contains(out, "blocked scheme") {
		t.Errorf("redirect to file:// should categorise as blocked scheme; got:\n%s", out)
	}
}

func TestExecute_RejectsRedirectLoop(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvURL, http.StatusFound)
	}))
	defer srv.Close()
	srvURL = srv.URL

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blocked target") {
		t.Errorf("redirect loop should categorise as blocked target; got:\n%s", out)
	}
	if !strings.Contains(out, "too many redirects") {
		t.Errorf("redirect loop message should mention 'too many redirects':\n%s", out)
	}
}

// --- 4xx / 5xx ------------------------------------------------------

func TestExecute_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<html><body>Page not found</body></html>"))
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[webfetch: http error]") {
		t.Errorf("4xx should emit http error category, got:\n%s", out)
	}
	if !strings.Contains(out, "404") {
		t.Errorf("result should include status code:\n%s", out)
	}
	if !strings.Contains(out, "Page not found") {
		t.Errorf("result should include body snippet:\n%s", out)
	}
}

func TestExecute_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("kaboom"))
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "500") || !strings.Contains(out, "kaboom") {
		t.Errorf("500 path missing status / snippet:\n%s", out)
	}
}

// --- Content-Type filter --------------------------------------------

func TestExecute_RejectsBinaryContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47}) // PNG magic
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blocked target") {
		t.Errorf("binary CT should be blocked target, got:\n%s", out)
	}
	if !strings.Contains(out, "not text/json/xml/markdown") {
		t.Errorf("error should explain CT filter:\n%s", out)
	}
}

func TestExecute_RejectsImageContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4e, 0x47})
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blocked target") {
		t.Errorf("image CT should be blocked, got:\n%s", out)
	}
}

func TestExecute_RejectsMissingContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Server doesn't set Content-Type. Go's default ResponseWriter
		// auto-detects text/plain for ASCII bodies, so write something
		// that triggers binary detection.
		_, _ = w.Write([]byte{0x00, 0x01, 0x02, 0x03})
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blocked target") {
		t.Errorf("non-text CT should be blocked, got:\n%s", out)
	}
}

// --- Size cap -------------------------------------------------------

func TestExecute_BodyTruncatedAtLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 100000))) // 100 KB
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, 8192)) // 8 KB cap
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated at 8192") {
		t.Errorf("expected truncation note for oversized body:\n%s", out[:min(500, len(out))])
	}
	if !strings.Contains(out, "max_bytes=") {
		t.Errorf("truncation note should hint at larger max_bytes:\n%s", out[:min(500, len(out))])
	}
}

func TestExecute_BodyExactlyLimitIsNotTruncated(t *testing.T) {
	const exactSize = 4096
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", exactSize)))
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, srv.URL, exactSize))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "truncated at") {
		t.Errorf("exact-size body should not be truncated:\n%s", out[:min(500, len(out))])
	}
}

// --- Timeout / ctx --------------------------------------------------

func TestExecute_ContextCancelStopsRequest(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-r.Context().Done() // hang until client cancels
	}))
	defer srv.Close()

	tool := toolForServer(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	out, err := tool.Execute(ctx, makeRequestRaw(t, srv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[webfetch:") {
		t.Errorf("cancelled fetch should return webfetch error, got:\n%s", out)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("server should have been hit once, got %d", atomic.LoadInt32(&hits))
	}
}

// --- Validator integration through Execute --------------------------

func TestExecute_RejectsFileScheme(t *testing.T) {
	tool := newTestTool()
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, "file:///etc/passwd", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blocked scheme") {
		t.Errorf("file:// should be blocked scheme, got:\n%s", out)
	}
}

func TestExecute_RejectsLocalhost(t *testing.T) {
	tool := newTestTool()
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, "https://localhost/admin", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blocked target") {
		t.Errorf("localhost should be blocked, got:\n%s", out)
	}
}

func TestExecute_RejectsBadPort(t *testing.T) {
	tool := newTestTool()
	out, err := tool.Execute(context.Background(), makeRequestRaw(t, "https://example.com:6379/", 0))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "blocked port") {
		t.Errorf("port 6379 should be blocked port, got:\n%s", out)
	}
}

func TestExecute_MissingURL(t *testing.T) {
	tool := newTestTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"max_bytes":1024}`))
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected 'url is required' error, got: %v", err)
	}
}

func TestExecute_RejectsMaxBytesAboveCap(t *testing.T) {
	tool := newTestTool()
	_, err := tool.Execute(context.Background(), makeRequestRaw(t, "https://example.com/", 10_000_000))
	// json schema enforces maximum=262144 server-side at the agent
	// layer; our runtime check is defence in depth.
	if err == nil {
		t.Errorf("expected runtime max_bytes rejection")
	}
}

// --- Schema integrity ----------------------------------------------

func TestSchema_IsValidJSON(t *testing.T) {
	t.Parallel()
	var v any
	if err := json.Unmarshal(schemaBytes, &v); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
}

func TestSchema_DeterministicBytes(t *testing.T) {
	t.Parallel()
	tool := New(DefaultOptions())
	a := tool.Schema()
	b := tool.Schema()
	if &a[0] != &b[0] {
		t.Fatalf("Schema() returned a different backing array — wire bytes would diverge")
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	if got := New(DefaultOptions()).Name(); got != "webfetch" {
		t.Fatalf("Name() = %q, want webfetch", got)
	}
}

func TestReadOnly(t *testing.T) {
	t.Parallel()
	if !New(DefaultOptions()).ReadOnly() {
		t.Fatal("webfetch must declare ReadOnly() = true")
	}
}
