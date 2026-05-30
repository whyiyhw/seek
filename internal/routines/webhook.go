package routines

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// WebhookDispatcher fans a (event, title, body) notification out to every
// configured push webhook whose events filter matches. It is the optional
// SIBLING of Notifier (notify.go): the OS Notifier carries only
// (title, body), but webhook routing needs the event so a channel can
// subscribe to e.g. only "cron.failed". Rather than widen the Notifier
// contract — and break every platform shim + stub — webhooks get their
// own dispatcher threaded alongside it (CLAUDE.md "don't break the main
// contract; add an OPTIONAL sibling"). nil = no webhooks configured.
//
// Best-effort, exactly like Notifier: a POST failure writes WARN to
// stderr but never blocks or rolls back the cron/trigger run that
// produced the notification. The call is synchronous (waits for all
// targets, bounded by the per-request timeout) on purpose — a cron tick
// is a short-lived process, so a fire-and-forget goroutine would be
// killed when the process exits before the POST lands.
type WebhookDispatcher func(ctx context.Context, event, title, body string)

// WebhookTarget is one parsed push destination. cmd/seek maps the on-disk
// config.PushWebhook onto this so the routines package stays agnostic of
// the config package (no import cycle, clean layering).
type WebhookTarget struct {
	URL    string
	Format string   // ntfy | slack | discord | raw (default raw)
	Events []string // empty = every event
}

// Webhook payload formats.
const (
	FormatNtfy    = "ntfy"
	FormatSlack   = "slack"
	FormatDiscord = "discord"
	FormatFeishu  = "feishu"
	FormatRaw     = "raw"
)

// webhookTimeout caps each individual POST. Short: a notification that
// can't land in 5s isn't worth delaying the tick for.
const webhookTimeout = 5 * time.Second

// ValidateWebhookURL gates a push-webhook URL. Unlike webfetch's SSRF gate
// (internal/tools/webfetch/validate.go), this deliberately does NOT block
// private / loopback addresses: a webhook URL is something the user
// statically wrote into their own ~/.seek/config.json, not a target the
// model chose, and self-hosted / LAN push (ntfy on a homelab box, a Slack
// relay on the intranet) is a first-class use case. The only check is the
// scheme — http/https — to catch config typos and reject exotic schemes
// (file://, gopher://, …). See docs/prd/feature-mobile-push.md §D3.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid webhook URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URL %q: scheme must be http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("webhook URL %q: missing host", raw)
	}
	return nil
}

// ValidateWebhookFormat rejects unknown format strings (empty defaults to
// raw and is accepted). Exported so `seek cron config check` can validate
// config before the user relies on it.
func ValidateWebhookFormat(format string) error {
	switch format {
	case "", FormatRaw, FormatNtfy, FormatSlack, FormatDiscord, FormatFeishu:
		return nil
	default:
		return fmt.Errorf("unknown webhook format %q (valid: ntfy, slack, discord, feishu, raw)", format)
	}
}

// NewWebhookDispatcher builds a dispatcher over the given targets. Invalid
// targets (bad URL / unknown format) are dropped with a WARN at
// construction time so a typo doesn't silently swallow every later
// notification. Returns nil when no valid target remains, so callers can
// leave TickOptions.Webhook nil and skip dispatch entirely. A nil client
// gets a default 5s-timeout client.
func NewWebhookDispatcher(targets []WebhookTarget, client *http.Client) WebhookDispatcher {
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	valid := make([]WebhookTarget, 0, len(targets))
	for _, t := range targets {
		if err := ValidateWebhookURL(t.URL); err != nil {
			fmt.Fprintf(os.Stderr, "routines: webhook dropped: %v\n", err)
			continue
		}
		if err := ValidateWebhookFormat(t.Format); err != nil {
			fmt.Fprintf(os.Stderr, "routines: webhook %s dropped: %v\n", t.URL, err)
			continue
		}
		if t.Format == "" {
			t.Format = FormatRaw
		}
		valid = append(valid, t)
	}
	if len(valid) == 0 {
		return nil
	}
	return func(ctx context.Context, event, title, body string) {
		var wg sync.WaitGroup
		for _, t := range valid {
			if !eventMatches(t.Events, event) {
				continue
			}
			wg.Add(1)
			go func(t WebhookTarget) {
				defer wg.Done()
				postWebhook(ctx, client, t, event, title, body)
			}(t)
		}
		wg.Wait()
	}
}

