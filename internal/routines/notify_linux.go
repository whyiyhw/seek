//go:build linux

package routines

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DefaultNotifier on Linux shells out to notify-send (libnotify).
// Most desktop environments (GNOME / KDE / Cinnamon / XFCE)
// have it pre-installed; headless servers don't.
//
// Headless heuristic: $DISPLAY and $WAYLAND_DISPLAY both empty
// → swap to a no-op silently. Avoids stderr spam every minute
// on a cron-driven server. PRD §8 risk row "OS notification in
// headless server has no meaning" calls this out directly.
var DefaultNotifier Notifier = pickLinuxNotifier()

func pickLinuxNotifier() Notifier {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return noopNotifier
	}
	return notifySendNotify
}

func notifySendNotify(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// notify-send takes title + optional body as separate args
	// — no shell quoting headaches. Truncate so the dispatcher
	// doesn't choke on a multi-MB body.
	cmd := exec.CommandContext(ctx, "notify-send",
		truncBodyLinux(title, 256),
		truncBodyLinux(body, 4096),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("notify-send: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func truncBodyLinux(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
