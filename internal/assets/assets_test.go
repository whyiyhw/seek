package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_ContentAddressedAndDedup(t *testing.T) {
	dir := t.TempDir()
	name1, err := Store(dir, "shot.png", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name1, ".png") || len(name1) != len("ab12cd34ef56.png") {
		t.Fatalf("name shape: %q", name1)
	}
	// Same content, different original name → same asset, single file.
	name2, err := Store(dir, "totally-different.jpeg-adjacent.png", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if name1 != name2 {
		t.Fatalf("identical content must dedup: %q vs %q", name1, name2)
	}
	// Different content → different asset.
	name3, err := Store(dir, "shot.png", []byte("world"))
	if err != nil {
		t.Fatal(err)
	}
	if name3 == name1 {
		t.Fatal("different content must not collide")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 {
		t.Fatalf("want 2 stored files, got %d", len(ents))
	}
	// No .tmp leftovers.
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("tmp leftover: %s", e.Name())
		}
	}
}

func TestStore_UnsupportedExt(t *testing.T) {
	if _, err := Store(t.TempDir(), "x.txt", []byte("hi")); err == nil {
		t.Fatal("txt must be rejected")
	}
}

func TestStore_SizeGate(t *testing.T) {
	old := MaxAssetBytes
	MaxAssetBytes = 4
	defer func() { MaxAssetBytes = old }()
	if _, err := Store(t.TempDir(), "x.png", []byte("too-long")); err == nil {
		t.Fatal("oversize must error")
	}
}

func TestLoad_DataURL(t *testing.T) {
	dir := t.TempDir()
	name, err := Store(dir, "a.png", []byte("hi"))
	if err != nil {
		t.Fatal(err)
	}
	url, err := Load(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if url != "data:image/png;base64,"+"aGk=" { // base64("hi")
		t.Fatalf("data URL: %q", url)
	}
}

func TestLoad_Missing(t *testing.T) {
	if _, err := Load(t.TempDir(), "deadbeefdead.png"); err == nil {
		t.Fatal("missing asset must error")
	}
}

// TestPath_TraversalGuard: asset names round-trip through session
// JSONL — they must never become path traversal vectors.
func TestPath_TraversalGuard(t *testing.T) {
	for _, bad := range []string{"", "..", "../x.png", `a\b.png`, "sub/x.png", "."} {
		if _, err := Path("/tmp", bad); err == nil {
			t.Errorf("Path accepted %q", bad)
		}
	}
	p, err := Path("/tmp", "ab12cd34ef56.png")
	if err != nil || p != filepath.Join("/tmp", "ab12cd34ef56.png") {
		t.Errorf("Path plain name: %q %v", p, err)
	}
}

// TestStoreFile: the route-time entry point reads then stores.
func TestStoreFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "in.png")
	if err := os.WriteFile(src, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name, err := StoreFile(dir, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}
