package fileroute

import (
	"context"
	"encoding/json"
	"errors"
	"math"
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
		attachMarker("x.pdf", deepseek.PurposeTextRecognition, okObj(), 34, false),
		attachMarker("x.pdf", deepseek.PurposeRichLayoutRecognition, &deepseek.FileObject{ID: "file-api-y"}, 2, true),
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

// TestRoute_MissingPageCountFallsBackToEstimate: the live API omits
// page_count (docs/pitfalls.md) — the router must NOT bill a flat $0
// for a parse that really happened; it records the flagged size
// estimate instead (card c_y2akdi, 口径 in pricing.EstimateParsePages).
// The upload-failure path still bills nothing: only a returned id
// means a parse ran.
func TestRoute_MissingPageCountFallsBackToEstimate(t *testing.T) {
	up := &fakeUploader{obj: &deepseek.FileObject{ID: "file-api-nopg"}}
	cs := &costSink{}
	r := Router{client: up, assetsDir: t.TempDir(), cost: cs, maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 8)

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "@"+doc)
	if len(parts) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
	wantPages := pricing.EstimateParsePages(8) // 8 B fixture → 1 page
	if wantPages != 1 {
		t.Fatalf("fixture sanity: EstimateParsePages(8) = %d, want 1", wantPages)
	}
	if want := pricing.ParseCost(wantPages); cs.total() != want {
		t.Fatalf("estimated cost = %v, want %v (no more flat $0)", cs.total(), want)
	}
	if cs.total() <= 0 {
		t.Fatalf("missing page_count must still bill: %v", cs.total())
	}
	// The observation record flags the estimate on BOTH surfaces: the
	// marker (human/transcript) and the FilePart (machine/JSONL).
	if !strings.Contains(got, "· ~1 pages·估 · file-api-nopg") {
		t.Fatalf("marker = %q", got)
	}
	if parts[0].Pages != 1 || !parts[0].PagesEstimated {
		t.Fatalf("part observation fields = %+v", parts[0])
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
	// Exact state (acceptance ①'s other half): a returned page_count
	// bills precisely and the observation record reads it as exact.
	if parts[0].Pages != 12 || parts[0].PagesEstimated {
		t.Fatalf("exact-state observation fields = %+v", parts[0])
	}
	if !strings.Contains(got, "12 pages · file-api-int1") {
		t.Fatalf("marker = %q", got)
	}
	if want := pricing.ParseCost(12); cs.total() != want {
		t.Fatalf("cost = %v, want %v", cs.total(), want)
	}
}

// TestRoute_RealClient_PageCountFallback is acceptance ①'s missing-
// count state on a REAL *deepseek.Client behind httptest: the live
// API shape (id/bytes/filename/purpose, no page_count) must bill the
// flagged size estimate, never the old flat $0. The exact state is
// pinned by TestRoute_RealClientIntegration (page_count=12 →
// ParseCost(12)); together the two tests are the two-state assertion.
func TestRoute_RealClient_PageCountFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" || r.Method != http.MethodPost {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Live-API response shape (2026-11-18 finding): no page_count,
		// no expires_at — bytes/created_at/filename/purpose only.
		_, _ = w.Write([]byte(`{"id":"file-api-live1","object":"file","bytes":4096,` +
			`"created_at":1764000000,"filename":"report.pdf",` +
			`"purpose":"text_recognition"}`))
	}))
	defer srv.Close()

	client := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	cs := &costSink{}
	r := Router{client: client, assetsDir: t.TempDir(), cost: cs, maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "report.pdf", 4096)

	got, parts := r.Route(context.Background(), deepseek.ModelV41Flash, "@"+doc)
	if len(parts) != 1 || parts[0].FileID != "file-api-live1" {
		t.Fatalf("parts = %+v", parts)
	}
	// Estimated state: cost off the size estimate, flagged everywhere.
	if want := pricing.ParseCost(pricing.EstimateParsePages(4096)); cs.total() != want || cs.total() <= 0 {
		t.Fatalf("cost = %v, want estimated %v (not $0)", cs.total(), want)
	}
	if parts[0].Pages != pricing.EstimateParsePages(4096) || !parts[0].PagesEstimated {
		t.Fatalf("part observation fields = %+v", parts[0])
	}
	if !strings.Contains(got, "[file: report.pdf — uploaded · text · ~1 pages·估 · file-api-live1]") {
		t.Fatalf("marker = %q", got)
	}
}

// seqUploader hands out one FileObject per call in order — the shape
// the observation fixture needs: same document submitted twice, two
// distinct file ids (what the live API mints per upload).
type seqUploader struct {
	mu   sync.Mutex
	objs []*deepseek.FileObject
	i    int
}

