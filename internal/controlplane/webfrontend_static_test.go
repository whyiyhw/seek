package controlplane

// W1.5 asset-pipeline tests (card c_ph2zax acceptance, §8 "embed" and
// the card row): the embedded page pinned by hash (edits become
// conscious — bump the pin in the same commit), the CSP header on /
// (with hashes consistent with the inline blocks it guards), no
// external links in the asset, and the gzip transport in all three
// shapes that matter: compress when asked, identity when not, and SSE
// NEVER compressed (flush semantics beat wire size on /events).

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// webPageSHA256 pins the embedded page (§8: 静态资产 hash 进测试，防手改
// 部署态漂移). Any intentional page edit (the W2 line will make many)
// updates this pin IN THE SAME COMMIT:
//
//	shasum -a 256 internal/controlplane/webfrontend.html
const webPageSHA256 = "e5bdb953336882f7a4d08d749465ed1e8799c13b0a2771b4182f52807e2469ae"

func TestWeb_AssetHashPinned(t *testing.T) {
	sum := sha256.Sum256(webPage)
	if got := hex.EncodeToString(sum[:]); got != webPageSHA256 {
		t.Errorf("embedded page hash drifted\n got: %s\nwant: %s\nif the edit was intentional, update webPageSHA256 in this file (shasum -a 256 internal/controlplane/webfrontend.html)", got, webPageSHA256)
	}
}

func TestWeb_CSPHeader(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("GET / carries no Content-Security-Policy header (W1.5 acceptance: curl / 得 CSP 头)")
	}
	if !strings.HasPrefix(csp, "default-src 'self'") {
		t.Errorf("CSP must start at the §5.5 baseline default-src 'self', got %q", csp)
	}
	// Hash consistency, both directions: every hash token in the policy
	// is the hash of an inline block of the page being served, and every
	// inline block's hash is pinned in the policy. One direction each
	// way catches a policy derived from anything but the served bytes.
	toks := map[string]bool{}
	for _, tok := range strings.Fields(csp) {
		tok = strings.TrimSuffix(tok, ";") // directives are ';'-separated with no space
		if strings.HasPrefix(tok, "'sha256-") {
			toks[tok] = true
		}
	}
	for _, m := range scriptBlockRE.FindAllSubmatchIndex(webPage, -1) {
		content := webPage[m[2]:m[3]]
		// mirror cspHashTokens: external <script src> blocks carry no
		// inline content — nothing to pin (the fetch rides 'self').
		if len(bytes.TrimSpace(content)) == 0 {
			continue
		}
		sum := sha256.Sum256(content)
		want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !toks[want] {
			t.Errorf("inline <script> block hash %s missing from CSP %q — the policy and the served page disagree", want, csp)
		}
	}
	for _, m := range styleBlockRE.FindAllSubmatchIndex(webPage, -1) {
		sum := sha256.Sum256(webPage[m[2]:m[3]])
		want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !toks[want] {
			t.Errorf("inline <style> block hash %s missing from CSP %q — the policy and the served page disagree", want, csp)
		}
	}
	if len(toks) == 0 {
		t.Errorf("single-file page carries inline script/style; CSP %q pins no hashes — both blocks would be blocked", csp)
	}

	// Independent cross-check (W5 c_ni7mzw): re-derive the block set by
	// a DIFFERENT route — strip HTML comments, then run UNanchored
	// regexes — and require every hash to match the policy. The loops
	// above re-run the production regexes, so a regex that opens a
	// phantom block inside a comment ("<style>" named in prose) stays
	// self-consistent and green while the browser refuses the REAL
	// block; this second derivation can't be fooled the same way, so it
	// pins the whole class of extraction drift, not one instance.
	htmlCommentRE := regexp.MustCompile(`(?s)<!--.*?-->`)
	stripped := htmlCommentRE.ReplaceAll(webPage, nil)
	for _, probe := range []*regexp.Regexp{
		regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`),
		regexp.MustCompile(`(?s)<style\b[^>]*>(.*?)</style>`),
	} {
		for _, m := range probe.FindAllSubmatchIndex(stripped, -1) {
			content := stripped[m[2]:m[3]]
			if len(bytes.TrimSpace(content)) == 0 {
				continue
			}
			sum := sha256.Sum256(content)
			want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !toks[want] {
				t.Errorf("comment-stripped re-derivation found block hash %s missing from CSP %q — the extraction regexes disagree with a comment-free view of the page", want, csp)
			}
		}
	}

	// The middleware is route-uniform (a future static/ asset inherits
	// it): spot-check one error-body route.
	resp2, err := http.Post(srv.URL+"/msg", "application/json", strings.NewReader(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.Header.Get("Content-Security-Policy") == "" {
		t.Errorf("CSP must ride every response, got none on POST /msg %d", resp2.StatusCode)
	}
}

// externalLinkRE matches the load surface the no-external-links rule
// guards: src=/href= pointing off-origin (absolute or protocol-relative)
// and CSS @import / url() fetches. Relative fetch('/state') strings are
// invisible to these shapes by design.
var externalLinkRE = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']\s*(?:https?:)?//|@import|url\(`)