// eventMatches reports whether event passes the target's filter. An empty
// filter matches every event.
func eventMatches(events []string, event string) bool {
	if len(events) == 0 {
		return true
	}
	for _, e := range events {
		if e == event {
			return true
		}
	}
	return false
}

// postWebhook delivers one notification to one target, best-effort. It
// retries once on a transport error or 5xx (per v6 §6) — a transient blip
// shouldn't lose the only push the user gets — then gives up with a WARN.
// 4xx is not retried: that's a config problem (bad token / topic), and
// hammering it won't help.
func postWebhook(ctx context.Context, client *http.Client, t WebhookTarget, event, title, body string) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := buildWebhookRequest(t, event, title, body)
		if err != nil {
			// Build errors are deterministic — no point retrying.
			fmt.Fprintf(os.Stderr, "routines: webhook %s: build failed: %v\n", t.URL, err)
			return
		}
		resp, err := client.Do(req.WithContext(ctx))
		if err != nil {
			if attempt == 0 && ctx.Err() == nil {
				continue // transport blip — one retry
			}
			fmt.Fprintf(os.Stderr, "routines: webhook %s: %v\n", t.URL, err)
			return
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		if status >= 500 && attempt == 0 && ctx.Err() == nil {
			continue // server-side 5xx — one retry
		}
		if status >= 400 {
			fmt.Fprintf(os.Stderr, "routines: webhook %s: HTTP %d\n", t.URL, status)
		}
		return
	}
}

// SendTestWebhook delivers a single test notification to one target and
// RETURNS the outcome (unlike the best-effort dispatcher, which only
// WARNs). It is the primitive behind `seek cron config check --probe`:
// the CLI wants a per-target pass/fail so the user can confirm each
// channel is reachable before relying on it (feature-mobile-push.md §D5).
// Bypasses the events filter on purpose — a probe should reach every
// configured target regardless of which events it subscribes to.
func SendTestWebhook(ctx context.Context, target WebhookTarget, client *http.Client) error {
	if err := ValidateWebhookURL(target.URL); err != nil {
		return err
	}
	if err := ValidateWebhookFormat(target.Format); err != nil {
		return err
	}
	if target.Format == "" {
		target.Format = FormatRaw
	}
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	req, err := buildWebhookRequest(target, "test", "seek webhook test",
		"If you can read this, your seek push webhook is configured correctly.")
	if err != nil {
		return err
	}
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// buildWebhookRequest constructs the POST for one target + payload,
// per-format. See docs/prd/feature-mobile-push.md §D6.
func buildWebhookRequest(t WebhookTarget, event, title, body string) (*http.Request, error) {
	switch t.Format {
	case FormatNtfy:
		// ntfy.sh: plain-text body, metadata in headers (NOT JSON body).
		req, err := http.NewRequest(http.MethodPost, t.URL, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Title", title)
		req.Header.Set("Tags", event)
		return req, nil
	case FormatSlack:
		return jsonRequest(t.URL, map[string]string{"text": title + "\n" + body})
	case FormatDiscord:
		return jsonRequest(t.URL, map[string]string{"content": "**" + title + "**\n" + body})
	case FormatFeishu:
		// Feishu / Lark custom-bot incoming webhook: msg_type "text" with a
		// nested content.text. CAVEAT: a keyword- or signature-protected bot
		// rejects this (keyword bots need the keyword in the text; signed
		// bots need timestamp+sign) AND Feishu signals such errors with
		// HTTP 200 + a non-zero `code` in the body — which postWebhook's
		// status-only check can't see. Best-effort by design; use an
		// unsigned custom bot (or one whose keyword you include) for
		// reliable delivery.
		return jsonRequest(t.URL, map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": title + "\n" + body},
		})
	case FormatRaw, "":
		return jsonRequest(t.URL, map[string]string{"event": event, "title": title, "body": body})
	default:
		return nil, fmt.Errorf("unknown webhook format %q", t.Format)
	}
}

func jsonRequest(rawURL string, payload any) (*http.Request, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}
