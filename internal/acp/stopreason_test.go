package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
)

// stopreason_test.go pins the termination vocabulary (c_vfgeam): the
// response enum stays inside the ACP spec's documented values, the
// frame enum adds the two seek-only states, and the classifiers map
// every finish/error shape the agent can produce onto exactly one of
// them.

func TestResponseStopReason(t *testing.T) {
	cases := []struct {
		finish, want string
	}{
		// the length family across lanes: "length" (DeepSeek /
		// OpenAI-compatible / opencode; Gemini normalises MAX_TOKENS to
		// it) and "max_tokens" (Anthropic passes stop_reason through)
		{"length", StopMaxTokens},
		{"max_tokens", StopMaxTokens},
		// refusals
		{"refusal", StopRefusal},
		{"content_filter", StopRefusal},
		// everything else is an end_turn: DeepSeek "stop", the
		// empty-retry placeholder's forced "stop", a stream that ended
		// without a finish_reason at all, Anthropic's "end_turn" /
		// "stop_sequence"
		{"stop", StopEndTurn},
		{"", StopEndTurn},
		{"end_turn", StopEndTurn},
		{"stop_sequence", StopEndTurn},
	}
	for _, c := range cases {
		if got := ResponseStopReason(c.finish); got != c.want {
			t.Errorf("ResponseStopReason(%q) = %q, want %q", c.finish, got, c.want)
		}
		if got := FrameStopFromFinish(c.finish); got != c.want {
			t.Errorf("FrameStopFromFinish(%q) = %q, want %q (frame mirrors response on non-error exits)", c.finish, got, c.want)
		}
	}
}

// timeoutError satisfies net.Error with Timeout()=true — the shape the
// HTTP client surfaces for a stalled connection.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestFrameStopFromError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"client cancel", context.Canceled, FrameStopCancelled},
		{"wrapped cancel", fmt.Errorf("turn: %w", context.Canceled), FrameStopCancelled},
		// the silent-stall shape: every provider http.Client carries a
		// 5-minute Timeout, so a hung SSE stream lands here as a
		// deadline error (url.Error wraps context.DeadlineExceeded)
		{"deadline", context.DeadlineExceeded, FrameStopSilentTimeout},
		{"wrapped deadline", fmt.Errorf("Post \"https://api/x\": %w", context.DeadlineExceeded), FrameStopSilentTimeout},
		{"net timeout", &net.OpError{Err: timeoutError{}}, FrameStopSilentTimeout},
		// everything else — API errors, decode failures, the
		// tool_calls invariant, recovered panics — is internal
		{"api error", errors.New("429 rate limited"), FrameStopInternalError},
		{"empty", nil, FrameStopInternalError},
	}
	for _, c := range cases {
		if got := FrameStopFromError(c.err); got != c.want {
			t.Errorf("%s: FrameStopFromError(%v) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
}

// TestTurnStopUpdateWireShape: the frame must serialise to the exact
// client-consumable form — the sessionUpdate discriminant FIRST (every
// client routes updates by that tag), then the flat payload, omitempty
// on the optional fields (fleet tooling greps these lines raw).
func TestTurnStopUpdateWireShape(t *testing.T) {
	b, err := json.Marshal(NewTurnStop(FrameStopMaxTokens, "agent: response truncated (finish_reason=length, max_tokens=8192)", "length"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"sessionUpdate":"_seek_turn_stop","stopReason":"max_tokens","detail":"agent: response truncated (finish_reason=length, max_tokens=8192)","finishReason":"length"}`
	if string(b) != want {
		t.Fatalf("wire = %s, want %s", b, want)
	}
	b, err = json.Marshal(NewTurnStop(FrameStopCancelled, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"sessionUpdate":"_seek_turn_stop","stopReason":"cancelled"}` {
		t.Fatalf("minimal wire = %s, want detail/finishReason omitted", b)
	}
	if TurnStopSessionUpdate != "_seek_turn_stop" {
		t.Fatalf("tag = %q — the v2 schema reserves the underscore prefix for implementation-specific updates", TurnStopSessionUpdate)
	}
}
