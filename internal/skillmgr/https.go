package skillmgr

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// stageHTTPS downloads opts.Source, optionally verifies its sha256,
// and extracts the archive into staging. The caller (Install) then
// validates the staged contents and proceeds with the rest of the
// install pipeline.
//
// Archive format is decided by URL suffix (case-insensitive, query
// and fragment stripped). Content-Type would be more "correct" but
// real-world CDNs serve archives with octet-stream or just plain
// text/plain, so the suffix is more reliable in practice.
func stageHTTPS(opts InstallOptions, staging string) error {
	client := opts.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	// Download to a temp file in the same parent as staging so the
	// later extraction step doesn't span filesystems unnecessarily.
	tmpFile, err := os.CreateTemp(filepath.Dir(staging), "seek-archive-*")
	if err != nil {
		return fmt.Errorf("create temp archive: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	resp, err := client.Get(opts.Source)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", opts.Source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP status %d", opts.Source, resp.StatusCode)
	}

	// Tee the response body through a sha256 hasher so we don't have
	// to read the file twice. The hash is consumed only when
	// opts.SHA256 is non-empty.
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmpFile, h), resp.Body); err != nil {
		return fmt.Errorf("download body: %w", err)
	}

	if opts.SHA256 != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, opts.SHA256) {
			return fmt.Errorf("sha256 mismatch: want %s, got %s", opts.SHA256, got)
		}
	}

	// Rewind for the extractor.
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}

	switch detectArchiveFormat(opts.Source) {
	case archiveZip:
		// archive/zip needs the file size up front (random-access
		// reader). We've got the *os.File; stat it.
		info, err := tmpFile.Stat()
		if err != nil {
			return err
		}
		if err := extractZip(tmpFile, info.Size(), staging); err != nil {
			return err
		}
	case archiveTarGz:
		gr, err := gzip.NewReader(tmpFile)
		if err != nil {
			return fmt.Errorf("gzip decode: %w", err)
		}
		defer gr.Close()
		if err := extractTar(tar.NewReader(gr), staging); err != nil {
			return err
		}
	case archiveTar:
		if err := extractTar(tar.NewReader(tmpFile), staging); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unrecognised archive format for %s (expected .tar.gz / .tgz / .tar / .zip)", opts.Source)
	}

	// GitHub-style archives nest everything under a top-level dir
	// like `<repo>-<version>/`. Promote that contents to the staging
	// root so SKILL.md ends up where the loader looks for it.
	return stripSingleLeadingDir(staging)
}

type archiveFormat int

const (
	archiveUnknown archiveFormat = iota
	archiveTar
	archiveTarGz
	archiveZip
)

func detectArchiveFormat(src string) archiveFormat {
	lower := strings.ToLower(src)
	// Strip query/fragment — `?token=` and `#ref` mustn't fool the
	// suffix match.
	if u, err := url.Parse(lower); err == nil {
		lower = u.Path
	}
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return archiveZip
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return archiveTarGz
	case strings.HasSuffix(lower, ".tar"):
		return archiveTar
	}
	return archiveUnknown
}

// extractTar writes the contents of a tar reader into dest. Refuses
// entries whose resolved path escapes dest (zip-slip / tar-slip
// protection); the install pipeline then leaves dest empty.
func extractTar(tr *tar.Reader, dest string) error {
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			// Cap copy size at MaxInt64 — tar headers can lie. Use
			// io.CopyN bounded to hdr.Size to avoid DoS via a tar
			// entry that claims to be 4 GiB.
			if _, err := io.CopyN(f, tr, hdr.Size); err != nil && err != io.EOF {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Skip symlinks / device nodes / FIFOs — skills are
			// regular files only.
		}
	}
}

// extractZip writes the contents of a zip archive into dest. Same
// safety rules as extractTar.
func extractZip(r io.ReaderAt, size int64, dest string) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("zip open: %w", err)
	}
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			_ = rc.Close()
			_ = out.Close()
			return err
		}
		_ = rc.Close()
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

// safeJoin builds dest+rel and verifies the result stays inside
// dest. Refuses absolute paths and `..`-traversal entries.
//
// This is the load-bearing defence against tar-slip / zip-slip
// when installing skills from untrusted archives. Without it, a
// malicious entry like `../../../etc/passwd` would write outside the
// intended directory.
func safeJoin(dest, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe archive entry %q: absolute path", rel)
	}
	clean := filepath.Clean(rel)
	if strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("unsafe archive entry %q: escapes destination", rel)
	}
	joined := filepath.Join(dest, clean)
	// Defence in depth: even after Clean+Join, verify the joined
	// path doesn't reach above dest (handles edge cases on Windows
	// and weird symlink configurations).
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(absJoined, absDest+string(filepath.Separator)) && absJoined != absDest {
		return "", fmt.Errorf("unsafe archive entry %q: lands outside %s", rel, dest)
	}
	return joined, nil
}

// stripSingleLeadingDir is a quality-of-life pass run after archive
// extraction: if the staging dir contains exactly one subdirectory
// and no other files, promote its contents up. Idempotent — does
// nothing when the archive was already flat.
//
// Why: GitHub release tarballs / "Download ZIP" archives wrap
// everything under `<repo>-<sha>/`. Without this step, SKILL.md
// would never be at the staging root and the install would fail.
func stripSingleLeadingDir(staging string) error {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return nil // archive was already flat or had multiple top-level entries
	}
	inner := filepath.Join(staging, entries[0].Name())
	inners, err := os.ReadDir(inner)
	if err != nil {
		return err
	}
	for _, e := range inners {
		from := filepath.Join(inner, e.Name())
		to := filepath.Join(staging, e.Name())
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("promote %s: %w", e.Name(), err)
		}
	}
	if err := os.Remove(inner); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
