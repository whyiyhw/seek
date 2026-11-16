package controlplane

// W1.2 auth-evolution acceptance (feature-web-frontend §8, card
// c_w5lidm): the cookie credential family, the /?token= first-hop 303
// exchange, the write-surface Origin/X-Seek-Token bar, and the Host
// allowlist — table-driven, all sandbox (httptest; zero real keys).
// The pre-existing family (TestWeb_MsgRequiresToken & co) must stay
// green unmodified: query/Bearer remain first-class credentials.

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWeb_AuthCredentialFamily: the §8 row "cookie 过" plus the 401
// half of the table, over GET /state (a plain request/response route,
// so the family reads as one table). Precedence note pinned alongside:
// a present-but-wrong credential never falls through to a later form.
func TestWeb_AuthCredentialFamily(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	for _, tc := range []struct {
		name   string
		cookie string // cookie value ("" = send no cookie)
		query  string // ?token= value
		bearer string // Authorization bearer value
		want   int
	}{
		{"no credential", "", "", "", http.StatusUnauthorized},
		{"wrong cookie", "not-the-token", "", "", http.StatusUnauthorized},
		{"wrong query token", "", "wrong", "", http.StatusUnauthorized},
		{"wrong bearer", "", "", "wrong", http.StatusUnauthorized},
		{"valid cookie", tok, "", "", http.StatusOK},
		{"valid query token", "", tok, "", http.StatusOK},
		{"valid bearer", "", "", tok, http.StatusOK},
		{"wrong cookie does not shadow a valid form", "not-the-token", tok, "", http.StatusOK},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/state", nil)
		if tc.query != "" {
			q := req.URL.Query()
			q.Set("token", tc.query)
			req.URL.RawQuery = q.Encode()
		}
		if tc.cookie != "" {
			req.Header.Set("Cookie", webTokenCookie+"="+tc.cookie)
		}
		if tc.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

// TestWeb_TokenFirstHopExchange: the §8 row "?token=
// 303+Set-Cookie+Location 无 token". A VALID token on the browser
// entry never renders — 303 to a bare / (Location carries no token:
// address bar, history and referer stay clean) with the §5.2 cookie
// attributes. Absent/wrong tokens serve the page with no cookie.
func TestWeb_TokenFirstHopExchange(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// No-redirect client so the 303 itself is observable.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, tc := range []struct {
		name         string
		url          string
		wantCode     int
		wantCookie   bool
		wantLocation string
	}{
		{"no token serves the page", "/", http.StatusOK, false, ""},
		{"wrong token serves the page, no cookie", "/?token=wrong-token-abcdef", http.StatusOK, false, ""},
		{"valid token 303-exchanges for a cookie", "/?token=" + tok, http.StatusSeeOther, true, "/"},
	} {
		resp, err := noFollow.Get(srv.URL + tc.url)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.wantCode {
			t.Errorf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.wantCode)
		}
		if got := resp.Header.Get("Location"); got != tc.wantLocation {
			t.Errorf("%s: Location = %q, want %q (must never carry the token)", tc.name, got, tc.wantLocation)
		}
		cookies := resp.Cookies()
		if tc.wantCookie {
			if len(cookies) != 1 {
				t.Fatalf("%s: got %d Set-Cookie, want 1", tc.name, len(cookies))
			}
			c := cookies[0]
			if c.Name != webTokenCookie || c.Value != tok {
				t.Errorf("%s: cookie = %s=%s, want %s=<the token>", tc.name, c.Name, c.Value, webTokenCookie)
			}
			// §5.2 attributes: HttpOnly; Secure(若 TLS — plain httptest,
			// so absent here); SameSite=Lax; Path=/.
			if !c.HttpOnly {
				t.Errorf("%s: cookie must be HttpOnly", tc.name)
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Errorf("%s: cookie SameSite = %v, want Lax", tc.name, c.SameSite)
			}
			if c.Path != "/" {
				t.Errorf("%s: cookie Path = %q, want /", tc.name, c.Path)
			}
			if c.Secure {
				t.Errorf("%s: Secure must be TLS-conditional (absent on plain http)", tc.name)
			}
		} else if len(cookies) != 0 {
			t.Errorf("%s: got %d Set-Cookie, want 0", tc.name, len(cookies))
		}
	}

	// End-to-end browser shape: a jar-following client opens the token'd
	// URL, lands on a clean /, and every later request rides the cookie.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	resp, err := client.Get(srv.URL + "/?token=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.String() != srv.URL+"/" {
		t.Fatalf("post-exchange landing = %d %q, want 200 on bare /", resp.StatusCode, resp.Request.URL)
	}
	if !strings.Contains(string(body), "<html") {
		t.Error("post-exchange landing must render the embedded page")
	}
	// The exchanged cookie alone authenticates a stateful route —
	// token-free URL, no Authorization header.
	resp, err = client.Get(srv.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /state on the exchanged cookie = %d, want 200", resp.StatusCode)
	}
}

