// Package upgrade pulls newer release binaries from GitHub and
// replaces the running executable in-place. Stdlib-only, cross-
// platform (POSIX rename on Unix, rename-then-replace on Windows).
//
// Public entry point:
//
//	upgrade.Run(ctx, upgrade.Options{
//	    Owner: "whyiyhw", Repo: "seek",
//	    Current: tui.VersionString(),
//	    Stdout: os.Stdout,
//	})
//
// See cmd/seek/main.go for the wired-up CLI flag.
package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrAlreadyLatest is returned (or surfaced through Result.AlreadyLatest)
// when the current binary already matches the latest release. The CLI
// treats this as success and prints a friendly note.
var ErrAlreadyLatest = errors.New("already on latest version")

// Options controls Run / Check behaviour. Zero values are safe defaults.
type Options struct {
	// Owner / Repo identify the GitHub repo. Required.
	Owner, Repo string

	// Current is the running binary's version, e.g. "v0.9.0" or "dev".
	// IsDev(Current) returning true means "always upgrade" unless
	// AllowDev is false (then Run refuses to clobber a dev build).
	Current string

	// AllowDev, when true, lets the upgrade proceed even from a dev
	// build. Defaults to false because devs commonly run a local
	// build that's NEWER than the latest release; clobbering it would
	// be a footgun.
	AllowDev bool

	// HTTPClient is used for both API + download requests. Defaults
	// to a client with a 60s overall timeout and 30s response-header
	// timeout — enough for slow connections, short enough that a
	// hung mirror doesn't lock the CLI.
	HTTPClient *http.Client

	// APIBase overrides https://api.github.com (used by tests).
	APIBase string

	// GOOS / GOARCH override runtime detection (used by tests).
	GOOS, GOARCH string

	// ExePath overrides os.Executable() (used by tests).
	ExePath string

	// Stdout / Stderr receive progress + diagnostic output.
	// Defaults to os.Stdout / os.Stderr.
	Stdout, Stderr io.Writer

	// DryRun stops after downloading + verifying — no replace. Useful
	// for `seek -upgrade -dry-run` to vet a release before committing.
	DryRun bool

	// Token is a GitHub personal access token (or Actions GITHUB_TOKEN)
	// used to raise the API rate limit from 60/h per IP to 5000/h.
	// When empty, withDefaults reads GITHUB_TOKEN then GH_TOKEN.
	Token string
}

// Result captures the outcome of an upgrade run so callers (CLI,
// programmatic callers) can format output as they like.
type Result struct {
	AlreadyLatest bool
	From, To      string
	AssetName     string
	BytesWritten  int64
	Elapsed       time.Duration
	ExePath       string
	DryRun        bool
}

