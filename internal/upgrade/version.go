package upgrade

import (
	"strconv"
	"strings"
)

// IsDev reports whether v is a development build identifier. seek's
// banner.formatVersion collapses pseudo-versions and (devel) to "dev",
// so callers comparing against the embedded version use this to decide
// "should we upgrade unconditionally?".
//
// Accepts both the raw module version ("v0.9.0", "dev", "(devel)") and
// the formatted banner string ("dev · abc1234+", "v0.9.0 · abc1234").
// Only the first whitespace-separated token is examined.
func IsDev(v string) bool {
	v = coreVersion(v)
	if v == "" || v == "dev" || v == "unknown" || v == "(devel)" {
		return true
	}
	return strings.HasPrefix(v, "v0.0.0-")
}

// coreVersion strips the " · <rev>[+]" suffix that
// tui.formatVersion appends, leaving just the version token.
func coreVersion(v string) string {
	v = strings.TrimSpace(v)
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case ' ', '\t', '·':
			return strings.TrimSpace(v[:i])
		}
		// Handle the middle-dot rune (UTF-8: 0xC2 0xB7).
		if i+1 < len(v) && v[i] == 0xC2 && v[i+1] == 0xB7 {
			return strings.TrimSpace(v[:i])
		}
	}
	return v
}

// UpToDate reports whether current is at or past tag. Accepts both raw
// tags and tui.formatVersion banner strings ("v0.9.0 · abc1234").
func UpToDate(current, tag string) bool {
	if tag == "" {
		return true
	}
	if IsDev(current) {
		return false
	}
	return compareSemver(current, tag) >= 0
}

// compareSemver returns -1, 0, or +1 for a < b, a == b, a > b.
// Accepts both "v0.9.0" and "0.9.0". Pre-release suffix (e.g. "-rc.1")
// makes a version strictly LESS than the same version without it. This
// is a deliberately small subset of semver — enough to decide
// "should we upgrade?" without pulling in golang.org/x/mod.
func compareSemver(a, b string) int {
	if IsDev(a) && !IsDev(b) {
		return -1 // any release is newer than a dev build
	}
	if !IsDev(a) && IsDev(b) {
		return +1
	}
	if IsDev(a) && IsDev(b) {
		return 0
	}

	aCore, aPre := splitSemver(a)
	bCore, bPre := splitSemver(b)

	for i := range 3 {
		var ai, bi int
		if i < len(aCore) {
			ai = aCore[i]
		}
		if i < len(bCore) {
			bi = bCore[i]
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return +1
		}
	}

	// Cores equal — pre-release is older than its release counterpart.
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "" && bPre != "":
		return +1
	case aPre != "" && bPre == "":
		return -1
	default:
		return strings.Compare(aPre, bPre)
	}
}

// splitSemver pulls "v1.2.3-rc.1+build" into ([1,2,3], "rc.1"). Build
// metadata is dropped — it doesn't affect ordering by semver §10. A
// malformed segment parses as 0; the function is best-effort and the
// caller treats "couldn't decide" as "treat them as equal".
func splitSemver(v string) ([]int, string) {
	v = strings.TrimPrefix(coreVersion(v), "v")
	if plus := strings.IndexByte(v, '+'); plus >= 0 {
		v = v[:plus]
	}
	var pre string
	if dash := strings.IndexByte(v, '-'); dash >= 0 {
		pre = v[dash+1:]
	}
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		nums = append(nums, n)
	}
	return nums, pre
}
