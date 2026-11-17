package friction

// Table-driven acceptance for Fold (card c_2i6sm6): the five required
// input classes — empty session / text-only turns / orphan tool_calls /
// malformed arguments JSON / identical call thrice — plus the
// attribution and equality-key edges the criteria pin: Denied ⊆ Errors
// with both prefixes built from the producers' constants (never a
// re-typed literal), key-order-insensitive repeat equality, and the
// unknown-ToolCallID tool message skipped.
//
// Everything is in-memory []deepseek.Message: no filesystem, no
// ~/.seek/sessions anywhere near this file.

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/whyiyhw/seek/internal/permission"
	"github.com/whyiyhw/seek/pkg/agent"
	"github.com/whyiyhw/seek/pkg/deepseek"
)

// toolErr / deniedErr build tool-message content the way the runtime
// does — prefix constant + the error's own text — so the tests assert
// against the wire format, not a copy of it.
func toolErr(err error) string {
	return agent.ToolErrorPrefix + err.Error()
}

func deniedErr(kind string) string {
	return toolErr(fmt.Errorf("%w: user declined %s", permission.ErrDenied, kind))
}

// call is an assistant tool_call; result is its tool-role answer.
func call(id, name, args string) deepseek.Message {
	return deepseek.Message{
		Role: deepseek.RoleAssistant,
		ToolCalls: []deepseek.ToolCall{{
			ID: id, Type: "function",
			Function: deepseek.ToolCallFunc{Name: name, Arguments: args},
		}},
	}
}

func result(id, content string) deepseek.Message {
	return deepseek.Message{Role: deepseek.RoleTool, ToolCallID: id, Content: content}
}

