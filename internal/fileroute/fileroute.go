// Package fileroute — the submit-time router for document references
// (feature-deepseek-app-api §3.3, card F3). The structural mirror of
// cmd/seek's visionRouter, one station over. One Router is shared by
// all three input entries (TUI submit, print/-p/pipe, ACP) so the
// routing decision and the marker text are identical wherever a
// document enters. Per reference:
//
//   - second-tier lane (client nil) on a model without file capability
//     (modelcap.Files, the single authority across lanes): an in-band
//     switch-model note, nothing stored or uploaded.
//   - DeepSeek lane (client set): copy into the asset library (the
//     Files API cannot re-download originals — the local copy is the
//     ONLY durable source for re-uploads) → upload eagerly with the
//     purpose the reference chose (text default, @rich: upgrade —
//     the Files API parses at upload, so by the time the submit
//     returns, the file_id, the page count, and the parse fee are all
//     settled) → record the parse fee off the RETURNED page count →
//     attach a FilePart carrying both the asset name and the live
//     file_id.
//   - second-tier lane (client nil, model capable): store into the
//     asset library and attach an Asset-only part — the send path
//     materialises the bytes as a data: URL (deepseek.ResolveFileData
//     → llm.Message.Files). PDF only: it is the one document format
//     every second-tier wire format accepts (pkg/llm's transport
//     set); Word/PPT degrade to an in-band note at this station
//     already, so the transcript never gains an "attached" marker a
//     later send would have to walk back.
//   - every failure mode (unreadable file, oversize, store failure,
//     upload failure) is an in-band note — the router NEVER errors
//     and never blocks the prompt (never-error philosophy inherited
//     from the vision router / 柱 Q pipeline). A failed upload still
//     leaves the asset stored, so F4's lazy re-upload can heal it.
//
// Deliberate simplification: each submit uploads its references anew —
// a doc referenced twice across two submits parses (and bills the
// per-page parse fee) twice. Re-REFERENCING one file_id in later turns
// does not re-charge the parse (PRD §1.2: parsing bills once at upload;
// chat references then cost only input tokens). The send path owns the
// other half: deepseek.ResolveFiles heals a missing/expired id by
// re-uploading from this same asset library, and the FilePart records
// expires_at + purpose so that decision needs no sidecar cache.
package fileroute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/assets"
	"github.com/whyiyhw/seek/internal/filerefs"
	"github.com/whyiyhw/seek/internal/modelcap"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
	"github.com/whyiyhw/seek/pkg/llm"
)

// Uploader is the Files-API surface the router needs — the one-method
// subset of *deepseek.Client, narrow so tests fake it without HTTP.
type Uploader interface {
	UploadFile(ctx context.Context, req *deepseek.FileUploadRequest) (*deepseek.FileObject, error)
}

// Compile-time: the real client satisfies Uploader (cmd/seek passes
// its *deepseek.Client straight in).
var _ Uploader = (*deepseek.Client)(nil)

// CostSink receives the per-upload parse fee. *cache.Tracker satisfies
// it (RecordFlatCost); nil disables reporting. Same narrow-local-
// interface shape as the web_search tool's.
type CostSink interface {
	RecordFlatCost(usd float64)
}

// Router routes document references at submit time.
type Router struct {
	client    Uploader // nil → second-tier lane (no Files API): store-and-attach, capability-gated by modelcap
	assetsDir string   // "" → asset store unavailable, degrade to notes
	cost      CostSink // nil → parse fee not recorded
	maxBytes  int64    // per-document size gate; deepseek.FileMaxBytes by default
}

// New builds the router. client is nil on non-DeepSeek lanes (GLM /
// opencode — the Files API is DeepSeek-only), where capable models take
// the store-and-attach path instead; cost may be nil.
func New(client Uploader, projectAbs string, cost CostSink) Router {
	dir, err := paths.ProjectAssets(projectAbs)
	if err != nil {
		dir = "" // assets unavailable → notes, never fatal
	}
	return Router{client: client, assetsDir: dir, cost: cost, maxBytes: deepseek.FileMaxBytes}
}

