package routines

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// This file implements the Feishu 企业自建应用 bot push path, which replaces
// the old custom-bot incoming webhook (FormatFeishuFlow) and the old
// unsigned-custom-bot interpretation of FormatFeishu. The new path delivers
// via the IM open API:
//
//	1. POST /open-apis/auth/v3/tenant_access_token/internal  → tenant token
//	2. POST /open-apis/im/v1/messages?receive_id_type=<t>    → send message
//
// Two non-obvious contracts (see docs/pitfalls.md "Feishu IM API returns
// HTTP 200 on business errors"):
//   - The IM API signals business failure with HTTP 200 + body code != 0,
//     NOT a 4xx/5xx. We must read and parse the body.
//   - The `content` field of the send-message body is itself a JSON STRING
//     (double-encoded): the outer body is JSON, and the value of `content`
//     is another JSON document serialized to a string.
//
// All network calls are stdlib net/http only — the official SDK is a large
// monolith for two calls, and seek's posture is stdlib-first.

// feishuBase is the open-platform API root. Constant so tests can swap it
// via a local httptest server by pointing an *http.Client transport at it
// is NOT supported — instead the URL is built from this constant, and the
// dispatcher/probe path always hits the real open.feishu.cn. Tests inject
// via a transport that rewrites the host (see webhook_test.go).
const feishuBase = "https://open.feishu.cn/open-apis"

// feishuTokenEndpoint / feishuMessageEndpoint are appended to feishuBase.
const (
	feishuTokenEndpoint   = "/auth/v3/tenant_access_token/internal"
	feishuMessageEndpoint = "/im/v1/messages"
)

// feishuTokenRefreshLead is how long before expiry we proactively refresh.
// Feishu tokens last 7200s; refreshing 5 min early leaves ample slack.
const feishuTokenRefreshLead = 5 * time.Minute

// feishuMsgMaxBytes is the practical per-message ceiling (Feishu's real
// limit is 150KB; we cap a bit under so the truncation suffix fits). Seek
// notifications are KB-scale, so this is defensive only — no real chunking.
const feishuMsgMaxBytes = 140 * 1024

// feishuTokenHolder caches one app's tenant_access_token in memory. It is
// safe for concurrent use: a single in-flight refresh serializes on mu,
// later callers see the freshly cached token.
type feishuTokenHolder struct {
	appID     string
	appSecret string

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// tokenResp models POST /auth/v3/tenant_access_token/internal.
type tokenResp struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"` // seconds
}

// Token returns a non-expired tenant_access_token, refreshing if needed.
// Concurrent callers share one in-flight refresh (mu serializes; the
// second caller blocks until the first writes the new token).
func (h *feishuTokenHolder) Token(ctx context.Context, client *http.Client) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.token != "" && time.Now().Before(h.expiresAt.Add(-feishuTokenRefreshLead)) {
		return h.token, nil
	}
	body, err := json.Marshal(map[string]string{
		"app_id":     h.appID,
		"app_secret": h.appSecret,
	})
	if err != nil {
		return "", fmt.Errorf("feishu: marshal token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		feishuBase+feishuTokenEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("feishu: token request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("feishu: read token response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("feishu: token endpoint HTTP %d: %s", resp.StatusCode, truncateForLog(raw))
	}
	var tr tokenResp
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", fmt.Errorf("feishu: parse token response: %w", err)
	}
	if tr.Code != 0 {
		return "", fmt.Errorf("feishu: token endpoint code=%d msg=%q", tr.Code, tr.Msg)
	}
	if tr.TenantAccessToken == "" || tr.Expire <= 0 {
		return "", fmt.Errorf("feishu: token endpoint returned empty token/expire")
	}
	h.token = tr.TenantAccessToken
	h.expiresAt = time.Now().Add(time.Duration(tr.Expire) * time.Second)
	return h.token, nil
}

// invalidate forces the next Token() call to refresh. Used after a
// "token invalid/expired" error from the message endpoint.
func (h *feishuTokenHolder) invalidate() {
	h.mu.Lock()
	h.token = ""
	h.expiresAt = time.Time{}
	h.mu.Unlock()
}

// validateFeishuBot checks the target has the required bot credentials.
// Called both at dispatcher construction and in the probe path.
func validateFeishuBot(t WebhookTarget) error {
	if strings.TrimSpace(t.AppID) == "" {
		return fmt.Errorf("feishu app_id is required (find it in the Feishu developer console → 凭证与基础信息)")
	}
	if strings.TrimSpace(t.AppSecret) == "" {
		return fmt.Errorf("feishu app_secret is required for app_id=%s", t.AppID)
	}
	if strings.TrimSpace(t.ReceiveID) == "" {
		return fmt.Errorf("feishu receive_id is required (chat_id for a group, open_id for a private chat)")
	}
	switch rt := t.ReceiveIDType; rt {
	case "", "chat_id", "open_id", "user_id", "union_id", "email":
		// ok — empty defaults to chat_id at send time
	default:
		return fmt.Errorf("feishu receive_id_type %q invalid (valid: chat_id, open_id, user_id, union_id, email)", rt)
	}
	return nil
}

