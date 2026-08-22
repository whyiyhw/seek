package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/assets"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// tinyPNG renders a real 3x2 PNG so DecodeConfig-based marker
// dimensions are exercised with genuine bytes.
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for x := 0; x < 3; x++ {
		for y := 0; y < 2; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 80), B: uint8(y * 120), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writePNG(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, tinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestVisionMarker_Family pins the wire-format contract: every marker
// this package produces starts with "[image: " so a single replay
// styling rule (dimImageMarkers) covers all generations. Variants may
// only extend AFTER the family prefix (AGENTS.md wire-format rule).
func TestVisionMarker_Family(t *testing.T) {
	vr := visionRouter{assetsDir: t.TempDir()}
	data := tinyPNG(t)
	markers := []string{
		switchModelNote("x.png"),
		attachMarker("x.png", data),
	}
	block, _ := vr.routeBytes(deepseek.ModelV4FlashVisionExp, "x.png", data, "")
	markers = append(markers, block)
	block, _ = vr.routeBytes(deepseek.ModelV4Flash, "x.png", data, "")
	markers = append(markers, block)
	block, _ = vr.routeBytes("", "x.png", data, "")
	markers = append(markers, block)
	for _, m := range markers {
		if !strings.HasPrefix(m, "[image: ") {
			t.Errorf("marker outside family: %q", m)
		}
	}
}

func TestVisionRouter_Route(t *testing.T) {
	dir := t.TempDir()
	vr := visionRouter{assetsDir: t.TempDir()}
	img := writePNG(t, dir, "shot.png")

	// No image refs → identity.
	got, parts := vr.route(deepseek.ModelV4Flash, "plain text")
	if got != "plain text" || parts != nil {
		t.Fatalf("no-refs: %q %v", got, parts)
	}

	// Non-existent .png mention (stat gate) → no attachment, no marker.
	got, parts = vr.route(deepseek.ModelV4FlashVisionExp, "see assets/logo.png in the code")
	if got != "see assets/logo.png in the code" || parts != nil {
		t.Fatalf("stat gate bypassed: %q %v", got, parts)
	}

	// Vision model → attach + marker with dimensions from the PNG.
	got, parts = vr.route(deepseek.ModelV4FlashVisionExp, "look "+img)
	if !strings.HasPrefix(got, "look "+img) {
		t.Fatalf("original text must be preserved: %q", got)
	}
	if !strings.Contains(got, "[image: shot.png — attached natively · 3x2 · ") {
		t.Fatalf("marker = %q", got)
	}
	if len(parts) != 1 || !strings.HasSuffix(parts[0].Asset, ".png") {
		t.Fatalf("parts = %+v", parts)
	}

	// Non-vision model → switch note, no parts.
	got, parts = vr.route(deepseek.ModelV4Flash, "look "+img)
	if !strings.Contains(got, "[image: shot.png — 当前模型不支持图片输入，/model deepseek-v4-flash-vision-exp 切换]") {
		t.Fatalf("switch note = %q", got)
	}
	if len(parts) != 0 {
		t.Fatalf("parts = %+v", parts)
	}
}

func TestVisionRouter_Degradations(t *testing.T) {
	// assetsDir empty → 资产库不可用 note, never an error.
	vr := visionRouter{}
	block, part := vr.routeBytes(deepseek.ModelV4FlashVisionExp, "x.png", []byte("data"), "")
	if part.Asset != "" || !strings.Contains(block, "资产库不可用") {
		t.Fatalf("no-store degrade: %q %+v", block, part)
	}

	// Oversize → gate at route time (cheap cap override).
	vr = visionRouter{assetsDir: t.TempDir()}
	old := assets.MaxAssetBytes
	assets.MaxAssetBytes = 32
	defer func() { assets.MaxAssetBytes = old }()
	huge := make([]byte, 64)
	block, part = vr.routeBytes(deepseek.ModelV4FlashVisionExp, "x.png", huge, "")
	if part.Asset != "" || !strings.Contains(block, "上限") {
		t.Fatalf("oversize: %q %+v", block, part)
	}

	// Unreadable file → 读取失败 note.
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone.png")
	block, part = vr.routeOne(deepseek.ModelV4FlashVisionExp, "gone.png", gone)
	if part.Asset != "" || !strings.Contains(block, "读取失败") {
		t.Fatalf("unreadable: %q %+v", block, part)
	}
}

// TestVisionRouter_NeverErrors: no input combination may produce a
// panic or an error return — every failure lands in-band.
func TestVisionRouter_NeverErrors(t *testing.T) {
	vr := visionRouter{assetsDir: t.TempDir()}
	for _, model := range []string{"", deepseek.ModelV4Flash, deepseek.ModelV4FlashVisionExp} {
		for _, text := range []string{"", "no refs", "@", "().png", "\x00 weird"} {
			got, parts := vr.route(model, text) // must not panic
			if got == "" && parts != nil {
				t.Errorf("route(%q) empty text with parts", text)
			}
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int]string{512: "512 B", 2048: "2 KiB", 3 << 20: "3.0 MiB"}
	for n, want := range cases {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