func TestWeb_PageHasNoExternalLinks(t *testing.T) {
	// §8 无外链断言: the page is self-sourced (同源自供).
	if m := externalLinkRE.Find(webPage); m != nil {
		t.Errorf("page contains an external-resource reference %q — CSP 'self' would block the load; keep assets same-origin (or embedded)", m)
	}
}

// rawClient sees the wire as-sent. DisableCompression stops the Go
// transport from adding its own Accept-Encoding: gzip when the request
// carries none — and from transparently decoding the answer and
// stripping Content-Encoding. http.DefaultClient does BOTH behind the
// caller's back, which is why the "not asked-for" branch of
// TestWeb_GzipPage previously passed vacuously (see its comment).
var rawClient = &http.Client{Transport: &http.Transport{DisableCompression: true}}

// getRaw issues a GET through rawClient so the test sees the exact
// wire bytes and headers, whatever Accept-Encoding the caller sets.
func getRaw(t *testing.T, url, acceptEnc string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acceptEnc != "" {
		req.Header.Set("Accept-Encoding", acceptEnc)
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestWeb_StaticAsset pins the W5 /static/ route (card c_x2au4t): the
// markdown renderer rides the same CSP/gzip middleware stack as the
// page, answers with the embedded bytes verbatim, and the flat-name
// lookup refuses listings and traversal shapes.
func TestWeb_StaticAsset(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	want, err := webStatic.ReadFile("static/markdown.js")
	if err != nil {
		t.Fatal("embed missing static/markdown.js:", err)
	}

	// Served verbatim with the JS content type and the CSP stamp (the
	// asset is same-origin, so 'self' admits it — no hash needed).
	resp := getRaw(t, srv.URL+"/static/markdown.js", "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /static/markdown.js = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/javascript; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/javascript; charset=utf-8", ct)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("static asset response carries no CSP — withCSP must wrap every route")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("served asset = %d bytes, want the embedded %d verbatim", len(got), len(want))
	}

	// The transport covers the new asset too: asked-for gzip arrives
	// compressed and round-trips.
	zr := getRaw(t, srv.URL+"/static/markdown.js", "gzip")
	defer zr.Body.Close()
	if zr.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Accept-Encoding: gzip on /static/markdown.js → Content-Encoding %q, want gzip", zr.Header.Get("Content-Encoding"))
	}
	dec, err := gzip.NewReader(zr.Body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(dec); err != nil {
		t.Fatal("gunzipped asset does not parse:", err)
	}

	// Flat directory, exact names only: no listing, no unknown file,
	// no traversal shape reaches the embed.
	for _, bad := range []string{"/static/", "/static/nope.js", "/static/markdown.js/x", "/static/..%5Cwebfrontend.html"} {
		r, err := http.Get(srv.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode == 200 {
			t.Errorf("GET %s = 200, want a refusal (404/redirect to it)", bad)
		}
	}
}

func TestWeb_GzipPage(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// Asked-for: gzipped on the wire, and the payload round-trips to
	// the embedded bytes exactly.
	resp := getRaw(t, srv.URL+"/", "gzip")
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Accept-Encoding: gzip on GET / → Content-Encoding %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("gzipped response must Vary: Accept-Encoding, got %q", resp.Header.Get("Vary"))
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("Content-Encoding says gzip but body does not parse: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, webPage) {
		t.Errorf("gunzipped page = %d bytes, want the embedded %d verbatim", len(body), len(webPage))
	}

	// Not asked-for: with no Accept-Encoding set, rawClient sends none
	// (DisableCompression suppresses the transport's auto-added "gzip")
	// and never decodes the answer, so the server's identity path is
	// what we see. http.DefaultClient here would ask for gzip behind
	// our back, transparently gunzip, and strip Content-Encoding —
	// this branch would pass without the server ever being asked not
	// to compress. Uncompressed is the tripwire for that regression.
	resp2 := getRaw(t, srv.URL+"/", "")
	defer resp2.Body.Close()
	if resp2.Uncompressed {
		t.Fatal("getRaw response came back transparently decompressed — the rawClient (DisableCompression) wiring regressed")
	}
	if resp2.Header.Get("Content-Encoding") != "" {
		t.Fatalf("no Accept-Encoding on GET / → Content-Encoding %q, want none", resp2.Header.Get("Content-Encoding"))
	}
	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body2, webPage) {
		t.Errorf("identity page = %d bytes, want the embedded %d verbatim", len(body2), len(webPage))
	}
}

func TestWeb_GzipJSONStillDecodes(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// The small-body path: /state answers gzipped JSON that still
	// decodes (tiny payloads pay a few gzip bytes — documented noise).
	resp := getRaw(t, srv.URL+"/state?token=test-token-0123456789abcdef", "gzip")
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Accept-Encoding: gzip on GET /state → Content-Encoding %q, want gzip", resp.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Bound bool `json:"bound"`
	}
	if err := json.NewDecoder(zr).Decode(&st); err != nil {
		t.Fatalf("gunzipped /state body does not decode as JSON: %v", err)
	}
}

