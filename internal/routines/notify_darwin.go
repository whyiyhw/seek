//go:build darwin

package routines

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultNotifier on macOS shells out to osascript with the
// `display notification` AppleScript command. Renders as a
// Notification Center banner — no popup window, no focus
// stealing, no PowerShell wartiness.
//
// Both title + body are AppleScript-quoted (double-quotes
// escaped) so a prompt containing quotes doesn't break the
// script. Cap at 4 KB to defend against a runaway "summary" —
// banners would be truncated by macOS anyway, no need to feed
// it the full thing.
var DefaultNotifier Notifier = osascriptNotify

func osascriptNotify(title, body string) error {
	script := fmt.Sprintf(
		`display notification "%s" with title "%s"`,
		escapeForAppleScript(truncBody(body, 4096)),
		escapeForAppleScript(truncBody(title, 256)),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// escapeForAppleScript escapes the two characters that break
// a double-quoted AppleScript string literal: `\` and `"`.
// Newlines are tolerated by `display notification`; we leave
// them alone so multi-line bodies render readably.
func escapeForAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// truncBody caps s at n bytes, appending ellipsis when cut.
// Stays at byte level since OS notification banners are UTF-8
// truncated by the OS itself if we over-feed them.
func truncBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
