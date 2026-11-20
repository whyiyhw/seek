package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUploader counts uploads and returns scripted objects — the
// no-HTTP seam for resolver logic tests.
type fakeUploader struct {
	mu    sync.Mutex
	calls []*FileUploadRequest
	obj   *FileObject
	err   error
	// block, when non-nil, receives each call before it returns — lets a
	// test hold uploads in flight for the concurrency case.
	block func()
}

func (f *fakeUploader) UploadFile(ctx context.Context, req *FileUploadRequest) (*FileObject, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	obj, err, block := f.obj, f.err, f.block
	f.mu.Unlock()
	if block != nil {
		block()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return &FileObject{ID: "file-api-new", Purpose: req.Purpose}, nil
	}
	cp := *obj
	return &cp, nil
}

func (f *fakeUploader) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeUploader) last() *FileUploadRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// pins the clock at t for expiry tests.
func at(tm time.Time) func() time.Time { return func() time.Time { return tm } }

// TestResolveFiles_LiveIDPassesThrough: an id that hasn't expired goes
// out untouched — no upload, no note, and the input is not mutated.
func TestResolveFiles_LiveIDPassesThrough(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{}
	r := NewFileResolver(func(string) ([]byte, error) { t.Error("load must not run for a live id"); return nil, nil }, up)
	r.Now = at(time.Unix(1000, 0))

	in := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "see", Files: []FilePart{
			{Asset: "a.pdf", FileID: "file-api-live", ExpiresAt: 2000, Purpose: PurposeRichLayoutRecognition},
		}},
	}
	out := ResolveFiles(context.Background(), in, r)
	if up.count() != 0 {
		t.Fatalf("uploads = %d, want 0", up.count())
	}
	got := out[1].Files[0]
	if got.FileID != "file-api-live" || got.ExpiresAt != 2000 || got.Purpose != PurposeRichLayoutRecognition {
		t.Fatalf("live part drifted: %+v", got)
	}
	if strings.Contains(out[1].Content, "[file: ") {
		t.Fatalf("live part must not add a note: %q", out[1].Content)
	}
	// Copy semantics: the input message keeps its original part.
	if in[1].Files[0].FileID != "file-api-live" || in[1].Content != "see" {
		t.Fatalf("input mutated: %+v", in[1])
	}
}

// TestResolveFiles_LazyUpload: an Asset-only part (submit-time upload
// failed, or a hand-built transcript) uploads from the asset library at
// send time, with the asset name as filename (extension drives the
// server's format sniffing) and the recorded purpose.
func TestResolveFiles_LazyUpload(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{obj: &FileObject{ID: "file-api-lazy", PageCount: 3, ExpiresAt: 5000}}
	var loaded string
	r := NewFileResolver(func(asset string) ([]byte, error) {
		loaded = asset
		return []byte("%PDF-1.4 body"), nil
	}, up)

	in := []Message{{Role: RoleUser, Content: "read", Files: []FilePart{{
		Asset: "ab12cd34ef56.pdf", Purpose: PurposeRichLayoutRecognition,
	}}}}
	out := ResolveFiles(context.Background(), in, r)

	if up.count() != 1 {
		t.Fatalf("uploads = %d, want 1", up.count())
	}
	req := up.last()
	if req.Filename != "ab12cd34ef56.pdf" || string(req.Data) != "%PDF-1.4 body" || req.Purpose != PurposeRichLayoutRecognition {
		t.Fatalf("upload request = %+v", req)
	}
	if loaded != "ab12cd34ef56.pdf" {
		t.Fatalf("loader got %q", loaded)
	}
	got := out[0].Files[0]
	if got.Asset != "ab12cd34ef56.pdf" || got.FileID != "file-api-lazy" || got.ExpiresAt != 5000 {
		t.Fatalf("resolved part = %+v", got)
	}
	if in[0].Files[0].FileID != "" {
		t.Fatalf("input mutated: %+v", in[0].Files[0])
	}
	// The resolved copy must marshal to a valid wire request.
	buf, err := json.Marshal(&ChatRequest{Model: ModelV41Flash, Messages: out})
	if err != nil {
		t.Fatalf("wire marshal: %v", err)
	}
	if !strings.Contains(string(buf), `{"type":"file","file_id":"file-api-lazy"}`) {
		t.Fatalf("wire body missing file part: %s", buf)
	}
	for _, banned := range []string{"expires_at", "purpose", `"asset"`} {
		if strings.Contains(string(buf), banned) {
			t.Errorf("persistence field %q leaked to wire: %s", banned, buf)
		}
	}
}

