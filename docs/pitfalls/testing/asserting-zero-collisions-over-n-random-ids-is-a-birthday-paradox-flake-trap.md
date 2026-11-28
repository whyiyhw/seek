### Asserting zero collisions over N random IDs is a birthday-paradox flake trap
- **Saw**: `TestNewSubSid_FormatAndUniqueness` failed intermittently with `duplicate sub-sid: 20260601-103412-7790c1` — 1000 sub-sids at a fixed timestamp, all asserted unique.
- **Why**: sub-sids use a 24-bit (6 hex) random suffix (`session.generateID` format contract); over 1000 draws P(collision) = `1000²/(2·2²⁴) ≈ 3%`, not the comment's "~6e-4" — ~1 in 34 runs. Production safe (fan-out ≪1000 IDs/s; second-resolution timestamps disambiguate); the test's assumption was the bug.
- **Fix**: assert RNG health — require ≥990/1000 distinct (≥11 collisions to trip, P≈1e-15; constant suffix fails immediately), plus the different-timestamp uniqueness check the comment claimed.
- **Lesson**: with a b-bit random ID "all unique" is unassertable for meaningful N (birthday bound `N²/2^(b+1)`): widen entropy if the format allows, or assert distinct ≥ N−k / not-degenerate — don't widen entropy for a test when fan-out is safe and the format is shared.
- **Refs**: `internal/subagent/types_test.go:TestNewSubSid_FormatAndUniqueness`, `internal/subagent/types.go:newSubSid`, `internal/session/session.go:generateID`
