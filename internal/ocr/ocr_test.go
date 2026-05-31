package ocr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// echoCmd builds a fake OCR command that prints fixed text (ignoring the
// appended image-path arg, which lands as $0 under `sh -c`).
func echoCmd(text string) []string { return []string{"sh", "-c", "printf '%s' " + shquote(text)} }
func shquote(s string) string      { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func writeImg(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("not-really-an-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetect_StatGated_NoFalsePositive(t *testing.T) {
	// A ".png" that doesn't exist on disk must NOT be detected — this is
	// the guard against OCR-ing code/URLs that merely mention an image.
	text := "the icon is at assets/logo.png and see https://x.com/a.jpg in code"
	if got := DetectImageRefs(text); len(got) != 0 {
		t.Fatalf("non-existent image paths must not be detected, got %v", got)
	}
}

func TestDetect_RealFile(t *testing.T) {
	img := writeImg(t, "shot.png")
	text := "fix this " + img + " please"
	got := DetectImageRefs(text)
	if len(got) != 1 || got[0] != img {
		t.Fatalf("got %v, want [%s]", got, img)
	}
}

func TestDetect_AtPrefixAndPunctuation(t *testing.T) {
	img := writeImg(t, "err.png")
	text := "look at (@" + img + ")," // wrapped + trailing comma + @ prefix
	got := DetectImageRefs(text)
	if len(got) != 1 || got[0] != img {
		t.Fatalf("should strip @/wrappers/punct, got %v want [%s]", got, img)
	}
}

func TestDetect_Dedup(t *testing.T) {
	img := writeImg(t, "a.png")
	got := DetectImageRefs(img + " and again " + img)
	if len(got) != 1 {
		t.Fatalf("duplicate refs should dedup, got %v", got)
	}
}

func TestRun_FakeCommand(t *testing.T) {
	img := writeImg(t, "x.png")
	out, err := Run(context.Background(), img, Options{Command: echoCmd("hello world")})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("out = %q", out)
	}
}

func TestRun_NoEngine(t *testing.T) {
	_, err := Run(context.Background(), "x.png", Options{})
	if err == nil {
		t.Fatal("no command + no helper should error (→ graceful degrade upstream)")
	}
}

func TestRun_Timeout(t *testing.T) {
	img := writeImg(t, "x.png")
	_, err := Run(context.Background(), img, Options{Command: []string{"sh", "-c", "sleep 5"}, Timeout: 50 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestExpand_InjectsBlock(t *testing.T) {
	img := writeImg(t, "doc.png")
	out := Expand(context.Background(), "what does "+img+" say?", Options{Command: echoCmd("INVOICE #42")})
	if !strings.Contains(out, "[image: doc.png — OCR]") || !strings.Contains(out, "INVOICE #42") || !strings.Contains(out, "[/image: doc.png]") {
		t.Fatalf("missing OCR block:\n%s", out)
	}
	if !strings.Contains(out, "what does") {
		t.Fatal("original text must be preserved")
	}
}

func TestExpand_EmptyOCR(t *testing.T) {
	img := writeImg(t, "blank.png")
	out := Expand(context.Background(), img, Options{Command: echoCmd("")})
	if !strings.Contains(out, "未识别到文字") {
		t.Fatalf("empty OCR should note non-text image:\n%s", out)
	}
}

func TestExpand_NoEngine_Graceful(t *testing.T) {
	img := writeImg(t, "z.png")
	out := Expand(context.Background(), img, Options{}) // no engine
	if !strings.Contains(out, "OCR 未启用") {
		t.Fatalf("missing engine should degrade with a hint, not crash:\n%s", out)
	}
}

func TestExpand_NoImages_Unchanged(t *testing.T) {
	text := "just a normal question about main.go"
	if out := Expand(context.Background(), text, Options{Command: echoCmd("x")}); out != text {
		t.Fatalf("text without image refs must be unchanged, got %q", out)
	}
}
