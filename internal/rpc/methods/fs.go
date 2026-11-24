package methods

// The fs method set (card c_heffj3): fs/list — read-only directory
// enumeration for the web /bind directory picker. The page had no way
// to LIST directories (every other family here is session/memory/
// project/stats/hooks/subagent state), so /bind candidates were
// type-by-hand. This is the enumeration half of the fix; the picker UI
// lives in webfrontend.html.
//
// Security surface (the card's口径, spelled out): a path is listable
// only under a browsable root — serve.project_roots ∪ serve.projects ∪
// every memory project root, plus HOME when project_roots is empty.
// /bind itself already validates and echoes existence for ARBITRARY
// paths, so this method adds no new ability to touch the filesystem —
// the new surface is enumeration only, hence the tighter root set:
// fail-closed, with the escape hatch written into the error message
// (existing project roots always stay reachable, so current usage is
// never locked out). A /srv-style deployment widens reach with one
// config line, not code.
//
// Read-only and depth-1 by contract: names and is_dir only, never file
// contents, never a recursive walk — drilling down is the caller's
// navigation, one fs/list per level.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/whyiyhw/seek/internal/memory"
	"github.com/whyiyhw/seek/internal/rpc"
)

const (
	fsDefaultLimit = 200
	fsMaxLimit     = 1000
)

// Fs is the fs/list family over the serve section's root lists. Both
// slices may be nil (no serve section); memory roots resolve per call
// through the env ladder (memory.ListProjects — the tolerant read-only
// walk project/list uses, no side effects).
type Fs struct {
	projectRoots []string
	projects     []string

	// home is a test seam over os.UserHomeDir (both ~ expansion and the
	// implicit HOME root); nil in production wiring.
	home func() (string, error)
}

// NewFs binds the family to serve.project_roots and serve.projects.
func NewFs(projectRoots, projects []string) *Fs {
	return &Fs{projectRoots: projectRoots, projects: projects}
}

func (f *Fs) userHome() (string, error) {
	if f.home != nil {
		return f.home()
	}
	return os.UserHomeDir()
}

// RegisterAll wires the one method into r.
func (f *Fs) RegisterAll(r *rpc.Registry) {
	r.Register("fs/list", f.list)
}

type fsListParams struct {
	Path       string `json:"path"`
	ShowHidden bool   `json:"show_hidden"`
	Limit      int    `json:"limit"`
}

type fsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

type fsListResult struct {
	Path      string    `json:"path"`
	Parent    string    `json:"parent,omitempty"` // "" at a browsable root — the picker's ↑ stops there
	Entries   []fsEntry `json:"entries"`          // never null
	Truncated bool      `json:"truncated"`
}

// fs/list — one directory's entries, name-sorted, names and types
// only. path absent = the default browsable root (self-describing
// start; the page never needs to know the server's HOME).
func (f *Fs) list(ctx context.Context, params json.RawMessage) (any, error) {
	var pr fsListParams
	if err := decodeParams(params, &pr); err != nil {
		return nil, err
	}
	roots, def, err := f.roots()
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, rpc.Errorf(rpc.CodeInvalidParams,
			"没有可浏览根：编辑 ~/.seek/config.json 的 serve.project_roots 配置")
	}
	raw := pr.Path
	if raw == "" {
		raw = def
	}
	abs, err := f.expand(raw)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInvalidParams, "路径无法解析：%v", err)
	}
	// Membership before existence: a path outside the roots answers 根外
	// whether or not it exists (no existence oracle beyond the roots),
	// and os.ReadDir is never reached for it.
	member := abs
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		member = resolved
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return nil, rpc.Errorf(rpc.CodeInternalError, "解析路径失败：%v", rerr)
	}
	if underRoot(member, roots) == "" {
		return nil, rpc.Errorf(rpc.CodeInvalidParams,
			"不在可浏览根内：编辑 ~/.seek/config.json 的 serve.project_roots 可扩大范围（本路径：%s）", abs)
	}
	listPath := member
	fi, err := os.Stat(listPath)
	if err != nil || !fi.IsDir() {
		return nil, rpc.Errorf(rpc.CodeInvalidParams, "不是目录或目录不存在：%s", abs)
	}
	limit := pr.Limit
	if limit <= 0 {
		limit = fsDefaultLimit
	}
	if limit > fsMaxLimit {
		limit = fsMaxLimit
	}
	dirEntries, err := os.ReadDir(listPath)
	if err != nil {
		return nil, err // plain error; the registry maps it to -32603
	}
	out := make([]fsEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if !pr.ShowHidden && strings.HasPrefix(de.Name(), ".") {
			continue
		}
		// IsDir is lstat-shaped: a symlink reads as a non-dir entry,
		// so the picker never descends through one — containment above
		// stays the whole security story.
		out = append(out, fsEntry{
			Name:  de.Name(),
			Path:  filepath.Join(listPath, de.Name()),
			IsDir: de.IsDir(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name }) // byte order, pinned by test
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	parent := ""
	if root := underRoot(listPath, roots); root == "" || listPath != root {
		parent = filepath.Dir(listPath)
	}
	return fsListResult{Path: listPath, Parent: parent, Entries: out, Truncated: truncated}, nil
}

// roots resolves the browsable root set: serve.project_roots (explicit
// — suppresses the implicit HOME) ∪ serve.projects ∪ memory project
// roots, each ~-expanded then symlink-resolved, deduped in that order.
// def is the default listing target: HOME when it's in the set (the
// page's self-describing start), else the first root. Roots that don't
// resolve on disk are skipped — nothing to browse into.
func (f *Fs) roots() (roots []string, def string, err error) {
	var raw []string
	raw = append(raw, f.projectRoots...)
	explicit := len(f.projectRoots) > 0
	raw = append(raw, f.projects...)
	projs, err := memory.ListProjects()
	if err != nil {
		return nil, "", err
	}
	for _, p := range projs {
		raw = append(raw, p.AbsPath)
	}
	homeRaw := ""
	if !explicit {
		if home, herr := f.userHome(); herr == nil {
			raw = append(raw, home)
			homeRaw = home
		}
	}
	seen := map[string]bool{}
	for _, r := range raw {
		abs, err := f.expand(r)
		if err != nil {
			continue // unresolvable config entry: not browsable, skipped
		}
		res, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue // missing root: nothing to browse into
		}
		if seen[res] {
			continue
		}
		seen[res] = true
		roots = append(roots, res)
	}
	if homeRaw != "" {
		if abs, err := f.expand(homeRaw); err == nil {
			if res, err := filepath.EvalSymlinks(abs); err == nil && seen[res] {
				def = res
			}
		}
	}
	if def == "" && len(roots) > 0 {
		def = roots[0]
	}
	return roots, def, nil
}

// expand resolves a leading ~ against the home dir, then Abs/Clean.
// ~user is not supported (the seam only knows the current user) and
// stays literal — such a path simply won't be under any root.
func (f *Fs) expand(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := f.userHome()
		if err != nil {
			return "", err
		}
		if p == "~" {
			p = home
		} else {
			p = filepath.Join(home, p[2:])
		}
	}
	return filepath.Abs(p)
}

// underRoot reports which browsable root p sits under (exact match or
// separator-prefixed); "" when none. Both sides must arrive
// symlink-resolved — the caller resolves p, roots() resolves the roots.
func underRoot(p string, roots []string) string {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return r
		}
	}
	return ""
}
