package methods

// fs/list tests (card c_heffj3 acceptance ①): the table drives the
// whole contract over one temp-dir fixture — default path = first
// browsable root, ~ expansion, the -32602 family (missing / file /
// outside-roots, including ../ escape and symlink-pointing-out, which
// must never reach os.ReadDir), hidden filtering, limit truncation,
// name-sorted stable order, parent converging to "" at the root.
//
// The home seam (Fs.home) pins ~ and the implicit HOME root to the
// fixture dir, so the suite is cross-platform (no real UserHomeDir)
// and hermetic (no real HOME touched).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/rpc"
)

// newFsEnv builds the fixture: rootA (sub-tree + files + hidden
// entries + symlink-out) registered into a fresh registry, all paths
// compared symlink-RESOLVED (t.TempDir on macOS lives under /var →
// /private/var, so lexical comparisons would flake).
type fsEnv struct {
	reg  *rpc.Registry
	root string // resolved rootA
}

func newFsEnv(t *testing.T) *fsEnv {
	t.Helper()
	t.Setenv("SEEK_HOME", t.TempDir()) // isolate memory.ListProjects()
	t.Setenv("SEEK_SESSIONS_DIR", "")
	base := t.TempDir()
	rootA := filepath.Join(base, "rootA")
	for _, d := range []string{
		filepath.Join(rootA, "sub1"),
		filepath.Join(rootA, "sub1", "sub2"),
		filepath.Join(rootA, "zdir"),
		filepath.Join(rootA, ".hidden-dir"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootA, "afile.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootA, ".hidden-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sibling OUTSIDE every root + a symlink inside rootA pointing at it
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootA, "escape-link")); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(rootA)
	if err != nil {
		t.Fatal(err)
	}
	fs := &Fs{
		projectRoots: []string{rootA},
		home:         func() (string, error) { return base, nil },
	}
	reg := rpc.NewRegistry()
	fs.RegisterAll(reg)
	return &fsEnv{reg: reg, root: root}
}

func (e *fsEnv) call(t *testing.T, params string) (fsListResult, error) {
	t.Helper()
	res, err := e.reg.Dispatch(context.Background(), "fs/list", json.RawMessage(params))
	if err != nil {
		return fsListResult{}, err
	}
	return res.(fsListResult), nil
}

func (e *fsEnv) names(r fsListResult) string {
	var names []string
	for _, en := range r.Entries {
		names = append(names, en.Name)
	}
	return strings.Join(names, ",")
}

