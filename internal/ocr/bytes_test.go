package ocr

import (
	"context"
	"strings"
	"testing"
)

func TestRunBytes_OCRsInMemoryImage(t *testing.T) {
	// echoCmd ignores the image content and prints fixed text, so any bytes
	// work as a stand-in (the real engine is exercised by the e2e test).
	out, err := RunBytes(context.Background(), []byte("\x89PNG fake"), ".png", Options{Command: echoCmd("HELLO FROM BYTES")})
	if err != nil {
		t.Fatal(err)
	}
	if out != "HELLO FROM BYTES" {
		t.Fatalf("out = %q", out)
	}
}

func TestRunBytes_EmptyDataErrors(t *testing.T) {
	if _, err := RunBytes(context.Background(), nil, ".png", Options{Command: echoCmd("x")}); err == nil {
		t.Fatal("empty image data should error")
	}
}

func TestRunBytes_DefaultExt(t *testing.T) {
	// ext "" falls back to .png — just verify it doesn't error / round-trips.
	if _, err := RunBytes(context.Background(), []byte("data"), "", Options{Command: echoCmd("ok")}); err != nil {
		t.Fatal(err)
	}
}

func TestExpandImageData_FormatsLikeFileBlock(t *testing.T) {
	out := ExpandImageData(context.Background(), "pasted-image", []byte("img"), ".png", Options{Command: echoCmd("INVOICE #42")})
	if !strings.Contains(out, "[image: pasted-image — OCR]") ||
		!strings.Contains(out, "INVOICE #42") ||
		!strings.Contains(out, "[/image: pasted-image]") {
		t.Fatalf("block format = %q", out)
	}
}

func TestExpandImageData_NoEngineDegrades(t *testing.T) {
	// No Command/Helper/Provision → errNoEngine → in-band hint, never panics.
	out := ExpandImageData(context.Background(), "x", []byte("img"), ".png", Options{})
	if !strings.Contains(out, "OCR 未启用") {
		t.Fatalf("no-engine should produce the build-helper hint, got %q", out)
	}
}

func TestExpandImageData_EmptyDataDegrades(t *testing.T) {
	// Empty data → RunBytes errors → in-band failure note (never panics).
	out := ExpandImageData(context.Background(), "x", nil, ".png", Options{Command: echoCmd("ok")})
	if !strings.Contains(out, "OCR 失败") {
		t.Fatalf("empty data should produce a failure note, got %q", out)
	}
}
