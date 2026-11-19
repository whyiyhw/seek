// Headless-lane configuration for `seek acp`. The ACP entry point
// short-circuits on os.Args[1]=="acp", which flag.Parse treats as a
// positional — parsing stops there, so NOT A SINGLE command-line flag
// is consumed on this lane (--yolo can never reach it; docs/prd/
// feature-paseo-provider.md §3.3 D-2). Behaviour knobs therefore
// arrive as env vars, parsed here so the resolution is testable
// without spawning a process. Transport-level env (SEEK_ACP_LOG) is
// read in cmd/seek's runACP; this file owns the permission posture
// and the provider/model startup pins.
package acp

import (
	"strings"
)

// YoloEnvVar is the opt-in switch that elevates the headless ACP lane
// from PrefDeny to PrefYolo. Default (unset / non-truthy) keeps Deny —
// interactive clients (Zed) stay safe until ACP
// session/request_permission lands (feature-acp.md backlog ①). Truthy
// set: 1 / true / yes / on (case-insensitive, whitespace-trimmed) —
// same set as cmd/seek's envBoolTrue.
//
// ⚠️ Trusted local headless ONLY (paseo/drive unattended lanes, where
// there is no human to answer y/N). Yolo permits every action the
// permission gate guards — bash included — under the process's own
// user; the env is inherited verbatim by anything that spawns `seek
// acp`, so never set it in untrusted or remotely reachable contexts.
const YoloEnvVar = "SEEK_ACP_YOLO"

// YoloFromEnv reports whether YoloEnvVar asks the ACP lane to run with
// PrefYolo. getenv is injected (os.Getenv in cmd/seek, a stub in tests)
// so the parsing is deterministic under test.
func YoloFromEnv(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(getenv(YoloEnvVar)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Startup pins for the lane's provider/model (feature-paseo-provider.md
// §3.4). With configOptions + session/set_config_option live, clients
// can switch models over the protocol at runtime — but the STARTUP lane
// still needs a channel flags can't reach (flag.Parse never gets past
// the "acp" positional), and embedding hosts (paseo provider entries)
// pin a default per entry exactly the way they pin ZCODE_PROVIDER on
// the zcode bridge. Both are plain names, no truthiness parsing:
// whitespace-only counts as unset. Scoped to the ACP lane at the call
// site in cmd/seek (gated on acpLane) so a stray exported var can't
// silently redirect the TUI — same posture as SEEK_ACP_YOLO above.
const (
	ProviderEnvVar = "SEEK_ACP_PROVIDER"
	ModelEnvVar    = "SEEK_ACP_MODEL"
)

// ProviderModelFromEnv resolves the startup provider/model pins.
// getenv is injected (os.Getenv in cmd/seek, a stub in tests) so the
// parsing is deterministic under test. Empty strings mean "not pinned —
// fall through to buildProvider's auto-detect".
func ProviderModelFromEnv(getenv func(string) string) (provider, model string) {
	if getenv == nil {
		return "", ""
	}
	provider = strings.TrimSpace(getenv(ProviderEnvVar))
	model = strings.TrimSpace(getenv(ModelEnvVar))
	return provider, model
}
