package references

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/lspclient"
	"github.com/whyiyhw/seek/internal/tools"
)

type fakeResolver struct {
	gotFile    string
	gotContent string
	gotPos     lspclient.Position
	locs       []lspclient.Location
	err        error
}

func (f *fakeResolver) References(ctx context.Context, absFile, content string, pos lspclient.Position) ([]lspclient.Location, error) {
	f.gotFile = absFile
	f.gotContent = content
	f.gotPos = pos
	return f.locs, f.err
}

func run(t *testing.T, r Resolver, a Args) (string, error) {
	t.Helper()
	b, _ := json.Marshal(a)
	return New(r).Execute(context.Background(), b)
}

// writeFile creates a temp file with the given content and returns its path.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func loc(uri string, line0, char0 int) lspclient.Location {
	return lspclient.Location{
		URI:   uri,
		Range: lspclient.Range{Start: lspclient.Position{Line: line0, Character: char0}},
	}
}

func TestRefs_PositionNormalization(t *testing.T) {
	// "func Kill" — symbol Kill starts at byte 5 on line 3 (1-based).
	file := writeFile(t, "x.go", "package x\n\nfunc Kill() {}\n")
	fr := &fakeResolver{}
	if _, err := run(t, fr, Args{File: file, Line: 3, Symbol: "Kill"}); err != nil {
		t.Fatal(err)
	}
	want := lspclient.Position{Line: 2, Character: 5} // 1-based(3) → 0-based(2); "Kill" at col 5
	if fr.gotPos != want {
		t.Fatalf("pos = %+v, want %+v (1-based line → 0-based; symbol column located)", fr.gotPos, want)
	}
	if !strings.Contains(fr.gotContent, "func Kill") {
		t.Fatalf("resolver should receive current file content, got %q", fr.gotContent)
	}
}

func TestRefs_ExplicitCharacter(t *testing.T) {
	file := writeFile(t, "x.go", "func Kill() {}\n")
	fr := &fakeResolver{}
	if _, err := run(t, fr, Args{File: file, Line: 1, Character: 6}); err != nil {
		t.Fatal(err)
	}
	if fr.gotPos != (lspclient.Position{Line: 0, Character: 5}) {
		t.Fatalf("pos = %+v, want {0,5} (explicit 1-based character → 0-based)", fr.gotPos)
	}
}

func TestRefs_SymbolNotOnLine(t *testing.T) {
	file := writeFile(t, "x.go", "package x\nfunc Other() {}\n")
	if _, err := run(t, &fakeResolver{}, Args{File: file, Line: 2, Symbol: "Kill"}); err == nil {
		t.Fatal("symbol not on the line should error")
	}
}

func TestRefs_NoReferences(t *testing.T) {
	file := writeFile(t, "x.go", "func Kill() {}\n")
	out, err := run(t, &fakeResolver{locs: nil}, Args{File: file, Line: 1, Symbol: "Kill"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no references to Kill") {
		t.Fatalf("out = %q, want a no-references message", out)
	}
}

func TestRefs_FormatsLocationsWithSnippet(t *testing.T) {
	// A referenced file the formatter can read for the snippet.
	ref := writeFile(t, "caller.go", "package x\nfunc f() { Kill() }\n")
	file := writeFile(t, "x.go", "func Kill() {}\n")
	fr := &fakeResolver{locs: []lspclient.Location{loc("file://"+ref, 1, 10)}} // line index 1 = "func f() { Kill() }"
	out, err := run(t, fr, Args{File: file, Line: 1, Symbol: "Kill"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 reference(s) to Kill") {
		t.Fatalf("missing count header: %q", out)
	}
	if !strings.Contains(out, ":2:11") { // 0-based (1,10) → 1-based (2,11)
		t.Fatalf("location not 1-based in output: %q", out)
	}
	if !strings.Contains(out, "func f() { Kill() }") {
		t.Fatalf("missing source snippet: %q", out)
	}
}

func TestRefs_OutputCap(t *testing.T) {
	file := writeFile(t, "x.go", "func Kill() {}\n")
	var locs []lspclient.Location
	for i := 0; i < 60; i++ {
		locs = append(locs, loc(fmt.Sprintf("file:///nonexistent/f%d.go", i), i, 0))
	}
	out, err := run(t, &fakeResolver{locs: locs}, Args{File: file, Line: 1, Symbol: "Kill"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "60 reference(s)") {
		t.Fatalf("header should report the true total (60): %q", firstLines(out, 1))
	}
	if !strings.Contains(out, "10 more") {
		t.Fatalf("expected truncation note for 60>50: %q", out)
	}
	if n := strings.Count(out, "/nonexistent/"); n != maxRefs {
		t.Fatalf("printed %d locations, want capped at %d", n, maxRefs)
	}
}

func TestRefs_MissingBinary(t *testing.T) {
	file := writeFile(t, "x.go", "func Kill() {}\n")
	fr := &fakeResolver{err: &lspclient.MissingBinaryError{Command: "gopls", Install: "go install golang.org/x/tools/gopls@latest"}}
	out, err := run(t, fr, Args{File: file, Line: 1, Symbol: "Kill"})
	if err != nil {
		t.Fatalf("missing binary should be a result hint, not a hard error: %v", err)
	}
	if !strings.Contains(out, "gopls not found") || !strings.Contains(out, "go install") || !strings.Contains(out, "grep") {
		t.Fatalf("missing-binary hint should name the binary, install cmd, and grep fallback: %q", out)
	}
}

func TestRefs_Timeout(t *testing.T) {
	file := writeFile(t, "x.go", "func Kill() {}\n")
	out, err := run(t, &fakeResolver{err: context.DeadlineExceeded}, Args{File: file, Line: 1, Symbol: "Kill"})
	if err != nil {
		t.Fatalf("timeout should degrade to a hint, not error: %v", err)
	}
	if !strings.Contains(out, "timed out") || !strings.Contains(out, "grep") {
		t.Fatalf("timeout hint should mention timeout + grep fallback: %q", out)
	}
}

func TestRefs_CtxCanceledPropagates(t *testing.T) {
	file := writeFile(t, "x.go", "func Kill() {}\n")
	_, err := run(t, &fakeResolver{err: context.Canceled}, Args{File: file, Line: 1, Symbol: "Kill"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Esc/cancel must propagate as an error, got %v", err)
	}
}

func TestRefs_FileNotFound(t *testing.T) {
	if _, err := run(t, &fakeResolver{}, Args{File: "/no/such/file.go", Line: 1, Symbol: "X"}); err == nil {
		t.Fatal("missing file should error")
	}
}

func TestRefs_RequiresSymbolOrCharacter(t *testing.T) {
	file := writeFile(t, "x.go", "func Kill() {}\n")
	if _, err := run(t, &fakeResolver{}, Args{File: file, Line: 1}); err == nil {
		t.Fatal("neither symbol nor character should error")
	}
}

func TestRefs_ReadOnlyMarker(t *testing.T) {
	var tool tools.Tool = New(&fakeResolver{})
	ro, ok := tool.(tools.ReadOnlyTool)
	if !ok || !ro.ReadOnly() {
		t.Fatal("references must be a ReadOnlyTool (query-only)")
	}
}

func TestRefs_NilResolverPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) should panic — registered tool with no resolver is a wiring bug")
		}
	}()
	New(nil)
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		return strings.Join(parts[:n], "\n")
	}
	return s
}
