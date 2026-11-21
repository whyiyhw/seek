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
	"embed"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// ---- static/ assets (W5, card c_x2au4t) ----

// webStatic holds the page's non-inline asset family. The markdown
// renderer (static/markdown.js) is the FIRST resident — the static/
// split the §4.3 rule pre-wrote for the day the single file bloats.
// Served same-origin from /static/, so CSP's script-src 'self' covers
// them with no hash pinning (only INLINE blocks hash below).
//
//go:embed static
var webStatic embed.FS

// handleStatic serves /static/<name>: flat directory, exact-name
// lookup only — no listing, no traversal (ServeMux's path cleaning is
// the outer belt; the explicit checks are the braces). No auth, the
// page's own posture: assets carry no secrets and must load before
// any token exchange could matter.
func (f *WebFrontend) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	if name == "" || strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	b, err := webStatic.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "application/octet-stream"
	switch {
	case strings.HasSuffix(name, ".js"):
		ct = "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		ct = "text/css; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Write(b)
}

// ---- Content-Security-Policy ----

var (
	cspOnce sync.Once
	cspVal  string
)

// One inline element per match; group 1 is the content BETWEEN the
// tags, verbatim — CSP3 hashes the exact element content, no trimming
// (a leading newline is part of it). Attributed tags (<script
// type=...>) match too; the hash still covers content only.
//
// Opening tags are LINE-ANCHORED. The page's tags all sit at column 0,
// while prose that NAMES a tag ("<style>") in an HTML comment sits
// mid-line — the anchor is what keeps a comment mention from opening a
// phantom block. An unanchored styleBlockRE did exactly that (W5
// c_ni7mzw): a comment above the real <style> said "解析到 <style> 时",
// the match ran from that mention across </script> to the real
// </style>, and the browser — which hashes the REAL block — refused
// the stylesheet under the mismatched policy: every CSS var
// unresolved, whole page unstyled. Go-side tests stayed green because
// they re-derived expectations from the same broken regex; the pin
// against this class is the strip-comments cross-check in
// TestWeb_CSPHeader. Keep the column-0 convention in the HTML, or
// widen the anchor consciously.
//
// Closing tags stay UNanchored on purpose: `<script src=…></script>`
// closes on its own opening line, and an anchored `^</script>` would
// make the lazy group swallow everything up to the NEXT line-initial
// close tag (one phantom mega-block). Inside a real block an inline
// `</script>`/`</style>` terminates it for the HTML parser too, so the
// unanchored close matches browser semantics exactly.
var (
	scriptBlockRE = regexp.MustCompile(`(?sm)^<script\b[^>]*>(.*?)</script>`)
	styleBlockRE  = regexp.MustCompile(`(?sm)^<style\b[^>]*>(.*?)</style>`)
)

// webCSP derives the Content-Security-Policy from the embedded page,
// once per process. Editing webfrontend.html cannot desync the policy
// — the hashes re-derive from the same embed — so W2-page cards don't
// maintain CSP by hand. Scope stays at the §5.5 baseline: default-src
// 'self' (fetch/EventSource same-origin only) plus the two hash
// directives the single-file shape needs. When the static/ split
// lands, the blocks vanish and this collapses to default-src 'self'
// on its own.
//
// img-src 'self' data: is the ONE widening (W5 attachment previews,
// c_56yrcj): a pasted/dropped image is previewed from a FileReader
// data: URL before it is uploaded, and `default-src 'self'` would block
// that. data: is the narrower of the two candidates — blob: would let
// page script mint arbitrary same-origin documents; a data: URL cannot
// execute, it can only be an image the page itself read from the user's
// file. Serving staged attachments back over HTTP was rejected: it would
// add a file-read route for a preview we already hold in memory.
func webCSP() string {
	cspOnce.Do(func() {
		p := "default-src 'self'"
		if toks := cspHashTokens(scriptBlockRE); len(toks) > 0 {
			p += "; script-src 'self' " + strings.Join(toks, " ")
		}
		if toks := cspHashTokens(styleBlockRE); len(toks) > 0 {
			p += "; style-src 'self' " + strings.Join(toks, " ")
		}
		p += "; img-src 'self' data:"
		cspVal = p
	})
	return cspVal
}

func cspHashTokens(re *regexp.Regexp) []string {
	var toks []string
	for _, m := range re.FindAllSubmatchIndex(webPage, -1) {
		content := webPage[m[2]:m[3]]
		// Empty/whitespace content = an external <script src=…></script>
		// shape (W5's /static/ assets) — CSP hashes pin INLINE content,
		// an empty block needs no permission, and the src fetch rides
		// 'self'. Hashing "" would pin a meaningless token.
		if len(strings.TrimSpace(string(content))) == 0 {
			continue
		}
		sum := sha256.Sum256(content)
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
