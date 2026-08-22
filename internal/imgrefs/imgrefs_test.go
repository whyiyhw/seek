package imgrefs

import (
	"os"
	"path/filepath"
	"testing"
)

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
	// the guard against attaching code/URLs that merely mention an image.
	text := "the icon is at assets/logo.png and see https://x.com/a.jpg in code"
	if got := Detect(text); len(got) != 0 {
		t.Fatalf("non-existent image paths must not be detected, got %v", got)
	}
}

func TestDetect_RealFile(t *testing.T) {
	img := writeImg(t, "shot.png")
	got := Detect("fix this " + img + " please")
	if len(got) != 1 || got[0] != img {
		t.Fatalf("got %v, want [%s]", got, img)
	}
}

func TestDetect_AtPrefixAndPunctuation(t *testing.T) {
	img := writeImg(t, "err.png")
	got := Detect("look at (@" + img + "),") // wrapped + trailing comma + @ prefix
	if len(got) != 1 || got[0] != img {
		t.Fatalf("should strip @/wrappers/punct, got %v want [%s]", got, img)
	}
}

func TestDetect_Dedup(t *testing.T) {
	img := writeImg(t, "a.png")
	if got := Detect(img + " and again " + img); len(got) != 1 {
		t.Fatalf("duplicate refs should dedup, got %v", got)
	}
}
