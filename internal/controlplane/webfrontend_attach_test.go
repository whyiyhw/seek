package controlplane

// W5 attachment intake (card c_56yrcj; feature-web-frontend §13.1) —
// the acceptance surface: the three browser entries all end in one
// POST /attach, so what has to hold is the server half of that wire.
// Every case here is sandbox (httptest + a temp serve dir): no real
// ~/.seek, no network, no browser.
//
// The load-bearing assertions are:
//
//   - auth: the SAME stack as /msg (401 without a credential, 403 for a
//     cookie without the §5.2 write gate) — an attachment ends up
//     driving a seek run, so it is write surface;
//   - the staged path is a working @-reference: imgrefs/filerefs
//     tokenize the prompt on whitespace, so the returned path must
//     survive BOTH detectors verbatim (that is the whole protocol
//     between the page and the child process);
//   - caps and magic checks fail EARLY with an attributable message
//     instead of becoming an in-band note seconds into a run;
//   - the response rides the same CSP/gzip stack as every other route.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/assets"
	"github.com/whyiyhw/seek/internal/filerefs"
	"github.com/whyiyhw/seek/internal/imgrefs"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// attachPNG renders a tiny valid PNG — the same fixture shape
// cmd/seek's visioninput_test uses.
func attachPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// attachPDF is a minimal but magic-correct PDF container: the server
// checks the header only, the Files API is the parse authority.
func attachPDF() []byte { return []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n") }

// attachDocx is a minimal OOXML container (the ZIP local header is the
// only magic the intake checks).
func attachDocx() []byte { return []byte("PK\x03\x04\x14\x00\x00\x00\x00\x00fake-docx-body") }

// attachDocOLE is the OLE compound-file magic (.doc/.ppt).
func attachDocOLE() []byte {
	return append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, []byte("legacy office body")...)
}

// postAttach issues the page's exact request shape: raw body, ?name=.
func postAttach(t *testing.T, srv *httptest.Server, tok, name string, body []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+"/attach?token="+tok+"&name="+name, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /attach %q: %v", name, err)
	}
	return resp
}

// decodeAttach reads a 201 body.
func decodeAttach(t *testing.T, resp *http.Response) attachResponse {
	t.Helper()
	defer resp.Body.Close()
	var out attachResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode /attach response: %v", err)
	}
	return out
}

// TestWeb_AttachAuthIsWriteSurface: /attach takes the /msg gate
// verbatim — an upload that later drives a run must not be reachable
// with a bare cookie (§5.2), and the script face (query token) keeps
// working unchanged.
func TestWeb_AttachAuthIsWriteSurface(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()
	body := attachPNG(t)

	// No credential → 401.
	resp, err := http.Post(srv.URL+"/attach?name=a.png", "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /attach = %d, want 401", resp.StatusCode)
	}

	// Cookie alone → 403 (the CSRF bar; the card's "CSRF 闸只卡 cookie"
	// pitfall row stays the contract).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/attach?name=a.png", bytes.NewReader(body))
	req.Header.Set("Cookie", webTokenCookie+"="+tok)
	r1, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	if r1.StatusCode != http.StatusForbidden {
		t.Errorf("cookie-only /attach = %d, want 403", r1.StatusCode)
	}

	// Cookie + same-origin Origin → accepted (the browser's real shape).
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/attach?name=b.png", bytes.NewReader(body))
	req.Header.Set("Cookie", webTokenCookie+"="+tok)
	req.Header.Set("Origin", srv.URL)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r2.StatusCode != http.StatusCreated {
		t.Errorf("cookie+same-origin /attach = %d, want 201", r2.StatusCode)
	}
	r2.Body.Close()

	// A tampered Host still dies at gateHost BEFORE auth (rebinding
	// defense wraps every route, §5.3).
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/attach?token="+tok+"&name=c.png", bytes.NewReader(body))
	req.Host = "attacker.example"
	r3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r3.Body.Close()
	if r3.StatusCode != http.StatusForbidden {
		t.Errorf("rebinding-host /attach = %d, want 403", r3.StatusCode)
	}
}