// feishuReceiveIDType returns the receive_id_type, defaulting to chat_id.
func feishuReceiveIDType(t WebhookTarget) string {
	if t.ReceiveIDType == "" {
		return "chat_id"
	}
	return t.ReceiveIDType
}

// postFeishuBot is the best-effort dispatcher path (mirrors postWebhook's
// posture: logs a WARN on failure, never returns an error to the caller).
func postFeishuBot(ctx context.Context, client *http.Client, holder *feishuTokenHolder,
	t WebhookTarget, event, title, body string) {
	text := formatFeishuText(event, title, body)
	if err := sendFeishuBot(ctx, client, holder, t, title, text); err != nil {
		fmt.Fprintf(os.Stderr, "routines: feishu app_id=%s receive_id=%s: %v\n",
			t.AppID, t.ReceiveID, err)
	}
}

// sendFeishuBot delivers one message via the IM API, parsing the body
// code (HTTP 200 can still mean failure). On a token-invalid/expired code
// it refreshes the token once and retries. Returns the underlying error
// so the probe path can surface it.
func sendFeishuBot(ctx context.Context, client *http.Client, holder *feishuTokenHolder,
	t WebhookTarget, title, text string) error {
	text = truncateFeishu(text)

	for attempt := 0; attempt < 2; attempt++ {
		token, err := holder.Token(ctx, client)
		if err != nil {
			return err
		}
		req, err := buildFeishuMessageRequest(t, token, text)
		if err != nil {
			return fmt.Errorf("build message request: %w", err)
		}
		resp, err := client.Do(req.WithContext(ctx))
		if err != nil {
			if attempt == 0 && ctx.Err() == nil {
				holder.invalidate() // transport blip — refresh + retry
				continue
			}
			return err
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 500 {
			if attempt == 0 && ctx.Err() == nil {
				continue // server-side 5xx — one retry
			}
			return fmt.Errorf("im/v1/messages HTTP %d: %s", resp.StatusCode, truncateForLog(raw))
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("im/v1/messages HTTP %d: %s", resp.StatusCode, truncateForLog(raw))
		}
		// HTTP 200 — but Feishu still signals failure in the body code.
		var fr feishuAPIResult
		if err := json.Unmarshal(raw, &fr); err != nil {
			return fmt.Errorf("parse im/v1/messages response: %w (body: %s)", err, truncateForLog(raw))
		}
		if fr.Code == 0 {
			return nil // success
		}
		// token-invalid / token-expired → refresh once and retry.
		if isFeishuTokenError(fr.Code) && attempt == 0 {
			holder.invalidate()
			continue
		}
		return fmt.Errorf("feishu im/v1/messages code=%d msg=%q (see https://open.feishu.cn/document/server-docs/api-call-guide/generic-error-code)",
			fr.Code, fr.Msg)
	}
	return nil
}

// feishuAPIResult is the envelope shared by every Feishu open API: a
// top-level {code, msg, data}. We only read code/msg for send-message.
type feishuAPIResult struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// isFeishuTokenError reports whether code indicates an invalid/expired
// tenant_access_token (worth one refresh+retry).
//   - 99991663: access token invalid
//   - 99991664: access token expired
//   - 99991671: include refresh token
//   - 99991668: token type mismatch
//   - 99991672: permission denied (sometimes transient right after grant)
func isFeishuTokenError(code int) bool {
	switch code {
	case 99991663, 99991664, 99991668, 99991671, 99991672:
		return true
	}
	return false
}

// buildFeishuMessageRequest constructs the POST /im/v1/messages request.
// The double-encoding of `content` is the trap: it must be a JSON string
// whose value is itself serialized JSON.
func buildFeishuMessageRequest(t WebhookTarget, token, text string) (*http.Request, error) {
	// inner = {"text":"..."}  — one JSON document
	inner, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	// outer body: receive_id / msg_type / content (content = string(inner))
	payload := map[string]string{
		"receive_id": t.ReceiveID,
		"msg_type":   "text",
		"content":    string(inner), // the serialized inner JSON, as a string
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("receive_id_type", feishuReceiveIDType(t))
	endpoint := feishuBase + feishuMessageEndpoint + "?" + q.Encode()
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// formatFeishuText composes the message body shown to the user. We keep
// the same title + body shape the other formats use.
func formatFeishuText(event, title, body string) string {
	if title == "" {
		return body
	}
	if body == "" {
		return title
	}
	return title + "\n" + body
}

// truncateFeishu caps text at feishuMsgMaxBytes (counting trailing
// suffix), so a runaway cron body can't trip the 150KB hard limit.
func truncateFeishu(text string) string {
	if len(text) <= feishuMsgMaxBytes {
		return text
	}
	const suffix = "…(truncated)"
	cut := feishuMsgMaxBytes - len(suffix)
	if cut < 0 {
		return suffix
	}
	// Step back to a UTF-8 boundary so we don't emit a half-rune.
	for cut > 0 && !utf8RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + suffix
}

// utf8RuneStart reports whether b is the first byte of a UTF-8 rune.
func utf8RuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// truncateForLog trims a response body for inclusion in an error message
// so a 50KB HTML error page doesn't blow up stderr.
func truncateForLog(b []byte) string {
	const max = 512
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