func TestFsList(t *testing.T) {
	env := newFsEnv(t)
	outsideParent := filepath.Dir(env.root) // one level above rootA, outside every root

	cases := []struct {
		name    string
		params  string
		wantErr string // non-empty: -32602 whose message contains this
		check   func(t *testing.T, r fsListResult)
	}{
		{
			name:   "缺省 path 回首根（自描述起点），根上 parent 为空",
			params: `{}`,
			check: func(t *testing.T, r fsListResult) {
				if r.Path != env.root {
					t.Errorf("default path = %q, want first root %q", r.Path, env.root)
				}
				if r.Parent != "" {
					t.Errorf("root's parent = %q, want \"\"", r.Parent)
				}
				if r.Truncated {
					t.Error("small fixture must not truncate")
				}
			},
		},
		{
			name:   "~ 前缀拼出根内路径",
			params: `{"path":"~/rootA/sub1"}`,
			check: func(t *testing.T, r fsListResult) {
				if r.Path != filepath.Join(env.root, "sub1") {
					t.Errorf("~ path = %q, want %q", r.Path, filepath.Join(env.root, "sub1"))
				}
			},
		},
		{
			name:    "路径不存在（根内）→ -32602 不是目录或不存在",
			params:  `{"path":"` + filepath.Join(env.root, "nope") + `"}`,
			wantErr: "不是目录或目录不存在",
		},
		{
			name:    "路径是文件 → -32602",
			params:  `{"path":"` + filepath.Join(env.root, "afile.txt") + `"}`,
			wantErr: "不是目录或目录不存在",
		},
		{
			name:    "根外（同级目录）→ -32602 带逃生口文案",
			params:  `{"path":"` + outsideParent + `"}`,
			wantErr: "不在可浏览根内：编辑 ~/.seek/config.json 的 serve.project_roots",
		},
		{
			name:    "根外不存在路径 → 同样根外（根外无存在性预言）",
			params:  `{"path":"` + filepath.Join(outsideParent, "no-such-dir") + `"}`,
			wantErr: "不在可浏览根内",
		},
		{
			name:    "../.. 逃逸 → 根外",
			params:  `{"path":"` + env.root + `/../../"}`,
			wantErr: "不在可浏览根内",
		},
		{
			name:    "符号链接指向根外 → 根外（不落到 ReadDir）",
			params:  `{"path":"` + filepath.Join(env.root, "escape-link") + `"}`,
			wantErr: "不在可浏览根内",
		},
		{
			name:   "hidden 过滤 + 文件行带 is_dir=false + 排序稳定",
			params: `{"path":"` + env.root + `"}`,
			check: func(t *testing.T, r fsListResult) {
				if want := "afile.txt,escape-link,sub1,zdir"; env.names(r) != want { // byte order, no dot entries
					t.Errorf("names = %q, want %q", env.names(r), want)
				}
				for _, e := range r.Entries {
					if e.Name == "sub1" && !e.IsDir {
						t.Error("sub1 must report is_dir=true")
					}
					if e.Name == "afile.txt" && e.IsDir {
						t.Error("afile.txt must report is_dir=false")
					}
					if want := filepath.Join(r.Path, e.Name); e.Path != want {
						t.Errorf("entry path = %q, want %q", e.Path, want)
					}
				}
			},
		},
		{
			name:   "show_hidden 把点条目带回（同序）",
			params: `{"path":"` + env.root + `","show_hidden":true}`,
			check: func(t *testing.T, r fsListResult) {
				if want := ".hidden-dir,.hidden-file,afile.txt,escape-link,sub1,zdir"; env.names(r) != want {
					t.Errorf("names = %q, want %q", env.names(r), want)
				}
			},
		},
		{
			name:   "limit 截断 + truncated",
			params: `{"path":"` + env.root + `","limit":2}`,
			check: func(t *testing.T, r fsListResult) {
				if len(r.Entries) != 2 || !r.Truncated {
					t.Errorf("limit 2: got %d entries, truncated=%v", len(r.Entries), r.Truncated)
				}
			},
		},
		{
			name:   "limit 超上限按 1000 收（不报错）",
			params: `{"path":"` + env.root + `","limit":5000}`,
			check: func(t *testing.T, r fsListResult) {
				if len(r.Entries) != 4 || r.Truncated {
					t.Errorf("limit 5000: got %d entries, truncated=%v", len(r.Entries), r.Truncated)
				}
			},
		},
		{
			name:   "parent 逐级收敛到空",
			params: `{"path":"` + filepath.Join(env.root, "sub1", "sub2") + `"}`,
			check: func(t *testing.T, r fsListResult) {
				if r.Parent != filepath.Join(env.root, "sub1") {
					t.Fatalf("sub2 parent = %q", r.Parent)
				}
				up1, err := env.call(t, `{"path":"`+r.Parent+`"}`)
				if err != nil {
					t.Fatal(err)
				}
				if up1.Parent != env.root {
					t.Fatalf("sub1 parent = %q, want root", up1.Parent)
				}
				up2, err := env.call(t, `{"path":"`+up1.Parent+`"}`)
				if err != nil {
					t.Fatal(err)
				}
				if up2.Parent != "" {
					t.Errorf("root parent = %q, want \"\" (到根为止)", up2.Parent)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := env.call(t, tc.params)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want -32602 containing %q, got result %+v", tc.wantErr, r)
				}
				var e *rpc.Error
				if !errors.As(err, &e) {
					t.Fatalf("error = %T (%v), want *rpc.Error", err, err)
				}
				if e.Code != rpc.CodeInvalidParams {
					t.Errorf("code = %d, want -32602", e.Code)
				}
				if !strings.Contains(e.Message, tc.wantErr) {
					t.Errorf("message = %q, want containing %q", e.Message, tc.wantErr)
				}
				if len(r.Entries) != 0 { // 错误路径不落到 os.ReadDir：绝无 entries 可言
					t.Errorf("error case leaked %d entries", len(r.Entries))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, r)
			}
		})
	}
}

// TestFsList_HomeDefaultRoot: project_roots 为空时根集含 HOME，缺省
// path 落 HOME——页面的自描述起点，不需要知道服务端 HOME 在哪。
func TestFsList_HomeDefaultRoot(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_SESSIONS_DIR", "")
	base := t.TempDir()
	fs := &Fs{home: func() (string, error) { return base, nil }}
	reg := rpc.NewRegistry()
	fs.RegisterAll(reg)
	res, err := reg.Dispatch(context.Background(), "fs/list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	r := res.(fsListResult)
	want, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != want {
		t.Errorf("default path = %q, want HOME %q", r.Path, want)
	}
	if r.Parent != "" {
		t.Errorf("HOME root parent = %q, want \"\"", r.Parent)
	}
}

// TestFsList_MemoryRootReachable: memory.ListProjects 的每个 root 始终
// 可达（逃生口径：已有项目根不会被锁死，即使 HOME 与显式根都不覆盖）。
func TestFsList_MemoryRootReachable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SEEK_HOME", home)
	t.Setenv("SEEK_SESSIONS_DIR", "")
	projRoot := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectFixture(t, home, "0123456789abcdef", projRoot,
		time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
	fs := &Fs{home: func() (string, error) { return t.TempDir(), nil }} // HOME 别处：memory root 仍须可达
	reg := rpc.NewRegistry()
	fs.RegisterAll(reg)
	res, err := reg.Dispatch(context.Background(), "fs/list", json.RawMessage(`{"path":"`+projRoot+`"}`))
	if err != nil {
		t.Fatalf("memory root must stay reachable: %v", err)
	}
	r := res.(fsListResult)
	want, _ := filepath.EvalSymlinks(projRoot)
	if r.Path != want {
		t.Errorf("path = %q, want %q", r.Path, want)
	}
}
