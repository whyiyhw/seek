package read

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
)

// testReadTool returns a read.Tool with a policy anchored to a temp dir
// so reads inside that dir are always allowed.
func testReadTool(t *testing.T) (Tool, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := permission.New(dir, permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	return New(p), dir
}

func writeFileIn(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead_Basic(t *testing.T) {
	tool, dir := testReadTool(t)
	p := writeFileIn(t, dir, "alpha\nbeta\ngamma\n")
	args, _ := json.Marshal(map[string]string{"path": p})
	out, err := tool.Execute(context.Background(), args)
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

func TestRead_LimitParamValidation(t *testing.T) {
	tool, dir := testReadTool(t)
	// Build a file with 400 LONG lines (well above the whole-read size
	// threshold and the default limit) so limit windowing applies.
	var sb strings.Builder
	for i := range 400 {
		fmt.Fprintf(&sb, "key_%03d = \"value-%d-%s\" // padding padding padding padding\n",
			i+1, i+1, strings.Repeat("x", 60))
	}
	p := writeFileIn(t, dir, sb.String())

	// Cases that should succeed.
	t.Run("valid", func(t *testing.T) {
		cases := []struct {
			name      string
			limit     int // -1 = omit from JSON
			wantLines int
		}{
			{"limit=200 (at cap)", 200, 200},
			{"limit=10 explicit", 10, 10},
			{"limit=1 explicit", 1, 1},
			{"limit=0 defaults to 200", 0, 200},
			{"no limit param defaults to 200", -1, 200},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				args := map[string]any{"path": p}
				if tc.limit >= 0 {
					args["limit"] = tc.limit
				}
				raw, _ := json.Marshal(args)
				out, err := tool.Execute(context.Background(), raw)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out, fmt.Sprintf("%d lines emitted", tc.wantLines)) {
					t.Errorf("expected %d lines emitted, got:\n%s", tc.wantLines, out)
				}
				// File has 400 lines; any limit < 400 should show TRUNCATED.
				if tc.wantLines < 400 && !strings.Contains(out, "TRUNCATED") {
					t.Errorf("expected TRUNCATED for limit=%d on 400-line file:\n%s", tc.limit, out)
				}
			})
		}
	})

	// Cases that should error.
	t.Run("over_max_errors", func(t *testing.T) {
		cases := []struct {
			name  string
			limit int
		}{
			{"limit=201", 201},
			{"limit=250", 250},
			{"limit=999", 999},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				raw, _ := json.Marshal(map[string]any{"path": p, "limit": tc.limit})
				_, err := tool.Execute(context.Background(), raw)
				if err == nil {
					t.Fatal("expected error for limit > 200")
				}
				if !strings.Contains(err.Error(), "exceeds maximum (200)") {
					t.Errorf("wrong error message: %v", err)
				}
			})
		}
	})
}

func TestRead_OffsetNavigates(t *testing.T) {
	tool, dir := testReadTool(t)
	p := writeFileIn(t, dir, "1\n2\n3\n4\n5\n")
	args, _ := json.Marshal(map[string]any{"path": p, "offset": 3})
	out, err := tool.Execute(context.Background(), args)
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
	_, err := Tool{}.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("err = %v", err)
	}
}

func TestRead_NotExist(t *testing.T) {
	tool, _ := testReadTool(t)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"/no/such/file.xyz"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRead_DirectoryDegradesToListing(t *testing.T) {
	dir := t.TempDir()
	p, err := permission.New(dir, permission.PrefDeny)
	if err != nil {
		t.Fatal(err)
	}
	tool := New(p)
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

	args, _ := json.Marshal(map[string]string{"path": dir})
	out, err := tool.Execute(context.Background(), args)
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
	// Build a file with 400 LONG lines (> 32 KiB whole-read threshold) —
	// reading without an explicit limit must stop at defaultMaxLimit and
	// emit a TRUNCATED notice.
	var sb strings.Builder
	total := 400
	for i := range total {
		fmt.Fprintf(&sb, "key_%03d = \"value-%d-%s\" // padding padding padding padding\n",
			i+1, i+1, strings.Repeat("x", 60))
	}
	tool, dir := testReadTool(t)
	p := writeFileIn(t, dir, sb.String())

	args, _ := json.Marshal(map[string]string{"path": p})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "TRUNCATED") {
		t.Errorf("expected TRUNCATED notice for %d-line file with default limit %d:\n%s", total, defaultMaxLimit, out)
	}
	// The last emitted line should be defaultMaxLimit, not total.
	wantLast := fmt.Sprintf("%6d\tkey_%03d", defaultMaxLimit, defaultMaxLimit)
	if !strings.Contains(out, wantLast) {
		t.Errorf("expected last emitted line %q:\n%s", wantLast, out)
	}
	unwanted := fmt.Sprintf("key_%03d", defaultMaxLimit+1)
	if strings.Contains(out, unwanted) {
		t.Errorf("output should not contain line beyond default limit: %s", out)
	}
}

func TestRead_SchemaIsStable(t *testing.T) {
	// PRD §4.8.1: schema must be byte-identical across calls so DeepSeek's
	// prefix cache hits. We guarantee this with a package-level []byte.
	a := Tool{}.Schema()
	b := Tool{}.Schema()
	if string(a) != string(b) {
		t.Errorf("schema bytes drifted")
	}
}