// TestWeb_AttachStagesUsableReference is the card's core contract: the
// 201 path is a token the child's EXISTING routers claim. Both the
// image detector (vision lane) and the document detector (Files API
// lane) must find it — that is what makes the web channel inherit
// modelcap's second-tier capability decision and the file pipeline
// with no new code.
func TestWeb_AttachStagesUsableReference(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// A hostile display name: spaces, CJK, a path separator, mixed case
	// extension. The stored name must be token-safe, the extension kept.
	resp := postAttach(t, srv, tok, "my%20shot%20%E6%88%AA%E5%9B%BE%201.PNG", attachPNG(t))
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("image upload = %d (%s), want 201", resp.StatusCode, b)
	}
	got := decodeAttach(t, resp)
	if !got.OK || got.Kind != "image" || got.Bytes == 0 {
		t.Fatalf("image response = %+v, want ok image with bytes", got)
	}
	if strings.ContainsAny(got.Path, " \t\r\n") {
		t.Fatalf("staged path %q carries whitespace — the @-reference cannot survive tokenisation", got.Path)
	}
	if !strings.HasSuffix(got.Path, ".png") {
		t.Errorf("staged path %q lost the (lowercased) extension", got.Path)
	}
	if info, err := os.Stat(got.Path); err != nil || info.Size() == 0 {
		t.Fatalf("staged file missing/empty: %v", err)
	}
	// The reference protocol, asserted through the REAL detectors.
	if refs := imgrefs.Detect("look @" + got.Path); len(refs) != 1 || refs[0] != got.Path {
		t.Errorf("imgrefs.Detect on the staged path = %v, want [%s] (vision lane would miss the image)", refs, got.Path)
	}

	// Documents route the same way through filerefs.
	for _, tc := range []struct {
		name string
		body []byte
		want string
	}{
		{"report.pdf", attachPDF(), ".pdf"},
		{"deck.pptx", attachDocx(), ".pptx"},
		{"legacy.doc", attachDocOLE(), ".doc"},
	} {
		r := postAttach(t, srv, tok, tc.name, tc.body)
		if r.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			t.Fatalf("%s upload = %d (%s), want 201", tc.name, r.StatusCode, b)
		}
		d := decodeAttach(t, r)
		if d.Kind != "file" {
			t.Errorf("%s kind = %q, want file", tc.name, d.Kind)
		}
		if !strings.HasSuffix(d.Path, tc.want) {
			t.Errorf("%s staged as %q, want the %s extension preserved", tc.name, d.Path, tc.want)
		}
		if refs := filerefs.DetectRefs("read @" + d.Path); len(refs) != 1 || refs[0].Path != d.Path {
			t.Errorf("filerefs.DetectRefs on %q = %v — the document lane would miss it", d.Path, refs)
		}
	}
}

// TestWeb_AttachSizeCaps: caps mirror the DOWNSTREAM lanes (images at
// assets.MaxAssetBytes, documents at deepseek.FileMaxBytes) and the
// refusal is a 413 naming the cap — the card's "超限提示正确" row.
// The image cap is a var so the gate is exercised without a 32 MiB
// fixture (the same lever assets' own test uses).
func TestWeb_AttachSizeCaps(t *testing.T) {
	old := assets.MaxAssetBytes
	assets.MaxAssetBytes = 8
	t.Cleanup(func() { assets.MaxAssetBytes = old })

	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	// Under the cap still passes (the boundary is not off by one).
	small := attachPNG(t)
	if len(small) <= 8 {
		t.Fatalf("fixture too small (%d bytes) — the cap is not being exercised", len(small))
	}
	if r := postAttach(t, srv, tok, "ok.png", small); r.StatusCode != http.StatusRequestEntityTooLarge {
		r.Body.Close()
		t.Fatalf("fixture %d bytes vs cap 8 = %d, want 413 (fixture must exceed the cap)", len(small), r.StatusCode)
	}

	assets.MaxAssetBytes = 64 << 20 // wide enough for the fixture
	r := postAttach(t, srv, tok, "small.png", small)
	if r.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		t.Fatalf("under-cap image = %d (%s), want 201", r.StatusCode, b)
	}
	r.Body.Close()

	assets.MaxAssetBytes = 4 // now the same fixture is over
	r = postAttach(t, srv, tok, "over.png", small)
	defer r.Body.Close()
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-cap image = %d, want 413", r.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatalf("decode 413 body: %v", err)
	}
	if !strings.Contains(body["error"], "上限") {
		t.Errorf("413 body = %q, want a cap-naming hint", body["error"])
	}

	// The document lane's cap is the Files API's own constant; the hint
	// text is asserted at the boundary path only (a 100 MiB fixture is
	// not worth the test time).
	if deepseek.FileMaxBytes != 100<<20 {
		t.Errorf("FileMaxBytes = %d, want the 100 MiB document cap the intake mirrors", deepseek.FileMaxBytes)
	}
}

