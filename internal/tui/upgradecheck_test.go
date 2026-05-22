package tui

import (
	"testing"
	"time"

	"github.com/whyiyhw/seek/internal/upgrade"
)

// TestVersionCheckCmd_EnvOptOut: SEEK_NO_UPGRADE_CHECK is the user's
// escape hatch — when set, NOTHING should fire (no goroutine, no
// network, no message). Returning nil from the factory is how we
// signal that to bubbletea.
func TestVersionCheckCmd_EnvOptOut(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_NO_UPGRADE_CHECK", "1")
	if cmd := versionCheckCmd("whyiyhw", "seek", "v0.1.0"); cmd != nil {
		t.Error("env opt-out should suppress the check")
	}
}

// TestVersionCheckCmd_DevBuild: a local build is almost always newer
// than the published release, so nudging the user to "upgrade" would
// be confusing. Skip silently for any IsDev() version.
func TestVersionCheckCmd_DevBuild(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_NO_UPGRADE_CHECK", "")
	if cmd := versionCheckCmd("whyiyhw", "seek", "dev"); cmd != nil {
		t.Error("dev build should suppress the check")
	}
	if cmd := versionCheckCmd("whyiyhw", "seek", "dev · abc1234+"); cmd != nil {
		t.Error("formatted dev banner should suppress the check")
	}
}

// TestVersionCheckCmd_FreshCacheReplay: when the cache says "we saw a
// newer tag less than 24h ago" we re-emit the same nudge without a
// network call. This is what makes the status-bar hint stick across
// offline relaunches until the user actually upgrades.
func TestVersionCheckCmd_FreshCacheReplay(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_NO_UPGRADE_CHECK", "")
	_ = upgrade.SaveCheckCache(upgrade.CheckCache{
		CheckedAt: time.Now(),
		LatestTag: "v0.9.0",
	})
	cmd := versionCheckCmd("whyiyhw", "seek", "v0.1.0")
	if cmd == nil {
		t.Fatal("expected a cached replay cmd")
	}
	msg := cmd()
	done, ok := msg.(versionCheckDoneMsg)
	if !ok || done.NewerTag != "v0.9.0" {
		t.Errorf("replay returned %+v, want versionCheckDoneMsg{NewerTag: v0.9.0}", msg)
	}
}

// TestVersionCheckCmd_FreshCacheNoNewer: cache says "we checked
// recently and the user was up to date" → no message. We don't keep
// polling GitHub on every launch.
func TestVersionCheckCmd_FreshCacheNoNewer(t *testing.T) {
	t.Setenv("SEEK_HOME", t.TempDir())
	t.Setenv("SEEK_NO_UPGRADE_CHECK", "")
	_ = upgrade.SaveCheckCache(upgrade.CheckCache{
		CheckedAt: time.Now(),
		LatestTag: "", // last check found nothing newer
	})
	if cmd := versionCheckCmd("whyiyhw", "seek", "v0.1.0"); cmd != nil {
		t.Error("fresh cache with no newer version should suppress the check")
	}
}
