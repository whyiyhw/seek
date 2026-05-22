package upgrade

import (
	"fmt"
	"strings"
)

// pickAsset returns the asset whose name matches our GOOS/GOARCH in
// the GoReleaser default scheme: "seek_<version>_<os>_<arch>.<ext>".
// Match is case-insensitive on os/arch and tolerates aliases (amd64 ==
// x86_64, arm64 == aarch64) so a future archive-naming change in the
// release config doesn't silently break the upgrader.
func pickAsset(assets []ghAsset, goos, goarch string) (*ghAsset, error) {
	wantOS := normalizeOS(goos)
	wantArch := normalizeArch(goarch)

	for i := range assets {
		a := &assets[i]
		name := strings.ToLower(a.Name)
		if !isArchive(name) {
			continue
		}
		if !containsToken(name, wantOS) {
			continue
		}
		if !containsToken(name, wantArch) {
			continue
		}
		return a, nil
	}
	return nil, fmt.Errorf("no asset matched %s/%s in release", goos, goarch)
}

// pickChecksum locates the GoReleaser checksums file. The naming
// convention is "<project>_<version>_checksums.txt"; we match on the
// "checksums" token to stay robust against the version field changing.
func pickChecksum(assets []ghAsset) (*ghAsset, error) {
	for i := range assets {
		a := &assets[i]
		name := strings.ToLower(a.Name)
		if strings.Contains(name, "checksums") && strings.HasSuffix(name, ".txt") {
			return a, nil
		}
	}
	return nil, fmt.Errorf("no checksums.txt asset in release")
}

func isArchive(lowerName string) bool {
	return strings.HasSuffix(lowerName, ".tar.gz") ||
		strings.HasSuffix(lowerName, ".tgz") ||
		strings.HasSuffix(lowerName, ".zip")
}

// containsToken matches "linux" inside "seek_0.9.0_linux_amd64.tar.gz"
// without false-positive matching "linux" inside e.g. "darwin-linux-
// musl" — the token must be bounded by [._-] or string edges.
func containsToken(s, token string) bool {
	if token == "" {
		return false
	}
	for i := 0; i+len(token) <= len(s); i++ {
		if s[i:i+len(token)] != token {
			continue
		}
		if i > 0 && !isSep(s[i-1]) {
			continue
		}
		after := i + len(token)
		if after < len(s) && !isSep(s[after]) {
			continue
		}
		return true
	}
	return false
}

func isSep(b byte) bool {
	return b == '.' || b == '-' || b == '_' || b == '/'
}

func normalizeOS(goos string) string {
	return strings.ToLower(goos)
}

func normalizeArch(goarch string) string {
	switch strings.ToLower(goarch) {
	case "amd64", "x86_64", "x86-64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	case "386", "i386", "x86":
		return "386"
	default:
		return strings.ToLower(goarch)
	}
}
