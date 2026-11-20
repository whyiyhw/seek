// Package fileroute — the submit-time router for document references
// (feature-deepseek-app-api §3.3, card F3). The structural mirror of
// cmd/seek's visionRouter, one station over: documents copy into the
// project assets store (copy-at-submit) AND upload eagerly — the Files
// API parses at upload, so by the time the submit returns, the
// file_id, the page count, and the parse fee are all settled
// (acceptance: "提交 @report.pdf → 标记 + file_id 就位").
//
// One Router is shared by all three input entries (TUI submit,
// print/-p/pipe, ACP) so the routing decision and the marker text are
// identical wherever a document enters. Per reference:
//
//   - DeepSeek lane (client set): stat/size gate → copy into the asset
//     library (the Files API cannot re-download originals — the local
//     copy is the ONLY durable source for re-uploads) → upload with
//     the purpose the reference chose (text default, @rich: upgrade)
//     → record the parse fee off the returned page count, or the
//     flagged size estimate when the server omits it (c_y2akdi) →
//     attach a FilePart carrying the asset name, the live file_id,
//     and the claimed pages.
//   - any other lane: an in-band note, nothing uploaded.
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
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/internal/pricing"
	"github.com/whyiyhw/seek/pkg/deepseek"
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
	client    Uploader // nil → file input unavailable on this lane, degrade to notes
	assetsDir string   // "" → asset store unavailable, degrade to notes
	cost      CostSink // nil → parse fee not recorded
	maxBytes  int64    // per-document size gate; deepseek.FileMaxBytes by default
}

// New builds the router. client is nil on non-DeepSeek lanes (GLM /
// opencode — the Files API is DeepSeek-only); cost may be nil.
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
// so one replay styling family covers both). model is accepted for
// hook-signature symmetry with the image router but not gated on:
// file input is an account-level capability with no per-model
// allow-list documented (unlike images' vision allow-list).
func (r Router) Route(ctx context.Context, model, text string) (string, []deepseek.FilePart) {
	_ = model // symmetric signature only — see doc comment
	refs := filerefs.DetectRefs(text)
	if len(refs) == 0 {
		return text, nil
	}
	var parts []deepseek.FilePart
	blocks := make([]string, 0, len(refs))
	for _, ref := range refs {
		block, part := r.routeOne(ctx, ref)
		blocks = append(blocks, block)
		if part.FileID != "" {
			parts = append(parts, part)
		}
	}
	return text + "\n\n" + strings.Join(blocks, "\n"), parts
}

// routeOne walks one reference through the gates. Every exit but the
// last is a note + empty part.
func (r Router) routeOne(ctx context.Context, ref filerefs.Ref) (string, deepseek.FilePart) {
	name := filepath.Base(ref.Path)
	if r.client == nil {
		return laneNote(name), deepseek.FilePart{}
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
	// Billing + observation (card c_y2akdi): the live API returns
	// page_count in neither the response nor any read-back endpoint
	// (GET /files/{id} is metadata-only — docs/pitfalls.md), so a
	// missing count falls back to the size estimate instead of the
	// old flat $0. The fee still keys off THIS upload only (failed
	// uploads billed nothing above); every estimated reading is
	// flagged on the marker and the FilePart, never passed off as
	// exact.
	pages, est := fo.PageCount, false
	if pages <= 0 {
		pages = pricing.EstimateParsePages(int64(len(data)))
		est = true
	}
	if r.cost != nil {
		r.cost.RecordFlatCost(pricing.ParseCost(pages))
	}
	return attachMarker(name, purpose, fo, pages, est), deepseek.FilePart{
		Asset:          asset,
		FileID:         fo.ID,
		ExpiresAt:      fo.ExpiresAt, // send path's expiry-reupload trigger
		Purpose:        purpose,      // faithful re-upload of a @rich doc
		Pages:          pages,        // claimed pages: reported, or estimated
		PagesEstimated: est,
	}
}

func laneNote(name string) string {
	return fmt.Sprintf("[file: %s — 文件输入需 DeepSeek 车道（--provider=deepseek）]", name)
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
// document: the purpose it parsed at, the pages the upload claimed,
// and the live file_id — the marker is what makes the transcript
// self-describing when the part is dropped (lane switch, resume on
// another machine). pages/est come from the caller's exact-or-
// estimated decision (the server reading when page_count came back,
// else the flagged size estimate) — the marker ALWAYS quantifies the
// pages now, because it doubles as the parse-completeness
// observation record: file id + claimed pages is the client's whole
// reconciliation material against actual context usage (card c_y2akdi;
// the API offers no read-back of either). Family prefix "[file: " is
// wire format — pinned by TestMarker_Family; variants may only
// extend AFTER the prefix (AGENTS.md wire-format rule).
func attachMarker(name, purpose string, fo *deepseek.FileObject, pages int, est bool) string {
	p := "text"
	if purpose == deepseek.PurposeRichLayoutRecognition {
		p = "rich"
	}
	pg := fmt.Sprintf(" · %d pages", pages)
	if est {
		pg = fmt.Sprintf(" · ~%d pages·估", pages)
	}
	return fmt.Sprintf("[file: %s — uploaded · %s%s · %s]", name, p, pg, fo.ID)
}
