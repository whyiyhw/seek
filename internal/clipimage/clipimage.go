// Package clipimage grabs an image off the OS clipboard to a temp file,
// for the TUI image-paste feature (feature-image-paste.md, M-imgpaste).
// It does NOT OCR — it just produces a PNG path; the caller inserts an
// `@<path>` reference that the existing 柱 Q OCR pipeline (ocr.Expand)
// picks up on submit. Grabbing is PLUGGABLE (env SEEK_CLIPBOARD_IMAGE_CMD)
// with platform defaults, mirroring how ocr.command is pluggable.
package clipimage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// gcMaxAge is how long a grabbed clip-*.png lingers before GC removes it.
// Generous: a pasted image is referenced by an unsubmitted @path only
// until the user hits Enter — 24h covers any realistic delay.
const gcMaxAge = 24 * time.Hour

var (
	// ErrNoImage means the clipboard held no image (the grabber exited
	// non-zero or wrote nothing). The caller falls back to normal text
	// paste — this is the common, non-error case.
	ErrNoImage = errors.New("clipimage: no image on the clipboard")
	// ErrNoGrabber means no grabber command is available on this platform
	// and none was configured (SEEK_CLIPBOARD_IMAGE_CMD). The caller
	// degrades to text paste.
	ErrNoGrabber = errors.New("clipimage: no clipboard-image grabber (set SEEK_CLIPBOARD_IMAGE_CMD)")
)

// Options configures a grab. Command overrides the platform default
// (env SEEK_CLIPBOARD_IMAGE_CMD); the output PNG path is appended to it as the
// LAST argument, and the command must write the clipboard image there. (The
// built-in Windows default is the one exception — it receives the path in
// windowsOutEnv, because argv there is re-parsed as PowerShell code.) CacheDir
// is where the temp PNG lands (default os.TempDir).
type Options struct {
	Command  []string
	CacheDir string
}

// defaultGrabCommand returns the platform default grabber — argv plus, when
// non-empty, the env var the output path must travel in instead of riding on
// argv (see windowsOutEnv). Nil argv when no default is known (→ ErrNoGrabber).
// A package var so tests can stub it.
var defaultGrabCommand = func() ([]string, string) {
	return defaultGrabCommandFor(runtime.GOOS)
}

// defaultGrabCommandFor is the per-GOOS table, split out so every branch is
// testable on any host (same seam as tui.legacyConhostInputFor).
func defaultGrabCommandFor(goos string) (argv []string, outEnv string) {
	switch goos {
	case "darwin":
		// AppleScript: write the clipboard's PNG representation to the path
		// passed as argv[1]; `on error` (no image) leaves the file empty →
		// ErrNoImage. (Real-mac verification lands in M-imgpaste.3.)
		return []string{"osascript", "-e", darwinGrabScript}, ""
	case "linux":
		// Wayland (wl-paste) first, X11 (xclip) fallback, writing to "$1".
		// Neither present → empty file → ErrNoImage (degrade). X11-only
		// users can still set SEEK_CLIPBOARD_IMAGE_CMD explicitly.
		return []string{"sh", "-c",
			`if command -v wl-paste >/dev/null 2>&1; then wl-paste --type image/png > "$1" 2>/dev/null; ` +
				`elif command -v xclip >/dev/null 2>&1; then xclip -selection clipboard -t image/png -o > "$1" 2>/dev/null; fi`,
			"sh"}, ""
	case "windows":
		// Windows PowerShell (ships with Windows, `powershell` is on PATH)
		// reading the clipboard bitmap through WinForms and saving it as PNG.
		// -STA is required by the clipboard API (the default since PS 3.0,
		// passed explicitly anyway). The output path travels in windowsOutEnv,
		// NOT on argv: everything after `-Command` is re-parsed as command
		// TEXT, so a path with a space or an apostrophe (C:\Users\John
		// Smith\…, C:\Users\O'Brien\…) splits or breaks the call. Verified on
		// a real Windows box — see docs/pitfalls.md "PowerShell `-Command`:
		// everything after it is command TEXT".
		return []string{"powershell", "-NoProfile", "-NonInteractive", "-STA", "-Command", windowsGrabScript}, windowsOutEnv
	default:
		return nil, ""
	}
}