func TestFold(t *testing.T) {
	for _, tc := range []struct {
		name string
		msgs []deepseek.Message
		want Summary
	}{
		{
			name: "empty session",
			msgs: nil,
			want: Summary{Tools: map[string]ToolStat{}},
		},
		{
			name: "text-only turns: user and assistant prose count turns, no tool rows",
			msgs: []deepseek.Message{
				{Role: deepseek.RoleUser, Content: "hi"},
				{Role: deepseek.RoleAssistant, Content: "hello"},
				{Role: deepseek.RoleUser, Content: "bye"},
				{Role: deepseek.RoleAssistant, Content: "ok"},
			},
			want: Summary{
				Turns: 2,
				Tools: map[string]ToolStat{},
			},
		},
		{
			// An interrupted run's tail: the call was issued (Counts,
			// and would score Repeats) but no result ever answered it,
			// so no Errors/Denied can be attributed.
			name: "orphan tool_calls count as calls with no error contribution",
			msgs: []deepseek.Message{
				{Role: deepseek.RoleUser, Content: "go"},
				call("c1", "bash", `{"command":"go build"}`),
				{Role: deepseek.RoleAssistant, Content: "interrupted"},
			},
			want: Summary{
				Turns:     2,
				ToolCalls: 1,
				Tools: map[string]ToolStat{
					"bash": {Calls: 1},
				},
			},
		},
		{
			// CanonicalArgs falls back to raw bytes for unparseable
			// arguments: still counted, and the raw form is the equality
			// key — identical malformed repeats, differing ones don't.
			name: "malformed arguments JSON still counts and repeats on raw bytes",
			msgs: []deepseek.Message{
				call("c1", "bash", `{not json`),
				result("c1", "ok"),
				call("c2", "bash", `{not json`), // identical malformed → repeat
				result("c2", "ok"),
				call("c3", "bash", `{also not`), // different malformed → not
				result("c3", "ok"),
			},
			want: Summary{
				Turns:     3,
				ToolCalls: 3,
				Tools: map[string]ToolStat{
					"bash": {Calls: 3, Repeats: 1},
				},
			},
		},
		{
			name: "identical call thrice scores two repeats",
			msgs: []deepseek.Message{
				call("c1", "read", `{"path":"main.go"}`),
				result("c1", "src"),
				call("c2", "read", `{"path":"main.go"}`),
				result("c2", "src"),
				call("c3", "read", `{"path":"main.go"}`),
				result("c3", "src"),
			},
			want: Summary{
				Turns:     3,
				ToolCalls: 3,
				Tools: map[string]ToolStat{
					"read": {Calls: 3, Repeats: 2},
				},
			},
		},
		{
			// The full mix: error + denial + healthy repeats in one
			// session, plus a same-name-different-args call that is NOT
			// a repeat (the repeatguard criterion: whole-args equality).
			name: "errors, denials and repeats attribute through tool_call ids",
			msgs: []deepseek.Message{
				{Role: deepseek.RoleUser, Content: "do things"},
				{
					Role: deepseek.RoleAssistant,
					ToolCalls: []deepseek.ToolCall{
						{ID: "c1", Type: "function", Function: deepseek.ToolCallFunc{Name: "bash", Arguments: `{"command":"go test"}`}},
						{ID: "c2", Type: "function", Function: deepseek.ToolCallFunc{Name: "write", Arguments: `{"path":"a.go"}`}},
					},
				},
				result("c1", toolErr(errors.New("exit status 1"))),
				result("c2", deniedErr("write")),
				call("c3", "bash", `{"command":"go test"}`), // repeat of c1
				result("c3", toolErr(errors.New("exit status 1"))),
				call("c4", "bash", `{"command":"go vet"}`), // same tool, different args
				result("c4", "ok"),
				{Role: deepseek.RoleAssistant, Content: "done"},
			},
			want: Summary{
				Turns:     4,
				ToolCalls: 4,
				Tools: map[string]ToolStat{
					"bash":  {Calls: 3, Errors: 2, Repeats: 1},
					"write": {Calls: 1, Errors: 1, Denied: 1},
				},
			},
		},
		{
			// RepeatGuard's equality key canonicalises key order and
			// whitespace; the fold must agree or live sessions (whose
			// arguments the model re-serialises slightly differently)
			// would undercount.
			name: "repeat equality is key-order and whitespace insensitive",
			msgs: []deepseek.Message{
				call("c1", "read", `{"path":"x.go","offset":10}`),
				result("c1", "src"),
				call("c2", "read", `{"offset":10, "path":"x.go"}`),
				result("c2", "src"),
			},
			want: Summary{
				Turns:     2,
				ToolCalls: 2,
				Tools: map[string]ToolStat{
					"read": {Calls: 2, Repeats: 1},
				},
			},
		},
		{
			// A tool message whose call never appears in this sequence
			// (e.g. folding a slice that starts mid-session) has no tool
			// name to attribute to — skipped, not invented.
			name: "tool message with unknown tool_call id is skipped",
			msgs: []deepseek.Message{
				result("cX", toolErr(errors.New("boom"))),
				{Role: deepseek.RoleAssistant, Content: "hi"},
			},
			want: Summary{
				Turns: 1,
				Tools: map[string]ToolStat{},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Fold(tc.msgs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Fold = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Denied ⊆ Errors is the documented invariant: a denial lands on the
// transcript wrapped in the tool-error prefix, so it must score both
// counters — and a non-denial error string that merely CONTAINS the
// denial text later on must not score Denied (prefix match, not
// substring match).
func TestDeniedIsSubsetOfErrors(t *testing.T) {
	msgs := []deepseek.Message{
		call("c1", "bash", `{"command":"ls"}`),
		result("c1", deniedErr("bash")),
		call("c2", "bash", `{"command":"ls -l"}`),
		result("c2", toolErr(fmt.Errorf("sh: permission denied by filesystem: %w", errors.New("eacces")))),
	}
	got := Fold(msgs)
	row := got.Tools["bash"]
	if row.Calls != 2 || row.Errors != 2 || row.Denied != 1 {
		t.Errorf("bash row = %+v, want Calls 2 / Errors 2 / Denied 1 (denial ⊆ errors; late substring ≠ prefix)", row)
	}
}

// Fold must not mutate its input — callers hand it live session state.
func TestFoldDoesNotMutateInput(t *testing.T) {
	msgs := []deepseek.Message{
		call("c1", "read", `{"path":"x"}`),
		result("c1", "ok"),
	}
	orig := fmt.Sprintf("%+v", msgs)
	_ = Fold(msgs)
	if after := fmt.Sprintf("%+v", msgs); after != orig {
		t.Errorf("Fold mutated its input:\n got %s\nwant %s", after, orig)
	}
}