// Route scans text for stat-gated document references and decides each
// one's fate. The original text is preserved; one marker block is
// appended per reference (mirrors the vision router's injection shape
// so one replay styling family covers both). modelcap.Files gates the
// SECOND-tier lanes only (see routeOne): there the model's own wire
// format is what can or cannot carry a document part. The DeepSeek
// lane answers to the Files API instead, which is account-level and
// format-agnostic (PDF/Word/PPT all parse server-side).
func (r Router) Route(ctx context.Context, model, text string) (string, []deepseek.FilePart) {
	refs := filerefs.DetectRefs(text)
	if len(refs) == 0 {
		return text, nil
	}
	var parts []deepseek.FilePart
	blocks := make([]string, 0, len(refs))
	for _, ref := range refs {
		block, part := r.routeOne(ctx, model, ref)
		blocks = append(blocks, block)
		if part.FileID != "" || part.Asset != "" {
			parts = append(parts, part)
		}
	}
	return text + "\n\n" + strings.Join(blocks, "\n"), parts
}

// routeOne walks one reference through the gates. Every exit but the
// last two is a note + empty part.
//
// The capability gate only applies to SECOND-tier lanes (client nil):
// there the model decides whether its own wire format carries document
// parts, so modelcap.Files — the static table + catalogue grants — is
// the authority. On the DeepSeek lane (client set) file input is an
// ACCOUNT-level capability (the Files API parses server-side and the
// chat part is just a file_id), so every model id on that lane can take
// a document; gating it on a per-id table would re-grow the vision
// allow-list bug that silently denied working models (deepseek-v4-pro
// among them — see docs/pitfalls.md).
func (r Router) routeOne(ctx context.Context, model string, ref filerefs.Ref) (string, deepseek.FilePart) {
	name := filepath.Base(ref.Path)
	if r.client == nil && !modelcap.Files(model) {
		return switchModelNote(name), deepseek.FilePart{}
	}
	// Size gate BEFORE reading: a 100 MiB+ document shouldn't be
	// dragged into memory just to be told no.
	info, err := os.Stat(ref.Path)
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("not a regular file")
	}
	if err != nil {
		return readFailNote(name, err), deepseek.FilePart{}
	}
	if info.Size() > r.maxBytes {
		return oversizeNote(name, r.maxBytes), deepseek.FilePart{}
	}
	if r.client != nil {
		return r.routeUpload(ctx, name, ref)
	}
	return r.routeAttach(name, ref.Path)
}

