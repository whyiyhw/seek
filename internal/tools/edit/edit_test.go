package edit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/whyiyhw/seek/internal/permission"
)

func setup(t *testing.T, body string) (string, Tool) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pol, _ := permission.New(dir, permission.PrefDeny)
	return p, New(pol)
}

func run(t *testing.T, tool Tool, args Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(args)
	return tool.Execute(context.Background(), b)
}

func TestEdit_BasicReplace(t *testing.T) {
	path, tool := setup(t, "hello world\n")
	out, err := run(t, tool, Args{Path: path, OldString: "world", NewString: "there"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 replacement") {
		t.Errorf("output: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello there\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEdit_SuccessResultWrapsDiffInFence(t *testing.T) {
	// The TUI scrollback renderer keys off ```diff fences to decide whether
	// to surface a coloured diff under the success line. If this fence ever
	// disappears the human-visible "before/after" view goes silently dark
	// while the model still sees the diff — a regression that's hard to
	// notice without screen-watching. Pin the contract here.
	path, tool := setup(t, "hello world\n")
	out, err := run(t, tool, Args{Path: path, OldString: "world", NewString: "there"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\n```diff\n") {
		t.Errorf("success result must wrap diff in a ```diff fence, got:\n%s", out)
	}
	if !strings.HasSuffix(out, "```") {
		t.Errorf("success result must end with closing ``` fence, got:\n%s", out)
	}
	// And the diff body must still contain real +/- lines.
	if !strings.Contains(out, "-hello world") {
		t.Errorf("diff body missing `-` line, got:\n%s", out)
	}
	if !strings.Contains(out, "+hello there") {
		t.Errorf("diff body missing `+` line, got:\n%s", out)
	}
}

func TestEdit_NotFound(t *testing.T) {
	path, tool := setup(t, "hello\n")
	_, err := run(t, tool, Args{Path: path, OldString: "missing", NewString: "x"})
	if err == nil || !strings.Contains(err.Error(), "occurs 0 times") {
		t.Errorf("err = %v", err)
	}
}

func TestEdit_AmbiguousMatchAtomic(t *testing.T) {
	path, tool := setup(t, "foo foo foo\n")
	_, err := run(t, tool, Args{Path: path, OldString: "foo", NewString: "bar"})
	if err == nil || !strings.Contains(err.Error(), "occurs 3 times") {
		t.Errorf("err = %v", err)
	}
	// File must be unchanged after a failed edit.
	got, _ := os.ReadFile(path)
	if string(got) != "foo foo foo\n" {
		t.Errorf("file mutated: %q", got)
	}
}

func TestEdit_ExpectedReplacements(t *testing.T) {
	path, tool := setup(t, "x x x\n")
	out, err := run(t, tool, Args{Path: path, OldString: "x", NewString: "y", ExpectedReplacements: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3 replacement") {
		t.Errorf("output: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "y y y\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEdit_DeleteWithEmptyNew(t *testing.T) {
	path, tool := setup(t, "abcXYZdef\n")
	_, err := run(t, tool, Args{Path: path, OldString: "XYZ", NewString: ""})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "abcdef\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEdit_NoOpRejected(t *testing.T) {
	path, tool := setup(t, "same\n")
	_, err := run(t, tool, Args{Path: path, OldString: "same", NewString: "same"})
	if err == nil || !strings.Contains(err.Error(), "no-op") {
		t.Errorf("err = %v", err)
	}
}

func TestEdit_OutsideCWDDenied(t *testing.T) {
	dir := t.TempDir()
	pol, _ := permission.New(dir, permission.PrefDeny)
	tool := New(pol)
	other := t.TempDir()
	p := filepath.Join(other, "x.txt")
	os.WriteFile(p, []byte("a"), 0o644)
	_, err := run(t, tool, Args{Path: p, OldString: "a", NewString: "b"})
	if !errors.Is(err, permission.ErrDenied) {
		t.Errorf("err = %v", err)
	}
}

// "café" — pre-composed (NFC): c,a,f,é (5 bytes), decomposed (NFD): c,a,f,e,◌́ (6 bytes).
const (
	nfcCafe = "café"  // é = U+00E9
	nfdCafe = "café" // e + combining acute U+0301
)

func TestEdit_NFCFallback_NFDNeedleOnNFCFile(t *testing.T) {
	// File on disk is NFC; model produces an NFD needle (common when copy/paste
	// passes through a normaliser, or the model was trained on decomposed text).
	path, tool := setup(t, "hello "+nfcCafe+"\n")
	if norm.NFC.String(nfdCafe) != nfcCafe {
		t.Fatal("test fixture broken: NFC(nfd) should equal nfc")
	}
	out, err := run(t, tool, Args{Path: path, OldString: nfdCafe, NewString: "coffee"})
	if err != nil {
		t.Fatalf("expected NFC fallback to succeed, got: %v", err)
	}
	if !strings.Contains(out, "NFC") {
		t.Errorf("expected result to mention NFC normalisation, got: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello coffee\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEdit_NFCFallback_NFCNeedleOnNFDFile(t *testing.T) {
	// File on disk is NFD (less common but possible — e.g. macOS HFS+
	// historically normalised filenames this way; same applies to file
	// contents that came from such a source).
	path, tool := setup(t, "hello "+nfdCafe+"!\n")
	out, err := run(t, tool, Args{Path: path, OldString: nfcCafe, NewString: "tea"})
	if err != nil {
		t.Fatalf("expected NFC fallback to succeed, got: %v", err)
	}
	if !strings.Contains(out, "NFC") {
		t.Errorf("expected result to mention NFC normalisation, got: %s", out)
	}
	// After fallback the file is rewritten in NFC form.
	got, _ := os.ReadFile(path)
	want := "hello tea!\n"
	if string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

func TestEdit_NFCFallback_CountMismatchStillFails(t *testing.T) {
	// File is fully NFD; needle is NFC. Exact match = 0, NFC match = 2,
	// expected_replacements defaults to 1 → must fail atomically.
	body := "x=" + nfdCafe + "; y=" + nfdCafe + ";\n"
	path, tool := setup(t, body)
	_, err := run(t, tool, Args{Path: path, OldString: nfcCafe, NewString: "Z"})
	if err == nil {
		t.Fatal("expected count-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "2 times (after Unicode NFC") {
		t.Errorf("error should report NFC count, got: %v", err)
	}
	// File must be unchanged on failure (no NFC rewrite on the failure path).
	got, _ := os.ReadFile(path)
	if string(got) != body {
		t.Errorf("file mutated on failure: %q", got)
	}
}

func TestEdit_ExactPreservesBytes(t *testing.T) {
	// Belt-and-suspenders: the exact-match path must not touch NFC. If a file
	// happens to be in NFD and the needle matches NFD exactly, bytes outside
	// the edit range must be preserved verbatim.
	body := "lead " + nfdCafe + " mid OLD tail " + nfdCafe + "\n"
	path, tool := setup(t, body)
	_, err := run(t, tool, Args{Path: path, OldString: "OLD", NewString: "NEW"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "lead " + nfdCafe + " mid NEW tail " + nfdCafe + "\n"
	if string(got) != want {
		t.Errorf("exact-match path rewrote unrelated bytes:\n  got  %q\n  want %q", got, want)
	}
}

func TestEdit_ClosestCandidateHint(t *testing.T) {
	body := strings.Join([]string{
		"line one",
		"this is the target line",
		"line three",
		"line four",
	}, "\n") + "\n"
	path, tool := setup(t, body)
	// Needle differs in whitespace — should miss exact and NFC, but match
	// closely on the closest-candidate scorer (which canonicalises whitespace).
	needle := "this is the  target line" // double space
	_, err := run(t, tool, Args{Path: path, OldString: needle, NewString: "x"})
	if err == nil {
		t.Fatal("expected miss error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "closest candidate") {
		t.Errorf("error should embed closest candidate, got:\n%s", msg)
	}
	if !strings.Contains(msg, "lines 2-2") {
		t.Errorf("hint should report line range, got:\n%s", msg)
	}
	// The diff must show BOTH sides on adjacent lines so the model can
	// compare byte-for-byte. The needle goes on `-`, the file goes on `+`.
	if !strings.Contains(msg, "-this is the  target line") {
		t.Errorf("diff should show needle on `-` line, got:\n%s", msg)
	}
	if !strings.Contains(msg, "+this is the target line") {
		t.Errorf("diff should show file content on `+` line, got:\n%s", msg)
	}
	if !strings.Contains(msg, "```diff") {
		t.Errorf("diff block should be fenced as ```diff, got:\n%s", msg)
	}
}

func TestEdit_ClosestCandidateExposesInvisibleUnicodeDiff(t *testing.T) {
	// The whole point: a Unicode mismatch that's byte-different but visually
	// identical. The diff format puts the two sequences on adjacent lines so
	// the model can SEE which bytes differ even when the rendered glyphs are
	// the same. We use a zero-width space (U+200B) to make the test robust:
	// the file has it, the model's needle doesn't.
	const zws = "​"
	fileLine := "value=" + zws + "config" // contains zero-width space
	body := "header\n" + fileLine + "\ntrailer\n"
	path, tool := setup(t, body)
	// Needle without the zero-width space — looks identical when printed.
	_, err := run(t, tool, Args{Path: path, OldString: "value=config", NewString: "x"})
	if err == nil {
		t.Fatal("expected miss, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "```diff") {
		t.Fatalf("expected diff block in hint, got:\n%s", msg)
	}
	// The diff must contain both bytes-different versions so the model can
	// notice the invisible character.
	if !strings.Contains(msg, "+"+fileLine) {
		t.Errorf("diff should show file line with the zero-width space, got:\n%s", msg)
	}
	if !strings.Contains(msg, "-value=config") {
		t.Errorf("diff should show needle without the zero-width space, got:\n%s", msg)
	}
}

func TestEdit_NoCandidateWhenNoNearMatch(t *testing.T) {
	// When the needle has nothing in common with the file, no hint is offered
	// (otherwise we'd return arbitrary noise).
	path, tool := setup(t, "alpha\nbeta\ngamma\n")
	_, err := run(t, tool, Args{Path: path, OldString: "totally unrelated", NewString: "x"})
	if err == nil {
		t.Fatal("expected miss error, got nil")
	}
	if strings.Contains(err.Error(), "closest candidate") {
		t.Errorf("should not embed a candidate for unrelated needle, got: %v", err)
	}
}
