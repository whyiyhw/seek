// webfetch.go — the `webfetch` Tool implementation: fetch one HTTPS
// URL via GET, returning a size-capped, text-only body. The validator
// in validate.go is the SSRF gate; the custom http.Client wires it
// into the dial path (DNS lookup + IP pinning + redirect re-validation)
// so the model can't reach private addresses even if it controls the
// hostname.
//
// Why a custom Client. The default http.Client lets the OS resolve
// hostnames every dial, which opens us to DNS rebinding: attacker
// returns a public IP at validation time, a private IP at dial time.
// We resolve once, validate, then pin the connection to the validated
// IP via a custom DialContext. Same logic re-runs on every redirect.
//
// What this Tool does NOT do (see PRD §四 non-goals):
//   - POST / PUT / DELETE — GET only, forever
//   - cookies / auth headers / custom headers — fixed set
//   - JS rendering, PDF parsing, binary content
//   - caching — every call is independent
//
// See docs/prd/feature-webfetch.md for the full design + threat model.

package webfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/whyiyhw/seek/internal/tools"
)

const toolName = "webfetch"

const description = "Fetch the body of an HTTPS URL via HTTP GET. Use to read external documentation (API specs, third-party library docs, RFCs, GitHub raw README, blog posts). Returns text content (HTML → plain text, JSON / XML / Markdown raw). Allowed schemes: https only (http requires SEEK_WEBFETCH_ALLOW_HTTP=1). Blocked targets: file://, localhost, private IPs (RFC1918), link-local, loopback, CGNAT — every redirect is re-validated. Body is size-capped (default 64 KiB, max 256 KiB) and returned truncated past the cap with a re-fetch hint. Does NOT do POST, custom headers, auth, cookies, JavaScript rendering, or binary content."

// schemaBytes is package-level so the wire bytes stay identical across
// turns (CLAUDE.md "Tool JSON schemas are package-level []byte constants").
var schemaBytes = []byte(`{
  "type": "object",
  "properties": {
    "url": {
      "type": "string",
      "description": "Absolute https:// URL. http:// is rejected unless SEEK_WEBFETCH_ALLOW_HTTP=1. file://, localhost, and private/loopback/link-local/CGNAT IPs are always rejected (SSRF defense). Redirects are followed (max 5) but each hop is re-validated."
    },
    "max_bytes": {
      "type": "integer",
      "minimum": 1024,
      "maximum": 262144,
      "description": "Maximum response body size in bytes. Default 65536 (64 KiB). Body is truncated past the cap and the result notes truncation + the larger max_bytes value to re-fetch with."
    }
  },
  "required": ["url"],
  "additionalProperties": false
}`)

const (
	// defaultMaxBytes — 64 KiB covers ~80% of typical doc pages
	// after HTML→text simplification (PRD §3.5).
	defaultMaxBytes = 64 * 1024
	// hardMaxBytes — 256 KiB. Matches the schema's `maximum`. Hard
	// ceiling; values higher than this are rejected by the schema
	// before reaching Execute, but we double-check at runtime as
	// defence in depth.
	hardMaxBytes = 256 * 1024
	// minMaxBytes — 1 KiB. Anything smaller than this is more
	// useful as a HEAD probe, which we don't expose; reject so
	// the model can't accidentally truncate to uselessness.
	minMaxBytes = 1024
	// requestTimeout — 30s. Tight enough that a hung server doesn't
	// stall the agent's turn, loose enough for slow CDNs.
	requestTimeout = 30 * time.Second
	// maxRedirects — 5 hops. Generous for legitimate auth-redirect
	// chains, tight enough to catch loops.
	maxRedirects = 5
)

// allowedContentTypes is the text-like MIME prefix allowlist. Match is
// prefix-based on the parsed media type (parameters stripped) so
// charsets don't trip it.
var allowedContentTypes = []string{
	"text/html",
	"text/plain",
	"text/markdown",
	"text/xml",
	"text/csv",
	"text/javascript",
	"application/json",
	"application/xml",
	"application/rss+xml",
	"application/atom+xml",
	"application/ld+json",
	"application/xhtml+xml",
	"application/javascript",
}

// Tool is the webfetch implementation. It holds the resolved
// ValidationOptions + a pre-built http.Client wired to the SSRF gate.
// New constructs a Tool; tests can poke `skipIPValidation` via the
// package-private testing helper to dial loopback for httptest.
type Tool struct {
	opts   ValidationOptions
	client *http.Client

	// skipIPValidation lets unit tests dial httptest servers (which
	// bind to 127.0.0.1) without disabling the validator entirely.
	// Production code MUST NOT touch this. It's package-private and
	// only set via the testing helper in webfetch_test_helpers.go.
	skipIPValidation bool
}

// New constructs a Tool. opts controls URL allow/deny (scheme, ports).
// IP-layer SSRF defense is always on.
func New(opts ValidationOptions) *Tool {
	if opts.AllowedPorts == nil {
		opts.AllowedPorts = DefaultOptions().AllowedPorts
	}
	t := &Tool{opts: opts}
	t.client = t.buildClient()
	return t
}

