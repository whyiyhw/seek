// stopreason.go owns the wire vocabulary for turn termination (card
// c_vfgeam, ACP·P0-3 会话终止可观测). Two surfaces, deliberately
// different enums:
//
//   - the session/prompt RESPONSE stopReason — the ACP spec's own enum
//     (v1: end_turn / max_tokens / refusal / cancelled). seek used to
//     answer end_turn for everything except cancel, which hid
//     finish_reason=length truncation behind a generic JSON-RPC error;
//   - a structured termination session/update frame tagged
//     "_seek_turn_stop" — a seek extension carrying the finer-grained
//     classification plus a readable detail. The spec has NO
//     termination update variant (termination normally rides the
//     response alone), so this frame follows the v2 schema's reserved
//     underscore prefix for implementation-specific updates. Clients on
//     the v1 schema (current Zed) drop unknown tags with a log line and
//     nothing else — every line of ndJson is an independent message, so
//     one dropped notification cannot corrupt the stream; JS clients
//     (paseo) fall through their switch and also survive. The frame is
//     emitted exactly once per session/prompt, immediately before the
//     response (or error) frame, on EVERY exit path — which is what
//     makes "no frame and no response" a reliable client-side signal
//     that the PROCESS died mid-turn (the c_4ap2ra silent-death
//     failure mode; no in-process frame can report its own SIGKILL).
package acp

import (
	"context"
	"errors"
	"net"
)

// Response stopReason values — the ACP spec enum for the
// session/prompt result (v1 prompt-turn.md). Internal errors do NOT
// get a response stopReason at all: they ride the JSON-RPC error
// channel (-32000 + message), which is the spec's error surface.
const (
	StopEndTurn   = "end_turn"   // model finished responding
	StopMaxTokens = "max_tokens" // token limit cut the response (finish_reason=length family)
	StopRefusal   = "refusal"    // model declined (safety filter)
	StopCancelled = "cancelled"  // client cancelled the turn
)

// Frame stopReason values — the seek-side enum carried on the
// _seek_turn_stop termination frame. Superset of the response enum's
// reachable values plus the two states only the frame can name:
// silent_timeout (upstream went quiet and the HTTP deadline fired) and
// internal_error (anything else that killed the turn).
const (
	FrameStopEndTurn       = "end_turn"
	FrameStopMaxTokens     = "max_tokens"
	FrameStopRefusal       = "refusal"
	FrameStopSilentTimeout = "silent_timeout"
	FrameStopCancelled     = "cancelled"
	FrameStopInternalError = "internal_error"
)

// TurnStopSessionUpdate is the sessionUpdate tag of the termination
// frame. Underscore-prefixed per the v2 schema's reserved namespace for
// implementation-specific updates.
const TurnStopSessionUpdate = "_seek_turn_stop"

// TurnStopUpdate is the structured payload of the termination frame's
// update object — a tagged union member like every other sessionUpdate
// variant. StopReason is one of the FrameStop* values; Detail is the
// human-readable explanation (advisory notice text, error message, or
// a synthesised line); FinishReason is the raw upstream finish_reason
// when one was observed ("length", "max_tokens", "refusal", …) — the
// field fleet tooling (paseo/butler) matches on to tell the opencode
// finish_reason=length family apart from a clean end_turn without
// parsing prose.
type TurnStopUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	StopReason    string `json:"stopReason"`
	Detail        string `json:"detail,omitempty"`
	FinishReason  string `json:"finishReason,omitempty"`
}

// NewTurnStop builds a termination frame payload with the tag pinned —
// the only sanctioned constructor, so an Update can never reach the
// wire missing its "sessionUpdate" discriminant (clients route every
// update by that tag; an untagged object is dropped by every consumer).
func NewTurnStop(reason, detail, finish string) TurnStopUpdate {
	return TurnStopUpdate{
		SessionUpdate: TurnStopSessionUpdate,
		StopReason:    reason,
		Detail:        detail,
		FinishReason:  finish,
	}
}

// IsLengthFinish reports whether an upstream finish reason belongs to
// the token-limit family: "length" (DeepSeek / OpenAI-compatible /
// opencode lanes; Gemini normalises MAX_TOKENS to it) and "max_tokens"
// (Anthropic passes stop_reason through verbatim).
func IsLengthFinish(finish string) bool {
	return finish == "length" || finish == "max_tokens"
}

// isRefusalFinish reports whether a finish reason means the model
// declined (DeepSeek "refusal", OpenAI-compatible "content_filter",
// Anthropic "refusal").
func isRefusalFinish(finish string) bool {
	return finish == "refusal" || finish == "content_filter"
}

// ResponseStopReason maps the final turn's upstream finish reason to
// the spec's session/prompt response stopReason. Anything not a
// truncation or a refusal — "stop", "", Anthropic's "end_turn" /
// "stop_sequence", the empty-retry placeholder's forced "stop" — is an
// end_turn.
func ResponseStopReason(finish string) string {
	switch {
	case IsLengthFinish(finish):
		return StopMaxTokens
	case isRefusalFinish(finish):
		return StopRefusal
	default:
		return StopEndTurn
	}
}

// FrameStopFromFinish maps the final turn's finish reason to the
// termination frame's stopReason for turn exits that were neither an
// error nor a cancel.
func FrameStopFromFinish(finish string) string {
	switch {
	case IsLengthFinish(finish):
		return FrameStopMaxTokens
	case isRefusalFinish(finish):
		return FrameStopRefusal
	default:
		return FrameStopEndTurn
	}
}

// FrameStopFromError classifies a turn-killing error into the frame
// enum. context.Canceled → cancelled (client hit stop); deadline and
// net timeouts → silent_timeout — the shape an upstream stall actually
// surfaces as, since every provider http.Client carries a 5-minute
// Timeout, a hung SSE stream cannot outlive it; everything else →
// internal_error.
func FrameStopFromError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return FrameStopCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return FrameStopSilentTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return FrameStopSilentTimeout
	}
	return FrameStopInternalError
}
