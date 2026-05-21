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

func TestRead_Directory(t *testing.T) {
	dir := t.TempDir()
	_, err := New().Execute(context.Background(), json.RawMessage(`{"path":"`+dir+`"}`))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("err = %v", err)
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
