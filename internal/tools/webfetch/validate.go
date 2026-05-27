// Package webfetch implements the `webfetch` tool: a narrow,
// opinionated HTTP GET path the agent can use in plan-analyze (and
// anywhere else) to read external documentation without opening the
// broad attack surface of letting the model run curl.
//
// validate.go is the URL / IP gate. It runs in two phases:
//
//   - Pre-request (ValidateRequestURL): parse the URL, check the
//     scheme / hostname / port. No DNS. Catches obvious junk before
//     we burn a network round-trip.
//
//   - Post-DNS (ValidateIP): given a resolved IP, reject anything
//     non-publicly-routable. This is the SSRF backbone — the model
//     can name `evil.com`, DNS can return `127.0.0.1`, and only this
//     layer catches that. The Tool wires this into a custom DialContext
//     so we never connect to a private IP even if the OS resolver
//     would race with us; we do the lookup ourselves once and pin
//     the connection to the validated IP.
//
// The validator is also called by the Tool's CheckRedirect hook —
// every 30x hop re-runs URL + IP validation. A server that 302s us
// to `http://10.0.0.1/` is just as dangerous as a model that types
// the URL directly.
//
// See docs/prd/feature-webfetch.md §2.3 for the full SSRF defense
// layering and rationale.
package webfetch

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ValidationOptions controls per-call validation behaviour. Defaults
// (see DefaultOptions) are strict; callers loosen via env-driven
// overrides at the Tool construction site.
type ValidationOptions struct {
	// AllowHTTP permits the http:// scheme. False (default) restricts
	// to https://. Set true when SEEK_WEBFETCH_ALLOW_HTTP=1 — typical
	// use case is internal company docs sites that haven't moved to TLS.
	AllowHTTP bool

	// AllowedPorts is the set of TCP ports webfetch will dial. Anything
	// outside this set is rejected up-front to prevent the model from
	// pointing at local services (Redis 6379, Postgres 5432, SSH 22, …)
	// via a URL with an explicit port. Defaults to the typical web
	// stack: 80, 443, 8080, 8443. A nil slice means "default set".
	AllowedPorts []int
}

// DefaultOptions returns the strict default validation posture: HTTPS
// only, standard web ports only.
func DefaultOptions() ValidationOptions {
	return ValidationOptions{
		AllowHTTP:    false,
		AllowedPorts: []int{80, 443, 8080, 8443},
	}
}

// Errors returned by the validator. Tool callers wrap these into the
// "[webfetch: <category>]" prefix used in the result string sent back
// to the model (see PRD §2.8). Test code asserts via errors.Is.
var (
	ErrInvalidURL    = errors.New("invalid url")
	ErrBlockedScheme = errors.New("blocked scheme")
	ErrBlockedHost   = errors.New("blocked host")
	ErrBlockedPort   = errors.New("blocked port")
	ErrBlockedTarget = errors.New("blocked target")
)

// ValidateRequestURL parses rawURL and runs the pre-DNS portion of
// the SSRF gate: scheme allow-list (https; http if opts.AllowHTTP),
// non-empty host, host blacklist (localhost, "0.0.0.0", etc.), and
// port allow-list (from opts.AllowedPorts or DefaultOptions).
//
// Returns the parsed *url.URL on success; nil + wrapped sentinel
// (ErrInvalidURL / ErrBlockedScheme / ErrBlockedHost / ErrBlockedPort)
// on rejection.
func ValidateRequestURL(rawURL string, opts ValidationOptions) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	if !u.IsAbs() {
		return nil, fmt.Errorf("%w: URL must be absolute (got %q)", ErrInvalidURL, rawURL)
	}

	switch strings.ToLower(u.Scheme) {
	case "https":
		// always OK
	case "http":
		if !opts.AllowHTTP {
			return nil, fmt.Errorf("%w: http:// is not allowed (set SEEK_WEBFETCH_ALLOW_HTTP=1 to enable)", ErrBlockedScheme)
		}
	default:
		return nil, fmt.Errorf("%w: scheme %q is not allowed (only https / http)", ErrBlockedScheme, u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: URL has no host", ErrInvalidURL)
	}
	if isBlockedHostname(host) {
		return nil, fmt.Errorf("%w: hostname %q is in the SSRF blacklist", ErrBlockedHost, host)
	}

	// Port resolution. Explicit port → parse + check; missing port →
	// derive from scheme and check that's in the allowlist too (it
	// will be for https/http, but defensive against future scheme
	// additions).
	port := portFor(u)
	allowed := opts.AllowedPorts
	if allowed == nil {
		allowed = DefaultOptions().AllowedPorts
	}
	if !portInAllowlist(port, allowed) {
		return nil, fmt.Errorf("%w: port %d not in allowlist %v", ErrBlockedPort, port, allowed)
	}

	return u, nil
}