// Run is the orchestrator: resolve latest, compare versions, download
// asset, verify sha256, atomically replace. Each step writes a short
// status line to opt.Stderr so the user sees what's happening.
func Run(ctx context.Context, opt Options) (*Result, error) {
	opt = withDefaults(opt)
	start := time.Now()

	if IsDev(opt.Current) && !opt.AllowDev {
		return nil, fmt.Errorf("current build is %q (dev) — refusing to overwrite a local build; pass -upgrade-force to override",
			opt.Current)
	}

	exePath, err := resolveExePath(opt.ExePath)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(opt.Stderr, "upgrade: checking %s/%s for releases…\n", opt.Owner, opt.Repo)
	rel, err := fetchLatestRelease(ctx, opt.HTTPClient, opt.APIBase, opt.Owner, opt.Repo, opt.Token)
	if err != nil {
		return nil, err
	}

	if !IsDev(opt.Current) && compareSemver(opt.Current, rel.TagName) >= 0 {
		fmt.Fprintf(opt.Stderr, "upgrade: already on %s (latest is %s)\n", opt.Current, rel.TagName)
		return &Result{AlreadyLatest: true, From: opt.Current, To: rel.TagName, ExePath: exePath}, ErrAlreadyLatest
	}

	asset, err := pickAsset(rel.Assets, opt.GOOS, opt.GOARCH)
	if err != nil {
		return nil, fmt.Errorf("upgrade: release %s: %w", rel.TagName, err)
	}
	checksumAsset, err := pickChecksum(rel.Assets)
	if err != nil {
		return nil, fmt.Errorf("upgrade: release %s: %w", rel.TagName, err)
	}

	fmt.Fprintf(opt.Stderr, "upgrade: %s → %s (%s, %s)\n",
		displayVersion(opt.Current), rel.TagName, asset.Name, humanBytes(asset.Size))

	wantSum, err := downloadChecksum(ctx, opt.HTTPClient, opt.Token, checksumAsset, asset.Name)
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(exePath)
	binName := filepath.Base(exePath)
	tmpArchive, err := downloadAsset(ctx, opt.HTTPClient, opt.Token, asset, dir, wantSum)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpArchive)

	tmpBin, err := extractToTemp(tmpArchive, asset.Name, binName, dir)
	if err != nil {
		return nil, err
	}

	if opt.DryRun {
		_ = os.Remove(tmpBin)
		fmt.Fprintf(opt.Stderr, "upgrade: dry-run OK — checksum verified, would install %s\n", rel.TagName)
		return &Result{
			From: opt.Current, To: rel.TagName, AssetName: asset.Name,
			ExePath: exePath, Elapsed: time.Since(start), DryRun: true,
		}, nil
	}

	if err := replaceBinary(tmpBin, exePath); err != nil {
		_ = os.Remove(tmpBin)
		return nil, err
	}

	info, _ := os.Stat(exePath)
	var size int64
	if info != nil {
		size = info.Size()
	}
	fmt.Fprintf(opt.Stderr, "upgrade: installed %s at %s (%s)\n",
		rel.TagName, exePath, humanBytes(size))
	return &Result{
		From: opt.Current, To: rel.TagName, AssetName: asset.Name,
		BytesWritten: size, ExePath: exePath, Elapsed: time.Since(start),
	}, nil
}

// Check is a non-mutating version of Run: returns the newer release
// metadata or (nil, nil) if already up-to-date. Suitable for startup
// "new version available" nudges. Uses Options.HTTPClient if provided,
// which lets callers set tight timeouts when this runs in the
// foreground at startup.
func Check(ctx context.Context, opt Options) (*ghRelease, error) {
	opt = withDefaults(opt)
	rel, err := fetchLatestRelease(ctx, opt.HTTPClient, opt.APIBase, opt.Owner, opt.Repo, opt.Token)
	if err != nil {
		return nil, err
	}
	if !IsDev(opt.Current) && compareSemver(opt.Current, rel.TagName) >= 0 {
		return nil, nil
	}
	return rel, nil
}

func withDefaults(opt Options) Options {
	if opt.HTTPClient == nil {
		opt.HTTPClient = &http.Client{
			Timeout: 120 * time.Second,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		}
	}
	if opt.APIBase == "" {
		opt.APIBase = githubAPIBase
	}
	if opt.GOOS == "" {
		opt.GOOS = runtime.GOOS
	}
	if opt.GOARCH == "" {
		opt.GOARCH = runtime.GOARCH
	}
	if opt.Stdout == nil {
		opt.Stdout = os.Stdout
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}
	if opt.Token == "" {
		opt.Token = githubTokenFromEnv()
	}
	return opt
}

// githubTokenFromEnv returns a GitHub API token from the environment.
// GITHUB_TOKEN wins over GH_TOKEN — matching gh CLI and Actions convention.
// Whitespace is trimmed to guard against quoting accidents in env files.
func githubTokenFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("GH_TOKEN"))
}

// resolveExePath returns the absolute path of the running binary
// with symlinks resolved. Honours an override for testing.
func resolveExePath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("upgrade: locate self: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// A broken symlink shouldn't be fatal — fall back to the raw
		// path. Writing to the symlink itself is then the behaviour
		// the user gets, which is recoverable.
		return exe, nil
	}
	return resolved, nil
}

