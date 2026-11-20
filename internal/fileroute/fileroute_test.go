package fileroute

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// fakeUploader records what the router asked the Files API for.
type fakeUploader struct {
	mu   sync.Mutex
	got  []*deepseek.FileUploadRequest
	obj  *deepseek.FileObject // returned on success
	err  error                // returned instead
	cap  int                  // 0 = unlimited; else max uploads allowed
	rate bool                 // honour ctx cancellation
}

func (f *fakeUploader) UploadFile(ctx context.Context, req *deepseek.FileUploadRequest) (*deepseek.FileObject, error) {
	if f.rate {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cap > 0 && len(f.got) >= f.cap {
		return nil, errors.New("fake: over upload cap")
	}
	cp := *req
	f.got = append(f.got, &cp)
	if f.err != nil {
		return nil, f.err
	}
	return f.obj, nil
}

func (f *fakeUploader) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// costSink accumulates RecordFlatCost calls (thread-safe: the TUI
// submit path and the tracker it reports into run on different
// goroutines in production).
type costSink struct {
	mu   sync.Mutex
	sums []float64
}

func (c *costSink) RecordFlatCost(usd float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sums = append(c.sums, usd)
}

func (c *costSink) total() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var s float64
	for _, v := range c.sums {
		s += v
	}
	return s
}

