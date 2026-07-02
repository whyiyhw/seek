package routines

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rewriteTransport RoundTrips every request onto target (an httptest
// server URL), preserving path, query, body and headers. This lets the
// feishu_bot code — which builds URLs from the package feishuBase
// constant — be exercised end-to-end against a local fake without
// changing production code or poking DNS.
type rewriteTransport struct{ target string }

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = "http"
	clone.URL.Host = strings.TrimPrefix(strings.TrimPrefix(t.target, "https://"), "http://")
	clone.RequestURI = "" // must be cleared for a client-side RoundTripper
	clone.Host = ""
	return http.DefaultTransport.RoundTrip(clone)
}

func redirectClient(srv *httptest.Server) *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &rewriteTransport{target: srv.URL},
	}
}

// feishuFake is a configurable fake of open.feishu.cn covering the two
// endpoints feishu_bot.go hits (token + message). Every response is JSON
// with HTTP 200 — the trap shape, so tests exercise that the production
// code actually parses the body code.
type feishuFake struct {
	mu           sync.Mutex
	tokenHits    int
	messageHits  int
	lastAuth     string
	lastQuery    string
	lastBody     string
	failMsgCode  int // non-zero → message endpoint returns this code
	failTokenCode int // non-zero → token endpoint returns this code
	// clearMsgCodeAfterHit flips failMsgCode to 0 after the first message
	// hit, so the refresh-retry path succeeds on attempt 2.
	clearMsgCodeAfterHit bool
}

