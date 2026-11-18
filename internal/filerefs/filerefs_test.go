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

// TestDetectRefs_RichPrefix: "@rich:report.pdf" upgrades the upload
// purpose (D-F1); the prefix is stat-gated like everything else, and
// plain + rich forms of the same file in one message dedup to the
// first occurrence's flag.
func TestDetectRefs_RichPrefix(t *testing.T) {
	doc := writeDoc(t, "report.pdf")

	// Plain form → Rich=false.
	refs := DetectRefs("read " + doc)
	if len(refs) != 1 || refs[0].Path != doc || refs[0].Rich {
		t.Fatalf("plain: %+v", refs)
	}

	// @rich: form → same path, Rich=true.
	refs = DetectRefs("read @rich:" + doc)
	if len(refs) != 1 || refs[0].Path != doc || !refs[0].Rich {
		t.Fatalf("rich: %+v", refs)
	}

	// Wrapped + punctuated rich ref still resolves.
	refs = DetectRefs("see (@rich:" + doc + "),")
	if len(refs) != 1 || refs[0].Path != doc || !refs[0].Rich {
		t.Fatalf("wrapped rich: %+v", refs)
	}

	// @rich: on a non-existent path is not a reference (stat gate).
	refs = DetectRefs("read @rich:/nope/missing.pdf")
	if len(refs) != 0 {
		t.Fatalf("missing rich target must not detect: %+v", refs)
	}

	// First occurrence wins: plain-then-rich and rich-then-plain both
	// dedup to one ref carrying the FIRST form's flag.
	refs = DetectRefs(doc + " and @rich:" + doc)
	if len(refs) != 1 || refs[0].Rich {
		t.Fatalf("plain-first: %+v", refs)
	}
	refs = DetectRefs("@rich:" + doc + " and " + doc)
	if len(refs) != 1 || !refs[0].Rich {
		t.Fatalf("rich-first: %+v", refs)
	}

	// A file literally named "rich:x.pdf" (relative token, the whole
	// token IS the path) keeps working: the prefix only binds when the
	// remainder stats as a document — which here it doesn't, so the
	// token itself is tried verbatim.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rich:x.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	refs = DetectRefs("open rich:x.pdf")
	if len(refs) != 1 || refs[0].Path != "rich:x.pdf" || refs[0].Rich {
		t.Fatalf("literal rich-named file: %+v", refs)
	}
}

// TestDetect_DetectRefsAgreement: Detect is the projection of
// DetectRefs — same order, same paths.
func TestDetect_DetectRefsAgreement(t *testing.T) {
	a := writeDoc(t, "a.pdf")
	b := writeDoc(t, "b.docx")
	text := "first " + a + " then @rich:" + b + " then " + a
	refs := DetectRefs(text)
	paths := Detect(text)
	if len(refs) != 2 || len(paths) != 2 {
		t.Fatalf("refs=%v paths=%v", refs, paths)
	}
	for i := range refs {
		if refs[i].Path != paths[i] {
			t.Fatalf("disagreement at %d: %+v vs %v", i, refs[i], paths)
		}
	}
}