// TestResolveFiles_ExpiredReupload: an id past its recorded expiry is
// re-uploaded from the asset, and the resolver's transport cache keeps
// the NEXT resolve from paying a second parse — the repeat-billing
// guard that makes the copy-not-mutate contract affordable.
func TestResolveFiles_ExpiredReupload(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{obj: &FileObject{ID: "file-api-fresh", ExpiresAt: 9000}}
	r := NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF"), nil }, up)
	r.Now = at(time.Unix(2000, 0))

	in := []Message{{Role: RoleUser, Content: "see", Files: []FilePart{{
		Asset: "a.pdf", FileID: "file-api-stale", ExpiresAt: 1500,
	}}}}

	out := ResolveFiles(context.Background(), in, r)
	if up.count() != 1 {
		t.Fatalf("uploads = %d, want 1", up.count())
	}
	got := out[0].Files[0]
	if got.FileID != "file-api-fresh" || got.ExpiresAt != 9000 || got.Purpose != PurposeTextRecognition {
		t.Fatalf("resolved part = %+v", got)
	}

	// Second turn, same history: the cache serves the fresh id.
	out2 := ResolveFiles(context.Background(), in, r)
	if up.count() != 1 {
		t.Fatalf("uploads after second resolve = %d, want still 1 (transport cache)", up.count())
	}
	if out2[0].Files[0].FileID != "file-api-fresh" {
		t.Fatalf("second resolve = %+v, want the cached fresh id", out2[0].Files[0])
	}

	// Once the cached id expires too, one more upload is due.
	r.Now = at(time.Unix(10000, 0))
	ResolveFiles(context.Background(), in, r)
	if up.count() != 2 {
		t.Fatalf("uploads after cache expiry = %d, want 2", up.count())
	}
}

// TestResolveFiles_Permanent: ExpiresAt 0 means no expiry advertised —
// never re-uploaded, however old the session.
func TestResolveFiles_Permanent(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{}
	r := NewFileResolver(func(string) ([]byte, error) { t.Error("load must not run"); return nil, nil }, up)
	r.Now = at(time.Unix(1<<31, 0))

	in := []Message{{Role: RoleUser, Content: "x", Files: []FilePart{{Asset: "a.pdf", FileID: "file-api-p"}}}}
	out := ResolveFiles(context.Background(), in, r)
	if up.count() != 0 || out[0].Files[0].FileID != "file-api-p" {
		t.Fatalf("permanent id must pass through: uploads=%d part=%+v", up.count(), out[0].Files[0])
	}
}

// TestResolveFiles_LoadFailureDegrades: a missing asset never aborts
// the turn — the part is dropped and the note rides "[file: " (the
// shared marker family, pinned by HasPrefix per the wire-format rule).
func TestResolveFiles_LoadFailureDegrades(t *testing.T) {
	t.Parallel()
	r := NewFileResolver(func(string) ([]byte, error) { return nil, errors.New("asset gone") }, &fakeUploader{})

	in := []Message{{Role: RoleUser, Content: "read", Files: []FilePart{{Asset: "dead.pdf"}}}}
	out := ResolveFiles(context.Background(), in, r)

	if len(out[0].Files) != 0 {
		t.Fatalf("failed part must be dropped: %+v", out[0].Files)
	}
	if !strings.HasPrefix(strings.TrimSpace(strings.SplitN(out[0].Content, "\n\n", 2)[1]), "[file: dead.pdf") {
		t.Fatalf("note lost the [file: family prefix: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "asset gone") {
		t.Fatalf("note must carry the reason: %q", out[0].Content)
	}
	// A dropped part means the message marshals as plain text — the
	// turn continues instead of failing at the wire guard.
	if _, err := json.Marshal(&ChatRequest{Model: ModelV41Flash, Messages: out}); err != nil {
		t.Fatalf("degraded message must marshal: %v", err)
	}
}

// TestResolveFiles_UploadFailureDegrades: same never-error posture for
// a failed (re-)upload.
func TestResolveFiles_UploadFailureDegrades(t *testing.T) {
	t.Parallel()
	r := NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF"), nil },
		&fakeUploader{err: errors.New("500 after 2 attempts")})
	r.Now = at(time.Unix(2000, 0))

	in := []Message{
		{Role: RoleUser, Content: "one", Files: []FilePart{{Asset: "a.pdf", FileID: "file-api-stale", ExpiresAt: 1500}}},
		{Role: RoleAssistant, Content: "ok"},
	}
	out := ResolveFiles(context.Background(), in, r)
	if len(out[0].Files) != 0 || !strings.Contains(out[0].Content, "500 after 2 attempts") {
		t.Fatalf("upload failure must degrade in-band: %+v", out[0])
	}
	if out[1].Content != "ok" {
		t.Fatalf("unrelated message touched: %+v", out[1])
	}
}