func (f *feishuFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The rewrite transport preserves the full production path
		// (/open-apis/auth/...); match by suffix so the test stays
		// coupled to the endpoint constant, not the API base prefix.
		switch {
		case strings.HasSuffix(r.URL.Path, feishuTokenEndpoint):
			f.tokenHits++
			if f.failTokenCode != 0 {
				writeJSON(w, map[string]any{"code": f.failTokenCode, "msg": "fake token fail"})
				return
			}
			writeJSON(w, map[string]any{
				"code": 0, "msg": "ok",
				"tenant_access_token": "t-fake", "expire": 7200,
			})
		case strings.HasSuffix(r.URL.Path, feishuMessageEndpoint):
			f.messageHits++
			f.lastAuth = r.Header.Get("Authorization")
			f.lastQuery = r.URL.RawQuery
			f.lastBody = string(body)
			code := f.failMsgCode
			if f.clearMsgCodeAfterHit && f.messageHits >= 1 {
				f.failMsgCode = 0
			}
			if code != 0 {
				writeJSON(w, map[string]any{"code": code, "msg": "fail", "data": map[string]any{}})
				return
			}
			writeJSON(w, map[string]any{
				"code": 0, "msg": "success",
				"data": map[string]any{"message_id": "om_test"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	_, _ = w.Write(b)
}

// startFeishuFake returns the running server + the fake (for assertions)
// + a client that redirects feishuBase requests onto it.
func startFeishuFake(t *testing.T) (*httptest.Server, *feishuFake, *http.Client) {
	t.Helper()
	f := &feishuFake{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return srv, f, redirectClient(srv)
}

// --- Validation ------------------------------------------------------------

func TestValidateFeishuBot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		target  WebhookTarget
		wantErr string
	}{
		{"missing app_id", WebhookTarget{AppSecret: "s", ReceiveID: "oc_x"}, "app_id"},
		{"missing app_secret", WebhookTarget{AppID: "a", ReceiveID: "oc_x"}, "app_secret"},
		{"missing receive_id", WebhookTarget{AppID: "a", AppSecret: "s"}, "receive_id"},
		{"bad receive_id_type", WebhookTarget{AppID: "a", AppSecret: "s", ReceiveID: "x", ReceiveIDType: "phone"}, "receive_id_type"},
		{"ok chat_id default", WebhookTarget{AppID: "a", AppSecret: "s", ReceiveID: "oc_x"}, ""},
		{"ok open_id explicit", WebhookTarget{AppID: "a", AppSecret: "s", ReceiveID: "ou_x", ReceiveIDType: "open_id"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateFeishuBot(tc.target)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("got %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

// --- Happy path -----------------------------------------------------------

func TestFeishuBot_SendSuccess(t *testing.T) {
	t.Parallel()
	_, f, client := startFeishuFake(t)

	holder := &feishuTokenHolder{appID: "app1", appSecret: "secret"}
	target := WebhookTarget{AppID: "app1", AppSecret: "secret",
		ReceiveID: "oc_chat", ReceiveIDType: "chat_id"}

	// sendFeishuBot takes a pre-formatted text (the production caller
	// postFeishuBot composes title+body via formatFeishuText). Mirror
	// that here so we assert on the wire shape the user actually sees.
	if err := sendFeishuBot(context.Background(), client, holder, target, "T", "T\nhello body"); err != nil {
		t.Fatalf("sendFeishuBot: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenHits != 1 {
		t.Errorf("token hits = %d, want 1 (first call mints)", f.tokenHits)
	}
	if f.messageHits != 1 {
		t.Errorf("message hits = %d, want 1", f.messageHits)
	}
	if f.lastAuth != "Bearer t-fake" {
		t.Errorf("Authorization = %q, want Bearer t-fake", f.lastAuth)
	}
	if !strings.Contains(f.lastQuery, "receive_id_type=chat_id") {
		t.Errorf("query = %q, want receive_id_type=chat_id", f.lastQuery)
	}
	// content must be a JSON STRING (double-encoded) whose inner JSON is {"text": ...}.
	var p map[string]any
	if err := json.Unmarshal([]byte(f.lastBody), &p); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if p["receive_id"] != "oc_chat" || p["msg_type"] != "text" {
		t.Errorf("receive_id/msg_type = %v/%v", p["receive_id"], p["msg_type"])
	}
	contentStr, ok := p["content"].(string)
	if !ok {
		t.Fatalf("content is %T, want string (double-encoded)", p["content"])
	}
	var inner map[string]string
	if err := json.Unmarshal([]byte(contentStr), &inner); err != nil {
		t.Fatalf("content %q is not valid JSON: %v", contentStr, err)
	}
	if inner["text"] != "T\nhello body" {
		t.Errorf("content.text = %q, want T\\nhello body", inner["text"])
	}
}

// --- Token caching --------------------------------------------------------

func TestFeishuBot_TokenCachedAcrossSends(t *testing.T) {
	t.Parallel()
	_, f, client := startFeishuFake(t)

	holder := &feishuTokenHolder{appID: "app1", appSecret: "secret"}
	target := WebhookTarget{AppID: "app1", AppSecret: "secret", ReceiveID: "oc_x"}
	for i := 0; i < 3; i++ {
		if err := sendFeishuBot(context.Background(), client, holder, target, "T", "B"); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokenHits != 1 {
		t.Errorf("token hits = %d, want 1 (cached)", f.tokenHits)
	}
	if f.messageHits != 3 {
		t.Errorf("message hits = %d, want 3", f.messageHits)
	}
}

// --- The trap: HTTP 200 + body code != 0 ----------------------------------

func TestFeishuBot_BodyCodeNonZeroIsError(t *testing.T) {
	t.Parallel()
	_, f, client := startFeishuFake(t)
	f.mu.Lock()
	f.failMsgCode = 230002 // data verification failed — NOT a token error
	f.mu.Unlock()

	holder := &feishuTokenHolder{appID: "app1", appSecret: "secret"}
	target := WebhookTarget{AppID: "app1", AppSecret: "secret", ReceiveID: "oc_x"}

	err := sendFeishuBot(context.Background(), client, holder, target, "T", "B")
	if err == nil {
		t.Fatal("got nil, want error on body code != 0 (HTTP 200 trap)")
	}
	if !strings.Contains(err.Error(), "230002") {
		t.Errorf("error %q should contain code 230002", err.Error())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.messageHits != 1 {
		t.Errorf("message hits = %d, want 1 (non-token error must not retry)", f.messageHits)
	}
}

// --- Token-error refresh + retry ------------------------------------------

func TestFeishuBot_TokenErrorRetriesAfterRefresh(t *testing.T) {
	t.Parallel()
	_, f, client := startFeishuFake(t)
	f.mu.Lock()
	f.failMsgCode = 99991664 // token expired → triggers invalidate + retry
	f.clearMsgCodeAfterHit = true
	f.mu.Unlock()

	holder := &feishuTokenHolder{appID: "app1", appSecret: "secret"}
	target := WebhookTarget{AppID: "app1", AppSecret: "secret", ReceiveID: "oc_x"}

	err := sendFeishuBot(context.Background(), client, holder, target, "T", "B")
	if err != nil {
		t.Fatalf("token-error retry: got %v, want nil", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.messageHits != 2 {
		t.Errorf("message hits = %d, want 2 (fail + retry)", f.messageHits)
	}
	if f.tokenHits != 2 {
		t.Errorf("token hits = %d, want 2 (initial + refresh after invalidate)", f.tokenHits)
	}
}

// --- Oversize text truncation ---------------------------------------------

func TestFeishuBot_TruncatesOversizeText(t *testing.T) {
	t.Parallel()
	// Multibyte text well over the limit — must cut on a rune boundary.
	huge := strings.Repeat("中", feishuMsgMaxBytes+1000)
	got := truncateFeishu(huge)
	if len(got) > feishuMsgMaxBytes {
		t.Errorf("truncated len = %d, want <= %d", len(got), feishuMsgMaxBytes)
	}
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("should end with suffix; tail = %q", got[len(got)-30:])
	}
	// Boundary: at-limit text is returned unchanged.
	at := strings.Repeat("a", feishuMsgMaxBytes)
	if truncateFeishu(at) != at {
		t.Error("at-limit text should not be truncated")
	}
}

// --- Dispatcher drops invalid feishu targets ------------------------------

func TestFeishuBot_MissingCredentialsDroppedByDispatcher(t *testing.T) {
	t.Parallel()
	// A feishu target missing app_id is dropped at construction (WARN to
	// stderr) so the dispatcher returns nil — no silent black hole.
	d := NewWebhookDispatcher([]WebhookTarget{
		{Format: "feishu", AppSecret: "s", ReceiveID: "oc_x"}, // no AppID
	}, nil)
	if d != nil {
		t.Errorf("dispatcher with only an invalid feishu target should be nil, got non-nil")
	}
}

// --- Concurrency: token refresh is single-flight --------------------------

func TestFeishuBot_ConcurrentSendsShareOneTokenRefresh(t *testing.T) {
	t.Parallel()
	_, f, client := startFeishuFake(t)

	holder := &feishuTokenHolder{appID: "app1", appSecret: "secret"}
	target := WebhookTarget{AppID: "app1", AppSecret: "secret", ReceiveID: "oc_x"}

	const n = 8
	var wg sync.WaitGroup
	var failCount atomic.Int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sendFeishuBot(context.Background(), client, holder, target, "T", "B"); err != nil {
				failCount.Add(1)
			}
		}()
	}
	wg.Wait()
	if failCount.Load() != 0 {
		t.Errorf("%d/%d concurrent sends failed", failCount.Load(), n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// All n sends went out, but the token was minted exactly once.
	if f.tokenHits != 1 {
		t.Errorf("token hits = %d, want 1 under concurrent fan-out", f.tokenHits)
	}
	if f.messageHits != n {
		t.Errorf("message hits = %d, want %d", f.messageHits, n)
	}
}