// TestWeb_AttachRejectsBadFiles: every "this file will not work"
// shape fails HERE, with a message attributable to the file, instead of
// as an in-band note deep inside a run.
func TestWeb_AttachRejectsBadFiles(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	for _, tc := range []struct {
		name  string
		body  []byte
		want  int
		hint  string
		label string
	}{
		{"empty.png", nil, http.StatusBadRequest, "文件为空", "empty body"},
		{"evil.png", []byte("#!/bin/sh\necho not a png\n"), http.StatusUnsupportedMediaType, "扩展名", "text masquerading as png"},
		{"evil.pdf", []byte("just text, no PDF header"), http.StatusUnsupportedMediaType, "扩展名", "text masquerading as pdf"},
		{"notes.txt", []byte("hello"), http.StatusUnsupportedMediaType, "不支持的文件类型", "unsupported extension"},
		{"script.sh", []byte("#!/bin/sh\n"), http.StatusUnsupportedMediaType, "不支持的文件类型", "executable extension"},
	} {
		r := postAttach(t, srv, tok, tc.name, tc.body)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		r.Body.Close()
		if r.StatusCode != tc.want {
			t.Errorf("%s (%s) = %d, want %d (%s)", tc.name, tc.label, r.StatusCode, tc.want, body["error"])
			continue
		}
		if !strings.Contains(body["error"], tc.hint) {
			t.Errorf("%s body = %q, want it to contain %q", tc.name, body["error"], tc.hint)
		}
	}

	// Nothing was staged for the refused shapes: only successful uploads
	// leave files behind.
	root := filepath.Join(fe.attachDir, webConvID().Native())
	if ents, err := os.ReadDir(root); err == nil && len(ents) != 0 {
		t.Errorf("refused uploads left %d file(s) in the staging dir", len(ents))
	}
}

// TestWeb_AttachMethodAndTransport: the route rides the ordinary
// pipeline — non-POST is 405, and the response carries CSP + the gzip
// transport (the pitfall row "gzip must keep the wrapper's optional
// interfaces" is about SSE; /attach is a plain JSON response and simply
// must not break the middleware).
func TestWeb_AttachMethodAndTransport(t *testing.T) {
	fe, _, tok := newTestWeb(t)
	srv := httptest.NewServer(fe.handler())
	defer srv.Close()

	r, err := http.Get(srv.URL + "/attach?token=" + tok + "&name=a.png")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /attach = %d, want 405", r.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/attach?token="+tok+"&name=g.png", bytes.NewReader(attachPNG(t)))
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Error("/attach carries no CSP — withCSP must wrap every route")
	}
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("/attach Content-Encoding = %q, want gzip (the JSON transport is shared)",
			resp.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("gzip body does not parse: %v", err)
	}
	var out attachResponse
	if err := json.NewDecoder(zr).Decode(&out); err != nil {
		t.Fatalf("gunzipped /attach body: %v", err)
	}
	if !out.OK || out.Kind != "image" {
		t.Errorf("gunzipped /attach body = %+v", out)
	}
}

// TestWeb_AttachPrunesStagingDir: a chatty paste session must not grow
// the staging directory without bound; the sweep is by mtime and keeps
// the newest attachKeepPerConv files.
func TestWeb_AttachPrunesStagingDir(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%02d.png", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	pruneAttachments(dir, 2)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 {
		t.Fatalf("prune kept %d files, want 2", len(ents))
	}
	// The newest survive.
	for _, want := range []string{"04.png", "05.png"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("prune dropped %s — the sweep must keep the newest", want)
		}
	}
}

