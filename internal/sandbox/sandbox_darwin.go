//go:build darwin

package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Available reports macOS seatbelt support: sandbox-exec must be present.
func Available() bool {
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

// Wrap runs name+args under a seatbelt profile (SBPL) generated from opt:
// reads + exec are allowed broadly, file WRITES are denied except to
// WritableDirs + the system temp dir, and network is denied unless
// AllowNetwork. Falls back to the plain command if sandbox-exec is
// missing (gate on Available() when confinement is required).
//
// seatbelt's `sandbox-exec` is deprecated-but-functional and needs no
// cgo (seek is CGO_ENABLED=0), which is why we shell out rather than use
// the sandbox_init C API.
func Wrap(ctx context.Context, opt Options, name string, args ...string) *exec.Cmd {
	n, a := Argv(opt, name, args...)
	return exec.CommandContext(ctx, n, a...)
}

// Argv returns the (name, args) to exec for running name+args under the
// sandbox — on macOS it prepends `sandbox-exec -p <profile>`, elsewhere
// it returns them unchanged. Callers that need their own *exec.Cmd setup
// (e.g. the bash tool's Setsid + process-group kill) use this instead of
// Wrap so they keep control of the command. Returns name+args unchanged
// when sandbox-exec is unavailable.
func Argv(opt Options, name string, args ...string) (string, []string) {
	if !Available() {
		return name, args
	}
	return "sandbox-exec", append([]string{"-p", profileFor(opt), name}, args...)
}

// profileFor builds the SBPL profile. Rule order matters — later rules
// override earlier — so: allow-all, then deny writes, then re-allow the
// permitted write subpaths; deny network last when disallowed.
func profileFor(opt Options) string {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(deny file-write*)\n")

	// Always allow writes to the system temp dir (subprocesses need it)
	// plus the explicit writable dirs, resolved through symlinks so
	// /var → /private/var and /tmp → /private/tmp match the kernel's
	// canonical paths.
	writable := append([]string{os.TempDir(), "/private/tmp"}, opt.WritableDirs...)
	b.WriteString("(allow file-write*")
	for _, d := range writable {
		if rp := realpath(d); rp != "" {
			fmt.Fprintf(&b, "\n  (subpath %q)", rp)
		}
	}
	// /dev/null and the std streams must stay writable or most tools break.
	b.WriteString("\n  (literal \"/dev/null\")")
	b.WriteString(")\n")

	if !opt.AllowNetwork {
		b.WriteString("(deny network*)\n")
	}
	return b.String()
}

func realpath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		return rp
	}
	return p // dir may not exist yet; use the abs path as-is
}
