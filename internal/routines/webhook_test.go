package routines

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestValidateWebhookURL(t *testing.T) {
	t.Parallel()
	ok := []string{
		"https://ntfy.sh/my-topic",
		"http://localhost:8080/hook",
		"http://192.168.1.50/notify", // private — ALLOWED (D3): user-configured outbound
		"http://127.0.0.1:9999/x",    // loopback — ALLOWED (D3)
	}
	for _, u := range ok {
		if err := ValidateWebhookURL(u); err != nil {
			t.Errorf("ValidateWebhookURL(%q) = %v, want nil (private/LAN must be allowed)", u, err)
		}
	}
	bad := []string{
		"file:///etc/passwd", // non-http scheme
		"gopher://x/y",
		"ftp://host/f",
		"https://", // missing host
		"not a url at all with spaces",
	}
	for _, u := range bad {
		if err := ValidateWebhookURL(u); err == nil {
			t.Errorf("ValidateWebhookURL(%q) = nil, want error", u)
		}
	}
}

func TestValidateWebhookFormat(t *testing.T) {
	t.Parallel()
	for _, f := range []string{"", "raw", "ntfy", "slack", "discord", "feishu", "template"} {
		if err := ValidateWebhookFormat(f); err != nil {
			t.Errorf("ValidateWebhookFormat(%q) = %v, want nil", f, err)
		}
	}
	for _, bad := range []string{"telegram", "feishu-flow", "feishu_flow"} {
		if err := ValidateWebhookFormat(bad); err == nil {
			t.Errorf("ValidateWebhookFormat(%q) = nil, want error (no longer supported)", bad)
		}
	}
}

// recorder is a thread-safe capture of inbound webhook requests.
type recorder struct {
	mu     sync.Mutex
	count  int
	method string
	header http.Header
	body   string
}

func newRecorderServer(t *testing.T, status int) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.count++
		rec.method = r.Method
		rec.header = r.Header.Clone()
		rec.body = string(b)
		rec.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestWebhookDispatcher_FormatPayloads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		format string
		check  func(t *testing.T, rec *recorder)
	}{
		{"ntfy", func(t *testing.T, rec *recorder) {
			if got := rec.header.Get("Title"); got != "T" {
				t.Errorf("ntfy Title header = %q, want T", got)
			}
			if got := rec.header.Get("Tags"); got != "cron.failed" {
				t.Errorf("ntfy Tags header = %q, want cron.failed", got)
			}
			if rec.body != "B" {
				t.Errorf("ntfy body = %q, want plain B (not JSON)", rec.body)
			}
		}},
		{"slack", func(t *testing.T, rec *recorder) {
			var p map[string]string
			if err := json.Unmarshal([]byte(rec.body), &p); err != nil {
				t.Fatalf("slack body not JSON: %v", err)
			}
			if p["text"] != "T\nB" {
				t.Errorf("slack text = %q, want T\\nB", p["text"])
			}
		}},
		{"discord", func(t *testing.T, rec *recorder) {
			var p map[string]string
			if err := json.Unmarshal([]byte(rec.body), &p); err != nil {
				t.Fatalf("discord body not JSON: %v", err)
			}
			if p["content"] != "**T**\nB" {
				t.Errorf("discord content = %q", p["content"])
			}
		}},
		{"raw", func(t *testing.T, rec *recorder) {
			var p map[string]string
			if err := json.Unmarshal([]byte(rec.body), &p); err != nil {
				t.Fatalf("raw body not JSON: %v", err)
			}
			if p["event"] != "cron.failed" || p["title"] != "T" || p["body"] != "B" {
				t.Errorf("raw payload = %+v", p)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			t.Parallel()
			srv, rec := newRecorderServer(t, 200)
			d := NewWebhookDispatcher([]WebhookTarget{{URL: srv.URL, Format: tc.format}}, srv.Client())
			d(context.Background(), "cron.failed", "T", "B")
			if rec.count != 1 {
				t.Fatalf("got %d requests, want 1", rec.count)
			}
			if rec.method != http.MethodPost {
				t.Errorf("method = %q, want POST", rec.method)
			}
			tc.check(t, rec)
		})
	}
}

func TestWebhookDispatcher_EventFilter(t *testing.T) {
	t.Parallel()
	srv, rec := newRecorderServer(t, 200)
	d := NewWebhookDispatcher([]WebhookTarget{
		{URL: srv.URL, Format: "raw", Events: []string{"cron.failed"}},
	}, srv.Client())

	d(context.Background(), "cron.completed", "T", "B") // filtered out
	if rec.count != 0 {
		t.Errorf("cron.completed should be filtered, got %d requests", rec.count)
	}
	d(context.Background(), "cron.failed", "T", "B") // matches
	if rec.count != 1 {
		t.Errorf("cron.failed should fire, got %d requests", rec.count)
	}
}

