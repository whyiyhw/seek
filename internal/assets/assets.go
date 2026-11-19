// Package assets — content-addressed asset store under
// ~/.seek/projects/<id>/assets/ (feature-vision D5; documents joined
// via StoreDoc, feature-deepseek-app-api §3.1).
//
// Copy-at-submit: the submit-time router copies each detected image
// here BEFORE the message is sent, so a cancelled send still leaves
// the session a durable copy, and `-resume` can re-materialise data
// URLs for every prior image. The session JSONL references assets by
// NAME only — lines stay small no matter how big the screenshots are.
//
// Design constraints:
//
//   - Content-addressed names (sha256[:12]+ext): identical images
//     dedup for free, and a name is safe to echo into transcripts.
//   - Atomic write (tmp + rename, mirrors plan/artifact.go): a
//     partial write can never leave a truncated asset behind.
//   - Name guard: assets are flat base names. Anything with a path
//     separator or ".." is rejected — asset names round-trip through
//     session files and must never traverse.
//   - Size gate mirrors the API's single-image cap (32 MiB for
//     base64/URL images) so a too-big image degrades at ROUTE time
//     (in-band note) instead of failing the whole request at send time.
//     Documents gate at their own 100 MiB cap (MaxDocBytes).
package assets

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxAssetBytes caps one stored image at the API's base64/URL limit.
// Var (not const) so tests can exercise the gate cheaply.
var MaxAssetBytes = 32 << 20

var allowedExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// docExt is the document extension table (feature-deepseek-app-api
// D-F2: PDF/Word/PPT, the Files API's parseable set). A set, not a
// mime map: documents upload as raw multipart bytes, never as a data
// URL, so there is nothing for a mime to label.
var docExt = map[string]bool{
	".pdf": true, ".doc": true, ".docx": true,
	".ppt": true, ".pptx": true,
}

// MaxDocBytes caps one stored document, mirroring the Files API's
// 100 MiB document limit (same value as pkg/deepseek.FileMaxBytes,
// restated so this package stays stdlib-only). Var for the same test
// reason as MaxAssetBytes. The Files API cannot re-download originals:
// the local copy is the ONLY source of truth for re-uploads, which is
// why documents store at all (feature-deepseek-app-api §3.1 持久层).
var MaxDocBytes = 100 << 20

// Store writes data under dir under its content-addressed name and
// returns that name. Existing content is left untouched (dedup).
func Store(dir, origName string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(origName))
	if _, ok := allowedExt[ext]; !ok {
		return "", fmt.Errorf("assets: unsupported image extension %q", ext)
	}
	if len(data) > MaxAssetBytes {
		return "", fmt.Errorf("assets: %d bytes exceeds %d MiB cap", len(data), MaxAssetBytes>>20)
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:6]) + ext // 12 hex chars
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return name, nil // identical content already stored
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return name, nil
}

// StoreFile reads src and hands it to Store.
func StoreFile(dir, src string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	return Store(dir, filepath.Base(src), data)
}

// StoreDoc writes a document under dir under its content-addressed
// name and returns that name — the document sibling of Store, SAME
// library (feature-deepseek-app-api §3.1 同库复用), same atomic
// write, same dedup. Extension gate is the doc table; size gate the
// document cap.
func StoreDoc(dir, origName string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(origName))
	if !docExt[ext] {
		return "", fmt.Errorf("assets: unsupported document extension %q", ext)
	}
	if len(data) > MaxDocBytes {
		return "", fmt.Errorf("assets: %d bytes exceeds %d MiB document cap", len(data), MaxDocBytes>>20)
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:6]) + ext // 12 hex chars
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return name, nil // identical content already stored
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return name, nil
}

// Path returns the absolute path of asset within dir. The name must be
// a flat base name — session files carry asset names and must never
// become traversal vectors.
func Path(dir, asset string) (string, error) {
	// ContainsAny catches both separators EXPLICITLY: filepath.Base
	// only splits on the HOST OS's separator, so on unix `a\b.png`
	// sails through Base unchanged — but the same session JSONL
	// opened on Windows would traverse. Asset names are portable
	// wire data; the guard must reject both separators on every OS.
	if asset == "" || asset == "." || asset != filepath.Base(asset) ||
		strings.Contains(asset, "..") || strings.ContainsAny(asset, `/\`) {
		return "", fmt.Errorf("assets: invalid asset name %q", asset)
	}
	return filepath.Join(dir, asset), nil
}

// Load reads the asset and returns it as a data: URL ready for the
// image_url content part. MIME comes from the extension; the API
// sniffs actual content anyway, so a mislabelled-but-supported file
// still renders.
func Load(dir, asset string) (string, error) {
	p, err := Path(dir, asset)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if len(data) > MaxAssetBytes {
		return "", fmt.Errorf("assets: %d bytes exceeds cap", len(data))
	}
	mime := allowedExt[strings.ToLower(filepath.Ext(asset))]
	if mime == "" {
		mime = "application/octet-stream"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// LoadDoc reads a stored document back as raw bytes — the Load sibling
// for the Files API's send-time re-upload
// (deepseek.ResolveFiles' FileLoader; feature-deepseek-app-api F4).
// Documents never become data URLs (they upload as multipart bodies),
// so there is no mime to resolve. Same name guard and size cap as Load;
// the cap mirrors the upload cap so a corrupted/oversized asset
// degrades at load, not mid-request.
func LoadDoc(dir, asset string) ([]byte, error) {
	p, err := Path(dir, asset)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDocBytes {
		return nil, fmt.Errorf("assets: %d bytes exceeds %d MiB document cap", len(data), MaxDocBytes>>20)
	}
	return data, nil
}
