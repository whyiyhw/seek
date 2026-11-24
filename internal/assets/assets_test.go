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

// TestStoreDoc mirrors the image Store suite for the document sibling
// (feature-deepseek-app-api F3): same library, same content
// addressing, doc extension table, doc size cap.
func TestStoreDoc(t *testing.T) {
	dir := t.TempDir()
	name1, err := StoreDoc(dir, "report.pdf", []byte("%PDF-1.4"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name1, ".pdf") || len(name1) != len("ab12cd34ef56.pdf") {
		t.Fatalf("name shape: %q", name1)
	}
	// Same content dedups even under a different original name.
	name2, err := StoreDoc(dir, "quarterly.PDF", []byte("%PDF-1.4"))
	if err != nil {
		t.Fatal(err)
	}
	if name1 != name2 {
		t.Fatalf("identical content must dedup: %q vs %q", name1, name2)
	}
	if _, err := StoreDoc(dir, "notes.txt", []byte("hi")); err == nil {
		t.Fatal("non-doc extension must be rejected")
	}
	// Image extension is NOT a document (family separation, both ways).
	if _, err := StoreDoc(dir, "shot.png", []byte("hi")); err == nil {
		t.Fatal("image extension must be rejected by StoreDoc")
	}
	// Size gate at the doc cap.
	old := MaxDocBytes
	MaxDocBytes = 4
	defer func() { MaxDocBytes = old }()
	if _, err := StoreDoc(dir, "big.docx", []byte("too-long")); err == nil {
		t.Fatal("oversize document must error")
	}
}

// TestLoadDoc: the load sibling of StoreDoc — raw bytes back (no data
// URL), the same asset-name traversal guard as Path, and the doc size
// cap enforced on read (a corrupted/oversized asset must degrade at
// load, not mid-request).
func TestLoadDoc(t *testing.T) {
	dir := t.TempDir()
	name, err := StoreDoc(dir, "report.pdf", []byte("%PDF-1.4 body"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadDoc(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "%PDF-1.4 body" {
		t.Fatalf("bytes = %q", got)
	}
	if _, err := LoadDoc(dir, "../escape.pdf"); err == nil {
		t.Fatal("traversal must be rejected")
	}
	if _, err := LoadDoc(dir, "nope.pdf"); err == nil {
		t.Fatal("missing asset must error")
	}
	old := MaxDocBytes
	MaxDocBytes = 4
	defer func() { MaxDocBytes = old }()
	if _, err := LoadDoc(dir, name); err == nil {
		t.Fatal("oversize asset must error on load")
	}
}