// TestResolveFiles_ContextCanceled: cancellation surfaces as an in-band
// note (never a panic or a hung turn), and the uploader sees the
// cancelled ctx. AGENTS.md "Testing": ctx-aware functions get a
// ctx.Done case.
func TestResolveFiles_ContextCanceled(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{}
	r := NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF"), nil }, up)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := []Message{{Role: RoleUser, Content: "x", Files: []FilePart{{Asset: "a.pdf"}}}}
	out := ResolveFiles(ctx, in, r)
	if len(out[0].Files) != 0 || !strings.Contains(out[0].Content, "context canceled") {
		t.Fatalf("cancel must degrade in-band: %+v", out[0])
	}
}

// TestResolveFiles_NoUploadPath: a lane without the Files API (nil
// uploader / loader) degrades asset-only parts to notes while still
// passing live ids through.
func TestResolveFiles_NoUploadPath(t *testing.T) {
	t.Parallel()
	r := NewFileResolver(nil, nil)
	in := []Message{{Role: RoleUser, Content: "x", Files: []FilePart{
		{Asset: "a.pdf"},
		{Asset: "b.pdf", FileID: "file-api-live"},
	}}}
	out := ResolveFiles(context.Background(), in, r)
	if len(out[0].Files) != 1 || out[0].Files[0].FileID != "file-api-live" {
		t.Fatalf("live id must survive: %+v", out[0].Files)
	}
	if !strings.Contains(out[0].Content, "[file: a.pdf") {
		t.Fatalf("unhealable part needs a note: %q", out[0].Content)
	}
}

// TestResolveFiles_NilResolver: callers may always call it; a nil
// resolver passes live ids through and notes everything else.
func TestResolveFiles_NilResolver(t *testing.T) {
	t.Parallel()
	in := []Message{{Role: RoleUser, Content: "x", Files: []FilePart{
		{Asset: "a.pdf"},
		{Asset: "b.pdf", FileID: "file-api-live", ExpiresAt: 1 << 40},
	}}}
	out := ResolveFiles(context.Background(), in, nil)
	if len(out[0].Files) != 1 || out[0].Files[0].FileID != "file-api-live" {
		t.Fatalf("live id must survive: %+v", out[0].Files)
	}
	if !strings.Contains(out[0].Content, "no file resolver wired") {
		t.Fatalf("missing note: %q", out[0].Content)
	}
}

// TestResolveFiles_NoFilesIsIdentity: a file-free history comes back
// as the same slice — the send path pays nothing for sessions that
// never touched documents.
func TestResolveFiles_NoFilesIsIdentity(t *testing.T) {
	t.Parallel()
	in := []Message{{Role: RoleUser, Content: "hi"}}
	out := ResolveFiles(context.Background(), in, nil)
	if len(out) != 1 || &out[0] != &in[0] {
		t.Fatalf("file-free slice must pass through unchanged")
	}
}

