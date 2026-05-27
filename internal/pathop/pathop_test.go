package pathop

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsInPATH(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()

	t.Setenv("PATH", filepath.Join(other, string(os.PathListSeparator)+dir))

	if !IsInPATH(dir) {
		t.Fatal("expected dir to be found in PATH")
	}
	if IsInPATH(t.TempDir()) {
		t.Fatal("unexpected dir should not match")
	}
}

func TestIsInPATH_ignoresEmptySegments(t *testing.T) {
	dir := t.TempDir()
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", sep+sep+dir+sep+sep)

	if !IsInPATH(dir) {
		t.Fatal("expected dir to match through empty segments")
	}
}

func TestIsInUserPATH_nonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses registry-backed IsInUserPATH")
	}
	ok, err := IsInUserPATH(t.TempDir())
	if err != nil {
		t.Fatalf("IsInUserPATH: %v", err)
	}
	if ok {
		t.Fatal("expected false on non-Windows stub")
	}
}

func TestEnsureInPATH_nonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows EnsureInPATH writes to the registry")
	}
	added, err := EnsureInPATH(t.TempDir())
	if err != nil {
		t.Fatalf("EnsureInPATH: %v", err)
	}
	if added {
		t.Fatal("expected no-op on non-Windows")
	}
}

func TestEnsureInPATHWithBroadcast_nonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows EnsureInPATHWithBroadcast writes to the registry")
	}
	added, err := EnsureInPATHWithBroadcast(t.TempDir())
	if err != nil {
		t.Fatalf("EnsureInPATHWithBroadcast: %v", err)
	}
	if added {
		t.Fatal("expected no-op on non-Windows")
	}
}

func TestPathContainsDir_caseSensitive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path casing semantics differ on Windows")
	}
	if pathContainsDir("/foo/bar", "/Foo/Bar", false) {
		t.Fatal("expected case-sensitive mismatch")
	}
}

func TestPathContainsDir_caseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive PATH uses Windows paths and ; separators")
	}
	if !pathContainsDir(`C:\Tools\Seek`, `C:\tools\seek`, true) {
		t.Fatal("expected case-insensitive match")
	}
}

func TestPathContainsDir_caseInsensitive_nonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("covered by TestPathContainsDir_caseInsensitive")
	}
	if !pathContainsDir("/FOO/BAR", "/foo/bar", true) {
		t.Fatal("expected case-insensitive match")
	}
}
