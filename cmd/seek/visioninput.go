// visioninput.go — the submit-time router for natively-attached
// images (feature-vision D3/D4, docs/prd/feature-vision.md).
//
// One instance is shared by all three input entries (TUI submit,
// print/-p/pipe, ACP paste) so the capability decision and the marker
// text are identical wherever an image enters. Behaviour:
//
//   - active model is a vision model → the image is copied into the
//     project assets store (copy-at-submit, D5) and attached as an
//     Asset-only ImagePart; the send path materialises the data URL.
//   - any other model → an in-band note suggesting /model, and the
//     image is NOT sent (the API would 400).
//   - every failure mode (unreadable file, oversize, store failure) is
//     an in-band note — the router NEVER errors and never blocks the
//     prompt (never-error philosophy inherited from the 柱 Q pipeline).
package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"  // register stdlib decoders for marker dimensions
	_ "image/jpeg" // (webp has no stdlib decoder → size-only marker)
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/whyiyhw/seek/internal/assets"
	"github.com/whyiyhw/seek/internal/imgrefs"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

type visionRouter struct {
	assetsDir string // "" → attachment disabled, degrades to notes
}

func newVisionRouter(projectAbs string) visionRouter {
	dir, err := paths.ProjectAssets(projectAbs)
	if err != nil {
		return visionRouter{} // assets unavailable → notes, never fatal
	}
	return visionRouter{assetsDir: dir}
}

// route scans text for stat-gated image-file references and decides
// each one's fate. The original text is preserved; one marker block is
// appended per image (mirrors the 柱 Q injection shape so replay
// styling covers both generations).
func (r visionRouter) route(model, text string) (string, []deepseek.ImagePart) {
	refs := imgrefs.Detect(text)
	if len(refs) == 0 {
		return text, nil
	}
	var parts []deepseek.ImagePart
	blocks := make([]string, 0, len(refs))
	for _, p := range refs {
		block, part := r.routeOne(model, filepath.Base(p), p)
		blocks = append(blocks, block)
		if part.Asset != "" {
			parts = append(parts, part)
		}
	}
	return text + "\n\n" + strings.Join(blocks, "\n"), parts
}

func (r visionRouter) routeOne(model, name, path string) (string, deepseek.ImagePart) {
	if !deepseek.IsVisionModel(model) {
		return switchModelNote(name), deepseek.ImagePart{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("[image: %s — 读取失败: %v]", name, err), deepseek.ImagePart{}
	}
	return r.routeBytes(model, name, data, "")
}

// routeBytes attaches in-memory image bytes (the ACP paste path).
// display is the human-readable name; ext is appended to the stored
// name when display carries no extension.
func (r visionRouter) routeBytes(model, display string, data []byte, ext string) (string, deepseek.ImagePart) {
	if !deepseek.IsVisionModel(model) {
		return switchModelNote(display), deepseek.ImagePart{}
	}
	if r.assetsDir == "" {
		return fmt.Sprintf("[image: %s — 资产库不可用]", display), deepseek.ImagePart{}
	}
	if len(data) > assets.MaxAssetBytes {
		return fmt.Sprintf("[image: %s — 超过单图 %d MiB 上限]", display, assets.MaxAssetBytes>>20), deepseek.ImagePart{}
	}
	storeName := display
	if filepath.Ext(display) == "" && ext != "" {
		storeName = display + ext
	}
	asset, err := assets.Store(r.assetsDir, storeName, data)
	if err != nil {
		return fmt.Sprintf("[image: %s — 入库失败: %v]", display, err), deepseek.ImagePart{}
	}
	return attachMarker(display, data), deepseek.ImagePart{Asset: asset}
}

func switchModelNote(name string) string {
	return fmt.Sprintf("[image: %s — 当前模型不支持图片输入，/model %s 切换]", name, deepseek.ModelV4FlashVisionExp)
}

// attachMarker is the transcript self-description for a natively
// attached image: dimensions when a stdlib decoder can read the header
// (webp degrades to size-only), plus a human size. Family prefix
// "[image: " is wire format — pinned by TestVisionMarker_Family.
func attachMarker(name string, data []byte) string {
	dims := ""
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width > 0 && cfg.Height > 0 {
		dims = fmt.Sprintf(" · %dx%d", cfg.Width, cfg.Height)
	}
	return fmt.Sprintf("[image: %s — attached natively%s · %s]", name, dims, humanBytes(len(data)))
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}
