//go:build darwin

package ocr

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestEnsureVisionHelper_E2E exercises the real compile-from-embedded path
// (柱 Q) end to end: EnsureVisionHelper compiles the embedded Swift into a
// temp cache, then Run OCRs a real image through it. Gated behind
// SEEK_OCR_E2E so CI (no guaranteed image / slow swiftc) skips it; run
// locally with:
//
//	SEEK_OCR_E2E=1 SEEK_OCR_E2E_IMG=/tmp/ocr_smoke.png \
//	  go test -run TestEnsureVisionHelper_E2E ./internal/ocr/
func TestEnsureVisionHelper_E2E(t *testing.T) {
	if os.Getenv("SEEK_OCR_E2E") != "1" {
		t.Skip("set SEEK_OCR_E2E=1 (+ SEEK_OCR_E2E_IMG) to run the real swiftc compile e2e")
	}
	img := os.Getenv("SEEK_OCR_E2E_IMG")
	if img == "" {
		t.Skip("SEEK_OCR_E2E_IMG not set")
	}
	ctx := context.Background()
	helper, err := EnsureVisionHelper(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("EnsureVisionHelper: %v", err)
	}
	out, err := Run(ctx, img, Options{Helper: helper, Languages: "zh-Hans,en-US"})
	if err != nil {
		t.Fatalf("Run via compiled helper: %v", err)
	}
	if !strings.Contains(out, "HELLO") || !strings.Contains(out, "OCR") {
		t.Fatalf("expected the rendered text in OCR output, got: %q", out)
	}
	t.Logf("compiled-from-embedded helper read: %q", strings.TrimSpace(out))
}