// TestResolveFiles_CacheScopedByPurpose: the same asset parsed as text
// and as rich are different documents — one cached id must not stand in
// for the other.
func TestResolveFiles_CacheScopedByPurpose(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{}
	r := NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF"), nil }, up)
	r.Now = at(time.Unix(2000, 0))

	text := []Message{{Role: RoleUser, Content: "a", Files: []FilePart{{
		Asset: "same.pdf", FileID: "stale", ExpiresAt: 1, Purpose: PurposeTextRecognition,
	}}}}
	rich := []Message{{Role: RoleUser, Content: "b", Files: []FilePart{{
		Asset: "same.pdf", FileID: "stale", ExpiresAt: 1, Purpose: PurposeRichLayoutRecognition,
	}}}}
	ResolveFiles(context.Background(), text, r)
	ResolveFiles(context.Background(), rich, r)
	if up.count() != 2 {
		t.Fatalf("uploads = %d, want 2 (text and rich are distinct documents)", up.count())
	}
	if up.calls[0].Purpose != PurposeTextRecognition || up.calls[1].Purpose != PurposeRichLayoutRecognition {
		t.Fatalf("purposes = %q,%q", up.calls[0].Purpose, up.calls[1].Purpose)
	}
	// And each purpose now hits its own cached entry.
	ResolveFiles(context.Background(), text, r)
	ResolveFiles(context.Background(), rich, r)
	if up.count() != 2 {
		t.Fatalf("uploads after second pass = %d, want still 2", up.count())
	}
}

// TestResolveFiles_EmptyUploadID: a malformed 200 (no id) degrades like
// any other upload failure and must NOT poison the cache — the next
// resolve has to try again rather than serving an empty id (which would
// hard-fail the wire marshal).
func TestResolveFiles_EmptyUploadID(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{obj: &FileObject{}}
	r := NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF"), nil }, up)
	in := []Message{{Role: RoleUser, Content: "x", Files: []FilePart{{Asset: "a.pdf"}}}}

	out := ResolveFiles(context.Background(), in, r)
	if len(out[0].Files) != 0 || !strings.Contains(out[0].Content, "no file id") {
		t.Fatalf("empty id must degrade in-band: %+v", out[0])
	}
	// Second attempt: still one upload per resolve, no cached empty ref.
	ResolveFiles(context.Background(), in, r)
	if up.count() != 2 {
		t.Fatalf("uploads = %d, want 2 (no cache poisoning)", up.count())
	}
}

// TestResolveFiles_InputImmutability is the deep-equality guard: a
// resolve that mixes pass-through, re-upload and failure must leave the
// caller's slice — messages AND part slices — byte-identical.
func TestResolveFiles_InputImmutability(t *testing.T) {
	t.Parallel()
	up := &fakeUploader{obj: &FileObject{ID: "file-api-new", ExpiresAt: 9000}}
	r := NewFileResolver(func(asset string) ([]byte, error) {
		if asset == "gone.pdf" {
			return nil, errors.New("asset gone")
		}
		return []byte("%PDF"), nil
	}, up)
	r.Now = at(time.Unix(2000, 0))

	in := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "mix", Files: []FilePart{
			{Asset: "ok.pdf", FileID: "file-api-live", ExpiresAt: 5000},
			{Asset: "stale.pdf", FileID: "file-api-old", ExpiresAt: 1},
			{Asset: "gone.pdf"},
		}},
	}
	before := make([]Message, len(in))
	for i, m := range in {
		before[i] = m
		before[i].Files = append([]FilePart(nil), m.Files...)
	}

	ResolveFiles(context.Background(), in, r)

	if len(in) != len(before) {
		t.Fatalf("message count changed: %d vs %d", len(in), len(before))
	}
	for i := range in {
		if in[i].Content != before[i].Content {
			t.Fatalf("message %d content mutated: %q", i, in[i].Content)
		}
		if len(in[i].Files) != len(before[i].Files) {
			t.Fatalf("message %d part count mutated: %d vs %d", i, len(in[i].Files), len(before[i].Files))
		}
		for j := range in[i].Files {
			if in[i].Files[j] != before[i].Files[j] {
				t.Fatalf("message %d part %d mutated: %+v vs %+v", i, j, in[i].Files[j], before[i].Files[j])
			}
		}
	}
}