func TestWeb_GzipSkipsSSE(t *testing.T) {
	fe, _, _ := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// SSE must ride the wire UNCOMPRESSED even when the client accepts
	// gzip: gzip buffers, /events must deliver each frame as written.
	// Read the initial retry frame to prove the stream is live, then go.
	resp := getRaw(t, srv.URL+"/events?token=test-token-0123456789abcdef&conv=default", "gzip")
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("GET /events Content-Type = %q, want text/event-stream", ct)
	}
	if resp.Header.Get("Content-Encoding") == "gzip" {
		t.Fatal("text/event-stream must never be gzipped — per-event flush dies behind the compressor")
	}
	hdr := make([]byte, len("retry: 3000"))
	if _, err := io.ReadFull(resp.Body, hdr); err != nil {
		t.Fatalf("reading the first SSE frame: %v", err)
	}
	if string(hdr) != "retry: 3000" {
		t.Errorf("first SSE bytes = %q, want the retry frame", hdr)
	}
}

// ---- merge-semantics structure guard (card c_lxe3lz) ----
//
// 2026-11-21, two same-day merge semantic conflicts: af1be012's
// auto-merge silently swallowed the </section> of
// <section id="settings"> — form+tray ended up inside the
// display:none settings pane (0×0, whole document refused focus)
// while `go test` ran 96 packages all green; only headless
// acceptance caught it. 5b57c1ba lost a sync import the same day
// (compile-time — the build gate catches that class). The compiler
// cannot see HTML: webfrontend.html is one embedded data asset no
// toolchain parses, while drive/* pre-merges ride textual
// auto-merge — the more concurrent the cards, the more of these.
// Three assertion groups move "page silently broken" from
// headless-acceptance time to go-test time. Pure test assertions,
// no production surface (mechanisms.md 口径: test-only code is not
// a resident mechanism).
//
// The good state runs against the embedded webPage; the bad states
// in the paired Detect* tests are transforms of that same string —
// no fixture files, so the guard can never rot behind a stale
// sample that the real page drifted away from.

// mergeSwallowHint rides every failure line, so a red points at the
// likely cause first: a textually-clean auto-merge that ate markup.
const mergeSwallowHint = "疑似合并吞标签，见 pitfalls 自动合并条目（docs/pitfalls/git-merge/，判例 af1be012）"

// tagBalanceREs: the page is inline-HTML style — no self-closing
// shapes for these four, so open/close COUNTS must pair. Verified
// scope: all their occurrences live in the markup region; the main
// <script> block builds DOM via createElement, so today a
// whole-page count cannot be perturbed by JS string content. If a
// future edit ever puts tag-shaped strings inside script blocks
// this guard goes red — that is it doing its job: make the
// decision consciously.
var tagBalanceREs = []struct {
	tag  string
	open *regexp.Regexp
}{
	{"section", regexp.MustCompile(`<section[\s>]`)},
	{"form", regexp.MustCompile(`<form[\s>]`)},
	{"script", regexp.MustCompile(`<script[\s>]`)},
	{"div", regexp.MustCompile(`<div[\s>]`)},
}