// TestWeb_AttachNameSanitising pins the token-safety transform in
// isolation (the end-to-end assertion above runs it through the real
// detectors; this table nails the edges). The returned base KEEPS the
// extension — the child's routers key their tables off it — so the
// assertions are about the stem plus that preserved suffix.
func TestWeb_AttachNameSanitising(t *testing.T) {
	for _, tc := range []struct {
		raw, stem, ext string
	}{
		{"shot.png", "shot", ".png"},
		{"my shot 截图.PNG", "my_shot", ".png"},
		{"../../etc/passwd.png", "passwd", ".png"},
		{`C:\Users\me\Desktop\a b.jpg`, "a_b", ".jpg"},
		{"....png", "attachment", ".png"},
		{"", "attachment", ""},
		{"no-extension", "no-extension", ""},
		{"a(b)[c]{d}.webp", "a_b_c_d", ".webp"},
	} {
		base, ext := sanitizeAttachName(tc.raw)
		if ext != tc.ext {
			t.Errorf("sanitizeAttachName(%q) ext = %q, want %q", tc.raw, ext, tc.ext)
		}
		if base != tc.stem+tc.ext {
			t.Errorf("sanitizeAttachName(%q) base = %q, want %q", tc.raw, base, tc.stem+tc.ext)
		}
		// Token safety: no whitespace, no separators, no "..", and never
		// a leading dot (a dotfile the sweep could confuse).
		if strings.ContainsAny(base, " \t\r\n/\\:") || strings.Contains(base, "..") ||
			strings.HasPrefix(base, ".") {
			t.Errorf("sanitizeAttachName(%q) base = %q — not a safe @-reference token", tc.raw, base)
		}
		if base == "" {
			t.Errorf("sanitizeAttachName(%q) returned an empty base", tc.raw)
		}
	}
}

// TestWeb_PageAttachmentUI pins the browser half against the embedded
// page the way the other W-series page contracts are pinned: the three
// entries, the shared upload path, the tray, and the @-token handoff.
// A rename on either side strands the feature silently (a dead 📎).
func TestWeb_PageAttachmentUI(t *testing.T) {
	page := string(webPage)
	for _, need := range []string{
		`id="attach-btn"`,          // entry 1: click-select
		`id="file"`,                // …its hidden input
		`id="tray"`,                // progress/holding area
		"addEventListener('paste'", // entry 3: paste
		"dragHasFiles",             // entry 2: drag-and-drop
		"ev.dataTransfer.files",    // drop payload
		"cd.files",                 // clipboard payload
		"q('/attach?name='",        // the ONE upload endpoint
		"xhr.upload.onprogress",    // real progress, not a fake spinner
		"renderTray",               // tray is the progress surface
		"thumbsFor",                // bubble thumbnails
		"@' + a.path",              // the @-token handoff to /msg
		"IMG_MAX", "DOC_MAX",       // local pre-checks mirror the caps
		"im.src = a.dataUrl", // previews ride the FileReader URL
	} {
		if !strings.Contains(page, need) {
			t.Errorf("embedded page lost the attachment piece %q (W5 c_56yrcj)", need)
		}
	}
	// Additivity: the pre-W5 chat plane survives untouched.
	for _, keep := range []string{"id=\"form\"", "id=\"mic\"", "addEventListener('submit'", "addMsg('seek'"} {
		if !strings.Contains(page, keep) {
			t.Errorf("embedded page lost the prior-plane piece %q — W5 attachments must be additive", keep)
		}
	}
	// The preview must not need an HTML parsing sink (the §5.5 rule the
	// page-wide test already enforces; re-pinned here because this card
	// adds DOM building).
	for _, sink := range []string{"innerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(page, sink) {
			t.Errorf("attachment UI introduced %q — previews/tray must build DOM via createElement", sink)
		}
	}
}

// TestWeb_CSPServesImagePreviews: the ONE policy widening this card
// makes. Without img-src data: every thumbnail is silently blocked by
// default-src 'self'.
func TestWeb_CSPServesImagePreviews(t *testing.T) {
	csp := webCSP()
	if !strings.Contains(csp, "img-src 'self' data:") {
		t.Errorf("CSP %q lacks img-src 'self' data: — data:-URL previews would be blocked", csp)
	}
	// The widening stays scoped: no blanket data:/blob: on script, style
	// or default.
	if strings.Contains(csp, "default-src 'self' data:") {
		t.Error("default-src must stay 'self' — only img-src may admit data:")
	}
	if strings.Contains(csp, "blob:") {
		t.Error("blob: must not be admitted — data:-URL previews are the narrower shape")
	}
}
