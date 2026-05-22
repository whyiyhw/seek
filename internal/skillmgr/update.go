package skillmgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/whyiyhw/seek/internal/skill"
)

// UpdateOptions controls Update and UpdateAll. Name selects a single
// skill (Update only); empty Name + UpdateAll walks the user dir.
type UpdateOptions struct {
	Name    string
	UserDir string // overrides paths.UserSkills() — tests inject
	HTTP    *http.Client
	Now     func() time.Time
}

// UpdateResult records one update attempt. Err is non-nil when the
// re-fetch failed — UpdateAll collects per-skill errors here so a
// single broken source doesn't abort the rest of the sweep.
type UpdateResult struct {
	Name string
	Path string // absolute path of the re-installed skill (empty on Err)
	Err  error
}

// Update re-fetches a single installed skill using the source
// recorded in its .install.json. Skills without a sidecar are
// refused with a clear error (manual `cp -r` installs can't be
// auto-updated — by design).
//
// Update is implemented as "read sidecar, rebuild InstallOptions,
// Install with Force=true". This means the install pipeline's
// failure modes (network down, missing SKILL.md, etc.) apply
// unchanged — and importantly, a failed update leaves the existing
// install in place (Install only RemoveAlls the target after
// validation succeeds).
func Update(opts UpdateOptions) (*UpdateResult, error) {
	if opts.Name == "" {
		return nil, errors.New("skillmgr: update: Name is required (use UpdateAll for all)")
	}
	userDir, err := resolveUserDir(opts.UserDir)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(userDir, opts.Name)
	if _, err := os.Stat(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("skillmgr: %q not found in %s", opts.Name, userDir)
		}
		return nil, fmt.Errorf("skillmgr: stat %s: %w", target, err)
	}

	src, err := readSidecarStrict(target)
	if err != nil {
		return nil, err
	}

	installOpts := installOptionsFromSidecar(src, opts)
	res, err := Install(installOpts)
	if err != nil {
		return nil, err
	}
	return &UpdateResult{Name: res.Name, Path: res.Dir}, nil
}

// UpdateAll runs Update on every installed skill in UserDir that has
// an .install.json. Returns one result per skill attempted; the
// top-level error is reserved for setup failures (e.g. cannot list
// the user dir). Per-skill failures live in UpdateResult.Err.
func UpdateAll(opts UpdateOptions) ([]UpdateResult, error) {
	userDir, err := resolveUserDir(opts.UserDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(userDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // empty user dir is fine
		}
		return nil, fmt.Errorf("skillmgr: list %s: %w", userDir, err)
	}
	// Stable order so output is reproducible — useful for both
	// tests and humans reading the CLI summary.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var results []UpdateResult
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only directories with a sidecar are eligible — manual
		// installs (no sidecar) are intentionally skipped, NOT
		// errored, so `update --all` is a no-op for them.
		sidecar := filepath.Join(userDir, e.Name(), ".install.json")
		if _, err := os.Stat(sidecar); err != nil {
			continue
		}

		sub := opts
		sub.Name = e.Name()
		res, err := Update(sub)
		out := UpdateResult{Name: e.Name(), Err: err}
		if res != nil {
			out.Path = res.Path
		}
		results = append(results, out)
	}
	return results, nil
}

// readSidecarStrict is like the loader's readInstallSource but treats
// missing-file as an error (Update has nothing to do without it).
func readSidecarStrict(dir string) (*skill.InstallSource, error) {
	path := filepath.Join(dir, ".install.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("skillmgr: %s has no install record (.install.json); manual installs cannot be auto-updated", dir)
		}
		return nil, err
	}
	var src skill.InstallSource
	if err := json.Unmarshal(data, &src); err != nil {
		return nil, fmt.Errorf("skillmgr: parse %s: %w", path, err)
	}
	return &src, nil
}

// installOptionsFromSidecar reconstructs the InstallOptions needed
// to re-fetch a skill from the values recorded at install time.
// Force is always true — by definition we're replacing the existing
// installation.
func installOptionsFromSidecar(src *skill.InstallSource, base UpdateOptions) InstallOptions {
	io := InstallOptions{
		Source:  src.URL,
		Force:   true,
		Subpath: src.Subpath,
		SHA256:  src.ChecksumSHA256,
		UserDir: base.UserDir,
		HTTP:    base.HTTP,
		Now:     base.Now,
	}
	switch src.Type {
	case "local":
		io.Type = SourceLocal
	case "git":
		io.Type = SourceGit
		// Re-attach the ref fragment so detectSourceType (and the
		// git fetcher's splitRefFragment) get the same input shape
		// they had at install time.
		if src.Ref != "" {
			io.Source = src.URL + "#" + src.Ref
		}
	case "https":
		io.Type = SourceHTTPS
	default:
		io.Type = SourceAuto
	}
	return io
}
