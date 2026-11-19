package main

// Tests for cmd/seek's document-resolver wiring (feature-deepseek-app-api
// F4). The interesting failure lives in the seam, not in pkg/deepseek: a
// nil *deepseek.Client stored into the deepseek.FileUploader interface is
// a NON-nil interface holding a nil pointer, so the resolver's own
// "no upload path" guard would not fire and an upload call would panic.

import (
	"context"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/assets"
	"github.com/whyiyhw/seek/internal/paths"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

func TestFileResolver_NilClientDegradesNotPanics(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	r := fileResolver(t.TempDir(), nil)
	if r == nil {
		t.Fatal("fileResolver returned nil with a valid assets dir")
	}
	// An asset-only part has to be healed or noted — never sent, never a
	// panic. (A typed-nil uploader here would panic inside UploadFile.)
	out := deepseek.ResolveFiles(context.Background(), []deepseek.Message{{
		Role:    deepseek.RoleUser,
		Content: "read",
		Files:   []deepseek.FilePart{{Asset: "ab12.pdf"}},
	}}, r)
	if len(out[0].Files) != 0 {
		t.Fatalf("part must be dropped on a lane without upload: %+v", out[0].Files)
	}
	if !strings.Contains(out[0].Content, "[file: ab12.pdf") {
		t.Fatalf("in-band note missing: %q", out[0].Content)
	}
	// A part with a live id still goes out untouched (no upload needed).
	out = deepseek.ResolveFiles(context.Background(), []deepseek.Message{{
		Role:  deepseek.RoleUser,
		Files: []deepseek.FilePart{{Asset: "ab12.pdf", FileID: "file-api-live"}},
	}}, r)
	if len(out[0].Files) != 1 || out[0].Files[0].FileID != "file-api-live" {
		t.Fatalf("live id must survive a client-less lane: %+v", out[0].Files)
	}
}

// TestFileAssetsLoader_RoundTrip: the FileLoader half is a real read of
// the project asset store (assets.LoadDoc) — store a document with the
// submit-time writer, then load it back through the loader the resolver
// is built with.
func TestFileAssetsLoader_RoundTrip(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	project := t.TempDir()

	dir, err := paths.ProjectAssets(project)
	if err != nil {
		t.Fatal(err)
	}
	name, err := assets.StoreDoc(dir, "report.pdf", []byte("%PDF-1.4 body"))
	if err != nil {
		t.Fatal(err)
	}
	load := fileAssetsLoader(project)
	if load == nil {
		t.Fatal("fileAssetsLoader returned nil")
	}
	got, err := load(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "%PDF-1.4 body" {
		t.Fatalf("bytes = %q", got)
	}
	if _, err := load("../escape.pdf"); err == nil {
		t.Fatal("loader must keep assets.Path's traversal guard")
	}
}
