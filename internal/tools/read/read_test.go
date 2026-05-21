package read

import (
	"context"
	"encoding/json"
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

func TestRead_OffsetAndLimit(t *testing.T) {
	p := writeFile(t, "1\n2\n3\n4\n5\n")
	args, _ := json.Marshal(map[string]any{"path": p, "offset": 2, "limit": 2})
	out, err := New().Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     2\t2") || !strings.Contains(out, "     3\t3") {
		t.Errorf("expected lines 2 and 3:\n%s", out)
	}
	if strings.Contains(out, "     4\t4") {
		t.Errorf("limit not enforced:\n%s", out)
	}
	if !strings.Contains(out, "TRUNCATED") {
		t.Errorf("expected truncation note:\n%s", out)
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

func TestRead_SchemaIsStable(t *testing.T) {
	// PRD §4.8.1: schema must be byte-identical across calls so DeepSeek's
	// prefix cache hits. We guarantee this with a package-level []byte.
	a := New().Schema()
	b := New().Schema()
	if string(a) != string(b) {
		t.Errorf("schema bytes drifted")
	}
}
