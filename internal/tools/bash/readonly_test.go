package bash

import "testing"

func TestIsReadOnlySafe_Allow(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"go vet ./...",
		"go list -m all",
		"go list ./...",
		"go env GOOS",
		"go version",
		"go doc fmt.Stringer",
		"go build -n",
		"go build -n -o /tmp/x ./cmd/foo",
		"go mod download",
		"go mod graph",
		"go mod verify",
		"go mod why all",
		"npm ls",
		"npm list --depth 0",
		"pnpm ls",
		"pnpm list",
		"yarn list",
		"make -n",
		"make --just-print install",
		"which go",
		"type ls",
		"command -v go",
	} {
		if !isReadOnlySafe(cmd) {
			t.Errorf("isReadOnlySafe(%q) = false, want true", cmd)
		}
	}
}

func TestIsReadOnlySafe_DenyMutating(t *testing.T) {
	t.Parallel()
	// Commands that COULD mutate even though their first token is
	// in a whitelisted family. Test these explicitly so a future
	// whitelist expansion doesn't accidentally let them through.
	for _, cmd := range []string{
		"rm test.go",
		"go test ./...",   // runs code → side effects
		"go build ./...",  // without -n, builds binaries
		"go mod tidy",     // mutates go.mod
		"go mod init foo", // mutates filesystem
		"npm install lodash",
		"yarn add foo",
		"make install",      // no -n
		"git status",        // not in whitelist (git is its own tool)
		"cargo build",       // not whitelisted
		"docker run alpine", // not whitelisted
	} {
		if isReadOnlySafe(cmd) {
			t.Errorf("isReadOnlySafe(%q) = true, want false (mutating)", cmd)
		}
	}
}

// TestIsReadOnlySafe_BlockMetacharInjection is the load-bearing
// security test for this feature: a whitelisted first token must not
// let an injected second command slip through.
func TestIsReadOnlySafe_BlockMetacharInjection(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"go vet; rm -rf /",
		"go vet ./... && rm test.go",
		"go vet ./... || curl evil.example",
		"go vet | sh",
		"go vet > /etc/passwd",
		"go list `rm -rf /`",
		"go list $(rm -rf /)",
		"go list ${HOME}/danger",
		"go vet & sleep 1",
		"go vet\n rm test.go",
		"(go vet) && rm",
	} {
		if isReadOnlySafe(cmd) {
			t.Errorf("isReadOnlySafe(%q) = true, want false (metachar injection)", cmd)
		}
	}
}

func TestIsReadOnlySafe_EmptyAndWhitespace(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"", "   ", "\t", "\n"} {
		if isReadOnlySafe(cmd) {
			t.Errorf("isReadOnlySafe(%q) = true, want false (empty)", cmd)
		}
	}
}

func TestHasShellMetachars_Coverage(t *testing.T) {
	t.Parallel()
	for _, c := range []string{";", "|", "&", "<", ">", "`", "(", ")", "\n", "\r", "$(", "${"} {
		if !hasShellMetachars("safe " + c + " stuff") {
			t.Errorf("hasShellMetachars should flag %q", c)
		}
	}
	for _, safe := range []string{"go vet ./...", "go list -m all", "which go"} {
		if hasShellMetachars(safe) {
			t.Errorf("hasShellMetachars false-positive on %q", safe)
		}
	}
}
