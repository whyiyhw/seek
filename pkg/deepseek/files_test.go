package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// filesServer mimics the Files API at /files and counts requests so
// retry-once behaviour is observable. Script semantics match
// asrServer / searchServer: each handler runs once in order, the last
// repeats.
func filesServer(t *testing.T, script ...http.HandlerFunc) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	i := -1
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		if r.URL.Path != endpointFiles {
			t.Errorf("path = %q, want %q", r.URL.Path, endpointFiles)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer t" {
			t.Errorf("Authorization = %q, want Bearer t", got)
		}
		h := script[len(script)-1]
		i++
		if i < len(script)-1 {
			h = script[i]
		}
		h(w, r)
	})), &n
}

// uploadPart is one parsed multipart part of an upload body.
type uploadPart struct {
	formName string
	filename string
	data     []byte
}

// readUploadParts walks the RAW part stream in wire order. This — not
// r.FormFile, which loses ordering — is the authority for F1's
// load-bearing constraint: the `purpose` field part must precede the
// `file` part or the API 400s (PRD §1.2).
func readUploadParts(t *testing.T, r *http.Request) []uploadPart {
	t.Helper()
	mr, err := r.MultipartReader()
	if err != nil {
		t.Errorf("multipart reader: %v", err)
		return nil
	}
	var parts []uploadPart
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return parts
		}
		if err != nil {
			t.Errorf("next multipart part: %v", err)
			return parts
		}
		b, err := io.ReadAll(p)
		if err != nil {
			t.Errorf("read part %q: %v", p.FormName(), err)
			return parts
		}
		parts = append(parts, uploadPart{formName: p.FormName(), filename: p.FileName(), data: b})
	}
}

func writeFileObjectJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{
		"id": "file-api-9f2c",
		"object": "file",
		"bytes": 120000,
		"created_at": 1762800000,
		"filename": "report.pdf",
		"purpose": "text_recognition",
		"page_count": 12,
		"expires_at": 1765392000
	}`))
}

// TestUploadFile_MultipartFieldOrder is F1's transport acceptance: the
// multipart body has exactly two parts, `purpose` first (a plain field,
// no filename) then `file` (carrying filename + bytes), and the 200
// body decodes into FileObject with the parse output (page_count) that
// F3's per-page billing keys off.
func TestUploadFile_MultipartFieldOrder(t *testing.T) {
	doc := []byte("%PDF-1.7 fake report body")
	srv, n := filesServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
			t.Errorf("Content-Type = %q, want multipart/form-data with boundary", ct)
		}
		parts := readUploadParts(t, r)
		if len(parts) != 2 {
			t.Fatalf("got %d multipart parts, want exactly 2 (purpose, file): %+v", len(parts), parts)
		}
		if parts[0].formName != "purpose" || parts[0].filename != "" || string(parts[0].data) != PurposeTextRecognition {
			t.Errorf("part 0 = {name:%q filename:%q data:%q}, want purpose field with %q and no filename",
				parts[0].formName, parts[0].filename, parts[0].data, PurposeTextRecognition)
		}
		if parts[1].formName != "file" || parts[1].filename != "report.pdf" || string(parts[1].data) != string(doc) {
			t.Errorf("part 1 = {name:%q filename:%q data:%d bytes}, want file part report.pdf with the document",
				parts[1].formName, parts[1].filename, len(parts[1].data))
		}
		writeFileObjectJSON(w)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	obj, err := c.UploadFile(context.Background(), &FileUploadRequest{
		Filename: "report.pdf",
		Data:     doc,
		Purpose:  PurposeTextRecognition,
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	want := FileObject{
		ID: "file-api-9f2c", Object: "file", Bytes: 120000, CreatedAt: 1762800000,
		Filename: "report.pdf", Purpose: PurposeTextRecognition, PageCount: 12, ExpiresAt: 1765392000,
	}
	if *obj != want {
		t.Errorf("obj = %+v, want %+v", *obj, want)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestUploadFile_4xxFailsFastWithoutRetry(t *testing.T) {
	srv, n := filesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "file field must come after purpose"}}`))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.UploadFile(context.Background(), &FileUploadRequest{Filename: "a.pdf", Data: []byte("x"), Purpose: PurposeTextRecognition})
	if err == nil {
		t.Fatal("want error on 400")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || apiErr.Message != "file field must come after purpose" {
		t.Errorf("err = %v, want APIError 400 with parsed message", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1 — non-429 4xx must NOT retry", got)
	}
}

func TestUploadFile_RetriesOnceThenSucceeds(t *testing.T) {
	doc := []byte("%PDF-1.7 retry me")
	var first []uploadPart
	srv, n := filesServer(t,
		func(w http.ResponseWriter, r *http.Request) { // attempt 1: transient 500
			first = readUploadParts(t, r)
			w.WriteHeader(http.StatusInternalServerError)
		},
		func(w http.ResponseWriter, r *http.Request) { // attempt 2: OK
			second := readUploadParts(t, r)
			if len(second) != 2 || len(first) != 2 ||
				first[0].formName != second[0].formName || string(first[0].data) != string(second[0].data) ||
				first[1].formName != second[1].formName || first[1].filename != second[1].filename ||
				string(first[1].data) != string(second[1].data) {
				t.Errorf("retry upload differs from first attempt:\n first %+v\nsecond %+v", first, second)
			}
			writeFileObjectJSON(w)
		},
	)
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	obj, err := c.UploadFile(context.Background(), &FileUploadRequest{Filename: "a.pdf", Data: doc, Purpose: PurposeRichLayoutRecognition})
	if err != nil {
		t.Fatalf("UploadFile after one retry: %v", err)
	}
	if obj.ID != "file-api-9f2c" {
		t.Errorf("obj = %+v, want decoded id", obj)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (one retry)", got)
	}
}

func TestUploadFile_RetriesOnceThenGivesUp(t *testing.T) {
	srv, n := filesServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.UploadFile(context.Background(), &FileUploadRequest{Filename: "a.pdf", Data: []byte("x"), Purpose: PurposeTextRecognition})
	if err == nil || !strings.Contains(err.Error(), "after 2 attempts") {
		t.Errorf("err = %v, want annotated 'after 2 attempts'", err)
	}
	if got := atomic.LoadInt32(n); got != 2 {
		t.Errorf("requests = %d, want 2 (retry once, no more)", got)
	}
}

// TestUploadFile_ContextCanceled is the ctx.Done path (AGENTS.md
// "Testing": any ctx-aware function gets one). Same drain-then-park
// dance as TestASR_ContextCanceled — see that test's comment for why
// the body MUST be drained before parking on srv.Close().
func TestUploadFile_ContextCanceled(t *testing.T) {
	srv, n := filesServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	})
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	obj, err := c.UploadFile(ctx, &FileUploadRequest{Filename: "a.pdf", Data: []byte("x"), Purpose: PurposeTextRecognition})
	if err == nil {
		t.Fatal("want error on cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled in chain", err)
	}
	if obj != nil {
		t.Errorf("obj = %+v, want nil", obj)
	}
	if got := atomic.LoadInt32(n); got > 1 {
		t.Errorf("requests = %d, want ≤1 — no retry after cancellation", got)
	}
}

func TestUploadFile_BadJSONResponse(t *testing.T) {
	srv, _ := filesServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	_, err := c.UploadFile(context.Background(), &FileUploadRequest{Filename: "a.pdf", Data: []byte("x"), Purpose: PurposeTextRecognition})
	if err == nil || !strings.Contains(err.Error(), "decode file upload response") {
		t.Errorf("err = %v, want decode failure", err)
	}
}

func TestUploadFile_RequestValidation(t *testing.T) {
	// The boundary sub-test pushes a real 100 MiB body through, so the
	// handler drains before responding — an unread body makes the
	// server's post-handler close race the client's write (same
	// discipline as TestASR_ContextCanceled's comment).
	srv, _ := filesServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writeFileObjectJSON(w)
	})
	defer srv.Close()

	c := New(WithAPIKey("t"), WithBaseURL(srv.URL))
	cases := []struct {
		name string
		req  *FileUploadRequest
		want string
	}{
		{"nil request", nil, "nil file upload request"},
		{"empty filename", &FileUploadRequest{Data: []byte("x"), Purpose: PurposeTextRecognition}, "filename is empty"},
		{"empty data", &FileUploadRequest{Filename: "a.pdf", Purpose: PurposeTextRecognition}, "data is empty"},
		{"bad purpose", &FileUploadRequest{Filename: "a.pdf", Data: []byte("x"), Purpose: "vision"}, "not one of"},
	}
	for _, tc := range cases {
		_, err := c.UploadFile(context.Background(), tc.req)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}

	// Size boundary: exactly 100 MiB must PASS client-side and reach
	// the server; 100 MiB + 1 byte must not. One allocation serves both
	// (shared prefix), so the suite doesn't pay 200 MiB.
	big := make([]byte, FileMaxBytes+1)
	if _, err := c.UploadFile(context.Background(), &FileUploadRequest{Filename: "big.pdf", Data: big, Purpose: PurposeTextRecognition}); err == nil || !strings.Contains(err.Error(), "100 MiB") {
		t.Errorf("oversize: err = %v, want containing %q", err, "100 MiB")
	}
	if _, err := c.UploadFile(context.Background(), &FileUploadRequest{Filename: "big.pdf", Data: big[:FileMaxBytes], Purpose: PurposeTextRecognition}); err != nil {
		t.Errorf("exactly 100 MiB rejected: %v", err)
	}
}

