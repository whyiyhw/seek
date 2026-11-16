package controlplane

// Static-asset pipeline for the web frontend (W1.5, card c_ph2zax,
// feature-web-frontend §4.2/§4.3/§5.5): the Content-Security-Policy
// header and the runtime gzip handler. The page stays ONE embedded
// file (webfrontend.html — the §4.3 rule: split into static/ only when
// it bloats), which is why the policy is NOT the bare
// `default-src 'self'` of §5.5's wording: a single-file page carries
// its script and CSS inline, and 'self' alone blocks both. The
// standard reconciliation is hash pinning — script-src/style-src carry
// the sha256 of each inline block, derived once at startup from the
// SAME embedded bytes that get served. Anything that is not the
// shipped script (injected markup, eval, a page hand-edited inside a
// repacked binary) fails closed — the same defense §5.5's "no inline
// script/eval" was after, without a build chain or a second file.
//
// gzip is §4.2's answer to "no esbuild minify": the wire size of the
// page comes down at RUNTIME (stdlib compress/gzip), so the build
// chain stays empty. Both live here, ahead of the static/ split, so
// the split inherits them without rewiring.

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// ---- Content-Security-Policy ----

var (
	cspOnce sync.Once
	cspVal  string
)

// One inline element per match; group 1 is the content BETWEEN the
// tags, verbatim — CSP3 hashes the exact element content, no trimming
// (a leading newline is part of it). Attributed tags (<script
// type=...>) match too; the hash still covers content only.
var (
	scriptBlockRE = regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`)
	styleBlockRE  = regexp.MustCompile(`(?s)<style\b[^>]*>(.*?)</style>`)
)

// webCSP derives the Content-Security-Policy from the embedded page,
// once per process. Editing webfrontend.html cannot desync the policy
// — the hashes re-derive from the same embed — so W2-page cards don't
// maintain CSP by hand. Scope stays at the §5.5 baseline: default-src
// 'self' (fetch/EventSource same-origin only) plus the two hash
// directives the single-file shape needs. When the static/ split
// lands, the blocks vanish and this collapses to default-src 'self'
// on its own.
func webCSP() string {
	cspOnce.Do(func() {
		p := "default-src 'self'"
		if toks := cspHashTokens(scriptBlockRE); len(toks) > 0 {
			p += "; script-src 'self' " + strings.Join(toks, " ")
		}
		if toks := cspHashTokens(styleBlockRE); len(toks) > 0 {
			p += "; style-src 'self' " + strings.Join(toks, " ")
		}
		cspVal = p
	})
	return cspVal
}

func cspHashTokens(re *regexp.Regexp) []string {
	var toks []string
	for _, m := range re.FindAllSubmatchIndex(webPage, -1) {
		sum := sha256.Sum256(webPage[m[2]:m[3]])
		toks = append(toks, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return toks
}

// withCSP stamps the policy on every response — uniform and cheap,
// and a future static/ split (extra same-origin assets) is covered
// without touching route code. It is set BEFORE the handler runs so
// WriteHeader picks it up on every path, error bodies included.
func withCSP(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", webCSP())
		h.ServeHTTP(w, r)
	})
}

// ---- gzip transport ----

// gzipCompressible: the content types worth compressing.
// text/event-stream is deliberately ABSENT: gzip buffers, and /events
// must deliver each frame the moment it is written (§10 risk table) —
// compressing SSE would reintroduce exactly the proxy batching the
// X-Accel-Buffering header exists to prevent.
var gzipCompressible = map[string]bool{
	"text/html":              true,
	"text/css":               true,
	"text/plain":             true,
	"application/javascript": true,
	"application/json":       true,
}

// withGzip compresses textual responses for clients that ask
// (Accept-Encoding: gzip). Single-user loopback audience: no writer
// pool, no minimum-size buffer — a few bytes of gzip overhead on tiny
// JSON bodies is noise next to keeping the wrapper flush-transparent.
func withGzip(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) || r.Method == http.MethodHead {
			h.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		h.ServeHTTP(gw, r)
	})
}

// acceptsGzip reports a plain "gzip" in Accept-Encoding. q-values are
// ignored (no browser sends gzip;q=0 in the wild; noted, not handled).
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

// gzipResponseWriter decides compression at header-write time: the
// Content-Type is unknown until the handler sets it, so the first
// WriteHeader (or implicit first Write) is the decision point. Until
// then everything records; after, bytes stream either through a
// gzip.Writer or byte-for-byte through the raw writer. The Flusher
// interface must stay satisfied either way — /events asserts it
// through this wrapper (http.ResponseWriter embedding alone would
// hide it and every SSE response would 500).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer // non-nil from the moment compression engaged
	decided bool
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.decided {
		g.ResponseWriter.WriteHeader(code) // handler double-write: server's problem, pass through
		return
	}
	g.decided = true
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(g.Header().Get("Content-Type"), ";", 2)[0]))
	if gzipCompressible[ct] && g.Header().Get("Content-Encoding") == "" {
		g.Header().Set("Content-Encoding", "gzip")
		g.Header().Del("Content-Length") // the compressed length is unknown until close
		g.gz = gzip.NewWriter(g.ResponseWriter)
	}
	// Responses from this wrapper vary by Accept-Encoding — say so for
	// any cache in between (harmless without one).
	if v := g.Header().Get("Vary"); v == "" {
		g.Header().Set("Vary", "Accept-Encoding")
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK) // implicit header, same decision path
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// Flush forwards flushes through the compression (gzip.Writer.Flush
// emits what is buffered so far WITHOUT ending the stream) so a
// handler that flushes mid-body keeps its per-chunk delivery.
func (g *gzipResponseWriter) Flush() {
	if g.gz != nil {
		g.gz.Flush()
	}
	if fl, ok := g.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// close finishes the gzip stream (the compressed tail) once the
// handler returns. No-op when compression never engaged.
func (g *gzipResponseWriter) close() {
	if g.gz != nil {
		g.gz.Close()
	}
}
