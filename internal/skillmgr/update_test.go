package skillmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readSidecar pulls the .install.json out of an installed skill so
// tests can assert what the install pipeline recorded. The sidecar
// schema is internal to seek (PRD v2 §4.2) — these tests are the
// regression catch if its layout drifts.
func readSidecar(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".install.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse sidecar: %v\n%s", err, data)
	}
	return m
}

// ---------- Sidecar contents (regression for M8.1c miss) ----------

func TestInstall_Local_SidecarRecordsAbsoluteSource(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	writePackage(t, srcParent, "pkg", "local-skill", "x")

	res, err := Install(InstallOptions{
		Source:  filepath.Join(srcParent, "pkg"),
		UserDir: userDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	sc := readSidecar(t, res.Dir)
	if sc["type"] != "local" {
		t.Errorf("sidecar type = %v, want local", sc["type"])
	}
	url, _ := sc["url"].(string)
	if !filepath.IsAbs(url) {
		t.Errorf("sidecar url should be absolute for local sources, got %q", url)
	}
	if got := filepath.Base(url); got != "pkg" {
		t.Errorf("filepath.Base(url) = %q, want pkg (full url: %s)", got, url)
	}
}

func TestInstall_Git_SidecarRecordsRefSeparately(t *testing.T) {
	// PRD v2 §4.2 — `.install.json` stores url and ref as separate
	// fields. Without this, `seek skill update` couldn't tell
	// whether to re-checkout v1.0.0 vs main vs a commit sha.
	ensureGit(t)
	repoDir := initRepoWith(t, map[string]string{
		"SKILL.md": "---\nname: git-skill\ndescription: tagged\n---\nbody\n",
	}, "v1.0.0")
	userDir := t.TempDir()

	res, err := Install(InstallOptions{
		Source:  "file://" + repoDir + "#v1.0.0",
		UserDir: userDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	sc := readSidecar(t, res.Dir)
	if sc["type"] != "git" {
		t.Errorf("sidecar type = %v, want git", sc["type"])
	}
	url, _ := sc["url"].(string)
	if strings.Contains(url, "#") {
		t.Errorf("sidecar url should not embed ref fragment, got %q", url)
	}
	if !strings.HasPrefix(url, "file://") {
		t.Errorf("sidecar url = %q, want file:// prefix", url)
	}
	if sc["ref"] != "v1.0.0" {
		t.Errorf("sidecar ref = %v, want v1.0.0", sc["ref"])
	}
}

func TestInstall_HTTPS_SidecarRecordsSHA256(t *testing.T) {
	payload := buildTarGz(t, map[string]string{"SKILL.md": validSKILL})
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])
	srv := serveBytes(t, "/x.tar.gz", payload)

	res, err := Install(InstallOptions{
		Source:  srv.URL + "/x.tar.gz",
		UserDir: t.TempDir(),
		SHA256:  want,
		HTTP:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sc := readSidecar(t, res.Dir)
	if sc["type"] != "https" {
		t.Errorf("sidecar type = %v, want https", sc["type"])
	}
	if sc["checksum_sha256"] != want {
		t.Errorf("sidecar sha256 = %v, want %s", sc["checksum_sha256"], want)
	}
}

// ---------- Update ----------

func TestUpdate_Local_ReplacesContent(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	pkgDir := writePackage(t, srcParent, "pkg", "local-skill", "v1")

	if _, err := Install(InstallOptions{
		Source: pkgDir, UserDir: userDir,
	}); err != nil {
		t.Fatal(err)
	}

	// Modify the source package and update.
	newContent := "---\nname: local-skill\ndescription: v2-updated\n---\n\n# Body\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "SKILL.md"), []byte(newContent), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Update(UpdateOptions{Name: "local-skill", UserDir: userDir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "local-skill" {
		t.Errorf("Name = %q, want local-skill", res.Name)
	}
	data, _ := os.ReadFile(filepath.Join(userDir, "local-skill", "SKILL.md"))
	if !strings.Contains(string(data), "description: v2-updated") {
		t.Errorf("update did not refresh SKILL.md; got:\n%s", data)
	}
}

func TestUpdate_HTTPS_Refetches(t *testing.T) {
	first := buildTarGz(t, map[string]string{
		"SKILL.md": "---\nname: tarball-skill\ndescription: first\n---\nbody\n",
	})
	// Server flips its served bytes mid-test so we can verify the
	// second install actually re-fetched (rather than reading cache).
	var served []byte = first
	mux := http.NewServeMux()
	mux.HandleFunc("/x.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(served)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	userDir := t.TempDir()
	if _, err := Install(InstallOptions{
		Source:  srv.URL + "/x.tar.gz",
		UserDir: userDir,
		HTTP:    srv.Client(),
	}); err != nil {
		t.Fatal(err)
	}

	// Swap payload to a "second" version.
	served = buildTarGz(t, map[string]string{
		"SKILL.md": "---\nname: tarball-skill\ndescription: second\n---\nbody\n",
	})

	if _, err := Update(UpdateOptions{
		Name:    "tarball-skill",
		UserDir: userDir,
		HTTP:    srv.Client(),
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(userDir, "tarball-skill", "SKILL.md"))
	if !strings.Contains(string(data), "description: second") {
		t.Errorf("update did not re-fetch; got:\n%s", data)
	}
}

func TestUpdate_Git_Refetches(t *testing.T) {
	ensureGit(t)
	repoDir := initRepoWith(t, map[string]string{
		"SKILL.md": "---\nname: git-skill\ndescription: v1\n---\nbody",
	}, "")
	userDir := t.TempDir()

	if _, err := Install(InstallOptions{
		Source: "file://" + repoDir, UserDir: userDir,
	}); err != nil {
		t.Fatal(err)
	}

	// Commit a new version upstream.
	if err := os.WriteFile(filepath.Join(repoDir, "SKILL.md"),
		[]byte("---\nname: git-skill\ndescription: v2\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "commit", "-q", "-am", "v2")

	if _, err := Update(UpdateOptions{Name: "git-skill", UserDir: userDir}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(userDir, "git-skill", "SKILL.md"))
	if !strings.Contains(string(data), "description: v2") {
		t.Errorf("git update did not re-fetch; got:\n%s", data)
	}
}

func TestUpdate_NoSidecar_Refuses(t *testing.T) {
	userDir := t.TempDir()
	// Manually install (cp -r style) without going through Install —
	// no .install.json gets written. Update must refuse with a
	// clear message because there's nothing to re-fetch from.
	dir := filepath.Join(userDir, "manual")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: manual\ndescription: x\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Update(UpdateOptions{Name: "manual", UserDir: userDir})
	if err == nil {
		t.Fatal("expected error for skill without .install.json")
	}
	if !strings.Contains(err.Error(), "install record") &&
		!strings.Contains(err.Error(), ".install.json") {
		t.Errorf("err = %v, want it to mention missing install record", err)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	_, err := Update(UpdateOptions{
		Name: "ghost-skill", UserDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("err = %v, want it to mention not found", err)
	}
}

// ---------- UpdateAll ----------

func TestUpdateAll_RefreshesEveryManagedSkill(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	// Install two managed skills + one manual (no sidecar) — UpdateAll
	// should refresh the two managed and skip the manual.
	a := writePackage(t, srcParent, "a-src", "alpha", "v1")
	b := writePackage(t, srcParent, "b-src", "beta", "v1")
	if _, err := Install(InstallOptions{Source: a, UserDir: userDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{Source: b, UserDir: userDir}); err != nil {
		t.Fatal(err)
	}
	// A manual third skill — no sidecar, should be left alone.
	manualDir := filepath.Join(userDir, "manual")
	if err := os.MkdirAll(manualDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manualDir, "SKILL.md"),
		[]byte("---\nname: manual\ndescription: untouched\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bump both source packages.
	for _, p := range []string{a, b} {
		// Parse the name out so we don't have to track it twice.
		data, _ := os.ReadFile(filepath.Join(p, "SKILL.md"))
		newer := strings.Replace(string(data), "description: v1", "description: v2", 1)
		if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(newer), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	results, err := UpdateAll(UpdateOptions{UserDir: userDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("UpdateAll returned %d results, want 2 (manual skipped)", len(results))
	}
	for _, name := range []string{"alpha", "beta"} {
		data, _ := os.ReadFile(filepath.Join(userDir, name, "SKILL.md"))
		if !strings.Contains(string(data), "description: v2") {
			t.Errorf("%s not updated; got:\n%s", name, data)
		}
	}
	// Manual stayed put.
	data, _ := os.ReadFile(filepath.Join(userDir, "manual", "SKILL.md"))
	if !strings.Contains(string(data), "description: untouched") {
		t.Errorf("manual was touched despite no sidecar; got:\n%s", data)
	}
}

func TestUpdateAll_PartialFailureKeepsGoing(t *testing.T) {
	srcParent := t.TempDir()
	userDir := t.TempDir()
	a := writePackage(t, srcParent, "a-src", "alpha", "v1")
	b := writePackage(t, srcParent, "b-src", "beta", "v1")
	if _, err := Install(InstallOptions{Source: a, UserDir: userDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallOptions{Source: b, UserDir: userDir}); err != nil {
		t.Fatal(err)
	}

	// Sabotage alpha by deleting its source directory. UpdateAll
	// should still refresh beta and report alpha's failure.
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "SKILL.md"),
		[]byte("---\nname: beta\ndescription: v2\n---\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, _ := UpdateAll(UpdateOptions{UserDir: userDir})
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	var alphaErr, betaErr error
	for _, r := range results {
		if r.Name == "alpha" {
			alphaErr = r.Err
		}
		if r.Name == "beta" {
			betaErr = r.Err
		}
	}
	if alphaErr == nil {
		t.Errorf("alpha should have errored (source gone)")
	}
	if betaErr != nil {
		t.Errorf("beta should have succeeded, got err=%v", betaErr)
	}
	// beta must have actually been refreshed.
	data, _ := os.ReadFile(filepath.Join(userDir, "beta", "SKILL.md"))
	if !strings.Contains(string(data), "description: v2") {
		t.Errorf("beta not updated despite no error; got:\n%s", data)
	}
}