func TestUploadFile_MissingAPIKey(t *testing.T) {
	c := New()
	_, err := c.UploadFile(context.Background(), &FileUploadRequest{Filename: "a.pdf", Data: []byte("x"), Purpose: PurposeTextRecognition})
	if err == nil || !strings.Contains(err.Error(), "missing api key") {
		t.Errorf("err = %v, want missing api key", err)
	}
}

// TestChatRequest_TextOnly_Bytes is F1's byte-equivalence guard at
// REQUEST level: a text-only request must serialise to exactly the
// pre-files bytes — this is what becomes prompt tokens and what
// DeepSeek's prefix cache keys on (PRD D-F6; message-level half of the
// guard lives in TestMessageWire_TextOnly_Bytes, which predates F1 and
// must stay green untouched). Any drift here needs a deliberate
// migration story.
func TestChatRequest_TextOnly_Bytes(t *testing.T) {
	req := &ChatRequest{
		Model: ModelV41Flash,
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: "", ToolCalls: []ToolCall{{Function: ToolCallFunc{Name: "grep"}}}},
			{Role: RoleTool, Content: "ok", ToolCallID: "t1"},
		},
	}
	got, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"deepseek-flash","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},` +
		`{"role":"assistant","tool_calls":[{"function":{"name":"grep","arguments":""}}]},` +
		`{"role":"tool","content":"ok","tool_call_id":"t1"}]}`
	if string(got) != want {
		t.Errorf("text-only request bytes drifted:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(string(got), `"files"`) {
		t.Errorf("text-only request leaked a files key: %s", got)
	}
}

// TestMessage_SessionForm_Bytes_Files pins the session/transcript
// form: text-only messages omit `files` entirely (old JSONL lines stay
// byte-identical), and file parts ride the sibling field with the
// string content untouched.
func TestMessage_SessionForm_Bytes_Files(t *testing.T) {
	got, err := json.Marshal(Message{Role: RoleUser, Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"role":"user","content":"hi"}` {
		t.Errorf("text-only session form drifted: %s", got)
	}

	got, err = json.Marshal(Message{
		Role:    RoleUser,
		Content: "see",
		Files:   []FilePart{{Asset: "ab12cd34ef56.pdf"}, {Asset: "ff0011.pdf", FileID: "file-api-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":"see","files":[{"asset":"ab12cd34ef56.pdf"},{"asset":"ff0011.pdf","file_id":"file-api-1"}]}`
	if string(got) != want {
		t.Errorf("file session form:\n got %s\nwant %s", got, want)
	}
}

// TestChatRequest_WireForm_Files is F1's wire acceptance: file-bearing
// user messages render array-form content whose file parts are
// {"type":"file","file_id":…} blocks standing alongside text and
// image_url parts, and nothing session-shaped leaks.
func TestChatRequest_WireForm_Files(t *testing.T) {
	req := &ChatRequest{
		Model: ModelV41Flash,
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{
				Role:    RoleUser,
				Content: "read these",
				Images:  []ImagePart{{Asset: "a.png", URL: "data:image/png;base64,QUJD"}},
				Files: []FilePart{
					{Asset: "r1.pdf", FileID: "file-api-1"},
					{FileID: "file-api-2"},
				},
			},
		},
	}
	buf, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(raw.Messages))
	}
	if !strings.Contains(string(raw.Messages[0]), `"content":"sys"`) {
		t.Errorf("system message content changed: %s", raw.Messages[0])
	}
	var parts []map[string]any
	if err := json.Unmarshal(raw.Messages[1], &struct {
		Content *[]map[string]any `json:"content"`
	}{&parts}); err != nil {
		t.Fatalf("user content not an array: %v (%s)", err, raw.Messages[1])
	}
	if len(parts) != 4 {
		t.Fatalf("want 4 content parts (text, image, file, file), got %d: %v", len(parts), parts)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "read these" {
		t.Errorf("part 0 = %v", parts[0])
	}
	if iu, _ := parts[1]["image_url"].(map[string]any); parts[1]["type"] != "image_url" || iu["url"] != "data:image/png;base64,QUJD" {
		t.Errorf("part 1 = %v", parts[1])
	}
	if parts[2]["type"] != "file" || parts[2]["file_id"] != "file-api-1" {
		t.Errorf("part 2 = %v, want file/file-api-1", parts[2])
	}
	if parts[3]["type"] != "file" || parts[3]["file_id"] != "file-api-2" {
		t.Errorf("part 3 = %v, want file/file-api-2", parts[3])
	}
	if _, leak := parts[2]["asset"]; leak {
		t.Errorf("file part leaked asset ref: %v", parts[2])
	}
	// Persistence-only fields must never cross the API boundary.
	for _, banned := range []string{`"files"`, `"asset"`, `"predicted_next"`} {
		if strings.Contains(string(buf), banned) {
			t.Errorf("wire form leaked %s: %s", banned, buf)
		}
	}
}

