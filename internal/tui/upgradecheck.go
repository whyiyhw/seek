package tui

import (
	"context"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/whyiyhw/seek/internal/upgrade"
)

// versionCheckCmd schedules the startup "is there a newer release?"
// probe. Returns nil (no message will be emitted) when:
//   - SEEK_NO_UPGRADE_CHECK is set — user opted out
//   - the running binary is a dev build — local builds are usually
//     ahead of the latest release, so a nudge would be noise
//   - the cache says we checked within the last 24h AND the last
//     check did not see a newer version (or the user has since
//     upgraded to that version)
//
// When the cache holds a still-newer tag the message fires
// immediately with no network call — the user sees the nudge even
// on offline relaunches until they actually upgrade.
//
// Network calls are time-bounded to upgradeCheckTimeout. A slow or
// hung mirror does not delay anything user-visible: the cmd runs in
// a tea.Cmd goroutine and only the eventual message reaches Update().
func versionCheckCmd(repoOwner, repoName, current string) tea.Cmd {
	if os.Getenv("SEEK_NO_UPGRADE_CHECK") != "" {
		return nil
	}
	if upgrade.IsDev(current) {
		return nil
	}

	cached := upgrade.LoadCheckCache()
	if cached.Fresh() {
		// Within TTL: replay the cached answer without network.
		if cached.LatestTag == "" || upgrade.UpToDate(current, cached.LatestTag) {
			return nil // up-to-date (now or when we last checked)
		}
		// We saw a newer tag and the user hasn't upgraded yet —
		// re-nudge so the status bar shows it after a restart.
		tag := cached.LatestTag
		return func() tea.Msg { return versionCheckDoneMsg{NewerTag: tag} }
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), upgradeCheckTimeout)
		defer cancel()
		rel, err := upgrade.Check(ctx, upgrade.Options{
			Owner:   repoOwner,
			Repo:    repoName,
			Current: current,
		})
		// Errors are intentionally swallowed: a transient network
		// failure must not draw the user's attention away from their
		// actual work. We just won't surface a nudge this run.
		now := time.Now().UTC()
		out := versionCheckDoneMsg{}
		entry := upgrade.CheckCache{CheckedAt: now}
		if err == nil && rel != nil {
			out.NewerTag = rel.TagName
			entry.LatestTag = rel.TagName
		}
		// Persist even the "no new version" outcome so we throttle
		// the next launch. Save failures are ignored — printing
		// would defeat the point of a quiet check.
		_ = upgrade.SaveCheckCache(entry)
		if out.NewerTag == "" && err != nil {
			// Don't emit a message on transport errors; nothing to show.
			return nil
		}
		if out.NewerTag == "" {
			return nil
		}
		return out
	}
}

// upgradeCheckTimeout caps the startup probe. 8s is long enough for a
// flaky mobile-hotspot connection to complete a small JSON GET and
// short enough that a hung mirror is invisible to the user — they're
// already in the TUI when the cmd fires, so a missed nudge is fine.
const upgradeCheckTimeout = 8 * time.Second
