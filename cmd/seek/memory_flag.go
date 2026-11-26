package main

import (
	"fmt"
	"strings"
)

// memoryMode is the --memory axis (judge-engine PRD §2.2 缺口②, card
// c_4rp2py): how much of the M/L memory chain this session may touch.
//
// It is deliberately ORTHOGONAL to --no-save. --no-save owns the
// transcript only ("do not persist this session"); memory is its own
// axis, so `--no-save --memory=ro` (the judge lane: no junk sessions,
// but read the taste) is expressible.
type memoryMode int

const (
	// memoryOff: no M, no L, no memory tools, no memory hooks. Nothing
	// about memory reaches the prompt or the registry.
	memoryOff memoryMode = iota
	// memoryReadOnly: L stable + M index are injected and memory_recall
	// is available, but no byte is ever written back (no
	// remember/archive/amend, no recall-count touch, no GC flips, no
	// auto-dream, no observer writes).
	memoryReadOnly
	// memoryReadWrite: the historical behaviour, and the default.
	memoryReadWrite
)

// String renders the canonical flag token (used in error messages).
func (m memoryMode) String() string {
	switch m {
	case memoryOff:
		return "off"
	case memoryReadOnly:
		return "ro"
	default:
		return "rw"
	}
}

// parseMemoryFlag validates --memory. An absent or explicitly empty
// value means the DEFAULT (rw) — the same "empty == absent" contract
// `-tools ""` and `--effort ""` already keep, so a shell interpolating
// a possibly-unset variable cannot silently strip the user's memory.
func parseMemoryFlag(v string) (memoryMode, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "rw":
		return memoryReadWrite, nil
	case "ro":
		return memoryReadOnly, nil
	case "off":
		return memoryOff, nil
	}
	return memoryReadWrite, fmt.Errorf("--memory: unknown mode %q — try off|ro|rw", v)
}

// resolveMemoryMode folds the two memory-related flags into one
// decision + the effective isolation posture.
//
// Rules (all additive — no existing default moves):
//   - absent --memory + absent --isolated → rw (the status quo), not isolated.
//   - --memory=X alone             → X, not isolated.
//   - --isolated alone             → off + isolated.
//   - --isolated --memory=off      → off + isolated (redundant, accepted).
//   - --isolated --memory=ro|rw    → exit 2: --isolated's whole point is
//     that no environment state bleeds into the prompt, so it cannot be
//     combined with a mode that loads memory.
//
// memoryGiven reports whether the user typed the flag (an explicit
// empty value counts as absent, matching parseMemoryFlag).
func resolveMemoryMode(raw string, memoryGiven, isolated bool) (memoryMode, error) {
	mode, err := parseMemoryFlag(raw)
	if err != nil {
		return mode, err
	}
	// An explicitly empty value is "absent" — the same contract
	// `-tools ""` keeps, so a shell interpolating an unset variable
	// can't turn --isolated into a conflict.
	if strings.TrimSpace(raw) == "" {
		memoryGiven = false
	}
	if !isolated {
		return mode, nil
	}
	if memoryGiven && mode != memoryOff {
		return mode, fmt.Errorf("--isolated conflicts with --memory=%s: --isolated implies --memory=off", mode)
	}
	return memoryOff, nil
}
