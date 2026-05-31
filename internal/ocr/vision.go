package ocr

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// visionSwiftSrc is the macOS Vision OCR helper, embedded so seek stays a
// single binary: there is nothing extra to ship or install. On first use
// (macOS) it is compiled with swiftc into ~/.seek/cache and reused. This
// is why the documented one-file install (`tar -xz seek`) still gets OCR
// — the helper rides inside the binary, not alongside it. (柱 Q)
//
//go:embed vision_ocr.swift
var visionSwiftSrc []byte

// visionMu serializes the one-time compile so two concurrent images don't
// both invoke swiftc on a cold cache.
var visionMu sync.Mutex

// EnsureVisionHelper returns the path to a compiled Vision OCR helper,
// building it from the embedded Swift source into cacheDir on first use
// and reusing the cached binary thereafter. Requires swiftc (Xcode
// Command Line Tools); without it, returns an error so the caller can
// degrade to an in-band hint rather than failing the turn. The compile
// goes through a temp file + atomic rename so a crashed/interrupted build
// never leaves a half-written binary the next run would trust.
func EnsureVisionHelper(ctx context.Context, cacheDir string) (string, error) {
	out := filepath.Join(cacheDir, "vision_ocr")
	if fresh(out) {
		return out, nil
	}
	visionMu.Lock()
	defer visionMu.Unlock()
	if fresh(out) { // re-check under lock
		return out, nil
	}
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		return "", fmt.Errorf("swiftc not found — install Xcode Command Line Tools (xcode-select --install)")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	srcFile := filepath.Join(cacheDir, "vision_ocr.swift")
	if err := os.WriteFile(srcFile, visionSwiftSrc, 0o644); err != nil {
		return "", err
	}
	tmpOut := out + ".tmp"
	if b, err := exec.CommandContext(ctx, swiftc, "-O", srcFile, "-o", tmpOut).CombinedOutput(); err != nil {
		return "", fmt.Errorf("swiftc compile failed: %v: %s", err, b)
	}
	if err := os.Rename(tmpOut, out); err != nil {
		return "", err
	}
	return out, nil
}

func fresh(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Size() > 0
}