// ValidateIP returns nil if ip is publicly routable, or a wrapped
// ErrBlockedTarget describing the rejection category. This is the
// "what the host actually resolves to" layer of the SSRF gate;
// hostname-level checks in ValidateRequestURL are necessary but not
// sufficient (DNS rebinding, attacker-controlled domains, IPv4-mapped
// IPv6 loopback, etc. all bypass hostname-only filters).
//
// Rejection categories (PRD §2.3 Layer 4):
//   - Loopback: 127.0.0.0/8, ::1
//   - Link-local: 169.254.0.0/16, fe80::/10
//   - Private (RFC1918, RFC4193): 10/8, 172.16/12, 192.168/16, fc00::/7
//   - CGNAT: 100.64.0.0/10
//   - Unspecified: 0.0.0.0, ::
//   - Multicast / broadcast: 224.0.0.0/4, 255.255.255.255, ff00::/8
//
// IPv4-mapped IPv6 addresses (::ffff:a.b.c.d) are normalised to IPv4
// before classification so ::ffff:127.0.0.1 is correctly identified
// as loopback.
func ValidateIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("%w: nil IP", ErrBlockedTarget)
	}
	// Normalise IPv4-mapped IPv6 → IPv4 so the standard library
	// classifiers fire on the actual address family.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	switch {
	case ip.IsUnspecified():
		return fmt.Errorf("%w: unspecified address %s", ErrBlockedTarget, ip)
	case ip.IsLoopback():
		return fmt.Errorf("%w: loopback address %s", ErrBlockedTarget, ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("%w: link-local address %s", ErrBlockedTarget, ip)
	case ip.IsPrivate():
		return fmt.Errorf("%w: private address %s (RFC1918 / RFC4193)", ErrBlockedTarget, ip)
	case ip.IsMulticast():
		return fmt.Errorf("%w: multicast address %s", ErrBlockedTarget, ip)
	case isCGNAT(ip):
		return fmt.Errorf("%w: CGNAT address %s (RFC6598)", ErrBlockedTarget, ip)
	case ip.Equal(net.IPv4bcast):
		return fmt.Errorf("%w: broadcast address %s", ErrBlockedTarget, ip)
	}
	return nil
}

// portFor returns the effective port number for u: explicit if
// present, otherwise the scheme default.
func portFor(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return -1
		}
		return n
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return 443
	case "http":
		return 80
	}
	return -1
}

func portInAllowlist(port int, allowed []int) bool {
	for _, p := range allowed {
		if p == port {
			return true
		}
	}
	return false
}

// isBlockedHostname covers the cheap hostname-string-level rejects
// that don't need DNS. The IP-level checks in ValidateIP are the real
// SSRF gate — this is just the fast-path "don't even bother dialing"
// filter for obvious junk.
func isBlockedHostname(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "" {
		return true
	}
	// "localhost" and any *.localhost subdomain (RFC 6761 reserves
	// localhost as a special TLD; some platforms resolve it to ::1).
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	// Placeholder / wildcard binds. ::, 0.0.0.0 are obvious. Some
	// libraries treat empty IPv6 as binding to all interfaces.
	switch h {
	case "0.0.0.0", "0", "::", "[::]":
		return true
	}
	// IPv6 literal of all-zeros (bracketed in URL but Hostname() strips them).
	if strings.HasPrefix(h, "[") || strings.HasSuffix(h, "]") {
		return true
	}
	return false
}

// isCGNAT returns true for 100.64.0.0/10 — RFC6598 carrier-grade NAT
// space. Go's net package has no helper for this even though IsPrivate
// covers RFC1918, so we add it manually.
func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	// 100.64.0.0/10: first octet 100, second octet 64-127
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}
