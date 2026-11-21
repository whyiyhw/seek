package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whyiyhw/seek/pkg/deepseek"
)

// sseAnswer writes one canned streaming answer (the shape every
// vision/files wire test in this package uses).
func sseAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"` + text + `"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n"))
}

// TestPrompt_Files_ResolvedOnWire verifies the F4 send path: a
// file-bearing message reaches the API with a {"type":"file"} content
// part, and the agent's own history keeps the durable Asset form (what
// a session save persists) — the document sibling of
// TestPrompt_Images_ResolvedOnWire.
func TestPrompt_Files_ResolvedOnWire(t *testing.T) {
	t.Parallel()
	var reqBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqBody.Store(string(b))
		sseAnswer(w, "the report says Zephyr-42")
	}))
	defer srv.Close()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
		FileResolver: deepseek.NewFileResolver(
			func(string) ([]byte, error) {
				t.Error("a live file_id must not be re-uploaded")
				return nil, nil
			},
			deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		),
	})
	if err != nil {
		t.Fatal(err)
	}

	for ev := range ag.PromptFiles(context.Background(), "summarise", nil,
		[]deepseek.FilePart{{Asset: "ab12cd34ef56.pdf", FileID: "file-api-live", ExpiresAt: time.Now().Add(time.Hour).Unix()}}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("unexpected error event: %v", e.Err)
		}
	}

	body, _ := reqBody.Load().(string)
	var raw struct {
		Messages []struct {
			Role    string          `json:"role"`
			Files   json.RawMessage `json:"files"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range raw.Messages {
		if m.Role != deepseek.RoleUser {
			continue
		}
		if len(m.Files) != 0 {
			t.Errorf("wire leaked the files sibling: %s", m.Files)
		}
		var parts []map[string]any
		if err := json.Unmarshal(m.Content, &parts); err == nil {
			for _, p := range parts {
				if p["type"] == "file" && p["file_id"] == "file-api-live" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("no file part on the wire: %s", body)
	}

	// History keeps the durable form.
	for _, m := range ag.Messages() {
		if m.Role == deepseek.RoleUser && len(m.Files) == 1 {
			if m.Files[0].Asset != "ab12cd34ef56.pdf" {
				t.Fatalf("history part drifted: %+v", m.Files[0])
			}
			return
		}
	}
	t.Fatal("history lost the file part")
}

// TestPrompt_ExpiredFile_ReuploadedOnWire: the session outlived its
// file_id — the agent's resolver must re-upload from the local asset
// (POST /files) and send the FRESH id, without touching history.
func TestPrompt_ExpiredFile_ReuploadedOnWire(t *testing.T) {
	t.Parallel()
	var uploads int32
	var reqBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files":
			atomic.AddInt32(&uploads, 1)
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"file-api-fresh","purpose":"text_recognition","page_count":1,"expires_at":`+
				`4102444800}`)
		default:
			b, _ := io.ReadAll(r.Body)
			reqBody.Store(string(b))
			sseAnswer(w, "read it again")
		}
	}))
	defer srv.Close()

	client := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	ag, err := New(Config{
		Client:       client,
		Model:        deepseek.ModelV41Flash,
		FileResolver: deepseek.NewFileResolver(func(string) ([]byte, error) { return []byte("%PDF-1.4"), nil }, client),
	})
	if err != nil {
		t.Fatal(err)
	}

	for ev := range ag.PromptFiles(context.Background(), "again", nil,
		[]deepseek.FilePart{{Asset: "ab12.pdf", FileID: "file-api-stale", ExpiresAt: 1}}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("expired id must heal, not fail: %v", e.Err)
		}
	}
	if got := atomic.LoadInt32(&uploads); got != 1 {
		t.Fatalf("uploads = %d, want 1", got)
	}
	body, _ := reqBody.Load().(string)
	if !strings.Contains(body, `"file_id":"file-api-fresh"`) || strings.Contains(body, "file-api-stale") {
		t.Fatalf("wire body = %s", body)
	}
	// History is untouched — the stale id is what persists; the resolver's
	// cache (not a history write-back) is what stops repeat uploads.
	for _, m := range ag.Messages() {
		if len(m.Files) == 1 && m.Files[0].FileID != "file-api-stale" {
			t.Fatalf("history mutated: %+v", m.Files[0])
		}
	}
}