// TestResolveFiles_Concurrent is the -race exercise: several turns
// resolving the same expired part at once must not corrupt the cache
// (duplicate uploads are acceptable — a transport cache is not a
// single-flight lock; correctness is).
func TestResolveFiles_Concurrent(t *testing.T) {
	up := &fakeUploader{block: func() { time.Sleep(time.Millisecond) }}
	r := NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF"), nil }, up)
	r.Now = at(time.Unix(2000, 0))
	in := []Message{{Role: RoleUser, Content: "x", Files: []FilePart{{Asset: "a.pdf", FileID: "stale", ExpiresAt: 1500}}}}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := ResolveFiles(context.Background(), in, r)
			if len(out[0].Files) != 1 || out[0].Files[0].FileID == "" {
				t.Errorf("concurrent resolve lost the part: %+v", out[0].Files)
			}
		}()
	}
	wg.Wait()
}

// TestResolveFiles_FullPathHTTP walks the whole F4 path against an
// httptest fake: an expired part's asset is re-uploaded through the
// real *Client (POST /files), and the resulting ChatRequest body
// carries the NEW file_id — with the multipart purpose-first contract
// intact (a re-upload is still an upload).
func TestResolveFiles_FullPathHTTP(t *testing.T) {
	t.Parallel()
	var uploads int32
	var gotFilename, gotPurpose string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != endpointFiles {
			t.Errorf("path = %q", req.URL.Path)
		}
		atomic.AddInt32(&uploads, 1)
		parts := readUploadParts(t, req)
		if len(parts) != 2 || parts[0].formName != "purpose" || parts[1].formName != "file" {
			t.Fatalf("multipart shape = %+v, want purpose-then-file", parts)
		}
		gotPurpose, gotFilename = string(parts[0].data), parts[1].filename
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"file-api-reup","purpose":"text_recognition","page_count":2,"expires_at":9000}`)
	}))
	defer srv.Close()

	client := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	r := NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF-1.4 reupload"), nil }, client)
	r.Now = at(time.Unix(2000, 0))

	in := []Message{{Role: RoleUser, Content: "read", Files: []FilePart{{Asset: "ab12.pdf", FileID: "file-api-old", ExpiresAt: 1500}}}}
	out := ResolveFiles(context.Background(), in, r)

	if got := atomic.LoadInt32(&uploads); got != 1 {
		t.Fatalf("uploads = %d, want 1", got)
	}
	if gotFilename != "ab12.pdf" || gotPurpose != PurposeTextRecognition {
		t.Fatalf("upload = {filename:%q purpose:%q}", gotFilename, gotPurpose)
	}
	if out[0].Files[0].FileID != "file-api-reup" || out[0].Files[0].ExpiresAt != 9000 {
		t.Fatalf("resolved = %+v", out[0].Files[0])
	}
	buf, err := json.Marshal(&ChatRequest{Model: ModelV41Flash, Messages: out})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(buf), `"file_id":"file-api-reup"`) || strings.Contains(string(buf), "file-api-old") {
		t.Fatalf("wire body = %s", buf)
	}
}

// TestWithoutFiles: the side-channel strip clears the parts, keeps the
// in-band markers (self-description), never mutates the input, and is
// the identity for file-free history.
func TestWithoutFiles(t *testing.T) {
	t.Parallel()
	in := []Message{
		{Role: RoleUser, Content: "read\n\n[file: r.pdf — uploaded · text · file-api-1]",
			Files: []FilePart{{Asset: "r.pdf", FileID: "file-api-1"}}},
		{Role: RoleAssistant, Content: "ok"},
	}
	out := WithoutFiles(in)
	if len(out[0].Files) != 0 {
		t.Fatalf("files not stripped: %+v", out[0].Files)
	}
	if !strings.Contains(out[0].Content, "[file: r.pdf") {
		t.Fatalf("marker must survive: %q", out[0].Content)
	}
	if len(in[0].Files) != 1 {
		t.Fatalf("input mutated: %+v", in[0])
	}
	if _, err := json.Marshal(&ChatRequest{Model: ModelV41Flash, Messages: out}); err != nil {
		t.Fatalf("stripped history must marshal: %v", err)
	}

	plain := []Message{{Role: RoleUser, Content: "hi"}}
	if got := WithoutFiles(plain); &got[0] != &plain[0] {
		t.Fatal("file-free slice must pass through unchanged")
	}
}

// TestResolveFileData: the second-tier send station materialises every
// Asset-only file part into a data: URL on the request copy (media
// type from the asset extension), leaves already-materialised parts
// alone, and never mutates the input — the byte-carrier sibling of
// ResolveFiles' file_id materialisation.
func TestResolveFileData(t *testing.T) {
	in := []Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleUser, Content: "read",
			Files: []FilePart{
				{Asset: "ab12.pdf"},
				{Asset: "cd34.docx"},
				{Asset: "ef56.pdf", Data: "data:application/pdf;base64,QQ=="}, // already materialised
			}},
	}
	load := func(asset string) ([]byte, error) { return []byte("%PDF-" + asset), nil }
	out := ResolveFileData(in, load)
	if len(out[0].Files) != 0 {
		t.Fatalf("file-free message changed: %+v", out[0])
	}
	fs := out[1].Files
	if len(fs) != 3 {
		t.Fatalf("parts = %+v", fs)
	}
	if fs[0].Data != "data:application/pdf;base64,JVBERi1hYjEyLnBkZg==" {
		t.Fatalf("pdf data URL = %q", fs[0].Data)
	}
	if !strings.HasPrefix(fs[1].Data, "data:application/vnd.openxmlformats-officedocument.wordprocessingml.document;base64,") {
		t.Fatalf("docx must keep its true media type: %q", fs[1].Data)
	}
	if fs[2].Data != "data:application/pdf;base64,QQ==" {
		t.Fatalf("materialised part must pass through: %q", fs[2].Data)
	}
	// The transcript-shaped input keeps the durable Asset form.
	if in[1].Files[0].Data != "" {
		t.Fatalf("input mutated: %+v", in[1].Files[0])
	}
	// File-free input returns the same slice (the fast path).
	plain := []Message{{Role: RoleUser, Content: "hi"}}
	if got := ResolveFileData(plain, load); &got[0] != &plain[0] {
		t.Fatal("file-free slice must pass through unchanged")
	}
}

// TestResolveFileData_FailuresDegradeInBand: a part with no asset, a
// nil loader, and a load error each degrade to the "[file: " note
// family instead of aborting — same never-error posture as
// ResolveImages / ResolveFiles.
func TestResolveFileData_FailuresDegradeInBand(t *testing.T) {
	boom := func(string) ([]byte, error) { return nil, errors.New("asset gone") }
	out := ResolveFileData([]Message{{Role: RoleUser, Content: "read", Files: []FilePart{
		{FileID: "file-api-1"}, // no asset to materialise
		{Asset: "ab12.pdf"},    // loader fails below
	}}}, boom)
	if len(out[0].Files) != 0 {
		t.Fatalf("failed parts must be dropped: %+v", out[0].Files)
	}
	if !strings.Contains(out[0].Content, "[file: ? — 附件不可用: file part has no asset") &&
		!strings.Contains(out[0].Content, "file part has no asset") {
		t.Fatalf("no-asset note missing: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "[file: ab12.pdf — 附件不可用: asset gone]") {
		t.Fatalf("load-fail note missing: %q", out[0].Content)
	}
	// Nil loader: same degradation, different reason.
	out = ResolveFileData([]Message{{Role: RoleUser, Files: []FilePart{{Asset: "ab12.pdf"}}}}, nil)
	if !strings.Contains(out[0].Content, "no file loader wired") {
		t.Fatalf("nil-loader note missing: %q", out[0].Content)
	}
}

// TestFileMediaType pins the extension map: the five document
// extensions filerefs detects, octet-stream for everything else. A
// local map, not mime.TypeByExtension, so the answer can't drift with
// the host OS.
func TestFileMediaType(t *testing.T) {
	cases := map[string]string{
		"x.pdf":      "application/pdf",
		"x.PDF":      "application/pdf", // case-insensitive
		"x.docx":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"x.ppt":      "application/vnd.ms-powerpoint",
		"ab12cd.pdf": "application/pdf", // content-addressed asset names keep the extension
		"x.txt":      "application/octet-stream",
		"noext":      "application/octet-stream",
	}
	for name, want := range cases {
		if got := FileMediaType(name); got != want {
			t.Errorf("FileMediaType(%q) = %q, want %q", name, got, want)
		}
	}
}