// downloadChecksum fetches the checksums.txt asset, parses it, and
// returns the expected sha256 for binAssetName.
func downloadChecksum(ctx context.Context, client *http.Client, token string, a *ghAsset, binAssetName string) (string, error) {
	body, err := httpGet(ctx, client, a.BrowserDownloadURL, token)
	if err != nil {
		return "", fmt.Errorf("upgrade: fetch checksums: %w", err)
	}
	defer body.Close()
	sums, err := parseChecksums(body)
	if err != nil {
		return "", err
	}
	sum, ok := sums[binAssetName]
	if !ok {
		return "", fmt.Errorf("upgrade: checksums.txt has no entry for %s", binAssetName)
	}
	return sum, nil
}

// downloadAsset streams the asset to a temp file in dir while hashing
// it; if the final sha256 doesn't match wantSum, the temp file is
// removed and an error is returned. dir MUST be the same directory
// the binary will eventually be renamed into — same-filesystem rename
// is what makes the replace atomic.
func downloadAsset(ctx context.Context, client *http.Client, token string, a *ghAsset, dir, wantSum string) (string, error) {
	body, err := httpGet(ctx, client, a.BrowserDownloadURL, token)
	if err != nil {
		return "", fmt.Errorf("upgrade: fetch asset: %w", err)
	}
	defer body.Close()

	tmp, err := os.CreateTemp(dir, "seek-upgrade-asset-*")
	if err != nil {
		return "", fmt.Errorf("upgrade: tmp asset: %w", err)
	}
	hr := newHashingReader(body)
	n, copyErr := io.Copy(tmp, hr)
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("upgrade: download: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("upgrade: close tmp: %w", closeErr)
	}
	got := hr.Sum()
	if !strings.EqualFold(got, wantSum) {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("upgrade: sha256 mismatch (got %s, want %s) — refusing", got, wantSum)
	}
	// Sanity check the size we got matches what GitHub advertised.
	// A mid-stream truncation that still happens to hash correctly is
	// vanishingly unlikely, but cheap to assert.
	if a.Size > 0 && n != a.Size {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("upgrade: size mismatch (got %d, want %d)", n, a.Size)
	}
	return tmp.Name(), nil
}

// extractToTemp pulls the seek binary out of archivePath into a fresh
// temp file in dir. Returns the temp file path; caller is responsible
// for either replaceBinary'ing it onto the target or removing it.
func extractToTemp(archivePath, assetName, binName, dir string) (string, error) {
	src, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("upgrade: open archive: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp(dir, "seek-upgrade-bin-*")
	if err != nil {
		return "", fmt.Errorf("upgrade: tmp bin: %w", err)
	}
	tmpName := tmp.Name()
	// extractBinary opens its own file at tmpName (it needs O_EXCL),
	// so close + remove the placeholder first.
	_ = tmp.Close()
	_ = os.Remove(tmpName)

	if err := extractBinary(src, assetName, binName, tmpName); err != nil {
		return "", err
	}
	return tmpName, nil
}

// httpGet does a GET (with User-Agent and optional Bearer auth headers
// set via setGitHubRequestHeaders) and returns the body. Caller closes
// it. Non-2xx maps to an error containing the status text — most useful
// for "404 asset moved".
func httpGet(ctx context.Context, client *http.Client, url, token string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setGitHubRequestHeaders(req, token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %s for %s", resp.Status, url)
	}
	return resp.Body, nil
}

func displayVersion(v string) string {
	if IsDev(v) {
		return "dev"
	}
	return v
}

func humanBytes(n int64) string {
	const (
		_  = iota
		kb = 1 << (10 * iota)
		mb
		gb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// sha256Hex is a small helper used by tests. Lives in non-test code so
// it's available to anything wanting a hex digest of a known buffer.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
