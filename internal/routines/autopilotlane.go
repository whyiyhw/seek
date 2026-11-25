package routines

// The cron ↔ autopilot-lane environment contract (v8 §9.5③1, card
// c_kdblc4).
//
// `seek autopilot run` is the unattended lane: a fleet of worktree
// subagents runs with nobody watching. Two things must therefore be
// decided by the SPAWNER and enforced by the CHILD, and both have to
// travel by env — flag.Parse stops at the "autopilot" positional, so no
// command-line flag can reach that lane (the same shape as
// SEEK_ACP_PROVIDER; docs/pitfalls/typed-agents-orchestration/
// autopilot-lane-never-elevates-preference…).
//
//  1. **The run came from a schedule.** Only internal/routines knows
//     that, so DefaultSubprocess stamps EnvAutopilotCron=1 on the child.
//     The child uses the marker to tell a cron-fired run apart from a
//     human typing `seek autopilot run` — the billing gate and the
//     subscription-lane default apply to the former only.
//
//  2. **Which lane bills the run.** EnvAutopilotProvider /
//     EnvAutopilotModel pin the lane (the pin wins over buildProvider's
//     auto-detect, which would otherwise pick up any DEEPSEEK_API_KEY
//     lying in the scheduler's env). Empty = the child's own default:
//     the OpenCode Go subscription lane.
//
//     EnvAutopilotAllowMetered is the escape hatch the standing rule
//     requires (docs/prd/v8.md §9.1, #9336): an unattended run must not
//     burn a metered key unless someone said so in writing. The value
//     names the approver (e.g. "approved-by:c_kdblc4"); the child
//     refuses a metered lane when it is empty and logs the approver when
//     it is set.
//
// All four are read by cmd/seek's autopilot lane resolver; the parser and
// the gate live there (one authority next to buildProvider), the spawn
// side only stamps the marker.
const (
	// EnvAutopilotCron marks a cron-fired `seek autopilot run` child.
	EnvAutopilotCron = "SEEK_AUTOPILOT_CRON"
	// EnvAutopilotProvider pins the lane (default: opencode subscription).
	EnvAutopilotProvider = "SEEK_AUTOPILOT_PROVIDER"
	// EnvAutopilotModel pins the model id (default: the lane's default).
	EnvAutopilotModel = "SEEK_AUTOPILOT_MODEL"
	// EnvAutopilotAllowMetered must name the approver before a
	// cron-fired autopilot run may use a metered (pay-per-token) lane.
	EnvAutopilotAllowMetered = "SEEK_AUTOPILOT_ALLOW_METERED"
)