// TestPrompt_FileAssetGone_DegradesInBand: an unrecoverable document
// never kills the turn — the request goes out without the part and the
// note rides in-band (never-error posture).
func TestPrompt_FileAssetGone_DegradesInBand(t *testing.T) {
	t.Parallel()
	var reqBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqBody.Store(string(b))
		sseAnswer(w, "ok")
	}))
	defer srv.Close()

	client := deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL))
	ag, err := New(Config{
		Client:       client,
		Model:        deepseek.ModelV41Flash,
		FileResolver: deepseek.NewFileResolver(func(string) ([]byte, error) { return nil, io.ErrUnexpectedEOF }, client),
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ag.PromptFiles(context.Background(), "read", nil,
		[]deepseek.FilePart{{Asset: "dead.pdf"}}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("missing asset must degrade in-band: %v", e.Err)
		}
	}
	body, _ := reqBody.Load().(string)
	if !strings.Contains(body, "[file: dead.pdf") {
		t.Fatalf("note missing from request: %s", body)
	}
	if strings.Contains(body, `"type":"file"`) {
		t.Fatalf("failed part must be dropped: %s", body)
	}
}

// TestSummarise_StripsFiles: the /compact side-channel call must not
// carry — or depend on resolving — document ids. Before WithoutFiles it
// would have shipped live file_ids into the summariser request (paying
// input tokens for the parsed document on a call that only needs prose).
func TestSummarise_StripsFiles(t *testing.T) {
	t.Parallel()
	var reqBody atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		reqBody.Store(string(b))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"briefing"}}],"usage":{}}`)
	}))
	defer srv.Close()

	ag, err := New(Config{
		Client: deepseek.New(deepseek.WithAPIKey("t"), deepseek.WithBaseURL(srv.URL)),
		Model:  deepseek.ModelV41Flash,
	})
	if err != nil {
		t.Fatal(err)
	}
	ag.Reset([]deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "read\n\n[file: r.pdf — uploaded · text · file-api-1]",
		Files:   []deepseek.FilePart{{Asset: "r.pdf", FileID: "file-api-1"}},
	}})
	if _, _, err := ag.Summarise(context.Background()); err != nil {
		t.Fatal(err)
	}
	body, _ := reqBody.Load().(string)
	// The in-band marker keeps the old file_id in Content by design;
	// what must NOT appear is a files sibling or a file content part.
	if strings.Contains(body, `"files"`) || strings.Contains(body, `"type":"file"`) {
		t.Fatalf("summarise leaked files: %s", body)
	}
	if !strings.Contains(body, "[file: r.pdf") {
		t.Fatalf("marker must stay for self-description: %s", body)
	}
	for _, m := range ag.Messages() {
		if len(m.Files) != 1 {
			t.Fatal("Summarise must not mutate history")
		}
	}
}

// TestMsgsToLLM_FilesNote: the second-tier provider path can't carry
// document parts — it must say so in-band instead of silently erasing
// the attachment (image sibling of this note predates F4).
func TestMsgsToLLM_FilesNote(t *testing.T) {
	t.Parallel()
	out := msgsToLLM([]deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "read",
		Files:   []deepseek.FilePart{{Asset: "a.pdf", FileID: "file-api-1"}},
	}})
	if len(out) != 1 || !strings.Contains(out[0].Content, "[file: 1 个文档未透传") {
		t.Fatalf("files note missing: %+v", out)
	}
	if !strings.HasPrefix(out[0].Content, "read") {
		t.Fatalf("original text lost: %q", out[0].Content)
	}
}

// TestPrompt_LLMPath_CarriesFiles: the second-tier send path must
// materialise and carry document bytes on a file-capable model — the
// file sibling of TestPrompt_LLMPath_CarriesImages. The FileResolver
// is wired exactly as cmd/seek wires it on second-tier lanes (loader
// set, no uploader).
func TestPrompt_LLMPath_CarriesFiles(t *testing.T) {
	prov := &recordingProvider{}
	ag, err := New(Config{
		Provider: prov,
		Model:    "gpt-4o", // file-capable, second-tier lane
		FileResolver: deepseek.NewFileResolver(
			func(asset string) ([]byte, error) { return []byte("%PDF-1.4"), nil }, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ag.PromptFiles(context.Background(), "read this", nil,
		[]deepseek.FilePart{{Asset: "ab12cd34.pdf"}}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	req := prov.last(t)
	var found bool
	for _, m := range req.Messages {
		if len(m.Files) == 1 && m.Files[0].Filename == "ab12cd34.pdf" &&
			m.Files[0].URL == "data:application/pdf;base64,JVBERi0xLjQ=" {
			found = true
		}
	}
	if !found {
		t.Fatalf("file part did not reach the provider: %+v", req.Messages)
	}
	// History keeps the durable Asset form, never the data URL.
	for _, m := range ag.Messages() {
		for _, p := range m.Files {
			if p.Data != "" || p.Asset != "ab12cd34.pdf" {
				t.Fatalf("history must keep the Asset form: %+v", p)
			}
		}
	}
}

// TestPrompt_LLMPath_IncapableModelNotesFiles: a model outside
// modelcap's Files axis degrades the parts to the in-band note — never
// hands a 400-triggering payload to the provider.
func TestPrompt_LLMPath_IncapableModelNotesFiles(t *testing.T) {
	prov := &recordingProvider{}
	ag, err := New(Config{
		Provider: prov,
		Model:    "glm-5.3", // no file part on the compatible endpoint
		FileResolver: deepseek.NewFileResolver(
			func(string) ([]byte, error) { return []byte("%PDF-1.4"), nil }, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	ag.Reset([]deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "read\n\n[file: r.pdf — attached · 1.0 KiB]",
		Files:   []deepseek.FilePart{{Asset: "r.pdf"}},
	}})
	for ev := range ag.Prompt(context.Background(), "and now?") {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("incapable replay must not error: %v", e.Err)
		}
	}
	req := prov.last(t)
	for _, m := range req.Messages {
		if len(m.Files) > 0 {
			t.Fatalf("files leaked to an incapable model: %+v", m)
		}
		if strings.Contains(m.Content, "attached · 1.0 KiB") && !strings.Contains(m.Content, "未透传") {
			t.Fatalf("marker without degradation note: %q", m.Content)
		}
	}
	var noted bool
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "[file: 1 个文档未透传") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("degradation note missing: %+v", req.Messages)
	}
}

// TestPrompt_LLMPath_UnsendableFormatNotes: a capable model still
// can't carry a .docx — PDF is the only format every second-tier wire
// accepts — so the part degrades with the format note at conversion.
func TestPrompt_LLMPath_UnsendableFormatNotes(t *testing.T) {
	prov := &recordingProvider{}
	ag, err := New(Config{
		Provider: prov,
		Model:    "claude-sonnet-4-20250514",
		FileResolver: deepseek.NewFileResolver(
			func(string) ([]byte, error) { return []byte("PK\x03\x04"), nil }, nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ag.PromptFiles(context.Background(), "read this", nil,
		[]deepseek.FilePart{{Asset: "spec.docx"}}) {
		if e, ok := ev.(ErrorEvent); ok {
			t.Fatalf("unexpected error: %v", e.Err)
		}
	}
	req := prov.last(t)
	for _, m := range req.Messages {
		if len(m.Files) > 0 {
			t.Fatalf("docx must not ride a second-tier lane: %+v", m.Files)
		}
	}
	var noted bool
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "[file: 1 个文档未透传") && strings.Contains(m.Content, "仅支持 PDF") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("format note missing: %+v", req.Messages)
	}
}

// TestMsgsToLLM_FilesRideWhenKept pins the conversion itself: keepFiles
// passes materialised sendable parts through as llm.File (filename =
// asset name) and drops nothing; keepFiles=false notes everything.
func TestMsgsToLLM_FilesRideWhenKept(t *testing.T) {
	msgs := []deepseek.Message{{
		Role: deepseek.RoleUser,
		Files: []deepseek.FilePart{
			{Asset: "r.pdf", Data: "data:application/pdf;base64,SUQ="},
			{Asset: "s.docx", Data: "data:application/msword;base64,SUQ="},
		},
	}}
	out := msgsToLLMWithAttachments(msgs, false, true)
	if len(out[0].Files) != 1 || out[0].Files[0].Filename != "r.pdf" {
		t.Fatalf("sendable files must ride: %+v", out[0].Files)
	}
	if !strings.Contains(out[0].Content, "[file: 1 个文档未透传") ||
		!strings.Contains(out[0].Content, "仅支持 PDF") {
		t.Fatalf("docx must stay behind with the format note: %q", out[0].Content)
	}
	out = msgsToLLMWithAttachments(msgs, false, false)
	if len(out[0].Files) != 0 || !strings.Contains(out[0].Content, "[file: 2 个文档未透传") {
		t.Fatalf("keepFiles=false must note all: %+v / %q", out[0].Files, out[0].Content)
	}
	// The two causes must read differently: an incapable model must not be
	// told its Word file "isn't PDF" (the format is irrelevant — nothing
	// would ride), and a capable one must not be told its model can't take
	// documents.
	if !strings.Contains(out[0].Content, "当前模型不支持文件输入") ||
		strings.Contains(out[0].Content, "仅支持 PDF") {
		t.Fatalf("incapable-model note must name the model, not the format: %q", out[0].Content)
	}
	out = msgsToLLMWithAttachments(msgs, false, true)
	if strings.Contains(out[0].Content, "当前模型不支持文件输入") {
		t.Fatalf("format-only drop must not claim the model is incapable: %q", out[0].Content)
	}
}