// TestChatRequest_UnresolvedFileErrors: an Asset-only part at wire
// time means the caller skipped ResolveFiles — the request must fail
// loudly instead of silently dropping a document the user attached.
// (ResolveFiles itself lands with F4; the guard rides along from day
// one so F4 can't ship without it.)
func TestChatRequest_UnresolvedFileErrors(t *testing.T) {
	req := &ChatRequest{
		Model:    ModelV41Flash,
		Messages: []Message{{Role: RoleUser, Content: "x", Files: []FilePart{{Asset: "r.pdf"}}}},
	}
	_, err := json.Marshal(req)
	if err == nil || !strings.Contains(err.Error(), "ResolveFiles") {
		t.Fatalf("want ResolveFiles error, got %v", err)
	}
}

// TestMessage_Unmarshal_WireFileParts: session load accepts the wire
// form — file parts become FileID-bearing FileParts, images and text
// land as usual (mirrors TestMessage_Unmarshal_BothForms).
func TestMessage_Unmarshal_WireFileParts(t *testing.T) {
	in := `{"role":"user","content":[` +
		`{"type":"text","text":"see "},` +
		`{"type":"file","file_id":"file-api-1"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}},` +
		`{"type":"file","file_id":"file-api-2"},` +
		`{"type":"text","text":"attached"}` +
		`]}`
	var m Message
	if err := json.Unmarshal([]byte(in), &m); err != nil {
		t.Fatal(err)
	}
	if m.Role != RoleUser || m.Content != "see attached" {
		t.Errorf("content = %q, want text parts concatenated", m.Content)
	}
	if len(m.Files) != 2 || m.Files[0].FileID != "file-api-1" || m.Files[1].FileID != "file-api-2" || m.Files[0].Asset != "" {
		t.Errorf("files = %+v, want two FileID-only parts", m.Files)
	}
	if len(m.Images) != 1 || m.Images[0].URL != "data:image/png;base64,QUJD" {
		t.Errorf("images = %+v, want the one image part", m.Images)
	}
}

// TestSessionRoundTrip_Files: session-form marshal → unmarshal
// preserves the asset reference and any resolved file_id (what
// internal/session does per JSONL line).
func TestSessionRoundTrip_Files(t *testing.T) {
	in := Message{
		Role:    RoleUser,
		Content: "see",
		Files:   []FilePart{{Asset: "r.pdf"}, {Asset: "r2.pdf", FileID: "file-api-9"}},
	}
	buf, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Message
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatal(err)
	}
	if out.Content != "see" || len(out.Files) != 2 ||
		out.Files[0].Asset != "r.pdf" || out.Files[0].FileID != "" ||
		out.Files[1].Asset != "r2.pdf" || out.Files[1].FileID != "file-api-9" {
		t.Errorf("round trip drifted: %+v", out)
	}
}
