//go:build linux

package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux confinement via Landlock (filesystem only — Landlock cannot
// restrict network, so AllowNetwork is ignored here; the seatbelt path on
// macOS does honor it). The model mirrors the darwin seatbelt profile:
// WRITE-ish access is DENIED everywhere except beneath WritableDirs (plus
// the system temp dir); reads and execs are left unrestricted.
//
// Landlock applies irreversibly to the calling process and is inherited
// across exec, but Go offers no post-fork/pre-exec hook, so we use a
// re-exec TRAMPOLINE: Argv/Wrap return `seek <trampolineArg> <opts> --
// <cmd> <args…>`; main() calls RunTrampolineIfRequested() first thing,
// which (in that re-exec'd seek) applies Landlock to itself and then
// unix.Exec's the real command — which inherits the jail.

// trampolineArg is the hidden argv[1] marking a re-exec whose only job is
// to lock itself down and exec the wrapped command. Distinctive on
// purpose so a real user command can never collide with it.
const trampolineArg = "__seek_sandbox_landlock_exec"

// LANDLOCK_RULE_PATH_BENEATH is the only rule type; x/sys/unix exports the
// access-fs constants and syscall numbers but not this enum, so define it.
const rulePathBeneath = 1

// Available reports whether the running kernel supports Landlock (ABI ≥ 1
// and not disabled). Used by callers to decide whether to rely on
// confinement; autopilot falls back to worktree logical isolation when
// false.
func Available() bool { return landlockABI() >= 1 }

// Argv returns the trampoline argv that, when run, confines itself via
// Landlock and execs name+args. self is the seek binary.
func Argv(opt Options, name string, args ...string) (string, []string) {
	out := append([]string{trampolineArg, encodeOptions(opt), "--", name}, args...)
	return self(), out
}

// Wrap is Argv as an *exec.Cmd bound to ctx.
func Wrap(ctx context.Context, opt Options, name string, args ...string) *exec.Cmd {
	p, a := Argv(opt, name, args...)
	return exec.CommandContext(ctx, p, a...)
}

// RunTrampolineIfRequested must be the FIRST thing main() calls. In a
// normal `seek` invocation it returns immediately. In a trampoline
// re-exec it applies Landlock and exec's the wrapped command — it never
// returns on success, and FAILS CLOSED (exits non-zero) if the jail can't
// be applied, so a command can never escape by the sandbox silently
// no-op'ing.
func RunTrampolineIfRequested() {
	if len(os.Args) < 2 || os.Args[1] != trampolineArg {
		return
	}
	// argv: [self, trampolineArg, <b64 opts>, "--", cmd, args…]
	if len(os.Args) < 5 || os.Args[3] != "--" {
		fmt.Fprintln(os.Stderr, "seek sandbox: malformed trampoline argv")
		os.Exit(127)
	}
	opt, err := decodeOptions(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "seek sandbox: "+err.Error())
		os.Exit(127)
	}
	if err := applyLandlock(opt); err != nil {
		fmt.Fprintln(os.Stderr, "seek sandbox: landlock: "+err.Error())
		os.Exit(127) // fail closed — do NOT run unconfined
	}
	argv := os.Args[4:]
	path := argv[0]
	if lp, err := exec.LookPath(path); err == nil {
		path = lp
	}
	err = unix.Exec(path, argv, os.Environ())
	// Exec only returns on failure.
	fmt.Fprintln(os.Stderr, "seek sandbox: exec "+argv[0]+": "+err.Error())
	os.Exit(127)
}

// landlockABI returns the kernel's Landlock ABI version, or -1 if Landlock
// is unavailable (ENOSYS / disabled).
func landlockABI() int {
	r, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 {
		return -1
	}
	return int(r)
}

// writeMask is the set of write-ish FS access rights to HANDLE (deny by
// default), masked to those the detected ABI understands — handling a
// right an older kernel doesn't know makes create_ruleset return EINVAL.
func writeMask(abi int) uint64 {
	m := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		m |= uint64(unix.LANDLOCK_ACCESS_FS_REFER)
	}
	if abi >= 3 {
		m |= uint64(unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	}
	if abi >= 5 {
		m |= uint64(unix.LANDLOCK_ACCESS_FS_IOCTL_DEV)
	}
	return m
}

// applyLandlock confines the CURRENT process: handled write rights are
// denied everywhere except beneath WritableDirs (+ system temp), where
// they're granted. Reads/execs are unrestricted. Idempotent only in the
// sense that it's meant to be called once, just before exec.
func applyLandlock(opt Options) error {
	abi := landlockABI()
	if abi < 1 {
		return errors.New("landlock unavailable")
	}
	handled := writeMask(abi)

	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	rfd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("create_ruleset: %w", errno)
	}
	defer unix.Close(int(rfd))

	// Grant the full handled write set beneath each writable dir + temp.
	// O_PATH-open each (best-effort: skip ones that don't exist).
	dirs := append([]string{}, opt.WritableDirs...)
	if tmp := os.TempDir(); tmp != "" {
		dirs = append(dirs, tmp)
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		fd, err := unix.Open(d, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			continue // a missing allowed dir just isn't granted; not fatal
		}
		pb := unix.LandlockPathBeneathAttr{Allowed_access: handled, Parent_fd: int32(fd)}
		_, _, e := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
			rfd, rulePathBeneath, uintptr(unsafe.Pointer(&pb)), 0, 0, 0)
		unix.Close(fd)
		if e != 0 {
			return fmt.Errorf("add_rule(%s): %w", d, e)
		}
	}

	// no_new_privs is required before restrict_self (unless CAP_SYS_ADMIN).
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(no_new_privs): %w", err)
	}
	if _, _, e := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, rfd, 0, 0); e != 0 {
		return fmt.Errorf("restrict_self: %w", e)
	}
	return nil
}

func self() string {
	if p, err := os.Executable(); err == nil {
		return p
	}
	return "/proc/self/exe"
}

func encodeOptions(o Options) string {
	b, _ := json.Marshal(o)
	return base64.RawStdEncoding.EncodeToString(b)
}

func decodeOptions(s string) (Options, error) {
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return Options{}, fmt.Errorf("decode opts: %w", err)
	}
	var o Options
	if err := json.Unmarshal(b, &o); err != nil {
		return Options{}, fmt.Errorf("unmarshal opts: %w", err)
	}
	return o, nil
}
