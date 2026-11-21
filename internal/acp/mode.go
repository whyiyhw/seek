package acp

// Session modes — the ACP schema's native channel for "what ceremony is
// this session in" (agentclientprotocol.com/protocol/session-modes).
// seek maps its plan-mode workflow onto it (ACP·P2, backlog ④):
// mode "plan" == WorkflowPlanAnalyze, mode "default" == WorkflowNone.
//
// Wire shape (per the schema, not invented):
//
//   - session/new / session/load results carry `modes:
//     SessionModeState | null` — the CAPABILITY half. A null/absent
//     field means "no mode support"; a non-null one advertises the
//     available modes and names the current one.
//   - `session/set_mode {sessionId, modeId}` is the SWITCH half;
//     modeId MUST be one of the advertised availableModes.
//   - After a mode change the agent emits a session/update with
//     update type `current_mode_update` carrying the new
//     currentModeId, so clients never have to infer state.
//
// The schema notes dedicated session-mode methods may eventually be
// deprecated in favour of session config options; until that
// materialises set_mode is the spec'd path and what seek implements.

// Mode ids seek serves. Wire vocabulary shared by the transport (tests
// assert on these literals) and the cmd/seek backend (which owns the
// mapping to permission workflows).
const (
	// ModeDefault is normal operation — no workflow ceremony.
	ModeDefault = "default"
	// ModePlan is seek's plan mode: read-only analysis until a plan is
	// approved (WorkflowPlanAnalyze).
	ModePlan = "plan"
)

// SessionMode is one entry of SessionModeState.AvailableModes.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState is the `modes` field on session/new and session/load
// results (schema: SessionModeState). Its presence IS the capability
// advertisement — there is no agentCapabilities field for modes.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// SetModeParams is session/set_mode's request (schema:
// SetSessionModeRequest). ModeID must be one of the ids advertised in
// SessionModeState.AvailableModes; the cmd/seek backend enforces that.
type SetModeParams struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// CurrentModeUpdate is the session/update variant sent after the current
// mode changes (schema: CurrentModeUpdate) — including as confirmation
// of a honoured session/set_mode, whose response itself carries no mode
// information (only _meta).
type CurrentModeUpdate struct {
	SessionUpdate string `json:"sessionUpdate"` // always "current_mode_update"
	CurrentModeID string `json:"currentModeId"`
}

// ModeSwitcher is the OPTIONAL backend half of session/set_mode
// (AGENTS.md: extend by sibling interface, not by widening Backend —
// every existing fake keeps compiling; a backend without it gets the
// standard -32601, the same answer an agent that never heard of modes
// would give). Like SetConfigOption, it is only invoked between turns;
// implementations may flip shared policy state without locks.
type ModeSwitcher interface {
	SetMode(p SetModeParams) error
}
