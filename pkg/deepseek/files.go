// files.go — the Files API (POST /files) + document file parts on chat
// messages, part of the 2026-11 tool/API family
// (docs/prd/feature-deepseek-app-api.md §1.2 / §3, card F1).
//
// The upload is the parse: POSTing a document with a recognition
// purpose parses it server-side BEFORE the id comes back — a returned
// FileObject.ID means the document is already queryable. Parsing bills
// once per page at upload (pricing lands with F3's router, not here).
//
// Same key / base URL as chat; same retryCall policy as ASR (transient
// 5xx / 429 / transport retry once, other 4xx fail fast). Second
// non-JSON transport in the package (after ASR) for the same reason:
// multipart bodies are rebuilt per attempt, keeping retries identical
// in content — only the boundary differs, which servers treat as
// framing, not content.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// endpointFiles is the Files API path (api-docs.deepseek.com
// /guides/files_api). Unversioned like chat's /chat/completions — the
// /v1 prefix belongs to the tools family only.
const endpointFiles = "/files"

// Published constraints (PRD §1.2): documents up to 100 MiB / 1000
// pages; upload and parse each time out server-side at 120 s (well
// inside Client's 5-minute HTTP timeout). Per-account / per-request
// document counts (15 concurrent, ≤20 per request) are server-side —
// a multi-user race makes a client-side check a false authority.
const FileMaxBytes = 100 << 20

// Upload purposes. user_data is for images / plain text / tables /
// ebooks and bills nothing, but 400s on PDF/Word/PPT — the
// extension×purpose routing decision lives in F3's router, this client
// only validates that the purpose is one of the three documented
// values. text_recognition is the seek default (PRD D-F1); rich buys
// figures at extra input tokens for the same parse price.
const (
	PurposeUserData              = "user_data"
	PurposeTextRecognition       = "text_recognition"
	PurposeRichLayoutRecognition = "rich_layout_recognition"
)

// validPurposes is the closed set the API documents. A purpose outside
// it is a certain 400 — one of the few things the client can judge
// with certainty before spending an upload.
var validPurposes = map[string]bool{
	PurposeUserData:              true,
	PurposeTextRecognition:       true,
	PurposeRichLayoutRecognition: true,
}

