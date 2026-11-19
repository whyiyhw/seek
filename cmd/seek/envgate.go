package main

import (
	"os"
	"strings"
)

// autoDistillEnabled returns true when $SEEK_AUTO_DISTILL is unset or set to
// a truthy value (1/true/yes/on). Controls memory_observe tool registration;
// default enabled because real-time notifications provide the safety net
// (PRD §6 v2).
func autoDistillEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SEEK_AUTO_DISTILL")))
	if v == "" {
		return true // default: enabled
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// envBoolTrue returns true when the named env var is set to a truthy
// value (1/true/yes/on). Empty/unset is false. Use for kill-switch /
// opt-in flags where "absent = disabled" is the safer default.
func envBoolTrue(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
