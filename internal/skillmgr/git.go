package skillmgr

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// stageGit clones opts.Source into staging, optionally promotes
// opts.Subpath to the staging root, and strips the .git directory.
// Refs encoded as a URL fragment (`#v1.2.0`, `#main`, `#<sha>`) are
// honoured. Tag and branch refs use `--depth=1 --branch`, which is
// cheap; sha refs aren't supported by --branch and would require a
// full clone — v2 deliberately punts on that (PRD §10 risk row).
//
// The fetch shells out to the user's git binary so SSH keys / netrc /
// credential helpers all work out of the box. If git isn't on PATH
// the caller gets a clear "git not installed" error rather than a
// vague exec failure.
func stageGit(opts InstallOptions, staging string) error {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("git binary not found on PATH (install git or use a tarball URL instead)")
	}

	url, ref := splitRefFragment(opts.Source)

	args := []string{"clone", "--depth=1", "--quiet"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, url, staging)

	cmd := exec.Command(gitBin, args...)
	// Mute git's interactive credential prompts so a missing
	// auth setup fails fast instead of hanging.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=true", // returns no password silently
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Surface git's stderr — its messages are usually
		// informative ("Repository not found", "could not read
		// from remote", "remote branch X not found").
		return fmt.Errorf("git clone %s%s failed: %w\n%s",
			url, refSuffix(ref), err, strings.TrimSpace(string(out)))
	}

	// Strip .git/ — skills shouldn't ship version control state.
	if err := os.RemoveAll(filepath.Join(staging, ".git")); err != nil {
		return fmt.Errorf("strip .git: %w", err)
	}

	// Subpath: move <staging>/<subpath>/* to <staging>/, drop everything else.
	if opts.Subpath != "" {
		if err := applySubpath(staging, opts.Subpath); err != nil {
			return err
		}
	}
	return nil
}

// splitRefFragment splits a source URL into (url, ref) on the last
// `#`. `https://example.com/foo#v1.2.0` → (`https://example.com/foo`, `v1.2.0`).
// URLs without a fragment yield an empty ref.
func splitRefFragment(s string) (string, string) {
	if i := strings.LastIndex(s, "#"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func refSuffix(ref string) string {
	if ref == "" {
		return ""
	}
	return "#" + ref
}

// applySubpath rearranges staging so that <staging>/<subpath>/ contents
// sit at <staging>/, with everything outside the subpath discarded.
// The implementation is "rename inside, sweep outside" so a partial
// failure leaves a recognisable mess instead of an empty directory.
func applySubpath(staging, sub string) error {
	subAbs := filepath.Join(staging, sub)
	info, err := os.Stat(subAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("subpath %q not found in repository", sub)
		}
		return fmt.Errorf("subpath stat: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("subpath %q is not a directory", sub)
	}

	// Move subpath out of the way (so we can wipe staging without
	// nuking it), wipe staging, move subpath contents back.
	parked, err := os.MkdirTemp(filepath.Dir(staging), "seek-subpath-*")
	if err != nil {
		return fmt.Errorf("park subpath: %w", err)
	}
	parkedTarget := filepath.Join(parked, "x")
	if err := os.Rename(subAbs, parkedTarget); err != nil {
		_ = os.RemoveAll(parked)
		return fmt.Errorf("rename subpath aside: %w", err)
	}

	// Wipe the rest of staging.
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(staging, e.Name())); err != nil {
			return err
		}
	}

	// Move parked contents back into staging.
	innerEntries, err := os.ReadDir(parkedTarget)
	if err != nil {
		return err
	}
	for _, e := range innerEntries {
		from := filepath.Join(parkedTarget, e.Name())
		to := filepath.Join(staging, e.Name())
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("restore %s: %w", e.Name(), err)
		}
	}
	_ = os.RemoveAll(parked)
	return nil
}