// TestWeb_WriteGate: the §8 row "写面无 Origin 无 X-Token 头 403" plus
// the pass shapes (§5.2). The gate applies ONLY to cookie-authenticated
// writes: query/Bearer requests bear the token itself (the script face
// curl lives on — no Origin header exists there), so they skip the bar.
func TestWeb_WriteGate(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	post := func(cookie, query, origin, xtok string) int {
		url := srv.URL + "/msg"
		if query != "" {
			url += "?token=" + query
		}
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"text":"hi"}`))
		if cookie != "" {
			req.Header.Set("Cookie", webTokenCookie+"="+cookie)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if xtok != "" {
			req.Header.Set(webTokenHeader, xtok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	self := srv.URL // the request's own origin (http://127.0.0.1:<port>)

	for _, tc := range []struct {
		name           string
		cookie, query  string
		origin, xtoken string
		want           int
	}{
		{"cookie alone: no Origin, no X-Seek-Token", tok, "", "", "", http.StatusForbidden},
		{"cookie + cross-site Origin", tok, "", "http://attacker.example", "", http.StatusForbidden},
		{"cookie + wrong X-Seek-Token", tok, "", "", "wrong", http.StatusForbidden},
		{"cookie + same-origin Origin", tok, "", self, "", http.StatusAccepted},
		{"cookie + valid X-Seek-Token", tok, "", "", tok, http.StatusAccepted},
		{"query token (script face), no Origin", "", tok, "", "", http.StatusAccepted},
	} {
		if got := post(tc.cookie, tc.query, tc.origin, tc.xtoken); got != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestWeb_HostAllowed: the §5.3 allowlist as a pure table. Host PART
// only (localhost / loopback / tailnet-CGNAT literals — IP literals
// cannot be DNS-rebound); the port never matches (documented deviation
// keeping the §5.4 tailnet forward and httptest's random ports legal).
func TestWeb_HostAllowed(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		// The client's own shapes: direct loopback (any port), the
		// tailnet TCP-forward posture (tailnet IP host), portless
		// default-port forms, case-insensitive localhost.
		{"127.0.0.1:8790", true},
		{"127.0.0.2:54321", true},
		{"localhost:8790", true},
		{"LOCALHOST:8790", true},
		{"localhost", true},
		{"127.0.0.1", true},
		{"[::1]:8790", true},
		{"100.80.25.74:8790", true}, // tailnet CGNAT forward (§5.4)
		{"100.64.0.0:8790", true},
		{"100.127.255.254:1", true},
		// Rebinding and strays: names (the rebinding weapon), LAN /
		// public / just-outside-CGNAT literals, empty, and names that
		// merely CONTAIN an allowed literal.
		{"attacker.example", false},
		{"attacker.example:8790", false},
		{"127.0.0.1.evil.example:8790", false},
		{"sub.localhost:8790", false},
		{"192.168.1.5:8790", false},
		{"8.8.8.8:8790", false},
		{"100.63.255.254:8790", false}, // one below the CGNAT block
		{"100.128.0.1:8790", false},    // one above it
		{"", false},
	} {
		if got := hostAllowed(tc.host); got != tc.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestWeb_HostGateWrapsEverything: the rebinding 403 lands BEFORE auth
// (gateHost sits outside the mux, §5.3) — even a request bearing the
// real token is refused while its Host names the attacker.
func TestWeb_HostGateWrapsEverything(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	for _, tc := range []struct {
		name string
		host string
		path string
		want int
	}{
		{"rebinding host on the page", "attacker.example", "/", http.StatusForbidden},
		{"rebinding host on a token'd write", "attacker.example:8790", "/msg?token=" + tok, http.StatusForbidden},
		{"localhost host passes to the page", "localhost", "/", http.StatusOK},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
		req.Host = tc.host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}
