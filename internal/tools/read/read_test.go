package read

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead_Basic(t *testing.T) {
	p := writeFile(t, "alpha\nbeta\ngamma\n")
	out, err := New().Execute(context.Background(), json.RawMessage(`{"path":"`+p+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     1\talpha") || !strings.Contains(out, "     3\tgamma") {
		t.Errorf("missing numbered lines:\n%s", out)
	}
	if !strings.Contains(out, "3 lines emitted") {
		t.Errorf("missing emitted count: %s", out)
	}
}

func TestRead_LimitParamRejected(t *testing.T) {
	// limit is intentionally absent from the schema — the model must not
	// be able to override the fixed 20-line window. UnmarshalStrict must
	// reject any call that includes "limit".
	p := writeFile(t, "line1\nline2\n")
	args, _ := json.Marshal(map[string]any{"path": p, "limit": 100})
	_, err := New().Execute(context.Background(), args)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("expected unknown-field error for 'limit', got: %v", err)
	}
}

func TestRead_OffsetNavigates(t *testing.T) {
	// offset alone (no limit) must navigate a multi-page file correctly.
	p := writeFile(t, "1\n2\n3\n4\n5\n")
	args, _ := json.Marshal(map[string]any{"path": p, "offset": 3})
	out, err := New().Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     3\t3") || !strings.Contains(out, "     5\t5") {
		t.Errorf("expected lines 3-5:\n%s", out)
	}
	if strings.Contains(out, "     2\t2") {
		t.Errorf("offset=3 should skip line 2:\n%s", out)
	}
}

func TestRead_MissingPath(t *testing.T) {
	_, err := New().Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("err = %v", err)
	}
}

func TestRead_NotExist(t *testing.T) {
	_, err := New().Execute(context.Background(), json.RawMessage(`{"path":"/no/such/file.xyz"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRead_DirectoryDegradesToListing(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("alpha.txt", "hi")
	mustWrite(".hidden", "secret")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := New().Execute(context.Background(), json.RawMessage(`{"path":"`+dir+`"}`))
	if err != nil {
		t.Fatalf("read on a directory should NOT error any more: %v", err)
	}
	for _, frag := range []string{"(directory)", "alpha.txt", "sub/", "list_dir"} {
		if !strings.Contains(out, frag) {
			t.Errorf("missing %q in listing:\n%s", frag, out)
		}
	}
	if strings.Contains(out, ".hidden") {
		t.Errorf("dotfiles should be excluded by default: %s", out)
	}
	// Sanity: directories appear before files in the body. We grab
	// the section between the header line and the trailing summary.
	body := strings.TrimSpace(strings.SplitN(out, "\n\n", 2)[0])
	if idx := strings.Index(body, "sub/"); idx == -1 || strings.Index(body, "alpha.txt") < idx {
		t.Errorf("expected sub/ before alpha.txt:\n%s", body)
	}
}

func TestRead_DefaultLimitTruncatesLargeFile(t *testing.T) {
	// Build a file with defaultLimit+10 lines — reading without an explicit
	// limit must stop at defaultLimit and emit a TRUNCATED notice.
	var sb strings.Builder
	total := defaultLimit + 10
	for i := range total {
		fmt.Fprintf(&sb, "line%d\n", i+1)
	}
	p := writeFile(t, sb.String())

	out, err := New().Execute(context.Background(), json.RawMessage(`{"path":"`+p+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "TRUNCATED") {
		t.Errorf("expected TRUNCATED notice for %d-line file with default limit %d:\n%s", total, defaultLimit, out)
	}
	// The last emitted line should be defaultLimit, not total.
	wantLast := fmt.Sprintf("%6d\tline%d", defaultLimit, defaultLimit)
	if !strings.Contains(out, wantLast) {
		t.Errorf("expected last emitted line %q:\n%s", wantLast, out)
	}
	unwanted := fmt.Sprintf("line%d", defaultLimit+1)
	if strings.Contains(out, unwanted) {
		t.Errorf("output should not contain line beyond default limit: %s", out)
	}
}

func TestRead_SchemaIsStable(t *testing.T) {
	// PRD §4.8.1: schema must be byte-identical across calls so DeepSeek's
	// prefix cache hits. We guarantee this with a package-level []byte.
	a := New().Schema()
	b := New().Schema()
	if string(a) != string(b) {
		t.Errorf("schema bytes drifted")
	}
}
