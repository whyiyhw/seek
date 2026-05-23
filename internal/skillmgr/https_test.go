package skillmgr

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTarGz constructs a tar.gz archive in memory. files maps path
// (relative inside the archive) → contents. Used as test fixture for
// the HTTPS tarball install path so we never hit real network or
// disk.
func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serveBytes(t *testing.T, urlPath string, payload []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(urlPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const validSKILL = "---\nname: tarball-skill\ndescription: from tarball\n---\n\n# Body\n"

// ---------- tar.gz ----------

func TestInstall_HTTPS_TarGz_HappyPath(t *testing.T) {
	payload := buildTarGz(t, map[string]string{
		"SKILL.md":          validSKILL,
		"references/api.md": "# refs",
	})
	srv := serveBytes(t, "/foo.tar.gz", payload)

	userDir := t.TempDir()
	res, err := Install(InstallOptions{
		Source:  srv.URL + "/foo.tar.gz",
		UserDir: userDir,
		HTTP:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "tarball-skill" {
		t.Errorf("Name = %q, want tarball-skill", res.Name)
	}
	if res.Type != SourceHTTPS {
		t.Errorf("Type = %v, want SourceHTTPS", res.Type)
	}
	// Both top-level and nested files must have landed.
	if _, err := os.Stat(filepath.Join(res.Dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "references", "api.md")); err != nil {
		t.Errorf("nested references/api.md missing: %v", err)
	}
}

func TestInstall_HTTPS_TarGz_StripsLeadingDirectory(t *testing.T) {
	// GitHub release tarballs wrap everything under <repo>-<version>/.
	// The installer should detect a single top-level directory and
	// promote its contents so SKILL.md ends up at the package root.
	payload := buildTarGz(t, map[string]string{
		"repo-1.2.3/SKILL.md":          validSKILL,
		"repo-1.2.3/references/api.md": "# refs",
	})
	srv := serveBytes(t, "/foo.tar.gz", payload)

	userDir := t.TempDir()
	res, err := Install(InstallOptions{
		Source:  srv.URL + "/foo.tar.gz",
		UserDir: userDir,
		HTTP:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not promoted to root: %v", err)
	}
}

func TestInstall_HTTPS_TarGz_SHA256_Match(t *testing.T) {
	payload := buildTarGz(t, map[string]string{"SKILL.md": validSKILL})
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])
	srv := serveBytes(t, "/x.tar.gz", payload)

	_, err := Install(InstallOptions{
		Source:  srv.URL + "/x.tar.gz",
		UserDir: t.TempDir(),
		SHA256:  want,
		HTTP:    srv.Client(),
	})
	if err != nil {
		t.Fatalf("expected install to succeed with matching sha256, got %v", err)
	}
}

func TestInstall_HTTPS_SHA256_Mismatch_NoFSWrites(t *testing.T) {
	payload := buildTarGz(t, map[string]string{"SKILL.md": validSKILL})
	srv := serveBytes(t, "/x.tar.gz", payload)
	userDir := t.TempDir()

	_, err := Install(InstallOptions{
		Source:  srv.URL + "/x.tar.gz",
		UserDir: userDir,
		SHA256:  "00" + strings.Repeat("00", 31), // 64 zero bytes — won't match
		HTTP:    srv.Client(),
	})
	if err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if !strings.Contains(err.Error(), "sha256") {
		t.Errorf("err = %v, want it to mention sha256", err)
	}
	// PRD v2 §7 #3: failed checksum leaves no on-disk artefact.
	entries, _ := os.ReadDir(userDir)
	if len(entries) != 0 {
		t.Errorf("userDir should be empty on checksum failure, got entries: %v", entries)
	}
}

func TestInstall_HTTPS_TarGz_NoSKILLMd(t *testing.T) {
	// Archive without a SKILL.md / skill.md at the root (or under a
	// single leading dir) is rejected with a clear error.
	payload := buildTarGz(t, map[string]string{
		"README.md":   "# not a skill",
		"src/main.go": "package main",
	})
	srv := serveBytes(t, "/bogus.tar.gz", payload)
	userDir := t.TempDir()

	_, err := Install(InstallOptions{
		Source:  srv.URL + "/bogus.tar.gz",
		UserDir: userDir,
		HTTP:    srv.Client(),
	})
	if err == nil {
		t.Fatal("expected error for tarball without SKILL.md")
	}
	entries, _ := os.ReadDir(userDir)
	if len(entries) != 0 {
		t.Errorf("userDir should be empty, got: %v", entries)
	}
}

func TestInstall_HTTPS_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	userDir := t.TempDir()
	_, err := Install(InstallOptions{
		Source:  srv.URL + "/x.tar.gz",
		UserDir: userDir,
		HTTP:    srv.Client(),
	})
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "status") {
		t.Errorf("err = %v, want it to surface the HTTP status", err)
	}
}

// ---------- zip ----------

func TestInstall_HTTPS_Zip_HappyPath(t *testing.T) {
	payload := buildZip(t, map[string]string{
		"SKILL.md":          validSKILL,
		"references/api.md": "# refs",
	})
	srv := serveBytes(t, "/foo.zip", payload)

	userDir := t.TempDir()
	res, err := Install(InstallOptions{
		Source:  srv.URL + "/foo.zip",
		UserDir: userDir,
		HTTP:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "references", "api.md")); err != nil {
		t.Errorf("references/api.md missing: %v", err)
	}
}

func TestInstall_HTTPS_Zip_StripsLeadingDirectory(t *testing.T) {
	payload := buildZip(t, map[string]string{
		"my-repo-main/SKILL.md": validSKILL,
		"my-repo-main/extra.md": "# extra",
	})
	srv := serveBytes(t, "/foo.zip", payload)

	userDir := t.TempDir()
	res, err := Install(InstallOptions{
		Source:  srv.URL + "/foo.zip",
		UserDir: userDir,
		HTTP:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not promoted to root: %v", err)
	}
}

// ---------- safety: path traversal ----------

func TestInstall_HTTPS_TarGz_RejectsZipSlip(t *testing.T) {
	// Hand-craft a tar containing a path-traversal entry. archive/tar
	// happily writes it; the installer must refuse to extract.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	evil := "../../../../etc/seek-pwned"
	hdr := &tar.Header{Name: evil, Mode: 0o644, Size: int64(len("pwn"))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("pwn")); err != nil {
		t.Fatal(err)
	}
	// Also include a valid SKILL.md so the failure is unambiguously
	// caused by the traversal entry, not by missing-skill validation.
	hdr2 := &tar.Header{Name: "SKILL.md", Mode: 0o644, Size: int64(len(validSKILL))}
	if err := tw.WriteHeader(hdr2); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(validSKILL)); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()

	srv := serveBytes(t, "/evil.tar.gz", buf.Bytes())
	userDir := t.TempDir()
	_, err := Install(InstallOptions{
		Source:  srv.URL + "/evil.tar.gz",
		UserDir: userDir,
		HTTP:    srv.Client(),
	})
	if err == nil {
		t.Fatal("expected error for path-traversal entry")
	}
	if !strings.Contains(err.Error(), "unsafe") && !strings.Contains(err.Error(), "outside") {
		t.Errorf("err = %v, want it to mention unsafe/outside", err)
	}
	// And no files written.
	entries, _ := os.ReadDir(userDir)
	if len(entries) != 0 {
		t.Errorf("userDir should be empty after rejected archive, got: %v", entries)
	}
}
