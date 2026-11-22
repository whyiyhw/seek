package grep

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup creates:
//
//	root/
//	  a.go      "package main\n\nfunc Foo() {}\nfunc Bar() {}\n"
//	  b.go      "package main\n\nfunc Baz() { Foo() }\n"
//	  sub/
//	    c.go    "package sub\n\nfunc Foo() {}\n"
//	  .hidden/
//	    d.go    "package hidden\n\nfunc Foo() {}\n"
//	  data.bin  "\x00\x01\x02" (binary — should be skipped)
func setup(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("a.go", "package main\n\nfunc Foo() {}\nfunc Bar() {}\n")
	write("b.go", "package main\n\nfunc Baz() { Foo() }\n")
	write("sub/c.go", "package sub\n\nfunc Foo() {}\n")
	write(".hidden/d.go", "package hidden\n\nfunc Foo() {}\n")
	write("data.bin", "\x00\x01\x02binary content")

	return root
}

func run(t *testing.T, a Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return New().Execute(context.Background(), b)
}

func TestGrep_SingleFile(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "func Foo", Path: filepath.Join(root, "a.go")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "func Foo") {
		t.Errorf("expected match: %s", out)
	}
	// Bar is on an adjacent line — should appear in context
	if !strings.Contains(out, "Bar") {
		t.Errorf("expected context line with Bar: %s", out)
	}
}

func TestGrep_Directory_RecursesFiles(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "func Foo", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf("expected a.go in results: %s", out)
	}
	if !strings.Contains(out, "sub/c.go") && !strings.Contains(out, "sub"+string(filepath.Separator)+"c.go") {
		t.Errorf("expected sub/c.go in results: %s", out)
	}
}

func TestGrep_Directory_SkipsHidden(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "func Foo", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".hidden") {
		t.Errorf("hidden dir should be skipped: %s", out)
	}
}

func TestGrep_Directory_SkipsBinary(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "binary", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	// binary file should be silently skipped even though it contains "binary"
	if strings.Contains(out, "data.bin") {
		t.Errorf("binary file should be skipped: %s", out)
	}
}

func TestGrep_Glob_DoubleStarGoFiles(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "func Foo", Path: filepath.Join(root, "**", "*.go")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "func Foo") {
		t.Errorf("expected match via ** glob: %s", out)
	}
}

// TestGrep_DotPath_WalksCwd pins `"path": "."` — the most natural way to say
// "search everything here". WalkDir reports the walk ROOT with its literal
// name, so for "." the root's Name() IS "." and the hidden-entry filter
// SkipDir'd the root itself: every such search silently returned "no matches".
func TestGrep_DotPath_WalksCwd(t *testing.T) {
	root := setup(t)
	t.Chdir(root)

	out, err := run(t, Args{Pattern: "func Foo", Path: "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf(`grep with path "." must search the cwd, got: %s`, out)
	}
	// The root exemption must not resurrect hidden CHILDREN.
	if strings.Contains(out, ".hidden") {
		t.Errorf("hidden dir must still be skipped below a . root: %s", out)
	}
}

// TestGrep_Glob_DoubleStarTrailing: a pattern ENDING in ** (e.g. "dir/**")
// leaves an empty suffix, and the old `suffix[0]` separator-strip indexed it —
// panic: index out of range. The grep tool runs in-process with no recover, so
// that crash takes the whole host down.
func TestGrep_Glob_DoubleStarTrailing(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "func Foo", Path: filepath.Join(root, "**")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf(`"<dir>/**" must match every file below it, got: %s`, out)
	}
	if strings.Contains(out, ".hidden") {
		t.Errorf("hidden dir must still be skipped: %s", out)
	}
}