func (s *seqUploader) UploadFile(ctx context.Context, req *deepseek.FileUploadRequest) (*deepseek.FileObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.objs) {
		return nil, errors.New("fake: no more objects")
	}
	o := s.objs[s.i]
	s.i++
	return o, nil
}

// TestRoute_ObservationRecordReconcilable is acceptance ②: the
// transcript's parse-observation records must (a) carry the whole
// client-side assertion material — file id + claimed pages + the
// estimate flag — through the session JSONL round-trip, and (b) make
// the post-hoc reconciliation computable from the records alone:
// recorded dollars re-derive from ParseCost(pages), and the
// same-document-twice uploads of the pitfall pair up by their shared
// content-addressed asset. (The API side offers nothing to reconcile
// against: GET /files/{id} is metadata-only and parse results are not
// downloadable — checked 2026-11-19 — so the runtime usage rows /
// smoke test are the other half, out of this file.)
func TestRoute_ObservationRecordReconcilable(t *testing.T) {
	// Three submits: the same PDF twice with the live-API response
	// shape (no page_count → estimated), then a different document
	// whose server DOES return a count (exact reading; a different
	// file so its content-addressed asset is its own).
	up := &seqUploader{objs: []*deepseek.FileObject{
		{ID: "file-api-a", Bytes: 8192, Purpose: deepseek.PurposeTextRecognition},
		{ID: "file-api-b", Bytes: 8192, Purpose: deepseek.PurposeTextRecognition},
		okObj(), // exact: PageCount 34
	}}
	cs := &costSink{}
	r := Router{client: up, assetsDir: t.TempDir(), cost: cs, maxBytes: deepseek.FileMaxBytes}
	doc := writePDF(t, "twice.pdf", 8192)
	other := writePDF(t, "exact.pdf", 64)

	var session []deepseek.Message // what session JSONL lines 2..N hold
	for _, d := range []string{doc, doc, other} {
		got, parts := r.Route(context.Background(), "m", "@"+d)
		if len(parts) != 1 {
			t.Fatalf("parts = %+v", parts)
		}
		if !strings.Contains(got, parts[0].FileID) {
			t.Fatalf("marker %q does not carry the id", got)
		}
		session = append(session, deepseek.Message{Role: deepseek.RoleUser, Content: got, Files: parts})
	}

	// (a) Record shape survives the JSONL round-trip with the estimate
	// flag on exactly the estimated parts.
	var reloaded []deepseek.Message
	for _, m := range session {
		line, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var back deepseek.Message
		if err := json.Unmarshal(line, &back); err != nil {
			t.Fatal(err)
		}
		reloaded = append(reloaded, back)
		raw := string(line)
		if i := len(reloaded) - 1; i < 2 { // the two live-API-shape uploads
			p := reloaded[i].Files[0]
			if p.FileID == "" || p.Pages != pricing.EstimateParsePages(8192) || !p.PagesEstimated {
				t.Fatalf("estimated record %d lost its shape: %+v", i, p)
			}
			if !strings.Contains(raw, `"pages_est":true`) {
				t.Fatalf("estimated record %d must persist the flag: %s", i, raw)
			}
		} else { // exact reading
			p := reloaded[i].Files[0]
			if p.Pages != 34 || p.PagesEstimated {
				t.Fatalf("exact record lost its shape: %+v", p)
			}
			if strings.Contains(raw, "pages_est") {
				t.Fatalf("exact record must not carry an estimate flag: %s", raw)
			}
		}
	}

	// (b1) Dollars re-derive from the records alone: recorded flat
	// costs == Σ ParseCost(recorded pages).
	var fromRecords float64
	for _, m := range reloaded {
		for _, p := range m.Files {
			fromRecords += pricing.ParseCost(p.Pages)
		}
	}
	if math.Abs(fromRecords-cs.total()) > 1e-15 {
		t.Fatalf("reconciled cost %v != recorded %v", fromRecords, cs.total())
	}

	// (b2) The pitfall's scenario — same document parsed twice — pairs
	// up by the content-addressed asset: two ids, one asset, one
	// claimed-pages pair to diff against the runtime usage rows.
	byAsset := map[string][]deepseek.FilePart{}
	for _, m := range reloaded {
		for _, p := range m.Files {
			byAsset[p.Asset] = append(byAsset[p.Asset], p)
		}
	}
	paired := 0
	for asset, recs := range byAsset {
		if len(recs) == 1 {
			continue // the exact-reading document, its own asset
		}
		if len(recs) != 2 || recs[0].FileID == recs[1].FileID {
			t.Fatalf("asset %q: want two distinct upload ids, got %+v", asset, recs)
		}
		paired++
	}
	if paired != 1 {
		t.Fatalf("want exactly one twice-parsed asset pairing, got %d", paired)
	}
}
