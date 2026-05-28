//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package routines

import "os"

// tryFlockNB on platforms without a usable advisory-flock
// primitive (aix, solaris, plan9, js, …) returns ok=true
// always. The single-process sync.Mutex inside Store still
// holds; what's lost is cross-PROCESS lock semantics. Two
// concurrent `seek cron tick` invocations on these platforms
// could race the same job.
//
// Acceptable trade-off for the long-tail of unsupported
// platforms: seek itself is not validated on them, and the
// realistic risk (two cron ticks on aix) is extreme tail.
// Users who need hard cross-process tick semantics on these
// platforms can revisit when seek's official support widens.
func tryFlockNB(_ *os.File) (bool, error) { return true, nil }

func flockUnlock(_ *os.File) error { return nil }
