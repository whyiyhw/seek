package upgrade

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxBinarySize caps the bytes we'll extract for the seek binary.
// Real builds are ~25MB; 128MB is well above worst case (debug info,
// future bloat) and well below "obvious garbage / DoS attempt" — if
// someone uploads a 2GB file as "seek.exe" we refuse to write it.
const maxBinarySize = 128 << 20

// extractBinary reads an archive (tar.gz, tgz, or zip — chosen from
// archiveName's extension) and writes the first entry whose basename
// matches wantName (case-insensitive) to dstPath. dstPath is created
// with mode 0o755 on Unix; on Windows the mode is ignored.
//
// Why basename matching: GoReleaser archives flatten to a single
// "seek" / "seek.exe" file at archive root, sometimes alongside
// README.md and LICENSE. We never want README.md as the target, so
// we filter on basename — directory traversal is irrelevant.
func extractBinary(archive io.Reader, archiveName, wantName, dstPath string) error {
	lower := strings.ToLower(archiveName)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(archive, wantName, dstPath)
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archive, wantName, dstPath)
	default:
		return fmt.Errorf("upgrade: unknown archive type %q", archiveName)
	}
}

func extractTarGz(r io.Reader, wantName, dstPath string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("upgrade: gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("upgrade: %q not found in archive", wantName)
		}
		if err != nil {
			return fmt.Errorf("upgrade: tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if !sameBasename(hdr.Name, wantName) {
			continue
		}
		return writeBinary(io.LimitReader(tr, maxBinarySize+1), dstPath)
	}
}

func extractZip(r io.Reader, wantName, dstPath string) error {
	// zip needs a ReaderAt — buffer to a temp file rather than RAM so
	// we don't hold the whole archive in memory.
	tmp, err := os.CreateTemp("", "seek-upgrade-zip-*")
	if err != nil {
		return fmt.Errorf("upgrade: tmp zip: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err := io.Copy(tmp, r); err != nil {
		return fmt.Errorf("upgrade: copy zip: %w", err)
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(tmp, info.Size())
	if err != nil {
		return fmt.Errorf("upgrade: zip open: %w", err)
	}
	for _, f := range zr.File {
		if f.Mode().IsDir() {
			continue
		}
		if !sameBasename(f.Name, wantName) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("upgrade: zip entry: %w", err)
		}
		err = writeBinary(io.LimitReader(rc, maxBinarySize+1), dstPath)
		_ = rc.Close()
		return err
	}
	return fmt.Errorf("upgrade: %q not found in zip", wantName)
}

// writeBinary copies r to dstPath as a fresh file with executable
// permissions. Refuses if r exceeds maxBinarySize (signaled by the
// LimitReader returning maxBinarySize+1 bytes). dstPath must NOT
// already exist — caller picks a fresh temp name.
func writeBinary(r io.Reader, dstPath string) error {
	f, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("upgrade: create %s: %w", dstPath, err)
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("upgrade: write %s: %w", dstPath, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("upgrade: close %s: %w", dstPath, closeErr)
	}
	if n > maxBinarySize {
		_ = os.Remove(dstPath)
		return fmt.Errorf("upgrade: binary exceeds %d bytes — refusing", maxBinarySize)
	}
	return nil
}

func sameBasename(path, want string) bool {
	return strings.EqualFold(filepath.Base(path), want)
}