// Name / Description / Schema satisfy tools.Tool.
func (*Tool) Name() string            { return toolName }
func (*Tool) Description() string     { return description }
func (*Tool) Schema() json.RawMessage { return schemaBytes }

// ReadOnly is true — webfetch never writes locally and uses only GET
// over the wire. Allows agent.Registry to dispatch it concurrently
// with other read-only tools if/when concurrent dispatch lands.
func (*Tool) ReadOnly() bool { return true }

type args struct {
	URL      string `json:"url"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

// Execute parses the args, runs the SSRF gate, dispatches the request,
// validates response Content-Type, reads the body up to max_bytes, and
// returns a model-readable result string. Errors are returned as
// "[webfetch: <category>] …" strings via formatErrorResult so the
// agent's tool-result path surfaces them without crashing the turn.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a args
	if err := tools.UnmarshalStrict(toolName, raw, &a, "url", "max_bytes"); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.URL) == "" {
		return "", tools.MissingField(toolName, "url", raw, "url", "max_bytes")
	}

	limit := a.MaxBytes
	if limit == 0 {
		limit = defaultMaxBytes
	}
	if limit < minMaxBytes || limit > hardMaxBytes {
		return "", fmt.Errorf("%s: max_bytes must be %d..%d, got %d",
			toolName, minMaxBytes, hardMaxBytes, limit)
	}

	u, err := ValidateRequestURL(a.URL, t.opts)
	if err != nil {
		return formatErrorResult(a.URL, err), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return formatErrorResult(a.URL, fmt.Errorf("%w: build request: %v", ErrInvalidURL, err)), nil
	}
	// Fixed header set. NOT model-controlled — letting the model
	// fiddle with headers re-opens the cookie/auth/exfil surface
	// that webfetch was created to close.
	req.Header.Set("User-Agent", "seek-webfetch/0.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,application/json,text/plain,text/markdown,*/*;q=0.5")
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := t.client.Do(req)
	if err != nil {
		return formatErrorResult(a.URL, classifyTransportError(err)), nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return formatHTTPError(a.URL, resp), nil
	}

	ct := resp.Header.Get("Content-Type")
	if !isAllowedContentType(ct) {
		return formatErrorResult(a.URL, fmt.Errorf("%w: response Content-Type %q is not text/json/xml/markdown — webfetch refuses binary content", ErrBlockedTarget, ct)), nil
	}

	// LimitReader caps the read at limit+1 so we can detect overrun.
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return formatErrorResult(a.URL, fmt.Errorf("read body: %w", err)), nil
	}
	truncated := len(bodyBytes) > limit
	if truncated {
		bodyBytes = bodyBytes[:limit]
	}

	rendered := simplifyBody(ct, bodyBytes)

	return formatSuccess(a.URL, resp, rendered, truncated, limit), nil
}

// buildClient assembles the http.Client with the SSRF-aware
// DialContext + redirect re-validation.
func (t *Tool) buildClient() *http.Client {
	transport := &http.Transport{
		// DialContext is the SSRF chokepoint. Steps:
		//   1. Split host:port (network/addr is "tcp" / "host:port").
		//   2. Resolve host via the default resolver.
		//   3. For each candidate IP, run ValidateIP. First public
		//      IP wins; private ones get refused.
		//   4. Dial the validated IP directly so the kernel doesn't
		//      re-resolve and possibly pick a different (private) IP
		//      under DNS rebinding.
		DialContext: t.ssrfDialContext,
		// Conservative defaults appropriate for one-shot doc fetches.
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		// Follow standard proxy env vars (HTTP_PROXY, HTTPS_PROXY,
		// NO_PROXY). Honoring these is the user's choice; if they
		// set HTTPS_PROXY they accept that webfetch URLs traverse
		// their corp proxy.
		Proxy: http.ProxyFromEnvironment,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: too many redirects (> %d)", ErrBlockedTarget, maxRedirects)
			}
			// Re-validate the new URL the same way we validated the
			// original. A server that 302s us to http://10.0.0.1/ is
			// just as dangerous as the model typing that URL directly.
			if _, err := ValidateRequestURL(req.URL.String(), t.opts); err != nil {
				return fmt.Errorf("redirect rejected: %w", err)
			}
			return nil
		},
	}
}

// ssrfDialContext is the dialer that resolves hostname → IP, runs
// ValidateIP, and only then dials. Pins the connection to the
// validated IP literal so subsequent rehandshakes / re-resolutions
// can't race us into a private address.
func (t *Tool) ssrfDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host:port %q: %w", addr, err)
	}

	// If the host is already a literal IP, just validate it directly.
	// No DNS round trip needed.
	if literal := net.ParseIP(host); literal != nil {
		if !t.skipIPValidation {
			if err := ValidateIP(literal); err != nil {
				return nil, err
			}
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}

	// Resolve hostname. Use the default resolver but with our own
	// context so it's cancellable. We resolve to ALL candidates
	// rather than just the first so we can prefer a public one if
	// the OS happened to put a private one first (shouldn't happen
	// for public DNS, but cheap insurance).
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("resolve %q: no IPs returned", host)
	}

	var lastErr error
	for _, ip := range ips {
		if !t.skipIPValidation {
			if err := ValidateIP(ip); err != nil {
				lastErr = err
				continue
			}
		}
		// Dial the validated IP literal directly. This is the SSRF
		// pin: even if the resolver returns a different IP on a
		// subsequent call, we use the one we already validated.
		dialAddr := net.JoinHostPort(ip.String(), port)
		conn, derr := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, dialAddr)
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = errors.New("no usable IPs returned by resolver")
	}
	return nil, fmt.Errorf("dial %s: %w", host, lastErr)
}

// classifyTransportError categorises errors from http.Client.Do so the
// model gets a useful prefix in formatErrorResult.
func classifyTransportError(err error) error {
	if err == nil {
		return nil
	}
	// Redirect/blocked-target errors propagated up from CheckRedirect
	// or DialContext already carry ErrBlockedTarget / ErrBlockedScheme.
	if errors.Is(err, ErrBlockedTarget) {
		return err
	}
	if errors.Is(err, ErrBlockedScheme) {
		return err
	}
	if errors.Is(err, ErrBlockedHost) {
		return err
	}
	if errors.Is(err, ErrBlockedPort) {
		return err
	}
	if errors.Is(err, ErrInvalidURL) {
		return err
	}
	// Context deadline → timeout category.
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("timeout after %s: %w", requestTimeout, err)
	}
	// Generic transport / DNS / TLS error.
	return err
}

// isAllowedContentType prefix-matches the Content-Type header (after
// stripping parameters) against the text-only allowlist.
func isAllowedContentType(ct string) bool {
	if ct == "" {
		return false
	}
	media := ct
	if idx := strings.Index(ct, ";"); idx >= 0 {
		media = ct[:idx]
	}
	media = strings.TrimSpace(strings.ToLower(media))
	for _, allowed := range allowedContentTypes {
		if media == allowed {
			return true
		}
	}
	return false
}

// formatSuccess builds the model-readable result text on a 2xx.
func formatSuccess(originalURL string, resp *http.Response, body string, truncated bool, limit int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "URL: %s\n", originalURL)
	final := resp.Request.URL.String()
	if final != originalURL {
		fmt.Fprintf(&sb, "Final URL: %s\n", final)
	}
	fmt.Fprintf(&sb, "Status: %s\n", resp.Status)
	fmt.Fprintf(&sb, "Content-Type: %s\n", resp.Header.Get("Content-Type"))
	fmt.Fprintf(&sb, "Size: %d bytes", len(body))
	if truncated {
		fmt.Fprintf(&sb, " (truncated at %d; re-fetch with max_bytes=%d if you need more)",
			limit, nextMaxBytesHint(limit))
	}
	sb.WriteString("\n\n")
	sb.WriteString(body)
	return sb.String()
}

// nextMaxBytesHint suggests a larger max_bytes value for re-fetch.
// Doubles up to the hard ceiling.
func nextMaxBytesHint(current int) int {
	next := current * 2
	if next > hardMaxBytes {
		next = hardMaxBytes
	}
	return next
}

// formatHTTPError builds the model-readable result text for a 4xx/5xx.
// Includes a brief body snippet (≤200 chars) so the model can see
// what the server actually said without burning budget on a full read.
func formatHTTPError(originalURL string, resp *http.Response) string {
	const snippetMax = 200
	snip, _ := io.ReadAll(io.LimitReader(resp.Body, snippetMax+1))
	body := string(snip)
	if len(body) > snippetMax {
		body = body[:snippetMax] + "…"
	}
	body = strings.TrimSpace(body)
	return fmt.Sprintf("[webfetch: http error] URL: %s\nStatus: %s\nContent-Type: %s\nBody snippet: %s",
		originalURL, resp.Status, resp.Header.Get("Content-Type"), body)
}

// formatErrorResult wraps a validator / transport / classified error
// into the standard "[webfetch: <category>] …" model-facing string.
func formatErrorResult(originalURL string, err error) string {
	var category string
	switch {
	case errors.Is(err, ErrInvalidURL):
		category = "invalid url"
	case errors.Is(err, ErrBlockedScheme):
		category = "blocked scheme"
	case errors.Is(err, ErrBlockedHost):
		category = "blocked target"
	case errors.Is(err, ErrBlockedPort):
		category = "blocked port"
	case errors.Is(err, ErrBlockedTarget):
		category = "blocked target"
	case strings.Contains(err.Error(), "timeout"):
		category = "timeout"
	default:
		category = "fetch error"
	}
	return fmt.Sprintf("[webfetch: %s] URL: %s\n%v", category, originalURL, err)
}

// resolveURL is a no-op shim kept for the tools package contract; the
// validator already resolved the URL inside Execute.
var _ = url.Parse // keep net/url import alive for future stub use