// TestGrep_Glob_DoubleStarFromDot covers the ** walker's own root check:
// "**/*.go" cleans its empty prefix to ".". Same silent-zero bug, different
// walker.
func TestGrep_Glob_DoubleStarFromDot(t *testing.T) {
	root := setup(t)
	t.Chdir(root)

	out, err := run(t, Args{Pattern: "func Foo", Path: "**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf(`"**/*.go" from the cwd must match, got: %s`, out)
	}
	if strings.Contains(out, ".hidden") {
		t.Errorf("hidden dir must still be skipped: %s", out)
	}
}

func TestGrep_NoMatch(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "XYZNOTFOUND", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no matches") {
		t.Errorf("expected no-match message: %s", out)
	}
}

func TestGrep_IgnoreCase(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "func foo", Path: root, IgnoreCase: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "func Foo") {
		t.Errorf("expected case-insensitive match: %s", out)
	}
}

func TestGrep_Fixed_LiteralDot(t *testing.T) {
	root := setup(t)
	// "Baz() { Foo() }" contains the literal string "{ Foo"
	// Without fixed=true, "." in a regex matches any char.
	// With fixed=true, "Foo()" is a literal and should still match.
	out, err := run(t, Args{Pattern: "Foo()", Path: root, Fixed: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Foo()") {
		t.Errorf("expected literal match: %s", out)
	}
}

func TestGrep_ZeroContext(t *testing.T) {
	root := setup(t)
	// Use raw JSON so context_lines=0 is explicitly present (not omitted by omitempty).
	raw := fmt.Sprintf(`{"pattern":"func Foo","path":%q,"context_lines":0}`, filepath.Join(root, "a.go"))
	out, err := New().Execute(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	// With 0 context, adjacent Bar line should not appear.
	if strings.Contains(out, "func Bar") {
		t.Errorf("context=0 should not show adjacent lines: %s", out)
	}
}

func TestGrep_MaxMatchesCaps(t *testing.T) {
	root := setup(t)
	// Foo() appears in a.go (lines 3, 4 via Baz), b.go, sub/c.go — cap at 1
	out, err := run(t, Args{Pattern: "func Foo", Path: root, MaxMatches: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "capped") {
		t.Errorf("expected capped notice: %s", out)
	}
}

func TestGrep_InvalidRegex(t *testing.T) {
	root := setup(t)
	_, err := run(t, Args{Pattern: "[invalid", Path: root})
	if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
		t.Errorf("expected invalid pattern error, got: %v", err)
	}
}

func TestGrep_MissingPattern(t *testing.T) {
	_, err := run(t, Args{Path: "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("expected missing pattern error, got: %v", err)
	}
}

func TestGrep_MissingPath(t *testing.T) {
	_, err := run(t, Args{Pattern: "foo"})
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("expected missing path error, got: %v", err)
	}
}

func TestGrep_UnknownFieldError(t *testing.T) {
	_, err := New().Execute(context.Background(),
		json.RawMessage(`{"pattern":"foo","path":"/tmp","typo":true}`))
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Errorf("expected unknown-field error naming 'typo', got: %v", err)
	}
}

func TestGrep_DefaultMaxMatchesCapsAt20(t *testing.T) {
	// Build a file with defaultMaxMatches+5 matching lines.
	// Calling grep without an explicit max_matches must stop at defaultMaxMatches
	// and include a "capped" notice so the LLM knows there are more results.
	root := t.TempDir()
	total := defaultMaxMatches + 5
	var sb strings.Builder
	for i := range total {
		fmt.Fprintf(&sb, "func Hit%d() {}\n", i)
	}
	p := filepath.Join(root, "hits.go")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, Args{Pattern: "func Hit", Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "capped") {
		t.Errorf("expected capped notice for %d matches with default limit %d:\n%s", total, defaultMaxMatches, out)
	}
	// Count match lines (lines starting with '>') — must equal defaultMaxMatches.
	matchLines := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, ">") {
			matchLines++
		}
	}
	if matchLines != defaultMaxMatches {
		t.Errorf("got %d match lines, want %d:\n%s", matchLines, defaultMaxMatches, out)
	}
}

func TestGrep_MatchLineMarkedWithArrow(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "func Foo", Path: filepath.Join(root, "a.go")})
	if err != nil {
		t.Fatal(err)
	}
	// Lines that look like "  NNN  content" or "> NNN  content" are output lines.
	// Match lines must start with '>'; context lines must not.
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip header / footer / separator lines (no leading digit after optional '>').
		if len(trimmed) == 0 || trimmed == "---" {
			continue
		}
		isOutputLine := strings.HasPrefix(line, "> ") || strings.HasPrefix(line, "  ")
		if !isOutputLine {
			continue
		}
		if strings.Contains(line, "func Foo") && !strings.HasPrefix(line, ">") {
			t.Errorf("match line should start with '>': %q", line)
		}
	}
}

// TestGrep_TruncatesLongLines pins per-line truncation. Source lines
// over maxLineChars (e.g. minified JS / generated declarations) get
// clipped with a "…(truncated)" sentinel so a single match can't blow
// the output budget on its own.
func TestGrep_TruncatesLongLines(t *testing.T) {
	root := t.TempDir()
	// Build a single-line file > maxLineChars where the pattern appears
	// once near the front. The full source line has length
	// maxLineChars*3, so the formatted match line would be huge
	// without truncation.
	longLine := "ALPHA " + strings.Repeat("x", maxLineChars*3) + " ZETA"
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(longLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, Args{Pattern: "ALPHA", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "…(truncated)") {
		t.Errorf("long line should be truncated with …(truncated) sentinel; got %q", out)
	}
	// Sanity: the displayed line should be much shorter than the source.
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "ALPHA") && len(line) > maxLineChars*2 {
			t.Errorf("line containing match still too long (%d bytes): %q", len(line), line)
		}
	}
}

// TestGrep_HardBytesCap pins the safety belt at the output level.
// A pathological pattern that would otherwise return tens of KiB of
// matches gets clipped at maxOutputBytes with a notice telling the
// model how to refine.
func TestGrep_HardBytesCap(t *testing.T) {
	root := t.TempDir()
	// Build many files each containing the pattern + verbose context
	// so the formatted output blows past maxOutputBytes.
	for i := range 50 {
		var b strings.Builder
		for range 20 {
			// Each match has 7 lines of context (3 before + 3 after by default).
			// Pad each line to ensure the file is meaty.
			b.WriteString(strings.Repeat("// padding line ", 8) + "\n")
		}
		b.WriteString("TARGET_PATTERN appears here\n")
		for range 20 {
			b.WriteString(strings.Repeat("// padding line ", 8) + "\n")
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	maxCap := 50
	out, err := run(t, Args{Pattern: "TARGET_PATTERN", Path: root, MaxMatches: maxCap})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > maxOutputBytes+200 {
		t.Errorf("output exceeded maxOutputBytes+200 budget: got %d bytes (cap=%d)", len(out), maxOutputBytes)
	}
	if !strings.Contains(out, "truncated at") {
		t.Errorf("truncated output should carry the refine-pattern notice; got tail %q", tail(out, 400))
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// --- 2026-11-22 argument extension ------------------------------------
//
// These five arguments exist because the 40-session baseline reached for
// bash 2,104 times to express things the tool could not: --include
// (11.3%), -v (9.6%), -c (9.7%), -l (4.4%), asymmetric -A/-B (10.3%).
// One test per argument, each pinning the shape the shell form produced.

func TestGrep_IncludeFiltersByExtension(t *testing.T) {
	root := setup(t)
	// "func Foo" appears in a.go, b.go (as a call), sub/c.go, .hidden/d.go.
	out, err := run(t, Args{Pattern: "func Foo", Path: root, Include: "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf("include=*.go must keep a.go: %s", out)
	}
	// And a non-matching include excludes everything.
	out, err = run(t, Args{Pattern: "func Foo", Path: root, Include: "*.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no matches") {
		t.Errorf("include=*.md over a Go-only tree must report no matches: %s", out)
	}
}

func TestGrep_IncludeAcceptsDoubleStarPrefix(t *testing.T) {
	root := setup(t)
	// `**/*.go` is what a model writes after reading the docs; it must
	// behave as `*.go`, not silently match nothing.
	out, err := run(t, Args{Pattern: "func Foo", Path: root, Include: "**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Errorf("include=**/*.go must behave as *.go: %s", out)
	}
}

func TestGrep_InvertReturnsNonMatchingLines(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "mix.txt")
	content := "keep one\nDROP me\nkeep two\nDROP me too\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, Args{Pattern: "DROP", Path: p, Invert: true, ContextLines: new(int)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "keep one") || !strings.Contains(out, "keep two") {
		t.Errorf("invert must return the non-matching lines: %s", out)
	}
	// Inverted lines ARE the matches, so they carry the '>' marker —
	// that is the point of the flag, not a leak of the dropped ones.
	if strings.Contains(out, "DROP me") {
		t.Errorf("inverted run must not echo the matching lines at all: %s", out)
	}
}

func TestGrep_CountOnlyIsExactAndUncapped(t *testing.T) {
	root := t.TempDir()
	// 250 matches in one file — above maxAllowedMatches (200), so a
	// capped implementation would report 200 and be wrong.
	var sb strings.Builder
	for i := range 250 {
		fmt.Fprintf(&sb, "HIT-%03d\n", i)
	}
	p := filepath.Join(root, "many.txt")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, Args{Pattern: "HIT-", Path: p, CountOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "250 match(es)") {
		t.Errorf("count_only must report the TRUE total (250), got: %s", out)
	}
	if !strings.Contains(out, "250  "+p) && !strings.Contains(out, "    250  "+p) {
		t.Errorf("per-file row should carry 250, got: %s", out)
	}
}

func TestGrep_CountOnlyAcrossFiles(t *testing.T) {
	root := setup(t)
	out, err := run(t, Args{Pattern: "package main", Path: root, CountOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	// a.go and b.go both open with "package main"; sub/c.go declares
	// "package sub", and .hidden/d.go is skipped by the walk.
	if !strings.Contains(out, "2 match(es) across 2 file(s)") {
		t.Errorf("expected 2 matches across 2 files, got: %s", out)
	}
}

func TestGrep_FilesOnlyListsPaths(t *testing.T) {
	root := setup(t)
	// Two matches in a.go must NOT consume the budget twice and hide the
	// second file: files_only counts FILES.
	out, err := run(t, Args{Pattern: "Foo", Path: root, FilesOnly: true, MaxMatches: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "b.go") {
		t.Errorf("files_only must list every matching file regardless of max_matches: %s", out)
	}
	if strings.Contains(out, "> ") {
		t.Errorf("files_only must print paths only, no match lines: %s", out)
	}
}

func TestGrep_FilesOnlyAndCountOnlyAreMutuallyExclusive(t *testing.T) {
	root := setup(t)
	_, err := run(t, Args{Pattern: "Foo", Path: root, FilesOnly: true, CountOnly: true})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected a mutual-exclusion error, got: %v", err)
	}
}

func TestGrep_AsymmetricContextBefore(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "ctx.txt")
	// Line 5 is the only match; 2 lines before, 0 after.
	var sb strings.Builder
	for i := 1; i <= 9; i++ {
		if i == 5 {
			sb.WriteString("THE_MATCH\n")
			continue
		}
		fmt.Fprintf(&sb, "filler %d\n", i)
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	zero, two := 0, 2
	out, err := run(t, Args{Pattern: "THE_MATCH", Path: p, Before: &two, After: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "filler 3") || !strings.Contains(out, "filler 4") {
		t.Errorf("before=2 must show the two preceding lines: %s", out)
	}
	if strings.Contains(out, "filler 6") {
		t.Errorf("after=0 must show no trailing context: %s", out)
	}
}

func TestGrep_BeforeAfterOverrideContextLines(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "ctx.txt")
	var sb strings.Builder
	for i := 1; i <= 9; i++ {
		if i == 5 {
			sb.WriteString("THE_MATCH\n")
			continue
		}
		fmt.Fprintf(&sb, "filler %d\n", i)
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	one, three := 1, 3
	out, err := run(t, Args{Pattern: "THE_MATCH", Path: p, Before: &one, After: &three})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "filler 4") {
		t.Errorf("before=1 must include line 4: %s", out)
	}
	if strings.Contains(out, "filler 3") {
		t.Errorf("before=1 must exclude line 3: %s", out)
	}
	if !strings.Contains(out, "filler 8") {
		t.Errorf("after=3 must include line 8: %s", out)
	}
	if strings.Contains(out, "filler 9") {
		t.Errorf("after=3 must exclude line 9: %s", out)
	}
}

// TestGrep_SchemaIsStableBytes pins the prefix-cache invariant: the
// schema is a package-level constant, so two calls must produce
// identical bytes. It also fails loudly if a future edit swaps the
// constant for a builder.
func TestGrep_SchemaIsStableBytes(t *testing.T) {
	a := string(New().Schema())
	b := string(New().Schema())
	if a != b {
		t.Error("schema bytes differ across calls — a schema built at call time breaks the prefix cache")
	}
	for _, field := range []string{"include", "invert", "files_only", "count_only", "before", "after"} {
		if !strings.Contains(a, `"`+field+`"`) {
			t.Errorf("schema lost the %q argument", field)
		}
	}
}