func tagBalanceProblems(page []byte) []string {
	var problems []string
	for _, t := range tagBalanceREs {
		nOpen := len(t.open.FindAllIndex(page, -1))
		nClose := bytes.Count(page, []byte("</"+t.tag+">"))
		if nOpen != nClose {
			problems = append(problems, fmt.Sprintf("<%s> 开标签 %d 个 vs 闭标签 %d 个不配平——%s", t.tag, nOpen, nClose, mergeSwallowHint))
		}
	}
	return problems
}

// idAttrRE: every id="…" on the page. All occurrences live in
// static markup (the JS block carries none — it createElement's its
// DOM), so a whole-page scan is the honest scope.
var idAttrRE = regexp.MustCompile(`\bid="([^"]+)"`)

func idUniquenessProblems(page []byte) []string {
	count := map[string]int{}
	var order []string
	for _, m := range idAttrRE.FindAllSubmatch(page, -1) {
		id := string(m[1])
		if count[id] == 0 {
			order = append(order, id)
		}
		count[id]++
	}
	var problems []string
	for _, id := range order {
		if count[id] > 1 {
			problems = append(problems, fmt.Sprintf("id=%q 出现 %d 次（全页必须唯一）——%s", id, count[id], mergeSwallowHint))
		}
	}
	return problems
}

// keyComponentIDs: the input surface. af1be012's failure shape was
// exactly these swallowed into a hidden panel — each must appear
// EXACTLY once (zero = swallowed by a merge; two = a merge kept
// both sides).
var keyComponentIDs = []string{"form", "input", "menu", "send"}

// textareaInputRE: <textarea … id="input". The real tag spans a
// newline (rows/placeholder attrs on the next line) — [^>]* crosses
// newlines fine.
var textareaInputRE = regexp.MustCompile(`<textarea\b[^>]*\bid="input"`)

func keyComponentProblems(page []byte) []string {
	var problems []string
	for _, id := range keyComponentIDs {
		if n := bytes.Count(page, []byte(`id="`+id+`"`)); n != 1 {
			problems = append(problems, fmt.Sprintf("关键部件 id=%q 应恰一次，现 %d 次——%s", id, n, mergeSwallowHint))
		}
	}
	if !textareaInputRE.Match(page) {
		problems = append(problems, `textarea#input 不在场（输入面被吞）——`+mergeSwallowHint)
	}
	return problems
}

// TestWeb_PageStructureGuard is the GOOD state: the embedded page
// as shipped satisfies all three groups. The paired Detect* tests
// prove each group goes red on constructed corruption — two states
// per group, or the green here could be vacuous.
func TestWeb_PageStructureGuard(t *testing.T) {
	for _, g := range []struct {
		name  string
		check func([]byte) []string
	}{
		{"tag-balance", tagBalanceProblems},
		{"id-uniqueness", idUniquenessProblems},
		{"key-components", keyComponentProblems},
	} {
		if probs := g.check(webPage); len(probs) > 0 {
			t.Errorf("结构守卫 %s 组对当前页面报红（好样本应绿）：%s", g.name, strings.Join(probs, "; "))
		}
	}
}

// TestWeb_StructureGuardDetectsSwallowedCloseTag: the af1be012
// replica. Drop exactly ONE closing tag and the balance group must
// go red naming the tag — including the incident's own shape (the
// settings section's close, not just any first occurrence).
func TestWeb_StructureGuardDetectsSwallowedCloseTag(t *testing.T) {
	// The incident shape: the </section> belonging to
	// <section id="settings"> (first close AFTER that open — the
	// section nests no sections).
	s := bytes.Index(webPage, []byte(`<section id="settings"`))
	if s < 0 {
		t.Fatal(`页面上找不到 <section id="settings"> —— 取样锚点漂了，换仍存在的锚点`)
	}
	c := bytes.Index(webPage[s:], []byte(`</section>`))
	swallowed := append([]byte{}, webPage[:s+c]...)
	swallowed = append(swallowed, webPage[s+c+len("</section>"):]...)
	probs := tagBalanceProblems(swallowed)
	if len(probs) == 0 {
		t.Error("吞掉 settings 的 </section> 未被检出（af1be012 形状）")
	} else if !strings.Contains(probs[0], "section") || !strings.Contains(probs[0], mergeSwallowHint) {
		t.Errorf("失败信息应点名 section 并带合并提示，得：%s", probs[0])
	}

	// The generic loop: first-occurrence swallow of every guarded tag.
	for _, tag := range []string{"section", "form", "script", "div"} {
		corrupt := bytes.Replace(webPage, []byte("</"+tag+">"), nil, 1)
		if bytes.Equal(corrupt, webPage) {
			t.Fatalf("</%s> 在页面上不存在——取样锚点漂了", tag)
		}
		if probs := tagBalanceProblems(corrupt); len(probs) == 0 {
			t.Errorf("吞掉第一个 </%s> 未被检出", tag)
		}
	}
}