func TestWebhookDispatcher_EmptyEventsMatchAll(t *testing.T) {
	t.Parallel()
	srv, rec := newRecorderServer(t, 200)
	d := NewWebhookDispatcher([]WebhookTarget{{URL: srv.URL}}, srv.Client())
	d(context.Background(), "trigger.completed", "T", "B")
	if rec.count != 1 {
		t.Errorf("empty events should match all, got %d", rec.count)
	}
}

func TestWebhookDispatcher_5xxRetriesOnce(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	d := NewWebhookDispatcher([]WebhookTarget{{URL: srv.URL}}, srv.Client())
	d(context.Background(), "cron.failed", "T", "B") // must not panic / block
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("5xx should retry exactly once (2 total requests), got %d", got)
	}
}

func TestWebhookDispatcher_4xxNoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	d := NewWebhookDispatcher([]WebhookTarget{{URL: srv.URL}}, srv.Client())
	d(context.Background(), "cron.failed", "T", "B")
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("4xx is a config error, must NOT retry; got %d requests", got)
	}
}

func TestWebhookDispatcher_Cancellation(t *testing.T) {
	t.Parallel()
	srv, rec := newRecorderServer(t, 200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	d := NewWebhookDispatcher([]WebhookTarget{{URL: srv.URL}}, srv.Client())
	d(ctx, "cron.failed", "T", "B") // must return, not hang
	if rec.count != 0 {
		t.Errorf("cancelled ctx should abort before sending, got %d requests", rec.count)
	}
}

func TestNewWebhookDispatcher_AllInvalidReturnsNil(t *testing.T) {
	t.Parallel()
	d := NewWebhookDispatcher([]WebhookTarget{
		{URL: "file:///nope"},
		{URL: "https://ok", Format: "telegram"}, // unknown format
	}, nil)
	if d != nil {
		t.Error("dispatcher over only-invalid targets should be nil")
	}
	if NewWebhookDispatcher(nil, nil) != nil {
		t.Error("nil targets → nil dispatcher")
	}
}

func TestWebhookDispatcher_ConcurrentTargets(t *testing.T) {
	t.Parallel()
	srv, rec := newRecorderServer(t, 200)
	// Three targets to the same server — fired concurrently inside the
	// dispatcher; the shared client + read-only target slice must be
	// race-free (run under -race).
	d := NewWebhookDispatcher([]WebhookTarget{
		{URL: srv.URL, Format: "raw"},
		{URL: srv.URL, Format: "slack"},
		{URL: srv.URL, Format: "ntfy"},
	}, srv.Client())
	d(context.Background(), "cron.failed", "T", "B")
	if rec.count != 3 {
		t.Errorf("all 3 targets should fire, got %d", rec.count)
	}
}

func TestWebhookDispatcher_TemplateRendersValidJSON(t *testing.T) {
	t.Parallel()
	srv, rec := newRecorderServer(t, 200)
	tmpl := `{"msg_type":"text","content":{"text":{"title":"{{title}}","msg":"{{body}}","ev":"{{event}}"}}}`
	d := NewWebhookDispatcher([]WebhookTarget{{URL: srv.URL, Format: "template", Template: tmpl}}, srv.Client())
	// title/body with chars that naive string interpolation would use to
	// break out of the JSON string — the load-bearing escaping test.
	d(context.Background(), "cron.failed", `He said "hi"`, "line1\nline2\tend")
	if rec.count != 1 {
		t.Fatalf("got %d requests, want 1", rec.count)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(rec.body), &p); err != nil {
		t.Fatalf("template body is not valid JSON (escaping broken): %v\nbody=%s", err, rec.body)
	}
	text := p["content"].(map[string]any)["text"].(map[string]any)
	if text["title"] != `He said "hi"` {
		t.Errorf("title = %v, want literal quotes preserved", text["title"])
	}
	if text["msg"] != "line1\nline2\tend" {
		t.Errorf("msg = %q, want newline/tab preserved", text["msg"])
	}
	if text["ev"] != "cron.failed" {
		t.Errorf("ev = %v", text["ev"])
	}
}

func TestNewWebhookDispatcher_EmptyTemplateDropped(t *testing.T) {
	t.Parallel()
	if NewWebhookDispatcher([]WebhookTarget{{URL: "https://ok", Format: "template"}}, nil) != nil {
		t.Error("format=template with empty Template should be dropped → nil dispatcher")
	}
}

func TestSendTestWebhook(t *testing.T) {
	t.Parallel()
	okSrv, _ := newRecorderServer(t, 200)
	if err := SendTestWebhook(context.Background(), WebhookTarget{URL: okSrv.URL, Format: "raw"}, okSrv.Client()); err != nil {
		t.Errorf("2xx probe should succeed, got %v", err)
	}
	badSrv, _ := newRecorderServer(t, 500)
	if err := SendTestWebhook(context.Background(), WebhookTarget{URL: badSrv.URL}, badSrv.Client()); err == nil {
		t.Error("5xx probe should return an error for the CLI to report")
	}
	if err := SendTestWebhook(context.Background(), WebhookTarget{URL: "file:///x"}, nil); err == nil {
		t.Error("bad scheme should fail validation before any request")
	}
}