// routeAttach is the second-tier path: store the bytes, attach an
// Asset-only part — no upload, no parse fee. The send path
// (pkg/agent runTurnLLM → deepseek.ResolveFileData) materialises the
// data: URL per request.
func (r Router) routeAttach(name, path string) (string, deepseek.FilePart) {
	if !llm.SendableFileMediaType(deepseek.FileMediaType(name)) {
		return formatNote(name), deepseek.FilePart{}
	}
	if r.assetsDir == "" {
		return fmt.Sprintf("[file: %s — 资产库不可用]", name), deepseek.FilePart{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return readFailNote(name, err), deepseek.FilePart{}
	}
	asset, err := assets.StoreDoc(r.assetsDir, name, data)
	if err != nil {
		return storeFailNote(name, err), deepseek.FilePart{}
	}
	return fmt.Sprintf("[file: %s — attached · %s]", name, humanDocBytes(len(data))), deepseek.FilePart{Asset: asset}
}

// routeUpload is the DeepSeek-lane path (the original F3 flow): copy
// into the asset library, upload eagerly, record the parse fee.
func (r Router) routeUpload(ctx context.Context, name string, ref filerefs.Ref) (string, deepseek.FilePart) {
	data, err := os.ReadFile(ref.Path)
	if err != nil {
		return readFailNote(name, err), deepseek.FilePart{}
	}
	if r.assetsDir == "" {
		return fmt.Sprintf("[file: %s — 资产库不可用]", name), deepseek.FilePart{}
	}
	// Copy-at-submit, before any network: whatever happens to the
	// upload (or the send), the session keeps the only durable copy.
	asset, err := assets.StoreDoc(r.assetsDir, name, data)
	if err != nil {
		return storeFailNote(name, err), deepseek.FilePart{}
	}
	purpose := deepseek.PurposeTextRecognition
	if ref.Rich {
		purpose = deepseek.PurposeRichLayoutRecognition
	}
	fo, err := r.client.UploadFile(ctx, &deepseek.FileUploadRequest{
		Filename: name,
		Data:     data,
		Purpose:  purpose,
	})
	if err != nil {
		// Asset stays stored — F4's lazy re-upload heals from it.
		return uploadFailNote(name, err), deepseek.FilePart{}
	}
	// Parse fee keys off the RETURNED page count — only a successful
	// upload parsed (and 0/absent pages bills nothing).
	if r.cost != nil {
		r.cost.RecordFlatCost(pricing.ParseCost(fo.PageCount))
	}
	return attachMarker(name, purpose, fo), deepseek.FilePart{
		Asset:     asset,
		FileID:    fo.ID,
		ExpiresAt: fo.ExpiresAt, // send path's expiry-reupload trigger
		Purpose:   purpose,      // faithful re-upload of a @rich doc
	}
}

// switchModelNote is the document sibling of the vision router's
// switchModelNote: names no model id (every lane's /model lists its
// own), and the "[file: " family prefix is wire format — pinned by
// TestMarker_Family; variants may only extend AFTER the prefix.
func switchModelNote(name string) string {
	return fmt.Sprintf("[file: %s — 当前模型不支持文件输入，用 /model 选一个支持文件的模型]", name)
}

// formatNote tells the user, at submit time already, that this lane
// carries PDF bytes only (Word/PPT ride on the DeepSeek lane via the
// Files API's server-side parse; no second-tier wire format accepts
// them).
func formatNote(name string) string {
	return fmt.Sprintf("[file: %s — 二线车道仅支持 PDF 附件（Word/PPT 需 DeepSeek 车道解析）]", name)
}

// humanDocBytes renders a size for the attach marker (the sibling of
// the vision router's humanBytes, kept local for the same
// no-cross-import reason).
func humanDocBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func readFailNote(name string, err error) string {
	return fmt.Sprintf("[file: %s — 读取失败: %v]", name, err)
}

func storeFailNote(name string, err error) string {
	return fmt.Sprintf("[file: %s — 入库失败: %v]", name, err)
}

func uploadFailNote(name string, err error) string {
	return fmt.Sprintf("[file: %s — 上传失败: %v]", name, err)
}

func oversizeNote(name string, max int64) string {
	unit := fmt.Sprintf("%d MiB", max>>20)
	if max < 1<<20 { // tests lower the gate below one MiB
		unit = fmt.Sprintf("%d B", max)
	}
	return fmt.Sprintf("[file: %s — 超过单文档 %s 上限]", name, unit)
}

// attachMarker is the transcript self-description for a routed
// document: the purpose it parsed at, the page count the server
// returned (omitted when absent), and the live file_id — the marker is
// what makes the transcript self-describing when the part is dropped
// (lane switch, resume on another machine). Family prefix "[file: "
// is wire format — pinned by TestMarker_Family; variants may only
// extend AFTER the prefix (AGENTS.md wire-format rule).
func attachMarker(name, purpose string, fo *deepseek.FileObject) string {
	p := "text"
	if purpose == deepseek.PurposeRichLayoutRecognition {
		p = "rich"
	}
	pages := ""
	if fo.PageCount > 0 {
		pages = fmt.Sprintf(" · %d pages", fo.PageCount)
	}
	return fmt.Sprintf("[file: %s — uploaded · %s%s · %s]", name, p, pages, fo.ID)
}