// TestWeb_StructureGuardDetectsDuplicateID: two cards adding an
// element with the same id is the other silent auto-merge shape.
// Rename one existing id onto another's value and the uniqueness
// group must go red naming the duplicated id.
func TestWeb_StructureGuardDetectsDuplicateID(t *testing.T) {
	corrupt := bytes.Replace(webPage, []byte(`id="settings"`), []byte(`id="tray"`), 1)
	if bytes.Equal(corrupt, webPage) {
		t.Fatal(`id="settings" 在页面上不存在——取样锚点漂了`)
	}
	probs := idUniquenessProblems(corrupt)
	if len(probs) == 0 {
		t.Fatal(`把 settings 改名成 tray 造出双 id="tray" 未被检出`)
	}
	found := false
	for _, p := range probs {
		if strings.Contains(p, `id="tray"`) && strings.Contains(p, mergeSwallowHint) {
			found = true
		}
	}
	if !found {
		t.Errorf(`失败信息应点名 id="tray" 并带合并提示，得：%s`, strings.Join(probs, "; "))
	}
}

// TestWeb_StructureGuardDetectsMissingKeyComponent: the input
// surface swallowed, three shapes. Note the first one: a WHOLE
// paired element vanishing keeps open==close — the balance group
// stays green — which is exactly why the exactly-once group exists
// separately.
func TestWeb_StructureGuardDetectsMissingKeyComponent(t *testing.T) {
	t.Run("form block excised", func(t *testing.T) {
		re := regexp.MustCompile(`(?s)<form id="form">.*?</form>`)
		if !re.Match(webPage) {
			t.Fatal(`页面上找不到 <form id="form">…</form> —— 取样锚点漂了`)
		}
		corrupt := re.ReplaceAll(webPage, nil)
		if probs := keyComponentProblems(corrupt); len(probs) == 0 {
			t.Error("整个 form 块被吞未检出——此时配平组仍绿（form 0==0），恰一次断言才是这形状的守卫")
		}
	})
	t.Run("send id dropped", func(t *testing.T) {
		corrupt := bytes.Replace(webPage, []byte(` id="send"`), nil, 1)
		if bytes.Equal(corrupt, webPage) {
			t.Fatal(` id="send" 在页面上不存在——取样锚点漂了`)
		}
		if probs := keyComponentProblems(corrupt); len(probs) == 0 {
			t.Error("send 按钮的 id 被吞未检出")
		}
	})
	t.Run("textarea input id renamed", func(t *testing.T) {
		corrupt := bytes.Replace(webPage, []byte(`id="input"`), []byte(`id="input2"`), 1)
		if bytes.Equal(corrupt, webPage) {
			t.Fatal(`id="input" 在页面上不存在——取样锚点漂了`)
		}
		probs := keyComponentProblems(corrupt)
		if len(probs) == 0 {
			t.Fatal("textarea 的 input id 被改名未检出")
		}
		// The rename must trip BOTH the exactly-once count and the
		// textarea#input presence probe.
		var sawCount, sawTextarea bool
		for _, p := range probs {
			if strings.Contains(p, `id="input"`) {
				sawCount = true
			}
			if strings.Contains(p, "textarea") {
				sawTextarea = true
			}
		}
		if !sawCount || !sawTextarea {
			t.Errorf("改名应同时红「input 恰一次」与「textarea#input 在场」两条，得：%s", strings.Join(probs, "; "))
		}
	})
}
