package ocr

import (
	"context"
	"errors"
	"os"
)

// RunBytes OCRs in-memory image bytes by writing them to a temp file and
// delegating to Run. For image inputs that aren't already on disk — ACP
// image content blocks (M-P.5) and TUI clipboard paste. The temp file is
// removed after OCR. ext is the image extension (".png" default) so the
// engine recognizes the format.
func RunBytes(ctx context.Context, data []byte, ext string, opt Options) (string, error) {
	if len(data) == 0 {
		return "", errors.New("empty image data")
	}
	if ext == "" {
		ext = ".png"
	}
	f, err := os.CreateTemp("", "seek-ocr-*"+ext)
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return Run(ctx, tmp, opt)
}

// ExpandImageData OCRs in-memory image bytes and returns a ready-to-inject
// block in the SAME format as Expand's file-path blocks. Never errors —
// failures become an in-band note (same contract as Expand). name is the
// label shown in the block (e.g. "pasted-image"). This is the entry point
// for non-file image inputs that must keep the柱 Q invariant: only OCR
// TEXT reaches the model, never the image bytes.
func ExpandImageData(ctx context.Context, name string, data []byte, ext string, opt Options) string {
	out, err := RunBytes(ctx, data, ext, opt)
	return formatBlock(name, out, err)
}