// windowsOutEnv carries the output PNG path into the Windows default grabber.
// It avoids argv: `powershell -Command` joins trailing argv into the command
// text (Go's argv quoting is consumed by PowerShell's own parser before the
// join), so a space splits the path and `'` / `;` / `$` make it code.
const windowsOutEnv = "SEEK_CLIPIMAGE_OUT"

const darwinGrabScript = `on run argv
	set outPath to item 1 of argv
	try
		set imgData to (the clipboard as «class PNGf»)
	on error
		return
	end try
	set fp to (open for access (POSIX file outPath) with write permission)
	try
		set eof fp to 0
		write imgData to fp
	end try
	close access fp
end run`

// windowsGrabScript is the Windows default's `-Command` body. The output path
// arrives in windowsOutEnv — it cannot ride on argv (see that const). No image
// (or any failure) → exit 1 and no file → ErrNoImage → the TUI falls back to
// text paste. Measured ~0.35s warm on Windows PowerShell 5.1, inside the TUI's
// 3s grab budget (internal/tui/paste.go).
const windowsGrabScript = `$out = $env:` + windowsOutEnv + `; ` +
	`Add-Type -AssemblyName System.Windows.Forms; ` +
	`Add-Type -AssemblyName System.Drawing; ` +
	`$img = [System.Windows.Forms.Clipboard]::GetImage(); ` +
	`if ($null -eq $img) { exit 1 }; ` +
	`$img.Save($out, [System.Drawing.Imaging.ImageFormat]::Png)`

// Grab writes the clipboard image to a fresh temp PNG under CacheDir and
// returns its path. Runs the grabber (Options.Command or the platform
// default) with the output path appended as the last arg — or, when the
// default asks for it (outEnv, the Windows case), in that environment
// variable instead. A non-zero exit or an empty/absent output file →
// ErrNoImage; no grabber → ErrNoGrabber. Honours ctx (the grabber is killed
// on cancel/timeout).
func Grab(ctx context.Context, opt Options) (string, error) {
	cmdline, outEnv := opt.Command, ""
	if len(cmdline) == 0 {
		cmdline, outEnv = defaultGrabCommand()
	}
	if len(cmdline) == 0 {
		return "", ErrNoGrabber
	}

	dir := opt.CacheDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	gcOldGrabs(dir) // best-effort: clear stale pasted PNGs before adding one
	// Reserve a unique path, then remove it so the grabber creates the file
	// fresh (avoids append/truncate ambiguity across grabber tools).
	f, err := os.CreateTemp(dir, "clip-*.png")
	if err != nil {
		return "", err
	}
	out := f.Name()
	f.Close()
	os.Remove(out)

	argv := append([]string{}, cmdline[1:]...)
	if outEnv == "" {
		argv = append(argv, out) // the shared contract: path is the last arg
	}
	cmd := exec.CommandContext(ctx, cmdline[0], argv...)
	if outEnv != "" {
		// Windows default: the path must not appear on argv at all, it would
		// be re-parsed as PowerShell command text — see windowsOutEnv.
		cmd.Env = append(os.Environ(), outEnv+"="+out)
	}
	runErr := cmd.Run()

	info, statErr := os.Stat(out)
	if runErr != nil || statErr != nil || info.Size() == 0 {
		os.Remove(out)
		return "", ErrNoImage
	}
	return out, nil
}

// gcOldGrabs removes clip-*.png files older than gcMaxAge from dir.
// Best-effort: errors are ignored (GC must never block a paste).
func gcOldGrabs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "clip-") || !strings.HasSuffix(name, ".png") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > gcMaxAge {
			os.Remove(filepath.Join(dir, name))
		}
	}
}
