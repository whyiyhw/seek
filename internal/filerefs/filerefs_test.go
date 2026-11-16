package filerefs

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDoc(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("not-really-a-document"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetect_StatGated_NoFalsePositive(t *testing.T) {
	// A ".pdf"/".docx" that doesn't exist on disk must NOT be detected —
	// the guard against attaching code/URLs that merely mention a document.
	cases := []string{
		"see https://example.com/report.pdf for details",
		`if strings.HasSuffix(name, ".pdf") { parse(name) }`,
		"the manual is at docs/manual.docx and slides at ~/deck.pptx",
	}
	for _, text := range cases {
		if got := Detect(text); len(got) != 0 {
			t.Fatalf("non-existent document paths must not be detected (%q), got %v", text, got)
		}
	}
}

func TestDetect_RealFile(t *testing.T) {
	doc := writeDoc(t, "report.pdf")
	got := Detect("summarize this " + doc + " please")
	if len(got) != 1 || got[0] != doc {
		t.Fatalf("got %v, want [%s]", got, doc)
	}
}

func TestDetect_AtPrefixAndPunctuation(t *testing.T) {
	doc := writeDoc(t, "contract.docx")
	got := Detect("look at (@" + doc + "),") // wrapped + trailing comma + @ prefix
	if len(got) != 1 || got[0] != doc {
		t.Fatalf("should strip @/wrappers/punct, got %v want [%s]", got, doc)
	}
}

func TestDetect_Dedup(t *testing.T) {
	doc := writeDoc(t, "a.pdf")
	if got := Detect(doc + " and again " + doc); len(got) != 1 {
		t.Fatalf("duplicate refs should dedup, got %v", got)
	}
}

func TestDetect_AllExtensionsAndCase(t *testing.T) {
	// Every extension in the v1 table fires, case-insensitively.
	for _, name := range []string{"a.pdf", "b.doc", "c.docx", "d.ppt", "e.pptx", "F.PDF"} {
		doc := writeDoc(t, name)
		got := Detect("read " + doc)
		if len(got) != 1 || got[0] != doc {
			t.Fatalf("ext %s: got %v, want [%s]", filepath.Ext(name), got, doc)
		}
	}
}

func TestDetect_DirectoryWithDocExtIsNotRef(t *testing.T) {
	// A directory named like a document is not a regular file.
	dir := filepath.Join(t.TempDir(), "fake.pdf")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Detect("open " + dir); len(got) != 0 {
		t.Fatalf("directory must not be detected as a document, got %v", got)
	}
}

func TestDetect_ImageExtIsNotDocRef(t *testing.T) {
	// Family separation: an existing image file stays imgrefs' business.
	img := writeDoc(t, "shot.png")
	if got := Detect("look at " + img); len(got) != 0 {
		t.Fatalf("image extension must not be a document reference, got %v", got)
	}
}