// FilePart is one document attached to a user message — the file
// sibling of ImagePart (feature-deepseek-app-api §3.1).
//
//   - Asset: the durable session/persistence form — a content-addressed
//     name under the project assets dir (same library as images; the
//     server cannot re-download originals, so the local copy is the
//     only source of truth for re-uploads). The API never sees it.
//   - FileID: the sendable form — a Files API id ("file-api-…") that
//     ResolveFiles (F4) materialises just before the request, with
//     lazy upload / expiry re-upload from the asset library.
//   - ExpiresAt / Purpose: the upload's lifecycle and parse mode,
//     recorded next to the id so the send path can tell "still live"
//     from "re-upload from the asset" WITHOUT a sidecar cache — the
//     transcript stays the only source of truth (D-F3's "fully
//     stateless" option; the PRD's filerefs.json was the alternative).
//     ExpiresAt is unix seconds, 0/absent = no expiry (the API
//     documents 1 h – 30 days or permanent). Both fields are session-
//     persistence only: toWire emits file_id alone.
//   - Pages / PagesEstimated: the parse-completeness observation
//     record (card c_y2akdi). Pages is the page count the upload
//     claimed — server-reported when page_count came back, else the
//     submit-time size estimate (pricing.EstimateParsePages) — and
//     PagesEstimated marks which. The live API returns NEITHER the
//     count nor any read-back endpoint (GET /files/{id} is
//     metadata-only, parse results are not downloadable), so this pair
//     plus the file id is the client's whole assertion material for
//     post-hoc reconciliation against actual context usage (the
//     same-PDF-twice parse gap, docs/pitfalls.md). Persistence only:
//     toWire emits file_id alone; resolve-time re-uploads keep the
//     original submit-time claim (the durable record), never mint a
//     new one.
type FilePart struct {
	Asset     string `json:"asset,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // unix seconds; 0 = permanent / unknown
	Purpose   string `json:"purpose,omitempty"`    // Purpose* const; "" = text_recognition (legacy parts)

	Pages          int  `json:"pages,omitempty"`     // pages the upload claimed (reported or estimated)
	PagesEstimated bool `json:"pages_est,omitempty"` // true = Pages is the size estimate, not a server reading
}

// FileObject is the POST /files 200 body. Structurally it models the
// documented file object, so PageCount (the ¥1.5/千页 billing basis)
// and ExpiresAt (lifecycle is 1 h – 30 days or permanent) live here —
// but the LIVE API sends NEITHER (checked 2026-11-18/19; see
// docs/pitfalls.md "POST /files 响应里没有 page_count"). They stay
// declared so a response that does carry them decodes, and so a future
// server-side addition needs no client change; callers must treat 0 as
// "not reported" and fall back (fileroute bills
// pricing.EstimateParsePages and flags the reading as an estimate).
// GET /files and GET /files/{id} return this same field set — no
// endpoint reports a page count for a Files-API document.
type FileObject struct {
	ID        string `json:"id"` // "file-api-…" form
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"` // Unix seconds
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`

	PageCount int   `json:"page_count,omitempty"` // documented, never actually sent — 0 means "not reported"
	ExpiresAt int64 `json:"expires_at,omitempty"` // Unix seconds; 0/absent = permanent
}

// FileUploadRequest is one document to upload and parse.
type FileUploadRequest struct {
	// Filename rides the multipart file part's filename attribute — the
	// server's extension sniffing keys off it, so it must carry a real
	// extension (.pdf/.doc(x)/.ppt(x) for recognition purposes).
	Filename string
	// Data is the complete file body.
	Data []byte
	// Purpose is one of the Purpose* constants.
	Purpose string
}

// UploadFile POSTs one document and returns the parsed FileObject.
// Transient failures (5xx, 429, transport) retry once via the same
// retryCall policy as Chat / ASR; other 4xx (bad purpose, oversize,
// user_data'd PDF, auth) fail fast. Only a 2xx counts as billable —
// "a parse ran", never "an upload was attempted". The page count to
// bill is NOT in this response (live behaviour, see FileObject's doc);
// the caller derives it (server reading when present, flagged size
// estimate otherwise).
func (c *Client) UploadFile(ctx context.Context, req *FileUploadRequest) (*FileObject, error) {
	if req == nil {
		return nil, errors.New("deepseek: nil file upload request")
	}
	if req.Filename == "" {
		return nil, errors.New("deepseek: file upload filename is empty")
	}
	if len(req.Data) == 0 {
		return nil, errors.New("deepseek: file upload data is empty")
	}
	if len(req.Data) > FileMaxBytes {
		return nil, fmt.Errorf("deepseek: file upload must be ≤%d MiB, got %d bytes", FileMaxBytes>>20, len(req.Data))
	}
	if !validPurposes[req.Purpose] {
		return nil, fmt.Errorf("deepseek: file upload purpose %q is not one of %s/%s/%s",
			req.Purpose, PurposeUserData, PurposeTextRecognition, PurposeRichLayoutRecognition)
	}

	return retryCall(ctx, func() (*FileObject, error) {
		resp, err := c.doUploadFile(ctx, req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("deepseek: read file upload response: %w", err)
		}
		if resp.StatusCode/100 != 2 {
			return nil, parseAPIError(resp.StatusCode, body)
		}

		out := &FileObject{}
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("deepseek: decode file upload response: %w", err)
		}
		return out, nil
	})
}

// doUploadFile POSTs the document as a two-field multipart form. FIELD
// ORDER IS LOAD-BEARING: the API rejects requests whose `purpose`
// part comes after `file` (PRD §1.2), and multipart.Writer emits parts
// in write order — purpose first, always. Pinned by
// TestUploadFile_MultipartFieldOrder, which walks the raw part stream
// (r.FormFile can't see order). The body is rebuilt per attempt, so
// retryCall retries are identical in content.
func (c *Client) doUploadFile(ctx context.Context, req *FileUploadRequest) (*http.Response, error) {
	if c.apiKey == "" {
		return nil, errors.New("deepseek: missing api key (set DEEPSEEK_API_KEY)")
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("purpose", req.Purpose); err != nil {
		return nil, fmt.Errorf("deepseek: build file upload multipart: %w", err)
	}
	fw, err := w.CreateFormFile("file", req.Filename)
	if err != nil {
		return nil, fmt.Errorf("deepseek: build file upload multipart: %w", err)
	}
	if _, err := fw.Write(req.Data); err != nil {
		return nil, fmt.Errorf("deepseek: build file upload multipart: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("deepseek: build file upload multipart: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpointFiles, &buf)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("deepseek: http: %w", err)
	}
	return resp, nil
}
