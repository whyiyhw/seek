package controlplane

// W1.2 auth evolution (feature-web-frontend §5.1–5.3, card c_w5lidm):
// the token's three credential forms, the write-surface CSRF bar, and
// the Host allowlist. Token generation / 0600 / constant-time compare
// stay in webfrontend.go untouched — this file only ADDS:
//
//   - authKind: the cookie as a third credential (?token= and Bearer
//     keep working verbatim for the curl/script face, §5.1);
//   - the /?token= first-hop 303 exchange (handlePage) that mints the
//     HttpOnly cookie and scrubs the URL;
//   - writeGate: a cookie alone must not cross the write surface
//     (§5.2) — POST /msg additionally needs a same-origin Origin
//     header (browsers always attach one to POSTs) or X-Seek-Token;
//   - gateHost/hostAllowed: the DNS-rebinding defense (§5.3), wrapped
//     around EVERY route — depth only, the token is the real gate.

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// webTokenCookie is the HttpOnly session cookie minted by the /?token=
// first-hop exchange. Host-only (no Domain attribute): it rides the
// exact host the browser opened, so a tailnet forward and a loopback
// open keep separate sessions. Session-scoped (no Max-Age/Expires):
// it dies with the browser, token rotation is "delete the file +
// restart" either way (§5.1).
const webTokenCookie = "seek_web_token"

// webTokenHeader is §5.2's second write-gate key: a header only the
// token holder can produce (cross-site pages cannot set custom headers
// without a CORS preflight this server never approves).
const webTokenHeader = "X-Seek-Token"

// authCred names which credential satisfied the token check — the
// write surface cares (§5.2): param/bearer requests already BEAR the
// token, so they are their own CSRF defense; only cookie requests
// need the extra Origin/X-Seek-Token bar.
type authCred int

const (
	credNone   authCred = iota // no valid credential → 401
	credParam                  // ?token= or Authorization: Bearer (script face)
	credCookie                 // the W1.2 HttpOnly cookie (browser face)
)

// authKind authenticates r, reporting WHICH credential passed.
// Precedence: query param, Bearer, cookie — a present-but-wrong
// credential never falls through to a later form (same shape the MVP's
// auth had for query-over-Bearer). Comparison is constant-time on
// every branch.
func (f *WebFrontend) authKind(r *http.Request) authCred {
	tok, err := f.ensureToken()
	if err != nil {
		return credNone
	}
	got := r.URL.Query().Get("token")
	if got == "" {
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		}
	}
	if got != "" {
		if subtle.ConstantTimeCompare([]byte(got), []byte(tok)) == 1 {
			return credParam
		}
		return credNone
	}
	if c, err := r.Cookie(webTokenCookie); err == nil && c.Value != "" {
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(tok)) == 1 {
			return credCookie
		}
	}
	return credNone
}

// auth keeps the MVP's boolean shape — the read surfaces (/state,
// /events, /rpc, /subagents) only need "is this request token'd",
// cookie included since W1.2.
func (f *WebFrontend) auth(r *http.Request) bool { return f.authKind(r) != credNone }

// setTokenCookie mints the §5.1 first-hop cookie: HttpOnly (no JS
// reach — the XSS blast radius shrinks to "exfiltrate via fetch",
// which the same-origin CSP already pins), SameSite=Lax (blocks the
// cookie on cross-site POSTs — most of the CSRF surface gone before
// writeGate even runs), Path=/ , Secure only under TLS (serve itself
// is plain HTTP; the WireGuard-encrypted tailnet forward does not
// terminate TLS here).
func (f *WebFrontend) setTokenCookie(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{
		Name:     webTokenCookie,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// writeGate is the §5.2 CSRF bar on the write surface: a cookie alone
// must not drive execution. Either a same-origin Origin header (the
// allow set is the request's own origin — scheme + the already
// Host-gated r.Host; browsers attach Origin to every POST, and a
// cross-site page's Origin names ITS origin) or X-Seek-Token (for
// cookie-holding non-browser clients). An explicit-but-wrong
// X-Seek-Token fails closed, like every other credential form.
func (f *WebFrontend) writeGate(r *http.Request) bool {
	if h := r.Header.Get(webTokenHeader); h != "" {
		tok, err := f.ensureToken()
		return err == nil && subtle.ConstantTimeCompare([]byte(h), []byte(tok)) == 1
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin, scheme+"://"+r.Host)
}

// hostAllowed is the §5.3 allowlist: the Host header's host part must
// be "localhost" (case-insensitive) or an IP literal in the loopback /
// tailnet-CGNAT families — the same audience checkWebAddr gates the
// bind to. Deviations from the PRD's literal set {bind 地址,
// localhost[:port], 127.0.0.1[:port]}, deliberately:
//
//   - host part only, never the port: the port is simply where the
//     client reached this listener; matching it would 403 the
//     documented tailnet posture (§5.4 TCP forward of a tailnet IP
//     onto 127.0.0.1:<port> — Host carries the tailnet IP, not the
//     bind) and every httptest hit (random port vs DefaultWebAddr).
//   - any loopback/CGNAT literal, not just 127.0.0.1: DNS rebinding
//     needs an attacker-controlled NAME resolving to the victim — an
//     IP literal cannot be rebound, so widening among literals keeps
//     the defense exactly where the threat is (names like
//     attacker.com, and 127.0.0.1.evil.com is a name, not a literal).
//
// §5.3's own framing applies: this is depth, the token is the gate.
func hostAllowed(host string) bool {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || tailnetCGNAT.Contains(ip)
	}
	return false
}

// gateHost wraps the whole route table (§5.3): rebinding pages reach
// every surface, the page included, so the check sits outside the mux
// and before any auth. Same JSON error shape as the auth 401s.
func gateHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "host not allowed"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
