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
	for _, line := range strings.Split(out, "\n") {
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
	for _, line := range strings.Split(out, "\n") {
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