func writePDF(t *testing.T, name string, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	data := make([]byte, size)
	copy(data, "%PDF-1.4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func okObj() *deepseek.FileObject {
	return &deepseek.FileObject{ID: "file-api-test123", PageCount: 34, ExpiresAt: 1765392000}
}

func TestRoute_AttachesFileID(t *testing.T) {
	// The card's headline acceptance: submit "@report.pdf" → marker
	// + file_id in place. writePDF hides in its own TempDir, so the
	// relative "@report.pdf" form needs a chdir'd stable dir.
	dir := t.TempDir()
	data := make([]byte, 64)
	copy(data, "%PDF-1.4")
	if err := os.WriteFile(filepath.Join(dir, "report.pdf"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()

	up := &fakeUploader{obj: okObj()}
	cs := &costSink{}
	r := Router{client: up, assetsDir: t.TempDir(), cost: cs, maxBytes: deepseek.FileMaxBytes}

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "summarize @report.pdf for me")
	if !strings.HasPrefix(got, "summarize @report.pdf for me") {
		t.Fatalf("original text must be preserved: %q", got)
	}
	if !strings.Contains(got, "[file: report.pdf — uploaded · text · 34 pages · file-api-test123]") {
		t.Fatalf("marker = %q", got)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
	if parts[0].FileID != "file-api-test123" {
		t.Fatalf("file_id must be in place at submit: %+v", parts[0])
	}
	if !strings.HasSuffix(parts[0].Asset, ".pdf") {
		t.Fatalf("asset name = %q", parts[0].Asset)
	}
	// The part records the upload's lifecycle + parse mode (F4): the
	// send path uses them to decide "still live" vs re-upload, and to
	// re-upload a @rich doc faithfully — no sidecar cache needed.
	if parts[0].ExpiresAt != 1765392000 || parts[0].Purpose != deepseek.PurposeTextRecognition {
		t.Fatalf("part lifecycle fields = %+v", parts[0])
	}
	// Copy-at-submit really copied.
	if _, err := os.Stat(filepath.Join(r.assetsDir, parts[0].Asset)); err != nil {
		t.Fatalf("asset not stored: %v", err)
	}
	// Upload carried the right filename / purpose / bytes.
	if up.calls() != 1 {
		t.Fatalf("uploads = %d", up.calls())
	}
	req := up.got[0]
	if req.Filename != "report.pdf" || req.Purpose != deepseek.PurposeTextRecognition || len(req.Data) != 64 {
		t.Fatalf("upload req = %+v", req)
	}
	// Parse fee recorded off the RETURNED page count (§6 row 3).
	if want := pricing.ParseCost(34); cs.total() != want {
		t.Fatalf("flat cost = %v, want %v", cs.total(), want)
	}
}

func TestRoute_RichPrefix(t *testing.T) {
	// "@rich:" upgrades the purpose per-file (D-F1); same message may
	// mix both purposes.
	up := &fakeUploader{obj: okObj()}
	r := Router{client: up, assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	textDoc := writePDF(t, "text.pdf", 8)
	richDoc := writePDF(t, "figures.pdf", 8)

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "compare @rich:"+richDoc+" with "+textDoc)
	if len(parts) != 2 {
		t.Fatalf("parts = %+v", parts)
	}
	if !strings.Contains(got, "[file: figures.pdf — uploaded · rich · 34 pages · file-api-test123]") {
		t.Fatalf("rich marker missing: %q", got)
	}
	if up.got[0].Purpose != deepseek.PurposeRichLayoutRecognition || up.got[0].Filename != "figures.pdf" {
		t.Fatalf("rich upload = %+v", up.got[0])
	}
	if up.got[1].Purpose != deepseek.PurposeTextRecognition || up.got[1].Filename != "text.pdf" {
		t.Fatalf("text upload = %+v", up.got[1])
	}
}

func TestRoute_OversizeDegradesNotFatal(t *testing.T) {
	// The card's second acceptance: 100 MiB over-limit degrades to an
	// in-band note, never an error, never an upload. Gate lowered so
	// the test doesn't allocate 100 MiB.
	up := &fakeUploader{obj: okObj()}
	r := Router{client: up, assetsDir: t.TempDir(), maxBytes: 32}
	big := writePDF(t, "huge.pdf", 64)

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "read @rich:"+big)
	if !strings.Contains(got, "[file: huge.pdf — 超过单文档 32 B 上限]") {
		t.Fatalf("oversize note = %q", got)
	}
	if len(parts) != 0 {
		t.Fatalf("oversize must not attach: %+v", parts)
	}
	if up.calls() != 0 {
		t.Fatalf("oversize must not upload, got %d", up.calls())
	}
	// Nothing stored either — the gate fires before the copy.
	if ents, _ := os.ReadDir(r.assetsDir); len(ents) != 0 {
		t.Fatalf("oversize must not store, got %d entries", len(ents))
	}
	// The full-size gate reads "100 MiB" in the note (format check
	// without a real 100 MiB file: formatting is pure).
	if got := oversizeNote("x.pdf", deepseek.FileMaxBytes); got != "[file: x.pdf — 超过单文档 100 MiB 上限]" {
		t.Fatalf("full-size note = %q", got)
	}
}

func TestRoute_UploadFailKeepsAsset(t *testing.T) {
	// Upload failure degrades in-band; the copy-at-submit asset stays
	// for F4's lazy re-upload to heal from.
	up := &fakeUploader{err: errors.New("boom 500")}
	r := Router{client: up, assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 16)

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "@"+doc)
	if !strings.Contains(got, "[file: report.pdf — 上传失败: boom 500]") {
		t.Fatalf("upload-fail note = %q", got)
	}
	if len(parts) != 0 {
		t.Fatalf("failed upload must not attach: %+v", parts)
	}
	if ents, _ := os.ReadDir(r.assetsDir); len(ents) != 1 {
		t.Fatalf("asset must survive a failed upload, got %d entries", len(ents))
	}
}

// TestRoute_IncapableModelNote: on a second-tier lane (client nil) a
// model outside modelcap's Files axis (unknown id, or a lane whose
// endpoint has no document input — GLM) gets the switch-model note,
// before any stat/read/store/upload work.
func TestRoute_IncapableModelNote(t *testing.T) {
	r := Router{assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 8)

	got, parts := r.Route(context.Background(), "glm-5.3", "@"+doc)
	if !strings.Contains(got, "[file: report.pdf — 当前模型不支持文件输入") {
		t.Fatalf("switch-model note = %q", got)
	}
	if len(parts) != 0 {
		t.Fatalf("incapable model must not attach: %+v", parts)
	}
	if ents, _ := os.ReadDir(r.assetsDir); len(ents) != 0 {
		t.Fatalf("incapable model must not store, got %d entries", len(ents))
	}
}

// TestRoute_DeepSeekLaneIgnoresModelTable: file input on the DeepSeek
// lane is account-level (the Files API parses server-side), so an id the
// modelcap table has never heard of — deepseek-v4-pro, or a freeform id
// — must still upload. Gating this lane on a per-id table is exactly the
// regression the vision allow-list taught (docs/pitfalls.md): a capable
// model silently answered "unsupported".
func TestRoute_DeepSeekLaneIgnoresModelTable(t *testing.T) {
	for _, model := range []string{deepseek.ModelV41Flash, "deepseek-v4-pro", "some-freeform-id", ""} {
		up := &fakeUploader{obj: okObj()}
		r := Router{client: up, assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
		doc := writePDF(t, "report.pdf", 8)

		got, parts := r.Route(context.Background(), model, "@"+doc)
		if !strings.Contains(got, "— uploaded · text") {
			t.Fatalf("model %q: marker = %q", model, got)
		}
		if len(parts) != 1 || parts[0].FileID == "" {
			t.Fatalf("model %q: parts = %+v", model, parts)
		}
		if up.calls() != 1 {
			t.Fatalf("model %q: uploads = %d, want 1", model, up.calls())
		}
	}
}

// TestRoute_SecondTierAttaches: no Files API client, but the model is
// file-capable (modelcap) — the PDF is stored and attached Asset-only;
// nothing is uploaded, no parse fee is recorded. The send path owns
// materialisation (deepseek.ResolveFileData).
func TestRoute_SecondTierAttaches(t *testing.T) {
	cs := &costSink{}
	r := Router{assetsDir: t.TempDir(), cost: cs, maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 8)

	got, parts := r.Route(context.Background(), "gpt-4o", "@"+doc)
	if !strings.Contains(got, "[file: report.pdf — attached · 8 B]") {
		t.Fatalf("attach marker = %q", got)
	}
	if len(parts) != 1 || parts[0].Asset == "" || parts[0].FileID != "" {
		t.Fatalf("second-tier part must be Asset-only: %+v", parts)
	}
	if _, err := os.Stat(filepath.Join(r.assetsDir, parts[0].Asset)); err != nil {
		t.Fatalf("asset not stored: %v", err)
	}
	if cs.total() != 0 {
		t.Fatalf("second-tier attach must bill no parse fee, got %v", cs.total())
	}
}

// TestRoute_SecondTierPDFOnly: Word/PPT have no second-tier wire
// format (pkg/llm's transport set is PDF-only) — the router says so at
// submit time and attaches nothing, so the transcript never gains an
// "attached" marker a later send would have to walk back.
func TestRoute_SecondTierPDFOnly(t *testing.T) {
	r := Router{assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	dir := t.TempDir()
	docx := filepath.Join(dir, "spec.docx")
	if err := os.WriteFile(docx, []byte("PK\x03\x04docx"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, parts := r.Route(context.Background(), "claude-sonnet-4-20250514", "@"+docx)
	if !strings.Contains(got, "[file: spec.docx — 二线车道仅支持 PDF 附件") {
		t.Fatalf("format note = %q", got)
	}
	if len(parts) != 0 {
		t.Fatalf("docx must not attach on a second-tier lane: %+v", parts)
	}
	if ents, _ := os.ReadDir(r.assetsDir); len(ents) != 0 {
		t.Fatalf("docx must not be stored on a second-tier lane, got %d entries", len(ents))
	}
}

func TestRoute_StatGateNoFalsePositive(t *testing.T) {
	// A ".pdf" that isn't a real file is not a reference (F2's guard,
	// re-verified at the router level): no marker, no parts.
	up := &fakeUploader{obj: okObj()}
	r := Router{client: up, assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	for _, text := range []string{
		"see https://example.com/report.pdf",
		`if strings.HasSuffix(name, ".pdf") { parse(name) }`,
		"plain text, no refs",
	} {
		got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, text)
		if got != text || len(parts) != 0 || up.calls() != 0 {
			t.Fatalf("false positive on %q: %q %+v", text, got, parts)
		}
	}
}

// TestMarker_Family pins the wire-format contract: every marker this
// package produces starts with "[file: " so a single replay styling
// rule can cover the whole family (F5). Variants may only extend
// AFTER the family prefix (AGENTS.md wire-format rule).
func TestMarker_Family(t *testing.T) {
	markers := []string{
		switchModelNote("x.pdf"),
		formatNote("x.docx"),
		oversizeNote("x.pdf", deepseek.FileMaxBytes),
		attachMarker("x.pdf", deepseek.PurposeTextRecognition, okObj()),
		attachMarker("x.pdf", deepseek.PurposeRichLayoutRecognition, &deepseek.FileObject{ID: "file-api-y"}),
		fmtMarkerReadFail(),
		fmtMarkerStoreFail(),
		fmtMarkerUploadFail(),
		fmtMarkerAttached(),
	}
	for _, m := range markers {
		if !strings.HasPrefix(m, "[file: ") {
			t.Errorf("marker outside family: %q", m)
		}
	}
}

// Thin wrappers so the family test references the note formats
// without replicating their verbs.
func fmtMarkerReadFail() string   { return readFailNote("x.pdf", errors.New("e")) }
func fmtMarkerStoreFail() string  { return storeFailNote("x.pdf", errors.New("e")) }
func fmtMarkerUploadFail() string { return uploadFailNote("x.pdf", errors.New("e")) }
func fmtMarkerAttached() string   { r := Router{}; m, _ := r.routeAttach("x.pdf", ""); return m }

// TestRoute_NeverErrors: no input combination may panic or block the
// submit — every failure lands in-band (mirrors the vision router's
// never-error sweep).
func TestRoute_NeverErrors(t *testing.T) {
	up := &fakeUploader{obj: okObj(), err: errors.New("always failing")}
	r := Router{client: up, assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	for _, text := range []string{"", "no refs", "@", "@rich:", "\x00 weird", "(@rich:).pdf"} {
		got, parts := r.Route(context.Background(), "m", text) // must not panic
		if got == "" && text != "" {
			t.Errorf("route(%q) emptied the text", text)
		}
		_ = parts
	}
}

// TestRoute_CtxCancel: a cancelled context surfaces as an upload
// failure note — the submit is never blocked, matching how a user
// Ctrl-C during a slow upload must land.
func TestRoute_CtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	up := &fakeUploader{obj: okObj(), rate: true}
	r := Router{client: up, assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 8)

	got, parts := r.Route(ctx, deepseek.ModelV41Flash, "@"+doc)
	if !strings.Contains(got, "[file: report.pdf — 上传失败: context canceled") {
		t.Fatalf("cancel note = %q", got)
	}
	if len(parts) != 0 {
		t.Fatalf("cancelled upload must not attach: %+v", parts)
	}
}

func TestRoute_CostSinkOptional(t *testing.T) {
	// nil cost sink must not panic (nil-disabled reporting, same
	// posture as web_search's CostSink).
	up := &fakeUploader{obj: okObj()}
	r := Router{client: up, assetsDir: t.TempDir(), maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 8)
	if _, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "@"+doc); len(parts) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
}

// TestRoute_ZeroPagesNoCost: server omitted the page count → no parse
// fee billed ("按上传返回页数" — never off "an upload was attempted").
func TestRoute_ZeroPagesNoCost(t *testing.T) {
	up := &fakeUploader{obj: &deepseek.FileObject{ID: "file-api-nopg"}}
	cs := &costSink{}
	r := Router{client: up, assetsDir: t.TempDir(), cost: cs, maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 8)

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "@"+doc)
	if len(parts) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
	if cs.total() != 0 {
		t.Fatalf("zero pages must bill nothing, got %v", cs.total())
	}
	if strings.Contains(got, "pages") {
		t.Fatalf("absent page count must not render: %q", got)
	}
}

// TestRoute_RealClientIntegration drives the router against a REAL
// *deepseek.Client on an httptest Files API — the Uploader seam is
// interface-shaped, so this is the one test that would catch a
// router↔client contract drift the fakes can't (multipart purpose
// order, FileObject decode, cost off the returned page_count).
func TestRoute_RealClientIntegration(t *testing.T) {
	var gotPurpose, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			http.Error(w, "bad multipart", 400)
			return
		}
		gotPurpose = r.FormValue("purpose")
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			http.Error(w, "no file", 400)
			return
		}
		defer f.Close()
		gotPath = r.URL.Path
		_ = hdr.Filename
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"file-api-int1","object":"file","bytes":8,` +
			`"created_at":1762800000,"filename":"report.pdf",` +
			`"purpose":"text_recognition","page_count":12}`))
	}))
	defer srv.Close()

	client := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	cs := &costSink{}
	r := Router{client: client, assetsDir: t.TempDir(), cost: cs, maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 8)

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "@"+doc)
	if gotPurpose != deepseek.PurposeTextRecognition || gotPath != "/files" {
		t.Fatalf("server saw purpose=%q path=%q", gotPurpose, gotPath)
	}
	if len(parts) != 1 || parts[0].FileID != "file-api-int1" {
		t.Fatalf("parts = %+v", parts)
	}
	if !strings.Contains(got, "12 pages · file-api-int1") {
		t.Fatalf("marker = %q", got)
	}
	if want := pricing.ParseCost(12); cs.total() != want {
		t.Fatalf("cost = %v, want %v", cs.total(), want)
	}
}
