package memory

import (
	"fmt"
	"strings"
	"time"
)

// promoteSourceThreshold is the minimum number of distinct project sources
// required before a Pending candidate auto-promotes to Stable (M5.10).
const promoteSourceThreshold = 3

// promoteMinAge is the minimum age of a Pending candidate's first observation
// before it's eligible for auto-promotion. Prevents a single lucky dream pass
// from promoting a candidate before time has validated it.
const promoteMinAge = 14 * 24 * time.Hour

// pendingExpiry is the age of last evidence above which a Pending candidate
// is dropped (M5.10). A candidate with no new evidence in this long is either
// a false positive or something the user doesn't care about.
const pendingExpiry = 30 * 24 * time.Hour

// EvaluatePending inspects the rendered Pending markdown section and returns
// the set of candidates to promote (move to Stable), keep (stay in Pending),
// and drop (delete). Decision rules:
//
//   - 来源项目 ≥ promoteSourceThreshold AND 首次观察 ≥ promoteMinAge → promote
//   - 最近确认 > pendingExpiry → drop
//   - everything else → keep
func EvaluatePending(pendingMarkdown string, now time.Time) (promoted, kept []LCandidate) {
	candidates := parseLMarkdown(pendingMarkdown)
	for _, c := range candidates {
		// Skip candidates with no timestamp — these are freshly merged
		// from this dream pass and haven't aged yet.
		if c.FirstSeen.IsZero() {
			kept = append(kept, c)
			continue
		}

		// Check deletion first: stale trumps all other rules.
		if !c.LastSeen.IsZero() && now.Sub(c.LastSeen) > pendingExpiry {
			// drop — don't add to any slice
			continue
		}

		distinctSrcs := distinctSourceCount(c.Sources)
		age := now.Sub(c.FirstSeen)

		if distinctSrcs >= promoteSourceThreshold && age >= promoteMinAge {
			promoted = append(promoted, c)
			continue
		}
		kept = append(kept, c)
	}
	return promoted, kept
}

// distinctSourceCount counts non-session, unique project sources.
func distinctSourceCount(sources []string) int {
	seen := map[string]struct{}{}
	for _, s := range sources {
		s = strings.TrimSpace(s)
		if s == "" || strings.HasPrefix(s, "session:") {
			continue
		}
		seen[s] = struct{}{}
	}
	return len(seen)
}

// ApplyMaintenance takes the promoted candidates and writes them to the
// Stable section of the Soul, while replacing the Pending section with the
// kept candidates. Previous Stable content is preserved — promoted candidates
// are APPENDED to existing Stable traits, not replacing them.
//
// The updated soul is saved to disk. Returns an error only if the save fails.
func (s *Soul) ApplyMaintenance(promoted, kept []LCandidate) error {
	newStable := s.Stable
	if len(promoted) > 0 {
		promotedText := FormatLCandidatesMarkdown(promoted)
		if strings.TrimSpace(newStable) != "" {
			newStable = strings.TrimRight(newStable, "\n") + "\n" + promotedText
		} else {
			newStable = promotedText
		}
	}

	newPending := FormatLCandidatesMarkdown(kept)
	s.SetSections(newStable, newPending)
	if err := s.Save(); err != nil {
		return fmt.Errorf("applyMaintenance: %w", err)
	}
	return nil
}
